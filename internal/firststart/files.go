// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package firststart

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// exists reports whether path is there, of any kind.
func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}
	return false, fmt.Errorf("cannot tell whether %s exists: %w", path, err)
}

// createDatabase has argv, given stdin, create the SQLite database tmp,
// then renames tmp to final, so final appears only once it is whole.
// Whatever is at tmp first, its journals too, is left from a start that
// stopped, and is removed; and so is what the command leaves at tmp when
// it fails.
func createDatabase(ctx context.Context, run Runner, argv []string, stdin []byte, tmp, final string) error {
	if err := removeDatabase(tmp); err != nil {
		return err
	}
	if err := placeDatabase(ctx, run, argv, stdin, tmp, final); err != nil {
		return errors.Join(err, removeDatabase(tmp))
	}
	return nil
}

// placeDatabase is createDatabase's run and rename.
func placeDatabase(ctx context.Context, run Runner, argv []string, stdin []byte, tmp, final string) error {
	if _, err := run(ctx, argv, nil, stdin); err != nil {
		return err
	}
	made, err := exists(tmp)
	switch {
	case err != nil:
		return err
	case !made:
		return fmt.Errorf("%s exited 0, but created no %s", argv[0], tmp)
	}
	if rerr := os.Rename(tmp, final); rerr != nil {
		return rerr
	}
	return syncDir(filepath.Dir(final))
}

// removeDatabase removes the SQLite database path and its journals,
// where they are.
func removeDatabase(path string) error {
	var err error
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		if rerr := os.Remove(path + suffix); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			err = errors.Join(err, rerr)
		}
	}
	return err
}

// writeNew writes text to the new file path, mode 0600, through a
// temporary file in the same directory and a hard link made by link, so
// the file appears whole or not at all. It never writes over a file that
// is there: that is an error that wraps fs.ErrExist. On a filesystem with
// no hard links, such as an SMB share, it creates the file in place
// instead, only where no file is.
func writeNew(path, text string, link func(oldname, newname string) error) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() {
		if rerr := os.Remove(tmp.Name()); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			err = errors.Join(err, rerr)
		}
	}()
	_, err = tmp.WriteString(text)
	if err == nil {
		err = tmp.Sync()
	}
	err = errors.Join(err, tmp.Close())
	if err != nil {
		return fmt.Errorf("write %s: %w", tmp.Name(), err)
	}
	err = link(tmp.Name(), path)
	if noHardLinks(err) {
		err = create(path, text)
	}
	if err != nil {
		return err
	}
	return syncDir(dir)
}

// noHardLinks reports whether err is link(2) failing because the
// filesystem makes no hard links: EPERM, as Linux says for one that cannot,
// or EOPNOTSUPP, as SMB and some network filesystems say.
func noHardLinks(err error) bool {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EOPNOTSUPP)
}

// create writes text to path, mode 0600, only if no file is there, and
// removes what it created if the write fails, so no partial file is left.
func create(path, text string) (err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: a file in the config directory the image names
	if err != nil {
		return err
	}
	_, err = f.WriteString(text)
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return errors.Join(err, os.Remove(path))
	}
	return nil
}

// syncDir makes dir's new entry durable.
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: a directory the image names
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
