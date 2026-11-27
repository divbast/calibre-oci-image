// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"encoding/json"
	"html"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/divbast/ci/harness"
)

// loginHeader is the header the test has Calibre-Web log a user in by, as
// a proxy in front of it that logs users in itself would send it.
const loginHeader = "X-Divbast-User"

// The addresses a client claims, in X-Forwarded-For, to forward for: from
// the range kept for documentation, so that no other source logs one.
const (
	forgedUnlisted = "203.0.113.7"
	forgedListed   = "203.0.113.9"
)

// The network TestForwardedHeaders runs its two containers on, and the
// address each runs at. On rootless Podman a request from the host comes
// from the container's own address, which a container started again would
// otherwise draw anew; on Docker it comes from the network's gateway.
// Either way, with the address fixed, the client's address is the same
// in both containers.
const (
	headersSubnet = "10.231.12.0/24"
	headersIP     = "10.231.12.10"
)

// failedLogin matches Calibre-Web's log line for a failed login by user,
// and keeps the address it logs as the client's.
func failedLogin(user string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)Login failed for user "` + regexp.QuoteMeta(user) + `" IP-address: (.+?)\s*$`)
}

// failWith logs in to s's Calibre-Web as a user that does not exist, with
// X-Forwarded-For forged, and returns the address Calibre-Web then logs
// as the client's.
func failWith(tb testing.TB, s *harness.Serving, forged string) string {
	tb.Helper()
	ctx := tb.Context()
	user := "nobody-" + random(tb, 8, lower)
	resp, err := newWebSession(tb, s).login(ctx, user, "wrong", http.Header{"X-Forwarded-For": {forged}})
	if err != nil || resp.Status != http.StatusUnauthorized {
		tb.Fatalf("a login as %s: %v, status %d, want 401", user, err, resp.Status)
	}
	m, err := waitLog(ctx, s.C, failedLogin(user), startWithin)
	if err != nil {
		tb.Fatal(err)
	}
	return m[1]
}

// addUser creates the user name with pw through Calibre-Web's admin page,
// as an admin does: the new user form as the page gives it, with a name,
// an e-mail address and the password. Calibre-Web answers with the same
// page, and a message that says whether it made the user.
func addUser(tb testing.TB, w *webSession, name, pw string) {
	tb.Helper()
	ctx := tb.Context()
	resp, err := w.get(ctx, "/admin/user/new", nil)
	if err != nil || resp.Status != http.StatusOK {
		tb.Fatalf("Calibre-Web's new user page: %v, status %d", err, resp.Status)
	}
	form, err := formValues(resp.Body, "email")
	if err != nil {
		tb.Fatal(err)
	}
	form.Set("name", name)
	form.Set("email", name+"@example.invalid")
	form.Set("password", pw)
	// The page answers 200 either way, and says which
	resp, err = w.post(ctx, "/admin/user/new", form, nil)
	want := "success: User '" + name + "' created"
	if err != nil || resp.Status != http.StatusOK || !slices.Contains(flashed(resp.Body), want) {
		tb.Fatalf("creating the user %s in Calibre-Web: %v, status %d, want 200 and %q: the page says %s", name, err, resp.Status, want, none(flashed(resp.Body)))
	}
}

// headerLogin turns Calibre-Web's login by a proxy's header on, for the
// header name, through its basic configuration page, as an admin does: the
// whole form as the page gives it, with that setting checked and the
// header named. Calibre-Web's own list of the proxies it trusts keeps its
// default, 127.0.0.1 and ::1, which is where PID 1's requests come from.
func headerLogin(tb testing.TB, w *webSession, name string) {
	tb.Helper()
	ctx := tb.Context()
	resp, err := w.get(ctx, "/admin/config", nil)
	if err != nil || resp.Status != http.StatusOK {
		tb.Fatalf("Calibre-Web's configuration page: %v, status %d", err, resp.Status)
	}
	form, err := formValues(resp.Body, "config_port")
	if err != nil {
		tb.Fatal(err)
	}
	form.Set("config_allow_reverse_proxy_header_login", "on")
	form.Set("config_reverse_proxy_login_header_name", name)
	resp, err = w.post(ctx, "/admin/ajaxconfig", form, nil)
	if err != nil || resp.Status != http.StatusOK {
		tb.Fatalf("saving Calibre-Web's configuration: %v, status %d", err, resp.Status)
	}
	var saved struct {
		Result []struct {
			Type string `json:"type"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp.Body, &saved); err != nil || len(saved.Result) != 1 || saved.Result[0].Type != "success" {
		tb.Fatalf("Calibre-Web did not save its configuration: %v: %s", err, harness.Clip(string(resp.Body)))
	}
}

