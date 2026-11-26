// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// observed is what an Observe function was called with.
type observed struct {
	mu     sync.Mutex
	calls  int
	status int
	header http.Header
	body   []byte
	whole  bool
	done   chan struct{}
}

func newObserved() *observed { return &observed{done: make(chan struct{}, 1)} }

func (o *observed) seen(status int, header http.Header, body []byte, whole bool) {
	o.mu.Lock()
	o.calls++
	o.status, o.header, o.body, o.whole = status, header, bytes.Clone(body), whole
	o.mu.Unlock()
	o.done <- struct{}{}
}

// wait waits for the call, in real time.
func (o *observed) wait(t *testing.T) {
	t.Helper()
	select {
	case <-o.done:
	case <-time.After(10 * time.Second):
		t.Fatal("Observe's function was not called")
	}
}

// observing is Hooks whose Observe picks every request for o, and whose
// Failed records each error.
func observing(o *observed, failed *[]string, mu *sync.Mutex) Hooks {
	return Hooks{
		Observe: func(*http.Request) func(int, http.Header, []byte, bool) { return o.seen },
		Failed: func(r *http.Request, err error) {
			mu.Lock()
			defer mu.Unlock()
			*failed = append(*failed, r.URL.Path+": "+err.Error())
		},
	}
}

func target(t *testing.T, h http.Handler) *url.URL {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// TestObserveStreams is an answer that comes in two parts, the second only
// once the client has the first: the client gets the first part while the
// app still holds the second, so the copy holds nothing back, and Observe's
// function gets the whole body once it is over.
func TestObserveStreams(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	u := target(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Test", "yes")
		_, _ = io.WriteString(w, "first\n")
		_ = http.NewResponseController(w).Flush() // the test checks what arrives
		<-release
		_, _ = io.WriteString(w, "second\n")
	}))
	o := newObserved()
	var failed []string
	var mu sync.Mutex
	front := httptest.NewServer(New(&Config{Target: u, Hooks: observing(o, &failed, &mu)}))
	t.Cleanup(front.Close)

	// The app sends its second part once the client has the first, or, if
	// the first does not arrive, after 10 s, so that a proxy that holds the
	// body back fails the test instead of hanging it
	var once sync.Once
	var released atomic.Bool
	free := func() {
		once.Do(func() {
			released.Store(true)
			close(release)
		})
	}
	timer := time.AfterFunc(10*time.Second, free)
	defer timer.Stop()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, front.URL+"/conversion/status/5", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	br := bufio.NewReader(resp.Body)
	first, err := br.ReadString('\n')
	if err != nil || first != "first\n" {
		t.Fatalf("first part = %q, %v", first, err)
	}
	if released.Load() {
		t.Fatal("the first part reached the client only once the app had sent the second")
	}
	o.mu.Lock()
	calls := o.calls
	o.mu.Unlock()
	if calls != 0 {
		t.Error("Observe's function ran before the answer was over")
	}
	free()
	rest, err := io.ReadAll(br)
	if err != nil || string(rest) != "second\n" {
		t.Fatalf("rest = %q, %v", rest, err)
	}
	o.wait(t)
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.calls != 1 || o.status != http.StatusOK || string(o.body) != "first\nsecond\n" || !o.whole || o.header.Get("X-Test") != "yes" {
		t.Errorf("Observe's function got %d calls, status %d, body %q, whole %t, X-Test %q; want 1, 200, the body, true, yes",
			o.calls, o.status, o.body, o.whole, o.header.Get("X-Test"))
	}
	if len(failed) != 0 {
		t.Errorf("Failed ran: %q", failed)
	}
}

// TestObserveLimit is an answer longer than ObserveLimit: the client gets
// all of it, and Observe's function gets the first ObserveLimit bytes, and
// is told they are not the whole body.
func TestObserveLimit(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("0123456789abcdef", ObserveLimit/16+10)
	u := target(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	o := newObserved()
	var failed []string
	var mu sync.Mutex
	h := New(&Config{Target: u, Hooks: observing(o, &failed, &mu)})
	w := do(h, untrustedPeer, http.MethodPost, "http://calibre.test/conversion/status/5", nil)
	if w.Body.String() != body {
		t.Fatalf("the client got %d bytes, want %d", w.Body.Len(), len(body))
	}
	o.wait(t)
	if o.calls != 1 || len(o.body) != ObserveLimit || string(o.body) != body[:ObserveLimit] || o.whole {
		t.Errorf("Observe's function got %d calls and %d bytes, whole %t; want 1, the first %d, false", o.calls, len(o.body), o.whole, ObserveLimit)
	}
}

// TestObserveNotPicked is Observe returning no function: nothing is kept,
// and the request is served as without it.
func TestObserveNotPicked(t *testing.T) {
	t.Parallel()
	u := target(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "upstream")
	}))
	asked := 0
	h := New(&Config{Target: u, Hooks: Hooks{Observe: func(*http.Request) func(int, http.Header, []byte, bool) {
		asked++
		return nil
	}}})
	if w := do(h, untrustedPeer, http.MethodGet, "http://calibre.test/x", nil); w.Code != 200 || w.Body.String() != "upstream" || asked != 1 {
		t.Errorf("status %d, body %q, Observe asked %d times", w.Code, w.Body.String(), asked)
	}
}

