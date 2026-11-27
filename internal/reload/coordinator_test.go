// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package reload

import (
	"bytes"
	"context"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
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
	t       *testing.T
	c       *Coordinator
	clk     *fakeClock
	r       *fakeReloader
	log     *bytes.Buffer
	waiting chan struct{} // a value each time Hold begins to wait
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	clk := newFakeClock()
	r := &fakeReloader{clk: clk}
	c := New(clk, r, production)
	log := new(bytes.Buffer)
	c.logw = log
	c.spawn = func(f func()) { f() }
	waiting := make(chan struct{}, 16)
	c.waits = func() { waiting <- struct{}{} }
	t.Cleanup(c.Stop)
	return &harness{t: t, c: c, clk: clk, r: r, log: log, waiting: waiting}
}

// running brings both apps up, as serve does before Watch starts, and
// lets their own starts pass.
func (h *harness) running() {
	for _, app := range []App{ContentServer, CalibreWeb} {
		h.c.Accepting(app, h.c.Started(app))
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

// TestRefusedWriteNotAnEdit is a request that can change the library,
// answered with an error, then an edit through the other app 1 s later:
// once it has ended with a 4xx or a 5xx, the refused request counts for
// nothing, so only the other app's edit reloads, and an edit through 8080
// is not refused meanwhile. A bot's POST /xmlrpc.php on a public 8083,
// answered 404, must not have calibre-server restarted for an 8080 edit.
// An answer of 2xx or 3xx still counts, as does a refused request still
// open when the change is seen, since its answer is not known yet.
func TestRefusedWriteNotAnEdit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                 string
		first, path          string // the first request's app, by port, and path
		status               int
		open                 bool // the change is seen while the first is still open
		reconnects, restarts int
		paused               bool // 8080 then refuses an edit, a restart being due
	}{
		{"a 404 POST on 8083, then an 8080 edit", "8083", "/xmlrpc.php", http.StatusNotFound, false, 1, 0, false},
		{"a 400 POST on 8083, then an 8080 edit", "8083", "/admin/book/12", http.StatusBadRequest, false, 1, 0, false},
		{"a 500 POST on 8083, then an 8080 edit", "8083", "/admin/book/12", http.StatusInternalServerError, false, 1, 0, false},
		{"a 502 POST on 8083, then an 8080 edit", "8083", "/upload", http.StatusBadGateway, false, 1, 0, false},
		{"a 401 POST on 8080, then an 8083 edit", "8080", "/cdb/set-fields/12", http.StatusUnauthorized, false, 0, 1, true},
		{"a 403 POST on 8080, then an 8083 edit", "8080", "/cdb/set-fields/12", http.StatusForbidden, false, 0, 1, true},
		{"a 200 POST on 8083, then an 8080 edit", "8083", "/xmlrpc.php", http.StatusOK, false, 1, 1, true},
		{"a 302 POST on 8083, then an 8080 edit", "8083", "/admin/book/12", http.StatusFound, false, 1, 1, true},
		{"a 200 POST on 8080, then an 8083 edit", "8080", "/cdb/set-fields/12", http.StatusOK, false, 1, 1, true},
		{"a 404 POST on 8083 still open at its change", "8083", "/admin/book/12", http.StatusNotFound, true, 0, 1, true},
		{"a 401 POST on 8080 still open at its change", "8080", "/cdb/set-fields/12", http.StatusUnauthorized, true, 1, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.running()
			app, other := CalibreWeb, ContentServer
			if tc.first == "8080" {
				app, other = ContentServer, CalibreWeb
			}
			done := h.c.RequestStarted(app, http.MethodPost, tc.path)
			if tc.open {
				h.change()
				done(tc.status)
			} else {
				done(tc.status)
				h.clk.Advance(time.Second)
				h.write(other)
			}
			if deny, _, why := h.c.Gate(ContentServer, http.MethodPost, "/cdb/set-fields/13"); deny != tc.paused {
				t.Errorf("the next edit through 8080: refused %t (%q), want %t", deny, why, tc.paused)
			}
			h.clk.Advance(time.Minute)
			h.expect(tc.reconnects, tc.restarts)
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
	for name, tt := range map[string]struct {
		start func(h *harness)
		tick  func(h *harness) // each minute
	}{
		"a conversion that never ends": {
			start: func(h *harness) { h.c.ConversionStarted(5) },
			// Polled, so it is not left behind
			tick: func(h *harness) { h.poll(5, true) },
		},
		"a request that never ends": {
			start: func(h *harness) { h.c.RequestStarted(ContentServer, http.MethodGet, "/get/EPUB/12") },
			tick:  func(*harness) {},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.running()
			start := h.clk.Now()
			h.write(CalibreWeb)
			tt.start(h)
			for range 9 {
				h.clk.Advance(time.Minute)
				tt.tick(h)
			}
			h.clk.Advance(time.Minute - time.Millisecond)
			h.expect(0, 0)
			h.clk.Advance(time.Hour)
			h.expect(0, 1)
			if _, restarts := h.r.times(); len(restarts) > 0 && !restarts[0].Equal(start.Add(10*time.Minute)) {
				t.Errorf("restarted %s after the change, want 10m0s", restarts[0].Sub(start))
			}
		})
	}
}

// poll is calibre-server answering a status poll for jobID.
func (h *harness) poll(jobID int, running bool) {
	body := `{"running": true, "percent": 0.5, "msg": "Converting"}`
	if !running {
		body = `{"running": false, "ok": true, "was_aborted": false, "traceback": "", "log": "", "size": 100, "fmt": "mobi"}`
	}
	h.c.Answered(http.MethodPost, "/conversion/status/"+strconv.Itoa(jobID), http.StatusOK, nil, []byte(body), true)
}

// TestReconnectCap is calibre-server changing the library every 2 s, so
// that it is never quiet for 5: Calibre-Web still reconnects, 10 minutes
// after the first change.
func TestReconnectCap(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.running()
	start := h.clk.Now()
	for h.clk.Now().Before(start.Add(11 * time.Minute)) {
		h.write(ContentServer)
		h.clk.Advance(2 * time.Second)
	}
	reconnects, restarts := h.r.times()
	if len(reconnects) != 1 || len(restarts) != 0 {
		t.Fatalf("%d reconnects and %d restarts in 11 minutes, want one reconnect", len(reconnects), len(restarts))
	}
	if !reconnects[0].Equal(start.Add(10 * time.Minute)) {
		t.Errorf("reconnected %s after the first change, want 10m0s", reconnects[0].Sub(start))
	}
}

// TestGateLetsConversionPollsThrough is port 8080 while a restart is to
// come: a conversion's status poll still goes on, so the conversion can
// end, and the poll that hears it ended write its format, while other
// requests that change the library are refused. While the restart runs,
// both are refused.
func TestGateLetsConversionPollsThrough(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.running()
	poll, write := "/conversion/status/5", "/cdb/set-fields/12"
	denied := func(path string) bool {
		deny, _, _ := h.c.Gate(ContentServer, http.MethodPost, path)
		return deny
	}
	h.c.ConversionStarted(5)
	h.write(CalibreWeb)
	if denied(poll) || !denied(write) {
		t.Errorf("while a restart is to come: the poll refused %t, the write refused %t; want false, true", denied(poll), denied(write))
	}
	h.clk.Advance(time.Minute)
	h.expect(1, 0) // waiting for the conversion

	done := h.c.RequestStarted(ContentServer, http.MethodPost, poll)
	h.poll(5, false)
	h.change() // the new format
	done(http.StatusOK)
	h.clk.Advance(6 * time.Second)
	h.expect(2, 1) // Calibre-Web sees the format; calibre-server restarts for Calibre-Web's write
	if !denied(poll) || !denied(write) {
		t.Errorf("while the restart runs: the poll refused %t, the write refused %t; want both", denied(poll), denied(write))
	}
}

// TestAbandonedConversion is a conversion whose page was left: calibre-
// server forgets one that no poll has asked about for 360 s, and so does
// the Coordinator; it holds a restart back until then, and no longer.
func TestAbandonedConversion(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		// run starts and polls the conversion, and writes from Calibre-Web,
		// and returns when the conversion was last heard of
		run func(h *harness) time.Time
	}{
		{"left after a poll", func(h *harness) time.Time {
			h.c.Answered(http.MethodPost, "/conversion/start/12", http.StatusOK, nil, []byte("5"), true)
			h.clk.Advance(time.Minute)
			h.poll(5, true)
			last := h.clk.Now()
			h.clk.Advance(10 * time.Second)
			h.write(CalibreWeb)
			return last
		}},
		{"never polled", func(h *harness) time.Time {
			h.c.Answered(http.MethodPost, "/conversion/start/12", http.StatusOK, nil, []byte("5"), true)
			last := h.clk.Now()
			h.clk.Advance(10 * time.Second)
			h.write(CalibreWeb)
			return last
		}},
		{"polled again later", func(h *harness) time.Time {
			h.c.Answered(http.MethodPost, "/conversion/start/12", http.StatusOK, nil, []byte("5"), true)
			h.clk.Advance(290 * time.Second)
			h.write(CalibreWeb)
			h.clk.Advance(10 * time.Second)
			h.poll(5, true)
			return h.clk.Now()
		}},
		{"polled, its start not seen", func(h *harness) time.Time {
			h.write(CalibreWeb)
			h.clk.Advance(time.Second)
			h.poll(7, true)
			return h.clk.Now()
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.running()
			last := tt.run(h)
			h.clk.Advance(last.Add(359 * time.Second).Sub(h.clk.Now()))
			if _, restarts := h.r.counts(); restarts != 0 {
				t.Fatalf("restarted while the conversion was still heard of 359 s ago")
			}
			h.clk.Advance(time.Second)
			_, restarts := h.r.times()
			if len(restarts) != 1 || !restarts[0].Equal(last.Add(360*time.Second)) {
				t.Errorf("restarts at %v, want one 360 s after the conversion was last heard of", restarts)
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

	start := h.c.Started(ContentServer)
	gate(http.MethodGet, read, true, runningReason)
	h.c.Accepting(ContentServer, start)
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
			start := h.c.Started(app)
			h.change()
			h.clk.Advance(time.Minute)
			h.change()
			h.c.Accepting(app, start)
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
		var start int
		h.r.during = func(app App) {
			if app != ContentServer {
				return
			}
			// What a restart does: it ends, writing as it closes, and starts,
			// writing as it opens; all within 10 s of the write that called
			// for it
			h.change()
			start = h.c.Started(ContentServer)
			h.change()
		}
		h.write(CalibreWeb)
		h.clk.Advance(5 * time.Second)
		h.expect(0, 1)
		h.change()
		h.c.Accepting(ContentServer, start)
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
				h.change() // within 10 s of calibre-server's write
			}
		}
		h.write(ContentServer)
		h.clk.Advance(5 * time.Second)
		h.expect(1, 0)
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
	h.c.Accepting(ContentServer, h.c.Started(ContentServer))
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

// TestCrashWhilePending is an app that ends on its own and is started
// again while a reload of it is to come, or while one runs: its new
// process reads the library as it is, so that start is the only one, and
// no other reload follows.
func TestCrashWhilePending(t *testing.T) {
	t.Parallel()
	t.Run("calibre-server, 2 s after the change", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.write(CalibreWeb)
		h.clk.Advance(2 * time.Second)
		start := h.c.Started(ContentServer)
		h.change() // the new process writes as it opens
		h.clk.Advance(time.Second)
		h.c.Accepting(ContentServer, start)
		h.clk.Advance(time.Hour)
		h.expect(0, 0)
	})
	t.Run("calibre-server, 7 s after the first change", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.write(CalibreWeb)
		h.clk.Advance(3 * time.Second)
		h.write(CalibreWeb) // the restart is due 5 s after this, at 8 s
		h.clk.Advance(4 * time.Second)
		start := h.c.Started(ContentServer)
		h.clk.Advance(2 * time.Second)
		h.c.Accepting(ContentServer, start)
		h.clk.Advance(time.Hour)
		h.expect(0, 0)
	})
	t.Run("Calibre-Web, 2 s after the change", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.write(ContentServer)
		h.clk.Advance(2 * time.Second)
		start := h.c.Started(CalibreWeb)
		h.clk.Advance(time.Second)
		h.c.Accepting(CalibreWeb, start)
		h.clk.Advance(time.Hour)
		h.expect(0, 0)
	})
}

