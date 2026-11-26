// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

// Package probe is the image's health check, which the image's
// HEALTHCHECK runs as divbast-calibre health. It reads the state file
// that serve keeps of the two apps, asks each app that is up to answer on
// its loopback port, and checks the two public ports are listened on.
package probe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/divbast/calibre-oci-image/internal/supervise"
)

// The names of the two apps, as serve keys the state file with them and
// as health's lines begin with them.
const (
	serverName = "calibre-server"
	webName    = "calibre-web"
)

// libraryInfo is the Content Server's page that health asks for, as the
// internal user. Calibre's server sends its version in the Server header
// of each answer it gives to a user who is logged in.
const libraryInfo = "/ajax/library-info"

// webPage is the page health asks Calibre-Web for. Calibre-Web gives no
// version or commit in any page that needs no login, so only its status
// is read.
const webPage = "/login"

// Options say where the state file is, where the two apps are, and what
// each must answer.
type Options struct {
	// State is the state file serve writes.
	State string
	// ServerBase and WebBase are the apps' addresses on loopback, without
	// a trailing slash, and not through the image's public ports.
	ServerBase, WebBase string
	// PasswordFile holds the internal user's password.
	PasswordFile string
	// Version is the Calibre version the image is built from, which the
	// Content Server must name.
	Version string
	// Within bounds each app's check: the whole exchange with it.
	Within time.Duration
	// PlannedLimit is how long an app may be down in a planned restart
	// and still count as healthy: under it, and not at it.
	PlannedLimit time.Duration
	// Ports must each be listened on, on every address, as the network
	// namespace that Proc, /proc, describes.
	Ports []int
	Proc  string
	// Now is the time; nil is time.Now.
	Now func() time.Time
}

// Probe returns 0 and prints nothing when both apps are healthy and every
// port is listened on. Otherwise it prints one line to stderr, which is
// the only record of why, for each thing that failed, and returns 1. A
// line is "state: <reason>" for the state file, "<app>: <reason>" for an
// app, or "listen: <reason>" for a port.
//
// An app is healthy when the state file says it is up, and it answers: the
// Content Server a 200 for the internal user, with its Server header
// naming o.Version; Calibre-Web a 200. An app that is in a planned
// restart, and has been for less than o.PlannedLimit, is healthy, and is
// not asked.
func Probe(ctx context.Context, o *Options, stderr io.Writer) int {
	code := 0
	fail := func(prefix string, err error) {
		// A failed write to stderr leaves nowhere better to report it.
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", prefix, err)
		code = 1
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	states, err := supervise.ReadState(o.State)
	if err != nil {
		fail("state", fmt.Errorf("cannot read the state file: %w", err))
	} else {
		for _, app := range []struct {
			name  string
			check func(context.Context) error
		}{
			{serverName, func(ctx context.Context) error { return checkServer(ctx, o) }},
			{webName, func(ctx context.Context) error { return checkWeb(ctx, o) }},
		} {
			if err := checkApp(ctx, states[app.name], o.PlannedLimit, now(), app.check); err != nil {
				fail(app.name, err)
			}
		}
	}
	for _, port := range o.Ports {
		if err := listening(o.Proc, port); err != nil {
			fail("listen", err)
		}
	}
	return code
}

// checkApp judges one app from its state, and asks it with check when it
// is up and no restart is planned.
func checkApp(ctx context.Context, st supervise.State, limit time.Duration, now time.Time, check func(context.Context) error) error {
	switch {
	case st.Planned:
		if d := now.Sub(st.Since).Round(time.Second); d >= limit {
			return fmt.Errorf("restarting for %s, which is not under the %s allowed", d, limit)
		}
		return nil
	case !st.Up:
		return errors.New("not running")
	}
	return check(ctx)
}

// checkServer asks the Content Server for libraryInfo as the internal
// user, in o.Within, and returns why it is not healthy. The Server's
// login is Digest, so the first request is answered 401 with the
// challenge, and the second carries the answer.
func checkServer(ctx context.Context, o *Options) error {
	ctx, cancel := context.WithTimeout(ctx, o.Within)
	defer cancel()
	pw, err := os.ReadFile(o.PasswordFile)
	if err != nil {
		return fmt.Errorf("cannot read the internal user's password: %w", err)
	}
	password := strings.TrimRight(string(pw), "\r\n")
	if password == "" {
		return fmt.Errorf("%s holds no password", o.PasswordFile)
	}
	status, header, err := get(ctx, o.ServerBase+libraryInfo, "")
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized {
		c, cerr := parseChallenge(header.Get("WWW-Authenticate"))
		if cerr != nil {
			return fmt.Errorf("GET %s %w", libraryInfo, cerr)
		}
		req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, o.ServerBase+libraryInfo, http.NoBody)
		if rerr != nil {
			return rerr
		}
		auth, aerr := c.authorization(internalUser, password, req)
		if aerr != nil {
			return aerr
		}
		if status, header, err = get(ctx, o.ServerBase+libraryInfo, auth); err != nil {
			return err
		}
	}
	if status != http.StatusOK {
		return fmt.Errorf("GET %s answered %d, want 200", libraryInfo, status)
	}
	if want := "calibre " + o.Version; header.Get("Server") != want {
		return fmt.Errorf("its Server header is %q, want %q", header.Get("Server"), want)
	}
	return nil
}

// internalUser is the Content Server's read-only user, which Calibre-Web
// reads the library as.
const internalUser = "calibre-web"

// checkWeb asks Calibre-Web for webPage, in o.Within, and returns why it
// is not healthy.
func checkWeb(ctx context.Context, o *Options) error {
	ctx, cancel := context.WithTimeout(ctx, o.Within)
	defer cancel()
	status, _, err := get(ctx, o.WebBase+webPage, "")
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("GET %s answered %d, want 200", webPage, status)
	}
	return nil
}

// get sends GET url, with the Authorization header auth when it is not
// empty, and returns the answer's status and header, once its body is read
// and closed.
func get(ctx context.Context, url, auth string) (int, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return 0, nil, err
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := noRedirects.Do(req)
	if err != nil {
		return 0, nil, err
	}
	_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if cerr := resp.Body.Close(); err == nil {
		err = cerr
	}
	return resp.StatusCode, resp.Header, err
}

// noRedirects reports a redirect as its status instead of following it,
// so each check judges the answer of the URL it was given.
var noRedirects = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}
