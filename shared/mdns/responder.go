package mdns

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"
)

// Port and multicast groups (RFC 6762 §3).
const Port = 5353

var (
	group4 = netip.AddrPortFrom(netip.MustParseAddr("224.0.0.251"), Port)
	group6 = netip.AddrPortFrom(netip.MustParseAddr("ff02::fb"), Port)
)

// IfaceAddr is one address of a local interface.
type IfaceAddr struct {
	Index  int
	Prefix netip.Prefix // address + its subnet
}

// Interfaces lists usable addresses: interfaces that are up and
// multicast-capable, excluding loopback. Docker's bridge interfaces
// (docker0, br-*, veth*) are skipped: advertising a 172.17.x address to
// the LAN only produces "found, but can't connect".
func Interfaces() ([]IfaceAddr, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []IfaceAddr
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		if isVirtualBridge(ifi.Name) {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			ones, _ := ipn.Mask.Size()
			out = append(out, IfaceAddr{Index: ifi.Index, Prefix: netip.PrefixFrom(ip.Unmap(), ones)})
		}
	}
	return out, nil
}

func isVirtualBridge(name string) bool {
	for _, p := range []string{"docker", "br-", "veth", "virbr", "cni", "flannel", "cali"} {
		if len(name) >= len(p) && name[:len(p)] == p {
			return true
		}
	}
	return false
}

// selectAddrs picks the addresses to put in a response to src:
//   - with an override (NULLDRIVE_ADVERTISE_ADDR), exactly those;
//   - otherwise the addresses on the interface(s) whose subnet contains
//     src, so a host with a LAN and a VPN/storage NIC answers each
//     network with the address that network can actually reach;
//   - if no subnet matches (src is routed, or an announcement), all.
//
// IPv6 link-local addresses are only included for link-local askers
// (they are useless without a zone), and never for announcements.
func selectAddrs(ifaces []IfaceAddr, override []netip.Addr, src netip.Addr) []netip.Addr {
	if len(override) > 0 {
		return override
	}
	var idx = -1
	if src.IsValid() {
		for _, ia := range ifaces {
			if ia.Prefix.Contains(src.WithZone("")) {
				idx = ia.Index
				break
			}
		}
	}
	askerLinkLocal := src.IsValid() && src.IsLinkLocalUnicast()
	var out []netip.Addr
	for _, ia := range ifaces {
		if idx >= 0 && ia.Index != idx {
			continue
		}
		a := ia.Prefix.Addr()
		if a.Is6() && a.IsLinkLocalUnicast() && !askerLinkLocal {
			continue
		}
		if a.Is4() && a.IsLinkLocalUnicast() {
			continue // 169.254.x: no DHCP, not worth advertising
		}
		out = append(out, a)
	}
	return out
}

// Responder answers mDNS queries for one service.
type Responder struct {
	Log *slog.Logger
	// Override, if set, replaces interface addresses in answers.
	Override []netip.Addr
	// ifaces is replaceable in tests.
	ifaces func() ([]IfaceAddr, error)

	mu  sync.Mutex
	svc Service
}

// NewResponder returns a responder for svc. Call Serve on one or more
// packet connections (see Listen) and Announce after starting.
func NewResponder(svc Service, log *slog.Logger) *Responder {
	if log == nil {
		log = slog.Default()
	}
	return &Responder{Log: log, svc: svc, ifaces: Interfaces}
}

// SetService replaces the advertised service (rename, TXT change).
func (r *Responder) SetService(svc Service) {
	r.mu.Lock()
	r.svc = svc
	r.mu.Unlock()
}

func (r *Responder) service() Service {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.svc
}

