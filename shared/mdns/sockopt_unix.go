//go:build unix

package mdns

import (
	"net"
	"net/netip"
	"syscall"
)

func joinGroup(c *net.UDPConn, group netip.Addr, ia IfaceAddr) {
	rc, err := c.SyscallConn()
	if err != nil {
		return
	}
	_ = rc.Control(func(fd uintptr) {
		if group.Is4() {
			mreq := &syscall.IPMreq{Multiaddr: group.As4(), Interface: ia.Prefix.Addr().As4()}
			_ = syscall.SetsockoptIPMreq(int(fd), syscall.IPPROTO_IP, syscall.IP_ADD_MEMBERSHIP, mreq)
		} else {
			mreq := &syscall.IPv6Mreq{Multiaddr: group.As16(), Interface: uint32(ia.Index)} //nolint:gosec // interface indexes are small
			_ = syscall.SetsockoptIPv6Mreq(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_JOIN_GROUP, mreq)
		}
	})
}

func setMulticastIface(c *net.UDPConn, ia IfaceAddr) {
	rc, err := c.SyscallConn()
	if err != nil {
		return
	}
	_ = rc.Control(func(fd uintptr) {
		if ia.Prefix.Addr().Is4() {
			_ = syscall.SetsockoptInet4Addr(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, ia.Prefix.Addr().As4())
		} else {
			_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_MULTICAST_IF, ia.Index)
		}
	})
}