// TestGateHoldsUntilTheNewStart is a restart of a calibre-server that was
// still starting: the process being stopped may yet accept a connection,
// but port 8080 refuses every request until the process the restart
// starts accepts.
func TestGateHoldsUntilTheNewStart(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.running()
	slow := h.c.Started(ContentServer) // a start that has not accepted yet
	h.clk.Advance(time.Minute)
	h.write(CalibreWeb)
	h.clk.Advance(5 * time.Second)
	h.expect(0, 1)
	refused := func() bool {
		deny, _, _ := h.c.Gate(ContentServer, http.MethodGet, "/ajax/books")
		return deny
	}
	h.c.Accepting(ContentServer, slow) // the process being stopped
	if !refused() {
		t.Error("port 8080 serves once the process being stopped accepts")
	}
	h.clk.Advance(2 * time.Second)
	start := h.c.Started(ContentServer)
	if !refused() {
		t.Error("port 8080 serves before the new process accepts")
	}
	h.c.Accepting(ContentServer, slow)
	if !refused() {
		t.Error("port 8080 serves after an Accepting of an earlier start")
	}
	h.c.Accepting(ContentServer, start)
	if refused() {
		t.Error("port 8080 refuses requests once the new process accepts")
	}
	h.clk.Advance(time.Hour)
	h.expect(0, 1)
}

