package main

import (
	"net/netip"
	"testing"

	"github.com/Null0Grid/null-drive/shared/mdns"
)

func TestToDiscoveredPrefersReachableIPv4(t *testing.T) {
	f := mdns.Found{
		Instance: "Null Drive on nas",
		Port:     8080,
		Addrs: []netip.Addr{
			netip.MustParseAddr("10.10.0.5"), netip.MustParseAddr("192.168.1.20"),
			netip.MustParseAddr("fd00::20"), netip.MustParseAddr("fe80::20"),
		},
		TXT: map[string]string{"name": "nas", "version": "0.5.0", "setup": "done"},
	}
	local := []netip.Prefix{netip.MustParsePrefix("192.168.1.33/24")}
	d, ok := toDiscovered(f, local)
	if !ok || d.URL != "http://192.168.1.20:8080" || d.Name != "nas" || !d.SetupDone || len(d.Addrs) != 4 {
		t.Fatalf("%+v", d)
	}
	f.Addrs = []netip.Addr{netip.MustParseAddr("fd00::20")}
	f.TXT = map[string]string{"setup": "pending"}
	d, _ = toDiscovered(f, nil)
	if d.URL != "http://[fd00::20]:8080" || d.Name != "nas" || d.SetupDone {
		t.Fatalf("%+v", d)
	}
	if _, ok := toDiscovered(mdns.Found{Port: 1}, nil); ok {
		t.Fatal("entry without addresses accepted")
	}
}

func TestHistoryURL(t *testing.T) {
	for in, want := range map[string]string{
		"notes.txt":             "http://nas:8080/files?history=notes.txt",
		"Photos/2026/a b#1.jpg": "http://nas:8080/files/Photos/2026?history=a+b%231.jpg",
	} {
		got, err := historyURL("http://nas:8080/", in)
		if err != nil || got != want {
			t.Errorf("historyURL(%q) = %q %v, want %q", in, got, err, want)
		}
	}
	if _, err := historyURL("http://nas", "../etc/passwd"); err == nil {
		t.Error("invalid path accepted")
	}
}
