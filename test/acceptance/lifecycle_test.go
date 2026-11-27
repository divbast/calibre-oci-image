// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/divbast/ci/harness"
)

// The lifecycle's bounds.
const (
	// stopWithin is PID 1's grace for the apps to stop, after which it
	// kills them and exits 1.
	stopWithin = 8 * time.Second
	// zombieWithin bounds how long an ended process may stay a zombie
	// before PID 1 collects it.
	zombieWithin = 10 * time.Second
)

// started returns when c's engine last started it, and fails tb if it is
// not running.
func started(tb testing.TB, c *harness.Container) time.Time {
	tb.Helper()
	in, err := c.Inspect(tb.Context())
	if err != nil {
		tb.Fatal(err)
	}
	if !in.State.Running {
		tb.Fatalf("%s is %s, not running", c, in.State.Status)
	}
	return in.State.StartedAt
}

// inPlace checks that c is the same container, started by the engine at
// since and never again, with PID 1 still the image's command.
func inPlace(tb testing.TB, c *harness.Container, since time.Time) {
	tb.Helper()
	checks := harness.NewChecks(tb)
	harness.Equal(checks, "the engine never restarted the container", since, started(tb, c))
	ps, err := processes(tb.Context(), c)
	if err != nil {
		tb.Fatal(err)
	}
	pid1 := "none"
	for i := range ps {
		if ps[i].PID == 1 {
			pid1 = strings.Join(ps[i].Argv, " ")
		}
	}
	harness.Equal(checks, "PID 1 is still the image's command", strings.Join(serverArgv, " "), pid1)
}

// waitUpSince waits until PID 1's state file in c says app is up, since a
// time after since.
func waitUpSince(ctx context.Context, c *harness.Container, app string, since time.Time) error {
	return harness.Poll(ctx, startWithin, healthEvery, func(ctx context.Context) error {
		states, err := appStates(ctx, c)
		switch {
		case err != nil:
			return harness.NotYet(err)
		case !states[app].Up || !states[app].Since.After(since):
			return harness.NotYet(fmt.Errorf("%s's state is %+v, not up since after %s", app, states[app], since))
		}
		return nil
	})
}

// TestCrashRestartsInPlace kills each app in turn with SIGKILL, as a crash
// ends it. PID 1 says so in its log, starts the app again as a new
// process, in the same container, and its state file has the app up since
// after the kill; then the shipped probe reports the image healthy.
func TestCrashRestartsInPlace(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	tb := redacting(t)
	ctx := tb.Context()
	sec := newSecrets(tb)
	s := (&launch{env: sec.env(), mounts: append(newStore(tb, e).mounts(), sec.mount()), passwords: sec.all()}).serve(tb, e, image)
	since := started(tb, s.C)
	checks := harness.NewChecks(tb)
	for _, app := range apps {
		old, err := appPID(ctx, s.C, app)
		if err != nil {
			tb.Fatal(err)
		}
		before, err := appStates(ctx, s.C)
		if err != nil {
			tb.Fatal(err)
		}
		kill(tb, s.C, old)
		pid, err := waitNewPID(ctx, s.C, app, old)
		checks.True(app+" killed: PID 1 starts it again, as a new process", err == nil, fmt.Sprintf("PID %d, then %d: %v", old, pid, err))
		said := regexp.MustCompile("divbast-calibre: " + app + ` was ended by signal 9 \(killed\); starting it again in `)
		_, err = waitLog(ctx, s.C, said, startWithin)
		checks.True(app+" killed: PID 1 says it was killed, and starts it again", err == nil, fmt.Sprint(err))
		err = waitUpSince(ctx, s.C, app, before[app].Since)
		checks.True(app+" killed: PID 1's state file has it up again, since after the kill", err == nil, fmt.Sprint(err))
		err = waitHealthy(ctx, s.C)
		checks.True(app+" killed: the probe reports the image healthy again", err == nil, fmt.Sprint(err))
	}
	inPlace(tb, s.C, since)
}

