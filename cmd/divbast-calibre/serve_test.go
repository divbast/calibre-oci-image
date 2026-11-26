// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
	env := withoutPasswords([]string{"PATH=/usr/bin", "CALIBRE_ADMIN_PASSWORD=x", "CALIBRE_ADMIN_PASSWORD_FILE=y", "CALIBRE_WEB_ADMIN_PASSWORD=z", "CALIBRE_WEB_ADMIN_PASSWORD_FILE=w", "HOME=/tmp"})
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
	got := children(image, env)
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
		if names := envNames(c.Env); !slices.Equal(names, []string{"PATH", "HOME"}) {
			t.Errorf("%s's environment holds %q, want PATH and HOME alone", c.Name, names)
		}
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
// no password, and its output trimmed.
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
		return nil, errors.New("exit status 1")
	}, nil, image)
	if _, err := failing(t.Context()); err == nil {
		t.Error("a failed read gave no error")
	}
}

// fakeEvents records what the hooks tell the Coordinator.
type fakeEvents struct {
	mu       sync.Mutex
	started  []string
	gated    []string
	answered []string
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
	cs, cw := hooks(f, reload.ContentServer, &log), hooks(f, reload.CalibreWeb, &log)
	if cw.Gate != nil || cw.Observe != nil {
		t.Error("Calibre-Web's port gates requests, or reads answers")
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
	accepting chan reload.App
}

func (f *fakeStarts) Started(app reload.App) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, app)
}

func (f *fakeStarts) Accepting(app reload.App) { f.accepting <- app }

// TestTracker is the state file and the starts: the file holds both apps
// down at first, and each change after; each start, and only a start, is
// told, and then that the app's port accepts.
func TestTracker(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "divbast-calibre", "state.json")
	f := &fakeStarts{accepting: make(chan reload.App, 4)}
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
	for range 3 {
		select {
		case <-f.accepting:
		case <-time.After(10 * time.Second):
			t.Fatal("the tracker did not tell the Coordinator that a port accepts")
		}
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
