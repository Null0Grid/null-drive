// Package mdns implements the small part of Multicast DNS (RFC 6762) and
// DNS-Based Service Discovery (RFC 6763) that Null Drive needs: a
// responder that advertises one service, and a browser that finds it.
//
// It is written against the standard library only. The DNS wire format
// subset (A, AAAA, PTR, SRV, TXT, name compression on read) is ~250 lines
// and fully unit-tested, which is less code to audit than a general DNS
// library and keeps the server binary dependency-light.
package mdns

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// Record types and classes.
const (
	TypeA    uint16 = 1
	TypePTR  uint16 = 12
	TypeTXT  uint16 = 16
	TypeAAAA uint16 = 28
	TypeSRV  uint16 = 33
	TypeANY  uint16 = 255

	ClassIN uint16 = 1
	// In questions the top bit of the class asks for a unicast response
	// ("QU"); in answers it is the cache-flush bit (RFC 6762 §10.2):
	// "this record replaces whatever you cached for this name/type".
	classTopBit uint16 = 0x8000
)

// Header flags.
const (
	flagResponse      uint16 = 0x8000
	flagAuthoritative uint16 = 0x0400
)

// Question is one DNS question.
type Question struct {
	Name    string // fully qualified, lower or mixed case, trailing dot
	Type    uint16
	Unicast bool // QU bit
}

// SRV record data.
type SRV struct {
	Priority, Weight, Port uint16
	Target                 string
}

// Record is a resource record with typed data; exactly one of the data
// fields is used, according to Type.
type Record struct {
	Name       string
	Type       uint16
	CacheFlush bool
	TTL        uint32

	Addr netip.Addr // A, AAAA
	Ptr  string     // PTR
	Srv  SRV        // SRV
	Txt  []string   // TXT
}

// Message is a DNS message.
type Message struct {
	ID        uint16
	Response  bool
	Questions []Question
	Answers   []Record
	Extra     []Record // additional section (authority is ignored)
}

var errTruncated = errors.New("mdns: truncated message")

// ---- encoding ----

func appendName(b []byte, name string) ([]byte, error) {
	name = strings.TrimSuffix(name, ".")
	if name != "" {
		for _, label := range strings.Split(name, ".") {
			if len(label) == 0 || len(label) > 63 {
				return nil, fmt.Errorf("mdns: invalid label %q in %q", label, name)
			}
			b = append(b, byte(len(label)))
			b = append(b, label...)
		}
	}
	return append(b, 0), nil
}

func appendRecord(b []byte, r Record) ([]byte, error) {
	b, err := appendName(b, r.Name)
	if err != nil {
		return nil, err
	}
	class := ClassIN
	if r.CacheFlush {
		class |= classTopBit
	}
	b = binary.BigEndian.AppendUint16(b, r.Type)
	b = binary.BigEndian.AppendUint16(b, class)
	b = binary.BigEndian.AppendUint32(b, r.TTL)
	lenAt := len(b)
	b = append(b, 0, 0)
	switch r.Type {
	case TypeA:
		if !r.Addr.Is4() {
			return nil, errors.New("mdns: A record needs an IPv4 address")
		}
		a := r.Addr.As4()
		b = append(b, a[:]...)
	case TypeAAAA:
		if !r.Addr.Is6() || r.Addr.Is4In6() {
			return nil, errors.New("mdns: AAAA record needs an IPv6 address")
		}
		a := r.Addr.As16()
		b = append(b, a[:]...)
	case TypePTR:
		if b, err = appendName(b, r.Ptr); err != nil {
			return nil, err
		}
	case TypeSRV:
		b = binary.BigEndian.AppendUint16(b, r.Srv.Priority)
		b = binary.BigEndian.AppendUint16(b, r.Srv.Weight)
		b = binary.BigEndian.AppendUint16(b, r.Srv.Port)
		if b, err = appendName(b, r.Srv.Target); err != nil {
			return nil, err
		}
	case TypeTXT:
		if len(r.Txt) == 0 {
			b = append(b, 0) // RFC 6763 §6.1: empty TXT is a single zero byte
		}
		for _, s := range r.Txt {
			if len(s) > 255 {
				return nil, fmt.Errorf("mdns: TXT string too long: %q", s)
			}
			b = append(b, byte(len(s)))
			b = append(b, s...)
		}
	default:
		return nil, fmt.Errorf("mdns: cannot encode record type %d", r.Type)
	}
	rdlen := len(b) - lenAt - 2
	if rdlen > 0xFFFF {
		return nil, errors.New("mdns: record too long")
	}
	binary.BigEndian.PutUint16(b[lenAt:], uint16(rdlen)) //nolint:gosec // bounded by the check above
	return b, nil
}

