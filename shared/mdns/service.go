package mdns

import (
	"net/netip"
	"strings"
)

// Well-known names.
const (
	// ServiceType is Null Drive's DNS-SD service type.
	ServiceType = "_nulldrive._tcp"
	// servicesMeta is the DNS-SD "list all service types" name, queried
	// by generic browsers (dns-sd -B _services._dns-sd._udp, Discovery.app).
	servicesMeta = "_services._dns-sd._udp.local."
	domain       = "local."
)

// TTLs from RFC 6762 §10: 120s for records tied to a host (SRV, A/AAAA),
// 75 minutes for the others.
const (
	hostTTL  = 120
	otherTTL = 4500
)

// Service is one advertised service instance.
type Service struct {
	// Instance is the human-readable instance label, e.g.
	// "Null Drive on nas". It may contain spaces but not dots.
	Instance string
	// Host is the single label used for the A/AAAA records (".local."
	// is appended), e.g. "nulldrive-nas".
	Host string
	Port uint16
	// TXT holds key=value strings (RFC 6763 §6).
	TXT []string
}

func (s Service) serviceName() string  { return ServiceType + "." + domain }
func (s Service) instanceName() string { return s.Instance + "." + s.serviceName() }
func (s Service) hostName() string     { return s.Host + "." + domain }

// records builds the full record set for the given addresses, ttl scaled
// (0 = goodbye packet). A/AAAA follow the address families supplied.
func (s Service) records(addrs []netip.Addr, goodbye bool) (ptr Record, rest []Record) {
	ttl := func(t uint32) uint32 {
		if goodbye {
			return 0
		}
		return t
	}
	ptr = Record{Name: s.serviceName(), Type: TypePTR, TTL: ttl(otherTTL), Ptr: s.instanceName()}
	rest = []Record{
		// SRV/TXT/A/AAAA are "unique" records owned by this host, so they
		// carry the cache-flush bit; the shared PTR does not.
		{Name: s.instanceName(), Type: TypeSRV, CacheFlush: true, TTL: ttl(hostTTL),
			Srv: SRV{Port: s.Port, Target: s.hostName()}},
		{Name: s.instanceName(), Type: TypeTXT, CacheFlush: true, TTL: ttl(otherTTL), Txt: s.TXT},
	}
	for _, a := range addrs {
		t := TypeA
		if !a.Is4() {
			t = TypeAAAA
		}
		rest = append(rest, Record{Name: s.hostName(), Type: t, CacheFlush: true, TTL: ttl(hostTTL), Addr: a})
	}
	return ptr, rest
}

// SanitizeLabel makes a string safe as a DNS label for a host name:
// lowercase letters, digits and hyphens, at most 63 bytes.
func SanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 63 {
		out = strings.TrimRight(out[:63], "-")
	}
	if out == "" {
		out = "nulldrive"
	}
	return out
}

// TruncateLabel cuts an instance label to 63 bytes without splitting a
// UTF-8 sequence.
func TruncateLabel(s string) string {
	s = strings.ReplaceAll(s, ".", " ")
	if len(s) <= 63 {
		return s
	}
	cut := 63
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}

// ParseTXT turns ["k=v", "flag"] into a map (keys lowercased, RFC 6763
// §6.4; a key without "=" maps to "").
func ParseTXT(txt []string) map[string]string {
	m := map[string]string{}
	for _, kv := range txt {
		k, v, _ := strings.Cut(kv, "=")
		k = strings.ToLower(k)
		if _, dup := m[k]; !dup && k != "" { // first occurrence wins (§6.4)
			m[k] = v
		}
	}
	return m
}
