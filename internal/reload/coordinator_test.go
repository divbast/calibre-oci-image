// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package reload

import (
	"bytes"
	"context"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"
)

// production holds the numbers the image runs with.
var production = Options{Window: 10 * time.Second, Quiet: 5 * time.Second, Cap: 10 * time.Minute, RetryAfter: 10 * time.Second}

// fakeClock is a clock that moves only when the test says.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	c       *fakeClock
	at      time.Time
	f       func()
	stopped bool
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := !t.stopped
	t.stopped = true
	return was
}

// Advance moves the clock on by d, running each timer that falls due on
// the way, in order, at its own time.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	end := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *fakeTimer
		for _, t := range c.timers {
			if !t.stopped && !t.at.After(end) && (next == nil || t.at.Before(next.at)) {
				next = t
			}
		}
		if next == nil {
			// A timer's function may have moved the clock on further
			if end.After(c.now) {
				c.now = end
			}
			c.mu.Unlock()
			return
		}
		next.stopped = true
		if next.at.After(c.now) {
			c.now = next.at
		}
		c.mu.Unlock()
		next.f()
	}
}

// fakeReloader records each reload, at the clock's time.
type fakeReloader struct {
	clk        *fakeClock
	mu         sync.Mutex
	reconnects []time.Time
	restarts   []time.Time
	// during, when set, runs inside each reload, as the app's own writes
	// would happen
	during func(app App)
}

func (f *fakeReloader) ReconnectCalibreWeb(context.Context) error {
	f.mu.Lock()
	f.reconnects = append(f.reconnects, f.clk.Now())
	during := f.during
	f.mu.Unlock()
	if during != nil {
		during(CalibreWeb)
	}
	return nil
}

func (f *fakeReloader) RestartContentServer(context.Context) error {
	f.mu.Lock()
	f.restarts = append(f.restarts, f.clk.Now())
	during := f.during
	f.mu.Unlock()
	if during != nil {
		during(ContentServer)
	}
	return nil
}

// counts is how many reconnects and restarts there were.
func (f *fakeReloader) counts() (reconnects, restarts int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reconnects), len(f.restarts)
}

func (f *fakeReloader) times() (reconnects, restarts []time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reconnects), slices.Clone(f.restarts)
}

// harness is a Coordinator with a fake clock and a fake Reloader, which
// runs each reload at once, on the goroutine that asked for it.
type harness struct {
	t   *testing.T
	c   *Coordinator
	clk *fakeClock
	r   *fakeReloader
	log *bytes.Buffer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	clk := newFakeClock()
	r := &fakeReloader{clk: clk}
	c := New(clk, r, production)
	log := new(bytes.Buffer)
	c.logw = log
	c.spawn = func(f func()) { f() }
	t.Cleanup(c.Stop)
	return &harness{t: t, c: c, clk: clk, r: r, log: log}
}

// running brings both apps up, as serve does before Watch starts, and
// lets their own starts pass.
func (h *harness) running() {
	for _, app := range []App{ContentServer, CalibreWeb} {
		h.c.Started(app)
		h.c.Accepting(app)
	}
	h.clk.Advance(time.Minute)
}

// change is a change to the library, seen now.
func (h *harness) change() { h.c.DBChanged(h.clk.Now()) }

// write is a request to app that changes things, with a change to the
// library while it runs.
func (h *harness) write(app App) {
	path := "/cdb/set-fields/12"
	if app == CalibreWeb {
		path = "/admin/book/12"
	}
	done := h.c.RequestStarted(app, http.MethodPost, path)
	h.change()
	done(http.StatusOK)
}

// expect checks how many reconnects and restarts there have been.
func (h *harness) expect(reconnects, restarts int) {
	h.t.Helper()
	if gotC, gotR := h.r.counts(); gotC != reconnects || gotR != restarts {
		h.t.Errorf("Calibre-Web reconnected %d times and calibre-server restarted %d times, want %d and %d", gotC, gotR, reconnects, restarts)
	}
}

