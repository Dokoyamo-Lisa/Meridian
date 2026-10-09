package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"meridian/internal/agent/hy"
	"meridian/internal/proto"
	"meridian/internal/seal"
)

// freeLoopbackPort is a TCP port nothing listens on right now.
func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// loopbackRelay is a relay on loopback that passes connections to target.
func loopbackRelay(t *testing.T, target string, allow ...string) (*relayServer, string) {
	t.Helper()
	r := newRelayServer(target)
	r.listenIP = "127.0.0.1"
	port := freeLoopbackPort(t)
	if err := r.Apply(&proto.Relay{Port: port, Allow: allow}, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Apply(nil, nil) })
	return r, "127.0.0.1:" + strconv.Itoa(port)
}

// testClient talks to the fake panel as https://example.com:<port> (the name its certificate holds);
// direct says how that name is dialled directly.
func testClient(t *testing.T, f *fakePanel, secret string, direct dialFunc) *Client {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(f.Certificate())
	c, err := newClient("https://example.com:"+f.port(), seal.Token(7, secret), "test", &tls.Config{RootCAs: pool}, direct)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func refuseDial(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("no route to the panel from here")
}

func dialTo(addr string) dialFunc {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
}

// TestPanelThroughRelay: with no direct route, the agent reaches the panel through the relay - TLS
// end to end under the panel's own name, the request signed and the answer sealed as always - and
// says so.
func TestPanelThroughRelay(t *testing.T) {
	secret := seal.NewSecret()
	f := newFakePanel(t, secret)
	_, relayAddr := loopbackRelay(t, f.Listener.Addr().String(), "127.0.0.1")
	c := testClient(t, f, secret, refuseDial)
	c.link.setVia([]string{relayAddr})
	st, err := c.State(context.Background(), "", 0)
	if err != nil || st == nil || st.Rev != "abc" {
		t.Fatalf("state through the relay: %v %+v", err, st)
	}
	f.mu.Lock()
	host, sni := f.hosts[0], f.snis[0]
	f.mu.Unlock()
	if host != "example.com:"+f.port() || sni != "example.com" {
		t.Errorf("the panel saw host %q, TLS name %q - the panel's own name must go through", host, sni)
	}
	if path, why := c.link.status(); path != pathRelay || why != "" {
		t.Errorf("status: %q %q", path, why)
	}
	// downloads from the panel go the same way; anything else goes out directly
	resp, err := (&http.Client{Transport: c.link}).Get("https://example.com:" + f.port() + "/agent/v1/state")
	if err != nil {
		t.Fatalf("download through the relay: %v", err)
	}
	resp.Body.Close()
	if _, err := (&http.Client{Transport: c.link}).Get("https://other.example:443/x"); err == nil {
		t.Error("another host was reached without a route")
	}
}

// TestPanelLinkFailover: a relay that cannot be reached sends the agent directly (and says why);
// directly first for a while, then the relay again; when the direct way fails, the relay is the way.
func TestPanelLinkFailover(t *testing.T) {
	secret := seal.NewSecret()
	f := newFakePanel(t, secret)
	c := testClient(t, f, secret, dialTo(f.Listener.Addr().String()))
	dead := "127.0.0.1:" + strconv.Itoa(freeLoopbackPort(t)) // nothing listens there
	c.link.setVia([]string{dead})
	if _, err := c.State(context.Background(), "", 0); err != nil {
		t.Fatalf("directly after the relay failed: %v", err)
	}
	path, why := c.link.status()
	if path != pathDirect || !strings.Contains(why, "refused") {
		t.Errorf("status after a refused relay: %q %q", path, why)
	}
	if r := c.link.routes(); r[0] != pathDirect {
		t.Errorf("right after the relay failed, directly comes first: %v", r)
	}
	c.link.mu.Lock()
	c.link.retryAt = time.Now().Add(-time.Second) // the pause is over
	c.link.mu.Unlock()
	if r := c.link.routes(); r[0] != pathRelay {
		t.Errorf("after the pause the relay comes first again: %v", r)
	}

	// the other way round: no direct route, the relay works
	c2 := testClient(t, f, secret, refuseDial)
	_, relayAddr := loopbackRelay(t, f.Listener.Addr().String(), "127.0.0.1")
	c2.link.setVia([]string{relayAddr})
	c2.link.failed(pathRelay, errors.New("a hiccup")) // directly first now...
	if _, err := c2.State(context.Background(), "", 0); err != nil {
		t.Fatalf("through the relay after the direct way failed: %v", err)
	}
	if path, why := c2.link.status(); path != pathRelay || why != "" {
		t.Errorf("status: %q %q", path, why)
	}

	// a relay that does not let this server in closes the connection at once
	c3 := testClient(t, f, secret, dialTo(f.Listener.Addr().String()))
	_, strict := loopbackRelay(t, f.Listener.Addr().String(), "192.0.2.1")
	c3.link.setVia([]string{strict})
	if _, err := c3.State(context.Background(), "", 0); err != nil {
		t.Fatalf("directly after the relay refused: %v", err)
	}
	if path, why := c3.link.status(); path != pathDirect || why == "" {
		t.Errorf("status after the relay closed the connection: %q %q", path, why)
	}

	// no way at all: the error names both
	c4 := testClient(t, f, secret, refuseDial)
	c4.link.setVia([]string{dead})
	if _, err := c4.State(context.Background(), "", 0); err == nil || !strings.Contains(err.Error(), "through the relay") ||
		!strings.Contains(err.Error(), "directly") {
		t.Errorf("both ways failed: %v", err)
	}
	// directly again: the relay is forgotten
	c4.link.setVia(nil)
	if r := c4.link.routes(); len(r) != 1 || r[0] != pathDirect {
		t.Errorf("without a relay: %v", r)
	}
}

func TestCleanVia(t *testing.T) {
	got := cleanVia([]string{"203.0.113.5:40000", "203.0.113.5:40000", "[2001:db8::1]:40000", "Relay.Example.com:1",
		"0.0.0.0:1", "203.0.113.6:0", "[fe80::1%eth0]:1", "::ffff:203.0.113.7:2", "[::ffff:203.0.113.7]:3", "x",
		"relay.example.com:0", "relay.example.com:70000", "-bad.example.com:1", "1.2.3:4", "a b.example:1", "relay.example.com"})
	want := []string{"203.0.113.5:40000", "[2001:db8::1]:40000", "relay.example.com:1", "203.0.113.7:3"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("cleanVia: %v, want %v", got, want)
	}
}

// echoServer stands in for the panel behind a relay: it sends back what it gets.
func echoServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				io.Copy(c, c)
			}()
		}
	}()
	return l.Addr().String()
}