// hangingReloader is a Calibre-Web whose first reconnect never answers.
type hangingReloader struct {
	entered chan struct{}
	calls   atomic.Int32
}

func (r *hangingReloader) ReconnectCalibreWeb(ctx context.Context) error {
	n := r.calls.Add(1)
	r.entered <- struct{}{}
	if n == 1 {
		<-ctx.Done()
		return context.Cause(ctx)
	}
	return nil
}

func (*hangingReloader) RestartContentServer(context.Context) error { return nil }

// lineLog is a log that tells of each line.
type lineLog struct {
	mu    sync.Mutex
	b     bytes.Buffer
	lines chan struct{}
}

func (l *lineLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.b.Write(p)
	l.lines <- struct{}{}
	return n, err
}

func (l *lineLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestReconnectTimeout is a Calibre-Web that does not answer its
// reconnect: the reconnect fails 10 s on, with one line, and a later change
// reconnects again.
func TestReconnectTimeout(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	r := &hangingReloader{entered: make(chan struct{}, 4)}
	log := &lineLog{lines: make(chan struct{}, 4)}
	h.c.r, h.c.logw = r, log
	h.c.spawn = func(f func()) { go f() }
	h.running()
	wait := func(ch <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s did not happen", what)
		}
	}

	h.write(ContentServer)
	h.clk.Advance(5 * time.Second)
	wait(r.entered, "the reconnect")
	h.clk.Advance(9900 * time.Millisecond)
	if got := log.String(); got != "" {
		t.Fatalf("the reconnect ended before 10 s: %q", got)
	}
	h.clk.Advance(100 * time.Millisecond)
	wait(log.lines, "the reconnect's failure")
	if want := "divbast-calibre: reconnecting Calibre-Web to the library failed: no answer within 10s\n"; log.String() != want {
		t.Errorf("log = %q, want %q", log.String(), want)
	}

	h.clk.Advance(time.Minute)
	h.write(ContentServer)
	h.clk.Advance(5 * time.Second)
	wait(r.entered, "the next reconnect")
	if n := r.calls.Load(); n != 2 {
		t.Errorf("%d reconnects, want 2", n)
	}
}

