// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/divbast/calibre-oci-image/internal/reload"
	"github.com/divbast/calibre-oci-image/internal/supervise"
)

// noApps is an apps function that must not run.
func noApps(t *testing.T) func(context.Context, *settings) int {
	t.Helper()
	return func(context.Context, *settings) int {
		t.Error("the apps were started")
		return 0
	}
}

// envNames is the names in an environment, never the values.
func envNames(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		name, _, _ := strings.Cut(e, "=")
		out = append(out, name)
	}
	return out
}

// TestServeStartsApps is a first start that works: the apps start after
// it, with TRUSTED_PROXIES read, and the exit code is theirs.
func TestServeStartsApps(t *testing.T) {
	t.Parallel()
	l := newLayout(t)
	h := &helpers{t: t}
	environ := []string{"PATH=/usr/bin", "TRUSTED_PROXIES=10.0.0.0/8, 192.0.2.1", "CALIBRE_ADMIN_PASSWORD=" + adminPW, "CALIBRE_WEB_ADMIN_PASSWORD=" + webPW}
	var got *settings
	code := runServe(t.Context(), environ, h.run, l, new(bytes.Buffer), new(bytes.Buffer), func(_ context.Context, s *settings) int {
		if len(h.calls) != 3 {
			t.Errorf("the apps started after %d of first start's 3 commands", len(h.calls))
		}
		got = s
		return 7
	})
	if code != 7 {
		t.Errorf("runServe() = %d, want the apps' 7", code)
	}
	if got == nil {
		t.Fatal("the apps were not started")
	}
	want := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.0.2.1/32")}
	if !slices.Equal(got.trusted, want) || got.l != l || !slices.Equal(envNames(got.environ), envNames(environ)) {
		t.Errorf("the apps got %v, %v and variables %q; want %v, %v and %q", got.trusted, got.l, envNames(got.environ), want, l, envNames(environ))
	}
}

// TestServeBadTrustedProxies is a TRUSTED_PROXIES that is not a list of
// addresses: one line names it, before first start runs anything.
func TestServeBadTrustedProxies(t *testing.T) {
	t.Parallel()
	h := &helpers{t: t}
	var stderr bytes.Buffer
	environ := []string{"TRUSTED_PROXIES=10.0.0.0/8,nonsense", "CALIBRE_ADMIN_PASSWORD=" + adminPW, "CALIBRE_WEB_ADMIN_PASSWORD=" + webPW}
	if code := runServe(t.Context(), environ, h.run, newLayout(t), new(bytes.Buffer), &stderr, noApps(t)); code != 1 {
		t.Errorf("runServe() = %d, want 1", code)
	}
	if got := stderr.String(); strings.Count(got, "\n") != 1 || !strings.Contains(got, "TRUSTED_PROXIES") || !strings.Contains(got, "nonsense") {
		t.Errorf("stderr = %q, want one line naming TRUSTED_PROXIES and its bad entry", got)
	}
	if len(h.calls) != 0 {
		t.Errorf("first start ran %q", h.argv0s())
	}
}

