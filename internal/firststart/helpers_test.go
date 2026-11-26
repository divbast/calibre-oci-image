// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package firststart

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// call is one command a Runner was given.
type call struct {
	argv, env []string
	stdin     []byte
}

// recorder is a Runner that records each command, then answers as do
// says, or with no output when do is nil.
type recorder struct {
	calls []call
	do    func(c call) ([]byte, error)
}

func (r *recorder) run(_ context.Context, argv, env []string, stdin []byte) ([]byte, error) {
	c := call{argv: slices.Clone(argv), env: slices.Clone(env), stdin: slices.Clone(stdin)}
	r.calls = append(r.calls, c)
	if r.do == nil {
		return nil, nil
	}
	return r.do(c)
}

// entries returns the names in dir, sorted.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(des))
	for _, de := range des {
		names = append(names, de.Name())
	}
	return names
}

// touch creates path, empty, as the helper whose work a test fakes would.
func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// noSecretIn fails the test for each argv element that holds one of the
// secrets.
func noSecretIn(t *testing.T, argv []string, secrets ...string) {
	t.Helper()
	for _, a := range argv {
		for _, s := range secrets {
			if strings.Contains(a, s) {
				t.Errorf("argv %q holds a password", argv)
			}
		}
	}
}

// within reports whether path is in dir.
func within(dir, path string) bool {
	return filepath.Dir(path) == dir
}
