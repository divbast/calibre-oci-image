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