// TestChildren is the two apps' command lines, in the image, and their
// environment: PID 1's, without the password variables.
func TestChildren(t *testing.T) {
	t.Parallel()
	env := withoutPasswords([]string{"PATH=/usr/bin", "CALIBRE_ADMIN_PASSWORD=x", "CALIBRE_ADMIN_PASSWORD_FILE=y", "CALIBRE_WEB_ADMIN_PASSWORD=z", "CALIBRE_WEB_ADMIN_PASSWORD_FILE=w", "HOME=/tmp", "CALIBRE_TEMP_DIR=/tmp"})
	want := []supervise.Child{
		{Name: "calibre-server", Argv: []string{
			"/opt/calibre/calibre-server", "--listen-on", "127.0.0.1", "--port", "18080", "--enable-auth",
			"--userdb", "/config/calibre/users.sqlite", "--disable-use-bonjour", "/books",
		}},
		{Name: "calibre-web", Argv: []string{
			"/app/venv/bin/python", "-OO", "/app/calibre-web/cps.py", "-p", "/config/calibre-web/app.db",
			"-g", "/config/calibre-web/gdrive.db", "-i", "127.0.0.1", "-r", "-o", "/dev/stdout",
		}},
	}
	got := children(image, env, io.Discard)
	if len(got) != len(want) {
		t.Fatalf("%d children, want %d", len(got), len(want))
	}
	for i, c := range got {
		if c.Name != want[i].Name || !slices.Equal(c.Argv, want[i].Argv) {
			t.Errorf("child %d = %s %q, want %s %q", i, c.Name, c.Argv, want[i].Name, want[i].Argv)
		}
		for _, a := range c.Argv {
			if strings.Contains(a, "--enable-local-write") || strings.Contains(a, "--trusted-ips") {
				t.Errorf("%s is started with %s", c.Name, a)
			}
		}
		if names := envNames(c.Env); !slices.Equal(names, []string{"PATH", "HOME", "CALIBRE_TEMP_DIR"}) {
			t.Errorf("%s's environment holds %q, want PATH, HOME and CALIBRE_TEMP_DIR alone", c.Name, names)
		}
		// Only calibre-server keeps its temporary files in /config
		temp, before := "/tmp", false
		if c.Name == "calibre-server" {
			temp, before = "/config/calibre/tmp", true
		}
		if !slices.Contains(c.Env, "CALIBRE_TEMP_DIR="+temp) {
			t.Errorf("%s's CALIBRE_TEMP_DIR is not %s", c.Name, temp)
		}
		if (c.Before != nil) != before {
			t.Errorf("%s has a Before: %t, want %t", c.Name, c.Before != nil, before)
		}
	}
}

// TestServerTmpFresh: calibre-server's Before leaves its folder of
// temporary files there, empty, and its owner's alone, whatever it held,
// and whether or not it was there, and logs nothing.
func TestServerTmpFresh(t *testing.T) {
	t.Parallel()
	l := layout{config: t.TempDir(), books: "/books"}
	if err := os.Mkdir(l.calibre(), 0o700); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	before := children(l, nil, &log)[0].Before
	for _, held := range []bool{false, true} {
		if held {
			if err := os.MkdirAll(filepath.Join(l.serverTmp(), "srv-x"), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(l.serverTmp(), "srv-x", "large.pdf"), []byte("a copy"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(l.serverTmp(), 0o755); err != nil { //nolint:gosec // G302: a mode Before must take away
				t.Fatal(err)
			}
		}
		if err := before(); err != nil {
			t.Fatalf("held %t: %v", held, err)
		}
		checkEmpty(t, l.serverTmp())
	}
	if log.Len() != 0 {
		t.Errorf("Before logged %q, want nothing", log.String())
	}
}

// checkEmpty checks that dir is an empty directory, mode 0700.
func checkEmpty(t *testing.T, dir string) {
	t.Helper()
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("%s is %v, %v; want a directory, mode 0700", dir, fi, err)
	}
	if left, err := os.ReadDir(dir); err != nil || len(left) != 0 {
		t.Errorf("%s holds %d entries: %v", dir, len(left), err)
	}
}

// TestServerTmpInPlace: calibre-server's Before empties its folder of
// temporary files in place, and never removes it: the folder may be a
// volume of its own, which cannot be removed. Here the folder that holds
// it is read-only, so it can be neither removed nor made again. An entry
// it cannot remove stays, with one line that names it and says how to
// give the folder to the image's user, and calibre-server still starts.
func TestServerTmpInPlace(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root writes past every mode")
	}
	setup := func(t *testing.T) (layout, os.FileInfo) {
		t.Helper()
		l := layout{config: t.TempDir(), books: "/books"}
		if err := os.MkdirAll(filepath.Join(l.serverTmp(), "srv-x"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(l.serverTmp(), "srv-x", "large.pdf"), []byte("a copy"), 0o600); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Lstat(l.serverTmp())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(l.calibre(), 0o500); err != nil { //nolint:gosec // G302: a directory the test makes read-only
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(l.calibre(), 0o700) }) //nolint:gosec // G302: so the test's cleanup can remove it
		return l, fi
	}
	t.Run("a folder that cannot be removed", func(t *testing.T) {
		t.Parallel()
		l, was := setup(t)
		var log bytes.Buffer
		if err := children(l, nil, &log)[0].Before(); err != nil {
			t.Fatal(err)
		}
		checkEmpty(t, l.serverTmp())
		if fi, err := os.Lstat(l.serverTmp()); err != nil || !os.SameFile(was, fi) {
			t.Errorf("%s is not the folder it was: %v", l.serverTmp(), err)
		}
		if log.Len() != 0 {
			t.Errorf("Before logged %q, want nothing", log.String())
		}
	})
	t.Run("an entry that cannot be removed", func(t *testing.T) {
		t.Parallel()
		l, _ := setup(t)
		stuck := filepath.Join(l.serverTmp(), "srv-x")
		if err := os.Chmod(stuck, 0o500); err != nil { //nolint:gosec // G302: a directory the test makes read-only
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(stuck, 0o700) }) //nolint:gosec // G302: so the test's cleanup can remove it
		if err := os.WriteFile(filepath.Join(l.serverTmp(), "other.epub"), []byte("a copy"), 0o600); err != nil {
			t.Fatal(err)
		}
		var log bytes.Buffer
		if err := children(l, nil, &log)[0].Before(); err != nil {
			t.Fatalf("Before = %v, want nil: calibre-server can start", err)
		}
		left, err := os.ReadDir(l.serverTmp())
		if err != nil || len(left) != 1 || left[0].Name() != "srv-x" {
			t.Errorf("%s holds %v, %v; want srv-x alone", l.serverTmp(), left, err)
		}
		lines := strings.Split(strings.TrimSpace(log.String()), "\n")
		if len(lines) != 1 || !strings.Contains(lines[0], stuck+" stays") || !strings.Contains(lines[0], "chown -R") {
			t.Errorf("Before logged %q, want one line naming %s, with how to give it to the user", log.String(), stuck)
		}
	})
}

