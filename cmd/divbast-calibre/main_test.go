// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The password variables, all four.
var passwordNames = []string{
	"CALIBRE_ADMIN_PASSWORD", "CALIBRE_ADMIN_PASSWORD_FILE",
	"CALIBRE_WEB_ADMIN_PASSWORD", "CALIBRE_WEB_ADMIN_PASSWORD_FILE",
}

// Passwords that meet Calibre-Web's default rules.
const (
	adminPW = "Calibre-Adm1n!"
	webPW   = "W3b-Admin-pass!"
)

// helpers is a Runner standing in for calibredb and the two helpers: it
// records each command, creates the file the real one would, and fails
// the command named fail.
type helpers struct {
	t     *testing.T
	fail  string
	calls []struct{ argv, env []string }
}

func (h *helpers) run(_ context.Context, argv, env []string, stdin []byte) ([]byte, error) {
	h.calls = append(h.calls, struct{ argv, env []string }{slices.Clone(argv), slices.Clone(env)})
	if argv[0] == h.fail {
		return nil, errors.New("exit status 1")
	}
	var req struct{ Userdb, Appdb string }
	if len(stdin) > 0 {
		if err := json.Unmarshal(stdin, &req); err != nil {
			h.t.Errorf("stdin of %q is not JSON: %v", argv, err)
		}
	}
	var path string
	switch argv[0] {
	case "/opt/calibre/calibredb":
		path = filepath.Join(argv[2], "metadata.db")
	case "/opt/calibre/calibre-debug":
		path = req.Userdb
	case "/app/venv/bin/python":
		path = req.Appdb
	default:
		h.t.Errorf("ran %q, which first start never runs", argv)
		return nil, errors.New("unknown command")
	}
	return nil, os.WriteFile(path, nil, 0o600)
}

// argv0s is the commands run, by their paths, in order.
func (h *helpers) argv0s() []string {
	out := make([]string, 0, len(h.calls))
	for _, c := range h.calls {
		out = append(out, c.argv[0])
	}
	return out
}

// newLayout is an empty /config and /books.
func newLayout(t *testing.T) layout {
	t.Helper()
	return layout{config: t.TempDir(), books: t.TempDir()}
}

// secretFile writes a password file, as Docker's secrets are, with a
// newline at its end.
func secretFile(t *testing.T, pw string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(pw+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestServeOrder is the first start on an empty /config and /books, with
// the passwords given each way: calibredb, then Calibre's helper, then
// Calibre-Web's. No command's environment holds a password variable, and
// no argv holds a password.
func TestServeOrder(t *testing.T) {
	t.Parallel()
	for name, environ := range map[string]func(t *testing.T) []string{
		"admin's in the variable, Calibre-Web's in a file": func(t *testing.T) []string {
			t.Helper()
			return []string{"PATH=/usr/bin", "CALIBRE_ADMIN_PASSWORD=" + adminPW, "CALIBRE_WEB_ADMIN_PASSWORD_FILE=" + secretFile(t, webPW)}
		},
		"admin's in a file, Calibre-Web's in the variable": func(t *testing.T) []string {
			t.Helper()
			return []string{"PATH=/usr/bin", "CALIBRE_ADMIN_PASSWORD_FILE=" + secretFile(t, adminPW), "CALIBRE_WEB_ADMIN_PASSWORD=" + webPW}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			l := newLayout(t)
			h := &helpers{t: t}
			var stdout bytes.Buffer
			if err := firstStart(t.Context(), environ(t), h.run, l, &stdout); err != nil {
				t.Fatalf("firstStart() = %v", err)
			}
			want := []string{"/opt/calibre/calibredb", "/opt/calibre/calibre-debug", "/app/venv/bin/python"}
			if got := h.argv0s(); !slices.Equal(got, want) {
				t.Errorf("first start ran %q, want %q", got, want)
			}
			for _, c := range h.calls {
				for _, e := range c.env {
					name, _, _ := strings.Cut(e, "=")
					if slices.Contains(passwordNames, name) {
						t.Errorf("%s ran with %s in its environment", c.argv[0], name)
					}
				}
				if !slices.Contains(c.env, "PATH=/usr/bin") {
					t.Errorf("%s ran with %q, want PID 1's environment but the passwords", c.argv[0], c.env)
				}
				for _, a := range c.argv {
					if strings.Contains(a, adminPW) || strings.Contains(a, webPW) {
						t.Errorf("argv %q holds a password", c.argv)
					}
				}
			}
			for _, dir := range []string{filepath.Join(l.config, "calibre"), filepath.Join(l.config, "calibre-web")} {
				if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
					t.Errorf("%s = %v, %v; want a directory of mode 0700", dir, fi, err)
				}
			}
			for _, f := range []string{
				filepath.Join(l.books, "metadata.db"),
				filepath.Join(l.config, "calibre", "users.sqlite"),
				filepath.Join(l.config, "calibre-web", "app.db"),
			} {
				if _, err := os.Stat(f); err != nil {
					t.Errorf("first start left no %s: %v", f, err)
				}
			}
			if lines := strings.Count(stdout.String(), "\n"); lines != 3 {
				t.Errorf("stdout = %q, want a line for each thing created", stdout.String())
			}
		})
	}
}

