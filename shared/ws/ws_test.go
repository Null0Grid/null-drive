package ws

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAcceptKeyRFCExample(t *testing.T) {
	// RFC 6455 §1.3 worked example.
	if got := AcceptKey("dGhlIHNhbXBsZSBub25jZQ=="); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("AcceptKey = %q", got)
	}
}

// echoServer echoes every data message and records errors.
func echoServer(t *testing.T) (*httptest.Server, chan error) {
	t.Helper()
	errs := make(chan error, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Accept(w, r)
		if err != nil {
			errs <- err
			return
		}
		defer c.Close()
		c.MaxMessage = 1024
		for {
			op, data, err := c.ReadMessage()
			if err != nil {
				errs <- err
				return
			}
			if err := c.WriteMessage(op, data); err != nil {
				errs <- err
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv, errs
}

func TestEchoAndClose(t *testing.T) {
	srv, errs := echoServer(t)
	c, err := Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{"hello", strings.Repeat("x", 200), strings.Repeat("y", 1000)} {
		if err := c.WriteMessage(OpText, []byte(msg)); err != nil {
			t.Fatal(err)
		}
		op, got, err := c.ReadMessage()
		if err != nil || op != OpText || string(got) != msg {
			t.Fatalf("echo %d bytes: op=%d len=%d err=%v", len(msg), op, len(got), err)
		}
	}
	// Client-initiated close: the server sees a normal CloseError and
	// echoes it, which the client reads as a CloseError too.
	if err := c.WriteClose(CloseNormal, "bye"); err != nil {
		t.Fatal(err)
	}
	_, _, err = c.ReadMessage()
	var ce *CloseError
	if !errors.As(err, &ce) || ce.Code != CloseNormal {
		t.Fatalf("client read after close: %v", err)
	}
	if err := <-errs; !errors.As(err, &ce) || ce.Reason != "bye" {
		t.Fatalf("server saw %v", err)
	}
	c.Close()
}

func TestPingPong(t *testing.T) {
	srv, _ := echoServer(t)
	c, err := Dial(context.Background(), srv.URL, DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	got := make(chan string, 1)
	c.OnPong = func(d []byte) { got <- string(d) }
	if err := c.Ping([]byte("hb-1")); err != nil {
		t.Fatal(err)
	}
	// Pong frames are consumed inside ReadMessage; send a message so the
	// read returns after the pong was processed.
	_ = c.WriteMessage(OpText, []byte("after"))
	if _, m, err := c.ReadMessage(); err != nil || string(m) != "after" {
		t.Fatal(m, err)
	}
	select {
	case p := <-got:
		if p != "hb-1" {
			t.Fatalf("pong payload %q", p)
		}
	case <-time.After(time.Second):
		t.Fatal("no pong")
	}
}

// rawClient performs a handshake by hand so tests can send arbitrary frames.
func rawClient(t *testing.T, srvURL string) (net.Conn, *bufio.Reader) {
	t.Helper()
	nc, err := net.Dial("tcp", strings.TrimPrefix(srvURL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nc.Close() })
	_, _ = nc.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n"))
	br := bufio.NewReader(nc)
	res, err := http.ReadResponse(br, nil)
	if err != nil || res.StatusCode != 101 {
		t.Fatalf("handshake: %v %v", res, err)
	}
	_ = res.Body.Close() // 101 has no body; frames follow on br
	return nc, br
}

func frameBytes(fin bool, op byte, masked bool, payload []byte) []byte {
	b0 := op
	if fin {
		b0 |= 0x80
	}
	out := []byte{b0}
	l := len(payload)
	mbit := byte(0)
	if masked {
		mbit = 0x80
	}
	switch {
	case l <= 125:
		out = append(out, mbit|byte(l))
	default:
		out = append(out, mbit|126, 0, 0)
		binary.BigEndian.PutUint16(out[2:], uint16(l))
	}
	if masked {
		key := [4]byte{1, 2, 3, 4}
		out = append(out, key[:]...)
		p := append([]byte(nil), payload...)
		maskBytes(key, p)
		return append(out, p...)
	}
	return append(out, payload...)
}

func readServerFrame(t *testing.T, br *bufio.Reader) (byte, []byte) {
	t.Helper()
	c := newConn(nil, br, true)
	f, err := c.readFrame(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	return f.opcode, f.payload
}

func TestFragmentedMessageWithInterleavedPing(t *testing.T) {
	srv, _ := echoServer(t)
	nc, br := rawClient(t, srv.URL)
	_, _ = nc.Write(frameBytes(false, OpText, true, []byte("frag")))
	_, _ = nc.Write(frameBytes(true, OpPing, true, []byte("p")))
	_, _ = nc.Write(frameBytes(false, OpContinuation, true, []byte("men")))
	_, _ = nc.Write(frameBytes(true, OpContinuation, true, []byte("ted")))
	op, p := readServerFrame(t, br)
	if op != OpPong || string(p) != "p" {
		t.Fatalf("want pong first, got %d %q", op, p)
	}
	op, p = readServerFrame(t, br)
	if op != OpText || string(p) != "fragmented" {
		t.Fatalf("got %d %q", op, p)
	}
}

func TestProtocolViolations(t *testing.T) {
	cases := []struct {
		name  string
		frame []byte
		code  int
	}{
		{"unmasked client frame", frameBytes(true, OpText, false, []byte("hi")), CloseProtocolError},
		{"orphan continuation", frameBytes(true, OpContinuation, true, []byte("hi")), CloseProtocolError},
		{"unknown opcode", frameBytes(true, 0x3, true, nil), CloseProtocolError},
		{"oversized", frameBytes(true, OpBinary, true, make([]byte, 2000)), CloseTooBig},
		{"fragmented ping", frameBytes(false, OpPing, true, nil), CloseProtocolError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, errs := echoServer(t)
			nc, br := rawClient(t, srv.URL)
			_, _ = nc.Write(tc.frame)
			op, p := readServerFrame(t, br)
			if op != OpClose || len(p) < 2 || int(binary.BigEndian.Uint16(p)) != tc.code {
				t.Fatalf("want close %d, got op=%d payload=%q", tc.code, op, p)
			}
			if err := <-errs; err == nil {
				t.Fatal("server should have failed")
			}
		})
	}
}

func TestBadHandshakes(t *testing.T) {
	srv, _ := echoServer(t)
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("plain GET: %d", res.StatusCode)
	}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "short")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad key: %d", res.StatusCode)
	}
}

func TestDialRejectsNon101(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := Dial(context.Background(), srv.URL, DialOptions{})
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 401 {
		t.Fatalf("got %v", err)
	}
}

func TestSameOrigin(t *testing.T) {
	mk := func(host, origin string) *http.Request {
		r := httptest.NewRequest("GET", "http://"+host+"/api/v1/ws", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}
	for _, tc := range []struct {
		host, origin string
		want         bool
	}{
		{"nas:8080", "", true},
		{"nas:8080", "http://nas:8080", true},
		{"nas:8080", "http://NAS:8080", true},
		{"nas:8080", "http://evil.example", false},
		{"nas:8080", "http://nas:9000", false},
		{"nas:8080", "null", false},
	} {
		if got := SameOrigin(mk(tc.host, tc.origin)); got != tc.want {
			t.Errorf("host %s origin %q: %v", tc.host, tc.origin, got)
		}
	}
}