// TestServerTmpRefused: calibre-server's Before refuses a folder of
// temporary files that is a symbolic link, which it does not follow, and
// one that is not a folder, saying what it is, and changes neither.
func TestServerTmpRefused(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, want string
		make       func(t *testing.T, l layout) (kept string)
	}{
		{"a symbolic link", "is a symbolic link", func(t *testing.T, l layout) string {
			t.Helper()
			elsewhere := t.TempDir()
			kept := filepath.Join(elsewhere, "metadata.db")
			if err := os.WriteFile(kept, []byte("a library"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, l.serverTmp()); err != nil {
				t.Fatal(err)
			}
			return kept
		}},
		{"a file", "is not a directory", func(t *testing.T, l layout) string {
			t.Helper()
			if err := os.WriteFile(l.serverTmp(), []byte("a file"), 0o600); err != nil {
				t.Fatal(err)
			}
			return l.serverTmp()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := layout{config: t.TempDir(), books: "/books"}
			if err := os.Mkdir(l.calibre(), 0o700); err != nil {
				t.Fatal(err)
			}
			kept := tc.make(t, l)
			was, err := os.Lstat(l.serverTmp())
			if err != nil {
				t.Fatal(err)
			}
			var log bytes.Buffer
			err = children(l, nil, &log)[0].Before()
			if err == nil || !strings.Contains(err.Error(), l.serverTmp()+" "+tc.want) {
				t.Errorf("Before = %v, want one that says %s %s", err, l.serverTmp(), tc.want)
			}
			if fi, lerr := os.Lstat(l.serverTmp()); lerr != nil || !os.SameFile(was, fi) || fi.Mode().Type() != was.Mode().Type() {
				t.Errorf("%s was changed: %v", l.serverTmp(), lerr)
			}
			if _, serr := os.Stat(kept); serr != nil {
				t.Errorf("%s is gone: %v", kept, serr)
			}
		})
	}
}

