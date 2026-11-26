// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package supervise

import (
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestBackoff: a child that keeps stopping waits 1, 2, 4 … 60, 60 s
// before each start, and the wait goes back to 1 s once a start has run
// for ResetAfter, and not before.
func TestBackoff(t *testing.T) {
	t.Parallel()
	h := newHelper(t, func(n int) string {
		if n <= 8 {
			return "exit"
		}
		return "wait"
	})
	s, clk, logs := newTest(t, h, Child{Name: "a"})
	r := runSupervisor(t, s)

	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60}
	for i, w := range want {
		nextStart(t, h)
		if got := nextWait(t, clk); got != w*time.Second {
			t.Fatalf("the wait after stop %d: got %s, want %s", i+1, got, w*time.Second)
		}
		clk.Advance(w * time.Second)
	}

	// Up a second short of ResetAfter: the wait stays at Max
	short := nextStart(t, h)
	clk.Advance(production.ResetAfter - time.Second)
	_ = syscall.Kill(short.pid, syscall.SIGKILL)
	if got := nextWait(t, clk); got != production.Max {
		t.Fatalf("after %s up: got a wait of %s, want %s", production.ResetAfter-time.Second, got, production.Max)
	}
	clk.Advance(production.Max)

	// Up for ResetAfter: the wait is Min again
	long := nextStart(t, h)
	clk.Advance(production.ResetAfter)
	_ = syscall.Kill(long.pid, syscall.SIGKILL)
	if got := nextWait(t, clk); got != production.Min {
		t.Fatalf("after %s up: got a wait of %s, want %s", production.ResetAfter, got, production.Min)
	}

	if code := r.stop(t); code != 0 {
		t.Errorf("Run returned %d, want 0", code)
	}
	for _, line := range []string{
		"divbast-calibre: a exited with status 1; starting it again in 1s\n",
		"divbast-calibre: a exited with status 1; starting it again in 1m0s\n",
		"divbast-calibre: a was ended by signal 9 (killed); starting it again in 1m0s\n",
		"divbast-calibre: a was ended by signal 9 (killed); starting it again in 1s\n",
	} {
		if !strings.Contains(logs.String(), line) {
			t.Errorf("the log has no line %q; it is:\n%s", line, logs)
		}
	}
}

// TestStopGrace: a child that ignores SIGTERM gets SIGKILL once Grace has
// passed, and so does every process in its group, and Run returns 1.
func TestStopGrace(t *testing.T) {
	t.Parallel()
	h := newHelper(t, always("ignore-term-tree"))
	s, clk, logs := newTest(t, h, Child{Name: "a"})
	r := runSupervisor(t, s)
	a := nextStart(t, h)
	t.Cleanup(func() { _ = syscall.Kill(-a.pid, syscall.SIGKILL) })

	r.cancel()
	if got := nextWait(t, clk); got != production.Grace {
		t.Fatalf("the stop set a timer of %s, want %s", got, production.Grace)
	}
	select {
	case code := <-r.code:
		t.Fatalf("Run returned %d before the grace ended", code)
	case <-time.After(200 * time.Millisecond):
	}
	if err := syscall.Kill(a.pid, 0); err != nil {
		t.Fatalf("the child ended before the grace did: %v", err)
	}

	clk.Advance(production.Grace)
	if code := r.wait(t); code != 1 {
		t.Errorf("Run returned %d, want 1", code)
	}
	gone(t, a)
	if line := "divbast-calibre: a did not stop within 8s; killing it\n"; !strings.Contains(logs.String(), line) {
		t.Errorf("the log has no line %q; it is:\n%s", line, logs)
	}
	if got := s.State("a"); got.Up {
		t.Errorf("after Run, a's state is %+v, want down", got)
	}
}

// TestStopClean: children that stop on SIGTERM within Grace give 0.
func TestStopClean(t *testing.T) {
	t.Parallel()
	h := newHelper(t, always("wait"))
	s, _, _ := newTest(t, h, Child{Name: "a"}, Child{Name: "b"})
	r := runSupervisor(t, s)
	a, b := nextStart(t, h), nextStart(t, h)

	if code := r.stop(t); code != 0 {
		t.Errorf("Run returned %d, want 0", code)
	}
	for _, c := range []started{a, b} {
		gone(t, c)
		if got := s.State(c.name); got.Up || got.Planned {
			t.Errorf("after Run, %s's state is %+v, want down and not planned", c.name, got)
		}
	}
}

// change is one call of OnChange.
type change struct {
	name  string
	state State
}

