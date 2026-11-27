// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package supervise

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// production holds the numbers the image runs with.
var production = Options{Min: time.Second, Max: time.Minute, ResetAfter: 10 * time.Minute, Grace: 8 * time.Second}

// waitFor bounds, in real time, every wait for the supervisor to act, so a
// test that goes wrong fails instead of hanging.
const waitFor = 10 * time.Second

// TestHelperProcess is not a test. The tests run this binary again as
// their children, with GO_WANT_HELPER_PROCESS=1 and HELPER_MODE naming
// what the child does.
func TestHelperProcess(t *testing.T) {
	t.Parallel()
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	os.Exit(helperMain(os.Getenv("HELPER_MODE")))
}

// helperMain is a child's whole life. When HELPER_READY names a file
// descriptor, the child writes one line to it once it is set up. It exits
// with HELPER_EXIT, or 0, where its mode says it exits.
func helperMain(mode string) int {
	code, _ := strconv.Atoi(os.Getenv("HELPER_EXIT"))
	f := readyFile()
	switch mode {
	case "exit":
		ready(f)
		return 1
	case "exit-now":
		return code
	case "wait":
		term := make(chan os.Signal, 1)
		signal.Notify(term, syscall.SIGTERM)
		ready(f)
		<-term
		return code
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
		ready(f)
		sleepForever()
	case "wait-tree":
		// Ends on SIGTERM, and leaves a second process in its group, which
		// holds the ready pipe open too
		term := make(chan os.Signal, 1)
		signal.Notify(term, syscall.SIGTERM)
		if !startLeaf(f) {
			return 2
		}
		ready(f)
		<-term
		return code
	case "ignore-term-tree":
		// A second process in the child's group, holding the ready pipe
		// open too, as calibre-server's conversion workers would
		signal.Ignore(syscall.SIGTERM)
		if !startLeaf(f) {
			return 2
		}
		ready(f)
		sleepForever()
	case "late-serve":
		return lateServeMain(f, code)
	case "echo":
		if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
			return 2
		}
		return code
	case "hang":
		sleepForever()
	case "stderr-lines":
		// More lines than Exec keeps, then the one that says why, then
		// lines with nothing in them
		w := bufio.NewWriter(os.Stderr)
		for i := range 5000 {
			_, _ = fmt.Fprintf(w, "line %d of what went before\n", i)
		}
		_, _ = w.WriteString("the last line\n\n  \n")
		if w.Flush() != nil {
			return 2
		}
		return code
	case "env":
		// Its environment, one variable to a line, on stdout, or on fd 3
		// when HELPER_OUT says so
		out := os.Stdout
		if os.Getenv("HELPER_OUT") == "3" {
			out = os.NewFile(3, "out")
		}
		if _, err := fmt.Fprintln(out, strings.Join(os.Environ(), "\n")); err != nil {
			return 2
		}
		return 0
	case "hold-stdout":
		// Exits 0 with some output written, while a process it started
		// holds stdout open for a while after
		leaf := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestHelperProcess$") //nolint:gosec // G204: the test binary itself
		leaf.Env = append(os.Environ(), "HELPER_MODE=exit-later", "HELPER_SLEEP_MS=2000")
		leaf.Stdout = os.Stdout
		if err := leaf.Start(); err != nil {
			return 2
		}
		_, _ = fmt.Print("partial")
		return 0
	case "orphan":
		// Starts a process that outlives it, and prints its PID
		leaf := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestHelperProcess$") //nolint:gosec // G204: the test binary itself
		leaf.Env = append(os.Environ(), "HELPER_MODE=exit-later")
		if err := leaf.Start(); err != nil {
			return 2
		}
		_, _ = fmt.Println(leaf.Process.Pid)
		return 0
	case "exit-later":
		ms, err := strconv.Atoi(os.Getenv("HELPER_SLEEP_MS"))
		if err != nil {
			ms = 100
		}
		time.Sleep(time.Duration(ms) * time.Millisecond)
		return 0
	}
	return 2
}

// lateServeMain is the late-serve child, which starts as calibre-server
// 9.19.0 does: each SIGTERM it gets before it serves is lost. It serves
// once the file HELPER_GO names exists, and then makes the file
// HELPER_SERVING names. A SIGTERM while it serves ends it: it removes that
// file at once, and exits HELPER_SLEEP_MS later, as a server takes a while
// to stop. Each SIGTERM it gets adds a line to the file HELPER_TERMS names.
func lateServeMain(f *os.File, code int) int {
	term := make(chan os.Signal, 4)
	signal.Notify(term, syscall.SIGTERM)
	ready(f)
	goFile, serving, terms := os.Getenv("HELPER_GO"), os.Getenv("HELPER_SERVING"), os.Getenv("HELPER_TERMS")
	ms, _ := strconv.Atoi(os.Getenv("HELPER_SLEEP_MS"))
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	var exit <-chan time.Time
	serves, stopping := false, false
	for {
		select {
		case <-term:
			if appendLine(terms) != nil {
				return 2
			}
			if serves && !stopping {
				stopping = true
				if os.Remove(serving) != nil { //nolint:gosec // G703: the test's own file, named in the child's environment by the test
					return 2
				}
				exit = time.After(time.Duration(ms) * time.Millisecond)
			}
		case <-exit:
			return code
		case <-tick.C:
			if _, err := os.Stat(goFile); err == nil && !serves { //nolint:gosec // G703: the test's own file, named in the child's environment by the test
				serves = true
				if os.WriteFile(serving, nil, 0o600) != nil { //nolint:gosec // G703: the test's own file, named in the child's environment by the test
					return 2
				}
			}
		}
	}
}