// TestLibraryFiles is what serve watches: metadata.db, its journal and its
// write-ahead log.
func TestLibraryFiles(t *testing.T) {
	t.Parallel()
	want := []string{"/books/metadata.db", "/books/metadata.db-journal", "/books/metadata.db-wal"}
	if got := libraryFiles(image); !slices.Equal(got, want) {
		t.Errorf("libraryFiles() = %q, want %q", got, want)
	}
}

// TestLoginReader is the read of Calibre-Web's login header setting: the
// helper's get-login-header mode, on app.db, with an environment that holds
// no password, and its output trimmed. A read that fails names app.db.
func TestLoginReader(t *testing.T) {
	t.Parallel()
	var argv, env []string
	run := func(_ context.Context, a, e []string, stdin []byte) ([]byte, error) {
		argv, env = a, e
		if stdin != nil {
			t.Error("the helper was given stdin")
		}
		return []byte(" X-Remote-User \n"), nil
	}
	read := loginReader(run, withoutPasswords([]string{"PATH=/usr/bin", "CALIBRE_WEB_ADMIN_PASSWORD=" + webPW}), image)
	name, err := read(t.Context())
	if err != nil || name != "X-Remote-User" {
		t.Errorf("read() = %q, %v, want X-Remote-User", name, err)
	}
	want := []string{"/app/venv/bin/python", "/usr/local/share/divbast-calibre/calibre_web_init.py", "get-login-header", "/config/calibre-web/app.db"}
	if !slices.Equal(argv, want) {
		t.Errorf("ran %q, want %q", argv, want)
	}
	if names := envNames(env); !slices.Equal(names, []string{"PATH"}) {
		t.Errorf("the helper's environment holds %q, want PATH alone", names)
	}
	failing := loginReader(func(context.Context, []string, []string, []byte) ([]byte, error) {
		return nil, errors.New("/app/venv/bin/python exited with status 1: calibre_web_init: DatabaseError: file is not a database")
	}, nil, image)
	_, err = failing(t.Context())
	wantErr := "cannot read Calibre-Web's login header setting from /config/calibre-web/app.db: " +
		"/app/venv/bin/python exited with status 1: calibre_web_init: DatabaseError: file is not a database"
	if err == nil || err.Error() != wantErr {
		t.Errorf("a failed read gave the error %v, want %q", err, wantErr)
	}
}

// TestRereadLogged is a read of the login header setting after the first:
// each that fails logs one line, which names app.db and says why, as the
// start's own failure does; one that works logs nothing. Before logging is
// on, a failure logs nothing, since the start reports the first read's
// failure itself.
func TestRereadLogged(t *testing.T) {
	t.Parallel()
	fail := true
	r := &loggedRead{read: loginReader(func(context.Context, []string, []string, []byte) ([]byte, error) {
		if fail {
			return nil, errors.New("/app/venv/bin/python exited with status 1: calibre_web_init: DatabaseError: file is not a database")
		}
		return []byte("X-Remote-User\n"), nil
	}, nil, image)}
	var logw bytes.Buffer
	r.logw = &logw
	line := "divbast-calibre: cannot read Calibre-Web's login header setting from /config/calibre-web/app.db: " +
		"/app/venv/bin/python exited with status 1: calibre_web_init: DatabaseError: file is not a database\n"

	if _, err := r.Read(t.Context()); err == nil || logw.Len() != 0 {
		t.Errorf("the first read, before logging is on: error %v, log %q, want an error and no log", err, logw.String())
	}
	r.on.Store(true)
	for i := range 2 {
		if _, err := r.Read(t.Context()); err == nil {
			t.Errorf("failed read %d gave no error", i+1)
		}
	}
	if got := logw.String(); got != line+line {
		t.Errorf("two failed reads logged %q, want this line twice: %q", got, line)
	}
	logw.Reset()
	fail = false
	if name, err := r.Read(t.Context()); err != nil || name != "X-Remote-User" || logw.Len() != 0 {
		t.Errorf("a read that works: %q, %v, log %q; want X-Remote-User, no error, and no log", name, err, logw.String())
	}
}

