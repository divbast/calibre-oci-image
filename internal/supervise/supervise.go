// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

// Package supervise keeps PID 1's children, calibre-server and
// Calibre-Web, running. A child that ends on its own is started again in
// its place, after a wait that grows each time it ends again, while the
// other child goes on serving. Restart stops and starts a child on
// purpose.
//
// One goroutine collects the exit status of every child process, so PID 1
// also collects the processes the kernel passes to it when their own
// parent ends first. Exec runs a one-off program the same way.
package supervise

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Child is a program the supervisor keeps running.
type Child struct {
	// Name names the child in State, Restart, OnChange and the log:
	// calibre-server or calibre-web.
	Name string
	// Argv is the command line. Argv[0] is the program's path, which is
	// not looked up in PATH.
	Argv []string
	// Env is the child's environment; nil means PID 1's own. Either way,
	// the child never gets PasswordVariables.
	Env []string
	// Before, when set, runs before each start of the child: its first,
	// and each after it ended, whether it crashed or was restarted on
	// purpose. It never runs while the child runs. When it fails, the
	// start fails, and is tried again as any failed start is.
	Before func() error
	// Serves, when set, returns once the child answers a request made
	// after Serves was called, or with ctx's error once ctx ends. After
	// each SIGTERM the supervisor sends the child, for Run's end or for a
	// Restart, it calls Serves, and ends it when the child ends. A child
	// that still answers after a SIGTERM has lost it, and gets one more,
	// once: calibre-server 9.19.0 loses a SIGTERM that comes while it
	// starts, before it serves. A second SIGTERM to a child that acted on
	// the first could cut short how it ends, so a child that does not
	// answer gets none, and Serves first gives such a child time to stop
	// answering, as Answers does.
	Serves func(ctx context.Context) error
}

// Options are the supervisor's times.
type Options struct {
	// Min is the wait before a child that ended on its own is started
	// again. Each time it ends again, the wait doubles, up to Max.
	Min, Max time.Duration
	// ResetAfter is how long a child must have run when it ends on its
	// own for the wait to go back to Min. A planned restart does not end
	// that run: the child was serving until it was stopped.
	ResetAfter time.Duration
	// Grace is how long a child has to end after SIGTERM, when Run stops
	// or Restart stops it, before SIGKILL goes to its process group.
	Grace time.Duration
}

// State is what the supervisor knows of one child.
type State struct {
	// Up is true from the child's start until the supervisor sees it end.
	Up bool `json:"up"`
	// Planned is true from a Restart until the child's next start.
	Planned bool `json:"planned"`
	// Since is the time Up or Planned last changed.
	Since time.Time `json:"since"`
}

// PasswordVariables are the environment variables that carry the first
// accounts' passwords. No child that Start or Exec starts ever gets them.
var PasswordVariables = [...]string{
	"CALIBRE_ADMIN_PASSWORD",
	"CALIBRE_ADMIN_PASSWORD_FILE",
	"CALIBRE_WEB_ADMIN_PASSWORD",
	"CALIBRE_WEB_ADMIN_PASSWORD_FILE",
}

// errStopping answers a Restart that Run's end overtook.
var errStopping = errors.New("supervise: the supervisor is stopping")

// Supervisor keeps its children running until Run's context ends.
type Supervisor struct {
	kids   []*kid
	byName map[string]*kid
	o      Options
	start  func(*Child) (*os.Process, error)

	// The tests replace these.
	clk         clock
	logw        io.Writer
	execStarted func(pid int) // called by Exec between the start and the claim

	exits    chan exit
	ticks    chan tick
	requests chan request
	lost     chan lostTerm
	serving  chan struct{} // closed when Run begins
	stopping chan struct{} // closed when Run's context has ended

	mu       sync.Mutex // guards states and onChange
	states   map[string]State
	onChange func(name string, state State)
}

// kid is a child as Run's goroutine sees it. No other goroutine touches
// one.
type kid struct {
	Child
	proc    *os.Process   // nil while the child is not running
	started time.Time     // when its run began: a planned restart does not end one
	delay   time.Duration // the wait before the start after the next end
	planned bool          // a Restart is under way
	gen     int           // the latest timer's number; a tick with another is stale
	waiting []chan<- error
	watch   context.CancelFunc // ends the Serves call after proc's SIGTERM; nil when none runs
}

