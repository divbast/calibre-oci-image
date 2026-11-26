// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

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
)

// reap is the process's one reaper. A process can have only one: two
// goroutines calling Wait4(-1) would each collect the other's children.
var reap = newReaper()

// reaper collects the exit status of every child of the process, with one
// goroutine calling Wait4(-1), and hands each to whoever started it.
//
// A child can end, and be collected, before its starter has claimed its
// PID. So a start is announced with begin, and while any start is in
// progress, a status no one has claimed is kept for its claim. Once no
// start is in progress, the statuses left unclaimed are dropped: they
// belong to processes nobody here started, such as those the kernel
// passes to PID 1 when their parent ends first.
type reaper struct {
	once sync.Once
	wake chan struct{} // a child now exists, for Wait4 to wait for

	mu      sync.Mutex
	waiting map[int]chan syscall.WaitStatus // claimed, and not yet collected
	early   map[int]syscall.WaitStatus      // collected, and not yet claimed
	starts  int                             // starts begun and not yet claimed or abandoned
}

func newReaper() *reaper {
	return &reaper{
		wake:    make(chan struct{}, 1),
		waiting: make(map[int]chan syscall.WaitStatus),
		early:   make(map[int]syscall.WaitStatus),
	}
}

// ensure starts r's goroutine, once.
func (r *reaper) ensure() {
	r.once.Do(func() { go r.run() })
}

func (r *reaper) run() {
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, 0, nil)
		switch {
		case err == nil:
			r.reaped(pid, ws)
		case errors.Is(err, syscall.EINTR):
		default:
			// ECHILD: no child exists until the next start
			<-r.wake
		}
	}
}

// reaped hands pid's status to its claim, or keeps it for one.
func (r *reaper) reaped(pid int, ws syscall.WaitStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.waiting[pid]; ok {
		delete(r.waiting, pid)
		c <- ws
		return
	}
	if r.starts > 0 {
		r.early[pid] = ws
	}
}

// begin announces a start, before the process exists.
func (r *reaper) begin() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts++
}

// started tells r's goroutine that a child exists, once the start has
// made one.
func (r *reaper) started() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// claim ends a start that made pid. The channel it returns receives pid's
// exit status.
func (r *reaper) claim(pid int) <-chan syscall.WaitStatus {
	c := make(chan syscall.WaitStatus, 1)
	r.mu.Lock()
	defer r.mu.Unlock()
	if ws, ok := r.early[pid]; ok {
		delete(r.early, pid)
		c <- ws
	} else {
		r.waiting[pid] = c
	}
	r.end()
	return c
}

// abandon ends a start that made no process.
func (r *reaper) abandon() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.end()
}

// end ends a start, under r.mu.
func (r *reaper) end() {
	r.starts--
	if r.starts == 0 {
		clear(r.early)
	}
}

// stderrKept is how much of a one-off program's stderr Exec keeps: the
// end, where its last line is.
const stderrKept = 64 << 10

// Exec runs argv once, with env (PID 1's own when nil) less
// PasswordVariables, and with stdin, and returns what it wrote to stdout.
// argv[0] is the program's path, which is not looked up in PATH. An exit
// with a status other than 0 is an error naming argv[0]. So is ctx ending
// before the program has exited, which kills it, or before its stdout has
// been read to the end: Exec then returns no output, never part of it.
//
// The program's stderr never reaches PID 1's. Exec keeps its end, and an
// error ends with the last line there that is not empty, so the caller
// reports the failure in one line, and nothing else the program wrote
// reaches the log.
//
// Its exit status comes from the process's one reaper, as the children's
// do. Once Run or Exec has started that reaper, every program PID 1 runs
// must go through Exec: an exec.Cmd's Wait would find its process already
// collected, and fail.
func (s *Supervisor) Exec(ctx context.Context, argv, env []string, stdin []byte) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("supervise: Exec needs a command")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", argv[0], err)
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		closeAll(inR, inW)
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		closeAll(inR, inW, outR, outW)
		return nil, err
	}
	reap.ensure()
	reap.begin()
	p, err := os.StartProcess(argv[0], argv, &os.ProcAttr{Env: childEnv(env), Files: []*os.File{inR, outW, errW}})
	closeAll(inR, outW, errW)
	if err != nil {
		reap.abandon()
		closeAll(inW, outR, errR)
		return nil, err
	}
	reap.started()
	if s.execStarted != nil {
		s.execStarted(p.Pid)
	}
	status := reap.claim(p.Pid)

	go func() {
		_, _ = inW.Write(stdin)
		_ = inW.Close()
	}()
	type read struct {
		b   []byte
		err error // nil once the pipe was read to its end
	}
	out, errs := make(chan read, 1), make(chan read, 1)
	go func() {
		b, err := io.ReadAll(outR)
		out <- read{b, err}
	}()
	go func() {
		t := &tail{max: stderrKept}
		_, err := io.Copy(t, errR)
		errs <- read{t.buf, err}
	}()

	var ws syscall.WaitStatus
	select {
	case ws = <-status:
	case <-ctx.Done():
		_ = p.Kill()
		ws = <-status
	}
	_ = p.Release()
	// A process the program started may still hold stdin, stdout or
	// stderr open
	_ = inW.Close()
	stdout, stderr := drain(ctx, outR, out), drain(ctx, errR, errs)
	why := ""
	if line := lastLine(stderr.b); line != "" {
		why = ": " + line
	}

	switch {
	case ctx.Err() != nil && (!ws.Exited() || stdout.err != nil):
		return nil, fmt.Errorf("%s: %w%s", argv[0], ctx.Err(), why)
	case stdout.err != nil:
		return nil, fmt.Errorf("%s: reading its stdout: %w%s", argv[0], stdout.err, why)
	case !ws.Exited() || ws.ExitStatus() != 0:
		return stdout.b, fmt.Errorf("%s %s%s", argv[0], describe(ws), why)
	}
	return stdout.b, nil
}

// drain waits for the read of r to end, which it does once every process
// holding the pipe's other end has closed it, or until ctx ends, which
// closes r and so ends the read with an error.
func drain[T any](ctx context.Context, r *os.File, done <-chan T) T {
	var got T
	select {
	case got = <-done:
	case <-ctx.Done():
		_ = r.Close()
		got = <-done
	}
	_ = r.Close()
	return got
}

// tail keeps the last max bytes written to it.
type tail struct {
	buf []byte
	max int
}

func (t *tail) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > t.max {
		p = p[len(p)-t.max:]
	}
	if over := len(t.buf) + len(p) - t.max; over > 0 {
		t.buf = t.buf[:copy(t.buf, t.buf[over:])]
	}
	t.buf = append(t.buf, p...)
	return n, nil
}

// lastLine returns the last line in b that is not empty, without the
// white space around it.
func lastLine(b []byte) string {
	lines := strings.Split(string(b), "\n")
	for _, l := range slices.Backward(lines) {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

func closeAll(files ...*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}
