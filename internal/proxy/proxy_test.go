// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package proxy

import (
	"context"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var trustedNet = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}

const (
	untrustedPeer = "192.0.2.7:5000"
	trustedPeer   = "10.1.2.3:5000"
)

// backend is an upstream that keeps what it received.
type backend struct {
	srv  *httptest.Server
	mu   sync.Mutex
	last http.Header
	hits atomic.Int32
}

func newBackend(t *testing.T) *backend {
	t.Helper()
	b := &backend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.hits.Add(1)
		b.mu.Lock()
		b.last = r.Header.Clone()
		b.mu.Unlock()
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, "upstream")
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *backend) target() *url.URL {
	u, err := url.Parse(b.srv.URL)
	if err != nil {
		panic(err)
	}
	return u
}

// got returns the headers of the last request upstream saw.
func (b *backend) got() http.Header {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.last
}

// do sends one request straight to the handler, as if from peer.
func do(h http.Handler, peer, method, target string, hdr http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), method, target, http.NoBody)
	r.RemoteAddr = peer
	maps.Copy(r.Header, hdr)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHeadersUntrusted(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	h := New(&Config{Target: b.target(), Trusted: trustedNet})
	forged := http.Header{
		"X-Forwarded-For":   {"203.0.113.9"},
		"X-Forwarded-Proto": {"https"},
		"X-Forwarded-Host":  {"evil.example"},
		"X-Forwarded-Port":  {"443"},
		"X_Forwarded_For":   {"203.0.113.9"},
		"X-Scheme":          {"https"},
		"X-Script-Name":     {"/evil"},
		"X_Script_Name":     {"/evil"},
		"X-Real-IP":         {"203.0.113.9"},
		"Forwarded":         {"for=203.0.113.9"},
		"X-Keep":            {"yes"},
	}
	if w := do(h, untrustedPeer, "GET", "http://calibre.test/x", forged); w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	got := b.got()
	if v := got.Values("X-Forwarded-For"); len(v) != 1 || v[0] != "192.0.2.7" {
		t.Errorf("X-Forwarded-For = %q, want only the peer", v)
	}
	for k := range got {
		n := strings.ToUpper(strings.ReplaceAll(k, "-", "_"))
		if n != "X_FORWARDED_FOR" && isProxyHeader(k) {
			t.Errorf("forged header %s reached upstream: %q", k, got[k])
		}
		if k == "X_Forwarded_For" {
			t.Errorf("underscore spelling of X-Forwarded-For reached upstream")
		}
	}
	if got.Get("X-Keep") != "yes" {
		t.Error("an ordinary header was dropped")
	}
}

func TestLoginHeaderUntrusted(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	lh, _ := loaded(t, "X-User")
	h := New(&Config{Target: b.target(), Trusted: trustedNet, Login: lh})
	hdr := http.Header{"X-User": {"admin"}, "X_User": {"admin"}, "X-Other": {"1"}}
	do(h, untrustedPeer, "GET", "http://calibre.test/", hdr)
	got := b.got()
	for k := range got {
		if strings.EqualFold(strings.ReplaceAll(k, "_", "-"), "X-User") {
			t.Errorf("login header %s reached upstream", k)
		}
	}
	if got.Get("X-Other") != "1" {
		t.Error("an ordinary header was dropped")
	}
}

