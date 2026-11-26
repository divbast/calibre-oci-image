// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/divbast/ci/harness"
)

// socketsCode is Python that prints, as JSON, the kernel's four socket
// tables for the container's network, the socket inodes the container's
// processes hold, and the processes whose descriptors it could not read.
const socketsCode = `import json, os
held, unreadable = set(), []
for d in os.listdir("/proc"):
    if not d.isdigit():
        continue
    try:
        fds = os.listdir(f"/proc/{d}/fd")
        targets = [os.readlink(f"/proc/{d}/fd/{fd}") for fd in fds]
    except FileNotFoundError:
        continue
    except OSError:
        unreadable.append(d)
        continue
    held.update(t[8:-1] for t in targets if t.startswith("socket:["))
tables = {n: open("/proc/net/" + n).read() for n in ("tcp", "tcp6", "udp", "udp6")}
print(json.dumps({"tables": tables, "held": sorted(held), "unreadable": unreadable}))
`

// socket is one line of a socket table: its two ends, its TCP state in
// hex, 0A for a listener, and its inode.
type socket struct {
	local, remote netip.AddrPort
	state, inode  string
}

// parseTable reads the sockets of one table of /proc/net.
func parseTable(table string) ([]socket, error) {
	var out []socket
	for i, line := range strings.Split(strings.TrimSpace(table), "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) == 0 {
			continue // the header
		}
		if len(f) < 10 {
			return nil, fmt.Errorf("a socket table's line %q has no inode", line)
		}
		local, err := parseEnd(f[1])
		if err != nil {
			return nil, err
		}
		remote, err := parseEnd(f[2])
		if err != nil {
			return nil, err
		}
		out = append(out, socket{local: local, remote: remote, state: f[3], inode: f[9]})
	}
	return out, nil
}

// parseEnd reads one end of a socket as the kernel writes it: the address
// in hex, each 32-bit word in the host's byte order (the little-endian of
// amd64 and arm64), then a colon and the port in hex.
func parseEnd(s string) (netip.AddrPort, error) {
	h, p, ok := strings.Cut(s, ":")
	if !ok {
		return netip.AddrPort{}, fmt.Errorf("%q is no socket address", s)
	}
	b, err := hex.DecodeString(h)
	if err != nil || len(b)%4 != 0 {
		return netip.AddrPort{}, fmt.Errorf("%q is no socket address", s)
	}
	for i := 0; i < len(b); i += 4 {
		slices.Reverse(b[i : i+4])
	}
	a, ok := netip.AddrFromSlice(b)
	port, err := strconv.ParseUint(p, 16, 16)
	if !ok || err != nil {
		return netip.AddrPort{}, fmt.Errorf("%q is no socket address", s)
	}
	return netip.AddrPortFrom(a.Unmap(), uint16(port)), nil
}

// where is a listener as a check shows it: *:<port> on every address.
func where(ap netip.AddrPort) string {
	if ap.Addr().IsUnspecified() {
		return "*:" + strconv.Itoa(int(ap.Port()))
	}
	return ap.String()
}