// TestCalibreWebButtons presses Calibre-Web's own Restart, then its own
// Shutdown, on its admin page. Restart starts Calibre-Web again in the
// same process; after Shutdown, PID 1 starts it again, as a new process.
// Each time the probe reports the image healthy again, in the same
// container.
func TestCalibreWebButtons(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	tb := redacting(t)
	ctx := tb.Context()
	sec := newSecrets(tb)
	s := (&launch{env: sec.env(), mounts: append(newStore(tb, e).mounts(), sec.mount()), passwords: sec.all()}).serve(tb, e, image)
	since := started(tb, s.C)
	checks := harness.NewChecks(tb)
	w := loginAdmin(tb, s, sec.web)
	before, err := appPID(ctx, s.C, calibreWeb)
	if err != nil {
		tb.Fatal(err)
	}
	press := func(what string, parameter int) {
		tb.Helper()
		if _, gerr := w.get(ctx, "/admin/view", nil); gerr != nil {
			tb.Fatal(gerr)
		}
		resp, perr := w.postJSON(ctx, "/shutdown", map[string]int{"parameter": parameter})
		harness.Answers(tb, what+": Calibre-Web answers the button", http.StatusOK, resp, perr)
	}

	press("Restart", 0)
	_, err = waitLog(ctx, s.C, regexp.MustCompile("Performing restart of Calibre-Web"), startWithin)
	checks.True("Restart: Calibre-Web restarts itself", err == nil, fmt.Sprint(err))
	err = waitHealthy(ctx, s.C)
	checks.True("Restart: the probe reports the image healthy again", err == nil, fmt.Sprint(err))
	pid, err := appPID(ctx, s.C, calibreWeb)
	checks.True("Restart: Calibre-Web keeps its PID", err == nil && pid == before, fmt.Sprintf("PID %d, then %d: %v", before, pid, err))

	w = loginAdmin(tb, s, sec.web)
	press("Shutdown", 1)
	pid, err = waitNewPID(ctx, s.C, calibreWeb, before)
	checks.True("Shutdown: PID 1 starts Calibre-Web again, as a new process", err == nil, fmt.Sprintf("PID %d, then %d: %v", before, pid, err))
	harness.LogContains(tb, s.C, "Shutdown: PID 1 says Calibre-Web ended, and starts it again", "divbast-calibre: calibre-web exited with status 0; starting it again")
	err = waitHealthy(ctx, s.C)
	checks.True("Shutdown: the probe reports the image healthy again", err == nil, fmt.Sprint(err))
	inPlace(tb, s.C, since)
}

// TestStop stops a serving container as a user does, with <engine> stop
// -t 10: PID 1 stops both apps within its grace, and exits 0, under 8 s
// after the stop began. The engine's own cleanup after PID 1 has exited
// does not count (see stop).
func TestStop(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	tb := redacting(t)
	sec := newSecrets(tb)
	s := (&launch{env: sec.env(), mounts: append(newStore(tb, e).mounts(), sec.mount()), passwords: sec.all()}).serve(tb, e, image)
	took, code := stop(tb, e, s.C)
	checks := harness.NewChecks(tb)
	harness.Equal(checks, "the container exits 0", 0, code)
	checks.True("PID 1 exits under "+stopWithin.String()+" after the stop began", took < stopWithin, took.String())
}

// serveLibrary serves the image on the fixture library, owned by uid
// 65532, and returns it with an admin's session with Calibre-Web.
func serveLibrary(r *redactor, e harness.Engine, image string) (*harness.Serving, *webSession) {
	r.Helper()
	sec := newSecrets(r)
	books := library(r, e, image, 65532)
	s := (&launch{
		env:       sec.env(),
		mounts:    []harness.Mount{books, volume(r, e, configDir), sec.mount()},
		passwords: sec.all(),
	}).serve(r, e, image)
	return s, loginAdmin(r, s, sec.web)
}