// appendLine adds one line to the file at path.
func appendLine(path string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // G304: a test's own file
	if err != nil {
		return err
	}
	if _, err := f.WriteString("term\n"); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// lateServe is a late-serve child's files, in a folder of the test's own.
type lateServe struct {
	goFile, serving, terms string
	sleepMS                int
}

// newLateServe is a late-serve child that serves at once when atOnce is
// true, and otherwise once the test calls serve, and that exits sleepMS
// after the SIGTERM that ends it.
func newLateServe(t *testing.T, atOnce bool, sleepMS int) *lateServe {
	t.Helper()
	dir := t.TempDir()
	ls := &lateServe{goFile: dir + "/go", serving: dir + "/serving", terms: dir + "/terms", sleepMS: sleepMS}
	if atOnce {
		if err := os.WriteFile(ls.goFile, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return ls
}

// child is the Child named name that runs ls, with ls.serves as its
// Serves.
func (ls *lateServe) child(name string) Child {
	return Child{
		Name: name,
		Env: []string{
			"HELPER_GO=" + ls.goFile, "HELPER_SERVING=" + ls.serving, "HELPER_TERMS=" + ls.terms,
			"HELPER_SLEEP_MS=" + strconv.Itoa(ls.sleepMS),
		},
		Serves: ls.serves,
	}
}

// serves is the child's Serves. It first waits a fifth of a second, as a
// request made after a SIGTERM reaches a server that has seen it, and then
// looks for the serving file every 10 ms.
func (ls *lateServe) serves(ctx context.Context) error {
	wait := time.NewTimer(200 * time.Millisecond)
	defer wait.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wait.C:
		}
		if _, err := os.Stat(ls.serving); err == nil {
			return nil
		}
		wait.Reset(10 * time.Millisecond)
	}
}

// serve has the child serve, and returns the time it was told to.
func (ls *lateServe) serve(t *testing.T) time.Time {
	t.Helper()
	at := time.Now()
	if err := os.WriteFile(ls.goFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return at
}

// termCount is how many SIGTERMs the child has got.
func (ls *lateServe) termCount(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile(ls.terms)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

// waitFile waits until path exists.
func waitFile(t *testing.T, path string) {
	t.Helper()
	for end := time.Now().Add(waitFor); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatalf("%s never appeared", path)
}

// startLeaf starts a process in the helper's own group that ignores
// SIGTERM and sleeps, holding f as its fd 3.
func startLeaf(f *os.File) bool {
	leaf := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestHelperProcess$") //nolint:gosec // G204: the test binary itself
	leaf.Env = append(os.Environ(), "HELPER_MODE=ignore-term", "HELPER_READY=")
	leaf.ExtraFiles = []*os.File{f}
	return leaf.Start() == nil
}

// readyFile is the file descriptor HELPER_READY names, or nil. One value
// serves every use, so no second one's finalizer closes the descriptor.
func readyFile() *os.File {
	fd, err := strconv.Atoi(os.Getenv("HELPER_READY"))
	if err != nil {
		return nil
	}
	return os.NewFile(uintptr(fd), "ready")
}

func ready(f *os.File) {
	if f != nil {
		_, _ = f.WriteString("ready\n")
	}
}

func sleepForever() {
	for {
		time.Sleep(time.Hour)
	}
}

// helperArgv is the command line that runs this binary as a child.
func helperArgv() []string {
	return []string{os.Args[0], "-test.run=^TestHelperProcess$"}
}

// helperEnv is a child's environment: mode and extra, then the test's.
// They come first because a child reads the first of two values. Built
// for the race detector, a child would wait 1 s at exit for other
// goroutines' races, unless GORACE says otherwise.
func helperEnv(mode string, extra ...string) []string {
	env := append([]string{"GO_WANT_HELPER_PROCESS=1", "HELPER_MODE=" + mode, "GORACE=atexit_sleep_ms=0"}, extra...)
	return append(env, os.Environ()...)
}

// started is one start the supervisor made through a helper.
type started struct {
	name string
	n    int // 1 for the helper's first start
	// pid is the child's PID. The tests signal it by its PID, never
	// through the supervisor's *os.Process, whose Release would race.
	pid int
	// ready is the read end of the child's ready pipe. It reads EOF once
	// every process holding the write end, the child and any process it
	// started, has ended.
	ready *os.File
}

// helper is a start function for New that runs this binary as the child,
// in the mode mode gives for the nth start, through startProcess, as Start
// does. It returns once the child is set up, and reports each start on
// starts.
type helper struct {
	t      *testing.T
	mode   func(n int) string
	gate   func(n int) // when set, called before the nth start
	starts chan started
	n      int // only the supervisor's goroutine touches it
}

func newHelper(t *testing.T, mode func(n int) string) *helper {
	t.Helper()
	return &helper{t: t, mode: mode, starts: make(chan started, 64)}
}

func always(mode string) func(int) string {
	return func(int) string { return mode }
}

func (h *helper) start(child *Child) (*os.Process, error) {
	c := *child // the supervisor's own Child is never changed
	h.n++
	n := h.n
	if h.gate != nil {
		h.gate(n)
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	c.Argv = helperArgv()
	c.Env = helperEnv(h.mode(n), append(c.Env, "HELPER_READY=3")...)
	p, err := startProcess(&c, w)
	_ = w.Close()
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	h.t.Cleanup(func() { _ = r.Close() })
	if _, err := bufio.NewReader(r).ReadString('\n'); err != nil {
		h.t.Errorf("start %d of %s: the child did not get ready: %v", n, c.Name, err)
	}
	h.starts <- started{name: c.Name, n: n, pid: p.Pid, ready: r}
	return p, nil
}

func nextStart(t *testing.T, h *helper) started {
	t.Helper()
	select {
	case s := <-h.starts:
		return s
	case <-time.After(waitFor):
		t.Fatal("the supervisor started nothing")
		return started{}
	}
}

// expectNoStart fails if the supervisor starts anything within a fifth of
// a second.
func expectNoStart(t *testing.T, h *helper) {
	t.Helper()
	select {
	case s := <-h.starts:
		t.Fatalf("an extra start: %s's start %d", s.name, s.n)
	case <-time.After(200 * time.Millisecond):
	}
}

// gone waits for EOF on s's ready pipe: s and every process it started
// have ended.
func gone(t *testing.T, s started) {
	t.Helper()
	if err := s.ready.SetReadDeadline(time.Now().Add(waitFor)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(s.ready); err != nil {
		t.Fatalf("%s's start %d, or a process it started, is still running: %v", s.name, s.n, err)
	}
}

// fakeClock is the supervisor's clock in the tests. Time moves only by
// Advance, and each timer the supervisor sets is reported on waits.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []fakeTimer
	waits  chan time.Duration
}

type fakeTimer struct {
	at time.Time
	c  chan time.Time
}

var t0 = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func newFakeClock() *fakeClock {
	return &fakeClock{now: t0, waits: make(chan time.Duration, 64)}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) After(d time.Duration) <-chan time.Time {
	c := make(chan time.Time, 1)
	f.mu.Lock()
	f.timers = append(f.timers, fakeTimer{at: f.now.Add(d), c: c})
	f.mu.Unlock()
	f.waits <- d
	return c
}

// Advance moves the clock on by d and fires every timer then due.
func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	pending := f.timers[:0]
	for _, t := range f.timers {
		if t.at.After(f.now) {
			pending = append(pending, t)
			continue
		}
		t.c <- f.now
	}
	f.timers = pending
}

func nextWait(t *testing.T, clk *fakeClock) time.Duration {
	t.Helper()
	select {
	case d := <-clk.waits:
		return d
	case <-time.After(waitFor):
		t.Fatal("the supervisor set no timer")
		return 0
	}
}

func expectNoWait(t *testing.T, clk *fakeClock) {
	t.Helper()
	select {
	case d := <-clk.waits:
		t.Fatalf("an extra timer, of %s", d)
	default:
	}
}

// syncBuffer is the supervisor's log in the tests.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// newTest is a supervisor of children, started through h, on a fake clock
// and logging to a buffer.
func newTest(t *testing.T, h *helper, children ...Child) (*Supervisor, *fakeClock, *syncBuffer) {
	t.Helper()
	s := New(children, production, h.start)
	clk, logs := newFakeClock(), &syncBuffer{}
	s.clk, s.logw = clk, logs
	return s, clk, logs
}

// running is a Run in progress.
type running struct {
	cancel context.CancelFunc
	code   chan int
}

func runSupervisor(t *testing.T, s *Supervisor) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	r := &running{cancel: cancel, code: make(chan int, 1)}
	go func() { r.code <- s.Run(ctx) }()
	return r
}

// stop ends Run's context, and returns what Run returns.
func (r *running) stop(t *testing.T) int {
	t.Helper()
	r.cancel()
	return r.wait(t)
}

func (r *running) wait(t *testing.T) int {
	t.Helper()
	select {
	case code := <-r.code:
		return code
	case <-time.After(waitFor):
		t.Fatal("Run did not return")
		return -1
	}
}

// restartAsync calls Restart("a") on a goroutine of its own.
func restartAsync(s *Supervisor) <-chan error {
	errc := make(chan error, 1)
	go func() { errc <- s.Restart("a") }()
	return errc
}

func receive(t *testing.T, errc <-chan error) error {
	t.Helper()
	select {
	case err := <-errc:
		return err
	case <-time.After(waitFor):
		t.Fatal("Restart did not return")
		return errors.New("no answer")
	}
}
