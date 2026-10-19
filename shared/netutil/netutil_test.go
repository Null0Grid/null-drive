package netutil

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

func TestIsLocalHost(t *testing.T) {
	fake := func(_ context.Context, h string) ([]netip.Addr, error) {
		switch h {
		case "drive.example.com":
			return []netip.Addr{netip.MustParseAddr("203.0.113.7")}, nil
		case "nas.example.com": // split-horizon: public name, private IP
			return []netip.Addr{netip.MustParseAddr("192.168.1.20")}, nil
		case "mixed.example.com":
			return []netip.Addr{netip.MustParseAddr("10.0.0.2"), netip.MustParseAddr("198.51.100.1")}, nil
		}
		return nil, errors.New("nxdomain")
	}
	for host, want := range map[string]bool{
		"192.168.1.20": true, "10.1.2.3": true, "172.16.0.1": true, "127.0.0.1": true,
		"100.101.102.103": true, "169.254.1.1": true, "[fd00::1]": true, "::1": true, "fe80::1": true,
		"8.8.8.8": false, "203.0.113.7": false, "[2001:db8::1]": false,
		"nas": true, "localhost": true, "nas.local": true, "nas.home.arpa": true, "nas.lan": true,
		"drive.example.com": false, "nas.example.com": true, "mixed.example.com": false,
		"unknown.example.com": false,
	} {
		if got := IsLocalHost(context.Background(), host, fake); got != want {
			t.Errorf("IsLocalHost(%q) = %v, want %v", host, got, want)
		}
	}
}
