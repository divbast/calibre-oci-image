// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package reload

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// poll is how often the tests have Watch and WaitAccepting look.
const poll = 5 * time.Millisecond

// settle is how long a test waits to be sure nothing more happens: many
// polls.
const settle = 30 * poll

// TestWatch is each way the library's files change: a write to
// metadata.db, an append to its journal, a new WAL file and a journal
// deleted. Each fires once, and nothing fires while nothing changes.
func TestWatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	const db, journal, wal = "metadata.db", "metadata.db-journal", "metadata.db-wal"
	for _, f := range []string{db, journal} {
		if err := root.WriteFile(f, []byte("0123456789"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	fired := make(chan time.Time, 16)
	stopped := make(chan struct{})
	go func() {
		paths := []string{filepath.Join(dir, db), filepath.Join(dir, journal), filepath.Join(dir, wal)}
		Watch(ctx, paths, poll, func(at time.Time) { fired <- at })
		close(stopped)
	}()

	expect := func(what string, n int) {
		t.Helper()
		got := 0
		deadline := time.After(settle)
		for {
			select {
			case <-fired:
				got++
				continue
			case <-deadline:
			}
			break
		}
		if got != n {
			t.Errorf("%s fired %d times, want %d", what, got, n)
		}
	}
	expect("nothing", 0)

	write := func(path string, flag int, b []byte) {
		t.Helper()
		f, err := root.OpenFile(path, flag, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(b); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	// The same size, as SQLite writes a page in place
	write(db, os.O_WRONLY, []byte("abcdefghij"))
	expect("a write to metadata.db", 1)
	write(journal, os.O_WRONLY|os.O_APPEND, []byte("more"))
	expect("an append to the journal", 1)
	write(wal, os.O_WRONLY|os.O_CREATE|os.O_EXCL, []byte("wal"))
	expect("a new WAL file", 1)
	if err := root.Remove(journal); err != nil {
		t.Fatal(err)
	}
	expect("the journal deleted", 1)
	expect("nothing more", 0)

	cancel()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Watch did not return once its context ended")
	}
}

// TestWaitAccepting is a port that is closed at first: WaitAccepting
// returns once something listens on it, and not before; and it returns
// when its context ends.
func TestWaitAccepting(t *testing.T) {
	t.Parallel()
	lc := net.ListenConfig{}
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if cerr := ln.Close(); cerr != nil {
		t.Fatal(cerr)
	}

	got := make(chan error, 1)
	go func() { got <- WaitAccepting(t.Context(), addr, poll) }()
	select {
	case werr := <-got:
		t.Fatalf("WaitAccepting() = %v before anything listened", werr)
	case <-time.After(settle):
	}
	ln, err = lc.Listen(t.Context(), "tcp", addr)
	if err != nil {
		t.Skipf("the port was taken meanwhile: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	select {
	case werr := <-got:
		if werr != nil {
			t.Errorf("WaitAccepting() = %v, want nil", werr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("WaitAccepting did not return once the port accepted")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := WaitAccepting(ctx, "127.0.0.1:1", poll); !errors.Is(err, context.Canceled) {
		t.Errorf("WaitAccepting() with its context ended = %v, want context.Canceled", err)
	}
}

// TestWatcherLook: Look finds a change at once, and tells it before it
// returns; a second Look, and the next look of Run, find nothing new.
func TestWatcherLook(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "metadata.db")
	if err := os.WriteFile(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	calls := 0
	w := NewWatcher([]string{path}, func(time.Time) {
		mu.Lock()
		calls++
		mu.Unlock()
	})
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
	w.Look()
	if got := count(); got != 0 {
		t.Fatalf("a look with no change told %d changes", got)
	}
	if err := os.WriteFile(path, []byte("two, longer"), 0o600); err != nil {
		t.Fatal(err)
	}
	w.Look()
	if got := count(); got != 1 {
		t.Fatalf("Look returned having told %d changes, want 1", got)
	}
	w.Look()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	w.Run(ctx, 5*time.Millisecond)
	if got := count(); got != 1 {
		t.Errorf("a second Look and Run told %d changes in all, want 1", got)
	}
}