// fakeEvents records what the hooks tell the Coordinator.
type fakeEvents struct {
	mu       sync.Mutex
	started  []string
	gated    []string
	answered []string
	holds    int
	waits    int // WaitRestart's calls
	wrapped  int // WrapperStarted's done functions called
}

func (f *fakeEvents) WrapperStarted() func() {
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.wrapped++
	}
}

func (f *fakeEvents) Hold(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.holds++
	return nil
}

func (f *fakeEvents) WaitRestart(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waits++
	return nil
}

func (f *fakeEvents) RequestStarted(app reload.App, method, path string) func(int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, app.String()+" "+method+" "+path)
	return func(int) {}
}

func (f *fakeEvents) Gate(app reload.App, method, path string) (deny bool, retryAfter time.Duration, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gated = append(f.gated, app.String()+" "+method+" "+path)
	return false, 0, ""
}

func (f *fakeEvents) Answered(method, path string, status int, _ http.Header, body []byte, whole bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answered = append(f.answered, method+" "+path+" "+http.StatusText(status)+" "+string(body)+" "+map[bool]string{true: "whole", false: "part"}[whole])
}

// TestHooks is each port's hooks: both count requests and log failures;
// only 8080's gate requests, and read the answers about conversions.
func TestHooks(t *testing.T) {
	t.Parallel()
	f := &fakeEvents{}
	var log bytes.Buffer
	cs, cw := hooks(f, reload.ContentServer, func() {}, &log), hooks(f, reload.CalibreWeb, func() {}, &log)
	if cw.Gate != nil || cw.Observe != nil || cw.Hold != nil {
		t.Error("Calibre-Web's port gates requests, reads answers, or holds requests")
	}
	req := func(method, path string) *http.Request {
		return httptest.NewRequestWithContext(t.Context(), method, "http://calibre.test"+path, http.NoBody)
	}
	cs.Started(req(http.MethodPost, "/cdb/set-fields/12"))
	cw.Started(req(http.MethodPost, "/upload"))
	cs.Gate(req(http.MethodGet, "/ajax/books"))
	if cs.Observe(req(http.MethodGet, "/ajax/books")) != nil {
		t.Error("an answer that is not about a conversion is read")
	}
	seen := cs.Observe(req(http.MethodPost, "/conversion/start/12"))
	if seen == nil {
		t.Fatal("the answer to a conversion's start is not read")
	}
	seen(http.StatusOK, nil, []byte("5"), true)
	f.mu.Lock()
	if want := []string{"calibre-server POST /cdb/set-fields/12", "calibre-web POST /upload"}; !slices.Equal(f.started, want) {
		t.Errorf("started %q, want %q", f.started, want)
	}
	if want := []string{"calibre-server GET /ajax/books"}; !slices.Equal(f.gated, want) {
		t.Errorf("gated %q, want %q", f.gated, want)
	}
	if want := []string{"POST /conversion/start/12 OK 5 whole"}; !slices.Equal(f.answered, want) {
		t.Errorf("answered %q, want %q", f.answered, want)
	}
	f.mu.Unlock()

	cw.Failed(req(http.MethodGet, "/kobo/s3cr3t-token/v1/library/sync"), errors.New("dial tcp 127.0.0.1:18083: connect: connection refused"))
	cs.Failed(req(http.MethodPost, "/cdb/set-fields/12"), errors.New("unexpected EOF"))
	want := "divbast-calibre: calibre-web could not answer GET /kobo/<token>/v1/library/sync: dial tcp 127.0.0.1:18083: connect: connection refused\n" +
		"divbast-calibre: calibre-server could not answer POST /cdb/set-fields/12: unexpected EOF\n"
	if got := log.String(); got != want {
		t.Errorf("log = %q, want %q", got, want)
	}
}

