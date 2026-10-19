package mdns

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sort"
	"strings"
	"time"
)

// Found is a discovered service instance.
type Found struct {
	Instance string            `json:"instance"` // "Null Drive on nas"
	Host     string            `json:"host"`     // "nulldrive-nas.local"
	Port     uint16            `json:"port"`
	Addrs    []netip.Addr      `json:"addrs"`
	TXT      map[string]string `json:"txt"`
}

// BrowseOptions configures Browse.
type BrowseOptions struct {
	// Service is the DNS-SD type (default ServiceType).
	Service string
	// Targets overrides where queries are sent (tests). Default: the IPv4
	// and IPv6 mDNS groups, out of every usable interface.
	Targets []netip.AddrPort
}

// Browse sends DNS-SD queries and collects answers until ctx is done.
//
// Queries are sent from an ephemeral port, which makes them "legacy
// unicast" queries (RFC 6762 §6.7): responders answer straight back to
// that port. The client therefore never binds 5353, so it coexists with
// Bonjour/Avahi/Windows' own mDNS service, which hold that port, and
// needs no firewall rule for inbound multicast. Each query is repeated
// three times because multicast over Wi-Fi is lossy.
func Browse(ctx context.Context, o BrowseOptions) ([]Found, error) {
	if o.Service == "" {
		o.Service = ServiceType
	}
	svcName := strings.TrimSuffix(o.Service, ".") + "." + domain
	q := &Message{ID: 0x4e44, Questions: []Question{{Name: svcName, Type: TypePTR}}}
	pkt, err := q.Pack()
	if err != nil {
		return nil, err
	}

	type sock struct {
		c       *net.UDPConn
		targets []func() // send functions
	}
	var socks []*sock
	open := func(network string) *sock {
		c, err := net.ListenUDP(network, nil)
		if err != nil {
			return nil
		}
		s := &sock{c: c}
		socks = append(socks, s)
		return s
	}
	if len(o.Targets) > 0 {
		for _, t := range o.Targets {
			network := "udp6"
			if t.Addr().Is4() {
				network = "udp4"
			}
			if s := open(network); s != nil {
				dst := t
				s.targets = append(s.targets, func() { _, _ = s.c.WriteToUDPAddrPort(pkt, dst) })
			}
		}
	} else {
		ifaces, err := Interfaces()
		if err != nil {
			return nil, err
		}
		s4, s6 := open("udp4"), open("udp6")
		seen := map[int]bool{}
		for _, ia := range ifaces {
			ia := ia
			if ia.Prefix.Addr().Is4() && s4 != nil {
				s4.targets = append(s4.targets, func() {
					setMulticastIface(s4.c, ia)
					_, _ = s4.c.WriteToUDPAddrPort(pkt, group4)
				})
			} else if !ia.Prefix.Addr().Is4() && s6 != nil && !seen[ia.Index] {
				seen[ia.Index] = true
				dst := netip.AddrPortFrom(group6.Addr().WithZone(zoneFor(ia.Index)), Port)
				s6.targets = append(s6.targets, func() { _, _ = s6.c.WriteToUDPAddrPort(pkt, dst) })
			}
		}
	}
	if len(socks) == 0 {
		return nil, errors.New("mdns: cannot open a UDP socket")
	}
	defer func() {
		for _, s := range socks {
			_ = s.c.Close()
		}
	}()

	acc := newAccumulator(svcName)
	results := make(chan *Message, 64)
	for _, s := range socks {
		go func(c *net.UDPConn) {
			buf := make([]byte, 9000)
			for {
				n, _, err := c.ReadFromUDPAddrPort(buf)
				if err != nil {
					return
				}
				if m, err := Unpack(buf[:n]); err == nil && m.Response {
					select {
					case results <- m:
					default:
					}
				}
			}
		}(s.c)
	}
	send := func() {
		for _, s := range socks {
			for _, f := range s.targets {
				f()
			}
		}
	}
	send()
	resend := time.NewTicker(400 * time.Millisecond)
	defer resend.Stop()
	sends := 1
	for {
		select {
		case <-ctx.Done():
			return acc.found(), nil
		case <-resend.C:
			if sends < 3 {
				send()
				sends++
			}
		case m := <-results:
			acc.add(m)
		}
	}
}

// accumulator joins PTR, SRV, TXT and address records that may arrive in
// different packets (and from multiple interfaces) into Found entries.
type accumulator struct {
	svc       string
	instances map[string]bool // lowercased instance FQDN -> seen
	labels    map[string]string
	srv       map[string]SRV
	txt       map[string][]string
	addrs     map[string]map[netip.Addr]bool // lowercased host -> addrs
}

func newAccumulator(svc string) *accumulator {
	return &accumulator{
		svc: svc, instances: map[string]bool{}, labels: map[string]string{},
		srv: map[string]SRV{}, txt: map[string][]string{}, addrs: map[string]map[netip.Addr]bool{},
	}
}

func (a *accumulator) add(m *Message) {
	for _, rr := range append(append([]Record(nil), m.Answers...), m.Extra...) {
		name := lower(rr.Name)
		switch rr.Type {
		case TypePTR:
			if equalName(rr.Name, a.svc) && rr.TTL > 0 {
				k := lower(rr.Ptr)
				a.instances[k] = true
				a.labels[k] = strings.TrimSuffix(strings.TrimSuffix(rr.Ptr, "."), "."+strings.TrimSuffix(a.svc, "."))
			} else if equalName(rr.Name, a.svc) {
				delete(a.instances, lower(rr.Ptr)) // goodbye
			}
		case TypeSRV:
			a.srv[name] = rr.Srv
		case TypeTXT:
			a.txt[name] = rr.Txt
		case TypeA, TypeAAAA:
			if a.addrs[name] == nil {
				a.addrs[name] = map[netip.Addr]bool{}
			}
			a.addrs[name][rr.Addr] = true
		}
	}
}

func (a *accumulator) found() []Found {
	var out []Found
	for inst := range a.instances {
		srv, ok := a.srv[inst]
		if !ok {
			continue
		}
		f := Found{
			Instance: a.labels[inst],
			Host:     strings.TrimSuffix(srv.Target, "."),
			Port:     srv.Port,
			TXT:      ParseTXT(a.txt[inst]),
		}
		for ip := range a.addrs[lower(srv.Target)] {
			f.Addrs = append(f.Addrs, ip)
		}
		// IPv4 first (works everywhere), then by value, for stable output.
		sort.Slice(f.Addrs, func(i, j int) bool {
			if f.Addrs[i].Is4() != f.Addrs[j].Is4() {
				return f.Addrs[i].Is4()
			}
			return f.Addrs[i].Less(f.Addrs[j])
		})
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Instance < out[j].Instance })
	return out
}
