// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package firststart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWritable passes a directory the running user can write, leaving
// nothing in it, and refuses one it cannot, naming it and saying how to
// give it to the user.
func TestWritable(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root writes past every mode")
	}
	ok := t.TempDir()
	if err := Writable(ok); err != nil {
		t.Errorf("Writable() of a writable directory = %v", err)
	}
	if names := entries(t, ok); len(names) != 0 {
		t.Errorf("Writable() left %q", names)
	}

	readOnly := t.TempDir()
	if err := os.Chmod(readOnly, 0o500); err != nil { //nolint:gosec // G302: a directory the test makes read-only
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) }) //nolint:gosec // G302: so the test's cleanup can remove it
	err := Writable(readOnly)
	if err == nil || !strings.Contains(err.Error(), readOnly) || !strings.Contains(err.Error(), "chown -R") {
		t.Errorf("Writable() of a read-only directory = %v, want an error naming it and saying chown -R", err)
	}
	if err != nil && strings.Contains(err.Error(), "\n") {
		t.Errorf("Writable() = %q, want one line", err)
	}
}

// TestEnsureDir creates a missing config directory with mode 0700, leaves
// one that exists as it is, and refuses a file in its place.
func TestEnsureDir(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	dir := filepath.Join(parent, "calibre")
	if err := EnsureDir(dir); err != nil {
		t.Fatalf("EnsureDir() = %v", err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Errorf("EnsureDir() made %v, %v; want a directory of mode 0700", fi, err)
	}

	existing := filepath.Join(parent, "calibre-web")
	if err := os.Mkdir(existing, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(existing); err != nil {
		t.Errorf("EnsureDir() of an existing directory = %v", err)
	}
	if fi, err := os.Stat(existing); err != nil || fi.Mode().Perm() != 0o750 {
		t.Errorf("EnsureDir() changed an existing directory to %v, %v; want it left at 0750", fi.Mode().Perm(), err)
	}

	file := filepath.Join(parent, "file")
	touch(t, file)
	if err := EnsureDir(file); err == nil || !strings.Contains(err.Error(), file) {
		t.Errorf("EnsureDir() of a file = %v, want an error naming it", err)
	}
}