// TestLogPath is a path as the log shows it: the token a Kobo, or a remote
// login, sends in the path is left out.
func TestLogPath(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ path, want string }{
		{"/kobo/0123abcd/v1/library/sync", "/kobo/<token>/v1/library/sync"},
		{"/kobo/0123abcd", "/kobo/<token>"},
		{"/verify/0123abcd", "/verify/<token>"},
		{"/kobo_auth/generate_auth_token/3", "/kobo_auth/generate_auth_token/3"},
		{"/kobo/", "/kobo/"},
		{"/ajax/books", "/ajax/books"},
		{"/", "/"},
	} {
		if got := logPath(tt.path); got != tt.want {
			t.Errorf("logPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

// TestReconnect is Calibre-Web's reconnect: a GET of /reconnect, which must
// answer 200.
func TestReconnect(t *testing.T) {
	t.Parallel()
	status := http.StatusOK
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, r.Method+" "+r.URL.Path)
		w.WriteHeader(status)
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)
	r := &reloader{client: srv.Client(), reconnect: srv.URL + "/reconnect"}
	if err := r.ReconnectCalibreWeb(t.Context()); err != nil {
		t.Errorf("ReconnectCalibreWeb() = %v", err)
	}
	mu.Lock()
	status = http.StatusNotFound
	mu.Unlock()
	if err := r.ReconnectCalibreWeb(t.Context()); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("ReconnectCalibreWeb() = %v, want an error naming the 404", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"GET /reconnect", "GET /reconnect"}; !slices.Equal(asked, want) {
		t.Errorf("asked %q, want %q", asked, want)
	}
}

// fakeStarts records what the tracker tells the Coordinator.
type fakeStarts struct {
	mu        sync.Mutex
	started   []reload.App
	accepting chan accepted
}

func (f *fakeStarts) Started(app reload.App) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, app)
	return len(f.started)
}

func (f *fakeStarts) Accepting(app reload.App, start int) {
	f.accepting <- accepted{app, start}
}

// accepted is one Accepting call.
type accepted struct {
	app   reload.App
	start int
}

// TestTracker is the state file and the starts: the file holds both apps
// down at first, and each change after; each start, and only a start, is
// told, and then that the app's port accepts.
func TestTracker(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "divbast-calibre", "state.json")
	f := &fakeStarts{accepting: make(chan accepted, 4)}
	tr := newTracker(t.Context(), path, f, new(bytes.Buffer))
	var mu sync.Mutex
	var polled []string
	tr.wait = func(_ context.Context, addr string, every time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		if every != time.Second {
			t.Errorf("the port is polled every %s, want 1s", every)
		}
		polled = append(polled, addr)
		return nil
	}
	if err := tr.write(); err != nil {
		t.Fatal(err)
	}
	states, err := supervise.ReadState(path)
	if err != nil || len(states) != 2 || states["calibre-server"].Up || states["calibre-web"].Up {
		t.Fatalf("the first state file = %v, %v; want both apps down", states, err)
	}

	at := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	for _, ch := range []struct {
		name string
		st   supervise.State
	}{
		{"calibre-server", supervise.State{Up: true, Since: at}},
		{"calibre-web", supervise.State{Up: true, Since: at}},
		{"calibre-server", supervise.State{Up: true, Planned: true, Since: at}}, // a restart begins
		{"calibre-server", supervise.State{Planned: true, Since: at}},
		{"calibre-server", supervise.State{Up: true, Since: at.Add(time.Second)}},
	} {
		tr.change(ch.name, ch.st)
	}
	var got []accepted
	for range 3 {
		select {
		case a := <-f.accepting:
			got = append(got, a)
		case <-time.After(10 * time.Second):
			t.Fatal("the tracker did not tell the Coordinator that a port accepts")
		}
	}
	// Each Accepting names the start it follows
	slices.SortFunc(got, func(a, b accepted) int { return a.start - b.start })
	if want := []accepted{{reload.ContentServer, 1}, {reload.CalibreWeb, 2}, {reload.ContentServer, 3}}; !slices.Equal(got, want) {
		t.Errorf("accepting %v, want %v", got, want)
	}
	f.mu.Lock()
	if want := []reload.App{reload.ContentServer, reload.CalibreWeb, reload.ContentServer}; !slices.Equal(f.started, want) {
		t.Errorf("started %v, want %v", f.started, want)
	}
	f.mu.Unlock()
	mu.Lock()
	if want := []string{"127.0.0.1:18080", "127.0.0.1:18083", "127.0.0.1:18080"}; !slices.Equal(polled, want) {
		t.Errorf("polled %q, want %q", polled, want)
	}
	mu.Unlock()
	states, err = supervise.ReadState(path)
	if err != nil || !states["calibre-server"].Up || !states["calibre-server"].Since.Equal(at.Add(time.Second)) || !states["calibre-web"].Up {
		t.Errorf("the state file = %v, %v; want both up, calibre-server since its second start", states, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Error(err)
	}
}

