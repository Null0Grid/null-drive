package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	srvapi "github.com/nulldrive/nulldrive/server/internal/api"
	"github.com/nulldrive/nulldrive/server/internal/realtime"
	"github.com/nulldrive/nulldrive/shared/api"
	"github.com/nulldrive/nulldrive/shared/ws"
)

func registerDevice(t *testing.T, sess *client, name string) *client {
	t.Helper()
	r := sess.json(http.MethodPost, "/api/v1/devices", api.CreateDeviceRequest{Name: name})
	want(t, r, http.StatusCreated)
	var dr api.CreateDeviceResponse
	r.decode(t, &dr)
	return &client{t: sess.t, base: sess.base, token: dr.Token}
}

func dialWS(t *testing.T, c *client, hdr http.Header) (*ws.Conn, error) {
	t.Helper()
	if hdr == nil {
		hdr = http.Header{}
	}
	if c.token != "" && hdr.Get("Cookie") == "" {
		hdr.Set("Authorization", "Bearer "+c.token)
	}
	return ws.Dial(context.Background(), strings.Replace(c.base, "http", "ws", 1)+"/api/v1/ws", ws.DialOptions{Header: hdr})
}

func readMsg(t *testing.T, c *ws.Conn) api.RealtimeMessage {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, b, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var m api.RealtimeMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRealtimePush(t *testing.T) {
	ts := newTestServer(t)
	sess := setupAdmin(t, ts)
	devA := registerDevice(t, sess, "A")
	devB := registerDevice(t, sess, "B")

	wsA, err := dialWS(t, devA, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer wsA.Close()
	wsB, err := dialWS(t, devB, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer wsB.Close()
	if m := readMsg(t, wsA); m.Type != api.RealtimeHello {
		t.Fatalf("A first message %+v", m)
	}
	hello := readMsg(t, wsB)
	if hello.Type != api.RealtimeHello {
		t.Fatalf("B first message %+v", hello)
	}

	// The devices list shows both online.
	var dl api.DeviceList
	sess.do(http.MethodGet, "/api/v1/devices", nil, nil).decode(t, &dl)
	for _, d := range dl.Devices {
		if !d.Online {
			t.Fatalf("device %s not online: %+v", d.Name, d)
		}
	}

	// A uploads: B is told, A (the origin) is not.
	start := time.Now()
	r := devA.put("notes.txt", "hello", nil)
	want(t, r, http.StatusCreated)
	var f api.File
	r.decode(t, &f)
	m := readMsg(t, wsB)
	if m.Type != api.RealtimeChanged || m.Version != f.Version {
		t.Fatalf("B got %+v, want changed %d", m, f.Version)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("push took %v", d)
	}
	_ = wsA.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, b, err := wsA.ReadMessage(); err == nil {
		t.Fatalf("origin device was notified of its own change: %s", b)
	}

	// An idempotent retry (same content) changes nothing and pushes nothing.
	want(t, devA.put("notes.txt", "hello", map[string]string{"If-Match": `"` + itoa(f.Version) + `"`}), http.StatusOK)
	_ = wsB.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, b, err := wsB.ReadMessage(); err == nil {
		t.Fatalf("no-op upload caused a push: %s", b)
	}
}

func TestRealtimeRevokeDisconnects(t *testing.T) {
	ts := newTestServer(t)
	sess := setupAdmin(t, ts)
	dev := registerDevice(t, sess, "laptop")
	c, err := dialWS(t, dev, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	readMsg(t, c)
	var dl api.DeviceList
	sess.do(http.MethodGet, "/api/v1/devices", nil, nil).decode(t, &dl)
	want(t, sess.do(http.MethodDelete, "/api/v1/devices/"+itoa(dl.Devices[0].ID), nil, nil), http.StatusOK)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = c.ReadMessage()
	var ce *ws.CloseError
	if !errors.As(err, &ce) || ce.Code != ws.ClosePolicyViolation {
		t.Fatalf("revoked device socket: %v", err)
	}
	// And it cannot reconnect.
	if _, err := dialWS(t, dev, nil); err == nil {
		t.Fatal("revoked token reconnected")
	}
}

func TestRealtimeAuthAndOrigin(t *testing.T) {
	ts := newTestServer(t)
	sess := setupAdmin(t, ts)

	var he *ws.HTTPError
	if _, err := dialWS(t, &client{t: t, base: ts.URL}, nil); !errors.As(err, &he) || he.Status != 401 {
		t.Fatalf("anonymous: %v", err)
	}
	// A browser session (cookie) from the right origin works...
	cookie := http.Header{}
	cookie.Set("Cookie", api.SessionCookieName+"="+sess.token)
	cookie.Set("Origin", ts.URL)
	c, err := dialWS(t, sess, cookie)
	if err != nil {
		t.Fatalf("same-origin cookie socket: %v", err)
	}
	if m := readMsg(t, c); m.Type != api.RealtimeHello {
		t.Fatalf("got %+v", m)
	}

	// ...and receives presence events for devices.
	dev := registerDevice(t, sess, "phone")
	dc, err := dialWS(t, dev, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := readMsg(t, c)
	if m.Type != api.RealtimePresence || m.Online == nil || !*m.Online {
		t.Fatalf("presence: %+v", m)
	}
	dc.Close()
	m = readMsg(t, c)
	if m.Type != api.RealtimePresence || m.Online == nil || *m.Online {
		t.Fatalf("offline presence: %+v", m)
	}
	c.Close()

	// Cross-site WebSocket hijacking: a page on another origin rides the
	// user's cookie. Must be refused.
	evil := http.Header{}
	evil.Set("Cookie", api.SessionCookieName+"="+sess.token)
	evil.Set("Origin", "http://evil.example")
	if _, err := dialWS(t, sess, evil); !errors.As(err, &he) || he.Status != http.StatusForbidden {
		t.Fatalf("cross-origin: %v", err)
	}
	// A plain GET (no upgrade) gets a helpful 426.
	r := sess.do(http.MethodGet, "/api/v1/ws", nil, nil)
	want(t, r, http.StatusUpgradeRequired)
}

func TestRealtimeShutdown(t *testing.T) {
	hub := realtime.NewHub(nil)
	ts := newTestServerWith(t, nil, func(o *srvapi.Options) { o.Hub = hub })
	sess := setupAdmin(t, ts)
	dev := registerDevice(t, sess, "A")
	c, err := dialWS(t, dev, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	readMsg(t, c)
	hub.Shutdown()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = c.ReadMessage()
	var ce *ws.CloseError
	if !errors.As(err, &ce) || ce.Code != ws.CloseGoingAway {
		t.Fatalf("after shutdown: %v", err)
	}
}

// TestRealtimeManyNotifications checks coalescing: a burst of changes
// never blocks the uploader, and the client ends at the newest version.
func TestRealtimeManyNotifications(t *testing.T) {
	ts := newTestServer(t)
	sess := setupAdmin(t, ts)
	devA := registerDevice(t, sess, "A")
	devB := registerDevice(t, sess, "B")
	c, err := dialWS(t, devB, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	readMsg(t, c)
	var last int64
	for i := 0; i < 20; i++ {
		r := devA.put("f"+itoa(int64(i))+".txt", "x"+itoa(int64(i)), nil)
		var f api.File
		r.decode(t, &f)
		last = f.Version
	}
	for {
		m := readMsg(t, c)
		if m.Version == last {
			break
		}
		if m.Version > last {
			t.Fatalf("version %d beyond last %d", m.Version, last)
		}
	}
}