// Handle computes the response to one packet from src. It returns nil if
// the packet needs no answer. unicast reports where the answer goes:
// back to src (legacy/QU queries) or to the multicast group.
func (r *Responder) Handle(pkt []byte, src netip.AddrPort) (resp []byte, unicast bool) {
	q, err := Unpack(pkt)
	if err != nil || q.Response || len(q.Questions) == 0 {
		return nil, false
	}
	svc := r.service()
	ifaces, _ := r.ifaces()
	addrs := selectAddrs(ifaces, r.Override, src.Addr())

	// RFC 6762 §6.7: a query from a port other than 5353 comes from a
	// simple ("legacy") resolver that is not listening on the multicast
	// group. It must get a unicast reply that echoes the query ID and
	// questions, with TTLs capped at 10s and no cache-flush bits.
	legacy := src.Port() != Port
	ptr, rest := svc.records(addrs, false)
	byName := map[string][]Record{}
	for _, rr := range append([]Record{ptr}, rest...) {
		byName[lower(rr.Name)] = append(byName[lower(rr.Name)], rr)
	}

	var answers, extra []Record
	wantUnicast := legacy
	for _, qq := range q.Questions {
		if qq.Unicast {
			wantUnicast = true
		}
		switch {
		case equalName(qq.Name, servicesMeta) && (qq.Type == TypePTR || qq.Type == TypeANY):
			answers = append(answers, Record{Name: servicesMeta, Type: TypePTR, TTL: otherTTL, Ptr: svc.serviceName()})
		case equalName(qq.Name, svc.serviceName()) && (qq.Type == TypePTR || qq.Type == TypeANY):
			if knownAnswer(q.Answers, ptr) {
				continue // the asker already has it (§7.1)
			}
			answers = append(answers, ptr)
			extra = append(extra, rest...) // §12: include SRV, TXT, A/AAAA
		default:
			for _, rr := range byName[lower(qq.Name)] {
				if qq.Type == TypeANY || qq.Type == rr.Type {
					answers = append(answers, rr)
				}
			}
		}
	}
	if len(answers) == 0 {
		return nil, false
	}
	m := &Message{Response: true, Answers: answers, Extra: dedupe(extra, answers)}
	if legacy {
		m.ID = q.ID
		m.Questions = q.Questions
		for _, sec := range [][]Record{m.Answers, m.Extra} {
			for i := range sec {
				sec[i].CacheFlush = false
				sec[i].TTL = min(sec[i].TTL, 10)
			}
		}
	}
	b, err := m.Pack()
	if err != nil {
		r.Log.Warn("mdns: pack response", "err", err)
		return nil, false
	}
	return b, wantUnicast
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func knownAnswer(known []Record, rr Record) bool {
	for _, k := range known {
		if k.Type == rr.Type && equalName(k.Name, rr.Name) && equalName(k.Ptr, rr.Ptr) && k.TTL >= rr.TTL/2 {
			return true
		}
	}
	return false
}

func dedupe(extra, answers []Record) []Record {
	var out []Record
outer:
	for _, e := range extra {
		for _, a := range answers {
			if a.Type == e.Type && equalName(a.Name, e.Name) {
				continue outer
			}
		}
		out = append(out, e)
	}
	return out
}

// PacketConn is the subset of *net.UDPConn the responder uses.
type PacketConn interface {
	ReadFromUDPAddrPort(b []byte) (int, netip.AddrPort, error)
	WriteToUDPAddrPort(b []byte, addr netip.AddrPort) (int, error)
}

// Serve answers queries arriving on conn until reading fails (the conn
// was closed). group is where multicast answers are sent.
func (r *Responder) Serve(conn PacketConn, group netip.AddrPort, setIface func(src netip.Addr)) error {
	buf := make([]byte, 9000)
	for {
		n, src, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return err
		}
		resp, unicast := r.Handle(buf[:n], src)
		if resp == nil {
			continue
		}
		dst := group
		if unicast {
			dst = src
		} else if setIface != nil {
			// Multicast replies leave through the interface the query
			// came in on, not just the default route's.
			setIface(src.Addr())
		}
		if _, err := conn.WriteToUDPAddrPort(resp, dst); err != nil {
			r.Log.Debug("mdns: send", "dst", dst, "err", err)
		}
	}
}

// announcement builds an unsolicited response for one interface's
// addresses (goodbye = TTL 0, sent on shutdown so caches drop us at once).
func (r *Responder) announcement(addrs []netip.Addr, goodbye bool) ([]byte, error) {
	ptr, rest := r.service().records(addrs, goodbye)
	m := &Message{Response: true, Answers: append([]Record{ptr}, rest...)}
	return m.Pack()
}

// Advertiser runs a Responder on the real network: one IPv4 and one IPv6
// socket bound to port 5353, joined to the mDNS group on every usable
// interface.
type Advertiser struct {
	R *Responder

	conns  []*net.UDPConn
	cancel context.CancelFunc
	wg     sync.WaitGroup
	addrs  []netip.Addr
}

// ErrNoInterfaces means no multicast-capable interface was found (e.g. a
// container on a bridge network with only loopback and veth).
var ErrNoInterfaces = errors.New("mdns: no multicast-capable network interface")