// wrote is a request to app that changed things and has ended, its change
// to the library not seen yet.
func (h *harness) wrote(app App) {
	path := "/cdb/set-fields/12"
	if app == CalibreWeb {
		path = "/admin/book/12"
	}
	h.c.RequestStarted(app, http.MethodPost, path)(http.StatusOK)
}

// TestEditDuringReload is a real edit by one app while the other is
// reloaded: an edit made once the reload has begun, or once the new
// process has started, is not one the reload reads, so it reloads that app
// again; an edit made before the new process started is in what it reads,
// so it does not.
func TestEditDuringReload(t *testing.T) {
	t.Parallel()
	t.Run("Calibre-Web edits after the new calibre-server accepts", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		var start int
		h.r.during = func(app App) {
			if app == ContentServer && start == 0 {
				h.change()
				start = h.c.Started(ContentServer)
				h.change()
			}
		}
		h.write(CalibreWeb)
		h.clk.Advance(5 * time.Second)
		h.expect(0, 1)
		h.clk.Advance(time.Second)
		h.c.Accepting(ContentServer, start)
		h.clk.Advance(2 * time.Second) // 3 s into the restart, within its own time
		h.wrote(CalibreWeb)
		h.clk.Advance(500 * time.Millisecond)
		h.change()
		h.clk.Advance(time.Hour)
		h.expect(0, 2)
	})
	t.Run("Calibre-Web edits while the new calibre-server starts", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		var start int
		h.r.during = func(app App) {
			if app == ContentServer && start == 0 {
				start = h.c.Started(ContentServer)
			}
		}
		h.write(CalibreWeb)
		h.clk.Advance(5 * time.Second)
		h.expect(0, 1)
		h.clk.Advance(2 * time.Second)
		h.wrote(CalibreWeb)
		h.clk.Advance(500 * time.Millisecond)
		h.change()
		if deny, _, _ := h.c.Gate(ContentServer, http.MethodGet, "/ajax/books"); !deny {
			t.Error("port 8080 serves while calibre-server restarts")
		}
		h.clk.Advance(time.Second)
		h.c.Accepting(ContentServer, start)
		h.clk.Advance(time.Hour)
		h.expect(0, 2)
	})
	t.Run("Calibre-Web edits before the new calibre-server starts", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.write(CalibreWeb)
		h.clk.Advance(5 * time.Second)
		h.expect(0, 1)
		h.clk.Advance(time.Second)
		h.wrote(CalibreWeb) // while the old process stops
		h.clk.Advance(500 * time.Millisecond)
		h.change()
		h.clk.Advance(time.Second)
		start := h.c.Started(ContentServer)
		h.change()
		h.clk.Advance(time.Second)
		h.c.Accepting(ContentServer, start)
		h.clk.Advance(time.Hour)
		h.expect(0, 1)
	})
	t.Run("calibre-server writes during Calibre-Web's reconnect", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		n := 0
		h.r.during = func(app App) {
			if app == CalibreWeb && n == 0 {
				n++
				h.clk.Advance(time.Second)
				h.wrote(ContentServer)
				h.clk.Advance(500 * time.Millisecond)
				h.change()
			}
		}
		h.write(ContentServer)
		h.clk.Advance(5 * time.Second)
		h.clk.Advance(time.Hour)
		h.expect(2, 0)
	})
}

