// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package firststart

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
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

// The advice for a directory, and for a file, on a read-only file system.
const (
	mountDirWritable  = "mount a writable volume or folder there, not a read-only one"
	mountFileWritable = "mount it writable, not read-only"
)

// checkReadOnly checks that err is the one line for path on a read-only
// file system: it names path, gives fix, the advice to mount it writable,
// and gives no chown hint, which cannot make a read-only file system
// writable.
func checkReadOnly(t *testing.T, err error, path, fix string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error for %s on a read-only file system", path)
	}
	msg := err.Error()
	for _, want := range []string{"cannot write " + path + ": it is on a read-only file system: " + fix + ": "} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error %q does not say %q", msg, want)
		}
	}
	if strings.Contains(msg, "chown") || strings.Contains(msg, "\n") {
		t.Errorf("the error %q gives a chown hint, or is more than one line", msg)
	}
}

// TestCannotWriteReadOnly is the error for a directory, and for a file in
// it, on a read-only file system, as a mount with ro is, or the image's
// own root with nothing mounted there: it says to mount it writable, and
// never to give it to the user. Any other error still says how to give it.
func TestCannotWriteReadOnly(t *testing.T) {
	t.Parallel()
	erofs := func(path string) error { return &fs.PathError{Op: "open", Path: path, Err: syscall.EROFS} }
	checkReadOnly(t, cannotWrite("/books", "", erofs("/books/.writable.1")), "/books", mountDirWritable)
	checkReadOnly(t, cannotWrite("/books/metadata.db", "/books", erofs("/books/metadata.db")), "/books/metadata.db", mountFileWritable)
	err := cannotWrite("/books", "", &fs.PathError{Op: "open", Path: "/books/.writable.1", Err: syscall.EACCES})
	if err == nil || !strings.Contains(err.Error(), "chown -R") || !errors.Is(err, syscall.EACCES) {
		t.Errorf("a directory the user cannot write: %v, want the chown hint and the cause", err)
	}
}

// TestWritableReadOnlyRoot is Writable on a read-only file system where
// the platform has one the test can use: macOS's root is one.
func TestWritableReadOnlyRoot(t *testing.T) {
	t.Parallel()
	f, err := os.CreateTemp("/", ".writable.*")
	if err == nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}
	if !errors.Is(err, syscall.EROFS) {
		t.Skipf("/ is not a read-only file system here: %v", err)
	}
	checkReadOnly(t, Writable("/"), "/", mountDirWritable)
}

// TestWritableFiles opens each file named that is there for appending, as
// the app will write it, and writes nothing to it: one the user cannot
// write is refused, naming it and saying how to give it to the user; one
// that is not there yet is no error. A file of mode 0444 stands in for
// one another uid owns.
func TestWritableFiles(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root writes past every mode")
	}
	dir := t.TempDir()
	ok := filepath.Join(dir, "metadata.db")
	if err := os.WriteFile(ok, []byte("library"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Writable(dir, "metadata.db", "missing.db"); err != nil {
		t.Errorf("Writable() with a writable file and a missing one = %v", err)
	}
	if data, err := os.ReadFile(ok); err != nil || string(data) != "library" { //nolint:gosec // G304: the test's own temporary file
		t.Errorf("Writable() left the file holding %q, %v; want it unchanged", data, err)
	}
	theirs := filepath.Join(dir, "users.sqlite")
	if err := os.WriteFile(theirs, []byte("theirs"), 0o444); err != nil { //nolint:gosec // G306: a file the test makes read-only
		t.Fatal(err)
	}
	err := Writable(dir, "metadata.db", "users.sqlite")
	if err == nil || !strings.Contains(err.Error(), theirs) || !strings.Contains(err.Error(), "chown -R") {
		t.Errorf("Writable() with a read-only file = %v, want an error naming it and saying chown -R", err)
	}
	if names := entries(t, dir); !slices.Equal(names, []string{"metadata.db", "users.sqlite"}) {
		t.Errorf("Writable() left %q", names)
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