// lostTerm is a child's Serves returning after a SIGTERM to p: p lost it.
type lostTerm struct {
	k *kid
	p *os.Process
}

// exit is a child's end, as the reaper saw it.
type exit struct {
	k  *kid
	p  *os.Process
	ws syscall.WaitStatus
}

// tick is a kid's timer firing: the wait before a start has passed, or
// the grace of a Restart's stop.
type tick struct {
	k   *kid
	gen int
}

type request struct {
	name  string
	reply chan<- error
}

type clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// New returns a supervisor of children, which Run starts with start, or
// with Start when start is nil.
func New(children []Child, o Options, start func(*Child) (*os.Process, error)) *Supervisor {
	if start == nil {
		start = Start
	}
	s := &Supervisor{
		kids:     make([]*kid, 0, len(children)),
		byName:   make(map[string]*kid, len(children)),
		o:        o,
		start:    start,
		clk:      realClock{},
		logw:     os.Stderr,
		exits:    make(chan exit),
		ticks:    make(chan tick),
		requests: make(chan request),
		lost:     make(chan lostTerm),
		serving:  make(chan struct{}),
		stopping: make(chan struct{}),
		states:   make(map[string]State, len(children)),
	}
	for _, c := range children {
		k := &kid{Child: c, delay: o.Min}
		s.kids = append(s.kids, k)
		s.byName[c.Name] = k
	}
	return s
}

// Start starts c as New does when given no start function: in a process
// group of its own, so that a signal from a terminal reaches PID 1 alone,
// which passes SIGTERM on itself, and so that SIGKILL to the group also
// ends the processes c starts. Its stdin is /dev/null, its stdout and
// stderr are PID 1's, and its environment is c.Env without
// PasswordVariables.
func Start(c *Child) (*os.Process, error) {
	return startProcess(c)
}

// startProcess is Start, with extra as the child's file descriptors from
// 3 on.
func startProcess(c *Child, extra ...*os.File) (*os.Process, error) {
	if len(c.Argv) == 0 {
		return nil, fmt.Errorf("supervise: %s has no command", c.Name)
	}
	null, err := os.Open(os.DevNull)
	if err != nil {
		return nil, err
	}
	defer func() { _ = null.Close() }()
	return os.StartProcess(c.Argv[0], c.Argv, &os.ProcAttr{
		Env:   childEnv(c.Env),
		Files: append([]*os.File{null, os.Stdout, os.Stderr}, extra...),
		Sys:   &syscall.SysProcAttr{Setpgid: true},
	})
}

// Run starts every child and keeps each running until ctx ends. Then it
// sends SIGTERM to each child, once more to one that lost it (see
// Child.Serves), and SIGKILL to the process group of each still running
// Options.Grace after the first. It returns once every child has ended:
// 0, or 1 when one had to be killed. Run is called once.
func (s *Supervisor) Run(ctx context.Context) (exitCode int) {
	reap.ensure()
	close(s.serving)
	// What watches a child after its SIGTERM outlives ctx, which has
	// ended when a stop begins: it ends when the child does
	watch := context.WithoutCancel(ctx)
	for _, k := range s.kids {
		s.launch(k, false)
	}
	for {
		select {
		case <-ctx.Done():
			return s.stop(watch)
		case e := <-s.exits:
			s.exited(e)
		case t := <-s.ticks:
			s.ticked(t)
		case r := <-s.requests:
			s.restart(watch, r)
		case l := <-s.lost:
			s.resend(l)
		}
	}
}

// Restart stops the child named name with SIGTERM, and starts it again as
// soon as it has ended, with no wait. A child that lost the SIGTERM gets
// one more (see Child.Serves). If it has not ended Options.Grace after the
// first SIGTERM, it and its process group get SIGKILL. If the child is
// waiting to be started again after ending on its own, it is started at
// once instead. A Restart while another is under way, or a child ending
// on its own meanwhile, still gives one start. Restart returns once that
// start is made, with its error, if any.
func (s *Supervisor) Restart(name string) error {
	select {
	case <-s.serving:
	default:
		return errors.New("supervise: Restart before Run")
	}
	reply := make(chan error, 1)
	select {
	case s.requests <- request{name: name, reply: reply}:
	case <-s.stopping:
		return errStopping
	}
	return <-reply
}

