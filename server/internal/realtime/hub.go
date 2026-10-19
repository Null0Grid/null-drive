// Package realtime pushes change notifications to connected clients over
// WebSockets and tracks which devices are online.
//
// The push channel carries only "something changed, the feed is now at
// version N" — never file data or metadata. Clients react by calling the
// existing change feed (GET /files?since=), so there is one code path for
// fetching changes whether the wake-up came from a push, a poll or a
// reconnect, and a lost or duplicated notification is harmless.
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Null0Grid/null-drive/shared/api"
	"github.com/Null0Grid/null-drive/shared/ws"
)

// Defaults. PingInterval sits well under the 60s idle timeout that nginx,
// Traefik and most corporate proxies apply to proxied connections, so a
// quiet socket is never cut by an intermediary.
const (
	DefaultPingInterval = 25 * time.Second
	DefaultMaxPerUser   = 32
)

// ErrTooManyConnections is returned by Serve when a user exceeds the limit.
var ErrTooManyConnections = errors.New("too many realtime connections for this user")

// Hub fans out notifications to the connections of each user.
type Hub struct {
	Log          *slog.Logger
	PingInterval time.Duration
	MaxPerUser   int

	mu     sync.Mutex
	users  map[int64]map[*conn]struct{}
	closed bool
}

// NewHub returns a hub with default settings.
func NewHub(log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	return &Hub{Log: log, PingInterval: DefaultPingInterval, MaxPerUser: DefaultMaxPerUser, users: map[int64]map[*conn]struct{}{}}
}

type conn struct {
	ws       *ws.Conn
	userID   int64
	deviceID int64 // 0 for a browser session
	session  int64 // session ID for a browser session, 0 for a device

	// Notifications are coalesced instead of queued: a client only needs
	// the newest version, so a slow or briefly stalled connection costs
	// one int64, never an unbounded queue, and never blocks the hub.
	latest  atomic.Int64
	wake    chan struct{}
	extraMu sync.Mutex
	extra   []api.RealtimeMessage // presence events, bounded
}

func (c *conn) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Serve runs a connection until it closes or ctx ends. It sends a hello
// with the current version first, so a client that reconnects after
// missing notifications immediately knows whether to sync.
func (h *Hub) Serve(ctx context.Context, w *ws.Conn, userID, deviceID, version int64) error {
	return h.ServePeer(ctx, w, Peer{UserID: userID, DeviceID: deviceID}, version)
}

// Peer identifies who holds a connection: a device (DeviceID) or a browser
// session (SessionID), so either can be disconnected when its credential
// is revoked.
type Peer struct {
	UserID, DeviceID, SessionID int64
}

// ServePeer is Serve with the full peer identity.
func (h *Hub) ServePeer(ctx context.Context, w *ws.Conn, p Peer, version int64) error {
	userID := p.UserID
	c := &conn{ws: w, userID: userID, deviceID: p.DeviceID, session: p.SessionID, wake: make(chan struct{}, 1)}
	if err := h.add(c); err != nil {
		_ = w.WriteClose(ws.ClosePolicyViolation, err.Error())
		_ = w.Close()
		return err
	}
	defer h.remove(c)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	readErr := make(chan error, 1)
	go func() { readErr <- h.readLoop(c); cancel() }()

	if err := h.send(c, api.RealtimeMessage{Type: api.RealtimeHello, Version: version}); err != nil {
		_ = w.Close()
		<-readErr
		return err
	}
	sent := version
	ping := time.NewTicker(h.PingInterval)
	defer ping.Stop()
	var err error
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-ping.C:
			if err = w.Ping(nil); err != nil {
				break loop
			}
		case <-c.wake:
			for _, m := range c.takeExtra() {
				if err = h.send(c, m); err != nil {
					break loop
				}
			}
			if v := c.latest.Load(); v > sent {
				if err = h.send(c, api.RealtimeMessage{Type: api.RealtimeChanged, Version: v}); err != nil {
					break loop
				}
				sent = v
			}
		}
	}
	_ = w.Close()
	if rerr := <-readErr; err == nil {
		err = rerr
	}
	return err
}

// readLoop consumes client frames. Clients have nothing to say except
// pongs and keepalives; any frame extends the deadline. Two missed ping
// intervals without a frame mean the peer is gone (a laptop that slept,
// a NAT that dropped the mapping) even though TCP has not noticed yet.
func (h *Hub) readLoop(c *conn) error {
	timeout := 2*h.PingInterval + h.PingInterval/2
	extend := func([]byte) { _ = c.ws.SetReadDeadline(time.Now().Add(timeout)) }
	c.ws.OnPong = extend
	c.ws.OnPing = extend
	c.ws.MaxMessage = 4 << 10
	extend(nil)
	for {
		if _, _, err := c.ws.ReadMessage(); err != nil {
			return err
		}
		extend(nil)
	}
}

