// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package reload

import (
	"context"
	"net"
	"os"
	"slices"
	"sync"
	"time"
)

// fileState is what Watch sees of a file.
type fileState struct {
	there bool
	size  int64
	mtime int64 // nanoseconds since 1970
}

// look returns the state of each file in paths. A file that cannot be
// looked at counts as not there.
func look(paths []string) []fileState {
	out := make([]fileState, 0, len(paths))
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			out = append(out, fileState{})
			continue
		}
		out = append(out, fileState{there: true, size: fi.Size(), mtime: fi.ModTime().UnixNano()})
	}
	return out
}

// Watcher looks at the size and modification time of each of a list of
// files, and calls a function each time it finds any of them changed, or
// a file appeared or gone, with the time it looked. What it sees when it
// is made is the state it compares against first.
type Watcher struct {
	paths   []string
	changed func(time.Time)
	mu      sync.Mutex // held while it looks, and calls changed
	last    []fileState
}

// NewWatcher returns a Watcher of paths, which calls changed.
func NewWatcher(paths []string, changed func(time.Time)) *Watcher {
	return &Watcher{paths: slices.Clone(paths), changed: changed, last: look(paths)}
}

// Look looks once, now, and calls changed before it returns when it finds
// a change: one Look that comes after another's finds the change the
// first found already told.
func (w *Watcher) Look() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if now := look(w.paths); !slices.Equal(now, w.last) {
		w.last = now
		w.changed(time.Now())
	}
}

// Run looks every every, until ctx ends.
func (w *Watcher) Run(ctx context.Context, every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		w.Look()
	}
}

// Watch checks the size and modification time of each file in paths every
// every, until ctx ends. Each time any of them changes, or a file appears
// or goes, it calls changed once, with the time it saw the change. What it
// sees first, when it starts, is the state it compares against.
func Watch(ctx context.Context, paths []string, every time.Duration, changed func(time.Time)) {
	NewWatcher(paths, changed).Run(ctx, every)
}

// WaitAccepting tries to connect to addr every every, and returns once a
// connection is made, or with ctx's error once ctx ends. Each try is given
// every to connect.
func WaitAccepting(ctx context.Context, addr string, every time.Duration) error {
	d := net.Dialer{Timeout: every}
	wait := time.NewTimer(every)
	defer wait.Stop()
	for {
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = conn.Close() // only the connection matters, not what it carries
			return nil
		}
		wait.Reset(every)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wait.C:
		}
	}
}