func TestHeadersTrusted(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	lh, _ := loaded(t, "X-User")
	h := New(&Config{Target: b.target(), Trusted: trustedNet, Login: lh})
	hdr := http.Header{
		"X-Forwarded-For":   {"203.0.113.5"},
		"X-Forwarded-Proto": {"https"},
		"X-Forwarded-Host":  {"books.example"},
		"Forwarded":         {"for=203.0.113.5"},
		"X-Scheme":          {"https"},
		"X-Script-Name":     {"/x"},
		"X-Real-IP":         {"203.0.113.5"},
		"X-User":            {"admin"},
	}
	for _, peer := range []string{trustedPeer, "[::ffff:10.1.2.3]:5000"} {
		do(h, peer, "GET", "http://calibre.test/", hdr)
		got := b.got()
		if got.Get("X-Forwarded-For") != "203.0.113.5, 10.1.2.3" {
			t.Errorf("%s: X-Forwarded-For = %q", peer, got.Get("X-Forwarded-For"))
		}
		if got.Get("X-Forwarded-Proto") != "https" || got.Get("X-Forwarded-Host") != "books.example" ||
			got.Get("Forwarded") != "for=203.0.113.5" {
			t.Errorf("%s: forwarded headers changed: %v", peer, got)
		}
		if got.Get("X-Scheme") != "https" || got.Get("X-Script-Name") != "/x" || got.Get("X-Real-IP") != "203.0.113.5" {
			t.Errorf("%s: X-Scheme, X-Script-Name or X-Real-IP changed: %v", peer, got)
		}
		if got.Get("X-User") != "admin" {
			t.Errorf("%s: the login header was removed", peer)
		}
	}
	do(h, trustedPeer, "GET", "http://calibre.test/", nil)
	if got := b.got().Get("X-Forwarded-For"); got != "10.1.2.3" {
		t.Errorf("without a header, X-Forwarded-For = %q", got)
	}
}

func TestLoginHeaderAlwaysStripped(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	lh, _ := loaded(t, "X-User")
	h := New(&Config{Target: b.target(), Trusted: trustedNet, Login: lh, StripLoginAlways: true})
	hdr := http.Header{"X-User": {"admin"}, "X-Forwarded-Proto": {"https"}, "X-Forwarded-For": {"203.0.113.5"}}
	do(h, trustedPeer, "GET", "http://calibre.test/", hdr)
	got := b.got()
	if v := got.Get("X-User"); v != "" {
		t.Errorf("a trusted peer's login header reached upstream: %q", v)
	}
	if got.Get("X-Forwarded-Proto") != "https" || got.Get("X-Forwarded-For") != "203.0.113.5, 10.1.2.3" {
		t.Errorf("a trusted peer's X-Forwarded headers were not kept: %v", got)
	}
	do(h, untrustedPeer, "GET", "http://calibre.test/", hdr)
	if v := b.got().Get("X-User"); v != "" {
		t.Errorf("an unlisted peer's login header reached upstream: %q", v)
	}
}

func TestHostIsKept(t *testing.T) {
	t.Parallel()
	var host string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { host = r.Host }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	do(New(&Config{Target: u}), untrustedPeer, "GET", "http://books.example/x", nil)
	if host != "books.example" {
		t.Fatalf("upstream saw Host %q", host)
	}
}

func TestReconnectBlocked(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	blocked := New(&Config{Target: b.target(), BlockReconnect: true})
	for _, target := range []string{"/reconnect", "/reconnect?x=1", "/reconnect?"} {
		for _, m := range []string{"GET", "POST"} {
			if w := do(blocked, untrustedPeer, m, "http://calibre.test"+target, nil); w.Code != 404 {
				t.Errorf("%s %s = %d, want 404", m, target, w.Code)
			}
		}
	}
	if n := b.hits.Load(); n != 0 {
		t.Fatalf("%d blocked requests reached upstream", n)
	}
	for _, target := range []string{"/reconnect/x", "/reconnected", "/a/reconnect"} {
		if w := do(blocked, untrustedPeer, "GET", "http://calibre.test"+target, nil); w.Code != 200 {
			t.Errorf("GET %s = %d, want 200", target, w.Code)
		}
	}
	open := New(&Config{Target: b.target()})
	if w := do(open, untrustedPeer, "GET", "http://calibre.test/reconnect", nil); w.Code != 200 {
		t.Errorf("without BlockReconnect, /reconnect = %d", w.Code)
	}
}