// TestCrashDuringRestart is the process a restart started ending before
// its port accepts, and the supervisor starting another: only that last
// start opens port 8080 again, and Calibre-Web's edit made after the first
// new start is in what the last one reads, so no other restart follows.
func TestCrashDuringRestart(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.running()
	var first int
	h.r.during = func(app App) {
		if app == ContentServer && first == 0 {
			first = h.c.Started(ContentServer)
		}
	}
	h.write(CalibreWeb)
	h.clk.Advance(5 * time.Second)
	h.expect(0, 1)
	h.clk.Advance(time.Second)
	h.write(CalibreWeb) // a real edit, after the first new start
	h.clk.Advance(time.Second)
	second := h.c.Started(ContentServer)
	refused := func() bool {
		deny, _, _ := h.c.Gate(ContentServer, http.MethodGet, "/")
		return deny
	}
	h.c.Accepting(ContentServer, first)
	if !refused() {
		t.Error("port 8080 serves once the start that ended accepts")
	}
	h.clk.Advance(time.Second)
	h.c.Accepting(ContentServer, second)
	if refused() {
		t.Error("port 8080 refuses requests once the last start accepts")
	}
	h.clk.Advance(time.Hour)
	h.expect(0, 1)
}

// lateReconnect is a Calibre-Web that answers its reconnect just as the
// time for it runs out.
type lateReconnect struct{ clk *fakeClock }

