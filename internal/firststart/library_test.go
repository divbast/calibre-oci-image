// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package firststart

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestEnsureLibrary runs calibredb once when /books holds no metadata.db,
// and never when it holds one.
func TestEnsureLibrary(t *testing.T) {
	t.Parallel()
	books := t.TempDir()
	r := &recorder{do: func(call) ([]byte, error) {
		touch(t, filepath.Join(books, "metadata.db"))
		return []byte("id title authors \n"), nil
	}}
	created, err := EnsureLibrary(t.Context(), r.run, books)
	if err != nil || !created {
		t.Fatalf("EnsureLibrary() = %t, %v; want a library created", created, err)
	}
	want := []string{"/opt/calibre/calibredb", "--with-library", books, "list"}
	if len(r.calls) != 1 || !slices.Equal(r.calls[0].argv, want) || len(r.calls[0].stdin) != 0 {
		t.Fatalf("EnsureLibrary() ran %v, want %q once, with no stdin", r.calls, want)
	}

	created, err = EnsureLibrary(t.Context(), r.run, books)
	if err != nil || created || len(r.calls) != 1 {
		t.Errorf("EnsureLibrary() with a library = %t, %v after %d runs; want nothing run", created, err, len(r.calls))
	}
}

// TestEnsureLibraryFails is calibredb failing, or exiting 0 and creating
// no library: each stops the start, naming /books.
func TestEnsureLibraryFails(t *testing.T) {
	t.Parallel()
	for name, do := range map[string]func(call) ([]byte, error){
		"calibredb fails":       func(call) ([]byte, error) { return nil, errors.New("exit status 1") },
		"calibredb writes none": func(call) ([]byte, error) { return nil, nil },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			books := t.TempDir()
			r := &recorder{do: do}
			created, err := EnsureLibrary(t.Context(), r.run, books)
			if err == nil || created || !strings.Contains(err.Error(), books) {
				t.Errorf("EnsureLibrary() = %t, %v; want an error naming %s", created, err, books)
			}
		})
	}
}
