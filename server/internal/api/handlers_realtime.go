package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"

	"github.com/Null0Grid/null-drive/server/internal/realtime"
	"github.com/Null0Grid/null-drive/shared/ws"
)

// handleWS: GET /api/v1/ws upgrades to a WebSocket that pushes
// {"type":"changed","version":N} whenever the user's files change.
//
// Desktop clients authenticate with their device token in the
// Authorization header; the browser UI with its session cookie (browsers
// cannot set headers on a WebSocket handshake). Cookie-authenticated
// sockets are exactly what Cross-Site WebSocket Hijacking abuses, hence
// the Origin check.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !ws.SameOrigin(r) {
		writeError(w, http.StatusForbidden, "bad_origin", "cross-origin websocket connections are not allowed")
		return
	}
	if !ws.IsUpgrade(r) {
		w.Header().Set("Sec-WebSocket-Version", "13")
		writeError(w, http.StatusUpgradeRequired, "upgrade_required", "this endpoint only accepts WebSocket connections")
		return
	}
	version, err := s.store.CurrentVersion(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	p := principalFrom(r.Context())
	conn, err := ws.Accept(w, r)
	if err != nil {
		var he *ws.HandshakeError
		if !errors.As(err, &he) {
			s.log.Warn("websocket accept", "err", err)
		}
		return
	}
	var deviceID int64
	if p.Device != nil {
		deviceID = p.Device.ID
	}
	// The request context ends when the handler returns, which it does
	// only after Serve; the hijacked connection itself is managed by the
	// hub (closed on error, timeout, revoke or shutdown).
	err = s.hub.ServePeer(context.WithoutCancel(r.Context()), conn,
		realtime.Peer{UserID: p.User.ID, DeviceID: deviceID, SessionID: p.SessionID}, version)
	if err != nil && !isNormalClose(err) {
		s.log.Debug("websocket closed", "user", p.User.Username, "device_id", deviceID, "err", err)
	}
}

func isNormalClose(err error) bool {
	var ce *ws.CloseError
	if errors.As(err, &ce) {
		return ce.Code == ws.CloseNormal || ce.Code == ws.CloseGoingAway
	}
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, realtime.ErrTooManyConnections)
}

// changed tells the user's other connections about a new feed version.
func (s *Server) changed(r *http.Request, version int64) {
	p := principalFrom(r.Context())
	var origin int64
	if p.Device != nil {
		origin = p.Device.ID
	}
	s.hub.Notify(p.User.ID, version, origin)
	if s.search != nil {
		s.search.Kick(p.User.ID) // index the change now, not at the next search
	}
}
