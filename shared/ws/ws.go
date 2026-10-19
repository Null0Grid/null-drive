// Package ws is a small, dependency-free WebSocket (RFC 6455) implementation
// covering what Null Drive needs: the server handshake (Accept), the client
// handshake (Dial), text/binary messages with fragmentation, and the
// ping/pong/close control frames.
//
// Why not a library: the realtime channel carries tiny JSON notifications
// ("changed, version 42"), never bulk data, so permessage-deflate,
// subprotocols and streaming writers are unnecessary. ~400 lines of
// stdlib code keeps the server a single dependency-light binary and the
// framing rules are covered by tests in this package.
package ws

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // mandated by RFC 6455 for Sec-WebSocket-Accept; not used for security
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// Opcodes (RFC 6455 §5.2).
const (
	OpContinuation = 0x0
	OpText         = 0x1
	OpBinary       = 0x2
	OpClose        = 0x8
	OpPing         = 0x9
	OpPong         = 0xA
)

// Close status codes used by Null Drive (RFC 6455 §7.4.1).
const (
	CloseNormal          = 1000
	CloseGoingAway       = 1001
	CloseProtocolError   = 1002
	CloseUnsupported     = 1003
	CloseNoStatus        = 1005
	CloseTooBig          = 1009
	ClosePolicyViolation = 1008
)

const guid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// DefaultMaxMessage bounds the size of one (reassembled) message. Realtime
// notifications are a few dozen bytes; anything large is a bug or abuse.
const DefaultMaxMessage = 64 << 10

var (
	// ErrClosed is returned after a close frame was received or sent.
	ErrClosed = errors.New("ws: connection closed")
	// ErrTooBig is returned when a message exceeds MaxMessage.
	ErrTooBig = errors.New("ws: message too big")
)

// CloseError carries the peer's close code and reason.
type CloseError struct {
	Code   int
	Reason string
}

func (e *CloseError) Error() string {
	return fmt.Sprintf("ws: closed by peer (%d %s)", e.Code, e.Reason)
}

// ProtocolError is a framing violation by the peer.
type ProtocolError struct{ Msg string }

func (e *ProtocolError) Error() string { return "ws: protocol error: " + e.Msg }