func (r lateReconnect) ReconnectCalibreWeb(context.Context) error {
	r.clk.Advance(reconnectTimeout)
	return nil
}

func (lateReconnect) RestartContentServer(context.Context) error { return nil }

// TestReconnectAnswersAtDeadline is a reconnect that answers 200 at the
// moment its 10 s end: it worked, so nothing is logged.
func TestReconnectAnswersAtDeadline(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.c.r = lateReconnect{h.clk}
	h.running()
	h.write(ContentServer)
	h.clk.Advance(5 * time.Second)
	if got := h.log.String(); got != "" {
		t.Errorf("a reconnect that answered logged %q", got)
	}
}

// hold calls Hold on a goroutine of its own, and returns what it returns.
func (h *harness) hold(ctx context.Context) <-chan error {
	errc := make(chan error, 1)
	go func() { errc <- h.c.Hold(ctx) }()
	return errc
}

// held calls Hold on a goroutine of its own, waits until Hold has done
// what it calls for, a restart among it, and holds the request, and
// returns what it returns.
func (h *harness) held(ctx context.Context) <-chan error {
	h.t.Helper()
	errc := h.hold(ctx)
	select {
	case <-h.waiting:
		return errc
	case err := <-errc:
		h.t.Fatalf("Hold returned %v at once; want it to hold the request", err)
	case <-time.After(10 * time.Second):
		h.t.Fatal("Hold never held the request")
	}
	return errc
}

// returned reports whether errc gives a value within a short real time,
// and the value.
func returned(errc <-chan error) (bool, error) {
	select {
	case err := <-errc:
		return true, err
	case <-time.After(100 * time.Millisecond):
		return false, nil
	}
}

// restarted lets the restart under way end, as the supervisor and serve
// do: calibre-server starts again, and its port accepts.
func (h *harness) restarted() {
	h.c.Accepting(ContentServer, h.c.Started(ContentServer))
}

// TestHold is Calibre-Web's own calibredb asking the Content Server for a
// book, in each state a restart can be in.
func TestHold(t *testing.T) {
	t.Parallel()
	t.Run("no restart to come: it goes on at once", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		if done, err := returned(h.hold(t.Context())); !done || err != nil {
			t.Fatalf("Hold returned %t, %v; want at once, nil", done, err)
		}
		h.expect(0, 0)
	})
	t.Run("a restart to come: it goes at once, and the request after it", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.write(CalibreWeb)
		errc := h.held(t.Context())
		// No time passes: the restart does not wait for the quiet
		h.expect(0, 1)
		if done, _ := returned(errc); done {
			t.Fatal("Hold returned before the new calibre-server took connections")
		}
		h.restarted()
		if done, err := returned(errc); !done || err != nil {
			t.Fatalf("once the new calibre-server takes connections, Hold returned %t, %v; want nil", done, err)
		}
		h.clk.Advance(time.Hour)
		h.expect(0, 1)
	})
	t.Run("a restart to come and a request open: the restart waits for it", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.write(CalibreWeb)
		open := h.c.RequestStarted(ContentServer, http.MethodGet, "/get/cover/1/books")
		errc := h.held(t.Context())
		h.expect(0, 0)
		open(http.StatusOK)
		h.expect(0, 1)
		h.restarted()
		if done, err := returned(errc); !done || err != nil {
			t.Fatalf("Hold returned %t, %v; want nil", done, err)
		}
	})
	t.Run("a restart to come and a conversion running: it goes on at once", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.c.ConversionStarted(3)
		h.write(CalibreWeb)
		if done, err := returned(h.hold(t.Context())); !done || err != nil {
			t.Fatalf("Hold returned %t, %v; want at once, nil", done, err)
		}
		h.clk.Advance(time.Minute)
		if _, restarts := h.r.counts(); restarts != 0 {
			t.Errorf("calibre-server restarted %d times while the conversion ran, want 0", restarts)
		}
	})
	t.Run("a conversion starting while it waits: it goes on", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.write(CalibreWeb)
		open := h.c.RequestStarted(ContentServer, http.MethodGet, "/get/cover/1/books")
		errc := h.held(t.Context())
		h.c.ConversionStarted(3)
		if done, err := returned(errc); !done || err != nil {
			t.Fatalf("once a conversion started, Hold returned %t, %v; want nil", done, err)
		}
		open(http.StatusOK)
		h.clk.Advance(time.Minute)
		if _, restarts := h.r.counts(); restarts != 0 {
			t.Errorf("calibre-server restarted %d times while the conversion ran, want 0", restarts)
		}
	})
	t.Run("a restart under way: it waits for the new calibre-server", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.write(CalibreWeb)
		h.clk.Advance(6 * time.Second)
		h.expect(0, 1)
		errc := h.held(t.Context())
		h.restarted()
		if done, err := returned(errc); !done || err != nil {
			t.Fatalf("Hold returned %t, %v; want nil", done, err)
		}
		h.expect(0, 1)
	})
	t.Run("the client gone: it returns the context's error", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.write(CalibreWeb)
		h.clk.Advance(6 * time.Second)
		ctx, cancel := context.WithCancel(t.Context())
		errc := h.held(ctx)
		cancel()
		if done, err := returned(errc); !done || err == nil {
			t.Fatalf("Hold returned %t, %v; want the context's error", done, err)
		}
	})
	t.Run("the Coordinator stopping: it goes on", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.write(CalibreWeb)
		h.clk.Advance(6 * time.Second)
		errc := h.held(t.Context())
		h.c.Stop()
		if done, err := returned(errc); !done || err != nil {
			t.Fatalf("Hold returned %t, %v; want nil", done, err)
		}
	})
	t.Run("the hurry ends with the restart", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.write(CalibreWeb)
		errc := h.held(t.Context())
		h.restarted()
		if done, _ := returned(errc); !done {
			t.Fatal("Hold never returned")
		}
		// The next change waits for its quiet again
		h.clk.Advance(time.Minute)
		h.write(CalibreWeb)
		h.clk.Advance(4 * time.Second)
		h.expect(0, 1)
		h.clk.Advance(2 * time.Second)
		h.expect(0, 2)
	})
}