// Pack encodes the message. Names are not compressed (allowed by the
// spec; our messages are a few hundred bytes).
func (m *Message) Pack() ([]byte, error) {
	b := make([]byte, 12, 512)
	binary.BigEndian.PutUint16(b[0:], m.ID)
	if m.Response {
		binary.BigEndian.PutUint16(b[2:], flagResponse|flagAuthoritative)
	}
	if len(m.Questions) > 0xFFFF || len(m.Answers) > 0xFFFF || len(m.Extra) > 0xFFFF {
		return nil, errors.New("mdns: too many records")
	}
	binary.BigEndian.PutUint16(b[4:], uint16(len(m.Questions))) //nolint:gosec // bounded by the check above
	binary.BigEndian.PutUint16(b[6:], uint16(len(m.Answers)))   //nolint:gosec // bounded by the check above
	binary.BigEndian.PutUint16(b[10:], uint16(len(m.Extra)))    //nolint:gosec // bounded by the check above
	var err error
	for _, q := range m.Questions {
		if b, err = appendName(b, q.Name); err != nil {
			return nil, err
		}
		class := ClassIN
		if q.Unicast {
			class |= classTopBit
		}
		b = binary.BigEndian.AppendUint16(b, q.Type)
		b = binary.BigEndian.AppendUint16(b, class)
	}
	for _, rs := range [][]Record{m.Answers, m.Extra} {
		for _, r := range rs {
			if b, err = appendRecord(b, r); err != nil {
				return nil, err
			}
		}
	}
	return b, nil
}

// ---- decoding ----

// readName decodes a possibly compressed name starting at off and returns
// it with a trailing dot plus the offset just after it in the original
// stream. Compression pointers may only point backwards, and the number
// of jumps is bounded, so a malicious packet cannot loop us forever.
func readName(msg []byte, off int) (string, int, error) {
	var labels []string
	end := -1
	for jumps := 0; ; {
		if off >= len(msg) {
			return "", 0, errTruncated
		}
		l := int(msg[off])
		switch {
		case l == 0:
			if end < 0 {
				end = off + 1
			}
			return strings.Join(labels, ".") + ".", end, nil
		case l&0xC0 == 0xC0:
			if off+1 >= len(msg) {
				return "", 0, errTruncated
			}
			ptr := int(binary.BigEndian.Uint16(msg[off:]) & 0x3FFF)
			if ptr >= off {
				return "", 0, errors.New("mdns: forward compression pointer")
			}
			if jumps++; jumps > 16 {
				return "", 0, errors.New("mdns: too many compression pointers")
			}
			if end < 0 {
				end = off + 2
			}
			off = ptr
		case l&0xC0 != 0:
			return "", 0, errors.New("mdns: unsupported label type")
		default:
			if off+1+l > len(msg) {
				return "", 0, errTruncated
			}
			labels = append(labels, string(msg[off+1:off+1+l]))
			off += 1 + l
			if len(labels) > 128 {
				return "", 0, errors.New("mdns: name too long")
			}
		}
	}
}

func u16(msg []byte, off int) (uint16, error) {
	if off+2 > len(msg) {
		return 0, errTruncated
	}
	return binary.BigEndian.Uint16(msg[off:]), nil
}

