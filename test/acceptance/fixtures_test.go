// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // G501: calibre-server's digest login and KOReader's sync key are MD5, as the servers ask for them
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"maps"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/divbast/ci/harness"
)

// Where the image keeps what the suite reads and sets up.
const (
	// python is Calibre-Web's Python, the image's own, which the suite
	// runs inside a container to set up and read what it checks: the
	// image has no shell.
	python = "/app/venv/bin/python"
	// calibredb is Calibre's own calibredb, in the bundle.
	calibredb = "/opt/calibre/calibredb"
	booksDir  = "/books"
	configDir = "/config"
	// secretsDir is where a test mounts the files that hold its
	// passwords.
	secretsDir = "/secrets"
	// statePath is where PID 1 writes each app's state, for health.
	statePath = "/tmp/divbast-calibre/state.json"
)

// The variables the image reads.
const (
	adminVar    = "CALIBRE_ADMIN_PASSWORD"
	webAdminVar = "CALIBRE_WEB_ADMIN_PASSWORD"
	trustedVar  = "TRUSTED_PROXIES"
)

// passwordVars are the four variables that can carry a password, none of
// which an app may inherit.
var passwordVars = []string{adminVar, adminVar + "_FILE", webAdminVar, webAdminVar + "_FILE"}

// The two apps, by the names PID 1 gives them in its log.
const (
	contentServer = "calibre-server"
	calibreWeb    = "calibre-web"
)

// apps are both apps, in the order the suite goes through them.
var apps = []string{contentServer, calibreWeb}

// admin is the first account of both apps.
const admin = "admin"

// The suite's times.
const (
	// startWithin is the image's start period: a start must be healthy
	// within it.
	startWithin = 60 * time.Second
	// refuseWithin bounds a start that must fail.
	refuseWithin = 60 * time.Second
	// healthEvery is how often a wait runs the shipped probe.
	healthEvery = 200 * time.Millisecond
	// cleanupWithin bounds what a test's cleanup asks the engine.
	cleanupWithin = 30 * time.Second
)

// engineLogs keeps Podman's container logs in a file of their own,
// k8s-file: journald, Podman's default, rate-limits, and a test that reads
// a log would see lines go missing.
func engineLogs(e harness.Engine) []string {
	if e.Name() == "podman" {
		return []string{"--log-driver", "k8s-file"}
	}
	return nil
}

// Character sets the suite's passwords are drawn from.
const (
	lower  = "abcdefghijklmnopqrstuvwxyz"
	upper  = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	digits = "0123456789"
)

// random returns n characters, each drawn at random from set.
func random(tb testing.TB, n int, set string) string {
	tb.Helper()
	b := make([]byte, n)
	for i := range b {
		k, err := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
		if err != nil {
			tb.Fatal(err)
		}
		b[i] = set[k.Int64()]
	}
	return string(b)
}

// password returns a new password that follows Calibre-Web's default
// password rules: a letter of each case, a digit and a special character,
// then 20 random letters and digits.
func password(tb testing.TB) string {
	tb.Helper()
	return "Aa1!" + random(tb, 20, lower+upper+digits)
}

// secrets are the two admin passwords a test starts the image with, each
// in a file of its own, which the container reads through a read-only
// bind mount at secretsDir, as Docker and Kubernetes give a container a
// secret. No password goes on a command line: each variable names a file.
type secrets struct {
	admin, web string
	dir        string // the directory on the host
}

// newSecrets returns two new passwords that follow the rules, which r
// hides.
func newSecrets(r *redactor) *secrets {
	r.Helper()
	return writeSecrets(r, password(r), password(r))
}

// writeSecrets writes the two passwords to their files, each with the
// newline that an editor or echo leaves at the end, which the image takes
// off, and has r hide both.
func writeSecrets(r *redactor, adminPW, webPW string) *secrets {
	r.Helper()
	r.hide(adminPW, webPW)
	dir := secretDir(r)
	for name, pw := range map[string]string{"admin": adminPW, "web": webPW} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(pw+"\n"), 0o600); err != nil {
			r.Fatal(err)
		}
		// The bind mount keeps the file's mode, and the image's user,
		// uid 65532, is not its owner
		if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // G302: uid 65532 reads it, through a read-only bind mount
			r.Fatal(err)
		}
	}
	return &secrets{admin: adminPW, web: webPW, dir: dir}
}

