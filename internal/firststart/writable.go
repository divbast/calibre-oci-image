// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package firststart

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Writable returns nil when the running user can write dir: it creates
// and removes a temporary file in dir. Otherwise it returns one error
// naming dir and saying how to give it to the user, where Calibre or
// Calibre-Web would stop later with a traceback.
func Writable(dir string) error {
	uid := os.Getuid()
	fix := fmt.Sprintf("give it, and everything in it, to uid %d, the user the image runs Calibre and Calibre-Web as, for example with chown -R %d:%d on the host's directory", uid, uid, os.Getgid())
	tmp, err := os.CreateTemp(dir, ".writable.*")
	if err != nil {
		return fmt.Errorf("Calibre and Calibre-Web cannot write %s: %s: %w", dir, fix, err) //nolint:staticcheck // ST1005: the sentence starts with the apps' names
	}
	return errors.Join(tmp.Close(), os.Remove(tmp.Name()))
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
