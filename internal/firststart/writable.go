// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package firststart

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Writable returns nil when the running user can write dir, and each of
// the files named that dir holds, as Calibre and Calibre-Web will write
// them: it creates and removes a temporary file in dir, and opens each
// file for appending, writing nothing. A file that is not there yet is no
// error. Otherwise it returns one error naming the directory or the file
// and saying how to give it to the user, where Calibre or Calibre-Web
// would stop later with a traceback.
func Writable(dir string, files ...string) error {
	uid := os.Getuid()
	fix := func(what string) string {
		return fmt.Sprintf("give %s, and everything in it, to uid %d, the user the image runs Calibre and Calibre-Web as, for example with chown -R %d:%d on the host's directory", what, uid, uid, os.Getgid())
	}
	tmp, err := os.CreateTemp(dir, ".writable.*")
	if err != nil {
		return fmt.Errorf("Calibre and Calibre-Web cannot write %s: %s: %w", dir, fix("it"), err) //nolint:staticcheck // ST1005: the sentence starts with the apps' names
	}
	if cerr := errors.Join(tmp.Close(), os.Remove(tmp.Name())); cerr != nil {
		return cerr
	}
	for _, name := range files {
		path := filepath.Join(dir, name)
		f, ferr := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0) //nolint:gosec // G304: a file the image names, in a directory it names
		switch {
		case errors.Is(ferr, fs.ErrNotExist):
			continue
		case ferr != nil:
			return fmt.Errorf("Calibre and Calibre-Web cannot write %s: %s: %w", path, fix(dir), ferr) //nolint:staticcheck // ST1005: the sentence starts with the apps' names
		}
		if cerr := f.Close(); cerr != nil {
			return cerr
		}
	}
	return nil
}

// EnsureDir makes dir, mode 0700, when it is not there, and leaves one
// that is as it is. Then it checks that the running user can write it, as
// Writable does.
func EnsureDir(dir string) error {
	err := os.Mkdir(dir, 0o700)
	if errors.Is(err, fs.ErrExist) {
		fi, serr := os.Stat(dir)
		switch {
		case serr != nil:
			return serr
		case !fi.IsDir():
			return fmt.Errorf("%s is not a directory", dir)
		}
		err = nil
	}
	if err != nil {
		return fmt.Errorf("could not make a directory: %w", err)
	}
	return Writable(dir)
}