// State returns the child named name's state. Before Run, and for a name
// the supervisor does not know, it is the zero State.
func (s *Supervisor) State(name string) State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.states[name]
}

// OnChange has f called after each change of a child's State, with the new
// State, whose Since is the time of the change: when the child is
// started, whether for the first time, after ending on its own, or for a
// Restart; when a Restart begins; and when the child ends. f runs on Run's
// goroutine, one call at a time and in the order of the changes, so it
// must not call Restart.
func (s *Supervisor) OnChange(f func(name string, state State)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onChange = f
}

// launch starts k. A start that fails is treated as an end: k waits, then
// is started again. keepRun is true for the start after a planned stop,
// which goes on with k's run rather than beginning one.
func (s *Supervisor) launch(k *kid, keepRun bool) {
	at := s.clk.Now()
	if k.Before != nil {
		if err := k.Before(); err != nil {
			k.planned = false
			s.setState(k, false, false)
			s.answer(k, err)
			s.logf("%s could not start: %v; trying again in %s", k.Name, err, k.delay)
			s.backoff(k)
			return
		}
	}
	reap.begin()
	p, err := s.start(&k.Child)
	if err != nil {
		reap.abandon()
		k.planned = false
		s.setState(k, false, false)
		s.answer(k, err)
		s.logf("%s could not start: %v; trying again in %s", k.Name, err, k.delay)
		s.backoff(k)
		return
	}
	reap.started()
	status := reap.claim(p.Pid)
	k.proc, k.planned = p, false
	if !keepRun {
		k.started = at
	}
	s.setState(k, true, false)
	s.answer(k, nil)
	go func() {
		ws := <-status
		s.exits <- exit{k: k, p: p, ws: ws}
	}()
}

// exited handles k's end while Run serves.
func (s *Supervisor) exited(e exit) {
	k := e.k
	ended(e.p)
	k.unwatch()
	k.proc = nil
	if k.planned {
		s.setState(k, false, true)
		s.launch(k, true)
		return
	}
	if s.clk.Now().Sub(k.started) >= s.o.ResetAfter {
		k.delay = s.o.Min
	}
	s.setState(k, false, false)
	s.logf("%s %s; starting it again in %s", k.Name, describe(e.ws), k.delay)
	s.backoff(k)
}

// backoff has k started again after its wait, and doubles the next one.
func (s *Supervisor) backoff(k *kid) {
	s.after(k, k.delay)
	k.delay = min(2*k.delay, s.o.Max)
}

// after sets a timer for k, to fire in d, and makes every earlier one
// stale. A tick that is k's latest still acts only on the state it was set
// for: the wait before a start, while k is down, or a Restart's grace,
// while k is stopping for it.
func (s *Supervisor) after(k *kid, d time.Duration) {
	k.gen++
	t := tick{k: k, gen: k.gen}
	fire := s.clk.After(d)
	go func() {
		select {
		case <-fire:
		case <-s.stopping:
			return
		}
		select {
		case s.ticks <- t:
		case <-s.stopping:
		}
	}()
}

func (s *Supervisor) ticked(t tick) {
	k := t.k
	switch {
	case t.gen != k.gen:
	case k.proc == nil:
		s.launch(k, false)
	case k.planned:
		s.logf("%s did not stop within %s; killing it", k.Name, s.o.Grace)
		kill(k.proc)
	}
}

func (s *Supervisor) restart(watch context.Context, r request) {
	k := s.byName[r.name]
	if k == nil {
		r.reply <- fmt.Errorf("supervise: no child is named %q", r.name)
		return
	}
	k.waiting = append(k.waiting, r.reply)
	switch {
	case k.planned:
		// Its stop is under way, and the start after it answers this
		// Restart too
	case k.proc == nil:
		// Waiting to start again after ending on its own: start it now.
		// Its run ended with that end, so this start begins a new one
		k.planned = true
		s.setState(k, false, true)
		s.launch(k, false)
	default:
		k.planned = true
		s.setState(k, true, true)
		s.term(watch, k)
		s.after(k, s.o.Grace)
	}
}