// TestAttributeOne is a change made by one app: the other alone is
// reloaded, once.
func TestAttributeOne(t *testing.T) {
	t.Parallel()
	t.Run("Calibre-Web's write restarts calibre-server", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.write(CalibreWeb)
		h.clk.Advance(time.Hour)
		h.expect(0, 1)
	})
	t.Run("calibre-server's write reconnects Calibre-Web", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.write(ContentServer)
		h.clk.Advance(time.Hour)
		h.expect(1, 0)
	})
}

// TestAttributeBothOrNone is a change that both apps, or neither, could
// have made: both are reloaded.
func TestAttributeBothOrNone(t *testing.T) {
	t.Parallel()
	t.Run("both", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		doneCS := h.c.RequestStarted(ContentServer, http.MethodPost, "/cdb/set-fields/12")
		doneCW := h.c.RequestStarted(CalibreWeb, http.MethodPost, "/upload")
		h.change()
		doneCS(http.StatusOK)
		doneCW(http.StatusOK)
		h.clk.Advance(time.Hour)
		h.expect(1, 1)
	})
	t.Run("neither", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.change()
		h.clk.Advance(time.Hour)
		h.expect(1, 1)
	})
	t.Run("only requests that change nothing", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		doneCS := h.c.RequestStarted(ContentServer, http.MethodGet, "/ajax/books")
		doneCW := h.c.RequestStarted(CalibreWeb, http.MethodPost, "/advsearch")
		h.change()
		doneCS(http.StatusOK)
		doneCW(http.StatusOK)
		h.clk.Advance(time.Hour)
		h.expect(1, 1)
	})
}

// TestWindow is a change after a write ended: up to 10 s after, it is put
// down to the writer's app; at 10.1 s, to neither.
func TestWindow(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		after                time.Duration
		reconnects, restarts int
	}{
		{9900 * time.Millisecond, 0, 1},
		{10 * time.Second, 0, 1},
		{10100 * time.Millisecond, 1, 1},
	} {
		t.Run(tt.after.String(), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.running()
			done := h.c.RequestStarted(CalibreWeb, http.MethodPost, "/admin/book/12")
			done(http.StatusOK)
			h.clk.Advance(tt.after)
			h.change()
			h.clk.Advance(time.Hour)
			h.expect(tt.reconnects, tt.restarts)
		})
	}
}

// TestQuiet is three changes 2 s apart: one reload of each app, 5 s after
// the last.
func TestQuiet(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.running()
	start := h.clk.Now()
	for range 3 {
		h.change()
		h.clk.Advance(2 * time.Second)
	}
	h.clk.Advance(2900 * time.Millisecond) // 8.9 s after the first change
	h.expect(0, 0)
	h.clk.Advance(time.Hour)
	h.expect(1, 1)
	want := start.Add(9 * time.Second)
	reconnects, restarts := h.r.times()
	if len(reconnects) == 0 || len(restarts) == 0 {
		return
	}
	if !reconnects[0].Equal(want) || !restarts[0].Equal(want) {
		t.Errorf("reloaded at %s and %s, want both 9 s after the first change", reconnects[0].Sub(start), restarts[0].Sub(start))
	}
}

// TestWaitsForConversion is a restart called for while calibre-server is
// busy: it waits while a conversion it saw start runs, and while a request
// to it is open, and goes ahead as soon as neither is left.
func TestWaitsForConversion(t *testing.T) {
	t.Parallel()
	t.Run("a conversion", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.write(CalibreWeb)
		h.c.ConversionStarted(5)
		h.clk.Advance(3 * time.Minute)
		h.expect(0, 0)
		h.c.ConversionDone(5)
		h.expect(0, 1)
	})
	t.Run("a request", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.write(CalibreWeb)
		done := h.c.RequestStarted(ContentServer, http.MethodGet, "/get/EPUB/12")
		h.clk.Advance(3 * time.Minute)
		h.expect(0, 0)
		done(http.StatusOK)
		h.expect(0, 1)
	})
}