// TestPlannedRestart: Restart stops and starts the child with no wait,
// Planned is true while it is down, and OnChange sees every change, each
// with its time.
func TestPlannedRestart(t *testing.T) {
	t.Parallel()
	h := newHelper(t, always("wait"))
	inGate, release := make(chan struct{}), make(chan struct{})
	h.gate = func(n int) {
		if n == 2 {
			inGate <- struct{}{}
			<-release
		}
	}
	s, clk, _ := newTest(t, h, Child{Name: "a"})
	changes := make(chan change, 16)
	s.OnChange(func(name string, st State) { changes <- change{name, st} })
	r := runSupervisor(t, s)
	nextStart(t, h)
	nextChange := func() change {
		t.Helper()
		select {
		case c := <-changes:
			return c
		case <-time.After(waitFor):
			t.Fatal("no change came")
			return change{}
		}
	}
	if got, want := nextChange(), (change{"a", State{Up: true, Since: t0}}); got != want {
		t.Fatalf("the first change: got %+v, want %+v", got, want)
	}

	clk.Advance(time.Minute)
	errc := restartAsync(s)
	select {
	case <-inGate:
	case <-time.After(waitFor):
		t.Fatal("the restart started nothing")
	}
	if got, want := s.State("a"), (State{Planned: true, Since: t0.Add(time.Minute)}); got != want {
		t.Errorf("while down: got %+v, want %+v", got, want)
	}
	clk.Advance(time.Second)
	close(release)
	if err := receive(t, errc); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	for _, want := range []change{
		{"a", State{Up: true, Planned: true, Since: t0.Add(time.Minute)}},
		{"a", State{Planned: true, Since: t0.Add(time.Minute)}},
		{"a", State{Up: true, Since: t0.Add(time.Minute + time.Second)}},
	} {
		if got := nextChange(); got != want {
			t.Errorf("got the change %+v, want %+v", got, want)
		}
	}
	// The one timer is the stop's grace: no wait before the start
	if got := nextWait(t, clk); got != production.Grace {
		t.Errorf("the restart set a timer of %s, want only the grace, %s", got, production.Grace)
	}
	expectNoWait(t, clk)
	if code := r.stop(t); code != 0 {
		t.Errorf("Run returned %d, want 0", code)
	}
}

// TestRestartRefused: Restart fails before Run, for a name it does not
// know, and once Run has returned.
func TestRestartRefused(t *testing.T) {
	t.Parallel()
	h := newHelper(t, always("wait"))
	s, _, _ := newTest(t, h, Child{Name: "a"})
	if err := s.Restart("a"); err == nil {
		t.Error("Restart before Run: no error")
	}
	r := runSupervisor(t, s)
	nextStart(t, h)
	if err := s.Restart("b"); err == nil || !strings.Contains(err.Error(), `"b"`) {
		t.Errorf("Restart of a name not known: got %v, want an error naming it", err)
	}
	if code := r.stop(t); code != 0 {
		t.Errorf("Run returned %d, want 0", code)
	}
	if err := s.Restart("a"); err == nil {
		t.Error("Restart after Run: no error")
	}
}

