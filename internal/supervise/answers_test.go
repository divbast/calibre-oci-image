// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package supervise

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestAnswers: Answers' Serves makes its first try one every after it is
// called, and returns once the server sends any answer, a 401 too; a port
// that takes the connection and sends nothing is no answer, nor is a
// refused one, and it keeps trying until ctx ends.
func TestAnswers(t *testing.T) {
	t.Parallel()
	const every = 20 * time.Millisecond
	t.Run("an answer", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		t.Cleanup(srv.Close)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if err := Answers(testClient(), srv.URL+"/", every)(ctx); err != nil {
			t.Errorf("Serves = %v, want nil: a 401 is an answer", err)
		}
	})
	t.Run("the first try comes one every after the call", func(t *testing.T) {
		t.Parallel()
		first := make(chan time.Time, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			select {
			case first <- time.Now():
			default:
			}
			w.WriteHeader(http.StatusUnauthorized)
		}))
		t.Cleanup(srv.Close)
		called := time.Now()
		if err := Answers(testClient(), srv.URL+"/", every)(t.Context()); err != nil {
			t.Fatalf("Serves = %v, want nil", err)
		}
		// A server that acted on a SIGTERM may still answer for a moment
		// after it, and must not be taken for one that lost it
		if after := (<-first).Sub(called); after < every {
			t.Errorf("the first try came %s after the call, want at least %s", after, every)
		}
	})
	t.Run("silent, then refused, then an answer", func(t *testing.T) {
		t.Parallel()
		ln, err := listen(t, "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		got := make(chan error, 1)
		go func() { got <- Answers(testClient(), "http://"+addr+"/", every)(t.Context()) }()
		// The port takes each connection and sends nothing, as
		// calibre-server's does before its serving loop runs, then refuses
		// them
		var taken atomic.Int32
		var mu sync.Mutex
		var held []net.Conn
		go func() {
			for {
				conn, aerr := ln.Accept()
				if aerr != nil {
					return
				}
				taken.Add(1)
				mu.Lock()
				held = append(held, conn)
				mu.Unlock()
			}
		}()
		t.Cleanup(func() {
			mu.Lock()
			defer mu.Unlock()
			for _, c := range held {
				_ = c.Close()
			}
		})
		select {
		case gerr := <-got:
			t.Fatalf("Serves = %v while the port took connections and sent nothing", gerr)
		case <-time.After(5 * every):
		}
		if taken.Load() == 0 {
			t.Fatal("Serves made no connection while the port took them")
		}
		_ = ln.Close()
		select {
		case gerr := <-got:
			t.Fatalf("Serves = %v while the port refused connections", gerr)
		case <-time.After(5 * every):
		}
		ln, err = listen(t, addr)
		if err != nil {
			t.Skipf("the port went to another program meanwhile: %v", err)
		}
		srv := &http.Server{Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
		go func() { _ = srv.Serve(ln) }()
		t.Cleanup(func() { _ = srv.Close() })
		select {
		case gerr := <-got:
			if gerr != nil {
				t.Errorf("Serves = %v, want nil: a 404 is an answer", gerr)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Serves did not return once the server answered")
		}
	})
	t.Run("ctx ends", func(t *testing.T) {
		t.Parallel()
		ln, err := listen(t, "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 5*every)
		defer cancel()
		if err := Answers(testClient(), "http://"+addr+"/", every)(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Serves = %v, want ctx's error", err)
		}
	})
}

// listen listens on addr, on TCP, until the test ends.
func listen(t *testing.T, addr string) (net.Listener, error) {
	t.Helper()
	return (&net.ListenConfig{}).Listen(t.Context(), "tcp", addr)
}

// testClient is a client as calibre-server's Serves has: no proxy, no
// redirect followed, a new connection each time.
func testClient() *http.Client {
	return &http.Client{
		Transport:     &http.Transport{DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
