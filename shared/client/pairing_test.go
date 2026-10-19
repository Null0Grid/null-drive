package client

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
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
	state := func(raw []byte) tls.ConnectionState {
		return tls.ConnectionState{PeerCertificates: []*x509.Certificate{{Raw: raw}}}
	}
	fp := Fingerprint(der)
	conf := PinnedTLSConfig(fp)
	if conf.VerifyPeerCertificate != nil || conf.VerifyConnection == nil {
		t.Fatal("the pin must be checked in VerifyConnection, which also runs on resumed sessions")
	}
	if err := conf.VerifyConnection(state(der)); err != nil {
		t.Fatal(err)
	}
	// Lowercase / no-colon forms of the same pin also match.
	if err := PinnedTLSConfig(lowerNoColons(fp)).VerifyConnection(state(der)); err != nil {
		t.Fatal(err)
	}
	if err := conf.VerifyConnection(state([]byte("other"))); !errors.Is(err, ErrCertChanged) {
		t.Fatalf("different cert: %v", err)
	}
	if err := conf.VerifyConnection(tls.ConnectionState{}); err == nil {
		t.Fatal("no certificate must fail")
	}
}

// Against a real TLS server: the right pin connects, a wrong one doesn't.
func TestPinnedTLSAgainstServer(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()
	get := func(pin string) error {
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: PinnedTLSConfig(pin)}}
		resp, err := c.Get(ts.URL)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}
	if err := get(Fingerprint(ts.Certificate().Raw)); err != nil {
		t.Fatalf("right pin: %v", err)
	}
	if err := get(Fingerprint([]byte("some other certificate"))); !errors.Is(err, ErrCertChanged) {
		t.Fatalf("wrong pin: %v", err)
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