// TestCrashDuringPendingReload: a child that ends on its own while a
// Restart is under way is started once, not twice.
func TestCrashDuringPendingReload(t *testing.T) {
	t.Parallel()

	t.Run("crash while stopping", func(t *testing.T) {
		t.Parallel()
		h := newHelper(t, func(n int) string {
			if n <= 2 {
				return "ignore-term"
			}
			return "wait"
		})
		s, clk, _ := newTest(t, h, Child{Name: "a"})
		r := runSupervisor(t, s)
		first := nextStart(t, h)

		errc := restartAsync(s)
		if got := nextWait(t, clk); got != production.Grace {
			t.Fatalf("the restart set a timer of %s, want %s", got, production.Grace)
		}
		_ = syscall.Kill(first.pid, syscall.SIGKILL)
		second := nextStart(t, h)
		t.Cleanup(func() { _ = syscall.Kill(second.pid, syscall.SIGKILL) })
		if err := receive(t, errc); err != nil {
			t.Fatalf("Restart: %v", err)
		}
		expectNoWait(t, clk)

		// The first grace's timer, stale now, fires during a second
		// Restart's grace, and must not cut that grace short
		clk.Advance(production.Grace / 2)
		errc = restartAsync(s)
		if got := nextWait(t, clk); got != production.Grace {
			t.Fatalf("the second restart set a timer of %s, want %s", got, production.Grace)
		}
		clk.Advance(production.Grace / 2)
		expectNoStart(t, h)
		if err := syscall.Kill(second.pid, 0); err != nil {
			t.Fatalf("the second start was killed before its grace ended: %v", err)
		}
		clk.Advance(production.Grace / 2)
		nextStart(t, h)
		if err := receive(t, errc); err != nil {
			t.Fatalf("the second Restart: %v", err)
		}
		if code := r.stop(t); code != 0 {
			t.Errorf("Run returned %d, want 0", code)
		}
	})

	t.Run("restart while waiting to start", func(t *testing.T) {
		t.Parallel()
		h := newHelper(t, func(n int) string {
			if n <= 2 {
				return "exit"
			}
			return "wait"
		})
		s, clk, _ := newTest(t, h, Child{Name: "a"})
		r := runSupervisor(t, s)
		nextStart(t, h)
		if got := nextWait(t, clk); got != production.Min {
			t.Fatalf("the crash set a timer of %s, want %s", got, production.Min)
		}

		if err := s.Restart("a"); err != nil {
			t.Fatalf("Restart: %v", err)
		}
		nextStart(t, h)
		// The Restart's start ends at once too. Its wait is the doubled
		// one, so the first wait's timer, stale now, fires before it ends
		if got := nextWait(t, clk); got != 2*production.Min {
			t.Fatalf("the second crash set a timer of %s, want %s", got, 2*production.Min)
		}
		clk.Advance(production.Min)
		expectNoStart(t, h)
		clk.Advance(production.Min)
		nextStart(t, h)
		if code := r.stop(t); code != 0 {
			t.Errorf("Run returned %d, want 0", code)
		}
	})

	t.Run("crash and restart together", func(t *testing.T) {
		t.Parallel()
		for range 10 {
			h := newHelper(t, always("wait"))
			s, clk, _ := newTest(t, h, Child{Name: "a"})
			r := runSupervisor(t, s)
			first := nextStart(t, h)

			errc := restartAsync(s)
			_ = syscall.Kill(first.pid, syscall.SIGKILL)
			nextStart(t, h)
			if err := receive(t, errc); err != nil {
				t.Fatalf("Restart: %v", err)
			}
			clk.Advance(production.Grace + production.Max)
			expectNoStart(t, h)
			if code := r.stop(t); code != 0 {
				t.Errorf("Run returned %d, want 0", code)
			}
		}
	})
}

// TestStartOwnGroup: Start puts the child in a process group of its own.
func TestStartOwnGroup(t *testing.T) {
	t.Parallel()
	reap.ensure()
	reap.begin()
	p, err := Start(Child{Name: "a", Argv: helperArgv(), Env: helperEnv("hang")})
	if err != nil {
		reap.abandon()
		t.Fatal(err)
	}
	reap.started()
	exited := reap.claim(p.Pid)
	pgid, err := syscall.Getpgid(p.Pid)
	_ = p.Kill()
	select {
	case <-exited:
	case <-time.After(waitFor):
		t.Fatal("the child was not reaped")
	}
	if err != nil {
		t.Fatal(err)
	}
	if pgid != p.Pid {
		t.Errorf("the child's process group is %d, want its own, %d", pgid, p.Pid)
	}
}

// TestPlannedRestartKeepsUptime: a planned restart does not end the run
// ResetAfter counts. A child up 20 minutes, restarted by plan, that ends
// on its own 3 minutes later waits Min, not its doubled wait.
func TestPlannedRestartKeepsUptime(t *testing.T) {
	t.Parallel()
	h := newHelper(t, func(n int) string {
		if n == 1 {
			return "exit"
		}
		return "wait"
	})
	s, clk, _ := newTest(t, h, Child{Name: "a"})
	r := runSupervisor(t, s)
	nextStart(t, h)
	if got := nextWait(t, clk); got != production.Min {
		t.Fatalf("the first end set a wait of %s, want %s", got, production.Min)
	}
	clk.Advance(production.Min)
	nextStart(t, h) // the next wait is now 2*Min

	clk.Advance(20 * time.Minute)
	errc := restartAsync(s)
	if got := nextWait(t, clk); got != production.Grace {
		t.Fatalf("the restart set a timer of %s, want %s", got, production.Grace)
	}
	third := nextStart(t, h)
	if err := receive(t, errc); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	clk.Advance(3 * time.Minute)
	_ = syscall.Kill(third.pid, syscall.SIGKILL)
	if got := nextWait(t, clk); got != production.Min {
		t.Errorf("after 20 minutes up, a planned restart and 3 more: got a wait of %s, want %s", got, production.Min)
	}
	if code := r.stop(t); code != 0 {
		t.Errorf("Run returned %d, want 0", code)
	}
}

