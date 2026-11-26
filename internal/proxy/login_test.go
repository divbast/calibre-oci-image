// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// reader is a fake for the helper that reads the login header's name.
type reader struct {
	mu    sync.Mutex
	calls int
	name  string
	err   error
	hold  chan struct{} // when set, a read waits for it, or for its context
}

func newReader(name string) *reader {
	return &reader{name: name}
}

func (r *reader) set(name string, err error, hold chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.name, r.err, r.hold = name, err, hold
}

func (r *reader) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *reader) read(ctx context.Context) (string, error) {
	r.mu.Lock()
	r.calls++
	name, err, hold := r.name, r.err, r.hold
	r.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return name, err
}

// clock is a time the test moves by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// loaded returns a login header that has read name once.
func loaded(t *testing.T, name string) (*LoginHeader, *reader) {
	t.Helper()
	lh, rd, _ := loadedWithClock(t, name)
	return lh, rd
}

func loadedWithClock(t *testing.T, name string) (*LoginHeader, *reader, *clock) {
	t.Helper()
	rd := newReader(name)
	clk := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	lh := NewLoginHeader(rd.read, 5*time.Second, clk.now)
	if err := lh.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rd.count() != 1 {
		t.Fatalf("Load read %d times, want 1", rd.count())
	}
	return lh, rd, clk
}

func TestLoginHeaderLoad(t *testing.T) {
	t.Parallel()
	lh, _ := loaded(t, " X-User \n")
	if got := lh.Name(); got != "X-User" {
		t.Fatalf("Name = %q", got)
	}
	rd := newReader("")
	rd.set("", errors.New("no helper"), nil)
	bad := NewLoginHeader(rd.read, time.Second, time.Now)
	if err := bad.Load(context.Background()); err == nil {
		t.Fatal("Load hid a failed read")
	}
}

func TestLoginHeaderTimeout(t *testing.T) {
	t.Parallel()
	rd := newReader("X-User")
	rd.set("X-User", nil, make(chan struct{})) // never released
	lh := NewLoginHeader(rd.read, 30*time.Millisecond, time.Now)
	start := time.Now()
	if err := lh.Load(context.Background()); err == nil {
		t.Fatal("Load did not time out")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Load took %v", d)
	}
}

func TestLoginHeaderTimeoutIgnoringContext(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	defer close(release)
	lh := NewLoginHeader(func(context.Context) (string, error) { <-release; return "X-User", nil },
		30*time.Millisecond, time.Now)
	if err := lh.Load(context.Background()); err == nil {
		t.Fatal("Load waited for a read that ignores its context")
	}
}

// waitReads waits until the helper has been called n times.
func waitReads(t *testing.T, rd *reader, n int) {
	t.Helper()
	for range 1000 {
		if rd.count() >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the helper was called %d times, want %d", rd.count(), n)
}

// within waits for ch, or says it did not come.
func within(ch <-chan struct{}, d time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}

func TestLoginHeaderHold(t *testing.T) {
	t.Parallel()
	inPost := make(chan struct{})
	releasePost := make(chan struct{})
	var mu sync.Mutex
	var seen []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		mu.Unlock()
		if r.Method == http.MethodPost {
			close(inPost)
			<-releasePost
		}
	}))
	defer srv.Close()
	b := &backend{srv: srv}
	lh, rd := loaded(t, "X-User")
	h := New(Config{Target: b.target(), Trusted: trustedNet, Login: lh})

	postDone := make(chan struct{})
	go func() {
		do(h, untrustedPeer, "POST", "http://calibre.test/admin/config", nil)
		close(postDone)
	}()
	<-inPost

	// A listed proxy does not wait while the hold is on.
	if w := do(h, trustedPeer, "GET", "http://calibre.test/", http.Header{"X-User": {"alice"}}); w.Code != 200 {
		t.Fatalf("trusted request during the hold: %d", w.Code)
	}

	rd.set("X-New", nil, make(chan struct{}))
	getDone := make(chan struct{})
	go func() {
		do(h, untrustedPeer, "GET", "http://calibre.test/", http.Header{"X-New": {"forged"}})
		close(getDone)
	}()
	if within(getDone, 150*time.Millisecond) {
		t.Fatal("an unlisted request did not wait for the settings request")
	}
	close(releasePost)
	<-postDone
	waitReads(t, rd, 2) // the first read is Load's
	if within(getDone, 150*time.Millisecond) {
		t.Fatal("an unlisted request did not wait for the re-read")
	}
	rd.mu.Lock()
	close(rd.hold)
	rd.mu.Unlock()
	if !within(getDone, 5*time.Second) {
		t.Fatal("the unlisted request never went on")
	}
	mu.Lock()
	defer mu.Unlock()
	last := seen[len(seen)-1]
	if last.Get("X-New") != "" {
		t.Fatalf("the new login header was not removed: %v", last)
	}
	if rd.count() != 2 {
		t.Fatalf("%d reads, want 2", rd.count())
	}
}

