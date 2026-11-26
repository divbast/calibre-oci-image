// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package supervise

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStateRoundTrip: WriteState writes the format health reads, and
// ReadState reads back what was written.
func TestStateRoundTrip(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "divbast-calibre")
	path := filepath.Join(dir, "state.json")
	since := time.Date(2026, 11, 26, 23, 55, 1, 123456789, time.UTC)
	states := map[string]State{
		"calibre-server": {Planned: true, Since: since},
		"calibre-web":    {Up: true, Since: since.Add(time.Second)},
	}

	if err := WriteState(path, states); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("the directory's mode is %v, want 0700", info.Mode().Perm())
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	raw, err := root.ReadFile("state.json")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"calibre-server":{"up":false,"planned":true,"since":"2026-11-26T23:55:01.123456789Z"},` +
		`"calibre-web":{"up":true,"planned":false,"since":"2026-11-26T23:55:02.123456789Z"}}`
	if string(raw) != want {
		t.Errorf("the file holds\n%s\nwant\n%s", raw, want)
	}

	got, err := ReadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(states) {
		t.Fatalf("read %v, want %v", got, states)
	}
	for name, st := range states {
		if g := got[name]; g.Up != st.Up || g.Planned != st.Planned || !g.Since.Equal(st.Since) {
			t.Errorf("%s: read %+v, want %+v", name, g, st)
		}
	}

	// A second write replaces the first, and leaves no temporary file
	if err = WriteState(path, map[string]State{"calibre-web": {}}); err != nil {
		t.Fatal(err)
	}
	if got, err = ReadState(path); err != nil || len(got) != 1 {
		t.Errorf("after the second write: read %v, %v; want calibre-web alone", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the directory holds %d files, want state.json alone", len(entries))
	}
}

// TestReadStateFails: a missing or broken file is an error naming it.
func TestReadStateFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := ReadState(filepath.Join(dir, "none.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing file: got %v, want %v", err, fs.ErrNotExist)
	}
	broken := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(broken, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadState(broken); err == nil || !strings.Contains(err.Error(), broken) {
		t.Errorf("a broken file: got %v, want an error naming it", err)
	}
}
