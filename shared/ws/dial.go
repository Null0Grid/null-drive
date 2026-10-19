package ws

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

var timeZero time.Time

// DialOptions configures Dial.
type DialOptions struct {
	// Header is sent with the handshake (e.g. Authorization, User-Agent).
	Header http.Header
	// TLSConfig is used for wss:// (nil = system roots). The desktop
	// client passes its pinned-certificate config here.
	TLSConfig *tls.Config
	// HandshakeTimeout bounds connect + TLS + upgrade (default 15s).
	HandshakeTimeout time.Duration
}

// HTTPError is a non-101 answer to the handshake (e.g. 401 for a revoked
// device token). Callers use the status to decide whether retrying makes
// sense.
type HTTPError struct {
	Status int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("ws: server answered %d %s instead of switching protocols", e.Status, http.StatusText(e.Status))
}

// Dial opens a client WebSocket to rawURL (ws://, wss://, http:// or
// https://; the latter two are mapped to the former).
func Dial(ctx context.Context, rawURL string, o DialOptions) (*Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	secure := false
	switch u.Scheme {
	case "ws", "http":
	case "wss", "https":
		secure = true
	default:
		return nil, fmt.Errorf("ws: unsupported scheme %q", u.Scheme)
	}
	host := u.Host
	if u.Port() == "" {
		if secure {
			host = net.JoinHostPort(u.Hostname(), "443")
		} else {
			host = net.JoinHostPort(u.Hostname(), "80")
		}
	}
	timeout := o.HandshakeTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = nc.SetDeadline(dl)
	}
	// Abort a hanging handshake when ctx is cancelled.
	stop := context.AfterFunc(ctx, func() { _ = nc.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	if secure {
		conf := &tls.Config{MinVersion: tls.VersionTLS12}
		if o.TLSConfig != nil {
			conf = o.TLSConfig.Clone()
		}
		if conf.ServerName == "" {
			conf.ServerName = u.Hostname()
		}
		// Force HTTP/1.1: the upgrade mechanism does not exist in HTTP/2.
		conf.NextProtos = []string{"http/1.1"}
		tc := tls.Client(nc, conf)
		if err := tc.HandshakeContext(ctx); err != nil {
			nc.Close()
			return nil, err
		}
		nc = tc
	}

	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		nc.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)

	reqURL := *u
	reqURL.Scheme = map[bool]string{false: "http", true: "https"}[secure]
	req, err := http.NewRequest(http.MethodGet, reqURL.String(), nil)
	if err != nil {
		nc.Close()
		return nil, err
	}
	for k, v := range o.Header {
		req.Header[k] = v
	}
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", key)
	req.Header.Set("Sec-WebSocket-Version", "13")
	if err := req.Write(nc); err != nil {
		nc.Close()
		return nil, err
	}
	br := bufio.NewReader(nc)
	res, err := http.ReadResponse(br, req)
	if err != nil {
		nc.Close()
		return nil, err
	}
	if res.StatusCode != http.StatusSwitchingProtocols {
		res.Body.Close()
		nc.Close()
		return nil, &HTTPError{Status: res.StatusCode}
	}
	if !headerHasToken(res.Header.Values("Upgrade"), "websocket") ||
		res.Header.Get("Sec-WebSocket-Accept") != AcceptKey(key) {
		nc.Close()
		return nil, &ProtocolError{"invalid handshake response (a proxy may be interfering)"}
	}
	_ = nc.SetDeadline(timeZero)
	return newConn(nc, br, true), nil
}
