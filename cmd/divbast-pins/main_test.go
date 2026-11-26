// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/divbast/calibre-oci-image/internal/pins"
)

const commit = "ffc619947b5ba4e6520e3a0bc70680024e184b2f"

// containerfile is the start of an app's Containerfile, as the tool reads
// it: the commit of the upstream it builds, the downloads whose checksums
// it fills in, and DHI's Python.
const containerfile = `ARG CALIBRE_VERSION=9.19.0
ARG CALIBRE_SHA256_X86_64=
ARG CALIBRE_SHA256_ARM64=
ARG CALIBRE_WEB_COMMIT=` + commit + `
ARG CALIBRE_WEB_SHA256=
ARG IMAGE_PAIR=
ARG GHOSTSCRIPT_VERSION=10.08.0
ARG GHOSTSCRIPT_TAG=10080
ARG GHOSTSCRIPT_SHA256=
ARG UNRAR_VERSION=7.3.2
ARG UNRAR_SHA256=
ARG RUNTIME_IMAGE=dhi.io/python:3.15.2-debian13@sha256:c6c9414d78bd285be1a4dacfb94ae61732c77019605b1678792823e319b720f6
FROM dhi.io/python:3.15.2-debian13-dev@sha256:c73e2cb9e8d56cfab7f01b99f67b2c830e84e7397dfe666d8bfaf5a69ad0ce85 AS deps
FROM ${RUNTIME_IMAGE}
`

const pinsTxt = `# upstream leaves it unpinned: cryptography>=3.0
cryptography==50.0.2

# builds cryptography and ujson from source
setuptools==82.0.1
`

// requirements is a requirements list of the kinds an app's holds: pins,
// a range, and a package for another platform.
const requirements = `six==1.17.0
ujson==6.0.0 # a C extension
cryptography>=3.0
pywin32==312; sys_platform == 'win32'
`

// otherCommit is another full commit, at which the test's GitHub serves
// more.txt too, so a file at it is refused for its commit, not for a 404.
const otherCommit = "0123456789abcdef0123456789abcdef01234567"

// moreRequirements is a second requirements file at the same commit: six at
// the first file's version again, and nothing else.
const moreRequirements = `six==1.17.0
`

// upstreamPath is where the test's GitHub serves the upstream's own
// requirements.txt, at the Containerfile's commit.
const upstreamPath = "/owner/app/" + commit + "/requirements.txt"

// downloads are the URLs the tool fetches to fill in a checksum, and the
// ARG each one fills, for the Containerfile above.
var downloads = map[string]string{
	"CALIBRE_SHA256_X86_64": "https://download.calibre-ebook.com/9.19.0/calibre-9.19.0-x86_64.txz",
	"CALIBRE_SHA256_ARM64":  "https://download.calibre-ebook.com/9.19.0/calibre-9.19.0-arm64.txz",
	"CALIBRE_WEB_SHA256":    "https://github.com/janeczku/calibre-web/archive/" + commit + ".tar.gz",
	"GHOSTSCRIPT_SHA256":    "https://github.com/ArtifexSoftware/ghostpdl-downloads/releases/download/gs10080/ghostscript-10.08.0.tar.xz",
	"UNRAR_SHA256":          "https://www.rarlab.com/rar/unrarsrc-7.3.2.tar.gz",
}

// sumOf is the sha256 of what the test's servers serve at u: the URL
// itself.
func sumOf(u string) string {
	h := sha256.Sum256([]byte(u))
	return hex.EncodeToString(h[:])
}

// filledContainerfile is the Containerfile above after a run: every
// checksum filled in, and IMAGE_PAIR written.
func filledContainerfile() string {
	out := strings.Replace(containerfile, "ARG IMAGE_PAIR=\n", "ARG IMAGE_PAIR=9.19.0-cw."+commit[:7]+"\n", 1)
	for arg, u := range downloads {
		out = strings.Replace(out, "ARG "+arg+"=\n", "ARG "+arg+"="+sumOf(u)+"\n", 1)
	}
	return out
}