// AcceptKey computes Sec-WebSocket-Accept for a Sec-WebSocket-Key.
func AcceptKey(key string) string {
	h := sha1.New() //nolint:gosec // see import comment
	h.Write([]byte(key + guid))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// Conn is one WebSocket connection. Reads must come from one goroutine;
// writes are safe from many (they are serialized internally).
type Conn struct {
	nc       net.Conn
	br       *bufio.Reader
	isClient bool // clients mask frames; servers must not

	// MaxMessage limits incoming message size (DefaultMaxMessage if 0).
	MaxMessage int
	// OnPong is called (on the reading goroutine) for each pong received.
	OnPong func(data []byte)
	// OnPing is called for each ping received, after the automatic pong.
	OnPing func(data []byte)

	wmu       sync.Mutex
	closeSent bool
	closed    bool
}

func newConn(nc net.Conn, br *bufio.Reader, isClient bool) *Conn {
	if br == nil {
		br = bufio.NewReader(nc)
	}
	return &Conn{nc: nc, br: br, isClient: isClient}
}

// NetConn exposes the underlying connection (for deadlines and addresses).
func (c *Conn) NetConn() net.Conn { return c.nc }

// SetReadDeadline sets the deadline for the next frame read. Keepalive is
// implemented by extending it whenever any frame (including a pong) arrives.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.nc.SetReadDeadline(t) }

type frame struct {
	fin     bool
	opcode  byte
	payload []byte
}

func (c *Conn) readFrame(limit int) (frame, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
		return frame{}, err
	}
	f := frame{fin: hdr[0]&0x80 != 0, opcode: hdr[0] & 0x0F}
	if hdr[0]&0x70 != 0 {
		return f, &ProtocolError{"reserved bits set (no extensions were negotiated)"}
	}
	masked := hdr[1]&0x80 != 0
	if masked == c.isClient {
		// RFC 6455 §5.1: client->server frames MUST be masked, and
		// server->client frames MUST NOT be.
		if c.isClient {
			return f, &ProtocolError{"server sent a masked frame"}
		}
		return f, &ProtocolError{"client sent an unmasked frame"}
	}
	n := uint64(hdr[1] & 0x7F)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(c.br, b[:]); err != nil {
			return f, err
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(c.br, b[:]); err != nil {
			return f, err
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if f.opcode >= 0x8 {
		if n > 125 || !f.fin {
			return f, &ProtocolError{"control frames must be unfragmented and at most 125 bytes"}
		}
	} else if n > uint64(limit) { //nolint:gosec // limit is a positive message-size bound
		return f, ErrTooBig
	}
	var key [4]byte
	if masked {
		if _, err := io.ReadFull(c.br, key[:]); err != nil {
			return f, err
		}
	}
	f.payload = make([]byte, n)
	if _, err := io.ReadFull(c.br, f.payload); err != nil {
		return f, err
	}
	if masked {
		maskBytes(key, f.payload)
	}
	return f, nil
}

func maskBytes(key [4]byte, b []byte) {
	for i := range b {
		b[i] ^= key[i&3]
	}
}

// ReadMessage returns the next data message (OpText or OpBinary),
// reassembling fragments and answering control frames along the way:
// pings get a pong, a close is echoed and reported as *CloseError.
func (c *Conn) ReadMessage() (op int, data []byte, err error) {
	limit := c.MaxMessage
	if limit <= 0 {
		limit = DefaultMaxMessage
	}
	var msgOp byte
	var buf []byte
	inMessage := false
	for {
		f, err := c.readFrame(limit - len(buf))
		if err != nil {
			var pe *ProtocolError
			switch {
			case errors.As(err, &pe):
				_ = c.WriteClose(CloseProtocolError, pe.Msg)
			case errors.Is(err, ErrTooBig):
				_ = c.WriteClose(CloseTooBig, "message too big")
			}
			return 0, nil, err
		}
		switch f.opcode {
		case OpPing:
			if err := c.write(OpPong, f.payload); err != nil {
				return 0, nil, err
			}
			if c.OnPing != nil {
				c.OnPing(f.payload)
			}
			continue
		case OpPong:
			if c.OnPong != nil {
				c.OnPong(f.payload)
			}
			continue
		case OpClose:
			ce := &CloseError{Code: CloseNoStatus}
			if len(f.payload) >= 2 {
				ce.Code = int(binary.BigEndian.Uint16(f.payload))
				ce.Reason = string(f.payload[2:])
			}
			// Echo the close (§5.5.1), then the server closes TCP.
			_ = c.WriteClose(ce.Code, "")
			return 0, nil, ce
		case OpText, OpBinary:
			if inMessage {
				return 0, nil, c.fail("new data frame before the previous message finished")
			}
			msgOp, inMessage = f.opcode, true
			buf = append(buf, f.payload...)
		case OpContinuation:
			if !inMessage {
				return 0, nil, c.fail("continuation frame without a message")
			}
			buf = append(buf, f.payload...)
		default:
			return 0, nil, c.fail(fmt.Sprintf("unknown opcode %#x", f.opcode))
		}
		if f.fin {
			return int(msgOp), buf, nil
		}
	}
}

func (c *Conn) fail(msg string) error {
	_ = c.WriteClose(CloseProtocolError, msg)
	return &ProtocolError{msg}
}

// WriteMessage sends one unfragmented data message.
func (c *Conn) WriteMessage(op int, data []byte) error {
	if op != OpText && op != OpBinary {
		return errors.New("ws: WriteMessage needs OpText or OpBinary")
	}
	return c.write(byte(op), data)
}

// Ping sends a ping control frame.
func (c *Conn) Ping(data []byte) error { return c.write(OpPing, data) }

// WriteClose sends a close frame (once). It does not close the TCP
// connection; call Close for that.
func (c *Conn) WriteClose(code int, reason string) error {
	if len(reason) > 123 {
		reason = reason[:123]
	}
	var p []byte
	if code != CloseNoStatus {
		p = make([]byte, 2+len(reason))
		binary.BigEndian.PutUint16(p, uint16(code)) //nolint:gosec // close codes are < 5000
		copy(p[2:], reason)
	}
	return c.write(OpClose, p)
}

// writeTimeout bounds every write. A stalled peer (e.g. a
// laptop that went to sleep mid-connection) must not block broadcasts.
const writeTimeout = 10 * time.Second

func (c *Conn) write(op byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closeSent {
		return ErrClosed
	}
	if op == OpClose {
		c.closeSent = true
	}
	var hdr [14]byte
	hdr[0] = 0x80 | op
	n := 2
	l := len(payload)
	switch {
	case l <= 125:
		hdr[1] = byte(l)
	case l <= 0xFFFF:
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:], uint16(l))
		n = 4
	default:
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:], uint64(l))
		n = 10
	}
	body := payload
	if c.isClient {
		hdr[1] |= 0x80
		var key [4]byte
		if _, err := rand.Read(key[:]); err != nil {
			return err
		}
		copy(hdr[n:], key[:])
		n += 4
		body = append([]byte(nil), payload...)
		maskBytes(key, body)
	}
	_ = c.nc.SetWriteDeadline(time.Now().Add(writeTimeout))
	if _, err := c.nc.Write(append(hdr[:n:n], body...)); err != nil {
		return err
	}
	return nil
}

// Close sends a normal close frame (if none was sent yet) and closes the
// TCP connection.
func (c *Conn) Close() error {
	_ = c.WriteClose(CloseNormal, "")
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.nc.Close()
}

func headerHasToken(h []string, token string) bool {
	for _, v := range h {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}
