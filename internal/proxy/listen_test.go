// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package proxy

import (
	"net"
	"strconv"
	"testing"
)

// dialAndAccept connects to addr and checks the listener sees the connection.
func dialAndAccept(t *testing.T, l net.Listener, addr string) {
	t.Helper()
	accepted := make(chan error, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			err = c.Close()
		}
		accepted <- err
	}()
	c, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer func() { _ = c.Close() }()
	if err := <-accepted; err != nil {
		t.Fatalf("accept: %v", err)
	}
}

func port(t *testing.T, l net.Listener) string {
	t.Helper()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

func TestListenDualStack(t *testing.T) {
	t.Parallel()
	if !hasIPv6() {
		t.Skip("this machine has no IPv6")
	}
	l, err := Listen(0)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = l.Close() }()
	if got := l.Addr().(*net.TCPAddr).IP; !got.IsUnspecified() || got.To4() != nil {
		t.Fatalf("listening on %v, want [::]", got)
	}
	dialAndAccept(t, l, net.JoinHostPort("127.0.0.1", port(t, l)))
	dialAndAccept(t, l, net.JoinHostPort("::1", port(t, l)))
}

func TestListenWithoutIPv6(t *testing.T) {
	t.Parallel()
	l, err := listenOn(0, false)
	if err != nil {
		t.Fatalf("listenOn: %v", err)
	}
	defer func() { _ = l.Close() }()
	if got := l.Addr().String(); got != "0.0.0.0:"+port(t, l) {
		t.Fatalf("listening on %s, want 0.0.0.0", got)
	}
	dialAndAccept(t, l, net.JoinHostPort("127.0.0.1", port(t, l)))
}
