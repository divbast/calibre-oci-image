// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

// Package proxy serves the image's public ports as reverse proxies to the
// two apps on the loopback address. It keeps forwarding headers and the
// login header only from the sources the operator lists.
package proxy

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// ParseTrusted reads the TRUSTED_PROXIES setting: IP addresses and CIDR
// ranges, separated by commas, with spaces around each entry ignored. A bare
// address stands for itself alone. An empty setting lists no one. An entry
// that is neither an address nor a range is refused, and the error names it.
func ParseTrusted(s string) ([]netip.Prefix, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []netip.Prefix
	for i, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return nil, fmt.Errorf("TRUSTED_PROXIES: entry %d is empty", i+1)
		}
		if strings.Contains(entry, "/") {
			p, err := netip.ParsePrefix(entry)
			if err != nil {
				return nil, fmt.Errorf("TRUSTED_PROXIES: %q is not an IP address or a CIDR range", entry)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(entry)
		if err != nil || a.Zone() != "" {
			return nil, fmt.Errorf("TRUSTED_PROXIES: %q is not an IP address or a CIDR range", entry)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// peerIP reads the address out of a request's RemoteAddr, as IPv4 when it
// is an IPv4 address in IPv6 form. ok is false when it holds no address.
func peerIP(remote string) (ip netip.Addr, ok bool) {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return netip.Addr{}, false
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.WithZone("").Unmap(), true
}

// isTrusted reports whether remote, a request's RemoteAddr, is in list.
func isTrusted(list []netip.Prefix, remote string) bool {
	ip, ok := peerIP(remote)
	if !ok {
		return false
	}
	for _, p := range list {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