func TestGate(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	var started atomic.Int32
	var deny atomic.Bool
	deny.Store(true)
	h := New(&Config{Target: b.target(), Hooks: Hooks{
		Gate: func(*http.Request) (bool, time.Duration, string) {
			return deny.Load(), 10 * time.Second, "a reload is pending"
		},
		Started: func(*http.Request) func(int) { started.Add(1); return func(int) {} },
	}})
	w := do(h, untrustedPeer, "POST", "http://calibre.test/x", nil)
	if w.Code != 503 || w.Header().Get("Retry-After") != "10" ||
		!strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") ||
		w.Body.String() != "a reload is pending\n" {
		t.Fatalf("denied: %d %v %q", w.Code, w.Header(), w.Body.String())
	}
	if b.hits.Load() != 0 || started.Load() != 0 {
		t.Fatalf("a denied request reached upstream (%d) or called Started (%d)", b.hits.Load(), started.Load())
	}
	deny.Store(false)
	if w := do(h, untrustedPeer, "POST", "http://calibre.test/x", nil); w.Code != 200 || started.Load() != 1 {
		t.Fatalf("allowed: %d, started %d", w.Code, started.Load())
	}
}

func TestGateRoundsRetryAfterUp(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	h := New(&Config{Target: b.target(), Hooks: Hooks{
		Gate: func(*http.Request) (bool, time.Duration, string) { return true, 1500 * time.Millisecond, "wait" },
	}})
	if got := do(h, untrustedPeer, "GET", "http://calibre.test/", nil).Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q, want 2", got)
	}
}

func TestStartedDone(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, "short and stout")
	}))
	u, _ := url.Parse(srv.URL)
	var statuses []int
	var calls int
	h := New(&Config{Target: u, Hooks: Hooks{Started: func(*http.Request) func(int) {
		calls++
		return func(s int) { statuses = append(statuses, s) }
	}}})
	w := do(h, untrustedPeer, "GET", "http://calibre.test/", nil)
	if w.Body.String() != "short and stout" || len(statuses) != 1 || statuses[0] != http.StatusTeapot {
		t.Fatalf("body %q, done calls %v", w.Body.String(), statuses)
	}
	srv.Close() // now upstream refuses connections
	w = do(h, untrustedPeer, "GET", "http://calibre.test/", nil)
	if w.Code != 502 || len(statuses) != 2 || statuses[1] != 502 || calls != 2 {
		t.Fatalf("down upstream: %d, done calls %v", w.Code, statuses)
	}
}

// heapWatch records the highest heap in use while it runs.
func heapWatch(stop <-chan struct{}, peak *uint64) {
	var m runtime.MemStats
	for {
		runtime.ReadMemStats(&m)
		if m.HeapAlloc > *peak {
			*peak = m.HeapAlloc
		}
		select {
		case <-stop:
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// zeros yields n zero bytes without holding them.
type zeros struct{ n int64 }

func (z *zeros) Read(p []byte) (int, error) {
	if z.n <= 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), z.n)
	clear(p[:n])
	z.n -= n
	return int(n), nil
}

//nolint:paralleltest // it measures the heap of the whole process, so nothing else may run
func TestStreams(t *testing.T) {
	const size = 300 << 20
	const limit = 64 << 20
	var received atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			n, _ := io.Copy(io.Discard, r.Body)
			received.Store(n)
			return
		}
		_, _ = io.Copy(w, &zeros{size})
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	front := httptest.NewServer(New(&Config{Target: u}))
	defer front.Close()

	measure := func(t *testing.T, run func() int64) {
		t.Helper()
		runtime.GC()
		var base runtime.MemStats
		runtime.ReadMemStats(&base)
		peak := base.HeapAlloc
		stop := make(chan struct{})
		done := make(chan struct{})
		go func() { heapWatch(stop, &peak); close(done) }()
		n := run()
		close(stop)
		<-done
		if n != size {
			t.Fatalf("%d bytes passed, want %d", n, size)
		}
		var growth uint64
		if peak > base.HeapAlloc {
			growth = peak - base.HeapAlloc
		}
		t.Logf("heap grew by %d MiB while %d MiB passed", growth>>20, n>>20)
		if growth > limit {
			t.Fatalf("heap grew by %d MiB, over %d MiB", growth>>20, uint64(limit)>>20)
		}
	}
	t.Run("upload", func(t *testing.T) {
		measure(t, func() int64 {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, front.URL, &zeros{size})
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			return received.Load()
		})
	})
	t.Run("download", func(t *testing.T) {
		measure(t, func() int64 {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, front.URL, http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			n, _ := io.Copy(io.Discard, resp.Body)
			return n
		})
	})
}

