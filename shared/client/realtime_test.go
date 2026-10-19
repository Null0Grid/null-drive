package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Null0Grid/null-drive/shared/ws"
)

type recorder struct {
	mu       sync.Mutex
	versions []int64
	states   []LiveState
	vch      chan int64
	sch      chan LiveState
}

func newRecorder() *recorder {
	return &recorder{vch: make(chan int64, 100), sch: make(chan LiveState, 100)}
}

func (r *recorder) notifier(c *Client) *Notifier {
	return &Notifier{
		Client:        c,
		OnVersion:     func(v int64) { r.mu.Lock(); r.versions = append(r.versions, v); r.mu.Unlock(); r.vch <- v },
		OnState:       func(s LiveState, _ error) { r.mu.Lock(); r.states = append(r.states, s); r.mu.Unlock(); r.sch <- s },
		PingInterval:  40 * time.Millisecond,
		MinBackoff:    10 * time.Millisecond,
		MaxBackoff:    40 * time.Millisecond,
		FallbackAfter: 3,
		FallbackRetry: time.Hour,
		StableAfter:   time.Second,
		rnd:           func() float64 { return 0.5 },
	}
}

func waitFor[T comparable](t *testing.T, ch chan T, want T) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case got := <-ch:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %v", want)
		}
	}
}

// wsServer runs handle for every accepted socket; it counts connections.
func wsServer(t *testing.T, handle func(n int, c *ws.Conn)) (*httptest.Server, *atomic.Int32) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ndd_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		c, err := ws.Accept(w, r)
		if err != nil {
			return
		}
		handle(int(n.Add(1)), c)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func send(c *ws.Conn, typ string, v int64) {
	_ = c.WriteMessage(ws.OpText, []byte(fmt.Sprintf(`{"type":%q,"version":%d}`, typ, v)))
}

// drain reads until the peer goes away, answering pings.
func drain(c *ws.Conn) {
	for {
		if _, _, err := c.ReadMessage(); err != nil {
			return
		}
	}
}

func TestNotifierDeliversAndReconnects(t *testing.T) {
	srv, conns := wsServer(t, func(n int, c *ws.Conn) {
		defer c.Close()
		send(c, "hello", int64(10*n))
		if n == 1 {
			send(c, "changed", 11)
			time.Sleep(50 * time.Millisecond)
			return // drop the connection: client must reconnect
		}
		drain(c)
	})
	rec := newRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- rec.notifier(New(srv.URL, "ndd_test", nil)).Run(ctx) }()

	waitFor(t, rec.vch, 10)
	waitFor(t, rec.vch, 11)
	waitFor(t, rec.sch, LiveConnecting) // dropped
	waitFor(t, rec.vch, 20)             // hello from the second connection
	if conns.Load() != 2 {
		t.Fatalf("connections = %d", conns.Load())
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
}

func TestNotifierKeepaliveDetectsDeadServer(t *testing.T) {
	// The first server sends hello and then goes silent without reading,
	// so it never answers pings: a half-open connection. The client must
	// give up after its read timeout and reconnect.
	release := make(chan struct{})
	srv, conns := wsServer(t, func(n int, c *ws.Conn) {
		defer c.Close()
		send(c, "hello", int64(n))
		if n == 1 {
			<-release
			return
		}
		drain(c)
	})
	defer close(release)
	rec := newRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = rec.notifier(New(srv.URL, "ndd_test", nil)).Run(ctx) }()
	waitFor(t, rec.vch, 1)
	waitFor(t, rec.vch, 2)
	if conns.Load() < 2 {
		t.Fatal("no reconnect")
	}
}

func TestNotifierFallsBackToPolling(t *testing.T) {
	// Every connection dies immediately (like a proxy that accepts the
	// upgrade and then kills the stream).
	srv, conns := wsServer(t, func(_ int, c *ws.Conn) { c.Close() })
	rec := newRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = rec.notifier(New(srv.URL, "ndd_test", nil)).Run(ctx) }()
	waitFor(t, rec.sch, LivePolling)
	if n := conns.Load(); n != 3 {
		t.Fatalf("attempts before fallback = %d, want 3", n)
	}
	time.Sleep(100 * time.Millisecond)
	if n := conns.Load(); n != 3 {
		t.Fatalf("kept retrying fast after fallback: %d", n)
	}
}

func TestNotifierMissingEndpointPollsImmediately(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	rec := newRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = rec.notifier(New(srv.URL, "ndd_test", nil)).Run(ctx) }()
	waitFor(t, rec.sch, LivePolling)
}

func TestNotifierStopsOnRevokedToken(t *testing.T) {
	srv, _ := wsServer(t, func(_ int, c *ws.Conn) { c.Close() })
	rec := newRecorder()
	err := rec.notifier(New(srv.URL, "ndd_revoked", nil)).Run(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Run = %v", err)
	}
}

func TestNotifierGoingAwayReconnectsWithoutCountingFailure(t *testing.T) {
	srv, _ := wsServer(t, func(n int, c *ws.Conn) {
		send(c, "hello", int64(n))
		if n <= 5 {
			_ = c.WriteClose(ws.CloseGoingAway, "restart")
			c.Close()
			return
		}
		drain(c)
	})
	rec := newRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := rec.notifier(New(srv.URL, "ndd_test", nil))
	n.rnd = func() float64 { return 0 } // 1s restart jitter floor
	go func() { _ = n.Run(ctx) }()
	waitFor(t, rec.vch, 1)
	waitFor(t, rec.vch, 2)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, s := range rec.states {
		if s == LivePolling {
			t.Fatal("server restarts must not push the client into polling mode")
		}
	}
}
