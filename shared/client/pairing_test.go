package client

import (
	"errors"
	"testing"
)

func TestPairLink(t *testing.T) {
	link := BuildPairLink("https://nas.example.com:8443", "NULL-7K2Q-9F")
	srv, code, ok, err := ParsePairLink(link)
	if !ok || err != nil || srv != "https://nas.example.com:8443" || code != "NULL-7K2Q-9F" {
		t.Fatalf("%q -> %q %q %v %v", link, srv, code, ok, err)
	}
	if _, _, ok, _ := ParsePairLink("NULL-7K2Q-9F"); ok {
		t.Fatal("bare code is not a link")
	}
	for _, bad := range []string{"nulldrive://pair?code=X", "nulldrive://evil?server=a&code=b", "nulldrive://pair?server=ftp://x&code=b"} {
		if _, _, ok, err := ParsePairLink(bad); !ok || err == nil {
			t.Errorf("%q: ok=%v err=%v", bad, ok, err)
		}
	}
}

func TestPinnedTLSConfig(t *testing.T) {
	if PinnedTLSConfig("") != nil {
		t.Fatal("empty pin must use system verification")
	}
	der := []byte("not really a certificate, but hashing does not care")
	fp := Fingerprint(der)
	conf := PinnedTLSConfig(fp)
	if err := conf.VerifyPeerCertificate([][]byte{der}, nil); err != nil {
		t.Fatal(err)
	}
	// Lowercase / no-colon forms of the same pin also match.
	if err := PinnedTLSConfig(lowerNoColons(fp)).VerifyPeerCertificate([][]byte{der}, nil); err != nil {
		t.Fatal(err)
	}
	if err := conf.VerifyPeerCertificate([][]byte{[]byte("other")}, nil); !errors.Is(err, ErrCertChanged) {
		t.Fatalf("different cert: %v", err)
	}
}

func lowerNoColons(s string) string {
	out := []byte{}
	for _, c := range []byte(s) {
		if c == ':' {
			continue
		}
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		out = append(out, c)
	}
	return string(out)
}
