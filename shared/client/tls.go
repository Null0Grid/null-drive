package client

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrCertChanged means the server presented a different certificate from
// the one the user trusted. That is either a legitimate renewal of a
// self-signed certificate or a man-in-the-middle; the client cannot tell,
// so it stops and asks the user to re-confirm the new fingerprint.
var ErrCertChanged = errors.New("the server's certificate changed since you trusted it")

// Fingerprint formats the SHA-256 of a DER certificate the way browsers
// and `openssl x509 -fingerprint -sha256` show it: AB:CD:...
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

func normalizeFingerprint(fp string) string {
	return strings.ToUpper(strings.NewReplacer(":", "", " ", "").Replace(fp))
}

// PinnedTLSConfig trusts exactly one certificate, identified by its
// SHA-256 fingerprint (trust on first use). Chain and hostname checks are
// replaced by the pin: a self-signed homelab certificate has no chain, and
// often names "localhost" or a bare IP. Pinning the exact leaf is
// stronger than either check for this one server. An empty pin returns
// nil (normal system verification).
func PinnedTLSConfig(pin string) *tls.Config {
	if pin == "" {
		return nil
	}
	want := normalizeFingerprint(pin)
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, //nolint:gosec // replaced by VerifyConnection below (certificate pinning)
		// VerifyConnection, not VerifyPeerCertificate: it also runs on
		// resumed TLS sessions, so a resumed session can't skip the pin.
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("server sent no certificate")
			}
			if normalizeFingerprint(Fingerprint(cs.PeerCertificates[0].Raw)) != want {
				return ErrCertChanged
			}
			return nil
		},
	}
}

// NewPinned is New with an optional pinned certificate fingerprint.
func NewPinned(baseURL, token, pin string) *Client {
	conf := PinnedTLSConfig(pin)
	var hc *http.Client
	if conf != nil {
		hc = &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			TLSClientConfig:       conf,
			ResponseHeaderTimeout: 60 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
		}}
	}
	c := New(baseURL, token, hc)
	c.TLS = conf
	return c
}

// CertInfo describes the certificate an HTTPS server presented.
type CertInfo struct {
	Fingerprint string    `json:"fingerprint"`
	Subject     string    `json:"subject"`
	Issuer      string    `json:"issuer"`
	NotAfter    time.Time `json:"not_after"`
	SelfSigned  bool      `json:"self_signed"`
	// Trusted is true when the system trust store and hostname check
	// accept the certificate (no pinning needed).
	Trusted bool `json:"trusted"`
	// Problem explains why it is not trusted.
	Problem string `json:"problem,omitempty"`
}

// ProbeTLS fetches the certificate of an https:// base URL without
// trusting it, then checks it against the system roots. The desktop app
// uses it to show a "trust this certificate?" prompt with the fingerprint.
func ProbeTLS(ctx context.Context, baseURL string) (*CertInfo, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" {
		return nil, nil //nolint:nilnil // no TLS to probe
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "443")
	}
	d := tls.Dialer{Config: &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, //nolint:gosec // only to read the certificate; verified explicitly below
		ServerName:         u.Hostname(),
	}}
	nc, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	defer nc.Close()
	certs := nc.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, errors.New("server sent no certificate")
	}
	leaf := certs[0]
	info := &CertInfo{
		Fingerprint: Fingerprint(leaf.Raw),
		Subject:     leaf.Subject.String(),
		Issuer:      leaf.Issuer.String(),
		NotAfter:    leaf.NotAfter,
		SelfSigned:  leaf.Subject.String() == leaf.Issuer.String(),
	}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	if _, verr := leaf.Verify(x509.VerifyOptions{DNSName: u.Hostname(), Intermediates: inter}); verr != nil {
		info.Problem = verr.Error()
	} else {
		info.Trusted = true
	}
	return info, nil
}
