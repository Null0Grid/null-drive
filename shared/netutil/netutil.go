// Package netutil holds small network classification helpers shared by the
// server (mDNS address selection) and the desktop client (TLS warnings).
package netutil

import (
	"context"
	"net"
	"net/netip"
	"strings"
)

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// IsLocalIP reports whether ip is loopback, link-local, RFC 1918 / ULA
// private, or in the CGNAT range used by overlay VPNs such as Tailscale
// (whose traffic is already encrypted end to end).
func IsLocalIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || cgnat.Contains(ip)
}

// IsLocalHost reports whether a host name or IP literal refers to the
// local network. Names are judged by their suffix first (.local is mDNS,
// .home.arpa is RFC 8375, single-label names only resolve on the LAN),
// then by resolving them: a public DNS name pointing at a private address
// (split-horizon DNS) still counts as local. resolve may be nil
// (net.DefaultResolver).
func IsLocalHost(ctx context.Context, host string, resolve func(context.Context, string) ([]netip.Addr, error)) bool {
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if ip, err := netip.ParseAddr(host); err == nil {
		return IsLocalIP(ip)
	}
	h := strings.ToLower(host)
	if h == "localhost" || !strings.Contains(h, ".") {
		return true
	}
	for _, suf := range []string{".local", ".home.arpa", ".lan", ".internal", ".localhost"} {
		if strings.HasSuffix(h, suf) {
			return true
		}
	}
	if resolve == nil {
		resolve = func(ctx context.Context, h string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", h)
		}
	}
	ips, err := resolve(ctx, host)
	if err != nil || len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		if !IsLocalIP(ip) {
			return false
		}
	}
	return true
}