// Unpack decodes a message. Records of types we do not use (NSEC, HINFO,
// ...) are skipped, not rejected: real networks are full of them.
func Unpack(msg []byte) (*Message, error) {
	if len(msg) < 12 {
		return nil, errTruncated
	}
	m := &Message{
		ID:       binary.BigEndian.Uint16(msg[0:]),
		Response: binary.BigEndian.Uint16(msg[2:])&flagResponse != 0,
	}
	qd := int(binary.BigEndian.Uint16(msg[4:]))
	counts := [3]int{
		int(binary.BigEndian.Uint16(msg[6:])),
		int(binary.BigEndian.Uint16(msg[8:])),
		int(binary.BigEndian.Uint16(msg[10:])),
	}
	off := 12
	for i := 0; i < qd; i++ {
		name, next, err := readName(msg, off)
		if err != nil {
			return nil, err
		}
		t, err := u16(msg, next)
		if err != nil {
			return nil, err
		}
		c, err := u16(msg, next+2)
		if err != nil {
			return nil, err
		}
		m.Questions = append(m.Questions, Question{Name: name, Type: t, Unicast: c&classTopBit != 0})
		off = next + 4
	}
	for section, n := range counts {
		for i := 0; i < n; i++ {
			r, next, ok, err := readRecord(msg, off)
			if err != nil {
				return nil, err
			}
			off = next
			if !ok {
				continue
			}
			switch section {
			case 0:
				m.Answers = append(m.Answers, r)
			case 2:
				m.Extra = append(m.Extra, r)
			}
		}
	}
	return m, nil
}

func readRecord(msg []byte, off int) (Record, int, bool, error) {
	var r Record
	name, next, err := readName(msg, off)
	if err != nil {
		return r, 0, false, err
	}
	if next+10 > len(msg) {
		return r, 0, false, errTruncated
	}
	r.Name = name
	r.Type = binary.BigEndian.Uint16(msg[next:])
	class := binary.BigEndian.Uint16(msg[next+2:])
	r.CacheFlush = class&classTopBit != 0
	r.TTL = binary.BigEndian.Uint32(msg[next+4:])
	rdlen := int(binary.BigEndian.Uint16(msg[next+8:]))
	start := next + 10
	end := start + rdlen
	if end > len(msg) {
		return r, 0, false, errTruncated
	}
	if class&^classTopBit != ClassIN {
		return r, end, false, nil
	}
	data := msg[start:end]
	switch r.Type {
	case TypeA:
		if rdlen != 4 {
			return r, 0, false, errors.New("mdns: bad A record")
		}
		r.Addr = netip.AddrFrom4([4]byte(data))
	case TypeAAAA:
		if rdlen != 16 {
			return r, 0, false, errors.New("mdns: bad AAAA record")
		}
		r.Addr = netip.AddrFrom16([16]byte(data))
	case TypePTR:
		if r.Ptr, _, err = readName(msg, start); err != nil {
			return r, 0, false, err
		}
	case TypeSRV:
		if rdlen < 7 {
			return r, 0, false, errors.New("mdns: bad SRV record")
		}
		r.Srv.Priority = binary.BigEndian.Uint16(data[0:])
		r.Srv.Weight = binary.BigEndian.Uint16(data[2:])
		r.Srv.Port = binary.BigEndian.Uint16(data[4:])
		if r.Srv.Target, _, err = readName(msg, start+6); err != nil {
			return r, 0, false, err
		}
	case TypeTXT:
		for i := 0; i < len(data); {
			l := int(data[i])
			if i+1+l > len(data) {
				return r, 0, false, errors.New("mdns: bad TXT record")
			}
			if l > 0 {
				r.Txt = append(r.Txt, string(data[i+1:i+1+l]))
			}
			i += 1 + l
		}
	default:
		return r, end, false, nil
	}
	return r, end, true, nil
}

// equalName compares DNS names case-insensitively (RFC 6762 §16: ASCII
// case-insensitive), ignoring a missing trailing dot.
func equalName(a, b string) bool {
	return strings.EqualFold(strings.TrimSuffix(a, "."), strings.TrimSuffix(b, "."))
}