// stop ends every child, and returns Run's exit code.
func (s *Supervisor) stop(watch context.Context) int {
	close(s.stopping)
	running := 0
	for _, k := range s.kids {
		s.answer(k, errStopping)
		if k.proc != nil {
			running++
			s.term(watch, k)
		}
	}
	if running == 0 {
		return 0
	}
	code := 0
	grace := s.clk.After(s.o.Grace)
	for running > 0 {
		select {
		case e := <-s.exits:
			ended(e.p)
			e.k.unwatch()
			e.k.proc = nil
			s.setState(e.k, false, false)
			running--
		case l := <-s.lost:
			s.resend(l)
		case <-grace:
			grace = nil
			code = 1
			for _, k := range s.kids {
				if k.proc != nil {
					s.logf("%s did not stop within %s; killing it", k.Name, s.o.Grace)
					kill(k.proc)
				}
			}
		}
	}
	return code
}

// term sends k SIGTERM. When k has Serves, it is called from now on, with
// a context from watch, in a goroutine of its own, and if it returns while
// k still runs, k lost the SIGTERM, which resend sends again. One call
// watches each process: a second SIGTERM, as when Run ends during a
// Restart's stop, starts no second.
func (s *Supervisor) term(watch context.Context, k *kid) {
	_ = k.proc.Signal(syscall.SIGTERM)
	if k.Serves == nil || k.watch != nil {
		return
	}
	ctx, cancel := context.WithCancel(watch)
	k.watch = cancel
	l := lostTerm{k: k, p: k.proc}
	serves := k.Serves
	go func() {
		if serves(ctx) != nil {
			return
		}
		select {
		case s.lost <- l:
		case <-ctx.Done():
		}
	}()
}

// resend sends SIGTERM once more to the process that answered after it
// lost one, unless it has ended since. A process the reaper has collected
// but whose end Run's goroutine has not yet seen keeps the same window as
// the first SIGTERM has. After a Restart and then Run's end, a process can
// get a SIGTERM from each and one more from resend: that third comes only
// when it still answered, so it had lost one.
func (s *Supervisor) resend(l lostTerm) {
	if l.k.proc == l.p {
		_ = l.p.Signal(syscall.SIGTERM)
	}
}

// unwatch ends the Serves call watching k's process, which has ended.
func (k *kid) unwatch() {
	if k.watch != nil {
		k.watch()
		k.watch = nil
	}
}

// kill sends SIGKILL to p's process group, which also holds the processes
// p started, and to p itself, in case a start gave it no group of its own.
func kill(p *os.Process) {
	if p.Pid > 0 {
		_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
	}
	_ = p.Kill()
}

// ended sends SIGKILL to what is left of the process group of p, which
// has ended, and releases p. A process p started that outlived it, such
// as a calibre-server conversion worker, would otherwise run on beside
// p's next start, and could hold what that start needs.
func ended(p *os.Process) {
	if p.Pid > 0 {
		_ = syscall.Kill(-p.Pid, syscall.SIGKILL) // ESRCH: nothing was left
	}
	_ = p.Release()
}

// childEnv is env, or PID 1's environment when env is nil, without
// PasswordVariables. It is never nil, since a nil Env would give the child
// PID 1's environment whole.
func childEnv(env []string) []string {
	if env == nil {
		env = os.Environ()
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(PasswordVariables[:], name) {
			out = append(out, kv)
		}
	}
	return out
}

// answer replies err to the Restart calls waiting on k's start.
func (s *Supervisor) answer(k *kid, err error) {
	for _, reply := range k.waiting {
		reply <- err
	}
	k.waiting = nil
}

// setState records k's state, when it changed, and tells OnChange's f.
func (s *Supervisor) setState(k *kid, up, planned bool) {
	s.mu.Lock()
	if old := s.states[k.Name]; old.Up == up && old.Planned == planned {
		s.mu.Unlock()
		return
	}
	st := State{Up: up, Planned: planned, Since: s.clk.Now()}
	s.states[k.Name] = st
	f := s.onChange
	s.mu.Unlock()
	if f != nil {
		f(k.Name, st)
	}
}

func (s *Supervisor) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(s.logw, "divbast-calibre: "+format+"\n", args...)
}

// describe says how a process ended.
func describe(ws syscall.WaitStatus) string {
	switch {
	case ws.Exited():
		return fmt.Sprintf("exited with status %d", ws.ExitStatus())
	case ws.Signaled():
		return fmt.Sprintf("was ended by signal %d (%s)", ws.Signal(), ws.Signal())
	default:
		return fmt.Sprintf("ended with wait status %#x", uint32(ws))
	}
}