// TestHoldWrapper: on 8080, an export from the loopback that logs in by
// digest as calibre-web, Calibre-Web's own calibredb, has PID 1 look at
// the library, and is held; every request of that calibredb counts as
// Calibre-Web's once it goes on. No other request is held, whatever it
// shares with it: show_metadata, any other request from the loopback,
// which waits out a restart alone, and any request from outside, which
// never waits.
func TestHoldWrapper(t *testing.T) {
	t.Parallel()
	const (
		digest   = `Digest username="calibre-web", realm="calibre", nonce="n", uri="/cdb/cmd/export/0", response="r"`
		export   = "/cdb/cmd/export/0"
		metadata = "/cdb/cmd/show_metadata/0"
	)
	cases := []struct {
		name, peer, auth, path string
		held, wrapper, waits   bool // held as an export; counted as Calibre-Web's; waiting out a restart alone
	}{
		{"the wrapper's export, on IPv4", "127.0.0.1:40000", digest, export, true, true, false},
		{"the wrapper's export, on IPv6", "[::1]:40000", digest, export, true, true, false},
		{"the wrapper's export, IPv4 in IPv6", "[::ffff:127.0.0.1]:40000", digest, export, true, true, false},
		{"the scheme in other case, the parameter elsewhere", "127.0.0.1:40000", `digest realm="calibre", username="calibre-web", nonce="n"`, export, true, true, false},
		{"the wrapper's export, empty parts in its path", "127.0.0.1:40000", digest, "//cdb//cmd/export/0/", true, true, false},
		{"the wrapper's export, with no version", "127.0.0.1:40000", digest, "/cdb/cmd/export", true, true, false},
		{"the wrapper's show_metadata", "127.0.0.1:40000", digest, metadata, false, true, true},
		{"the wrapper, a command that only starts as export", "127.0.0.1:40000", digest, "/cdb/cmd/exports/0", false, true, true},
		{"the loopback, no login", "127.0.0.1:40000", "", export, false, false, true},
		{"the loopback on IPv6, no login", "[::1]:40000", "", export, false, false, true},
		{"the loopback, as admin", "127.0.0.1:40000", `Digest username="admin", realm="calibre"`, export, false, false, true},
		{"the loopback, Basic", "127.0.0.1:40000", "Basic Y2FsaWJyZS13ZWI6eA==", export, false, false, true},
		{"the loopback, a name that only ends so", "127.0.0.1:40000", `Digest username="x-calibre-web", realm="calibre"`, export, false, false, true},
		{"a client outside, as calibre-web", "192.0.2.7:40000", digest, export, false, false, false},
		{"the container's own address, as calibre-web", "10.89.0.2:40000", digest, export, false, false, false},
		{"a client outside, no login", "192.0.2.7:40000", "", export, false, false, false},
	}
	one := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeEvents{}
			looks := 0
			h := hooks(f, reload.ContentServer, func() { looks++ }, io.Discard)
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://127.0.0.1:8080/", http.NoBody)
			r.URL.Path = tc.path
			r.RemoteAddr = tc.peer
			if tc.auth != "" {
				r.Header.Set("Authorization", tc.auth)
			}
			if err := h.Hold(r); err != nil {
				t.Fatal(err)
			}
			h.Started(r)(http.StatusOK)
			if looks != one(tc.held) || f.holds != one(tc.held) {
				t.Errorf("looked %d times and held %d, want %d each", looks, f.holds, one(tc.held))
			}
			if f.wrapped != one(tc.wrapper) {
				t.Errorf("counted %d for Calibre-Web, want %d", f.wrapped, one(tc.wrapper))
			}
			if f.waits != one(tc.waits) {
				t.Errorf("waited out a restart %d times, want %d", f.waits, one(tc.waits))
			}
			if len(f.started) != 1 {
				t.Errorf("Started counted %d requests, want 1", len(f.started))
			}
		})
	}
}