// secretDir is a new directory for a test's password files, which uid
// 65532 may enter, removed when the test ends. It is on /dev/shm, a tmpfs,
// where the host is Linux, so that no password reaches a disk, and in the
// test's temporary directory elsewhere.
func secretDir(tb testing.TB) string {
	tb.Helper()
	dir := ""
	if runtime.GOOS == "linux" {
		if d, err := os.MkdirTemp("/dev/shm", "calibre-acceptance-"); err == nil {
			dir = d
			tb.Cleanup(func() {
				if err := os.RemoveAll(d); err != nil {
					tb.Error(err)
				}
			})
		}
	}
	if dir == "" {
		dir = tb.TempDir()
	}
	if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // G302: uid 65532 enters it, through a read-only bind mount
		tb.Fatal(err)
	}
	return dir
}

// mount is the password files' directory, read-only at secretsDir.
func (s *secrets) mount() harness.Mount {
	return harness.Mount{Source: s.dir, Target: secretsDir, ReadOnly: true}
}

// env names both files, in the two _FILE variables.
func (s *secrets) env() map[string]string {
	return map[string]string{
		adminVar + "_FILE":    secretsDir + "/admin",
		webAdminVar + "_FILE": secretsDir + "/web",
	}
}

// all are both passwords, which no log may hold.
func (s *secrets) all() []string {
	return []string{s.admin, s.web}
}

// noPassword checks that text, what a container or a process printed,
// holds none of passwords, nor any part of one between its control
// characters. It says how often it found one, never which one, or where.
// An empty password is a failure: it would be found nowhere, which proves
// nothing.
func noPassword(c *harness.Checks, what, text string, passwords []string) {
	found := 0
	empty := len(passwords) == 0
	for _, pw := range passwords {
		empty = empty || pw == ""
		for _, p := range pieces(pw) {
			found += strings.Count(text, p)
		}
	}
	c.True(what+" holds no password", found == 0 && !empty, fmt.Sprintf("%d passwords, an empty one among them: %t; found %d times", len(passwords), empty, found))
}

// volume is a new, empty volume at target, which the engine gives the
// image's owner of target, uid 65532, on its first mount.
func volume(tb testing.TB, e harness.Engine, target string) harness.Mount {
	tb.Helper()
	name, err := e.Volume(tb.Context(), tb, "")
	if err != nil {
		tb.Fatal(err)
	}
	return harness.Mount{Source: name, Target: target, Volume: true}
}

// store is the two volumes the image keeps its data in: the library at
// /books, and the apps' settings at /config.
type store struct {
	books, config harness.Mount
}

// newStore returns two new, empty volumes.
func newStore(tb testing.TB, e harness.Engine) *store {
	tb.Helper()
	return &store{books: volume(tb, e, booksDir), config: volume(tb, e, configDir)}
}

// mounts are both volumes.
func (s *store) mounts() []harness.Mount {
	return []harness.Mount{s.books, s.config}
}

// asRoot runs the image's own tools as root, for what a test sets up in a
// volume, or reads from one: read-only, with no new privileges, and with a
// /tmp for what Calibre writes while it works.
var asRoot = harness.Confinement{User: "0:0", ReadOnly: true, NoNewPrivileges: true, TmpfsSize: "256m"}

// rootPython runs code, a Python program, in image as root, on no
// network, with mounts and with args as its sys.argv[1:], and returns what
// it printed to stdout. Anything but exit 0 fails t.
func rootPython(tb testing.TB, e harness.Engine, image, code string, mounts []harness.Mount, args ...string) string {
	tb.Helper()
	res, err := e.Run(tb.Context(), tb, &harness.RunSpec{
		Image:      image,
		Network:    "none",
		Confine:    asRoot,
		Mounts:     mounts,
		Entrypoint: []string{python},
		Args:       slices.Concat([]string{"-I", "-c", code}, args),
	})
	if err != nil || res.Code != 0 {
		tb.Fatalf("running Python as root in %s: %v, exit status %d: %s", image, err, res.Code, harness.Clip(string(res.Stderr)))
	}
	return string(res.Stdout)
}

