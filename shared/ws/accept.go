package ws

import (
	"encoding/base64"
	"net/http"
	"strings"
)

// HandshakeError is a malformed upgrade request; Accept has already
// written an HTTP error response for it.
type HandshakeError struct{ Msg string }

func (e *HandshakeError) Error() string { return "ws: bad handshake: " + e.Msg }

// IsUpgrade reports whether r asks for a WebSocket upgrade.
func IsUpgrade(r *http.Request) bool {
	return headerHasToken(r.Header.Values("Connection"), "upgrade") &&
		headerHasToken(r.Header.Values("Upgrade"), "websocket")
}

// Accept validates a client handshake, hijacks the connection and returns
// the WebSocket. On a bad handshake it writes a 400/426 response and
// returns a *HandshakeError. Origin checks are the caller's job (see
// SameOrigin): what is safe depends on how the caller authenticates.
func Accept(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	fail := func(status int, msg string) (*Conn, error) {
		if status == http.StatusUpgradeRequired {
			w.Header().Set("Sec-WebSocket-Version", "13")
		}
		http.Error(w, msg, status)
		return nil, &HandshakeError{msg}
	}
	if r.Method != http.MethodGet {
		return fail(http.StatusMethodNotAllowed, "websocket handshake must be GET")
	}
	if !IsUpgrade(r) {
		return fail(http.StatusUpgradeRequired, "expected a websocket upgrade")
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		return fail(http.StatusUpgradeRequired, "unsupported websocket version")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if b, err := base64.StdEncoding.DecodeString(key); err != nil || len(b) != 16 {
		return fail(http.StatusBadRequest, "invalid Sec-WebSocket-Key")
	}
	nc, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return fail(http.StatusInternalServerError, "connection cannot be upgraded")
	}
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + AcceptKey(key) + "\r\n\r\n"
	if _, err := brw.WriteString(resp); err != nil {
		nc.Close()
		return nil, err
	}
	if err := brw.Flush(); err != nil {
		nc.Close()
		return nil, err
	}
	// Clear any deadline net/http left on the hijacked connection.
	_ = nc.SetDeadline(timeZero)
	return newConn(nc, brw.Reader, false), nil
}

// SameOrigin reports whether a browser request's Origin matches its Host.
//
// Browsers attach cookies to cross-site WebSocket handshakes (SameSite
// does not help when the attacker page is "same-site", e.g. another
// service on the same NAS hostname), and WebSockets are exempt from CORS.
// Without this check any page could open an authenticated socket as the
// user: Cross-Site WebSocket Hijacking. Requests without an Origin header
// come from non-browser clients, which authenticate with a bearer token
// that a web page cannot obtain.
func SameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	_, hostPart, ok := strings.Cut(origin, "://")
	if !ok {
		return false
	}
	return strings.EqualFold(hostPart, r.Host)
}