// talks says whether a connection to the relay gets through: what it sends comes back.
func talks(t *testing.T, addr string) (net.Conn, bool) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, false
	}
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte("ping")); err != nil {
		c.Close()
		return nil, false
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		c.Close()
		return nil, false
	}
	c.SetDeadline(time.Time{})
	return c, true
}

// closed says whether the relay closed a connection (within a few seconds).
func closed(c net.Conn) bool {
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, err := c.Read(make([]byte, 1))
	var ne net.Error
	return err != nil && !(errors.As(err, &ne) && ne.Timeout())
}

// TestRelayAllowListAndLimits: the relay passes only the relayed servers' connections - anything
// else is closed at once -, at most so many at a time and from one address, closes quiet ones, and
// drops the connections of a server that is no longer relayed.
func TestRelayAllowListAndLimits(t *testing.T) {
	panel := echoServer(t)
	r, addr := loopbackRelay(t, panel, "127.0.0.1", "not an address", "2001:db8::7")
	if r.allow == nil || len(r.allow) != 2 {
		t.Errorf("allow list: %v", r.allow)
	}
	c1, ok := talks(t, addr)
	if !ok {
		t.Fatal("an allowed address does not get through")
	}
	defer c1.Close()

	// limits: at most two at a time here, and one per address
	r.mu.Lock()
	r.maxConns = 2
	r.mu.Unlock()
	c2, ok := talks(t, addr)
	if !ok {
		t.Fatal("a second connection does not get through")
	}
	if c3, err := net.Dial("tcp", addr); err == nil {
		if !closed(c3) {
			t.Error("a third connection was not closed at once")
		}
		c3.Close()
	}
	c2.Close()
	deadline := time.Now().Add(3 * time.Second)
	for r.open() > 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	r.mu.Lock()
	r.maxConns, r.perAddr = 64, 1
	r.mu.Unlock()
	if c4, err := net.Dial("tcp", addr); err == nil {
		if !closed(c4) {
			t.Error("a second connection from one address was not closed at once")
		}
		c4.Close()
	}
	r.mu.Lock()
	r.perAddr = relayPerAddr
	r.mu.Unlock()

	// a server no longer relayed loses its connections; strangers are closed at once
	port, _ := strconv.Atoi(addr[strings.LastIndex(addr, ":")+1:])
	if err := r.Apply(&proto.Relay{Port: port, Allow: []string{"192.0.2.1"}}, nil); err != nil {
		t.Fatal(err)
	}
	if !closed(c1) {
		t.Error("the connection of a server no longer relayed stayed open")
	}
	if c5, err := net.Dial("tcp", addr); err == nil {
		if !closed(c5) {
			t.Error("a stranger was not closed at once")
		}
		c5.Close()
	}

	// quiet connections end
	if err := r.Apply(&proto.Relay{Port: port, Allow: []string{"127.0.0.1"}}, nil); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.idle = 200 * time.Millisecond
	r.mu.Unlock()
	c6, ok := talks(t, addr)
	if !ok {
		t.Fatal("allowed again, but no way through")
	}
	start := time.Now()
	if !closed(c6) || time.Since(start) > 2*time.Second {
		t.Error("a quiet connection was not closed")
	}
	c6.Close()

	// a port this server uses otherwise is refused; no relay closes the listener
	if err := r.Apply(&proto.Relay{Port: port, Allow: []string{"127.0.0.1"}}, map[int]string{port: "a protocol"}); err == nil ||
		!strings.Contains(err.Error(), "a protocol") {
		t.Errorf("a taken port: %v", err)
	}
	if err := r.Apply(nil, nil); err != nil {
		t.Fatal(err)
	}
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		t.Error("the relay still listens after it was turned off")
	}
}

