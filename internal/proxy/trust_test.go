// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package proxy

import (
	"net/netip"
	"strings"
	"testing"
)

func TestParseTrusted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		want    []string
		wantErr string // a part of the message, naming the bad entry
	}{
		{name: "empty", in: "", want: nil},
		{name: "blank", in: "  ", want: nil},
		{name: "bare v4", in: "10.0.0.1", want: []string{"10.0.0.1/32"}},
		{name: "bare v6", in: "fd00::1", want: []string{"fd00::1/128"}},
		{name: "cidr v4", in: "172.16.0.0/12", want: []string{"172.16.0.0/12"}},
		{name: "cidr v6", in: "fd00::/8", want: []string{"fd00::/8"}},
		{name: "spaces", in: " 10.0.0.1 ,  192.168.0.0/16 ", want: []string{"10.0.0.1/32", "192.168.0.0/16"}},
		{name: "host bits are cleared", in: "10.1.2.3/8", want: []string{"10.0.0.0/8"}},
		{name: "mapped bare v4", in: "::ffff:10.0.0.1", want: []string{"10.0.0.1/32"}},
		{name: "mapped cidr v4", in: "::ffff:10.0.0.0/104", want: []string{"10.0.0.0/8"}},
		{name: "mapped cidr, host bits cleared", in: "::ffff:10.1.2.3/104", want: []string{"10.0.0.0/8"}},
		{name: "mapped cidr, one address", in: "::ffff:192.0.2.10/128", want: []string{"192.0.2.10/32"}},
		{name: "mapped cidr, every v4", in: "::ffff:0.0.0.0/96", want: []string{"0.0.0.0/0"}},
		{name: "mapped cidr, wider than v4", in: "::ffff:10.0.0.0/95", wantErr: "::ffff:10.0.0.0/95"},
		{name: "bad name", in: "10.0.0.1,proxy.example", wantErr: "proxy.example"},
		{name: "bad prefix length", in: "10.0.0.0/33", wantErr: "10.0.0.0/33"},
		{name: "zone", in: "fe80::1%eth0", wantErr: "fe80::1%eth0"},
		{name: "empty entry", in: "10.0.0.1,,10.0.0.2", wantErr: "entry 2"},
		{name: "trailing comma", in: "10.0.0.1,", wantErr: "entry 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseTrusted(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParseTrusted(%q) error = %v, want one naming %q", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTrusted(%q): %v", tt.in, err)
			}
			var gs []string
			for _, p := range got {
				gs = append(gs, p.String())
			}
			if strings.Join(gs, " ") != strings.Join(tt.want, " ") {
				t.Fatalf("ParseTrusted(%q) = %v, want %v", tt.in, gs, tt.want)
			}
		})
	}
}

// TestMappedRangeMatches: a range written as IPv4 in IPv6 form matches
// the peers in it, which reach the proxy as IPv4, or as IPv4 in IPv6
// form, and no other.
func TestMappedRangeMatches(t *testing.T) {
	t.Parallel()
	list, err := ParseTrusted("::ffff:10.0.0.0/104")
	if err != nil {
		t.Fatal(err)
	}
	for remote, want := range map[string]bool{
		"10.1.2.3:4000":          true,
		"[::ffff:10.1.2.3]:4000": true,
		"11.1.2.3:4000":          false,
		"[::ffff:11.1.2.3]:4000": false,
	} {
		if got := isTrusted(list, remote); got != want {
			t.Errorf("isTrusted(%q) = %v, want %v", remote, got, want)
		}
	}
}

func TestIsTrusted(t *testing.T) {
	t.Parallel()
	list := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("fd00::/8")}
	tests := []struct {
		remote string
		want   bool
	}{
		{"10.1.2.3:4000", true},
		{"[::ffff:10.1.2.3]:4000", true},
		{"[fd00::5]:4000", true},
		{"[fd00::5%eth0]:4000", true},
		{"11.1.2.3:4000", false},
		{"[2001:db8::1]:4000", false},
		{"10.1.2.3", false}, // no port: not a TCP peer address
		{"garbage", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.remote, func(t *testing.T) {
			t.Parallel()
			if got := isTrusted(list, tt.remote); got != tt.want {
				t.Fatalf("isTrusted(%q) = %v, want %v", tt.remote, got, tt.want)
			}
		})
	}
	if isTrusted(nil, "10.1.2.3:1") {
		t.Fatal("an empty list trusted a peer")
	}
}