// TestGroupEndsWithLeader: once a child has ended, however it ended, the
// processes it left in its group are killed, so none outlives it into
// its next start.
func TestGroupEndsWithLeader(t *testing.T) {
	t.Parallel()
	tree := func(n int) string {
		if n == 1 {
			return "wait-tree"
		}
		return "wait"
	}

	t.Run("planned restart", func(t *testing.T) {
		t.Parallel()
		h := newHelper(t, tree)
		s, _, _ := newTest(t, h, Child{Name: "a"})
		r := runSupervisor(t, s)
		first := nextStart(t, h)
		t.Cleanup(func() { _ = syscall.Kill(-first.pid, syscall.SIGKILL) })
		if err := s.Restart("a"); err != nil {
			t.Fatalf("Restart: %v", err)
		}
		nextStart(t, h)
		gone(t, first)
		if code := r.stop(t); code != 0 {
			t.Errorf("Run returned %d, want 0", code)
		}
	})

	t.Run("crash", func(t *testing.T) {
		t.Parallel()
		h := newHelper(t, tree)
		s, clk, _ := newTest(t, h, Child{Name: "a"})
		r := runSupervisor(t, s)
		first := nextStart(t, h)
		t.Cleanup(func() { _ = syscall.Kill(-first.pid, syscall.SIGKILL) })
		_ = syscall.Kill(first.pid, syscall.SIGKILL)
		nextWait(t, clk)
		gone(t, first)
		if code := r.stop(t); code != 0 {
			t.Errorf("Run returned %d, want 0", code)
		}
	})

	t.Run("stop", func(t *testing.T) {
		t.Parallel()
		h := newHelper(t, tree)
		s, _, _ := newTest(t, h, Child{Name: "a"})
		r := runSupervisor(t, s)
		first := nextStart(t, h)
		t.Cleanup(func() { _ = syscall.Kill(-first.pid, syscall.SIGKILL) })
		if code := r.stop(t); code != 0 {
			t.Errorf("Run returned %d, want 0: the child itself ended within the grace", code)
		}
		gone(t, first)
	})
}

// setPasswords puts every password variable in the test's own
// environment, as PID 1 would have them before first start unsets them.
func setPasswords(t *testing.T) {
	t.Helper()
	for _, name := range PasswordVariables {
		t.Setenv(name, "pw-"+name)
	}
}

// inheritEnvHelper sets the test's own environment so that a child that
// inherits it runs as a helper printing its environment.
func inheritEnvHelper(t *testing.T) {
	t.Helper()
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	t.Setenv("HELPER_MODE", "env")
	t.Setenv("GORACE", "atexit_sleep_ms=0")
}

// checkNoPasswords fails if env, as a child printed it, holds a password
// variable, or does not look like the child's environment at all.
func checkNoPasswords(t *testing.T, how string, env []byte) {
	t.Helper()
	if !strings.Contains(string(env), "GO_WANT_HELPER_PROCESS=1") {
		t.Errorf("%s: the child printed no environment: %q", how, env)
	}
	for _, name := range PasswordVariables {
		if strings.Contains(string(env), name+"=") || strings.Contains(string(env), "pw-"+name) {
			t.Errorf("%s: the child got %s", how, name)
		}
	}
}

// TestStartNeverPassesPasswords: a child gets no password variable,
// whether its Env names them or it inherits PID 1's environment.
// Not parallel: it sets the test process's own environment.
func TestStartNeverPassesPasswords(t *testing.T) {
	setPasswords(t)
	inheritEnvHelper(t)
	t.Setenv("HELPER_OUT", "3")
	reap.ensure()
	for _, c := range []struct {
		how string
		env []string
	}{
		{"given", helperEnv("env", "HELPER_OUT=3", "CALIBRE_ADMIN_PASSWORD=pw-CALIBRE_ADMIN_PASSWORD")},
		{"inherited", nil},
	} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		reap.begin()
		p, err := startProcess(Child{Name: "a", Argv: helperArgv(), Env: c.env}, w)
		_ = w.Close()
		if err != nil {
			reap.abandon()
			t.Fatal(err)
		}
		reap.started()
		ended := reap.claim(p.Pid)
		env, err := io.ReadAll(r)
		_ = r.Close()
		<-ended
		if err != nil {
			t.Fatal(err)
		}
		checkNoPasswords(t, c.how, env)
	}
}
