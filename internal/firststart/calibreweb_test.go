// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package firststart

import (
	"encoding/json"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// sentWeb is what calibre_web_init.py init reads from stdin.
type sentWeb struct {
	Appdb    string         `json:"appdb"`
	Password string         `json:"password"`
	Settings map[string]any `json:"settings"`
}

// testSettings are seven settings, each a value no default has.
var testSettings = CalibreWebSettings{
	CalibreDir:      "/library",
	BinariesDir:     "/bin/calibre",
	KepubifyPath:    "/bin/kepubify",
	RarfileLocation: "/bin/unrar",
	Port:            1234,
	Logfile:         "/dev/stderr",
	Uploading:       true,
}

// TestEnsureCalibreWeb creates app.db once, with admin's password and
// exactly the seven settings on stdin, and no password in argv.
func TestEnsureCalibreWeb(t *testing.T) {
	t.Parallel()
	const adminPW = "W3b-admin-pass!"
	dir := t.TempDir()
	r := &recorder{do: func(c call) ([]byte, error) {
		var req sentWeb
		if err := json.Unmarshal(c.stdin, &req); err != nil {
			return nil, err
		}
		if !within(dir, req.Appdb) {
			t.Errorf("appdb %s is not in %s", req.Appdb, dir)
		}
		touch(t, req.Appdb)
		return nil, nil
	}}
	created, err := EnsureCalibreWeb(t.Context(), r.run, dir, adminPW, &testSettings)
	if err != nil || !created {
		t.Fatalf("EnsureCalibreWeb() = %t, %v; want app.db created", created, err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("EnsureCalibreWeb() ran %d commands, want 1", len(r.calls))
	}
	c := r.calls[0]
	want := []string{"/app/venv/bin/python", "/usr/local/share/divbast-calibre/calibre_web_init.py", "init"}
	if !slices.Equal(c.argv, want) {
		t.Errorf("argv = %q, want %q", c.argv, want)
	}
	noSecretIn(t, c.argv, adminPW)
	var req sentWeb
	if uerr := json.Unmarshal(c.stdin, &req); uerr != nil {
		t.Fatal(uerr)
	}
	if req.Password != adminPW {
		t.Errorf("stdin's password = %q, want %q", req.Password, adminPW)
	}
	wantSettings := map[string]any{
		"config_calibre_dir":      "/library",
		"config_binariesdir":      "/bin/calibre",
		"config_kepubifypath":     "/bin/kepubify",
		"config_rarfile_location": "/bin/unrar",
		"config_port":             float64(1234),
		"config_logfile":          "/dev/stderr",
		"config_uploading":        true,
	}
	if !maps.Equal(req.Settings, wantSettings) {
		t.Errorf("stdin's settings = %v, want %v", req.Settings, wantSettings)
	}
	if names := entries(t, dir); !slices.Equal(names, []string{"app.db"}) {
		t.Errorf("the directory holds %q, want app.db alone", names)
	}

	created, err = EnsureCalibreWeb(t.Context(), r.run, dir, adminPW, &testSettings)
	if err != nil || created || len(r.calls) != 1 {
		t.Errorf("EnsureCalibreWeb() again = %t, %v after %d runs; want nothing run", created, err, len(r.calls))
	}
}

// TestEnsureCalibreWebExisting is an app.db already there: no run.
func TestEnsureCalibreWebExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "app.db"))
	r := &recorder{}
	created, err := EnsureCalibreWeb(t.Context(), r.run, dir, "", &testSettings)
	if err != nil || created || len(r.calls) != 0 {
		t.Errorf("EnsureCalibreWeb() = %t, %v after %d runs; want nothing run", created, err, len(r.calls))
	}
	if names := entries(t, dir); !slices.Equal(names, []string{"app.db"}) {
		t.Errorf("the directory holds %q, want app.db alone", names)
	}
}

// TestEnsureCalibreWebFails is the helper failing, or creating no
// database: the start stops, and leaves no app.db and no temporary file.
func TestEnsureCalibreWebFails(t *testing.T) {
	t.Parallel()
	for name, do := range map[string]func(call) ([]byte, error){
		"the helper fails": func(c call) ([]byte, error) {
			var req sentWeb
			if err := json.Unmarshal(c.stdin, &req); err != nil {
				return nil, err
			}
			// what Calibre-Web's helper leaves when a setting is refused
			touch(t, req.Appdb)
			touch(t, req.Appdb+"-journal")
			return nil, errors.New("exit status 1")
		},
		"the helper creates no database": func(call) ([]byte, error) { return nil, nil },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			r := &recorder{do: do}
			created, err := EnsureCalibreWeb(t.Context(), r.run, dir, "W3b-admin-pass!", &testSettings)
			if err == nil || created || !strings.Contains(err.Error(), dir) {
				t.Errorf("EnsureCalibreWeb() = %t, %v; want an error naming %s", created, err, dir)
			}
			if names := entries(t, dir); len(names) != 0 {
				t.Errorf("a failed start left %q", names)
			}
		})
	}
}
