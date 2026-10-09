package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"meridian/internal/proto"
	"meridian/internal/seal"
)

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestWebSocketByDefault: requests go on one WebSocket - a report passes while the state's long poll
// waits on the same connection - and the report says so.
func TestWebSocketByDefault(t *testing.T) {
	secret := seal.NewSecret()
	f := newFakePanel(t, secret)
	c := testClient(t, f, secret, dialTo(f.Listener.Addr().String()))
	hold := make(chan struct{})
	f.hold.Store(&hold)
	polled := make(chan error, 1)
	go func() {
		_, err := c.State(context.Background(), "abc", 25) // waits until the panel lets go
		polled <- err
	}()
	waitFor(t, "the long poll", func() bool { _, onWS, _ := f.counts(); return onWS == 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if ack, err := c.Report(ctx, &proto.Report{Instance: "i"}); err != nil || ack != 1 {
		t.Fatalf("a report beside the long poll: %d %v", ack, err)
	}
	close(hold)
	if err := <-polled; err != nil {
		t.Fatalf("the long poll: %v", err)
	}
	if httpReqs, onWS, opened := f.counts(); httpReqs != 0 || onWS != 2 || opened != 1 {
		t.Errorf("HTTP requests %d, on the WebSocket %d, WebSockets %d", httpReqs, onWS, opened)
	}
	var lv proto.Live
	c.linkStatus(&lv)
	if lv.PanelConn != connWebSocket || lv.ConnError != "" || lv.PanelPath != pathDirect {
		t.Errorf("status: %+v", lv)
	}
	f.mu.Lock()
	host, sni := f.hosts[0], f.snis[0]
	f.mu.Unlock()
	if host != "example.com:"+f.port() || sni != "example.com" {
		t.Errorf("the WebSocket asked for %q, TLS name %q", host, sni)
	}
}

// TestWebSocketFallback: a panel that does not open a WebSocket gets HTTP requests (for a good while),
// and the report says why; a broken WebSocket is opened again without falling back; Settings can
// have HTTP only; a refused agent hears it as over HTTP.
func TestWebSocketFallback(t *testing.T) {
	secret := seal.NewSecret()
	f := newFakePanel(t, secret)
	f.noWS.Store(true)
	c := testClient(t, f, secret, dialTo(f.Listener.Addr().String()))
	if _, err := c.State(context.Background(), "", 0); err != nil {
		t.Fatalf("over HTTP: %v", err)
	}
	if httpReqs, onWS, _ := f.counts(); httpReqs != 1 || onWS != 0 {
		t.Errorf("HTTP %d, WebSocket %d", httpReqs, onWS)
	}
	var lv proto.Live
	c.linkStatus(&lv)
	if lv.PanelConn != connHTTP || !strings.Contains(lv.ConnError, "answered 404") {
		t.Errorf("status: %+v", lv)
	}
	if c.ws.wanted() {
		t.Error("the WebSocket is tried again at once")
	}
	c.ws.mu.Lock()
	if time.Until(c.ws.retryAt) < 50*time.Minute {
		t.Errorf("a panel without WebSockets is asked again in %v", time.Until(c.ws.retryAt))
	}
	c.ws.retryAt = time.Time{} // later: the panel takes WebSockets now
	c.ws.mu.Unlock()
	f.noWS.Store(false)
	if _, err := c.State(context.Background(), "", 0); err != nil {
		t.Fatal(err)
	}
	c.linkStatus(&lv)
	if lv.PanelConn != connWebSocket || lv.ConnError != "" {
		t.Errorf("status on the WebSocket: %+v", lv)
	}

	// the panel restarts: the next request opens a new WebSocket, no HTTP in between
	f.dropWebSockets()
	if _, err := c.State(context.Background(), "", 0); err != nil {
		t.Fatalf("after the WebSocket broke: %v", err)
	}
	if httpReqs, _, opened := f.counts(); httpReqs != 1 || opened != 2 {
		t.Errorf("after the WebSocket broke: HTTP %d, WebSockets %d", httpReqs, opened)
	}

	// Settings say HTTP: the WebSocket closes and requests go as HTTP requests
	c.follow(&proto.State{Agent: proto.AgentSettings{Transport: "http"}})
	if _, err := c.State(context.Background(), "", 0); err != nil {
		t.Fatal(err)
	}
	c.linkStatus(&lv)
	if httpReqs, _, opened := f.counts(); httpReqs != 2 || opened != 2 || lv.PanelConn != connHTTP || lv.ConnError != "" {
		t.Errorf("HTTP by Settings: HTTP %d, WebSockets %d, %+v", httpReqs, opened, lv)
	}
	c.follow(&proto.State{})
	if !c.ws.wanted() {
		t.Error("the WebSocket is not wanted again")
	}

	// an agent the panel refuses hears it, as over HTTP
	bad := testClient(t, f, seal.NewSecret(), dialTo(f.Listener.Addr().String()))
	_, err := bad.State(context.Background(), "", 0)
	var se *statusError
	if !errors.As(err, &se) || se.code != 401 {
		t.Errorf("a refused agent: %v", err)
	}
}

// TestWebSocketThroughRelay: with no direct route the WebSocket goes through the relay; one opened
// directly while the relay failed moves back to the relay once it is tried again.
func TestWebSocketThroughRelay(t *testing.T) {
	secret := seal.NewSecret()
	f := newFakePanel(t, secret)
	_, relayAddr := loopbackRelay(t, f.Listener.Addr().String(), "127.0.0.1")
	c := testClient(t, f, secret, refuseDial)
	c.link.setVia([]string{relayAddr})
	if _, err := c.State(context.Background(), "", 0); err != nil {
		t.Fatalf("through the relay: %v", err)
	}
	var lv proto.Live
	c.linkStatus(&lv)
	if lv.PanelPath != pathRelay || lv.PanelConn != connWebSocket {
		t.Errorf("status: %+v", lv)
	}

	// the relay failed a moment ago: the WebSocket opens directly...
	c2 := testClient(t, f, secret, dialTo(f.Listener.Addr().String()))
	c2.link.setVia([]string{relayAddr})
	c2.link.failed(pathRelay, errors.New("a hiccup"))
	if _, err := c2.State(context.Background(), "", 0); err != nil {
		t.Fatal(err)
	}
	if w := c2.ws.conn; w == nil || w.route != pathDirect {
		t.Fatalf("opened directly: %+v", w)
	}
	// ...and once the relay is tried again, a new one goes through it
	c2.link.mu.Lock()
	c2.link.retryAt = time.Now().Add(-time.Second)
	c2.link.mu.Unlock()
	if _, err := c2.State(context.Background(), "", 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the WebSocket through the relay", func() bool {
		c2.ws.mu.Lock()
		defer c2.ws.mu.Unlock()
		return c2.ws.conn != nil && c2.ws.conn.route == pathRelay
	})
	// another relay: connections opened before it go
	old := c2.ws.conn
	c2.link.setVia(nil)
	if _, err := c2.State(context.Background(), "", 0); err != nil {
		t.Fatal(err)
	}
	if c2.ws.conn == old || c2.ws.conn.route != pathDirect {
		t.Errorf("after the relay was dropped: %+v", c2.ws.conn)
	}
	waitFor(t, "the old connection to close", func() bool { return !old.alive() })
}
