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
	// downWithin bounds how long the shipped probe may take to see an app
	// that was killed: it asks each app, whose port closed with it.
	downWithin = 10 * time.Second
	// reloadWithin bounds a planned reload of calibre-server: PID 1 waits
	// for 5 s with no change to the library after the 10 s in which it
	// tells whose change it was, then restarts it.
	reloadWithin = 60 * time.Second
	// zombieWithin bounds how long an ended process may stay a zombie
	// before PID 1 collects it.
	zombieWithin = 10 * time.Second
)

// started returns when c's engine last started it, and fails t if it is
// not running.
func started(t *testing.T, c *harness.Container) time.Time {
	t.Helper()
	in, err := c.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !in.State.Running {
		t.Fatalf("%s is %s, not running", c, in.State.Status)
	}
	return in.State.StartedAt
}

// inPlace checks that c is the same container, started by the engine at
// since and never again, with PID 1 still the image's command.
func inPlace(t *testing.T, c *harness.Container, since time.Time) {
	t.Helper()
	checks := harness.NewChecks(t)
	harness.Equal(checks, "the engine never restarted the container", since, started(t, c))
	ps, err := processes(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	pid1 := "none"
	for i := range ps {
		if ps[i].PID == 1 {
			pid1 = strings.Join(ps[i].Argv, " ")
		}
	}
	harness.Equal(checks, "PID 1 is still the image's command", strings.Join(serverArgv, " "), pid1)
}

// TestCrashRestartsInPlace kills each app in turn with SIGKILL, as a crash
// ends it: the shipped probe reports the image unhealthy, then PID 1
// starts the app again, as a new process, in the same container, and the
// probe reports it healthy.
func TestCrashRestartsInPlace(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	ctx := t.Context()
	sec := newSecrets(t)
	s := (&launch{env: sec.env(), mounts: append(newStore(t, e).mounts(), sec.mount()), passwords: sec.all()}).serve(t, e, image)
	since := started(t, s.C)
	checks := harness.NewChecks(t)
	for _, app := range apps {
		old, err := appPID(ctx, s.C, app)
		if err != nil {
			t.Fatal(err)
		}
		kill(t, s.C, old)
		err = waitHealth(ctx, s.C, false, downWithin)
		checks.True(app+" killed: the probe reports the image unhealthy", err == nil, fmt.Sprint(err))
		err = waitHealth(ctx, s.C, true, startWithin)
		checks.True(app+" killed: the probe reports the image healthy again", err == nil, fmt.Sprint(err))
		pid, err := appPID(ctx, s.C, app)
		checks.True(app+" killed: it runs again, as a new process", err == nil && pid != old, fmt.Sprintf("PID %d, then %d: %v", old, pid, err))
	}
	inPlace(t, s.C, since)
}

// TestCalibreWebButtons presses Calibre-Web's own Restart, then its own
// Shutdown, on its admin page. Restart starts Calibre-Web again in the
// same process; after Shutdown, PID 1 starts it again, as a new process.
// Each time the probe reports the image healthy again, in the same
// container.
func TestCalibreWebButtons(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	ctx := t.Context()
	sec := newSecrets(t)
	s := (&launch{env: sec.env(), mounts: append(newStore(t, e).mounts(), sec.mount()), passwords: sec.all()}).serve(t, e, image)
	since := started(t, s.C)
	checks := harness.NewChecks(t)
	w := loginAdmin(t, s, sec.web)
	before, err := appPID(ctx, s.C, calibreWeb)
	if err != nil {
		t.Fatal(err)
	}
	press := func(what string, parameter int) {
		t.Helper()
		if _, gerr := w.get(ctx, "/admin/view", nil); gerr != nil {
			t.Fatal(gerr)
		}
		resp, perr := w.postJSON(ctx, "/shutdown", map[string]int{"parameter": parameter})
		harness.Answers(t, what+": Calibre-Web answers the button", http.StatusOK, resp, perr)
	}

	press("Restart", 0)
	_, err = waitLog(ctx, s.C, regexp.MustCompile("Performing restart of Calibre-Web"), startWithin)
	checks.True("Restart: Calibre-Web restarts itself", err == nil, fmt.Sprint(err))
	err = waitHealth(ctx, s.C, true, startWithin)
	checks.True("Restart: the probe reports the image healthy again", err == nil, fmt.Sprint(err))
	pid, err := appPID(ctx, s.C, calibreWeb)
	checks.True("Restart: Calibre-Web keeps its PID", err == nil && pid == before, fmt.Sprintf("PID %d, then %d: %v", before, pid, err))

	w = loginAdmin(t, s, sec.web)
	press("Shutdown", 1)
	pid, err = waitNewPID(ctx, s.C, calibreWeb, before, startWithin)
	checks.True("Shutdown: PID 1 starts Calibre-Web again, as a new process", err == nil, fmt.Sprintf("PID %d, then %d: %v", before, pid, err))
	harness.LogContains(t, s.C, "Shutdown: PID 1 says Calibre-Web ended, and starts it again", "divbast-calibre: calibre-web exited with status 0; starting it again")
	err = waitHealth(ctx, s.C, true, startWithin)
	checks.True("Shutdown: the probe reports the image healthy again", err == nil, fmt.Sprint(err))
	inPlace(t, s.C, since)
}

// TestStop stops a serving container as a user does, with <engine> stop
// -t 10: PID 1 stops both apps within its grace, and exits 0.
func TestStop(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	sec := newSecrets(t)
	s := (&launch{env: sec.env(), mounts: append(newStore(t, e).mounts(), sec.mount()), passwords: sec.all()}).serve(t, e, image)
	took, code := stop(t, e, s.C)
	checks := harness.NewChecks(t)
	harness.Equal(checks, "the container exits 0", 0, code)
	checks.True("the stop takes under "+stopWithin.String(), took < stopWithin, took.String())
}

// serveLibrary serves the image on the fixture library, owned by uid
// 65532, and returns it with an admin's session with Calibre-Web.
func serveLibrary(t *testing.T, e harness.Engine, image string) (*harness.Serving, *webSession) {
	t.Helper()
	sec := newSecrets(t)
	books := library(t, e, image, 65532)
	s := (&launch{
		env:       sec.env(),
		mounts:    []harness.Mount{books, volume(t, e, configDir), sec.mount()},
		passwords: sec.all(),
	}).serve(t, e, image)
	return s, loginAdmin(t, s, sec.web)
}

// reload edits the fixture book's title in Calibre-Web, and returns
// calibre-server's new PID once PID 1 has restarted it to read the change.
func reload(t *testing.T, s *harness.Serving, w *webSession, old int) int {
	t.Helper()
	editTitle(t, w, 1, "Edited in Calibre-Web "+random(t, 6, lower))
	pid, err := waitNewPID(t.Context(), s.C, contentServer, old, reloadWithin)
	if err != nil {
		t.Fatalf("PID 1 never restarted calibre-server after an edit in Calibre-Web: %v", err)
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
	ctx := t.Context()
	s, w := serveLibrary(t, e, image)
	old, err := appPID(ctx, s.C, contentServer)
	if err != nil {
		t.Fatal(err)
	}
	kill(t, s.C, old)
	crashed, err := waitNewPID(ctx, s.C, contentServer, old, startWithin)
	if err != nil {
		t.Fatal(err)
	}
	if err = waitHealth(ctx, s.C, true, startWithin); err != nil {
		t.Fatal(err)
	}
	reload(t, s, w, crashed)
	if err = waitHealth(ctx, s.C, true, startWithin); err != nil {
		t.Fatal(err)
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
	harness.NewChecks(t).True("no process is a zombie after a crash and a planned reload", err == nil, fmt.Sprintf("zombies: %s: %v", none(zombies), err))
}

// TestNoStrayProcesses has PID 1 restart calibre-server, after an edit in
// Calibre-Web, as it does to load each change Calibre-Web makes: no
// process of the old calibre-server is left, neither the server itself
// nor the worker it ran, in its process group.
func TestNoStrayProcesses(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	ctx := t.Context()
	s, w := serveLibrary(t, e, image)
	old, err := appPID(ctx, s.C, contentServer)
	if err != nil {
		t.Fatal(err)
	}
	ps, err := processes(ctx, s.C)
	if err != nil {
		t.Fatal(err)
	}
	var group []int
	for i := range ps {
		if ps[i].PGID == old {
			group = append(group, ps[i].PID)
		}
	}
	checks := harness.NewChecks(t)
	checks.True("before the reload, calibre-server's process group holds the server and its worker", len(group) >= 2, fmt.Sprint(group))

	reload(t, s, w, old)
	ps, err = processes(ctx, s.C)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for i := range ps {
		if slices.Contains(group, ps[i].PID) || ps[i].PGID == old {
			left = append(left, strconv.Itoa(ps[i].PID)+" "+strings.Join(ps[i].Argv, " "))
		}
	}
	harness.Equal(checks, "processes of the old calibre-server left after the reload", "none", none(left))
}
