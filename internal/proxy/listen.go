// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package proxy

import (
	"context"
	"net"
	"strconv"
	"syscall"
)

// hasIPv6 reports whether the kernel can open an IPv6 socket.
func hasIPv6() bool {
	fd, err := syscall.Socket(syscall.AF_INET6, syscall.SOCK_STREAM, 0)
	if err != nil {
		return false
	}
	_ = syscall.Close(fd) // a probe socket, never used
	return true
}

// Listen listens on port at every address: on [::], which takes IPv4 as
// well, or on 0.0.0.0 when the kernel has no IPv6. ctx bounds the setting
// up of the listener alone.
func Listen(ctx context.Context, port int) (net.Listener, error) {
	return listenOn(ctx, port, hasIPv6())
}

func listenOn(ctx context.Context, port int, ipv6 bool) (net.Listener, error) {
	var lc net.ListenConfig
	p := strconv.Itoa(port)
	if ipv6 {
		return lc.Listen(ctx, "tcp", "[::]:"+p)
	}
	// "tcp4": Go would open a dual-stack socket for "tcp" on a wildcard address
	return lc.Listen(ctx, "tcp4", "0.0.0.0:"+p)
}