// TestCap is a restart whose wait never ends: it goes ahead 10 minutes
// after the change that called for it.
func TestCap(t *testing.T) {
	t.Parallel()
	for name, busy := range map[string]func(h *harness){
		"a conversion that never ends": func(h *harness) { h.c.ConversionStarted(5) },
		"a request that never ends": func(h *harness) {
			h.c.RequestStarted(ContentServer, http.MethodGet, "/get/EPUB/12")
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.running()
			start := h.clk.Now()
			h.write(CalibreWeb)
			busy(h)
			h.clk.Advance(10*time.Minute - time.Millisecond)
			h.expect(0, 0)
			h.clk.Advance(time.Hour)
			h.expect(0, 1)
			if _, restarts := h.r.times(); len(restarts) > 0 && !restarts[0].Equal(start.Add(10*time.Minute)) {
				t.Errorf("restarted %s after the change, want 10m0s", restarts[0].Sub(start))
			}
		})
	}
}

// TestGate is port 8080 while a restart is to come and while it runs:
// first only the requests that change things are refused, then all.
func TestGate(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.running()
	gate := func(method, path string, wantDeny bool, wantReason string) {
		t.Helper()
		deny, after, reason := h.c.Gate(ContentServer, method, path)
		if deny != wantDeny || reason != wantReason {
			t.Errorf("Gate(%s %s) = %t, %q, want %t, %q", method, path, deny, reason, wantDeny, wantReason)
		}
		if deny && after != 10*time.Second {
			t.Errorf("Gate(%s %s) asks to wait %s, want 10s", method, path, after)
		}
	}
	write, read := "/cdb/set-fields/12", "/ajax/books"

	gate(http.MethodPost, write, false, "")
	gate(http.MethodGet, read, false, "")

	h.write(CalibreWeb)
	gate(http.MethodPost, write, true, pendingReason)
	gate(http.MethodGet, read, false, "")
	if deny, _, _ := h.c.Gate(CalibreWeb, http.MethodPost, "/upload"); deny {
		t.Error("a request to Calibre-Web was refused")
	}

	h.clk.Advance(6 * time.Second)
	h.expect(0, 1)
	gate(http.MethodPost, write, true, runningReason)
	gate(http.MethodGet, read, true, runningReason)

	h.c.Started(ContentServer)
	gate(http.MethodGet, read, true, runningReason)
	h.c.Accepting(ContentServer)
	gate(http.MethodPost, write, false, "")
	gate(http.MethodGet, read, false, "")
}

// TestConversionWriteIsContentServers is calibre-server writing a
// conversion's new format, which it does while it answers the status poll
// that first says the conversion is no longer running: only Calibre-Web
// is reloaded, never calibre-server.
func TestConversionWriteIsContentServers(t *testing.T) {
	t.Parallel()
	t.Run("the poll that answers running: false", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.c.Answered(http.MethodPost, "/conversion/start/12", http.StatusOK, nil, []byte("5"), true)
		h.clk.Advance(time.Minute)
		done := h.c.RequestStarted(ContentServer, http.MethodPost, "/conversion/status/5")
		h.c.Answered(http.MethodPost, "/conversion/status/5", http.StatusOK, nil,
			[]byte(`{"running": false, "ok": true, "was_aborted": false, "traceback": "", "log": "done", "size": 100, "fmt": "mobi"}`), true)
		done(http.StatusOK)
		h.clk.Advance(time.Second)
		h.change()
		h.clk.Advance(time.Hour)
		h.expect(1, 0)
	})
	t.Run("a change while a conversion runs", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.c.ConversionStarted(5)
		h.clk.Advance(time.Minute)
		h.change()
		h.clk.Advance(time.Hour)
		h.expect(1, 0)
	})
	t.Run("a change within 10 s of a conversion's end", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.c.ConversionStarted(5)
		h.clk.Advance(time.Minute)
		h.c.ConversionDone(5)
		h.clk.Advance(9 * time.Second)
		h.change()
		h.clk.Advance(time.Hour)
		h.expect(1, 0)
	})
}