// TestWrapperCountsForCalibreWeb is a Kobo's first sync: Calibre-Web
// converts book after book to KEPUB in the background, with no request
// of its own open, each time reading the book first through its own
// calibredb, then writing the new format. A write that falls in
// calibre-server's own start, after a restart, must still restart it
// again, as Calibre-Web's, since its calibredb read came just before.
func TestWrapperCountsForCalibreWeb(t *testing.T) {
	t.Parallel()
	for _, open := range []bool{false, true} {
		name := "a change just after the read"
		if open {
			name = "a change while the read is open"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.running()
			h.write(CalibreWeb)
			h.clk.Advance(6 * time.Second)
			h.expect(0, 1)
			h.restarted()
			// The next conversion, within calibre-server's own start: its
			// read, and its write
			h.clk.Advance(time.Second)
			done := h.c.WrapperStarted()
			h.clk.Advance(500 * time.Millisecond)
			if open {
				h.change()
				done()
			} else {
				done()
				h.clk.Advance(300 * time.Millisecond)
				h.change()
			}
			h.clk.Advance(time.Minute)
			h.expect(0, 2)
		})
	}
}

// TestWrapperReadNotAnEdit is a read of Calibre-Web's own calibredb, a
// download, with no edit in Calibre-Web, and then a change it cannot be
// the cause of. An edit through 8080, while the read is open or within
// 10 s after it, inside or outside calibre-server's own start, is
// calibre-server's alone: Calibre-Web reconnects once, calibre-server is
// not restarted after its own edit, and the next 8080 edit is not
// refused. A change no request made, outside any start, still reloads
// both.
func TestWrapperReadNotAnEdit(t *testing.T) {
	t.Parallel()
	edit := func(h *harness) {
		h.write(ContentServer)
		if deny, _, why := h.c.Gate(ContentServer, http.MethodPost, "/cdb/set-fields/13"); deny {
			h.t.Errorf("the next edit through 8080 was refused: %q", why)
		}
	}
	cases := []struct {
		name                string
		steps               func(h *harness)
		reconnects, restart int
	}{
		{"an edit through 8080, 3 s after the read", func(h *harness) {
			h.c.WrapperStarted()()
			h.clk.Advance(3 * time.Second)
			edit(h)
		}, 1, 0},
		{"an edit through 8080 while the read is open", func(h *harness) {
			done := h.c.WrapperStarted()
			h.clk.Advance(time.Second)
			edit(h)
			done()
		}, 1, 0},
		{"an edit through 8080 in calibre-server's own start", func(h *harness) {
			h.c.Accepting(ContentServer, h.c.Started(ContentServer))
			h.clk.Advance(time.Second)
			h.c.WrapperStarted()()
			h.clk.Advance(time.Second)
			edit(h)
		}, 1, 0},
		{"a change no request made, outside any start", func(h *harness) {
			h.c.WrapperStarted()()
			h.clk.Advance(3 * time.Second)
			h.change()
		}, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.running()
			tc.steps(h)
			h.clk.Advance(time.Minute)
			h.expect(tc.reconnects, tc.restart)
		})
	}
}

