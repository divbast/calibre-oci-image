// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package proxy

import (
	"math"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// loginRefusal is what an unlisted source reads while Calibre-Web's login
// header name is unknown.
const loginRefusal = "Calibre-Web's login settings could not be read; try again in 10 seconds."

// loginRetryAfter is how many seconds such a source is told to wait.
const loginRetryAfter = 10 * time.Second

// Hooks lets the caller take part in each request.
type Hooks struct {
	// Both hooks get the request with its path as the app will route it:
	// when the request carries an X-Script-Name that the proxy keeps (it
	// came from a listed source) and the path starts with it, the hooks see
	// the path without that prefix. The request the app receives is not
	// changed. The hooks must not rely on r.URL.RawPath.
	//
	// Gate runs before a request goes on. When it denies, the client gets
	// a 503 with a Retry-After of retryAfter, in whole seconds, and reason
	// as its one line of text. A denied request does not call Started.
	Gate func(*http.Request) (deny bool, retryAfter time.Duration, reason string)

	// Started runs when a request goes on. The function it returns is
	// called once, when the response has been fully copied to the client or
	// the request failed, with the status the app gave, or 502 when it
	// gave none.
	Started func(*http.Request) (done func(status int))
}

// Config says how one public port proxies to its app.
type Config struct {
	// Target is the app's address.
	Target *url.URL
	// Trusted lists the sources whose X-Forwarded-* headers and login
	// header are kept.
	Trusted []netip.Prefix
	// Login is the login header's name, for Calibre-Web. Nil when there is
	// no login header to manage.
	Login *LoginHeader
	// StripLoginAlways removes the login header from every source, listed
	// or not. It is for Calibre's Content Server, which has no use for it.
	StripLoginAlways bool
	// BlockReconnect answers 404 for /reconnect.
	BlockReconnect bool
	Hooks          Hooks
}

type handler struct {
	c  Config
	rp *httputil.ReverseProxy
}

// New returns the handler of one public port.
func New(c Config) http.Handler {
	h := &handler{c: c}
	h.rp = &httputil.ReverseProxy{
		Rewrite:       h.rewrite,
		FlushInterval: -1, // bodies stream through, in both directions
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	return h
}

// managed reports whether this port waits for the login header's name: it
// has one, and keeps it from unlisted sources instead of from everyone.
func (h *handler) managed() bool {
	return h.c.Login != nil && !h.c.StripLoginAlways
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	trusted := isTrusted(h.c.Trusted, r.RemoteAddr)
	routed := routedPath(r, trusted)
	seen := r // what the hooks see
	if routed != r.URL.Path {
		seen = r.Clone(r.Context())
		seen.URL.Path, seen.URL.RawPath = routed, ""
	}
	if h.c.BlockReconnect && routed == "/reconnect" {
		http.NotFound(w, r)
		return
	}
	if h.managed() && !trusted && !h.c.Login.admit(r.Context()) {
		refuse(w, loginRetryAfter, loginRefusal)
		return
	}
	if gate := h.c.Hooks.Gate; gate != nil {
		if deny, after, reason := gate(seen); deny {
			refuse(w, after, reason)
			return
		}
	}
	if h.managed() && changesSettings(r.Method, routed) {
		h.c.Login.begin()
		defer h.c.Login.end()
	}
	sw := &statusWriter{ResponseWriter: w}
	if started := h.c.Hooks.Started; started != nil {
		if done := started(seen); done != nil {
			defer func() {
				status := sw.status
				if status == 0 {
					status = http.StatusBadGateway
				}
				done(status)
			}()
		}
	}
	h.rp.ServeHTTP(sw, r)
}

// routedPath returns the path Calibre-Web routes. It starts from the decoded
// path, removes the X-Script-Name prefix when the path starts with it, as
// Calibre-Web does, and then replaces any leading slashes by one, as the
// web framework does before it matches a route. A source that is not listed
// has X-Script-Name removed, so its path keeps its prefix.
func routedPath(r *http.Request, trusted bool) string {
	path := r.URL.Path
	if trusted {
		if script := strings.Join(r.Header.Values("X-Script-Name"), ", "); script != "" {
			path = strings.TrimPrefix(path, script)
		}
	}
	return "/" + strings.TrimLeft(path, "/")
}

// changesSettings reports whether a request can change Calibre-Web's
// settings: any method but GET and HEAD on a path under /admin/.
func changesSettings(method, routed string) bool {
	return method != http.MethodGet && method != http.MethodHead && strings.HasPrefix(routed, "/admin/")
}

// refuse answers 503 with the reason as one line of text.
func refuse(w http.ResponseWriter, retryAfter time.Duration, reason string) {
	secs := int(math.Ceil(retryAfter.Seconds()))
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(strings.Join(strings.Fields(reason), " ") + "\n"))
}

// rewrite builds the request to the app. The proxy removes the forwarding
// headers from it before this runs, and rewrite puts them back, or not.
func (h *handler) rewrite(pr *httputil.ProxyRequest) {
	pr.SetURL(h.c.Target)
	pr.Out.Host = pr.In.Host // the app builds its links from the host the client used

	out := pr.Out.Header
	for k := range out {
		if isProxyHeader(k) {
			delete(out, k)
		}
	}
	peer := pr.In.RemoteAddr
	if ip, ok := peerIP(peer); ok {
		peer = ip.String()
	}
	trusted := isTrusted(h.c.Trusted, pr.In.RemoteAddr)
	if trusted {
		for k, v := range pr.In.Header {
			// An underscore spelling would reach the app as the same header
			if isProxyHeader(k) && !strings.Contains(k, "_") && !strings.EqualFold(k, "X-Forwarded-For") {
				out[k] = slices.Clone(v)
			}
		}
		out["X-Forwarded-For"] = []string{strings.Join(slices.Concat(pr.In.Header.Values("X-Forwarded-For"), []string{peer}), ", ")}
	} else {
		out["X-Forwarded-For"] = []string{peer}
	}

	if h.c.Login != nil && (h.c.StripLoginAlways || !trusted) {
		if name := h.c.Login.Name(); name != "" {
			for k := range out {
				if sameHeader(k, name) {
					delete(out, k)
				}
			}
		}
	}
}

// sameHeader reports whether two header names are the same to an app behind
// WSGI, which writes both the dash and the underscore as an underscore.
func sameHeader(a, b string) bool {
	return strings.EqualFold(strings.ReplaceAll(a, "-", "_"), strings.ReplaceAll(b, "-", "_"))
}

// isProxyHeader reports whether name is one of the headers that say how a
// request came through a proxy, in any spelling of the dashes: Forwarded,
// X-Forwarded-*, X-Scheme, X-Script-Name and X-Real-IP.
func isProxyHeader(name string) bool {
	n := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	return n == "FORWARDED" || strings.HasPrefix(n, "X_FORWARDED_") ||
		n == "X_SCHEME" || n == "X_SCRIPT_NAME" || n == "X_REAL_IP"
}

// statusWriter keeps the status the app gave.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 && (code >= 200 || code == http.StatusSwitchingProtocols) {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the real writer to flush it.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }
