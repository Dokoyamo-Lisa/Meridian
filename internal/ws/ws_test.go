package ws

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// server runs handle on every WebSocket it accepts (max bounds a message).
func server(t *testing.T, max int64, handle func(c *Conn)) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/refuse" {
			http.Error(w, "not you", http.StatusUnauthorized)
			return
		}
		c, err := Accept(w, r, max)
		if err != nil {
			return
		}
		defer c.Close()
		handle(c)
	}))
	t.Cleanup(s.Close)
	return s
}

func echo(c *Conn) {
	for {
		msg, err := c.Read()
		if err != nil {
			return
		}
		if c.Write(msg) != nil {
			return
		}
	}
}

func dial(t *testing.T, s *httptest.Server, path string) (*Conn, *http.Response, error) {
	t.Helper()
	nc, err := net.Dial("tcp", s.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nc.Close() })
	req, _ := http.NewRequest(http.MethodGet, s.URL+path, nil)
	req.Header.Set("User-Agent", "test")
	return Client(nc, req, 8<<20)
}

func TestRoundTrip(t *testing.T) {
	s := server(t, 8<<20, echo)
	c, resp, err := dial(t, s, "/")
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake: %v %v", err, resp)
	}
	for _, n := range []int{0, 1, 125, 126, 65535, 65536, 3 << 20} {
		msg := bytes.Repeat([]byte{byte(n)}, n)
		if err := c.Write(msg); err != nil {
			t.Fatal(err)
		}
		got, err := c.Read()
		if err != nil || !bytes.Equal(got, msg) {
			t.Fatalf("%d bytes: %v (got %d)", n, err, len(got))
		}
	}
	if err := c.Ping(); err != nil {
		t.Fatal(err)
	}
	if err := c.Write([]byte("after the ping")); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Read(); err != nil || string(got) != "after the ping" {
		t.Fatalf("after a ping: %q %v", got, err)
	}
	c.Close()
}

// TestRefusedAndNotWebSocket: a server that answers otherwise comes back as its answer; a request
// that is no WebSocket is refused.
func TestRefusedAndNotWebSocket(t *testing.T) {
	s := server(t, 1<<20, echo)
	_, resp, err := dial(t, s, "/refuse")
	if !errors.Is(err, ErrRefused) || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("refused: %v %+v", err, resp)
	}
	if b, _ := io.ReadAll(resp.Body); !strings.Contains(string(b), "not you") {
		t.Errorf("the refusal's body: %q", b)
	}
	r, err := http.Get(s.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Errorf("a plain GET: %d", r.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, s.URL+"/", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "8")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	r, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusUpgradeRequired {
		t.Errorf("another version: %d", r.StatusCode)
	}
}

// raw is a frame as a client writes it, with the FIN bit and masking chosen by the test.
func raw(fin bool, op byte, payload []byte, masked bool) []byte {
	b := []byte{op}
	if fin {
		b[0] |= 0x80
	}
	m := byte(0)
	if masked {
		m = 0x80
	}
	switch n := len(payload); {
	case n < 126:
		b = append(b, m|byte(n))
	default:
		b = append(b, m|126)
		b = binary.BigEndian.AppendUint16(b, uint16(n))
	}
	if !masked {
		return append(b, payload...)
	}
	key := [4]byte{1, 2, 3, 4}
	b = append(b, key[:]...)
	p := append([]byte(nil), payload...)
	mask(p, key)
	return append(b, p...)
}

// TestFragmentsPingsAndLimits: a message split in frames with a ping in between arrives whole; an
// unmasked frame from a client, a text frame, or a message over the limit ends the connection.
func TestFragmentsPingsAndLimits(t *testing.T) {
	got := make(chan []byte, 4)
	errs := make(chan error, 4)
	s := server(t, 1024, func(c *Conn) {
		for {
			msg, err := c.Read()
			if err != nil {
				errs <- err
				return
			}
			got <- msg
		}
	})
	c, _, err := dial(t, s, "/")
	if err != nil {
		t.Fatal(err)
	}
	frames := append(append(append(raw(false, opBinary, []byte("hello, "), true), raw(true, opPing, []byte("p"), true)...),
		raw(false, opContinue, []byte("split "), true)...), raw(true, opContinue, []byte("world"), true)...)
	if _, err := c.nc.Write(frames); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		if string(m) != "hello, split world" {
			t.Errorf("joined: %q", m)
		}
	case err := <-errs:
		t.Fatalf("fragments: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("no message")
	}
	// the pong for the ping came back (the client's Read skips it, then sees the close below)
	if _, err := c.nc.Write(raw(true, opBinary, bytes.Repeat([]byte("x"), 2000), true)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errs:
		if !errors.Is(err, ErrTooLarge) {
			t.Errorf("too large: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a message over the limit was taken")
	}
	if _, err := c.Read(); err == nil {
		t.Error("the connection stayed open after a message over the limit")
	}

	c2, _, err := dial(t, s, "/")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c2.nc.Write(raw(true, opBinary, []byte("unmasked"), false)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errs:
		if !errors.Is(err, errProtocol) {
			t.Errorf("unmasked: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("an unmasked frame from a client was taken")
	}

	c3, _, err := dial(t, s, "/") // Meridian sends binary messages only
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c3.nc.Write(raw(true, opText, []byte("text"), true)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errs:
		if !errors.Is(err, errProtocol) {
			t.Errorf("text frame: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a text frame was taken")
	}
}

// TestCloseAndIdle: a close from the other side ends Read with io.EOF; a quiet connection ends after
// its idle time, while pings keep it open.
func TestCloseAndIdle(t *testing.T) {
	errs := make(chan error, 2)
	s := server(t, 1024, func(c *Conn) {
		c.SetIdle(300 * time.Millisecond)
		for {
			if _, err := c.Read(); err != nil {
				errs <- err
				return
			}
		}
	})
	c, _, err := dial(t, s, "/")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err := <-errs; err != io.EOF {
		t.Errorf("closed by the client: %v", err)
	}
	if err := c.Write([]byte("x")); err == nil {
		t.Error("a write after Close went out")
	}

	c2, _, err := dial(t, s, "/")
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	start := time.Now()
	for range 4 { // pings keep it open beyond its idle time
		time.Sleep(150 * time.Millisecond)
		if err := c2.Ping(); err != nil {
			t.Fatal(err)
		}
	}
	err = <-errs
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() || time.Since(start) < 700*time.Millisecond || time.Since(start) > 3*time.Second {
		t.Errorf("a quiet connection: %v after %v", err, time.Since(start))
	}
}