// flash is a message Calibre-Web shows at the top of a page, and its kind.
var flash = regexp.MustCompile(`(?s)<div id="flash_(\w+)" class="alert alert-\w+">(.*?)</div>`)

// flashed returns the messages page shows, each after its kind, such as
// "success: " or "danger: ".
func flashed(page []byte) []string {
	found := flash.FindAllSubmatch(page, -1)
	msgs := make([]string, 0, len(found))
	for _, m := range found {
		msgs = append(msgs, string(m[1])+": "+html.UnescapeString(strings.TrimSpace(string(m[2]))))
	}
	return msgs
}

// notLoggedIn checks that Calibre-Web answered a request to a page that
// needs a login by sending the client to its login page.
func notLoggedIn(tb testing.TB, what string, resp harness.Response, err error) {
	tb.Helper()
	harness.Answers(tb, what+": 302", http.StatusFound, resp, err)
	if err == nil {
		harness.Contains(tb, what+": to the login page", "/login", resp.Header.Get("Location"))
	}
}

// TestForwardedHeaders checks which sources PID 1 keeps forwarding and
// login headers from, end to end through Calibre-Web, in two containers
// at one address on one network, so that the address requests come from
// is the same in both.
//
// With TRUSTED_PROXIES unset, a forged X-Forwarded-For never reaches
// Calibre-Web's log as the client: it logs the address the request came
// from, which the test then lists. An admin creates a user and turns on
// Calibre-Web's header login through its admin pages, and PID 1, which
// reads the header's name again after each admin change, keeps that
// header from the unlisted client: a request with it is not logged in.
// Then, on the same volumes, with the client's address in TRUSTED_PROXIES,
// the same request is logged in as that user, and a forwarded address
// reaches the log ahead of the client's own.
func TestForwardedHeaders(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	tb := redacting(t)
	ctx := tb.Context()
	network, err := e.Network(ctx, tb, "", headersSubnet, false)
	if err != nil {
		tb.Fatal(err)
	}
	sec := newSecrets(tb)
	st := newStore(tb, e)
	reader, readerPW := "reader-"+random(tb, 8, lower), tb.password()
	pws := append(sec.all(), readerPW)
	checks := harness.NewChecks(tb)

	s := (&launch{env: sec.env(), mounts: append(st.mounts(), sec.mount()), network: network, ip: headersIP, passwords: pws}).serve(tb, e, image)
	peer := failWith(tb, s, forgedUnlisted)
	_, perr := netip.ParseAddr(peer)
	checks.True("unlisted: Calibre-Web logs the address the request came from as the client's", peer != forgedUnlisted && perr == nil, peer)
	harness.LogLacks(tb, s.C, "unlisted: the forged X-Forwarded-For never reaches Calibre-Web's log", forgedUnlisted)

	w := loginAdmin(tb, s, sec.web)
	addUser(tb, w, reader, readerPW)
	headerLogin(tb, w, loginHeader)
	resp, err := s.Health().Get(ctx, "/me", http.Header{loginHeader: {reader}})
	notLoggedIn(tb, "unlisted: a request with "+loginHeader+" is not logged in", resp, err)
	if _, code := stop(tb, e, s.C); code != 0 {
		tb.Fatalf("the first container exits %d when stopped", code)
	}

	s = (&launch{env: map[string]string{trustedVar: peer}, mounts: st.mounts(), network: network, ip: headersIP, passwords: pws}).serve(tb, e, image)
	resp, err = s.Health().Get(ctx, "/me", http.Header{loginHeader: {reader}})
	harness.Answers(tb, "listed: a request with "+loginHeader+" is logged in", http.StatusOK, resp, err)
	if err == nil {
		harness.Contains(tb, "listed: logged in as "+reader, reader+"&#39;s Profile", string(resp.Body))
	}
	resp, err = s.Health().Get(ctx, "/me", nil)
	notLoggedIn(tb, "listed: a request without "+loginHeader+" is not logged in", resp, err)
	got := failWith(tb, s, forgedListed)
	harness.Equal(checks, "listed: Calibre-Web logs the forwarded address, then the client's", forgedListed+", "+peer, strings.TrimSpace(got))
}