// TestServeOrderStops is each reason the first start stops before it
// runs anything: a missing, doubled or weak password, for an account
// still to be created.
func TestServeOrderStops(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		environ []string
		errHas  []string
	}{
		{name: "no admin password", environ: []string{"CALIBRE_WEB_ADMIN_PASSWORD=" + webPW}, errHas: []string{"CALIBRE_ADMIN_PASSWORD"}},
		{name: "no Calibre-Web password", environ: []string{"CALIBRE_ADMIN_PASSWORD=" + adminPW}, errHas: []string{"CALIBRE_WEB_ADMIN_PASSWORD"}},
		{
			name:    "both of a pair",
			environ: []string{"CALIBRE_ADMIN_PASSWORD=" + adminPW, "CALIBRE_ADMIN_PASSWORD_FILE=/run/secrets/pw", "CALIBRE_WEB_ADMIN_PASSWORD=" + webPW},
			errHas:  []string{"CALIBRE_ADMIN_PASSWORD", "CALIBRE_ADMIN_PASSWORD_FILE"},
		},
		{name: "a weak admin password", environ: []string{"CALIBRE_ADMIN_PASSWORD=weakweak", "CALIBRE_WEB_ADMIN_PASSWORD=" + webPW}, errHas: []string{"CALIBRE_ADMIN_PASSWORD", "digit"}},
		{name: "a weak Calibre-Web password", environ: []string{"CALIBRE_ADMIN_PASSWORD=" + adminPW, "CALIBRE_WEB_ADMIN_PASSWORD=Weakweak1"}, errHas: []string{"CALIBRE_WEB_ADMIN_PASSWORD", "not a letter"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			l := newLayout(t)
			h := &helpers{t: t}
			err := firstStart(t.Context(), tt.environ, h.run, l, new(bytes.Buffer))
			if err == nil {
				t.Fatal("firstStart() = nil, want it stopped")
			}
			for _, s := range tt.errHas {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("firstStart() = %q, want it to name %q", err, s)
				}
			}
			for _, s := range []string{adminPW, webPW, "weakweak", "Weakweak1"} {
				if strings.Contains(err.Error(), s) {
					t.Errorf("firstStart() = %q, which holds a password", err)
				}
			}
			if len(h.calls) != 0 {
				t.Errorf("first start ran %q before it stopped", h.argv0s())
			}
			if names, _ := os.ReadDir(l.config); len(names) != 0 {
				t.Errorf("first start left %v in /config", names)
			}
		})
	}
}

