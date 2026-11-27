// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

// Package proxy serves the image's public ports as reverse proxies to the
// two apps on the loopback address. It keeps forwarding headers and the
// login header only from the sources the operator lists.
package proxy

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// ParseTrusted reads the TRUSTED_PROXIES setting: IP addresses and CIDR
// ranges, separated by commas, with spaces around each entry ignored. A bare
// address stands for itself alone. An IPv4 address or range in IPv6 form,
// ::ffff:a.b.c.d, is read as the IPv4 one, as the proxy reads its peers.
// An empty setting lists no one. An entry that is neither an address nor
// a range is refused, and the error names it.
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
			p, err = unmapPrefix(p)
			if err != nil {
				return nil, fmt.Errorf("TRUSTED_PROXIES: %q %w", entry, err)
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

// unmapPrefix returns p, a range written as IPv4 in IPv6 form
// (::ffff:a.b.c.d/n), as the IPv4 range it is (a.b.c.d/n-96), as a bare
// address in that form is read: the proxy reads every peer in that form
// as IPv4, so the range as written would match none. A range of that
// form shorter than /96 holds more than IPv4 addresses, and is refused.
// Any other range is returned as it is.
func unmapPrefix(p netip.Prefix) (netip.Prefix, error) {
	if !p.Addr().Is4In6() {
		return p, nil
	}
	if p.Bits() < 96 {
		return netip.Prefix{}, errors.New("is an IPv4 address in IPv6 form with a range shorter than /96, which holds more than IPv4 addresses: write the IPv4 range itself, such as 10.0.0.0/8")
	}
	return netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96), nil
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
