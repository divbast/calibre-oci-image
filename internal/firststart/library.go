// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package firststart

import (
	"context"
	"fmt"
	"path/filepath"
)

const (
	// Calibredb is Calibre's own calibredb, in the bundle.
	Calibredb = "/opt/calibre/calibredb"
	// MetadataDB is the library's database, in /books.
	MetadataDB = "metadata.db"
)

// EnsureLibrary creates an empty Calibre library in books when books holds
// no metadata.db, and reports whether it created one. calibredb creates a
// library when the one it is asked to list is not there.
func EnsureLibrary(ctx context.Context, run Runner, books string) (bool, error) {
	db := filepath.Join(books, MetadataDB)
	if present, err := exists(db); err != nil || present {
		return false, err
	}
	if _, err := run(ctx, []string{Calibredb, "--with-library", books, "list"}, nil, nil); err != nil {
		return false, fmt.Errorf("could not create an empty library in %s: %w", books, err)
	}
	present, err := exists(db)
	switch {
	case err != nil:
		return false, err
	case !present:
		return false, fmt.Errorf("%s exited 0, but created no library in %s", Calibredb, books)
	}
	return true, nil
}