func TestRoutedPath(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	var seenGate, seenStarted []string
	h := New(&Config{Target: b.target(), Trusted: trustedNet, BlockReconnect: true, Hooks: Hooks{
		Gate: func(r *http.Request) (bool, time.Duration, string) {
			seenGate = append(seenGate, r.URL.Path)
			return false, 0, ""
		},
		Started: func(r *http.Request) func(int) {
			seenStarted = append(seenStarted, r.URL.Path)
			return nil
		},
	}})
	prefix := http.Header{"X-Script-Name": {"/x"}}
	if w := do(h, trustedPeer, "GET", "http://calibre.test/x/reconnect", prefix); w.Code != 404 {
		t.Errorf("/x/reconnect behind X-Script-Name /x = %d, want 404", w.Code)
	}
	if w := do(h, trustedPeer, "GET", "http://calibre.test/reconnect", prefix); w.Code != 404 {
		t.Errorf("/reconnect behind X-Script-Name /x = %d, want 404: the prefix is removed only when present", w.Code)
	}
	// An unlisted source's X-Script-Name is removed, so its path is not shortened.
	if w := do(h, untrustedPeer, "GET", "http://calibre.test/x/reconnect", prefix); w.Code != 200 {
		t.Errorf("unlisted /x/reconnect = %d, want 200", w.Code)
	}
	seenGate, seenStarted = nil, nil
	do(h, trustedPeer, "GET", "http://calibre.test/x/books?a=1", prefix)
	if len(seenGate) != 1 || seenGate[0] != "/books" || len(seenStarted) != 1 || seenStarted[0] != "/books" {
		t.Errorf("hooks saw %v and %v, want /books", seenGate, seenStarted)
	}
	if b.got().Get("X-Script-Name") != "/x" {
		t.Error("the app did not get X-Script-Name")
	}
}

func TestRoutedPathLikeCalibreWeb(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	h := New(&Config{Target: b.target(), Trusted: trustedNet, BlockReconnect: true})
	tests := []struct {
		name    string
		peer    string
		script  string
		target  string
		blocked bool
	}{
		{"double slash", untrustedPeer, "", "//reconnect", true},
		{"triple slash", untrustedPeer, "", "///reconnect", true},
		{"encoded slash", untrustedPeer, "", "/%2Freconnect", true},
		{"prefix glued on", trustedPeer, "/x", "/xreconnect", true},
		{"prefix with slash", trustedPeer, "/x", "/x//reconnect", true},
		{"root prefix", trustedPeer, "/", "/reconnect", true},
		{"dot segment", untrustedPeer, "", "/./reconnect", false},
		{"parent segment", untrustedPeer, "", "/a/../reconnect", false},
		{"trailing slash", untrustedPeer, "", "/reconnect/", false},
		{"semicolon", untrustedPeer, "", "/reconnect;x", false},
		{"capital", untrustedPeer, "", "/Reconnect", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hdr := http.Header{}
			if tt.script != "" {
				hdr["X-Script-Name"] = []string{tt.script}
			}
			got := do(h, tt.peer, "GET", "http://calibre.test"+tt.target, hdr).Code == 404
			if got != tt.blocked {
				t.Fatalf("%s blocked = %v, want %v", tt.target, got, tt.blocked)
			}
		})
	}
}
