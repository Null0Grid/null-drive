package mdns

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"reflect"
	"testing"
	"time"
)

var testLog = slog.New(slog.NewTextHandler(io.Discard, nil))

func testService() Service {
	return Service{
		Instance: "Null Drive on nas",
		Host:     "nulldrive-nas",
		Port:     8080,
		TXT:      []string{"txtvers=1", "version=0.5.0", "name=nas", "setup=done"},
	}
}

var lanIfaces = []IfaceAddr{
	{Index: 2, Prefix: netip.MustParsePrefix("192.168.1.20/24")},
	{Index: 2, Prefix: netip.MustParsePrefix("fd00:1::20/64")},
	{Index: 2, Prefix: netip.MustParsePrefix("fe80::20/64")},
	{Index: 3, Prefix: netip.MustParsePrefix("10.10.0.5/16")}, // storage / VPN NIC
}

func testResponder() *Responder {
	r := NewResponder(testService(), testLog)
	r.ifaces = func() ([]IfaceAddr, error) { return lanIfaces, nil }
	return r
}

func TestPackUnpackRoundTrip(t *testing.T) {
	m := &Message{
		ID: 7, Response: true,
		Questions: []Question{{Name: "_nulldrive._tcp.local.", Type: TypePTR, Unicast: true}},
		Answers: []Record{
			{Name: "_nulldrive._tcp.local.", Type: TypePTR, TTL: 4500, Ptr: "Null Drive on nas._nulldrive._tcp.local."},
			{Name: "Null Drive on nas._nulldrive._tcp.local.", Type: TypeSRV, CacheFlush: true, TTL: 120,
				Srv: SRV{Priority: 1, Weight: 2, Port: 8080, Target: "nulldrive-nas.local."}},
			{Name: "Null Drive on nas._nulldrive._tcp.local.", Type: TypeTXT, TTL: 4500, Txt: []string{"a=1", "b"}},
		},
		Extra: []Record{
			{Name: "nulldrive-nas.local.", Type: TypeA, CacheFlush: true, TTL: 120, Addr: netip.MustParseAddr("192.168.1.20")},
			{Name: "nulldrive-nas.local.", Type: TypeAAAA, CacheFlush: true, TTL: 120, Addr: netip.MustParseAddr("fd00:1::20")},
		},
	}
	b, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Unpack(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, m) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, m)
	}
}

func TestEmptyTXTIsOneZeroByte(t *testing.T) {
	b, err := (&Message{Response: true, Answers: []Record{{Name: "x.local.", Type: TypeTXT}}}).Pack()
	if err != nil {
		t.Fatal(err)
	}
	// header 12 + name(1+1+1+5+1=9) + type/class/ttl 8 + rdlength 2 + rdata 1
	if len(b) != 12+9+8+2+1 || b[len(b)-1] != 0 || binary.BigEndian.Uint16(b[len(b)-3:]) != 1 {
		t.Fatalf("empty TXT encoded as % x", b)
	}
}

func TestUnpackCompressedNames(t *testing.T) {
	// A response as Bonjour would send it: the PTR target and the SRV
	// name point back into earlier names.
	msg := []byte{0, 0, 0x84, 0, 0, 0, 0, 2, 0, 0, 0, 0}
	svcOff := len(msg)
	msg = append(msg, 10, '_', 'n', 'u', 'l', 'l', 'd', 'r', 'i', 'v', 'e', 4, '_', 't', 'c', 'p', 5, 'l', 'o', 'c', 'a', 'l', 0)
	msg = append(msg, 0, 12, 0, 1, 0, 0, 0x11, 0x94) // PTR IN 4500
	rdata := []byte{3, 'n', 'a', 's', 0xC0, byte(svcOff)}
	msg = binary.BigEndian.AppendUint16(msg, uint16(len(rdata)))
	instOff := len(msg)
	msg = append(msg, rdata...)
	// SRV whose owner name is a pointer to the PTR target.
	msg = append(msg, 0xC0, byte(instOff), 0, 33, 0x80, 1, 0, 0, 0, 120)
	srv := []byte{0, 0, 0, 0, 0x1F, 0x90, 1, 'h', 0xC0, byte(svcOff + 16)} // h.local.
	msg = binary.BigEndian.AppendUint16(msg, uint16(len(srv)))
	msg = append(msg, srv...)

	m, err := Unpack(msg)
	if err != nil {
		t.Fatal(err)
	}
	if m.Answers[0].Ptr != "nas._nulldrive._tcp.local." || m.Answers[1].Name != "nas._nulldrive._tcp.local." ||
		m.Answers[1].Srv.Target != "h.local." || m.Answers[1].Srv.Port != 8080 || !m.Answers[1].CacheFlush {
		t.Fatalf("decoded %+v", m.Answers)
	}
}

