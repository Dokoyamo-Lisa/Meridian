// Package ws is the part of WebSocket (RFC 6455) the agent and the panel use between themselves: one
// lasting connection that carries whole binary messages, pings and a clean close - no extensions, no
// subprotocols. Both ends are Meridian, so it is strict about everything else; proxies in between
// (Caddy, nginx, Cloudflare) pass WebSockets on, splitting messages at most, which it joins again.
package ws

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	opContinue = 0x0
	opText     = 0x1
	opBinary   = 0x2
	opClose    = 0x8
	opPing     = 0x9
	opPong     = 0xa
)

// keyGUID is what RFC 6455 has the server append to the client's key before hashing it.
const keyGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// writeWait bounds one message on its way out: a peer that stops reading ends the connection.
const writeWait = 2 * time.Minute

var (
	// ErrRefused: the server answered the upgrade with something else than a WebSocket.
	ErrRefused = errors.New("the server did not switch to a WebSocket")
	// ErrTooLarge: a message is larger than the connection accepts.
	ErrTooLarge = errors.New("websocket: message too large")
	errProtocol = errors.New("websocket: protocol error")
)

// Conn is one WebSocket. Read is for one goroutine; Write, Ping and Close may be called from any.
type Conn struct {
	nc     net.Conn
	br     *bufio.Reader
	client bool  // a client masks everything it sends; a server takes nothing unmasked
	max    int64 // the largest message accepted
	idle   time.Duration

	wmu    sync.Mutex
	closed bool // a close frame went out (under wmu)
}

// acceptKey is the server's answer to a client's key.
func acceptKey(key string) string {
	h := sha1.Sum([]byte(key + keyGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

// hasToken says whether a comma-separated header holds a token, ignoring case.
func hasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

// Accept switches an HTTP request to a WebSocket (server side) and takes its connection over. A
// request that is not a WebSocket gets its answer here (400, or 426 for another version) and an error.
func Accept(w http.ResponseWriter, r *http.Request, max int64) (*Conn, error) {
	if r.Method != http.MethodGet || !hasToken(r.Header, "Connection", "upgrade") || !hasToken(r.Header, "Upgrade", "websocket") {
		http.Error(w, "this address takes WebSocket connections only", http.StatusBadRequest)
		return nil, errors.New("not a WebSocket request")
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		w.Header().Set("Sec-WebSocket-Version", "13")
		http.Error(w, "unsupported WebSocket version", http.StatusUpgradeRequired)
		return nil, errors.New("unsupported WebSocket version")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if k, err := base64.StdEncoding.DecodeString(key); err != nil || len(k) != 16 {
		http.Error(w, "bad WebSocket key", http.StatusBadRequest)
		return nil, errors.New("bad WebSocket key")
	}
	nc, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, "this connection cannot become a WebSocket", http.StatusInternalServerError)
		return nil, err
	}
	_ = nc.SetDeadline(time.Time{}) // the HTTP server's limits are for requests, not for this
	_ = nc.SetWriteDeadline(time.Now().Add(writeWait))
	if _, err := nc.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + acceptKey(key) + "\r\n\r\n")); err != nil {
		nc.Close()
		return nil, err
	}
	return &Conn{nc: nc, br: rw.Reader, max: max}, nil
}

// Client starts a WebSocket on nc, an open connection to the server (TLS done, when there is any): it
// sends req - a GET with the caller's headers - asking to switch, and reads the answer. When the
// server answers anything else, that answer comes back (its body read, up to 4 KB) with ErrRefused.
func Client(nc net.Conn, req *http.Request, max int64) (*Conn, *http.Response, error) {
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return nil, nil, err
	}
	k := base64.StdEncoding.EncodeToString(key)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", k)
	req.Header.Set("Sec-WebSocket-Version", "13")
	if err := req.Write(nc); err != nil {
		return nil, nil, err
	}
	br := bufio.NewReader(nc)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil, resp, ErrRefused
	}
	if !hasToken(resp.Header, "Upgrade", "websocket") || !hasToken(resp.Header, "Connection", "upgrade") ||
		resp.Header.Get("Sec-WebSocket-Accept") != acceptKey(k) {
		return nil, resp, errors.New("the server's answer is not a WebSocket")
	}
	return &Conn{nc: nc, br: br, client: true, max: max}, resp, nil
}

// SetIdle has Read end with a timeout when nothing at all - not even a ping or a pong - arrives for
// d (0: wait for ever).
func (c *Conn) SetIdle(d time.Duration) { c.idle = d }

// RemoteAddr is the other end of the connection.
func (c *Conn) RemoteAddr() net.Addr { return c.nc.RemoteAddr() }

