// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package supervise

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestReapsOrphans: the one Wait4(-1) goroutine hands each exit status to
// the child it belongs to, and to Exec. Only PID 1 is passed the
// processes whose parent ended first, so only a test binary run as a
// container's PID 1 checks those are collected too; elsewhere this checks
// the dispatch, with two children and a one-off run.
func TestReapsOrphans(t *testing.T) {
	t.Parallel()
	t.Run("an orphan, as PID 1", func(t *testing.T) {
		t.Parallel()
		if os.Getpid() != 1 {
			t.Skip("not PID 1, so no orphan is passed to this process")
		}
		s := New(nil, production, nil)
		out, err := s.Exec(t.Context(), helperArgv(), helperEnv("orphan"), nil)
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil {
			t.Fatal(err)
		}
		// Once it ends, it stays a zombie, which kill finds, until it is
		// collected
		deadline := time.Now().Add(waitFor)
		for syscall.Kill(pid, 0) == nil {
			if time.Now().After(deadline) {
				t.Fatalf("the orphan %d was not collected", pid)
			}
			time.Sleep(10 * time.Millisecond)
		}
	})

	h := newHelper(t, always("wait"))
	s, clk, logs := newTest(t, h,
		Child{Name: "a", Env: []string{"HELPER_EXIT=4"}},
		Child{Name: "b", Env: []string{"HELPER_EXIT=5"}})
	r := runSupervisor(t, s)
	kids := map[string]started{}
	for range 2 {
		k := nextStart(t, h)
		kids[k.name] = k
	}

	out, err := s.Exec(t.Context(), helperArgv(), helperEnv("echo", "HELPER_EXIT=6"), []byte("while both run"))
	if string(out) != "while both run" {
		t.Errorf("Exec's stdout: got %q, want %q", out, "while both run")
	}
	if want := helperArgv()[0] + " exited with status 6"; err == nil || err.Error() != want {
		t.Errorf("Exec's error: got %v, want %q", err, want)
	}

	// b ends first, though it started second
	_ = syscall.Kill(kids["b"].pid, syscall.SIGTERM)
	nextWait(t, clk)
	if !strings.Contains(logs.String(), "divbast-calibre: b exited with status 5;") {
		t.Errorf("b's status did not reach b; the log is:\n%s", logs)
	}
	if !s.State("a").Up || s.State("b").Up {
		t.Errorf("after b ended: a is %+v and b is %+v, want a up and b down", s.State("a"), s.State("b"))
	}
	_ = syscall.Kill(kids["a"].pid, syscall.SIGTERM)
	nextWait(t, clk)
	if !strings.Contains(logs.String(), "divbast-calibre: a exited with status 4;") {
		t.Errorf("a's status did not reach a; the log is:\n%s", logs)
	}
	if code := r.stop(t); code != 0 {
		t.Errorf("Run returned %d, want 0", code)
	}
}

// TestOrphanStatusesDiscarded: the reaper keeps a status no start has
// claimed only while a start is in progress, so statuses of processes
// nobody started do not pile up.
func TestOrphanStatusesDiscarded(t *testing.T) {
	t.Parallel()
	r := newReaper()
	status := func(code uint32) syscall.WaitStatus { return syscall.WaitStatus(code << 8) }

	r.reaped(101, status(0))
	if len(r.early) != 0 {
		t.Errorf("with no start in progress, an orphan's status was kept: %v", r.early)
	}

	r.begin()
	r.reaped(102, status(0)) // an orphan's, which nobody claims
	r.reaped(103, status(7)) // the start's own, reaped before its claim
	if len(r.early) != 2 {
		t.Errorf("during a start, the statuses kept are %v, want 102's and 103's", r.early)
	}
	exitStatus(t, r.claim(103), 7)
	if len(r.early) != 0 {
		t.Errorf("after the last start, statuses are still kept: %v", r.early)
	}

	r.begin()
	r.abandon()
	if r.starts != 0 {
		t.Errorf("after an abandoned start, %d starts are in progress, want 0", r.starts)
	}

	r.begin()
	exited := r.claim(104)
	r.reaped(104, status(3))
	exitStatus(t, exited, 3)
}

// exitStatus checks c holds a status with exit status want already, as it
// must when the reaper has handed it over.
func exitStatus(t *testing.T, c <-chan syscall.WaitStatus, want int) {
	t.Helper()
	select {
	case ws := <-c:
		if ws.ExitStatus() != want {
			t.Errorf("the claim got exit status %d, want %d", ws.ExitStatus(), want)
		}
	default:
		t.Errorf("the claim got no status, want exit status %d", want)
	}
}

// TestExecOneOff: Exec passes stdin, returns stdout, and takes the exit
// status from the reaper.
func TestExecOneOff(t *testing.T) {
	t.Parallel()
	s := New(nil, production, nil)
	bin := helperArgv()[0]

	t.Run("stdin to stdout", func(t *testing.T) {
		t.Parallel()
		out, err := s.Exec(t.Context(), helperArgv(), helperEnv("echo"), []byte("a\x00b\nc"))
		if err != nil || string(out) != "a\x00b\nc" {
			t.Errorf("got %q, %v; want %q, no error", out, err, "a\x00b\nc")
		}
	})
	t.Run("exit status", func(t *testing.T) {
		t.Parallel()
		out, err := s.Exec(t.Context(), helperArgv(), helperEnv("echo", "HELPER_EXIT=3"), []byte("x"))
		if want := bin + " exited with status 3"; err == nil || err.Error() != want {
			t.Errorf("got the error %v, want %q", err, want)
		}
		if string(out) != "x" {
			t.Errorf("got stdout %q, want %q", out, "x")
		}
	})
	t.Run("context ends", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()
		_, err := s.Exec(ctx, helperArgv(), helperEnv("hang"), nil)
		if !errors.Is(err, context.DeadlineExceeded) || !strings.HasPrefix(err.Error(), bin+": ") {
			t.Errorf("got the error %v, want one naming %s and wrapping %v", err, bin, context.DeadlineExceeded)
		}
	})
	t.Run("no program", func(t *testing.T) {
		t.Parallel()
		if _, err := s.Exec(t.Context(), nil, nil, nil); err == nil {
			t.Error("an empty argv: no error")
		}
		missing := t.TempDir() + "/missing"
		if _, err := s.Exec(t.Context(), []string{missing}, nil, nil); err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("a missing program: got %v, want an error naming it", err)
		}
	})
}

