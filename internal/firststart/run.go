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
	"os/exec"
	"slices"
	"strings"
)

// Runner runs argv, with env as its whole environment and stdin as its
// standard input, and returns what it wrote to stdout. The error says why
// it could not run, or that it exited with a code other than 0.
type Runner func(ctx context.Context, argv, env []string, stdin []byte) (stdout []byte, err error)

// stderrKept is how much of a command's stderr Exec keeps: the end, where
// its last line is.
const stderrKept = 64 << 10

// Exec is the Runner first start uses, before PID 1 starts anything else:
// it waits for the command it starts, so the command is reaped. A nil env
// is an empty environment, never this process's. The command's stderr
// never reaches PID 1's: when the command fails, the error ends with the
// last line it wrote there that is not empty, so PID 1 reports the
// failure in one line, and nothing else the command wrote reaches the
// log.
func Exec(ctx context.Context, argv, env []string, stdin []byte) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("no command to run")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // G204: first start's own commands, at the image's paths
	// Never nil: a nil Env would give the command this process's environment
	cmd.Env = append([]string{}, env...)
	cmd.Stdin = bytes.NewReader(stdin)
	stderr := &tail{max: stderrKept}
	cmd.Stderr = stderr
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	if line := lastLine(stderr.buf); line != "" {
		return out, fmt.Errorf("%s: %w: %s", argv[0], err, line)
	}
	return out, fmt.Errorf("%s: %w", argv[0], err)
}

// tail keeps the last max bytes written to it.
type tail struct {
	buf []byte
	max int
}

func (t *tail) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > t.max {
		p = p[len(p)-t.max:]
	}
	if over := len(t.buf) + len(p) - t.max; over > 0 {
		t.buf = t.buf[:copy(t.buf, t.buf[over:])]
	}
	t.buf = append(t.buf, p...)
	return n, nil
}

// lastLine returns the last line in b that is not empty, without the
// white space around it.
func lastLine(b []byte) string {
	lines := strings.Split(string(b), "\n")
	for _, l := range slices.Backward(lines) {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}