// reload edits the fixture book's title in Calibre-Web, and returns
// calibre-server's new PID once PID 1 has restarted it to read the change:
// PID 1 waits for 5 s with no change to the library after the 10 s in
// which it tells whose change it was, and the wait allows a start's 60 s
// in all.
func reload(tb testing.TB, s *harness.Serving, w *webSession, old int) int {
	tb.Helper()
	editTitle(tb, w, 1, "Edited in Calibre-Web "+random(tb, 6, lower))
	pid, err := waitNewPID(tb.Context(), s.C, contentServer, old)
	if err != nil {
		tb.Fatalf("PID 1 never restarted calibre-server after an edit in Calibre-Web: %v", err)
	}
	return pid
}

// TestNoZombies crashes calibre-server, whose worker the kernel then gives
// to PID 1, and has PID 1 restart it after an edit in Calibre-Web: once
// the image is healthy again, no process in the container is a zombie,
// since PID 1 collects every process that ends there.
func TestNoZombies(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	tb := redacting(t)
	ctx := tb.Context()
	s, w := serveLibrary(tb, e, image)
	old, err := appPID(ctx, s.C, contentServer)
	if err != nil {
		tb.Fatal(err)
	}
	kill(tb, s.C, old)
	crashed, err := waitNewPID(ctx, s.C, contentServer, old)
	if err != nil {
		tb.Fatal(err)
	}
	if err = waitHealthy(ctx, s.C); err != nil {
		tb.Fatal(err)
	}
	reload(tb, s, w, crashed)
	if err = waitHealthy(ctx, s.C); err != nil {
		tb.Fatal(err)
	}
	var zombies []string
	err = harness.Poll(ctx, zombieWithin, healthEvery, func(ctx context.Context) error {
		ps, perr := processes(ctx, s.C)
		if perr != nil {
			return harness.NotYet(perr)
		}
		zombies = nil
		for i := range ps {
			if ps[i].State == "Z" {
				zombies = append(zombies, fmt.Sprintf("PID %d, child of %d", ps[i].PID, ps[i].PPID))
			}
		}
		if len(zombies) > 0 {
			return harness.NotYet(fmt.Errorf("%d zombies", len(zombies)))
		}
		return nil
	})
	harness.NewChecks(tb).True("no process is a zombie after a crash and a planned reload", err == nil, fmt.Sprintf("zombies: %s: %v", none(zombies), err))
}

// TestNoStrayProcesses has PID 1 restart calibre-server, after an edit in
// Calibre-Web, as it does to load each change Calibre-Web makes: no
// process of the old calibre-server is left, neither the server itself
// nor the worker it ran, in its process group.
func TestNoStrayProcesses(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	tb := redacting(t)
	ctx := tb.Context()
	s, w := serveLibrary(tb, e, image)
	old, err := appPID(ctx, s.C, contentServer)
	if err != nil {
		tb.Fatal(err)
	}
	ps, err := processes(ctx, s.C)
	if err != nil {
		tb.Fatal(err)
	}
	var group []int
	for i := range ps {
		if ps[i].PGID == old {
			group = append(group, ps[i].PID)
		}
	}
	checks := harness.NewChecks(tb)
	checks.True("before the reload, calibre-server's process group holds the server and its worker", len(group) >= 2, fmt.Sprint(group))

	reload(tb, s, w, old)
	ps, err = processes(ctx, s.C)
	if err != nil {
		tb.Fatal(err)
	}
	var left []string
	for i := range ps {
		if slices.Contains(group, ps[i].PID) || ps[i].PGID == old {
			left = append(left, strconv.Itoa(ps[i].PID)+" "+strings.Join(ps[i].Argv, " "))
		}
	}
	harness.Equal(checks, "processes of the old calibre-server left after the reload", "none", none(left))
}