// TestServeOrderExisting is /config and /books from an earlier start: no
// password is needed, none is read, even a bad one, and nothing runs. With
// Calibre-Web's account alone still to create, only its password is
// needed.
func TestServeOrderExisting(t *testing.T) {
	t.Parallel()
	l := newLayout(t)
	for _, dir := range []string{filepath.Join(l.config, "calibre"), filepath.Join(l.config, "calibre-web")} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{filepath.Join(l.books, "metadata.db"), filepath.Join(l.config, "calibre", "users.sqlite")} {
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h := &helpers{t: t}
	environ := []string{"CALIBRE_ADMIN_PASSWORD=weak", "CALIBRE_ADMIN_PASSWORD_FILE=/nowhere", "CALIBRE_WEB_ADMIN_PASSWORD=" + webPW}
	if err := firstStart(t.Context(), environ, h.run, l, new(bytes.Buffer)); err != nil {
		t.Fatalf("firstStart() = %v", err)
	}
	if got := h.argv0s(); !slices.Equal(got, []string{"/app/venv/bin/python"}) {
		t.Errorf("first start ran %q, want Calibre-Web's helper alone", got)
	}
	if err := firstStart(t.Context(), nil, h.run, l, new(bytes.Buffer)); err != nil || len(h.calls) != 1 {
		t.Errorf("firstStart() with everything there = %v after %q; want nothing run", err, h.argv0s())
	}
}

// TestServeOrderHelperFails is a helper failing: the start stops there,
// naming what it could not do, and runs nothing after it.
func TestServeOrderHelperFails(t *testing.T) {
	t.Parallel()
	l := newLayout(t)
	h := &helpers{t: t, fail: "/opt/calibre/calibredb"}
	environ := []string{"CALIBRE_ADMIN_PASSWORD=" + adminPW, "CALIBRE_WEB_ADMIN_PASSWORD=" + webPW}
	err := firstStart(t.Context(), environ, h.run, l, new(bytes.Buffer))
	if err == nil || !strings.Contains(err.Error(), l.books) {
		t.Errorf("firstStart() = %v, want an error naming %s", err, l.books)
	}
	if got := h.argv0s(); !slices.Equal(got, []string{"/opt/calibre/calibredb"}) {
		t.Errorf("first start ran %q, want it to stop after calibredb", got)
	}
}

// TestRefusesUnwritable is /config or /books that uid 65532 cannot write,
// as a library from a desktop Calibre owned by uid 1000 is: the start
// stops before it reads a password or runs anything, naming the directory
// and saying how to give it to the user.
func TestRefusesUnwritable(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root writes past every mode")
	}
	for _, which := range []string{"config", "books"} {
		t.Run(which, func(t *testing.T) {
			t.Parallel()
			l := newLayout(t)
			dir := l.config
			if which == "books" {
				dir = l.books
			}
			if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // G302: a directory the test makes read-only
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // G302: so the test's cleanup can remove it
			h := &helpers{t: t}
			err := firstStart(t.Context(), nil, h.run, l, new(bytes.Buffer))
			if err == nil || !strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), "chown -R") {
				t.Errorf("firstStart() = %v, want an error naming %s and saying chown -R", err, dir)
			}
			if len(h.calls) != 0 {
				t.Errorf("first start ran %q", h.argv0s())
			}
		})
	}
}

// TestUnsetPasswords is what keeps the passwords from the apps PID 1
// starts after first start: they inherit its environment, which then
// holds none of the four variables.
func TestUnsetPasswords(t *testing.T) {
	for _, name := range passwordNames {
		t.Setenv(name, "s3cret")
	}
	t.Setenv("DIVBAST_TEST_KEPT", "kept")
	if err := unsetPasswords(); err != nil {
		t.Fatal(err)
	}
	for _, name := range passwordNames {
		if v, ok := os.LookupEnv(name); ok {
			t.Errorf("%s = %q after unsetPasswords(), want it unset", name, v)
		}
	}
	if os.Getenv("DIVBAST_TEST_KEPT") != "kept" {
		t.Error("unsetPasswords() unset another variable")
	}
}