func (h *Hub) send(c *conn, m api.RealtimeMessage) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return c.ws.WriteMessage(ws.OpText, b)
}

func (c *conn) addExtra(m api.RealtimeMessage) {
	c.extraMu.Lock()
	if len(c.extra) < 64 {
		c.extra = append(c.extra, m)
	}
	c.extraMu.Unlock()
	c.signal()
}

func (c *conn) takeExtra() []api.RealtimeMessage {
	c.extraMu.Lock()
	defer c.extraMu.Unlock()
	out := c.extra
	c.extra = nil
	return out
}

func (h *Hub) add(c *conn) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return errors.New("server shutting down")
	}
	set := h.users[c.userID]
	if set == nil {
		set = map[*conn]struct{}{}
		h.users[c.userID] = set
	}
	if len(set) >= h.MaxPerUser {
		h.mu.Unlock()
		return ErrTooManyConnections
	}
	wasOnline := c.deviceID != 0 && h.deviceOnlineLocked(c.userID, c.deviceID)
	set[c] = struct{}{}
	h.mu.Unlock()
	if c.deviceID != 0 && !wasOnline {
		h.presence(c.userID, c.deviceID, true)
	}
	return nil
}

func (h *Hub) remove(c *conn) {
	h.mu.Lock()
	set := h.users[c.userID]
	delete(set, c)
	if len(set) == 0 {
		delete(h.users, c.userID)
	}
	stillOnline := c.deviceID != 0 && h.deviceOnlineLocked(c.userID, c.deviceID)
	h.mu.Unlock()
	if c.deviceID != 0 && !stillOnline {
		h.presence(c.userID, c.deviceID, false)
	}
}

func (h *Hub) deviceOnlineLocked(userID, deviceID int64) bool {
	for c := range h.users[userID] {
		if c.deviceID == deviceID {
			return true
		}
	}
	return false
}

// presence tells the user's browser sessions that a device came or went,
// so the devices page can update its online dot without polling.
func (h *Hub) presence(userID, deviceID int64, online bool) {
	h.Log.Debug("presence", "user_id", userID, "device_id", deviceID, "online", online)
	m := api.RealtimeMessage{Type: api.RealtimePresence, DeviceID: deviceID, Online: &online}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.users[userID] {
		if c.deviceID == 0 {
			c.addExtra(m)
		}
	}
}

// Notify announces that the user's change feed reached version. The
// connection(s) of originDevice (the device that made the change) are
// skipped: it already knows. Browser tabs are always notified.
func (h *Hub) Notify(userID, version, originDevice int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.users[userID] {
		if originDevice != 0 && c.deviceID == originDevice {
			continue
		}
		for {
			cur := c.latest.Load()
			if version <= cur || c.latest.CompareAndSwap(cur, version) {
				break
			}
		}
		c.signal()
	}
}

// DisconnectDevice closes a device's sockets (used when it is revoked, so
// it stops receiving even metadata-free notifications at once).
func (h *Hub) DisconnectDevice(userID, deviceID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.users[userID] {
		if c.deviceID == deviceID {
			_ = c.ws.WriteClose(ws.ClosePolicyViolation, "device revoked")
			_ = c.ws.NetConn().Close()
		}
	}
}

// DisconnectSessions closes browser-session sockets of a user: the given
// session (logout), or every session except keep when session is 0
// (password change, which ends all other sessions). Without this a
// signed-out browser tab would keep receiving notifications until its
// next reconnect.
func (h *Hub) DisconnectSessions(userID, session, keep int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.users[userID] {
		if c.deviceID != 0 || c.session == 0 {
			continue
		}
		if (session != 0 && c.session == session) || (session == 0 && c.session != keep) {
			_ = c.ws.WriteClose(ws.ClosePolicyViolation, "signed out")
			_ = c.ws.NetConn().Close()
		}
	}
}

// Online returns the IDs of the user's devices that have a live socket.
func (h *Hub) Online(userID int64) map[int64]bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[int64]bool{}
	for c := range h.users[userID] {
		if c.deviceID != 0 {
			out[c.deviceID] = true
		}
	}
	return out
}

// Count returns the number of open connections (for tests and metrics).
func (h *Hub) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, set := range h.users {
		n += len(set)
	}
	return n
}

// Shutdown sends "going away" to every client and closes the sockets.
// http.Server.Shutdown does not track hijacked connections, so without
// this a server restart would wait for every socket's ping timeout.
// Clients treat 1001 as "reconnect soon".
func (h *Hub) Shutdown() {
	h.mu.Lock()
	h.closed = true
	var all []*conn
	for _, set := range h.users {
		for c := range set {
			all = append(all, c)
		}
	}
	h.mu.Unlock()
	for _, c := range all {
		_ = c.ws.WriteClose(ws.CloseGoingAway, "server restarting")
		_ = c.ws.NetConn().Close()
	}
}