// redirect sends every request for a host other than the test server's to
// the test server, at /dl/<host><path>, so the tool's fixed download URLs
// reach it.
type redirect struct {
	to   *url.URL
	next http.RoundTripper
}

func (r redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != r.to.Host {
		req = req.Clone(req.Context())
		req.URL.Path = "/dl/" + req.URL.Host + req.URL.Path
		req.URL.Scheme, req.URL.Host = r.to.Scheme, r.to.Host
		req.Host = r.to.Host
	}
	return r.next.RoundTrip(req)
}

// servers stands in for GitHub and PyPI, on one server that speaks TLS,
// with each release in yanked yanked on PyPI, for broken metadata. It
// returns the sources and the server's URL.
func servers(t *testing.T, yanked ...string) (src pins.Sources, base string) {
	t.Helper()
	release := func(files ...string) []map[string]any {
		out := make([]map[string]any, 0, len(files))
		for _, f := range files {
			kind := "bdist_wheel"
			if strings.HasSuffix(f, ".tar.gz") {
				kind = "sdist"
			}
			h := sha256.Sum256([]byte(f))
			pulled := slices.ContainsFunc(yanked, func(r string) bool { return strings.HasPrefix(f, r+"-") })
			out = append(out, map[string]any{
				"filename": f, "packagetype": kind, "url": "https://files.example/" + f,
				"digests": map[string]string{"sha256": hex.EncodeToString(h[:])},
				"yanked":  pulled, "yanked_reason": map[bool]any{true: "broken metadata", false: nil}[pulled],
			})
		}
		return out
	}
	pypi := map[string][]map[string]any{
		"/pypi/six/1.17.0/json":          release("six-1.17.0-py2.py3-none-any.whl", "six-1.17.0.tar.gz"),
		"/pypi/ujson/6.0.0/json":         release("ujson-6.0.0-cp315-cp315-manylinux_2_17_x86_64.manylinux2014_x86_64.whl", "ujson-6.0.0.tar.gz"),
		"/pypi/cryptography/50.0.2/json": release("cryptography-50.0.2-cp311-abi3-manylinux_2_34_x86_64.whl", "cryptography-50.0.2.tar.gz"),
		"/pypi/setuptools/82.0.1/json":   release("setuptools-82.0.1-py3-none-any.whl", "setuptools-82.0.1.tar.gz"),
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/dl/"):
			// A download: its body is its own URL
			for _, u := range downloads {
				if "https://"+strings.TrimPrefix(r.URL.Path, "/dl/") == u {
					_, _ = w.Write([]byte(u))
					return
				}
			}
			http.NotFound(w, r)
		case r.URL.Path == upstreamPath:
			_, _ = w.Write([]byte(requirements))
		case r.URL.Path == "/owner/app/"+commit+"/more.txt", r.URL.Path == "/owner/app/"+otherCommit+"/more.txt":
			_, _ = w.Write([]byte(moreRequirements))
		case r.URL.Path == "/owner/app/"+commit+"/conflict.txt":
			_, _ = w.Write([]byte("six==1.16.0\n"))
		case pypi[r.URL.Path] != nil:
			_ = json.NewEncoder(w).Encode(map[string]any{"urls": pypi[r.URL.Path]})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{Transport: redirect{to: u, next: srv.Client().Transport}}
	return pins.Sources{PyPI: &pins.PyPI{Base: srv.URL, Client: c}, Client: c}, srv.URL
}

// repo writes a repository holding the Containerfile, pins.txt, and
// requirements.in as in, or none when in is empty.
func repo(t *testing.T, in string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "python"), 0o750); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"Containerfile": containerfile, "python/pins.txt": pinsTxt}
	if in != "" {
		files["python/requirements.in"] = in
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name))) //nolint:gosec // G304: the test's own temporary file
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// runIn runs the command in dir, and returns its exit code, stdout and
// stderr.
func runIn(t *testing.T, src pins.Sources, dir string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errs bytes.Buffer
	code = run(t.Context(), append([]string{"-C", dir}, args...), src, &out, &errs)
	return code, out.String(), errs.String()
}