// TestExecFoldsStderr: a one-off program's stderr never reaches PID 1's.
// One that fails after writing many lines there gives an error of one
// line, ending with its last line that is not empty; one that succeeds
// gives no error.
func TestExecFoldsStderr(t *testing.T) { //nolint:paralleltest // it replaces os.Stderr, which every test shares
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	leaked := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r) // the write end's close ends it
		leaked <- b
	}()
	saved := os.Stderr
	os.Stderr = w
	s := New(nil, production, nil)
	_, failed := s.Exec(t.Context(), helperArgv(), helperEnv("stderr-lines", "HELPER_EXIT=3"), nil)
	_, worked := s.Exec(t.Context(), helperArgv(), helperEnv("stderr-lines"), nil)
	os.Stderr = saved
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	if want := helperArgv()[0] + " exited with status 3: the last line"; failed == nil || failed.Error() != want {
		t.Errorf("a failed run: got the error %v, want %q", failed, want)
	}
	if worked != nil {
		t.Errorf("a run that worked: got the error %v, want none", worked)
	}
	if b := <-leaked; len(b) > 0 {
		t.Errorf("PID 1's stderr got %d bytes of the program's, starting %q, want none", len(b), b[:min(len(b), 60)])
	}
}

// TestExecRaceWithReaper: a process that exits at once may be reaped
// before Exec claims its PID, and Exec still gets its status.
func TestExecRaceWithReaper(t *testing.T) {
	t.Parallel()

	t.Run("reaped before the claim", func(t *testing.T) {
		t.Parallel()
		s := New(nil, production, nil)
		// Hold the claim back until the reaper has collected the process
		s.execStarted = func(pid int) {
			deadline := time.Now().Add(waitFor)
			for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			time.Sleep(20 * time.Millisecond)
		}
		for i := range 20 {
			code := i%5 + 1
			err := execWithin(t, s, helperEnv("exit-now", "HELPER_EXIT="+strconv.Itoa(code)))
			if want := helperArgv()[0] + " exited with status " + strconv.Itoa(code); err == nil || err.Error() != want {
				t.Fatalf("run %d: got %v, want %q", i, err, want)
			}
		}
	})

	t.Run("many at once", func(t *testing.T) {
		t.Parallel()
		s := New(nil, production, nil)
		var wg sync.WaitGroup
		for w := range 8 {
			wg.Go(func() {
				for i := range 25 {
					code := (w + i) % 2 * 5
					err := execWithin(t, s, helperEnv("exit-now", "HELPER_EXIT="+strconv.Itoa(code)))
					if (code == 0) != (err == nil) {
						t.Errorf("exit status %d: got the error %v", code, err)
					}
				}
			})
		}
		wg.Wait()
	})
}

// execWithin runs the helper through Exec, and fails the test if Exec
// does not return.
func execWithin(t *testing.T, s *Supervisor, env []string) error {
	t.Helper()
	errc := make(chan error, 1)
	go func() {
		_, err := s.Exec(t.Context(), helperArgv(), env, nil)
		errc <- err
	}()
	select {
	case err := <-errc:
		return err
	case <-time.After(waitFor):
		t.Error("Exec did not return: its status was lost")
		return errors.New("no answer")
	}
}

// TestExecNeverPassesPasswords: a one-off run gets no password variable,
// whether env names them or it inherits PID 1's environment.
// Not parallel: it sets the test process's own environment.
func TestExecNeverPassesPasswords(t *testing.T) { //nolint:paralleltest // its helpers call t.Setenv, which sets the process's environment
	setPasswords(t)
	inheritEnvHelper(t)
	s := New(nil, production, nil)
	given := helperEnv("env", "CALIBRE_WEB_ADMIN_PASSWORD_FILE=pw-CALIBRE_WEB_ADMIN_PASSWORD_FILE")
	for how, env := range map[string][]string{"given": given, "inherited": nil} {
		out, err := s.Exec(t.Context(), helperArgv(), env, nil)
		if err != nil {
			t.Fatalf("%s: %v", how, err)
		}
		checkNoPasswords(t, how, out)
	}
}

// TestExecStdoutCutShort: when ctx ends after the program has exited 0
// but before its stdout has been read to the end, Exec returns ctx's
// error and no output, never the part it read as a success.
func TestExecStdoutCutShort(t *testing.T) {
	t.Parallel()
	s := New(nil, production, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	out, err := s.Exec(ctx, helperArgv(), helperEnv("hold-stdout"), nil)
	if !errors.Is(err, context.DeadlineExceeded) || out != nil {
		t.Errorf("got %q, %v; want no output and an error wrapping %v", out, err, context.DeadlineExceeded)
	}
}