// Read returns the next message, answering pings on the way. The other side closing ends it with
// io.EOF; anything that breaks the protocol ends the connection.
func (c *Conn) Read() ([]byte, error) {
	var msg []byte
	started := false
	for {
		fin, op, payload, err := c.frame()
		if err != nil {
			return nil, err
		}
		switch op {
		case opPing:
			_ = c.send(opPong, payload) // a connection that broke shows on the next read
			continue
		case opPong:
			continue
		case opClose:
			code := payload
			if len(code) > 2 {
				code = code[:2] // the status, without the reason
			}
			_ = c.send(opClose, code)
			c.nc.Close()
			return nil, io.EOF
		case opBinary: // Meridian sends binary messages only; a text frame is not one of ours
			if started {
				return nil, c.fail(errProtocol)
			}
			started, msg = true, payload
		case opContinue:
			if !started {
				return nil, c.fail(errProtocol)
			}
			if int64(len(msg))+int64(len(payload)) > c.max {
				return nil, c.fail(ErrTooLarge)
			}
			msg = append(msg, payload...)
		default:
			return nil, c.fail(errProtocol)
		}
		if fin {
			return msg, nil
		}
	}
}

// frame reads one frame.
func (c *Conn) frame() (fin bool, op byte, payload []byte, err error) {
	if c.idle > 0 {
		_ = c.nc.SetReadDeadline(time.Now().Add(c.idle))
	}
	var h [2]byte
	if _, err = io.ReadFull(c.br, h[:]); err != nil {
		return
	}
	fin, op = h[0]&0x80 != 0, h[0]&0x0f
	masked := h[1]&0x80 != 0
	if h[0]&0x70 != 0 || masked == c.client { // no extensions were agreed; clients mask, servers do not
		err = c.fail(errProtocol)
		return
	}
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err = io.ReadFull(c.br, b[:]); err != nil {
			return
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err = io.ReadFull(c.br, b[:]); err != nil {
			return
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if op >= opClose && (n > 125 || !fin) {
		err = c.fail(errProtocol)
		return
	}
	if n > uint64(c.max) {
		err = c.fail(ErrTooLarge)
		return
	}
	var key [4]byte
	if masked {
		if _, err = io.ReadFull(c.br, key[:]); err != nil {
			return
		}
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return
	}
	if masked {
		mask(payload, key)
	}
	return
}

// Write sends one binary message.
func (c *Conn) Write(msg []byte) error { return c.send(opBinary, msg) }

// Ping asks the other side for a pong (Read answers pings by itself).
func (c *Conn) Ping() error { return c.send(opPing, nil) }

// Close says goodbye, as far as that goes at once, and closes the connection.
func (c *Conn) Close() error {
	if c.wmu.TryLock() { // a write that hangs must not hold up the close
		if !c.closed {
			c.closed = true
			_ = c.nc.SetWriteDeadline(time.Now().Add(time.Second))
			_, _ = c.nc.Write(c.frameBytes(opClose, []byte{0x03, 0xe8})) // 1000: normal closure
		}
		c.wmu.Unlock()
	}
	return c.nc.Close()
}

// fail ends the connection after a protocol error, telling the other side why (1002) when it can.
func (c *Conn) fail(err error) error {
	code := []byte{0x03, 0xea} // 1002: protocol error
	if errors.Is(err, ErrTooLarge) {
		code = []byte{0x03, 0xf1} // 1009: message too big
	}
	_ = c.send(opClose, code)
	c.nc.Close()
	return err
}

func (c *Conn) send(op byte, payload []byte) error {
	b := c.frameBytes(op, payload)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	if op == opClose {
		c.closed = true
	}
	_ = c.nc.SetWriteDeadline(time.Now().Add(writeWait))
	_, err := c.nc.Write(b)
	return err
}

// frameBytes is one whole frame, masked when this is the client.
func (c *Conn) frameBytes(op byte, payload []byte) []byte {
	n := len(payload)
	b := make([]byte, 0, 14+n)
	b = append(b, 0x80|op)
	m := byte(0)
	if c.client {
		m = 0x80
	}
	switch {
	case n < 126:
		b = append(b, m|byte(n))
	case n <= 0xffff:
		b = append(b, m|126)
		b = binary.BigEndian.AppendUint16(b, uint16(n))
	default:
		b = append(b, m|127)
		b = binary.BigEndian.AppendUint64(b, uint64(n))
	}
	if !c.client {
		return append(b, payload...)
	}
	var key [4]byte
	_, _ = rand.Read(key[:])
	b = append(b, key[:]...)
	start := len(b)
	b = append(b, payload...)
	mask(b[start:], key)
	return b
}

func mask(b []byte, key [4]byte) {
	for i := range b {
		b[i] ^= key[i&3]
	}
}