// The one book the fixture library holds.
const (
	fixtureTitle  = "Divbast Fixture Book"
	fixtureAuthor = "Divbast Test"
)

// ownDef is Python that defines own(top, uid), which gives the directory
// top, and everything in it, to uid and the gid of the same number.
const ownDef = `import os, sys
def own(top, uid):
    for root, dirs, files in os.walk(top):
        for name in dirs + files:
            os.lchown(os.path.join(root, name), uid, uid)
    os.chown(top, uid, uid)
`

// ownCode is Python that gives the directory sys.argv[1], and everything in
// it, to the uid and gid sys.argv[2].
const ownCode = ownDef + `own(sys.argv[1], int(sys.argv[2]))
`

// libraryCode is Python that makes a Calibre library in /books with one
// book, a text file, through the bundle's own calibredb, as a desktop
// Calibre of the image's version would make it, then gives everything in
// /books to the uid sys.argv[1]. sys.argv[2] and [3] are the book's title
// and author.
const libraryCode = ownDef + `
import subprocess
os.makedirs("/tmp/in")
book = "/tmp/in/book.txt"
with open(book, "w") as f:
    f.write("A book the acceptance suite reads.\n")
env = dict(os.environ, CALIBRE_CONFIG_DIRECTORY="/tmp/calibre-config")
subprocess.run(["` + calibredb + `", "--with-library", "/books", "add", "--title", sys.argv[2], "--authors", sys.argv[3], book],
               check=True, env=env, stdout=subprocess.DEVNULL)
own("/books", int(sys.argv[1]))
`

// library is a new books volume holding a Calibre library of one book,
// made by the image's own calibredb, and owned by uid.
func library(tb testing.TB, e harness.Engine, image string, uid int) harness.Mount {
	tb.Helper()
	books := volume(tb, e, booksDir)
	rootPython(tb, e, image, libraryCode, []harness.Mount{books}, strconv.Itoa(uid), fixtureTitle, fixtureAuthor)
	return books
}

// own gives m's directory, and everything in it, to uid, as an operator
// does with chown -R.
func own(tb testing.TB, e harness.Engine, image string, m harness.Mount, uid int) {
	tb.Helper()
	rootPython(tb, e, image, ownCode, []harness.Mount{m}, m.Target, strconv.Itoa(uid))
}

// listCode is Python that prints every path under each directory sys.argv
// names, one to a line.
const listCode = `import os, sys
for top in sys.argv[1:]:
    for root, dirs, files in os.walk(top):
        for name in dirs + files:
            print(os.path.join(root, name))
`

// nothingWritten checks that st's volumes hold nothing, as the engine
// first made them.
func nothingWritten(tb testing.TB, e harness.Engine, image string, st *store) {
	tb.Helper()
	out := rootPython(tb, e, image, listCode, st.mounts(), booksDir, configDir)
	harness.Equal(harness.NewChecks(tb), "nothing is written to /books or /config", "", strings.TrimSpace(out))
}

// launch is one container of the image, under the contract.
type launch struct {
	env       map[string]string
	mounts    []harness.Mount
	network   string   // empty for one of its own
	ip        string   // a fixed address on network, or empty
	passwords []string // none of which its log may hold
}

// spec is l as the engine runs it: under the contract, with the health
// command Podman needs, and Podman's log kept whole.
func (l *launch) spec(e harness.Engine, image string) harness.RunSpec {
	return harness.RunSpec{
		Image:     image,
		Network:   l.network,
		IP:        l.ip,
		Confine:   harness.Contract(tmpfsSize),
		Env:       maps.Clone(l.env),
		Mounts:    slices.Clone(l.mounts),
		HealthCmd: healthCmd,
		Extra:     engineLogs(e),
	}
}

// serve starts l with ports 8080 and 8083 published on the host's
// loopback, and returns it once the shipped probe passes in it: both apps
// answer. 8083 is the ServeSpec's health port, the second port Serve
// publishes, so the Serving's Health copy sends to Calibre-Web. When the
// test ends, its log must hold none of l's passwords.
func (l *launch) serve(tb testing.TB, e harness.Engine, image string) *harness.Serving {
	tb.Helper()
	ctx := tb.Context()
	spec := l.spec(e, image)
	s, err := e.Serve(ctx, tb, &harness.ServeSpec{Run: spec, AppPort: appPort, HealthPort: webPort})
	if err != nil {
		tb.Fatalf("serving %s: %v", image, err)
	}
	watchLog(tb, s.C, l.passwords)
	if err := waitHealthy(ctx, s.C); err != nil {
		tb.Fatalf("the shipped probe never passed: %v", harness.WithLog(ctx, s.C, err))
	}
	return s
}