// TestHoldPaced is a run of Calibre-Web's reads, each after a change, as
// in a Kobo's first sync: the first read restarts calibre-server at once;
// the next, held 1 s apart, bring no second restart until Quiet after the
// first restart's start, and then exactly one, which lets them all go.
func TestHoldPaced(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.running()
	h.write(CalibreWeb)
	first := h.held(t.Context())
	h.expect(0, 1)
	h.restarted()
	if done, err := returned(first); !done || err != nil {
		t.Fatalf("the first read: Hold returned %t, %v; want nil", done, err)
	}
	var reads []<-chan error
	for i := 1; i <= 4; i++ {
		h.clk.Advance(time.Second)
		h.write(CalibreWeb)
		reads = append(reads, h.held(t.Context()))
		if _, restarts := h.r.counts(); restarts != 1 {
			t.Fatalf("%d s after the restart's start: calibre-server restarted %d times, want 1", i, restarts)
		}
	}
	h.clk.Advance(time.Second)
	h.expect(0, 2)
	h.restarted()
	for i, errc := range reads {
		if done, err := returned(errc); !done || err != nil {
			t.Errorf("read %d: Hold returned %t, %v; want nil", i+1, done, err)
		}
	}
	// The second restart read every change: none is left to restart for
	h.clk.Advance(time.Minute)
	h.expect(0, 2)
}

// TestWaitRestart is a request from inside the container that is not
// known as Calibre-Web's: it waits only while a restart runs, until the
// new calibre-server takes connections, and never while one is to come.
func TestWaitRestart(t *testing.T) {
	t.Parallel()
	wait := func(h *harness, ctx context.Context) <-chan error {
		errc := make(chan error, 1)
		go func() { errc <- h.c.WaitRestart(ctx) }()
		return errc
	}
	t.Run("no restart, or one to come: it goes on at once", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		if done, err := returned(wait(h, t.Context())); !done || err != nil {
			t.Fatalf("with no restart: %t, %v; want at once, nil", done, err)
		}
		h.write(CalibreWeb)
		if done, err := returned(wait(h, t.Context())); !done || err != nil {
			t.Fatalf("with a restart to come: %t, %v; want at once, nil", done, err)
		}
		h.expect(0, 0)
	})
	t.Run("a restart under way: it waits for the new calibre-server", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.write(CalibreWeb)
		h.clk.Advance(6 * time.Second)
		h.expect(0, 1)
		errc := wait(h, t.Context())
		if done, _ := returned(errc); done {
			t.Fatal("WaitRestart returned during the restart")
		}
		h.restarted()
		if done, err := returned(errc); !done || err != nil {
			t.Fatalf("once the new calibre-server takes connections: %t, %v; want nil", done, err)
		}
		h.expect(0, 1)
	})
	t.Run("the client gone: it returns the context's error", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.running()
		h.write(CalibreWeb)
		h.clk.Advance(6 * time.Second)
		ctx, cancel := context.WithCancel(t.Context())
		errc := wait(h, ctx)
		cancel()
		if done, err := returned(errc); !done || err == nil {
			t.Fatalf("WaitRestart returned %t, %v; want the context's error", done, err)
		}
	})
}