// TestRelayPortIsNeverFiltered: the relay's port is not among the ports the country rule and IP
// blocks filter, and the ports the server uses otherwise are never taken for it.
func TestRelayPortIsNeverFiltered(t *testing.T) {
	a := &Agent{cfg: &Config{APIPort: 50000}, hy: &hy.Engine{AuthPort: 50001}}
	st := &proto.State{
		Xray:     &proto.Xray{Inbounds: []proto.XrayInbound{{Tag: "n1", Config: []byte(`{"port":443,"protocol":"vless"}`)}}},
		Forwards: []proto.Forward{{ID: 1, ListenPort: 30000, Network: "tcp+udp", Target: "203.0.113.9:443"}},
		Relay:    &proto.Relay{Port: 40123, Allow: []string{"198.51.100.10"}},
	}
	spec := a.nftSpec(st)
	for _, p := range append(spec.TCPPorts, spec.UDPPorts...) {
		if p == 40123 {
			t.Errorf("the relay's port is filtered: %v %v", spec.TCPPorts, spec.UDPPorts)
		}
	}
	taken := a.relayTaken(st)
	for _, p := range []int{50000, 50001, 443, 30000} {
		if taken[p] == "" {
			t.Errorf("port %d is not taken: %v", p, taken)
		}
	}
}

// TestActionResultsSurviveRestart: an action's result is on disk until the panel has it, so an agent
// that restarts first (an upgrade does) sends it after the restart; the old file format still loads.
func TestActionResultsSurviveRestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "actions.json")
	a := &Agent{done: map[int64]bool{}, kick: make(chan struct{}, 1), actions: file}
	a.runActions(&proto.State{}, []proto.Action{{ID: 7, Kind: "nothing-like-this"}})
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, _ := os.ReadFile(file)
		if strings.Contains(string(b), `"results"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the result never reached the disk: %s", b)
		}
		time.Sleep(10 * time.Millisecond)
	}
	b := &Agent{done: map[int64]bool{}, actions: file}
	b.loadDone()
	if !b.done[7] || len(b.results) != 1 || b.results[0].ID != 7 || b.results[0].OK || !strings.Contains(b.results[0].Output, "unknown action") {
		t.Fatalf("after a restart: done %v, results %+v", b.done, b.results)
	}
	// started once, never twice - also after the restart
	b.kick = make(chan struct{}, 1)
	b.runActions(&proto.State{}, []proto.Action{{ID: 7, Kind: "nothing-like-this"}})
	time.Sleep(50 * time.Millisecond)
	b.mu.Lock()
	n := len(b.results)
	b.mu.Unlock()
	if n != 1 {
		t.Errorf("the action ran again: %d results", n)
	}
	// what agents before 1.0 wrote
	os.WriteFile(file, []byte("[1,2,3]"), 0o600)
	c := &Agent{done: map[int64]bool{}, actions: file}
	c.loadDone()
	if !c.done[1] || !c.done[3] || len(c.results) != 0 {
		t.Errorf("the old format: %v %v", c.done, c.results)
	}
}