// checkLocks holds the three files generate wrote in dir to the
// requirements above, merged with pinsTxt.
func checkLocks(t *testing.T, dir string) {
	t.Helper()
	lock := read(t, dir, "python/requirements.lock")
	for _, want := range []string{"--no-binary cryptography,ujson\n", "six==1.17.0 \\\n", "cryptography==50.0.2 \\\n", "ujson==6.0.0 \\\n"} {
		if !strings.Contains(lock, want) {
			t.Errorf("requirements.lock =\n%s\nwant it to hold %q", lock, want)
		}
	}
	if strings.Contains(lock, "pywin32") || strings.Contains(lock, "setuptools") {
		t.Errorf("requirements.lock =\n%s\nwant no Windows package and no build backend", lock)
	}
	if b := read(t, dir, "python/build.lock"); !strings.Contains(b, "setuptools==82.0.1 \\\n") {
		t.Errorf("build.lock =\n%s\nwant setuptools 82.0.1", b)
	}
	// Every file either lock lets pip take, by its sha256 and URL, and no other
	files := read(t, dir, "python/files.txt")
	for _, f := range []string{"six-1.17.0-py2.py3-none-any.whl", "ujson-6.0.0.tar.gz", "cryptography-50.0.2.tar.gz", "setuptools-82.0.1-py3-none-any.whl"} {
		h := sha256.Sum256([]byte(f))
		if want := hex.EncodeToString(h[:]) + " https://files.example/" + f + "\n"; !strings.Contains(files, want) {
			t.Errorf("files.txt =\n%s\nwant it to hold %q", files, want)
		}
	}
	if n := strings.Count(files, " https://"); n != 4 {
		t.Errorf("files.txt names %d files, want the 4 the locks take", n)
	}
	if got, want := read(t, dir, "Containerfile"), filledContainerfile(); got != want {
		t.Errorf("Containerfile =\n%s\nwant every checksum filled in, and IMAGE_PAIR written:\n%s", got, want)
	}
}

