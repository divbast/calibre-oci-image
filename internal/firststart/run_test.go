// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package firststart

import (
	"strings"
	"testing"
)

// TestExec runs a real command: it gets exactly the environment given,
// none at all when given none, and the stdin given, and its stdout comes
// back.
func TestExec(t *testing.T) {
	t.Parallel()
	out, err := Exec(t.Context(), []string{"/usr/bin/env"}, []string{"ONLY=this"}, nil)
	if err != nil || string(out) != "ONLY=this\n" {
		t.Errorf("Exec(env) printed %d variables, %v; want ONLY=this alone", strings.Count(string(out), "\n"), err)
	}
	out, err = Exec(t.Context(), []string{"/usr/bin/env"}, nil, nil)
	if err != nil || len(out) != 0 {
		// The count alone: the variables may be this process's, secrets included
		t.Errorf("Exec(env) with no environment printed %d variables, %v; want none, not this process's", strings.Count(string(out), "\n"), err)
	}
	out, err = Exec(t.Context(), []string{"/bin/cat"}, nil, []byte("on stdin"))
	if err != nil || string(out) != "on stdin" {
		t.Errorf("Exec(cat) = %q, %v; want stdin back", out, err)
	}
	if _, err = Exec(t.Context(), []string{"/usr/bin/false"}, nil, nil); err == nil || !strings.Contains(err.Error(), "/usr/bin/false") {
		t.Errorf("Exec(false) = %v, want an error naming the command", err)
	}
}

// TestExecOneLine is a command that fails after writing more to stderr
// than Exec keeps: the error is one line, ending with the command's last
// line that is not empty, and holds none of the lines before it.
func TestExecOneLine(t *testing.T) {
	t.Parallel()
	script := "i=0; while [ $i -lt 5000 ]; do echo \"traceback line $i, padded to be long enough\" >&2; i=$((i+1)); done; " +
		"printf 'helper: the last line\\r\\n\\n  \\n' >&2; exit 3"
	_, err := Exec(t.Context(), []string{"/bin/sh", "-c", script}, nil, nil)
	if err == nil {
		t.Fatal("Exec() = nil, want the command's failure")
	}
	msg := err.Error()
	if strings.ContainsAny(msg, "\r\n") || !strings.HasSuffix(msg, "helper: the last line") || strings.Contains(msg, "traceback line") {
		t.Errorf("Exec() = %q, want one line ending with the command's last line", msg)
	}
	if !strings.Contains(msg, "exit status 3") {
		t.Errorf("Exec() = %q, want it to say how the command exited", msg)
	}
	// A command that succeeds is no error, whatever it writes to stderr
	if _, err := Exec(t.Context(), []string{"/bin/sh", "-c", "echo warning >&2"}, nil, nil); err != nil {
		t.Errorf("Exec() of a command that succeeds = %v", err)
	}
}