// watchLog checks, when the test ends, that c's log holds none of
// passwords.
func watchLog(tb testing.TB, c *harness.Container, passwords []string) {
	tb.Helper()
	pws := slices.Clone(passwords)
	tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), cleanupWithin)
		defer cancel()
		checkLog(ctx, tb, c, pws)
	})
}

// checkLog checks that c's log holds none of passwords.
func checkLog(ctx context.Context, tb testing.TB, c *harness.Container, passwords []string) {
	tb.Helper()
	logs, err := c.Logs(ctx)
	if err != nil {
		tb.Errorf("reading the log of %s: %v", c, err)
		return
	}
	noPassword(harness.NewChecks(tb), "the log of "+c.String(), logs, passwords)
}

// refusal is how a start that must fail ended.
type refusal struct {
	code           int
	stdout, stderr string
	took           time.Duration
}

// refuse runs l in the foreground, on no network, and returns how it
// ended. One that runs past refuseWithin fails t.
func (l *launch) refuse(tb testing.TB, e harness.Engine, image string) refusal {
	tb.Helper()
	spec := l.spec(e, image)
	spec.Network, spec.HealthCmd, spec.Extra = "none", nil, nil
	ctx, cancel := context.WithTimeout(tb.Context(), refuseWithin)
	defer cancel()
	start := time.Now()
	res, err := e.Run(ctx, tb, &spec)
	took := time.Since(start)
	if err != nil {
		tb.Fatalf("the start of %s did not end within %s: %v", image, refuseWithin, err)
	}
	return refusal{code: res.Code, stdout: string(res.Stdout), stderr: string(res.Stderr), took: took}
}

// lines are text's lines that are not empty.
func lines(text string) []string {
	return slices.DeleteFunc(strings.Split(text, "\n"), func(s string) bool { return strings.TrimSpace(s) == "" })
}

// refused checks that r exited 1 within refuseWithin, that its stderr is
// one line holding each of needles, and that nothing it printed holds one
// of passwords.
func refused(tb testing.TB, r refusal, passwords []string, needles ...string) {
	tb.Helper()
	checks := harness.NewChecks(tb)
	harness.Equal(checks, "the start exits 1", 1, r.code)
	checks.True("the start ends within "+refuseWithin.String(), r.took < refuseWithin, r.took.String())
	got := lines(r.stderr)
	checks.True("the start prints one line to stderr", len(got) == 1, harness.Clip(r.stderr))
	for _, n := range needles {
		harness.Contains(tb, "the line says '"+n+"'", n, r.stderr)
	}
	noPassword(checks, "what the start printed", r.stdout+r.stderr, passwords)
}

// inPython runs code, a Python program, in c as the container's user, with
// args as its sys.argv[1:], and returns what it printed to stdout. An exit
// status other than 0 is an error.
func inPython(ctx context.Context, c *harness.Container, code string, args ...string) (string, error) {
	res, err := c.Exec(ctx, nil, slices.Concat([]string{python, "-I", "-c", code}, args)...)
	if err != nil {
		return "", err
	}
	if res.Code != 0 {
		return "", fmt.Errorf("python in %s: exit status %d: %s", c, res.Code, harness.Clip(string(res.Stderr)))
	}
	return string(res.Stdout), nil
}

// proc is one process in a container, as /proc/<pid>/stat and
// /proc/<pid>/cmdline give it.
type proc struct {
	PID   int      `json:"pid"`
	State string   `json:"state"`
	PPID  int      `json:"ppid"`
	PGID  int      `json:"pgid"`
	Argv  []string `json:"argv"`
}