// countingReloader counts calibre-server's restarts, and reconnects
// Calibre-Web at once.
type countingReloader struct{ restarts atomic.Int32 }

func (*countingReloader) ReconnectCalibreWeb(context.Context) error { return nil }

func (r *countingReloader) RestartContentServer(context.Context) error {
	r.restarts.Add(1)
	return nil
}

// TestHoldExportOnly runs 8080's hooks against a Coordinator on the real
// clock, with a Quiet of 300 ms: while a restart is due after a change in
// Calibre-Web, the wrapper's show_metadata goes on at once and brings no
// restart; its export is held, restarts calibre-server at once, and goes
// on once calibre-server takes connections. A second export, right after
// that restart's start, brings the next restart only Quiet after it.
func TestHoldExportOnly(t *testing.T) {
	t.Parallel()
	opts := reload.Options{Window: time.Second, Quiet: 300 * time.Millisecond, Cap: time.Minute, RetryAfter: time.Second}
	rl := &countingReloader{}
	c := reload.New(reload.SystemClock(), rl, opts)
	t.Cleanup(c.Stop)
	for _, app := range []reload.App{reload.ContentServer, reload.CalibreWeb} {
		c.Accepting(app, c.Started(app))
	}
	time.Sleep(opts.Quiet + 50*time.Millisecond) // past the pacing of calibre-server's start
	h := hooks(c, reload.ContentServer, func() {}, io.Discard)
	edit := func() {
		done := c.RequestStarted(reload.CalibreWeb, http.MethodPost, "/admin/book/1")
		c.DBChanged(time.Now())
		done(http.StatusOK)
	}
	hold := func(path string) <-chan error {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://127.0.0.1:8080"+path, http.NoBody)
		r.RemoteAddr = "127.0.0.1:40000"
		r.Header.Set("Authorization", `Digest username="calibre-web", realm="calibre", nonce="n"`)
		errc := make(chan error, 1)
		go func() { errc <- h.Hold(r) }()
		return errc
	}
	within := func(errc <-chan error, d time.Duration) bool {
		select {
		case err := <-errc:
			if err != nil {
				t.Errorf("Hold: %v", err)
			}
			return true
		case <-time.After(d):
			return false
		}
	}

	edit()
	if !within(hold("/cdb/cmd/show_metadata/0"), 100*time.Millisecond) || rl.restarts.Load() != 0 {
		t.Fatalf("show_metadata, with a restart due: held, or %d restarts; want at once, none", rl.restarts.Load())
	}
	exported := hold("/cdb/cmd/export/0")
	if within(exported, 100*time.Millisecond) {
		t.Fatal("the export, with a restart due, went on before the restart")
	}
	if got := rl.restarts.Load(); got != 1 {
		t.Fatalf("the export brought %d restarts, want 1 at once", got)
	}
	c.Accepting(reload.ContentServer, c.Started(reload.ContentServer))
	if !within(exported, time.Second) {
		t.Fatal("the export was not let go once calibre-server took connections")
	}

	edit()
	again := hold("/cdb/cmd/export/0")
	if within(again, opts.Quiet/2) || rl.restarts.Load() != 1 {
		t.Fatalf("an export right after the restart's start: went on, or %d restarts; want held, 1", rl.restarts.Load())
	}
	if within(again, opts.Quiet) {
		t.Fatal("the second export went on before its restart")
	}
	if got := rl.restarts.Load(); got != 2 {
		t.Fatalf("Quiet after the restart's start: %d restarts, want 2", got)
	}
	c.Accepting(reload.ContentServer, c.Started(reload.ContentServer))
	if !within(again, time.Second) {
		t.Fatal("the second export was not let go")
	}
}