// brokenOff is an app that promises 1000 bytes, sends 10, and hangs up.
func brokenOff(t *testing.T) *url.URL {
	t.Helper()
	return target(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "0123456789")
		_ = http.NewResponseController(w).Flush() // the test checks what arrives
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
}

// TestObserveBrokenOff is an answer that breaks off: Observe's function is
// told the copy is not the whole body, and Failed runs once, with the path
// as the app routes it. Through a server, which aborts the response, and
// straight to the handler, which does not.
func TestObserveBrokenOff(t *testing.T) {
	t.Parallel()
	t.Run("through a server", func(t *testing.T) {
		t.Parallel()
		o := newObserved()
		var failed []string
		var mu sync.Mutex
		front := httptest.NewServer(New(&Config{Target: brokenOff(t), Hooks: observing(o, &failed, &mu)}))
		t.Cleanup(front.Close)
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, front.URL+"//conversion/start/12", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, err = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
		if err == nil {
			t.Error("the client read a whole body from an answer that broke off")
		}
		o.wait(t)
		mu.Lock()
		defer mu.Unlock()
		if string(o.body) != "0123456789" || o.whole || len(failed) != 1 || !strings.HasPrefix(failed[0], "/conversion/start/12: ") {
			t.Errorf("Observe's function got %q, whole %t; Failed ran with %q", o.body, o.whole, failed)
		}
	})
	t.Run("straight to the handler", func(t *testing.T) {
		t.Parallel()
		o := newObserved()
		var failed []string
		var mu sync.Mutex
		h := New(&Config{Target: brokenOff(t), Hooks: observing(o, &failed, &mu)})
		do(h, untrustedPeer, http.MethodPost, "http://calibre.test/conversion/start/12", nil)
		o.wait(t)
		mu.Lock()
		defer mu.Unlock()
		if o.whole || len(failed) != 1 {
			t.Errorf("whole %t; Failed ran with %q", o.whole, failed)
		}
	})
}

// TestFailedNoAnswer is an app that is down: 502, Failed runs once with
// the error, and Observe's function gets the 502 and no body.
func TestFailedNoAnswer(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.NotFoundHandler())
	u, _ := url.Parse(srv.URL)
	srv.Close()
	o := newObserved()
	var failed []string
	var mu sync.Mutex
	h := New(&Config{Target: u, Hooks: observing(o, &failed, &mu)})
	w := do(h, untrustedPeer, http.MethodGet, "http://calibre.test/ajax/books", nil)
	o.wait(t)
	if w.Code != http.StatusBadGateway || o.status != http.StatusBadGateway || len(o.body) != 0 || o.whole {
		t.Errorf("client got %d; Observe's function got %d, %q, whole %t", w.Code, o.status, o.body, o.whole)
	}
	if len(failed) != 1 || !strings.HasPrefix(failed[0], "/ajax/books: ") {
		t.Errorf("Failed ran with %q, want once, for /ajax/books", failed)
	}
}

// TestFailedNotWhenClientLeft is a client that gives up before the app
// answers: Failed does not run.
func TestFailedNotWhenClientLeft(t *testing.T) {
	t.Parallel()
	arrived := make(chan struct{})
	u := target(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-r.Context().Done()
	}))
	over := make(chan struct{})
	var failed []error
	var mu sync.Mutex
	front := httptest.NewServer(New(&Config{Target: u, Hooks: Hooks{
		Started: func(*http.Request) func(int) { return func(int) { close(over) } },
		Failed: func(_ *http.Request, err error) {
			mu.Lock()
			defer mu.Unlock()
			failed = append(failed, err)
		},
	}}))
	t.Cleanup(front.Close)
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-arrived
		cancel()
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, front.URL+"/x", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := http.DefaultClient.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatal("the request was answered")
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do() = %v", err)
	}
	select {
	case <-over:
	case <-time.After(10 * time.Second):
		t.Fatal("the proxy did not finish the request")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(failed) != 0 {
		t.Errorf("Failed ran with %v, want not at all", failed)
	}
}