// psCode is Python that prints every process in the container but itself,
// as JSON.
const psCode = `import json, os
me = os.getpid()
out = []
for d in os.listdir("/proc"):
    if not d.isdigit() or int(d) == me:
        continue
    try:
        stat = open(f"/proc/{d}/stat").read()
        argv = open(f"/proc/{d}/cmdline", "rb").read().split(b"\0")
    except OSError:
        continue
    f = stat[stat.rindex(")") + 2:].split()
    out.append({"pid": int(d), "state": f[0], "ppid": int(f[1]), "pgid": int(f[2]),
                "argv": [a.decode(errors="replace") for a in argv if a]})
print(json.dumps(out))
`

// processes returns every process in c.
func processes(ctx context.Context, c *harness.Container) ([]proc, error) {
	out, err := inPython(ctx, c, psCode)
	if err != nil {
		return nil, err
	}
	var ps []proc
	if err := json.Unmarshal([]byte(out), &ps); err != nil {
		return nil, fmt.Errorf("reading the processes in %s: %w", c, err)
	}
	return ps, nil
}

// isApp reports whether p is app's own process: calibre-server, by its
// program's name, or Calibre-Web, by its script.
func (p *proc) isApp(app string) bool {
	if app == contentServer {
		return len(p.Argv) > 0 && filepath.Base(p.Argv[0]) == "calibre-server"
	}
	return slices.Contains(p.Argv, "/app/calibre-web/cps.py")
}

// appPID returns the PID of app in c, which exactly one process must be.
func appPID(ctx context.Context, c *harness.Container, app string) (int, error) {
	ps, err := processes(ctx, c)
	if err != nil {
		return 0, err
	}
	var pids []int
	for i := range ps {
		if ps[i].isApp(app) {
			pids = append(pids, ps[i].PID)
		}
	}
	if len(pids) != 1 {
		return 0, fmt.Errorf("%d processes in %s are %s: %v", len(pids), c, app, pids)
	}
	return pids[0], nil
}

// waitNewPID waits until app in c runs as a PID other than old, for as
// long as a start may take, and returns it.
func waitNewPID(ctx context.Context, c *harness.Container, app string, old int) (int, error) {
	pid := 0
	err := harness.Poll(ctx, startWithin, healthEvery, func(ctx context.Context) error {
		got, err := appPID(ctx, c, app)
		switch {
		case err != nil:
			return harness.NotYet(err)
		case got == old:
			return harness.NotYet(fmt.Errorf("%s is still PID %d", app, old))
		}
		pid = got
		return nil
	})
	return pid, err
}

// environCode is Python that prints the names in the environment of the
// process sys.argv[1], as JSON, and never a value.
const environCode = `import json, sys
data = open(f"/proc/{sys.argv[1]}/environ", "rb").read()
print(json.dumps(sorted({e.split(b"=", 1)[0].decode(errors="replace") for e in data.split(b"\0") if e})))
`

// environNames returns the names in the environment of the process pid in
// c, and never their values.
func environNames(ctx context.Context, c *harness.Container, pid int) ([]string, error) {
	out, err := inPython(ctx, c, environCode, strconv.Itoa(pid))
	if err != nil {
		return nil, err
	}
	var names []string
	if err := json.Unmarshal([]byte(out), &names); err != nil {
		return nil, fmt.Errorf("reading the environment of PID %d: %w", pid, err)
	}
	return names, nil
}

// killCode is Python that sends SIGKILL to the process sys.argv[1].
const killCode = `import os, signal, sys
os.kill(int(sys.argv[1]), signal.SIGKILL)
`

// kill sends SIGKILL to the process pid in c, as a crash ends it.
func kill(tb testing.TB, c *harness.Container, pid int) {
	tb.Helper()
	if _, err := inPython(tb.Context(), c, killCode, strconv.Itoa(pid)); err != nil {
		tb.Fatalf("SIGKILL to PID %d: %v", pid, err)
	}
}

// fileCode is Python that prints True when sys.argv[1] is a file.
const fileCode = `import os, sys
print(os.path.isfile(sys.argv[1]))
`

// isFile reports whether path is a file in c, as the container's user
// sees it.
func isFile(ctx context.Context, c *harness.Container, path string) (bool, error) {
	out, err := inPython(ctx, c, fileCode, path)
	return strings.TrimSpace(out) == "True", err
}

// appState is one app's entry in PID 1's state file: whether it is up,
// and since when.
type appState struct {
	Up    bool      `json:"up"`
	Since time.Time `json:"since"`
}