// TestOwnStartNotAReload is an app writing to the library while it
// starts, or while it is reloaded: from its start until 10 s after its
// port first accepts a connection, its writes reload no app.
func TestOwnStartNotAReload(t *testing.T) {
	t.Parallel()
	for _, app := range []App{ContentServer, CalibreWeb} {
		t.Run(app.String()+"'s start", func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.running()
			h.c.Started(app)
			h.change()
			h.clk.Advance(time.Minute)
			h.change()
			h.c.Accepting(app)
			h.clk.Advance(9900 * time.Millisecond)
			h.change()
			h.clk.Advance(time.Hour)
			h.expect(0, 0)
			// After that, a change no app made reloads both
			h.change()
			h.clk.Advance(time.Hour)
			h.expect(1, 1)
		})
	}
	t.Run("calibre-server's planned restart", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.r.during = func(app App) {
			if app != ContentServer {
				return
			}
			// What a restart does: it ends, writing as it closes, and starts,
			// writing as it opens
			h.change()
			h.c.Started(ContentServer)
			h.change()
		}
		h.write(CalibreWeb)
		// A read keeps the restart waiting until Calibre-Web's write is
		// more than 10 s old, so the restart's own writes are put down to
		// no request
		done := h.c.RequestStarted(ContentServer, http.MethodGet, "/ajax/books")
		h.clk.Advance(11 * time.Second)
		done(http.StatusOK)
		h.expect(0, 1)
		h.change()
		h.c.Accepting(ContentServer)
		h.clk.Advance(9 * time.Second)
		h.change()
		h.clk.Advance(time.Hour)
		h.expect(0, 1)
	})
	t.Run("Calibre-Web's reconnect", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.r.during = func(app App) {
			if app == CalibreWeb {
				// Past calibre-server's write by more than 10 s
				h.clk.Advance(6 * time.Second)
				h.change()
			}
		}
		h.write(ContentServer)
		h.clk.Advance(6 * time.Second) // reconnects at 5 s, and writes 6 s into it
		h.clk.Advance(9 * time.Second)
		h.change()
		h.clk.Advance(time.Hour)
		h.expect(1, 0)
	})
	t.Run("an edit during a start still counts", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.c.Started(ContentServer)
		h.write(CalibreWeb)
		h.clk.Advance(time.Minute)
		h.expect(0, 1)
	})
}

// TestRestartClearsConversions is calibre-server started again: the
// conversions it was running ended with it, so a restart called for after
// does not wait for them.
func TestRestartClearsConversions(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.running()
	h.c.ConversionStarted(5)
	h.c.Started(ContentServer)
	h.c.Accepting(ContentServer)
	h.clk.Advance(time.Minute)
	h.write(CalibreWeb)
	h.clk.Advance(6 * time.Second)
	h.expect(0, 1)
}

// TestReloadFails is a reload that fails: one line says so.
func TestReloadFails(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.c.r = failingReloader{}
	h.running()
	h.change()
	h.clk.Advance(time.Hour)
	got := h.log.String()
	for _, want := range []string{
		"divbast-calibre: reconnecting Calibre-Web to the library failed: no answer\n",
		"divbast-calibre: restarting calibre-server failed: no start\n",
	} {
		if !bytes.Contains([]byte(got), []byte(want)) {
			t.Errorf("log = %q, want a line %q", got, want)
		}
	}
}

type failingReloader struct{}

func (failingReloader) ReconnectCalibreWeb(context.Context) error {
	return errString("no answer")
}

func (failingReloader) RestartContentServer(context.Context) error {
	return errString("no start")
}

type errString string

func (e errString) Error() string { return string(e) }
