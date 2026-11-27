// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package firststart

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

// noLinks is a filesystem with no hard links, as SMB shares and some
// network volumes are: link fails as Linux's link(2) does there.
func noLinks(errno syscall.Errno) func(string, string) error {
	return func(oldname, newname string) error {
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: errno}
	}
}

// TestWriteNew writes a file, mode 0600, through a temporary file and a
// link, or in place on a filesystem with no hard links, and never over a
// file that is there.
func TestWriteNew(t *testing.T) {
	t.Parallel()
	for name, link := range map[string]func(string, string) error{
		"hard links":           os.Link,
		"EPERM, no links":      noLinks(syscall.EPERM),
		"EOPNOTSUPP, no links": noLinks(syscall.EOPNOTSUPP),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "secret")
			if err := writeNew(path, "text", link); err != nil {
				t.Fatalf("writeNew() = %v", err)
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != "text" { //nolint:gosec // G304: the test's own temporary file
				t.Errorf("the file holds %q, %v; want %q", data, err, "text")
			}
			if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
				t.Errorf("the file's mode = %v, %v; want 0600", fi.Mode().Perm(), err)
			}
			if names := entries(t, dir); !slices.Equal(names, []string{"secret"}) {
				t.Errorf("the directory holds %q, want the file alone", names)
			}
			if err := writeNew(path, "other", link); !errors.Is(err, fs.ErrExist) {
				t.Errorf("writeNew() over a file = %v, want fs.ErrExist", err)
			}
			if data, _ := os.ReadFile(path); string(data) != "text" { //nolint:gosec // G304: the test's own temporary file
				t.Errorf("writeNew() changed the file there to %q", data)
			}
		})
	}
}
