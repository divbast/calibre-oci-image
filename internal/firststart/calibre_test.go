// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package firststart

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// sentUsers is what calibre_users.py reads from stdin.
type sentUsers struct {
	Userdb string `json:"userdb"`
	Users  []struct {
		Name     string `json:"name"`
		Password string `json:"password"`
		Readonly bool   `json:"readonly"`
	} `json:"users"`
}

// fakeUsers is calibre_users.py: it reads the request, checks the
// internal user's password file is in place, and creates the database the
// request names.
func fakeUsers(t *testing.T, dir string) func(call) ([]byte, error) {
	t.Helper()
	return func(c call) ([]byte, error) {
		var req sentUsers
		if err := json.Unmarshal(c.stdin, &req); err != nil {
			t.Errorf("stdin %q is not the request: %v", c.stdin, err)
			return nil, err
		}
		if !within(dir, req.Userdb) {
			t.Errorf("userdb %s is not in %s", req.Userdb, dir)
		}
		pw, err := os.ReadFile(filepath.Join(dir, "internal-password")) //nolint:gosec // G304: the test's own temporary file
		if err != nil || len(req.Users) != 2 || string(pw) != req.Users[1].Password {
			t.Errorf("internal-password = %q, %v when the helper ran; want the internal user's password", pw, err)
		}
		touch(t, req.Userdb)
		return nil, nil
	}
}

// TestEnsureCalibreUsers creates users.sqlite once, holding admin and the
// internal read-only user, whose password is in internal-password, mode
// 0600. Every password goes on stdin, none in argv.
func TestEnsureCalibreUsers(t *testing.T) {
	t.Parallel()
	const adminPW = "Adm1n-pass!"
	dir := t.TempDir()
	key := bytes.Repeat([]byte{0xfb}, 32)
	internal := base64.RawURLEncoding.EncodeToString(key) // holds - and _, base64url's own
	r := &recorder{do: fakeUsers(t, dir)}

	created, err := EnsureCalibreUsers(t.Context(), r.run, dir, adminPW, bytes.NewReader(key))
	if err != nil || !created {
		t.Fatalf("EnsureCalibreUsers() = %t, %v; want the users created", created, err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("EnsureCalibreUsers() ran %d commands, want 1", len(r.calls))
	}
	c := r.calls[0]
	want := []string{"/opt/calibre/calibre-debug", "-e", "/usr/local/share/divbast-calibre/calibre_users.py"}
	if !slices.Equal(c.argv, want) {
		t.Errorf("argv = %q, want %q", c.argv, want)
	}
	noSecretIn(t, c.argv, adminPW, internal)
	var req sentUsers
	if uerr := json.Unmarshal(c.stdin, &req); uerr != nil {
		t.Fatal(uerr)
	}
	type user = struct {
		name, password string
		readonly       bool
	}
	got := make([]user, 0, len(req.Users))
	for _, u := range req.Users {
		got = append(got, user{u.Name, u.Password, u.Readonly})
	}
	if wantUsers := []user{{"admin", adminPW, false}, {"calibre-web", internal, true}}; !slices.Equal(got, wantUsers) {
		t.Errorf("stdin's users = %+v, want %+v", got, wantUsers)
	}

	pwFile := filepath.Join(dir, "internal-password")
	if fi, serr := os.Stat(pwFile); serr != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("internal-password's mode = %v, %v; want 0600", fi.Mode().Perm(), serr)
	}
	if data, rerr := os.ReadFile(pwFile); rerr != nil || string(data) != internal { //nolint:gosec // G304: the test's own temporary file
		t.Errorf("internal-password = %q, %v; want %q", data, rerr, internal)
	}
	if names := entries(t, dir); !slices.Equal(names, []string{"internal-password", "users.sqlite"}) {
		t.Errorf("the directory holds %q, want internal-password and users.sqlite alone", names)
	}

	// Once users.sqlite is there, nothing runs and nothing changes
	created, err = EnsureCalibreUsers(t.Context(), r.run, dir, adminPW, bytes.NewReader(key))
	if err != nil || created || len(r.calls) != 1 {
		t.Errorf("EnsureCalibreUsers() again = %t, %v after %d runs; want nothing run", created, err, len(r.calls))
	}
}

// TestEnsureCalibreUsersExisting is a users.sqlite already there: no run,
// and no password file, whatever the password.
func TestEnsureCalibreUsersExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "users.sqlite"))
	r := &recorder{}
	created, err := EnsureCalibreUsers(t.Context(), r.run, dir, "", bytes.NewReader(make([]byte, 32)))
	if err != nil || created || len(r.calls) != 0 {
		t.Errorf("EnsureCalibreUsers() = %t, %v after %d runs; want nothing run", created, err, len(r.calls))
	}
	if names := entries(t, dir); !slices.Equal(names, []string{"users.sqlite"}) {
		t.Errorf("the directory holds %q, want users.sqlite alone", names)
	}
}

// TestEnsureCalibreUsersFails is the helper failing, or creating no
// database: the start stops, and leaves no users.sqlite, no password file
// and no temporary file, so the next start tries again.
func TestEnsureCalibreUsersFails(t *testing.T) {
	t.Parallel()
	for name, fails := range map[string]func(dir string) func(call) ([]byte, error){
		"the helper fails": func(string) func(call) ([]byte, error) {
			return func(c call) ([]byte, error) {
				var req sentUsers
				if err := json.Unmarshal(c.stdin, &req); err != nil {
					return nil, err
				}
				// what Calibre's helper leaves when a user is refused
				touch(t, req.Userdb)
				touch(t, req.Userdb+"-journal")
				return nil, errors.New("exit status 1")
			}
		},
		"the helper creates no database": func(string) func(call) ([]byte, error) {
			return func(call) ([]byte, error) { return nil, nil }
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			r := &recorder{do: fails(dir)}
			created, err := EnsureCalibreUsers(t.Context(), r.run, dir, "Adm1n-pass!", bytes.NewReader(make([]byte, 32)))
			if err == nil || created || !strings.Contains(err.Error(), dir) {
				t.Errorf("EnsureCalibreUsers() = %t, %v; want an error naming %s", created, err, dir)
			}
			if names := entries(t, dir); len(names) != 0 {
				t.Errorf("a failed start left %q", names)
			}
		})
	}
}

// TestEnsureCalibreUsersStale is an internal-password left by a start that
// stopped before users.sqlite was made: no user has that password, so a
// new one replaces it.
func TestEnsureCalibreUsersStale(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "internal-password"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &recorder{do: fakeUsers(t, dir)}
	created, err := EnsureCalibreUsers(t.Context(), r.run, dir, "Adm1n-pass!", bytes.NewReader(make([]byte, 32)))
	if err != nil || !created {
		t.Fatalf("EnsureCalibreUsers() = %t, %v; want the users created", created, err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "internal-password")); err != nil || string(data) == "stale" { //nolint:gosec // G304: the test's own temporary file
		t.Errorf("internal-password = %q, %v; want a new password", data, err)
	}
}

// TestEnsureCalibreUsersNoRandom is a random source that fails: nothing is
// run or written.
func TestEnsureCalibreUsersNoRandom(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := &recorder{}
	created, err := EnsureCalibreUsers(t.Context(), r.run, dir, "Adm1n-pass!", bytes.NewReader(make([]byte, 7)))
	if err == nil || created || len(r.calls) != 0 || len(entries(t, dir)) != 0 {
		t.Errorf("EnsureCalibreUsers() with 7 random bytes = %t, %v after %d runs; want an error, nothing run or written", created, err, len(r.calls))
	}
}