func TestUnpackRejectsHostileInput(t *testing.T) {
	// Pointer to itself (infinite loop) and forward pointer.
	loop := []byte{0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0xC0, 12, 0, 1, 0, 1}
	if _, err := Unpack(loop); err == nil {
		t.Fatal("self-pointer accepted")
	}
	fwd := []byte{0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0xC0, 20, 0, 1, 0, 1}
	if _, err := Unpack(fwd); err == nil {
		t.Fatal("forward pointer accepted")
	}
	// Every truncation of a valid packet errors cleanly (no panic).
	b, _ := (&Message{Response: true, Answers: testResponder().service().recordsForTest()}).Pack()
	for i := 0; i < len(b); i++ {
		_, _ = Unpack(b[:i])
	}
}

func (s Service) recordsForTest() []Record {
	ptr, rest := s.records([]netip.Addr{netip.MustParseAddr("192.168.1.20")}, false)
	return append([]Record{ptr}, rest...)
}

func FuzzUnpack(f *testing.F) {
	b, _ := (&Message{Response: true, Answers: testService().recordsForTest()}).Pack()
	f.Add(b)
	f.Fuzz(func(_ *testing.T, data []byte) { _, _ = Unpack(data) })
}

func query(t *testing.T, id uint16, qs ...Question) []byte {
	t.Helper()
	b, err := (&Message{ID: id, Questions: qs}).Pack()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestHandleMulticastPTRQuery(t *testing.T) {
	r := testResponder()
	src := netip.MustParseAddrPort("192.168.1.50:5353")
	resp, unicast := r.Handle(query(t, 0, Question{Name: "_nulldrive._tcp.local.", Type: TypePTR}), src)
	if resp == nil || unicast {
		t.Fatalf("resp=%v unicast=%v", resp != nil, unicast)
	}
	m, err := Unpack(resp)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != 0 || len(m.Questions) != 0 || len(m.Answers) != 1 || m.Answers[0].Ptr != "Null Drive on nas._nulldrive._tcp.local." {
		t.Fatalf("answers %+v", m)
	}
	var addrs []string
	var sawSRV, sawTXT bool
	for _, rr := range m.Extra {
		switch rr.Type {
		case TypeSRV:
			sawSRV = rr.Srv.Port == 8080 && rr.Srv.Target == "nulldrive-nas.local." && rr.CacheFlush
		case TypeTXT:
			sawTXT = ParseTXT(rr.Txt)["setup"] == "done"
		case TypeA, TypeAAAA:
			addrs = append(addrs, rr.Addr.String())
		}
	}
	// Asked from the 192.168.1.0/24 LAN: only that NIC's routable
	// addresses, not the storage NIC's 10.10.x and not link-local v6.
	if !sawSRV || !sawTXT || !reflect.DeepEqual(addrs, []string{"192.168.1.20", "fd00:1::20"}) {
		t.Fatalf("srv=%v txt=%v addrs=%v", sawSRV, sawTXT, addrs)
	}
}

func TestHandleOtherSubnetGetsItsOwnAddress(t *testing.T) {
	r := testResponder()
	resp, _ := r.Handle(query(t, 0, Question{Name: "nulldrive-nas.local.", Type: TypeA}), netip.MustParseAddrPort("10.10.3.3:5353"))
	m, _ := Unpack(resp)
	if len(m.Answers) != 1 || m.Answers[0].Addr.String() != "10.10.0.5" {
		t.Fatalf("answers %+v", m.Answers)
	}
}

func TestHandleLegacyUnicastQuery(t *testing.T) {
	r := testResponder()
	q := Question{Name: "_NullDrive._TCP.local.", Type: TypePTR} // case-insensitive
	resp, unicast := r.Handle(query(t, 0xBEEF, q), netip.MustParseAddrPort("192.168.1.50:49152"))
	if resp == nil || !unicast {
		t.Fatal("legacy query must get a unicast answer")
	}
	m, _ := Unpack(resp)
	if m.ID != 0xBEEF || len(m.Questions) != 1 || !m.Response {
		t.Fatalf("legacy reply must echo id and question: %+v", m)
	}
	for _, rr := range append(m.Answers, m.Extra...) {
		if rr.TTL > 10 || rr.CacheFlush {
			t.Fatalf("legacy reply record %+v: TTL must be <=10 and no cache-flush", rr)
		}
	}
}

func TestHandleQUBitAndKnownAnswer(t *testing.T) {
	r := testResponder()
	src := netip.MustParseAddrPort("192.168.1.50:5353")
	_, unicast := r.Handle(query(t, 0, Question{Name: "_nulldrive._tcp.local.", Type: TypePTR, Unicast: true}), src)
	if !unicast {
		t.Fatal("QU question must be answered unicast")
	}
	// Known-answer suppression: the asker already has our PTR.
	ptr, _ := testService().records(nil, false)
	b, _ := (&Message{Questions: []Question{{Name: "_nulldrive._tcp.local.", Type: TypePTR}}, Answers: []Record{ptr}}).Pack()
	if resp, _ := r.Handle(b, src); resp != nil {
		t.Fatal("known answer was not suppressed")
	}
}

func TestHandleIgnoresIrrelevant(t *testing.T) {
	r := testResponder()
	src := netip.MustParseAddrPort("192.168.1.50:5353")
	if resp, _ := r.Handle(query(t, 0, Question{Name: "_http._tcp.local.", Type: TypePTR}), src); resp != nil {
		t.Fatal("answered someone else's service")
	}
	b, _ := (&Message{Response: true, Answers: testService().recordsForTest()}).Pack()
	if resp, _ := r.Handle(b, src); resp != nil {
		t.Fatal("answered a response")
	}
	if resp, _ := r.Handle([]byte{1, 2, 3}, src); resp != nil {
		t.Fatal("answered garbage")
	}
	// Service enumeration lists our type.
	resp, _ := r.Handle(query(t, 0, Question{Name: "_services._dns-sd._udp.local.", Type: TypePTR}), src)
	m, _ := Unpack(resp)
	if len(m.Answers) != 1 || m.Answers[0].Ptr != "_nulldrive._tcp.local." {
		t.Fatalf("enumeration: %+v", m.Answers)
	}
}

func TestSelectAddrs(t *testing.T) {
	str := func(as []netip.Addr) []string {
		var out []string
		for _, a := range as {
			out = append(out, a.String())
		}
		return out
	}
	if got := str(selectAddrs(lanIfaces, nil, netip.Addr{})); !reflect.DeepEqual(got, []string{"192.168.1.20", "fd00:1::20", "10.10.0.5"}) {
		t.Errorf("announcement addrs %v", got)
	}
	if got := str(selectAddrs(lanIfaces, nil, netip.MustParseAddr("fe80::99%eth0"))); !reflect.DeepEqual(got, []string{"192.168.1.20", "fd00:1::20", "fe80::20"}) {
		t.Errorf("link-local asker %v", got)
	}
	ov := []netip.Addr{netip.MustParseAddr("192.168.1.99")}
	if got := str(selectAddrs(lanIfaces, ov, netip.MustParseAddr("192.168.1.5"))); !reflect.DeepEqual(got, []string{"192.168.1.99"}) {
		t.Errorf("override %v", got)
	}
}

func TestLabels(t *testing.T) {
	if got := SanitizeLabel("My NAS_01.lan"); got != "my-nas-01-lan" {
		t.Errorf("SanitizeLabel = %q", got)
	}
	long := "Null Drive on " + string(make([]byte, 0)) + "ééééééééééééééééééééééééééééééééééé"
	if got := TruncateLabel(long); len(got) > 63 || !utf8Valid(got) {
		t.Errorf("TruncateLabel = %q (%d)", got, len(got))
	}
	if got := TruncateLabel("a.b"); got != "a b" {
		t.Errorf("dots must not survive in an instance label: %q", got)
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

// TestBrowseAgainstResponder runs the real browser against the real
// responder over loopback UDP (unicast, since CI containers rarely route
// multicast). The browser's queries come from an ephemeral port, so this
// is the legacy-unicast path that real clients use.
func TestBrowseAgainstResponder(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	r := testResponder()
	go func() { _ = r.Serve(conn, group4, nil) }()
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	found, err := Browse(ctx, BrowseOptions{Targets: []netip.AddrPort{conn.LocalAddr().(*net.UDPAddr).AddrPort()}})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("found %+v", found)
	}
	f := found[0]
	if f.Instance != "Null Drive on nas" || f.Port != 8080 || f.Host != "nulldrive-nas.local" ||
		f.TXT["version"] != "0.5.0" || len(f.Addrs) != 3 || f.Addrs[0].String() != "10.10.0.5" {
		t.Fatalf("found %+v", f)
	}

	// Rename: browsers see the new instance name.
	svc := testService()
	svc.Instance = "Null Drive on attic"
	r.SetService(svc)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	found, _ = Browse(ctx2, BrowseOptions{Targets: []netip.AddrPort{conn.LocalAddr().(*net.UDPAddr).AddrPort()}})
	if len(found) != 1 || found[0].Instance != "Null Drive on attic" {
		t.Fatalf("after rename %+v", found)
	}
}

func TestAccumulatorGoodbye(t *testing.T) {
	a := newAccumulator("_nulldrive._tcp.local.")
	ptr, rest := testService().records([]netip.Addr{netip.MustParseAddr("192.168.1.20")}, false)
	a.add(&Message{Response: true, Answers: append([]Record{ptr}, rest...)})
	if len(a.found()) != 1 {
		t.Fatal("not found")
	}
	gptr, _ := testService().records(nil, true)
	a.add(&Message{Response: true, Answers: []Record{gptr}})
	if len(a.found()) != 0 {
		t.Fatal("goodbye ignored")
	}
}

// TestLiveMulticast exercises real multicast sockets. It is opt-in
// (NULLDRIVE_MDNS_LIVE=1) because many CI runners and containers have no
// multicast route; see docs/DESIGN.md for the manual LAN checklist.
func TestLiveMulticast(t *testing.T) {
	if os.Getenv("NULLDRIVE_MDNS_LIVE") != "1" {
		t.Skip("set NULLDRIVE_MDNS_LIVE=1 to run against the real network")
	}
	adv := &Advertiser{R: NewResponder(testService(), testLog)}
	if err := adv.Start(); err != nil {
		t.Fatal(err)
	}
	defer adv.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	found, err := Browse(ctx, BrowseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		if f.Instance == "Null Drive on nas" {
			t.Logf("found %+v", f)
			return
		}
	}
	t.Fatalf("not found over multicast; got %+v", found)
}
