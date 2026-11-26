// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package probe

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/divbast/calibre-oci-image/internal/supervise"
)

// TestDigestResponse is the example RFC 2617 gives in its section 3.5.
func TestDigestResponse(t *testing.T) {
	t.Parallel()
	c := challenge{realm: "testrealm@host.com", nonce: "dcd98b7102dd2f0e8b11d0f600bfb0c093"}
	got := c.response("Mufasa", "Circle Of Life", "GET", "/dir/index.html", "00000001", "0a4f113b")
	if want := "6629fae49393a05397450978507c4ef1"; got != want {
		t.Errorf("response = %q, want %q", got, want)
	}
}

const (
	testUser     = "calibre-web"
	testPassword = "pw-for-the-test"
	testVersion  = "9.19.0"
)

// contentServer is a server that asks for Digest login as Calibre's does,
// and answers GET /ajax/library-info for testUser alone. It sends the
// header Server: serverHeader with the answer, when that is not empty.
func contentServer(t *testing.T, serverHeader string) string {
	t.Helper()
	c := challenge{realm: "calibre", nonce: "41064d50dcc972580007:4546fe60"}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ajax/library-info" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		a := r.Header.Get("Authorization")
		if !strings.HasPrefix(a, "Digest ") {
			// Basic, as Calibre answers it: it takes no other login
			if a != "" {
				http.Error(w, "Unsupported authentication method", http.StatusBadRequest)
				return
			}
			w.Header().Set("WWW-Authenticate", `Digest realm="calibre", nonce="`+c.nonce+`", algorithm="MD5", qop="auth"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		p := parseParams(strings.TrimPrefix(a, "Digest "))
		want := c.response(testUser, testPassword, r.Method, r.RequestURI, p["nc"], p["cnonce"])
		if p["username"] != testUser || p["realm"] != "calibre" || p["nonce"] != c.nonce || p["uri"] != r.RequestURI ||
			p["qop"] != "auth" || p["response"] != want {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if serverHeader != "" {
			w.Header().Set("Server", serverHeader)
		}
		_, _ = io.WriteString(w, `{"library_map": {"books": "books"}, "default_library": "books"}`)
	}))
	t.Cleanup(s.Close)
	return s.URL
}

// calibreWeb is a server that answers GET /login with status and body.
func calibreWeb(t *testing.T, status int, body string) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/login" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(s.Close)
	return s.URL
}

const loginPage = "<!DOCTYPE html>\n<html><head><title>Calibre-Web | Login</title></head></html>\n"

// closedURL is a URL nothing listens on.
func closedURL(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return "http://" + addr
}

// both is the proc of a container whose ports 8080 and 8083 are listened
// on, on every address.
func both(t *testing.T) string {
	t.Helper()
	return proc(t, "", new(tcp6Line(anyV6, "1F90", "0A")+tcp6Line(anyV6, "1F93", "0A")))
}

// now is the clock of the tests.
var now = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

type states = map[string]supervise.State

func up() supervise.State { return supervise.State{Up: true, Since: now.Add(-time.Hour)} }

// down is a child that has been down, in a planned restart or not, for d.
func down(planned bool, d time.Duration) supervise.State {
	return supervise.State{Planned: planned, Since: now.Add(-d)}
}

func state(t *testing.T, s states) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "state.json")
	if err := supervise.WriteState(path, s); err != nil {
		t.Fatal(err)
	}
	return path
}

func password(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "internal-password")
	if err := os.WriteFile(path, []byte(testPassword), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHealth(t *testing.T) {
	t.Parallel()
	good := states{"calibre-server": up(), "calibre-web": up()}
	ok := func(t *testing.T) Options {
		t.Helper()
		return Options{
			State: state(t, good), ServerBase: contentServer(t, "calibre "+testVersion), WebBase: calibreWeb(t, http.StatusOK, loginPage),
			PasswordFile: password(t), Version: testVersion, Within: time.Second, PlannedLimit: 2 * time.Minute,
			Ports: []int{8080, 8083}, Proc: both(t), Now: func() time.Time { return now },
		}
	}
	tests := []struct {
		name  string
		edit  func(t *testing.T, o *Options)
		lines []string // each line must start so; there are no more
	}{
		{name: "healthy", edit: func(*testing.T, *Options) {}},
		{
			name: "no state file",
			edit: func(t *testing.T, o *Options) {
				t.Helper()
				o.State = filepath.Join(t.TempDir(), "state.json")
			},
			lines: []string{"state: "},
		},
		{
			name: "state file is not JSON",
			edit: func(t *testing.T, o *Options) {
				t.Helper()
				o.State = filepath.Join(t.TempDir(), "s.json")
				_ = os.WriteFile(o.State, []byte("{"), 0o600)
			},
			lines: []string{"state: "},
		},
		{
			name: "calibre-server down",
			edit: func(t *testing.T, o *Options) {
				t.Helper()
				o.State = state(t, states{"calibre-server": down(false, time.Second), "calibre-web": up()})
			},
			lines: []string{"calibre-server: not running"},
		},
		{
			name: "calibre-web down",
			edit: func(t *testing.T, o *Options) {
				t.Helper()
				o.State = state(t, states{"calibre-server": up(), "calibre-web": down(false, time.Hour)})
			},
			lines: []string{"calibre-web: not running"},
		},
		{
			name: "neither in the state file",
			edit: func(t *testing.T, o *Options) {
				t.Helper()
				o.State = state(t, states{})
			},
			lines: []string{"calibre-server: not running", "calibre-web: not running"},
		},
		{
			name: "planned restart down for 1 minute",
			edit: func(t *testing.T, o *Options) {
				t.Helper()
				o.State = state(t, states{"calibre-server": down(true, time.Minute), "calibre-web": up()})
			},
		},
		{
			name: "planned restart, still stopping",
			edit: func(t *testing.T, o *Options) {
				t.Helper()
				o.State = state(t, states{"calibre-server": {Up: true, Planned: true, Since: now.Add(-5 * time.Second)}, "calibre-web": up()})
			},
		},
		{
			name: "planned restart down for 1m59s",
			edit: func(t *testing.T, o *Options) {
				t.Helper()
				o.State = state(t, states{"calibre-server": down(true, 119*time.Second), "calibre-web": up()})
			},
		},
		{
			name: "planned restart down for 2 minutes",
			edit: func(t *testing.T, o *Options) {
				t.Helper()
				o.State = state(t, states{"calibre-server": down(true, 2*time.Minute), "calibre-web": up()})
			},
			lines: []string{"calibre-server: restarting for 2m0s, which is not under the 2m0s allowed"},
		},
		{
			name: "planned restart down for 2.5 minutes",
			edit: func(t *testing.T, o *Options) {
				t.Helper()
				o.State = state(t, states{"calibre-server": down(true, 150*time.Second), "calibre-web": up()})
			},
			lines: []string{"calibre-server: restarting for 2m30s, which is not under the 2m0s allowed"},
		},
		{
			name: "calibre-web planned for 3 minutes",
			edit: func(t *testing.T, o *Options) {
				t.Helper()
				o.State = state(t, states{"calibre-server": up(), "calibre-web": down(true, 3*time.Minute)})
			},
			lines: []string{"calibre-web: restarting for 3m0s"},
		},
		{
			name: "calibre-server of another version",
			edit: func(t *testing.T, o *Options) {
				t.Helper()
				o.ServerBase = contentServer(t, "calibre 9.18.0")
			},
			lines: []string{`calibre-server: its Server header is "calibre 9.18.0", want "calibre 9.19.0"`},
		},
		{
			name: "calibre-server names no version",
			edit: func(t *testing.T, o *Options) {
				t.Helper()
				o.ServerBase = contentServer(t, "")
			},
			lines: []string{`calibre-server: its Server header is ""`},
		},
		{
			name: "calibre-server refuses the password",
			edit: func(t *testing.T, o *Options) {
				t.Helper()
				p := filepath.Join(t.TempDir(), "p")
				_ = os.WriteFile(p, []byte("wrong"), 0o600)
				o.PasswordFile = p
			},
			lines: []string{"calibre-server: GET /ajax/library-info answered 401, want 200"},
		},
		{
			name: "no password file",
			edit: func(t *testing.T, o *Options) {
				t.Helper()
				o.PasswordFile = filepath.Join(t.TempDir(), "none")
			},
			lines: []string{"calibre-server: "},
		},
		{
			name: "calibre-server unreachable",
			edit: func(t *testing.T, o *Options) {
				t.Helper()
				o.ServerBase = closedURL(t)
			},
			lines: []string{"calibre-server: "},
		},
		{
			name: "calibre-web answers 500",
			edit: func(t *testing.T, o *Options) {
				t.Helper()
				o.WebBase = calibreWeb(t, http.StatusInternalServerError, loginPage)
			},
			lines: []string{"calibre-web: GET /login answered 500, want 200"},
		},
		{
			name: "calibre-web unreachable",
			edit: func(t *testing.T, o *Options) {
				t.Helper()
				o.WebBase = closedURL(t)
			},
			lines: []string{"calibre-web: "},
		},
		{
			name: "port 8083 not listened on",
			edit: func(t *testing.T, o *Options) {
				t.Helper()
				o.Proc = proc(t, "", new(tcp6Line(anyV6, "1F90", "0A")))
			},
			lines: []string{"listen: nothing listens on port 8083"},
		},
		{
			name: "no port listened on",
			edit: func(t *testing.T, o *Options) {
				t.Helper()
				o.Proc = proc(t, "", new(""))
			},
			lines: []string{"listen: nothing listens on port 8080", "listen: nothing listens on port 8083"},
		},
		{
			name: "everything fails",
			edit: func(t *testing.T, o *Options) {
				t.Helper()
				o.State = filepath.Join(t.TempDir(), "state.json")
				o.Proc = proc(t, "", new(""))
			},
			lines: []string{"state: ", "listen: nothing listens on port 8080", "listen: nothing listens on port 8083"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := ok(t)
			tt.edit(t, &o)
			var stderr bytes.Buffer
			code := Probe(t.Context(), &o, &stderr)
			want := 0
			if len(tt.lines) > 0 {
				want = 1
			}
			if code != want {
				t.Errorf("Probe() = %d, want %d; stderr %q", code, want, stderr.String())
			}
			var lines []string
			if stderr.Len() > 0 {
				lines = strings.Split(strings.TrimSuffix(stderr.String(), "\n"), "\n")
			}
			if len(lines) != len(tt.lines) {
				t.Fatalf("Probe() printed %q, want one line for each of %q", lines, tt.lines)
			}
			for i, w := range tt.lines {
				if !strings.HasPrefix(lines[i], w) {
					t.Errorf("line %d = %q, want it to start %q", i+1, lines[i], w)
				}
			}
		})
	}
}

// TestHealthWithin is an app that never answers: health gives up on it
// after Within, and says so.
func TestHealthWithin(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(release) })
	o := Options{
		State: state(t, states{"calibre-server": up(), "calibre-web": up()}), ServerBase: slow.URL, WebBase: slow.URL,
		PasswordFile: password(t), Version: testVersion, Within: 200 * time.Millisecond, PlannedLimit: time.Minute,
		Now: func() time.Time { return now },
	}
	var stderr bytes.Buffer
	start := time.Now()
	code := Probe(t.Context(), &o, &stderr)
	if took := time.Since(start); code != 1 || took > 5*time.Second {
		t.Errorf("Probe() = %d in %v, want 1 within 2 x Within and a margin", code, took)
	}
	if lines := strings.Split(strings.TrimSuffix(stderr.String(), "\n"), "\n"); len(lines) != 2 ||
		!strings.HasPrefix(lines[0], "calibre-server: ") || !strings.HasPrefix(lines[1], "calibre-web: ") {
		t.Errorf("Probe() printed %q, want one line for each app", stderr.String())
	}
}

func TestParseParams(t *testing.T) {
	t.Parallel()
	got := parseParams(`realm="calibre", nonce="41064d:4546", algorithm="MD5", qop="auth", opaque=abc`)
	want := map[string]string{"realm": "calibre", "nonce": "41064d:4546", "algorithm": "MD5", "qop": "auth", "opaque": "abc"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("parseParams = %v, want %v", got, want)
	}
}

func TestChallengeRefused(t *testing.T) {
	t.Parallel()
	for _, h := range []string{
		`Basic realm="x"`,
		`Digest realm="calibre", nonce="n", algorithm="SHA-256", qop="auth"`,
		`Digest realm="calibre", nonce="n", algorithm="MD5", qop="auth-int"`,
		`Digest realm="calibre", algorithm="MD5", qop="auth"`,
	} {
		if _, err := parseChallenge(h); err == nil {
			t.Errorf("parseChallenge(%q) = nil error, want one", h)
		}
	}
	if _, err := parseChallenge(`Digest realm="calibre", nonce="n", algorithm="MD5", qop="auth"`); err != nil {
		t.Errorf("parseChallenge of Calibre's own = %v, want none", err)
	}
}