// TestCheckMode is requirements.in holding the app's own requirements.
func TestCheckMode(t *testing.T) {
	t.Parallel()
	src, _ := servers(t)
	dir := repo(t, requirements)

	// Nothing generated yet: stale
	if code, out, _ := runIn(t, src, dir, "-check"); code != 1 || !strings.Contains(out, "+++ python/requirements.lock") {
		t.Errorf("-check before any lock = %d, %q; want 1 and a diff creating the lock", code, out)
	}
	if code, out, errs := runIn(t, src, dir); code != 0 || !strings.Contains(out, "wrote Containerfile") {
		t.Fatalf("generate = %d, %q; stderr: %s; want 0, and the Containerfile written", code, out, errs)
	}
	checkLocks(t, dir)
	if lock := read(t, dir, "python/requirements.lock"); !strings.Contains(lock, "from python/requirements.in\n# and python/pins.txt") {
		t.Errorf("requirements.lock =\n%s\nwant its header to name requirements.in", lock)
	}

	// Current: nothing to say
	if code, out, errs := runIn(t, src, dir, "-check"); code != 0 || out != "" {
		t.Errorf("-check when current = %d, %q (stderr %q); want 0 and nothing", code, out, errs)
	}

	// A lock edited by hand is stale, and -check writes nothing
	lock := read(t, dir, "python/requirements.lock")
	stale := strings.Replace(lock, "six==1.17.0", "six==1.16.0", 1)
	if err := os.WriteFile(filepath.Join(dir, "python", "requirements.lock"), []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runIn(t, src, dir, "-check")
	if code != 1 || !strings.Contains(out, "--- python/requirements.lock") || !strings.Contains(out, "-six==1.16.0 \\") || !strings.Contains(out, "+six==1.17.0 \\") {
		t.Errorf("-check of a stale lock = %d,\n%s\nwant 1 and a unified diff of the change", code, out)
	}
	if got := read(t, dir, "python/requirements.lock"); got != stale {
		t.Error("-check rewrote the lock")
	}
}

// TestIncludedRequirements is requirements.in naming the upstream's own
// requirements.txt, at the commit the Containerfile pins, by an ARG.
func TestIncludedRequirements(t *testing.T) {
	t.Parallel()
	src, base := servers(t)
	dir := repo(t, "# The upstream's own list, at the commit it is built from\n-r "+base+"/owner/app/${CALIBRE_WEB_COMMIT}/requirements.txt\n")
	if code, _, errs := runIn(t, src, dir); code != 0 {
		t.Fatalf("generate = %d; stderr: %s", code, errs)
	}
	checkLocks(t, dir)
	if lock := read(t, dir, "python/requirements.lock"); !strings.Contains(lock, base+upstreamPath) {
		t.Errorf("requirements.lock =\n%s\nwant its header to name %s", lock, base+upstreamPath)
	}
	if code, out, errs := runIn(t, src, dir, "-check"); code != 0 || out != "" {
		t.Errorf("-check when current = %d, %q (stderr %q); want 0 and nothing", code, out, errs)
	}
}

// TestIncludedRequirementsTwoFiles is requirements.in naming two files at
// the commit the Containerfile pins, as Calibre-Web's two are: their
// requirements are merged, a package both list at one specifier is kept
// once, and at two specifiers, or at another commit, is refused.
func TestIncludedRequirementsTwoFiles(t *testing.T) {
	t.Parallel()
	src, base := servers(t)
	line := func(file string) string { return "-r " + base + "/owner/app/${CALIBRE_WEB_COMMIT}/" + file + "\n" }

	dir := repo(t, line("requirements.txt")+line("more.txt"))
	if code, _, errs := runIn(t, src, dir); code != 0 {
		t.Fatalf("generate = %d; stderr: %s", code, errs)
	}
	// six is in both files at ==1.17.0, and the lock holds it once
	checkLocks(t, dir)
	lock := read(t, dir, "python/requirements.lock")
	if n := strings.Count(lock, "\nsix==1.17.0 "); n != 1 {
		t.Errorf("requirements.lock holds six %d times, want once:\n%s", n, lock)
	}
	for _, want := range []string{base + upstreamPath, base + "/owner/app/" + commit + "/more.txt"} {
		if !strings.Contains(lock, want) {
			t.Errorf("requirements.lock =\n%s\nwant its header to name %s", lock, want)
		}
	}
	if code, out, errs := runIn(t, src, dir, "-check"); code != 0 || out != "" {
		t.Errorf("-check when current = %d, %q (stderr %q); want 0 and nothing", code, out, errs)
	}

	for _, tt := range []struct{ name, in, want string }{
		{"another-commit", line("requirements.txt") + "-r " + base + "/owner/app/" + otherCommit + "/more.txt\n", "one commit"},
		{"conflicting-specifiers", line("requirements.txt") + line("conflict.txt"), "six"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := repo(t, tt.in)
			if code, _, errs := runIn(t, src, dir); code != 1 || !strings.Contains(errs, tt.want) || strings.Contains(errs, "404") {
				t.Errorf("generate = %d, stderr %q; want 1, naming %q, and no 404", code, errs, tt.want)
			}
			if _, err := os.Stat(filepath.Join(dir, "python", "requirements.lock")); err == nil {
				t.Error("a lock was written")
			}
		})
	}
}

