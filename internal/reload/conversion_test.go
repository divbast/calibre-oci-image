// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package reload

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestWatched is which answers the Coordinator reads: calibre-server's
// to a conversion's start and to its status, both POSTs.
func TestWatched(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		method, path string
		want         bool
	}{
		{"POST", "/conversion/start/12", true},
		{"POST", "/conversion/status/5", true},
		{"POST", "//conversion/status/5/", true},
		{"GET", "/conversion/status/5", false},
		{"GET", "/conversion/book-data/12", false},
		{"POST", "/conversion/status", false},
		{"POST", "/conversion/status/5/6", false},
		{"POST", "/cdb/set-fields/12", false},
	} {
		if got := Watched(tt.method, tt.path); got != tt.want {
			t.Errorf("Watched(%s, %q) = %t, want %t", tt.method, tt.path, got, tt.want)
		}
	}
}

func gzipped(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var gzipHeader = http.Header{"Content-Encoding": {"gzip"}}

// TestAnsweredStart is each answer to a conversion's start: a job id
// tracks that job until its status says it ended; an answer that cannot be
// read counts as a conversion running until Cap; a refusal starts none.
func TestAnsweredStart(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		status int
		header http.Header
		body   func(t *testing.T) []byte
		whole  bool
		// runs is how long the restart waits for the conversion, or 0 when
		// it does not wait
		runs time.Duration
		// endsWith5 is whether job 5's end ends the wait
		endsWith5 bool
	}{
		{"a job id", 200, nil, func(*testing.T) []byte { return []byte("5") }, true, 10 * time.Minute, true},
		{"a gzipped job id", 200, gzipHeader, func(t *testing.T) []byte { t.Helper(); return gzipped(t, []byte("5\n")) }, true, 10 * time.Minute, true},
		{"null", 200, nil, func(*testing.T) []byte { return []byte("null") }, true, 10 * time.Minute, false},
		{"not JSON", 200, nil, func(*testing.T) []byte { return []byte("<html>") }, true, 10 * time.Minute, false},
		{"cut short", 200, nil, func(*testing.T) []byte { return []byte("5") }, false, 10 * time.Minute, false},
		{"an unknown encoding", 200, http.Header{"Content-Encoding": {"br"}}, func(*testing.T) []byte { return []byte("5") }, true, 10 * time.Minute, false},
		{"refused", 400, nil, func(*testing.T) []byte { return []byte("Unsupported input format") }, true, 0, false},
		{"not found", 404, nil, func(*testing.T) []byte { return []byte("5") }, true, 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.running()
			start := h.clk.Now()
			h.c.Answered(http.MethodPost, "/conversion/start/12", tt.status, tt.header, tt.body(t), tt.whole)
			h.write(CalibreWeb)
			if tt.runs == 0 {
				h.clk.Advance(6 * time.Second)
				h.expect(0, 1)
				return
			}
			h.clk.Advance(time.Minute)
			h.expect(1, 0)
			h.c.ConversionDone(5)
			if tt.endsWith5 {
				h.expect(1, 1)
				return
			}
			h.expect(1, 0)
			h.clk.Advance(time.Hour)
			h.expect(1, 1)
			if _, restarts := h.r.times(); len(restarts) > 0 && !restarts[0].Equal(start.Add(tt.runs)) {
				t.Errorf("restarted %s after the start, want %s", restarts[0].Sub(start), tt.runs)
			}
		})
	}
}

// TestAnsweredStatus is each answer to a status poll: only "running":
// false ends the conversion, gzipped or not, and when the proxy kept only
// the start of a long answer.
func TestAnsweredStatus(t *testing.T) {
	t.Parallel()
	done := []byte(`{"running": false, "ok": true, "was_aborted": false, "traceback": "", "log": "` +
		strings.Repeat("a conversion log line\\n", 5000) + `", "size": 100, "fmt": "mobi"}`)
	kept := func(b []byte) []byte { return b[:min(len(b), 64<<10)] }
	for _, tt := range []struct {
		name   string
		path   string
		status int
		header http.Header
		body   func(t *testing.T) []byte
		whole  bool
		ends   bool
	}{
		{"running: false", "/conversion/status/5", 200, nil, func(*testing.T) []byte { return done }, true, true},
		{"the start of a long answer", "/conversion/status/5", 200, nil, func(*testing.T) []byte { return kept(done) }, false, true},
		{
			"the gzipped start of a long answer", "/conversion/status/5", 200, gzipHeader,
			func(t *testing.T) []byte { t.Helper(); gz := gzipped(t, done); return gz[:len(gz)/2] }, false, true,
		},
		{"running: true", "/conversion/status/5", 200, nil, func(*testing.T) []byte {
			return []byte(`{"running": true, "percent": 0.5, "msg": "Converting"}`)
		}, true, false},
		{"another job", "/conversion/status/6", 200, nil, func(*testing.T) []byte { return done }, true, false},
		{"not found", "/conversion/status/5", 404, nil, func(*testing.T) []byte { return []byte("No job with id: 5") }, true, false},
		{"not JSON", "/conversion/status/5", 200, nil, func(*testing.T) []byte { return []byte("running: false") }, true, false},
		{"running is not a boolean", "/conversion/status/5", 200, nil, func(*testing.T) []byte {
			return []byte(`{"running": "false"}`)
		}, true, false},
		{"not an object", "/conversion/status/5", 200, nil, func(*testing.T) []byte { return []byte(`[false]`) }, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.running()
			h.c.ConversionStarted(5)
			h.clk.Advance(time.Minute)
			h.write(CalibreWeb)
			h.clk.Advance(time.Minute)
			h.expect(1, 0)
			h.c.Answered(http.MethodPost, tt.path, tt.status, tt.header, tt.body(t), tt.whole)
			if tt.ends {
				h.expect(1, 1)
			} else {
				h.expect(1, 0)
			}
		})
	}
}

// TestUnreadStartEndsAtCap is a start whose answer could not be read: it
// counts as a conversion for 10 minutes from its start, not from the
// change that called for the restart.
func TestUnreadStartEndsAtCap(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.running()
	start := h.clk.Now()
	h.c.Answered(http.MethodPost, "/conversion/start/12", http.StatusOK, nil, []byte("<html>"), true)
	h.clk.Advance(5 * time.Minute)
	h.write(CalibreWeb)
	h.clk.Advance(5*time.Minute - time.Millisecond)
	h.expect(1, 0)
	h.clk.Advance(time.Hour)
	h.expect(1, 1)
	if _, restarts := h.r.times(); len(restarts) > 0 && !restarts[0].Equal(start.Add(10*time.Minute)) {
		t.Errorf("restarted %s after the start, want 10m0s", restarts[0].Sub(start))
	}
}
