// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package supervise

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// WriteState writes states, keyed by child name, to path as one JSON
// object. It writes a temporary file in path's directory and renames it
// over path, so a reader finds the old file or the new one, never part of
// either. It creates the directory, with mode 0700, if it is absent.
func WriteState(path string, states map[string]State) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(states)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	return nil
}

// ReadState reads the states WriteState wrote to path, through an
// [os.Root] on its directory.
func ReadState(path string) (states map[string]State, err error) {
	dir, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	b, err := dir.ReadFile(filepath.Base(path))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &states); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return states, nil
}