// TestImagePair is IMAGE_PAIR, which the tool writes from CALIBRE_VERSION
// and the first seven characters of CALIBRE_WEB_COMMIT, and -check holds.
func TestImagePair(t *testing.T) {
	t.Parallel()
	src, _ := servers(t)
	dir := repo(t, requirements)
	if code, _, errs := runIn(t, src, dir); code != 0 {
		t.Fatalf("generate = %d; stderr: %s", code, errs)
	}
	want := "ARG IMAGE_PAIR=9.19.0-cw." + commit[:7] + "\n"
	if got := read(t, dir, "Containerfile"); !strings.Contains(got, want) {
		t.Errorf("Containerfile =\n%s\nwant it to hold %q", got, want)
	}
	// A stale value, as after Renovate moves CALIBRE_VERSION
	stale := strings.Replace(read(t, dir, "Containerfile"), want, "ARG IMAGE_PAIR=9.18.0-cw."+commit[:7]+"\n", 1)
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runIn(t, src, dir, "-check")
	if code != 1 || !strings.Contains(out, "-ARG IMAGE_PAIR=9.18.0-cw."+commit[:7]) || !strings.Contains(out, "+"+strings.TrimSuffix(want, "\n")) {
		t.Errorf("-check with a stale IMAGE_PAIR = %d,\n%s\nwant 1 and a diff to %q", code, out, want)
	}
}

// TestRequirementsRefused is each requirements.in the tool will not read:
// it fails, naming why, and writes nothing.
func TestRequirementsRefused(t *testing.T) {
	t.Parallel()
	src, base := servers(t)
	include := "-r " + base + "/owner/app/${CALIBRE_WEB_COMMIT}/requirements.txt\n"
	for _, tt := range []struct{ name, in, want string }{
		{"missing", "", "python/requirements.in"},
		{"http", "-r http://example.com/" + commit + "/requirements.txt\n", "https"},
		{"no-commit", "-r " + base + "/owner/app/main/requirements.txt\n", "commit"},
		{"undefined-arg", "-r " + base + "/owner/app/${APP_REF}/requirements.txt\n", "APP_REF"},
		{"mixed", include + "six==1.17.0\n", "-r lines"},
		{"not-served", "-r " + base + "/owner/app/${CALIBRE_WEB_COMMIT}/other.txt\n", "404"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := repo(t, tt.in)
			if code, _, errs := runIn(t, src, dir); code != 1 || !strings.Contains(errs, tt.want) {
				t.Errorf("generate = %d, stderr %q; want 1, naming %q", code, errs, tt.want)
			}
			if _, err := os.Stat(filepath.Join(dir, "python", "requirements.lock")); err == nil {
				t.Error("a lock was written")
			}
		})
	}
}

// TestYankedUpstreamPin is a release the requirements pin with == that
// PyPI has since yanked: pip still installs it, so the tool keeps it, says
// so in the lock and in a note, and exits 0. One pins.txt names is refused.
func TestYankedUpstreamPin(t *testing.T) {
	t.Parallel()
	src, _ := servers(t, "six")
	dir := repo(t, requirements)
	code, _, errs := runIn(t, src, dir)
	if code != 0 {
		t.Fatalf("run() = %d, stderr %q; want 0", code, errs)
	}
	if !strings.Contains(errs, "note: six 1.17.0") || !strings.Contains(errs, "yanked") || !strings.Contains(errs, "broken metadata") {
		t.Errorf("stderr = %q; want a note naming six 1.17.0, yanked, and PyPI's reason", errs)
	}
	if lock := read(t, dir, "python/requirements.lock"); !strings.Contains(lock, "# pinned by upstream; yanked on PyPI: broken metadata\nsix==1.17.0 \\\n") {
		t.Errorf("requirements.lock =\n%s\nwant six's comment to say it is yanked, and why", lock)
	}

	src, _ = servers(t, "cryptography")
	if code, _, errs = runIn(t, src, repo(t, requirements)); code != 1 || !strings.Contains(errs, "cryptography 50.0.2 is yanked") {
		t.Errorf("run() with pins.txt's cryptography yanked = %d, stderr %q; want 1, naming it", code, errs)
	}
}

func TestUsage(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), []string{"extra"}, pins.Sources{}, &stdout, &stderr); code != 2 {
		t.Errorf("run(extra) = %d, want 2", code)
	}
}