func TestLoginHeaderGetDoesNotHold(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	lh, rd := loaded(t, "X-User")
	h := New(Config{Target: b.target(), Trusted: trustedNet, Login: lh})
	for _, c := range []struct{ method, path string }{
		{"GET", "/admin/config"}, {"HEAD", "/admin/config"}, {"POST", "/admin/other"}, {"POST", "/"},
	} {
		do(h, untrustedPeer, c.method, "http://calibre.test"+c.path, nil)
	}
	if rd.count() != 1 {
		t.Fatalf("%d reads, want only Load's", rd.count())
	}
	do(h, untrustedPeer, "POST", "http://calibre.test/admin/configuration", nil)
	do(h, untrustedPeer, "DELETE", "http://calibre.test/admin/config/x", nil)
	do(h, untrustedPeer, "GET", "http://calibre.test/", nil) // waits for the re-reads
	if rd.count() != 3 {
		t.Fatalf("%d reads, want 3", rd.count())
	}
}

func TestLoginHeaderFailClosed(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	lh, rd := loaded(t, "X-User")
	h := New(Config{Target: b.target(), Trusted: trustedNet, Login: lh})
	rd.set("", errors.New("the helper failed"), nil)
	do(h, trustedPeer, "POST", "http://calibre.test/admin/config", nil)
	hits := b.hits.Load()

	w := do(h, untrustedPeer, "GET", "http://calibre.test/", http.Header{"X-User": {"admin"}})
	if w.Code != 503 || w.Header().Get("Retry-After") != "10" ||
		!strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") ||
		w.Body.String() != "Calibre-Web's login settings are being read again; try again shortly\n" {
		t.Fatalf("unlisted request while unknown: %d %v %q", w.Code, w.Header(), w.Body.String())
	}
	if b.hits.Load() != hits {
		t.Fatal("an unlisted request reached upstream while the name was unknown")
	}
}

func TestLoginHeaderRetryOncePerSecond(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	lh, rd, clk := loadedWithClock(t, "X-User")
	h := New(Config{Target: b.target(), Trusted: trustedNet, Login: lh})
	get := func() int {
		return do(h, untrustedPeer, "GET", "http://calibre.test/", http.Header{"X-New": {"forged"}}).Code
	}
	rd.set("", errors.New("the helper failed"), nil)
	do(h, trustedPeer, "POST", "http://calibre.test/admin/config", nil)
	if c := get(); c != 503 || rd.count() != 2 {
		t.Fatalf("at once: status %d, %d reads, want 503 and 2", c, rd.count())
	}
	clk.advance(999 * time.Millisecond)
	if c := get(); c != 503 || rd.count() != 2 {
		t.Fatalf("after 999 ms: status %d, %d reads, want 503 and 2", c, rd.count())
	}
	clk.advance(time.Millisecond)
	if c := get(); c != 503 || rd.count() != 3 {
		t.Fatalf("after 1 s, the helper failing: status %d, %d reads, want 503 and 3", c, rd.count())
	}
	if c := get(); c != 503 || rd.count() != 3 {
		t.Fatalf("straight after: status %d, %d reads, want 503 and 3", c, rd.count())
	}
	clk.advance(time.Second)
	rd.set("X-New", nil, nil)
	if c := get(); c != 200 || rd.count() != 4 {
		t.Fatalf("after 1 s, the helper working: status %d, %d reads, want 200 and 4", c, rd.count())
	}
	if lh.Name() != "X-New" || b.got().Get("X-New") != "" {
		t.Fatalf("name %q; X-New upstream: %q", lh.Name(), b.got().Get("X-New"))
	}
	if c := get(); c != 200 || rd.count() != 4 {
		t.Fatalf("once known: status %d, %d reads, want 200 and 4", c, rd.count())
	}
}

func TestLoginHeaderTrustedUnaffected(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	lh, rd, clk := loadedWithClock(t, "X-User")
	h := New(Config{Target: b.target(), Trusted: trustedNet, Login: lh})
	rd.set("", errors.New("the helper failed"), nil)
	do(h, trustedPeer, "POST", "http://calibre.test/admin/config", nil)

	// While the name is unknown, a listed proxy goes straight through.
	for range 2 {
		w := do(h, trustedPeer, "GET", "http://calibre.test/", http.Header{"X-User": {"alice"}})
		if w.Code != 200 || b.got().Get("X-User") != "alice" {
			t.Fatalf("trusted while unknown: %d %v", w.Code, b.got())
		}
	}

	// While an unlisted request waits for a hung re-read, it still does.
	waitReads(t, rd, 2)
	hold := make(chan struct{})
	defer close(hold)
	rd.set("X-User", nil, hold)
	clk.advance(time.Second)
	go do(h, untrustedPeer, "GET", "http://calibre.test/", nil)
	waitReads(t, rd, 3)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- do(h, trustedPeer, "GET", "http://calibre.test/", http.Header{"X-User": {"bob"}})
	}()
	select {
	case w := <-done:
		if w.Code != 200 || b.got().Get("X-User") != "bob" {
			t.Fatalf("trusted during a re-read: %d %v", w.Code, b.got())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a listed proxy waited")
	}
}