// Start opens the sockets, starts answering and announces the service.
func (a *Advertiser) Start() error {
	ifaces, err := a.R.ifaces()
	if err != nil {
		return err
	}
	if len(ifaces) == 0 && len(a.R.Override) == 0 {
		return ErrNoInterfaces
	}
	var errs []error
	for _, fam := range []struct {
		network string
		group   netip.AddrPort
	}{{"udp4", group4}, {"udp6", group6}} {
		c, err := listenGroup(fam.network, fam.group, ifaces)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		a.conns = append(a.conns, c)
		g, conn := fam.group, c
		setIf := func(src netip.Addr) { setMulticastIfaceFor(conn, ifaces, src) }
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			_ = a.R.Serve(conn, g, setIf)
		}()
	}
	if len(a.conns) == 0 {
		return errors.Join(errs...)
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.addrs = selectAddrs(ifaces, a.R.Override, netip.Addr{})
	// RFC 6762 §8.3: announce at least twice, one second apart.
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		for _, d := range []time.Duration{0, time.Second, 4 * time.Second} {
			t := time.NewTimer(d)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
			a.announce(ifaces, false)
		}
	}()
	return nil
}

// Addrs reports the addresses being announced.
func (a *Advertiser) Addrs() []netip.Addr { return a.addrs }

// Reannounce sends a fresh announcement (after a rename or TXT change).
func (a *Advertiser) Reannounce() {
	if ifaces, err := a.R.ifaces(); err == nil {
		a.announce(ifaces, false)
	}
}

func (a *Advertiser) announce(ifaces []IfaceAddr, goodbye bool) {
	// One announcement per interface, each carrying that interface's
	// addresses and sent out of that interface.
	byIndex := map[int][]netip.Addr{}
	var order []int
	for _, ia := range ifaces {
		if _, ok := byIndex[ia.Index]; !ok {
			order = append(order, ia.Index)
		}
		byIndex[ia.Index] = append(byIndex[ia.Index], ia.Prefix.Addr())
	}
	for _, c := range a.conns {
		is4 := c.LocalAddr().(*net.UDPAddr).IP.To4() != nil
		group := group6
		if is4 {
			group = group4
		}
		for _, idx := range order {
			var src netip.Addr
			for _, ad := range byIndex[idx] {
				if ad.Is4() == is4 {
					src = ad
					break
				}
			}
			if !src.IsValid() {
				continue
			}
			addrs := selectAddrs(ifaces, a.R.Override, src)
			pkt, err := a.R.announcement(addrs, goodbye)
			if err != nil {
				continue
			}
			setMulticastIfaceFor(c, ifaces, src)
			dst := group
			if !is4 {
				dst = netip.AddrPortFrom(group.Addr().WithZone(zoneFor(idx)), Port)
			}
			_, _ = c.WriteToUDPAddrPort(pkt, dst)
		}
	}
}

func zoneFor(index int) string {
	if ifi, err := net.InterfaceByIndex(index); err == nil {
		return ifi.Name
	}
	return ""
}

// Stop sends goodbye packets and closes the sockets.
func (a *Advertiser) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
	if ifaces, err := a.R.ifaces(); err == nil && len(a.conns) > 0 {
		a.announce(ifaces, true)
	}
	for _, c := range a.conns {
		_ = c.Close()
	}
	a.wg.Wait()
	a.conns = nil
}

// listenGroup binds the mDNS port and joins the group on every interface
// that has an address of the family. net.ListenMulticastUDP joins only
// one interface; the rest are joined with setsockopt (sockopt_*.go).
func listenGroup(network string, group netip.AddrPort, ifaces []IfaceAddr) (*net.UDPConn, error) {
	c, err := net.ListenMulticastUDP(network, nil, net.UDPAddrFromAddrPort(group))
	if err != nil {
		return nil, err
	}
	seen := map[int]bool{}
	for _, ia := range ifaces {
		if seen[ia.Index] || ia.Prefix.Addr().Is4() != group.Addr().Is4() {
			continue
		}
		seen[ia.Index] = true
		joinGroup(c, group.Addr(), ia)
	}
	return c, nil
}

// setMulticastIfaceFor points outgoing multicast at the interface whose
// subnet contains src (best effort).
func setMulticastIfaceFor(c *net.UDPConn, ifaces []IfaceAddr, src netip.Addr) {
	for _, ia := range ifaces {
		if ia.Prefix.Contains(src.WithZone("")) && ia.Prefix.Addr().Is4() == src.Unmap().Is4() {
			setMulticastIface(c, ia)
			return
		}
	}
}
