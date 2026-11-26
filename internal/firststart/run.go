// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

// Package firststart prepares what Calibre's Content Server and
// Calibre-Web need before either starts for the first time: a Calibre
// library in /books, the Content Server's users, and Calibre-Web's
// settings and admin password. It creates each only when it is not there,
// and never reads or changes one that is.
package firststart

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// Runner runs argv, with env as its whole environment and stdin as its
// standard input, and returns what it wrote to stdout. The error says why
// it could not run, or that it exited with a code other than 0.
type Runner func(ctx context.Context, argv, env []string, stdin []byte) (stdout []byte, err error)

// Exec is the Runner first start uses, before PID 1 starts anything else:
// it waits for the command it starts, so the command is reaped. The
// command's stderr is PID 1's, so its own one-line error reaches the log.
// A nil env is an empty environment, never this process's.
func Exec(ctx context.Context, argv, env []string, stdin []byte) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("no command to run")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // G204: first start's own commands, at the image's paths
	// Never nil: a nil Env would give the command this process's environment
	cmd.Env = append([]string{}, env...)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s: %w", argv[0], err)
	}
	return out, nil
}
