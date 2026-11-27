// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

// Package reload keeps Calibre's Content Server and Calibre-Web in step
// over the one library they share. When one of them changes the library,
// the other is reloaded: Calibre-Web reconnects to it, and the Content
// Server is restarted.
package reload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// abandonedAfter is how long a conversion stays counted after it was last
// heard of: calibre-server forgets a conversion whose status no poll has
// asked for in 360 s (expire_old_jobs, in src/calibre/srv/convert.py),
// and then never writes its new format.
const abandonedAfter = 360 * time.Second

// reconnectTimeout is how long a reconnect of Calibre-Web may take, as
// long as a read of its login header setting: one that hangs then fails,
// and the next change reconnects again.
const reconnectTimeout = 10 * time.Second

// errReconnectTimeout is why a reconnect that took too long was given up.
var errReconnectTimeout = fmt.Errorf("no answer within %s", reconnectTimeout)

// The reasons a refused request on port 8080 is given.
const (
	pendingReason = "Calibre's library is about to reload a change made in Calibre-Web; changes are paused, try again in 10 seconds."
	runningReason = "Calibre's Content Server is restarting to load a change made in Calibre-Web; try again in 10 seconds."
)

// Clock is the time, and the timers, the Coordinator runs by.
type Clock interface {
	Now() time.Time
	// AfterFunc runs f on its own goroutine once d has passed.
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a timer AfterFunc set.
type Timer interface {
	// Stop keeps the timer's function from running, if it has not yet.
	Stop() bool
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// SystemClock is the computer's own clock.
func SystemClock() Clock { return systemClock{} }

// Reloader reloads each app.
type Reloader interface {
	// ReconnectCalibreWeb has Calibre-Web open the library again. It must
	// return once ctx ends: the Coordinator ends ctx when the reconnect
	// has taken 10 s, and when it stops.
	ReconnectCalibreWeb(ctx context.Context) error
	// RestartContentServer restarts calibre-server, and returns once it
	// has been started again, or once the supervisor stops. The
	// Coordinator sets it no time limit; it ends ctx when it stops.
	RestartContentServer(ctx context.Context) error
}

// Options are the Coordinator's times.
type Options struct {
	// Window is how long after a request that changes things ends a
	// change to the library is still put down to that request's app.
	Window time.Duration
	// Quiet is how long the library must go unchanged before a reload.
	Quiet time.Duration
	// Cap is the longest a reload waits, from the first change that called
	// for it: for the library to go quiet, and, for a restart of
	// calibre-server, for the Content Server to be idle.
	Cap time.Duration
	// RetryAfter is how long a refused request on port 8080 is told to
	// wait.
	RetryAfter time.Duration
}

// Coordinator decides which app to reload after each change to the
// library, and when. It learns of the changes from Watch, of the requests
// from the proxies, and of the apps' starts from the supervisor.
//
// A change is put down to each app with a request that can change the
// library (see Mutating) open when the change is seen, or ended at most
// Window before. A conversion calibre-server runs, or ended at most
// Window before, counts as such a request. A conversion runs from its
// start until a status poll says it no longer runs, until calibre-server
// starts again, or until 360 s after its start or its last poll, when
// calibre-server forgets it. When one app made the change,
// the other is reloaded; when both did, both are. When neither did, but an
// app is starting, or being reloaded, the change is that app's own, and
// reloads nothing: an app's own start lasts until Window after its port
// first accepts a connection, and Calibre-Web's reconnect until Window
// after it ends. Otherwise both apps are reloaded. A start, or a reload,
// reads the library as it is when it begins: within its own time, a write
// the other app ended before then no longer counts, and a start drops a
// reload of that app still to come.
//
// A reload waits until the library has gone Quiet long without a change.
// A restart of calibre-server also waits until no request to it is open
// and no conversion it runs is left. Neither waits longer than Cap after
// the first change that called for it. While a restart is to come, port
// 8080 refuses the requests that can change the library, but a
// conversion's status poll, so that the conversion can end.
type Coordinator struct {
	clk  Clock
	r    Reloader
	o    Options
	ctx  context.Context
	stop context.CancelFunc

	// The tests replace these.
	logw  io.Writer
	spawn func(func()) // runs a reload

	mu           sync.Mutex
	stopped      bool
	apps         [2]appState
	jobs         map[int]*job  // the conversions calibre-server runs, by id
	unread       []time.Time   // the starts of those whose id could not be read
	want         [2]bool       // a reload of each app is to come
	since        [2]time.Time  // when each app's reload was first called for
	quietAt      time.Time     // when the library will have gone Quiet long unchanged
	quiet        bool          // it has, since the last change
	restarting   bool          // from a restart's call until the port accepts again
	restartFrom  int           // calibre-server's starts when the restart was called
	reconnecting bool          // a reconnect is under way
	hurry        bool          // a held request has the restart to come skip the quiet time
	hurryAt      time.Time     // from when it may: Quiet after calibre-server's latest start
	held         chan struct{} // closed when the held requests may go on; nil while none waits
	timer        Timer
	timerAt      time.Time
	timerGen     int // the latest timer's number; an earlier one firing late leaves it be
}

// appState is what the Coordinator knows of one app.
type appState struct {
	requests  int       // requests open
	writes    int       // those of them that can change the library
	wroteAt   time.Time // when the last of those ended
	starting  bool      // started, and its port has not accepted since
	starts    int       // how many times it was started
	startedAt time.Time // when it was last started
	loadedAt  time.Time // when its latest start, or reload, began
	reloading bool      // a reconnect of Calibre-Web is under way
	ownUntil  time.Time // when its own start, or reload, ends
}

// job is a conversion calibre-server runs.
type job struct {
	running bool
	heard   time.Time // its start, or its last poll that said it runs
	doneAt  time.Time
}

// runningAt reports whether j runs at at: it has not ended, and
// calibre-server has not forgotten it.
func (j *job) runningAt(at time.Time) bool {
	return j.running && at.Before(j.heard.Add(abandonedAfter))
}

// actions are the reloads a step calls for.
type actions struct {
	reconnect, restart bool
}

// New returns a Coordinator that reloads the apps with r.
func New(clk Clock, r Reloader, o Options) *Coordinator {
	ctx, stop := context.WithCancel(context.Background())
	return &Coordinator{
		clk: clk, r: r, o: o, ctx: ctx, stop: stop,
		logw:  os.Stderr,
		spawn: func(f func()) { go f() },
		jobs:  make(map[int]*job),
	}
}

// Stop ends the Coordinator's work: a reload under way is cancelled, and
// none starts after.
func (c *Coordinator) Stop() {
	c.mu.Lock()
	c.stopped = true
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	c.release()
	c.mu.Unlock()
	c.stop()
}

// Hold is for a request to the Content Server that Calibre-Web's own
// calibredb makes, to read a book's file or its metadata for Calibre-Web:
// it returns once calibre-server reads the library as it is, or at once
// when the request can go on as it is. Calibre-Web reads the library
// itself, and would otherwise be given the book as calibre-server last
// read it.
//
// While a restart of calibre-server is to come and no conversion runs,
// Hold has the restart go ahead without waiting for the library to go
// Quiet, but no sooner than Quiet after calibre-server's latest start, so
// that a run of reads, each after a change, restarts it at most once each
// Quiet. The restart still waits until no request to the Content Server is
// open, which the held ones are not. While a restart is to come, or under way,
// Hold returns once the new calibre-server takes connections. It returns
// at once when no restart is to come or under way, and while a
// conversion runs, since the restart then waits for the conversion, up to
// Cap; and when a conversion starts while it waits. It returns ctx's
// error when ctx ends first, and nil when the Coordinator stops.
func (c *Coordinator) Hold(ctx context.Context) error {
	c.mu.Lock()
	now := c.clk.Now()
	c.prune(now)
	switch {
	case c.stopped:
		c.mu.Unlock()
		return nil
	case c.restarting:
	case c.want[ContentServer] && !c.converting(now):
		if !c.hurry {
			c.hurry, c.hurryAt = true, c.apps[ContentServer].startedAt.Add(c.o.Quiet)
		}
	default:
		c.mu.Unlock()
		return nil
	}
	if c.held == nil {
		c.held = make(chan struct{})
	}
	held := c.held
	acts := c.step(now)
	c.mu.Unlock()
	c.do(acts)
	select {
	case <-held:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WaitRestart is for any other request to the Content Server from inside
// the container: while a restart of calibre-server runs, it returns once
// the new calibre-server takes connections, so the request waits rather
// than being refused, and it returns at once otherwise. calibredb's first
// request of a call carries no login yet, so Hold cannot know it; only the
// image's own tools reach the Content Server from inside. It returns
// ctx's error when ctx ends first, and nil when the Coordinator stops.
func (c *Coordinator) WaitRestart(ctx context.Context) error {
	c.mu.Lock()
	if c.stopped || !c.restarting {
		c.mu.Unlock()
		return nil
	}
	if c.held == nil {
		c.held = make(chan struct{})
	}
	held := c.held
	c.mu.Unlock()
	select {
	case <-held:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// release lets every held request go on. The caller holds the lock.
func (c *Coordinator) release() {
	if c.held != nil {
		close(c.held)
		c.held = nil
	}
	c.hurry = false
}

// RequestStarted tells the Coordinator that a request to app has gone on,
// with its method and the path the app routes. It returns the function to
// call once the request is over.
func (c *Coordinator) RequestStarted(app App, method, path string) (done func(status int)) {
	writes := Mutating(app, method, path)
	c.mu.Lock()
	a := &c.apps[app]
	a.requests++
	if writes {
		a.writes++
	}
	c.mu.Unlock()
	var once sync.Once
	return func(int) {
		once.Do(func() {
			c.mu.Lock()
			now := c.clk.Now()
			a.requests--
			if writes {
				a.writes--
				a.wroteAt = now
			}
			acts := c.step(now)
			c.mu.Unlock()
			c.do(acts)
		})
	}
}

// WrapperStarted tells the Coordinator that Calibre-Web's own calibredb
// sent a request to the Content Server. It returns the function to call
// once the request is over. Calibre-Web reads a book that way before it
// converts it, and writes the new format after, in the background, with
// no request of its own open, as for each book of a Kobo's first sync.
// So the request counts as one of Calibre-Web's that can change the
// library: a change seen while it is open, or Window after it, is put
// down to Calibre-Web, even within calibre-server's own start.
func (c *Coordinator) WrapperStarted() (done func()) {
	c.mu.Lock()
	a := &c.apps[CalibreWeb]
	a.writes++
	c.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			now := c.clk.Now()
			a.writes--
			a.wroteAt = now
			acts := c.step(now)
			c.mu.Unlock()
			c.do(acts)
		})
	}
}

// DBChanged tells the Coordinator that the library changed, as seen at at.
func (c *Coordinator) DBChanged(at time.Time) {
	c.mu.Lock()
	now := c.clk.Now()
	c.prune(now)
	own := c.own(at)
	editors := c.editors(at, own)
	var reload [2]bool
	switch {
	case editors[ContentServer] || editors[CalibreWeb]:
		for _, app := range []App{ContentServer, CalibreWeb} {
			if editors[app] {
				reload[app.other()] = true
			}
		}
	case own[ContentServer] || own[CalibreWeb]:
	default:
		reload = [2]bool{true, true}
	}
	for app, r := range reload {
		if r && !c.want[app] {
			c.want[app], c.since[app] = true, now
		}
	}
	if c.want[ContentServer] || c.want[CalibreWeb] {
		c.quiet, c.quietAt = false, now.Add(c.o.Quiet)
	}
	acts := c.step(now)
	c.mu.Unlock()
	c.do(acts)
}

// ConversionStarted tells the Coordinator that calibre-server started the
// conversion jobID.
func (c *Coordinator) ConversionStarted(jobID int) {
	c.conversionRuns(jobID)
}

// conversionRuns tells the Coordinator that calibre-server said the
// conversion jobID runs: it starts, or a poll found it running.
func (c *Coordinator) conversionRuns(jobID int) {
	c.mu.Lock()
	now := c.clk.Now()
	c.jobs[jobID] = &job{running: true, heard: now}
	c.converts()
	acts := c.step(now)
	c.mu.Unlock()
	c.do(acts)
}

// conversionUnread tells the Coordinator that calibre-server started a
// conversion whose id could not be read: it counts as running for Cap.
func (c *Coordinator) conversionUnread() {
	c.mu.Lock()
	now := c.clk.Now()
	c.unread = append(c.unread, now)
	c.converts()
	acts := c.step(now)
	c.mu.Unlock()
	c.do(acts)
}

// converts lets the held requests go on, as they would have had the
// conversion that runs now already run when they came, unless a restart
// is under way: the restart to come now waits for the conversion. The
// caller holds the lock.
func (c *Coordinator) converts() {
	if !c.restarting {
		c.release()
	}
}

// ConversionDone tells the Coordinator that calibre-server answered that
// the conversion jobID is no longer running: the request that heard it
// wrote the new format to the library.
func (c *Coordinator) ConversionDone(jobID int) {
	c.mu.Lock()
	now := c.clk.Now()
	c.jobs[jobID] = &job{doneAt: now}
	acts := c.step(now)
	c.mu.Unlock()
	c.do(acts)
}

// Started tells the Coordinator that the supervisor started app, and
// returns the start's number, for Accepting. The new process reads the
// library as it is, so a reload of app still to come is dropped. For
// calibre-server, the conversions it ran ended with it.
func (c *Coordinator) Started(app App) (start int) {
	c.mu.Lock()
	now := c.clk.Now()
	a := &c.apps[app]
	a.starting = true
	a.starts++
	a.startedAt = now
	a.loadedAt = later(a.loadedAt, now)
	c.want[app] = false
	start = a.starts
	if app == ContentServer {
		clear(c.jobs)
		c.unread = nil
	}
	acts := c.step(now)
	c.mu.Unlock()
	c.do(acts)
	return start
}

// Accepting tells the Coordinator that app's loopback port accepted a
// connection, for the first time since its start numbered start. A restart
// of calibre-server ends there, when start is the restart's own. An
// Accepting for a start that a later one has replaced changes nothing.
func (c *Coordinator) Accepting(app App, start int) {
	c.mu.Lock()
	now := c.clk.Now()
	a := &c.apps[app]
	if start != a.starts || (app == ContentServer && c.restarting && start <= c.restartFrom) {
		c.mu.Unlock()
		return
	}
	a.starting = false
	a.ownUntil = later(a.ownUntil, now.Add(c.o.Window))
	if app == ContentServer {
		c.restarting = false
		c.release()
	}
	acts := c.step(now)
	c.mu.Unlock()
	c.do(acts)
}

// Gate says whether a request to app must be refused, for how long, and
// why. While a restart of calibre-server is to come, the requests to it
// that can change the library are refused; while the restart runs, all
// requests to it are.
func (c *Coordinator) Gate(app App, method, path string) (deny bool, retryAfter time.Duration, reason string) {
	if app != ContentServer {
		return false, 0, ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.restarting:
		return true, c.o.RetryAfter, runningReason
	case c.want[ContentServer] && Mutating(app, method, path) && !isStatusPoll(method, path):
		return true, c.o.RetryAfter, pendingReason
	}
	return false, 0, ""
}

// editors is the apps a change seen at at is put down to, own being the
// apps whose own start or reload at falls in. The caller holds the lock.
func (c *Coordinator) editors(at time.Time, own [2]bool) (set [2]bool) {
	for app := range c.apps {
		a := &c.apps[app]
		set[app] = a.writes > 0 || c.counts(App(app), a.wroteAt, at, own)
	}
	if c.converting(at) {
		set[ContentServer] = true
	}
	for _, j := range c.jobs {
		if !j.running && c.counts(ContentServer, j.doneAt, at, own) {
			set[ContentServer] = true
		}
	}
	return set
}

// counts reports whether a write of app that ended at ended still counts
// for a change seen at at: it ended at most Window before, and, when at
// falls in the other app's own start or reload, after that began, since
// what began then reads the library with the write in it. The caller holds
// the lock.
func (c *Coordinator) counts(app App, ended, at time.Time, own [2]bool) bool {
	reloaded := app.other()
	if own[reloaded] && ended.Before(c.apps[reloaded].loadedAt) {
		return false
	}
	return at.Sub(ended) <= c.o.Window
}

// own is the apps whose own start, or reload, a change seen at at falls
// in. The caller holds the lock.
func (c *Coordinator) own(at time.Time) (set [2]bool) {
	for app := range c.apps {
		a := &c.apps[app]
		set[app] = a.starting || a.reloading || !at.After(a.ownUntil)
	}
	return set
}

// converting reports whether a conversion calibre-server runs is left at
// at. The caller holds the lock.
func (c *Coordinator) converting(at time.Time) bool {
	for _, j := range c.jobs {
		if j.runningAt(at) {
			return true
		}
	}
	for _, s := range c.unread {
		if at.Before(s.Add(c.o.Cap)) {
			return true
		}
	}
	return false
}

// prune forgets the conversions that no longer count. The caller holds the
// lock.
func (c *Coordinator) prune(now time.Time) {
	for id, j := range c.jobs {
		if (!j.running && now.Sub(j.doneAt) > c.o.Window) || (j.running && !j.runningAt(now)) {
			delete(c.jobs, id)
		}
	}
	kept := c.unread[:0]
	for _, s := range c.unread {
		if now.Before(s.Add(c.o.Cap)) {
			kept = append(kept, s)
		}
	}
	c.unread = kept
}

// step starts each reload that is due, and sets the timer for the next
// moment one may be. The caller holds the lock, and runs the actions once
// it has let go of it.
func (c *Coordinator) step(now time.Time) (acts actions) {
	if c.stopped {
		return acts
	}
	c.prune(now)
	if !c.quiet && !now.Before(c.quietAt) {
		c.quiet = true
	}
	if c.want[CalibreWeb] && !c.reconnecting && (c.quiet || !now.Before(c.since[CalibreWeb].Add(c.o.Cap))) {
		c.want[CalibreWeb] = false
		c.reconnecting = true
		c.apps[CalibreWeb].reloading = true
		c.apps[CalibreWeb].loadedAt = now
		acts.reconnect = true
	}
	if c.want[ContentServer] && !c.restarting {
		capped := !now.Before(c.since[ContentServer].Add(c.o.Cap))
		idle := c.apps[ContentServer].requests == 0 && !c.converting(now)
		early := c.hurry && !now.Before(c.hurryAt)
		if capped || ((c.quiet || early) && idle) {
			c.want[ContentServer] = false
			c.restarting = true
			c.restartFrom = c.apps[ContentServer].starts
			c.apps[ContentServer].starting = true
			c.apps[ContentServer].loadedAt = now
			acts.restart = true
		}
	}
	c.arm(now)
	return acts
}

// arm sets the timer for the next moment a reload may fall due: the end of
// the quiet time, the cap on a reload's wait, or the moment a conversion
// stops counting. The caller holds the lock.
func (c *Coordinator) arm(now time.Time) {
	var next time.Time
	soonest := func(t time.Time) {
		if t.After(now) && (next.IsZero() || t.Before(next)) {
			next = t
		}
	}
	if (c.want[ContentServer] || c.want[CalibreWeb]) && !c.quiet {
		soonest(c.quietAt)
	}
	if c.want[CalibreWeb] && !c.reconnecting {
		soonest(c.since[CalibreWeb].Add(c.o.Cap))
	}
	if c.want[ContentServer] && !c.restarting {
		soonest(c.since[ContentServer].Add(c.o.Cap))
		if c.hurry {
			soonest(c.hurryAt)
		}
		for _, s := range c.unread {
			soonest(s.Add(c.o.Cap))
		}
		for _, j := range c.jobs {
			if j.running {
				soonest(j.heard.Add(abandonedAfter))
			}
		}
	}
	if c.timer != nil && next.Equal(c.timerAt) {
		return
	}
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	if !next.IsZero() {
		c.timerGen++
		gen := c.timerGen
		c.timerAt = next
		c.timer = c.clk.AfterFunc(next.Sub(now), func() { c.fire(gen) })
	}
}

// fire is the function of the timer numbered gen.
func (c *Coordinator) fire(gen int) {
	c.mu.Lock()
	if gen == c.timerGen {
		c.timer = nil
	}
	acts := c.step(c.clk.Now())
	c.mu.Unlock()
	c.do(acts)
}

// do runs the reloads a step called for.
func (c *Coordinator) do(acts actions) {
	if acts.reconnect {
		c.spawn(c.reconnect)
	}
	if acts.restart {
		c.spawn(c.restart)
	}
}

func (c *Coordinator) reconnect() {
	ctx, cancel := context.WithCancelCause(c.ctx)
	timer := c.clk.AfterFunc(reconnectTimeout, func() { cancel(errReconnectTimeout) })
	err := c.r.ReconnectCalibreWeb(ctx)
	timer.Stop()
	if err != nil && errors.Is(context.Cause(ctx), errReconnectTimeout) {
		err = errReconnectTimeout
	}
	cancel(nil)
	if err != nil && c.ctx.Err() == nil {
		c.logf("reconnecting Calibre-Web to the library failed: %v", err)
	}
	c.mu.Lock()
	now := c.clk.Now()
	c.reconnecting = false
	a := &c.apps[CalibreWeb]
	a.reloading = false
	a.ownUntil = later(a.ownUntil, now.Add(c.o.Window))
	acts := c.step(now)
	c.mu.Unlock()
	c.do(acts)
}

func (c *Coordinator) restart() {
	err := c.r.RestartContentServer(c.ctx)
	if err == nil {
		return // the restart ends once the port accepts
	}
	if c.ctx.Err() == nil {
		c.logf("restarting calibre-server failed: %v", err)
	}
	c.mu.Lock()
	c.restarting = false
	c.release()
	acts := c.step(c.clk.Now())
	c.mu.Unlock()
	c.do(acts)
}

func (c *Coordinator) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(c.logw, "divbast-calibre: "+format+"\n", args...)
}

// later is the later of a and b.
func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