// TestNoOtherListeners reads the kernel's socket tables in a serving
// container, for the sockets its processes hold: TCP 8080 and 8083 are
// listened on on every address, 18080 and 18083 on 127.0.0.1 alone, and
// nothing else; every other TCP socket is a connection to or from one of
// those four ports; and no UDP socket is open, since Bonjour is off. A
// socket no process in the container holds is the engine's, such as
// Docker's resolver for a network's names, at 127.0.0.11.
func TestNoOtherListeners(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	sec := newSecrets(t)
	s := (&launch{env: sec.env(), mounts: append(newStore(t, e).mounts(), sec.mount()), passwords: sec.all()}).serve(t, e, image)
	out, err := inPython(t.Context(), s.C, socketsCode)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Tables     map[string]string `json:"tables"`
		Held       []string          `json:"held"`
		Unreadable []string          `json:"unreadable"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("reading the socket tables: %v", err)
	}
	ours := []uint16{appPort, webPort, serverLoopback, webLoopback}
	var listening, strays, udp, engine []string
	for _, name := range []string{"tcp", "tcp6", "udp", "udp6"} {
		sockets, err := parseTable(got.Tables[name])
		if err != nil {
			t.Fatal(err)
		}
		for _, so := range sockets {
			switch {
			case !slices.Contains(got.Held, so.inode):
				engine = append(engine, name+" "+so.local.String())
			case strings.HasPrefix(name, "udp"):
				udp = append(udp, name+" "+so.local.String())
			case so.state == "0A":
				listening = append(listening, where(so.local))
			case !slices.Contains(ours, so.local.Port()) && !slices.Contains(ours, so.remote.Port()):
				strays = append(strays, name+" "+so.local.String()+" to "+so.remote.String())
			}
		}
	}
	t.Logf("sockets in the tables that no process in the container holds: %s", none(engine))
	slices.Sort(listening)
	checks := harness.NewChecks(t)
	harness.Equal(checks, "processes whose descriptors cannot be read", "none", none(got.Unreadable))
	harness.Equal(checks, "the TCP listeners", "*:8080, *:8083, 127.0.0.1:18080, 127.0.0.1:18083", none(listening))
	harness.Equal(checks, "TCP sockets that are no connection to or from those four ports", "none", none(strays))
	harness.Equal(checks, "UDP sockets", "none", none(udp))
}

// reconnectCode is Python that asks Calibre-Web's own /reconnect on its
// loopback port, as PID 1 does after a library change, and prints the
// status.
const reconnectCode = `import urllib.request
print(urllib.request.urlopen("http://127.0.0.1:18083/reconnect", timeout=10).status)
`

// TestReconnectNotPublic asks for Calibre-Web's /reconnect on 8083, which
// reloads its library unasked and needs no login: it answers 404, however
// the path is spelt. Calibre-Web serves it on its loopback port, where
// PID 1 uses it.
func TestReconnectNotPublic(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	sec := newSecrets(t)
	s := (&launch{env: sec.env(), mounts: append(newStore(t, e).mounts(), sec.mount()), passwords: sec.all()}).serve(t, e, image)
	for _, path := range []string{"/reconnect", "//reconnect", "/reconnect?x=1", "/%72econnect"} {
		harness.StatusIs(t, s.Health(), "8083: GET "+path+" answers 404", http.StatusNotFound, path)
	}
	out, err := inPython(t.Context(), s.C, reconnectCode)
	if err != nil {
		t.Fatalf("asking Calibre-Web's own /reconnect: %v", err)
	}
	harness.Equal(harness.NewChecks(t), "inside, Calibre-Web's own /reconnect answers 200 on 127.0.0.1:18083", "200", strings.TrimSpace(out))
}

// TestOPDSAndKosync checks the feeds and the sync service that devices
// use, through PID 1: /opds on both ports, and KOReader's sync service,
// /kosync, on 8080, each refuses a request with no login and answers one
// with the admin's.
func TestOPDSAndKosync(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	ctx := t.Context()
	sec := newSecrets(t)
	s := (&launch{env: sec.env(), mounts: append(newStore(t, e).mounts(), sec.mount()), passwords: sec.all()}).serve(t, e, image)
	checks := harness.NewChecks(t)
	feed := func(what string, resp harness.Response, err error) {
		harness.Answers(t, what, http.StatusOK, resp, err)
		if err == nil {
			checks.True(what+" with an Atom feed", bytes.Contains(resp.Body, []byte("<feed")), harness.Clip(string(resp.Body)))
		}
	}

	resp, err := s.Get(ctx, "/opds", nil)
	harness.Answers(t, "8080: /opds with no login answers 401", http.StatusUnauthorized, resp, err)
	resp, err = serverAdmin(ctx, s, sec.admin, "/opds")
	feed("8080: /opds with admin's digest login answers 200", resp, err)

	web := s.Health()
	resp, err = web.Get(ctx, "/opds", nil)
	harness.Answers(t, "8083: /opds with no login answers 401", http.StatusUnauthorized, resp, err)
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte(admin+":"+sec.web))
	resp, err = web.Get(ctx, "/opds", http.Header{"Authorization": {basic}})
	feed("8083: /opds with admin's basic login answers 200", resp, err)

	resp, err = s.Get(ctx, "/kosync/users/auth", nil)
	harness.Answers(t, "8080: /kosync/users/auth with no login answers 401", http.StatusUnauthorized, resp, err)
	resp, err = s.Get(ctx, "/kosync/users/auth", http.Header{"X-Auth-User": {admin}, "X-Auth-Key": {md5Hex(sec.admin)}})
	harness.Answers(t, "8080: /kosync/users/auth with admin's KOReader login answers 200", http.StatusOK, resp, err)
	if err == nil {
		var got struct {
			Authorized string `json:"authorized"`
		}
		uerr := json.Unmarshal(resp.Body, &got)
		checks.True("8080: KOReader's login is authorized", uerr == nil && got.Authorized == "OK", harness.Clip(string(resp.Body)))
	}
}
