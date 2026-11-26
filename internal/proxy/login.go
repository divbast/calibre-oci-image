// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package proxy

import (
	"context"
	"strings"
	"sync"
	"time"
)

// retryGap is how soon after one re-read began another may begin, when the
// name is unknown and an unlisted source asks again.
const retryGap = time.Second

// LoginHeader holds the name of the header that Calibre-Web trusts for
// logging in behind a proxy. The name is a setting that an administrator
// changes in Calibre-Web, so it is read at start and again after each
// request that changes the administrator settings.
//
// While such a request runs, and while the name is read again, requests from
// unlisted sources wait, so none of them is sent on with a header that is
// about to be the login header. When a read fails, the name is unknown, and
// unlisted sources are refused until a read works.
type LoginHeader struct {
	base    context.Context // the context of reads that no request owns
	read    func(context.Context) (string, error)
	timeout time.Duration
	now     func() time.Time

	mu        sync.Mutex
	name      string // the last name read, kept after a read fails
	known     bool
	holds     int       // settings requests still running
	reading   bool      // a read loop is running
	dirty     bool      // another read is wanted
	lastStart time.Time // when the latest read began
	settled   chan struct{}
}

// NewLoginHeader makes a LoginHeader that reads the name with read, which
// must stop when its context ends, and gives each read timeout to finish.
func NewLoginHeader(read func(context.Context) (string, error), timeout time.Duration, now func() time.Time) *LoginHeader {
	return &LoginHeader{base: context.Background(), read: read, timeout: timeout, now: now}
}

// Load reads the name once, for the start. An error means the start fails.
func (l *LoginHeader) Load(ctx context.Context) error {
	l.mu.Lock()
	l.lastStart = l.now()
	l.mu.Unlock()
	name, err := l.fetch(ctx)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.finish(name, err)
	return err
}

// Name returns the last name read. It is the empty string when the setting
// is empty, and it is stale while a read has failed.
func (l *LoginHeader) Name() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.name
}

// finish records what a read gave. The caller holds the lock.
func (l *LoginHeader) finish(name string, err error) {
	if err != nil {
		l.known = false
		return
	}
	l.name, l.known = name, true
}

// fetch runs one read, and gives up on it when the timeout ends, even if the
// read function does not notice.
func (l *LoginHeader) fetch(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	type result struct {
		name string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		name, err := l.read(ctx)
		ch <- result{strings.TrimSpace(name), err}
	}()
	select {
	case r := <-ch:
		return r.name, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// open makes sure there is a channel that waiters can wait on. The caller
// holds the lock.
func (l *LoginHeader) open() {
	if l.settled == nil {
		l.settled = make(chan struct{})
	}
}

// release wakes the waiters when nothing is held and nothing is being read.
// The caller holds the lock.
func (l *LoginHeader) release() {
	if l.settled != nil && l.holds == 0 && !l.reading && !l.dirty {
		close(l.settled)
		l.settled = nil
	}
}

// want asks for a read, starting the loop that does it when none runs. The
// caller holds the lock.
func (l *LoginHeader) want() {
	l.dirty = true
	l.open()
	if !l.reading {
		l.reading = true
		go l.loop()
	}
}

func (l *LoginHeader) loop() {
	for {
		l.mu.Lock()
		if !l.dirty {
			l.reading = false
			l.release()
			l.mu.Unlock()
			return
		}
		l.dirty = false
		l.lastStart = l.now()
		l.mu.Unlock()

		name, err := l.fetch(l.base)

		l.mu.Lock()
		l.finish(name, err)
		l.mu.Unlock()
	}
}

// begin starts the hold: a request that changes the settings has started.
func (l *LoginHeader) begin() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.holds++
	l.open()
}

// end stops the hold, because the request that began it is over, and asks
// for the name to be read again. The hold lasts until that read ends.
func (l *LoginHeader) end() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.holds--
	l.want()
}

// admit waits while a hold or a read is open, and then reports whether a
// request from an unlisted source may go on: only when the name is known.
// When it is not, admit starts a read if the last one began at least
// retryGap ago, unless this request already waited for one. It returns false
// when ctx ends first.
func (l *LoginHeader) admit(ctx context.Context) bool {
	waited := false
	for {
		l.mu.Lock()
		ch := l.settled
		if ch == nil {
			break
		}
		l.mu.Unlock()
		waited = true
		select {
		case <-ch:
		case <-ctx.Done():
			return false
		}
	}
	if l.known {
		l.mu.Unlock()
		return true
	}
	if waited || l.now().Sub(l.lastStart) < retryGap {
		l.mu.Unlock()
		return false
	}
	l.want()
	ch := l.settled
	l.mu.Unlock()
	select {
	case <-ch:
	case <-ctx.Done():
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.known
}