// appStates reads PID 1's state file in c.
func appStates(ctx context.Context, c *harness.Container) (map[string]appState, error) {
	out, err := inPython(ctx, c, "import sys; sys.stdout.write(open(sys.argv[1]).read())", statePath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", statePath, err)
	}
	var states map[string]appState
	if err := json.Unmarshal([]byte(out), &states); err != nil {
		return nil, fmt.Errorf("reading %s: %w: %s", statePath, err, harness.Clip(out))
	}
	return states, nil
}

// probeHealth runs the shipped probe in c, and returns its exit status and
// what it printed.
func probeHealth(ctx context.Context, c *harness.Container) (code int, out string, err error) {
	res, err := c.Exec(ctx, nil, healthCmd...)
	if err != nil {
		return -1, "", err
	}
	return res.Code, strings.TrimSpace(string(res.Stdout) + string(res.Stderr)), nil
}

// waitHealthy runs the shipped probe in c every healthEvery until it
// passes, exit 0 printing nothing, for as long as a start may take.
func waitHealthy(ctx context.Context, c *harness.Container) error {
	return harness.Poll(ctx, startWithin, healthEvery, func(ctx context.Context) error {
		code, out, err := probeHealth(ctx, c)
		switch {
		case err != nil:
			return harness.NotYet(err)
		case code == 0 && out == "":
			return nil
		}
		return harness.NotYet(fmt.Errorf("the probe exits %d, printing %s", code, harness.Clip(out)))
	})
}

// waitLog waits until c's log holds a match for re, and returns the
// first, with its groups.
func waitLog(ctx context.Context, c *harness.Container, re *regexp.Regexp, within time.Duration) ([]string, error) {
	var m []string
	err := harness.Poll(ctx, within, healthEvery, func(ctx context.Context) error {
		logs, err := c.Logs(ctx)
		if err != nil {
			return harness.NotYet(err)
		}
		if m = re.FindStringSubmatch(logs); m == nil {
			return harness.NotYet(fmt.Errorf("no line in the log of %s matches %s", c, re))
		}
		return nil
	})
	return m, err
}

// stop stops c as a user does, with <engine> stop -t 10, and returns how
// long the stop took, and the container's exit status.
func stop(tb testing.TB, e harness.Engine, c *harness.Container) (took time.Duration, code int) {
	tb.Helper()
	start := time.Now()
	out, err := exec.CommandContext(tb.Context(), e.Name(), "stop", "-t", "10", c.ID).CombinedOutput() //nolint:gosec // G204: the engine and the container are the harness's own
	took = time.Since(start)
	if err != nil {
		tb.Fatalf("%s stop %s: %v: %s", e.Name(), c, err, out)
	}
	code, err = c.WaitExit(tb.Context(), time.Second)
	if err != nil {
		tb.Fatal(err)
	}
	return took, code
}

// md5Hex is s's MD5 sum, in hex.
func md5Hex(s string) string {
	sum := md5.Sum([]byte(s)) //nolint:gosec // G401: calibre-server's digest login and KOReader's sync key are MD5
	return hex.EncodeToString(sum[:])
}

// authParam is one name=value pair of a WWW-Authenticate challenge.
var authParam = regexp.MustCompile(`(\w+)=(?:"([^"]*)"|([^\s,]+))`)

// digestAnswer answers challenge, calibre-server's WWW-Authenticate, for
// method uri, as user with pw: RFC 7616 with MD5 and qop auth, which is
// what calibre-server asks for.
func digestAnswer(challenge, method, uri, user, pw string) (string, error) {
	scheme, rest, _ := strings.Cut(challenge, " ")
	if !strings.EqualFold(scheme, "Digest") {
		return "", fmt.Errorf("the challenge %q is no digest one", challenge)
	}
	p := map[string]string{}
	for _, m := range authParam.FindAllStringSubmatch(rest, -1) {
		p[strings.ToLower(m[1])] = m[2] + m[3]
	}
	qops := strings.Split(strings.ReplaceAll(p["qop"], " ", ""), ",")
	if p["nonce"] == "" || !slices.Contains(qops, "auth") {
		return "", fmt.Errorf("the challenge %q has no nonce, or no qop auth", challenge)
	}
	cnonce := make([]byte, 8)
	if _, err := rand.Read(cnonce); err != nil {
		return "", err
	}
	const nc = "00000001"
	ha1 := md5Hex(user + ":" + p["realm"] + ":" + pw)
	ha2 := md5Hex(method + ":" + uri)
	answer := md5Hex(strings.Join([]string{ha1, p["nonce"], nc, hex.EncodeToString(cnonce), "auth", ha2}, ":"))
	h := fmt.Sprintf(`Digest username=%q, realm=%q, nonce=%q, uri=%q, algorithm=MD5, qop=auth, nc=%s, cnonce=%q, response=%q`,
		user, p["realm"], p["nonce"], uri, nc, hex.EncodeToString(cnonce), answer)
	if opaque := p["opaque"]; opaque != "" {
		h += fmt.Sprintf(", opaque=%q", opaque)
	}
	return h, nil
}

