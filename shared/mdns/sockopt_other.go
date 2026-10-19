//go:build !unix && !windows

package mdns

import (
	"net"
	"net/netip"
)

func joinGroup(*net.UDPConn, netip.Addr, IfaceAddr) {}
func setMulticastIface(*net.UDPConn, IfaceAddr)     {}
