package client

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/nulldrive/nulldrive/shared/api"
	"github.com/nulldrive/nulldrive/shared/ws"
)

// LiveState describes the realtime connection.
type LiveState string

const (
	// LiveConnecting: (re)connecting after a drop.
	LiveConnecting LiveState = "connecting"
	// LiveConnected: the socket is up; changes arrive within a second.
	LiveConnected LiveState = "live"
	// LivePolling: the socket keeps failing (a proxy that does not pass
	// WebSockets, a captive portal...). Sync relies on periodic polling
	// and the notifier only retries occasionally.
	LivePolling LiveState = "polling"
)

// Notifier keeps a WebSocket to GET /api/v1/ws open and reports the
// server's change-feed version. It never fetches changes itself: the
// caller reacts to OnVersion by running its normal sync, which reads the
// change feed. That way a missed, duplicated or out-of-order notification
// can never corrupt state; at worst it causes one extra or one delayed
// sync, and the periodic poll is still there as a safety net.
type Notifier struct {
	Client *Client
	// OnVersion is called with the version from the hello message (on
	// every (re)connect) and from each change notification.
	OnVersion func(version int64)
	// OnState is called on every state transition. err is the reason for
	// leaving LiveConnected (nil otherwise).
	OnState func(state LiveState, err error)

	// PingInterval (default 25s) keeps NAT mappings and proxies alive and
	// detects a dead server: no frame for 2.5×PingInterval = reconnect.
	PingInterval time.Duration
	// MinBackoff/MaxBackoff bound the reconnect delay (1s / 60s).
	MinBackoff, MaxBackoff time.Duration
	// After FallbackAfter consecutive failed or short-lived connections
	// (default 4) the notifier reports LivePolling and only retries every
	// FallbackRetry (default 5m).
	FallbackAfter int
	FallbackRetry time.Duration
	// A connection that stays up StableAfter (default 30s) counts as
	// healthy and resets the failure count.
	StableAfter time.Duration

	// rnd is replaceable in tests (returns [0,1)).
	rnd func() float64
}

func (n *Notifier) defaults() {
	if n.PingInterval <= 0 {
		n.PingInterval = 25 * time.Second
	}
	if n.MinBackoff <= 0 {
		n.MinBackoff = time.Second
	}
	if n.MaxBackoff <= 0 {
		n.MaxBackoff = time.Minute
	}
	if n.FallbackAfter <= 0 {
		n.FallbackAfter = 4
	}
	if n.FallbackRetry <= 0 {
		n.FallbackRetry = 5 * time.Minute
	}
	if n.StableAfter <= 0 {
		n.StableAfter = 30 * time.Second
	}
	if n.rnd == nil {
		n.rnd = rand.Float64
	}
}

// ErrUnauthorized means the device token was rejected (revoked); the
// notifier stops, since retrying cannot help.
var ErrUnauthorized = errors.New("realtime: device token rejected")

// Run blocks until ctx is done or the token is rejected.
func (n *Notifier) Run(ctx context.Context) error {
	n.defaults()
	state := LiveState("")
	setState := func(s LiveState, err error) {
		if s != state {
			state = s
			if n.OnState != nil {
				n.OnState(s, err)
			}
		}
	}
	fails := 0
	var backoff time.Duration
	for ctx.Err() == nil {
		if state != LivePolling {
			setState(LiveConnecting, nil)
		}
		start := time.Now()
		err := n.session(ctx, func() { setState(LiveConnected, nil) })
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var he *ws.HTTPError
		var ce *ws.CloseError
		var delay time.Duration
		switch {
		case errors.As(err, &he) && he.Status == http.StatusUnauthorized:
			setState(LivePolling, ErrUnauthorized)
			return ErrUnauthorized
		case errors.As(err, &he) && (he.Status == http.StatusNotFound || he.Status == http.StatusUpgradeRequired ||
			he.Status == http.StatusBadRequest || he.Status == http.StatusMethodNotAllowed):
			// The endpoint is missing (older server) or something in
			// between stripped the Upgrade header: retrying soon is
			// pointless. Go straight to polling.
			fails = n.FallbackAfter
		case errors.As(err, &ce) && ce.Code == ws.CloseGoingAway:
			// Server restart: every client gets this at once. Reconnect
			// after a random 1-6s so they do not all hit the new process
			// in the same millisecond (thundering herd).
			fails, backoff = 0, 0
			delay = time.Second + time.Duration(n.rnd()*float64(5*time.Second))
		case time.Since(start) >= n.StableAfter:
			fails, backoff = 0, 0
		default:
			fails++
		}
		if delay == 0 {
			if fails >= n.FallbackAfter {
				setState(LivePolling, err)
				// Jitter the long retry too (50-100%).
				delay = time.Duration((0.5 + n.rnd()/2) * float64(n.FallbackRetry))
			} else {
				if state == LiveConnected {
					setState(LiveConnecting, err)
				}
				backoff = nextDelay(backoff, n.MinBackoff, n.MaxBackoff)
				// "Full jitter": uniform in [0, backoff], floored so a
				// tight loop is impossible.
				delay = max(time.Duration(n.rnd()*float64(backoff)), n.MinBackoff/2)
			}
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
	return ctx.Err()
}

func nextDelay(cur, lo, hi time.Duration) time.Duration {
	if cur < lo {
		return lo
	}
	return min(cur*2, hi)
}

// session runs one connection until it fails.
func (n *Notifier) session(ctx context.Context, connected func()) error {
	c := n.Client
	h := http.Header{}
	h.Set("Authorization", "Bearer "+c.token)
	h.Set("User-Agent", c.UserAgent)
	u := c.base + "/api/v1/ws"
	if rest, ok := strings.CutPrefix(u, "https://"); ok {
		u = "wss://" + rest
	} else if rest, ok := strings.CutPrefix(u, "http://"); ok {
		u = "ws://" + rest
	}
	conn, err := ws.Dial(ctx, u, ws.DialOptions{Header: h, TLSConfig: c.TLS})
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	timeout := 2*n.PingInterval + n.PingInterval/2
	extend := func([]byte) { _ = conn.SetReadDeadline(time.Now().Add(timeout)) }
	conn.OnPing = extend
	conn.OnPong = extend
	extend(nil)

	pingDone := make(chan struct{})
	defer close(pingDone)
	go func() {
		t := time.NewTicker(n.PingInterval)
		defer t.Stop()
		for {
			select {
			case <-pingDone:
				return
			case <-t.C:
				if conn.Ping(nil) != nil {
					return
				}
			}
		}
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		extend(nil)
		var m api.RealtimeMessage
		if json.Unmarshal(data, &m) != nil {
			continue // unknown/garbled message: ignore, stay connected
		}
		switch m.Type {
		case api.RealtimeHello:
			connected()
			fallthrough
		case api.RealtimeChanged:
			if n.OnVersion != nil {
				n.OnVersion(m.Version)
			}
		}
	}
}