// serverAdmin sends GET path to the Content Server, on s's port 8080, as
// admin with pw, by HTTP digest authentication, as calibre-server asks for
// it with --enable-auth: once without a login, to read the challenge, and
// again with the answer to it. An answer to the first that is not 401 is
// returned as it is.
func serverAdmin(ctx context.Context, s *harness.Serving, pw, path string) (harness.Response, error) {
	first, err := s.Get(ctx, path, nil)
	if err != nil || first.Status != http.StatusUnauthorized {
		return first, err
	}
	auth, err := digestAnswer(first.Header.Get("WWW-Authenticate"), http.MethodGet, path, admin, pw)
	if err != nil {
		return first, err
	}
	return s.Get(ctx, path, http.Header{"Authorization": {auth}})
}

// webSession is a browser's session with Calibre-Web, through port 8083:
// its cookies, and the CSRF token of the last page that carried one.
type webSession struct {
	s      *harness.Serving // the copy that sends to 8083
	client *http.Client
	token  string
}

// newWebSession starts a session with s's Calibre-Web, with no cookie yet.
func newWebSession(tb testing.TB, s *harness.Serving) *webSession {
	tb.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		tb.Fatal(err)
	}
	c := harness.HostClient()
	c.Jar = jar
	return &webSession{s: s.Health(), client: c}
}

// csrfField is the CSRF token in a page of Calibre-Web's.
var csrfField = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

// do sends one request, and keeps the CSRF token of a page that has one.
func (w *webSession) do(ctx context.Context, method, path string, header http.Header, body []byte) (harness.Response, error) {
	resp, err := w.s.Do(ctx, w.client, method, path, header, body)
	if err == nil {
		if m := csrfField.FindSubmatch(resp.Body); m != nil {
			w.token = html.UnescapeString(string(m[1]))
		}
	}
	return resp, err
}

// get sends GET path, with header.
func (w *webSession) get(ctx context.Context, path string, header http.Header) (harness.Response, error) {
	return w.do(ctx, http.MethodGet, path, header, nil)
}

// post sends form to path, with the session's CSRF token, as a browser
// posts a form, with header.
func (w *webSession) post(ctx context.Context, path string, form url.Values, header http.Header) (harness.Response, error) {
	f := maps.Clone(form)
	f.Set("csrf_token", w.token)
	h := header.Clone()
	if h == nil {
		h = http.Header{}
	}
	h.Set("Content-Type", "application/x-www-form-urlencoded")
	return w.do(ctx, http.MethodPost, path, h, []byte(f.Encode()))
}

// postJSON sends v to path as JSON, with the session's CSRF token in the
// header Calibre-Web's own pages send it in.
func (w *webSession) postJSON(ctx context.Context, path string, v any) (harness.Response, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return harness.Response{}, err
	}
	h := http.Header{"Content-Type": {"application/json"}, "X-CSRFToken": {w.token}}
	return w.do(ctx, http.MethodPost, path, h, b)
}

// login logs in as user with pw, through Calibre-Web's login page, with
// header on both of its requests. Calibre-Web answers 302 to a login that
// worked, and 401 to one that did not.
func (w *webSession) login(ctx context.Context, user, pw string, header http.Header) (harness.Response, error) {
	if _, err := w.get(ctx, "/login", header); err != nil {
		return harness.Response{}, err
	}
	return w.post(ctx, "/login", url.Values{"username": {user}, "password": {pw}, "next": {"/"}}, header)
}

// loginAdmin logs a new session in as admin with pw, and fails t unless
// the login works.
func loginAdmin(tb testing.TB, s *harness.Serving, pw string) *webSession {
	tb.Helper()
	w := newWebSession(tb, s)
	resp, err := w.login(tb.Context(), admin, pw, nil)
	if err != nil || resp.Status != http.StatusFound {
		tb.Fatalf("Calibre-Web's admin login: %v, status %d: %s", err, resp.Status, harness.Clip(string(resp.Body)))
	}
	return w
}

// editTitle sets the title of book id through Calibre-Web, as its book
// list's own edit does, which writes the library's metadata.db.
func editTitle(tb testing.TB, w *webSession, id int, title string) {
	tb.Helper()
	ctx := tb.Context()
	if _, err := w.get(ctx, "/", nil); err != nil {
		tb.Fatal(err)
	}
	resp, err := w.postJSON(ctx, "/ajax/editbooks/title", map[string]any{"pk": []int{id}, "value": title})
	if err != nil || resp.Status != http.StatusOK || !bytes.Contains(resp.Body, []byte(`"success":true`)) {
		tb.Fatalf("editing book %d's title in Calibre-Web: %v, status %d: %s", id, err, resp.Status, harness.Clip(string(resp.Body)))
	}
}

// The parts of an HTML form that formValues reads.
var (
	formBlock = regexp.MustCompile(`(?is)<form\b[^>]*>(.*?)</form>`)
	control   = regexp.MustCompile(`(?is)<(input|select|/select|option)\b([^>]*)>`)
	attribute = regexp.MustCompile(`([^\s=/]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+)))?`)
)

// formValues returns what a browser sends for the form in page that has a
// field named field: each enabled field's value, each checked checkbox's,
// and each select's selected option, or its first. Calibre-Web saves a
// whole settings page at once, and takes a checkbox left out for one
// turned off, so a test changes a setting by sending every field as the
// page shows it, with that one changed.
func formValues(page []byte, field string) (url.Values, error) {
	for _, m := range formBlock.FindAllSubmatch(page, -1) {
		if bytes.Contains(m[1], []byte(`name="`+field+`"`)) {
			return controls(string(m[1])), nil
		}
	}
	return nil, fmt.Errorf("the page has no form with a field named %s", field)
}

// controls reads the fields of one form's body.
func controls(form string) url.Values {
	v := url.Values{}
	var (
		sel           string // the select being read, or ""
		first, chosen string
		seen, picked  bool
	)
	for _, m := range control.FindAllStringSubmatch(form, -1) {
		a := attributes(m[2])
		_, disabled := a["disabled"]
		switch strings.ToLower(m[1]) {
		case "input":
			name := a["name"]
			if name == "" || disabled {
				continue
			}
			switch strings.ToLower(a["type"]) {
			case "checkbox", "radio":
				if _, on := a["checked"]; on {
					value, ok := a["value"]
					if !ok {
						value = "on"
					}
					v.Add(name, value)
				}
			case "submit", "button", "reset", "image", "file":
			default:
				v.Add(name, a["value"])
			}
		case "select":
			sel, first, chosen, seen, picked = a["name"], "", "", false, false
			if disabled {
				sel = ""
			}
		case "option":
			if sel == "" {
				continue
			}
			if !seen {
				first, seen = a["value"], true
			}
			if _, on := a["selected"]; on && !picked {
				chosen, picked = a["value"], true
			}
		case "/select":
			switch {
			case sel == "" || !seen:
			case picked:
				v.Add(sel, chosen)
			default:
				v.Add(sel, first)
			}
			sel = ""
		}
	}
	return v
}

// attributes reads a tag's attributes, each value unescaped. One with no
// value, such as checked, is there with an empty one.
func attributes(s string) map[string]string {
	a := map[string]string{}
	for _, m := range attribute.FindAllStringSubmatch(s, -1) {
		a[strings.ToLower(m[1])] = html.UnescapeString(m[2] + m[3] + m[4])
	}
	return a
}

// none is what a check shows for an empty list.
func none(items []string) string {
	if len(items) == 0 {
		return "none"
	}
	return strings.Join(items, ", ")
}
