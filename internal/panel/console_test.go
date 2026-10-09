package panel

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"meridian/internal/proto"
	"meridian/internal/seal"
	"meridian/internal/ws"
)

// wsDial opens a WebSocket to the harness with the given request headers.
func wsDial(t *testing.T, h *harness, path string, hdr http.Header) (*ws.Conn, *http.Response, error) {
	t.Helper()
	u, _ := url.Parse(h.srv.URL)
	nc, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", h.srv.URL+path, nil)
	for k, v := range hdr {
		req.Header[k] = v
	}
	c, resp, err := ws.Client(nc, req, 1<<20)
	if err != nil {
		nc.Close()
	}
	return c, resp, err
}

// TestConsole: only the supervisor's browser opens a console, after confirming the password; the
// agent dials back for the session; the browser's WebSocket must come from the panel's page with the
// ticket; what is typed and printed passes, nothing else; opening and closing are recorded.
func TestConsole(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.70"}, 201)["server"].(map[string]any)["id"])
	full := b.must("POST", "/api/tokens", map[string]any{"name": "rw", "scope": "full"}, 201)["token"].(string)
	if code, _, _ := h.bearer(full).do("POST", fmt.Sprintf("/api/servers/%d/console", sid), map[string]any{}); code != 403 {
		t.Errorf("an API token opened a console: %d", code)
	}
	if code, _, raw := b.do("POST", fmt.Sprintf("/api/servers/%d/console", sid), map[string]any{"password": "owner-password-1"}); code != 409 ||
		!strings.Contains(string(raw), "offline") {
		t.Errorf("an offline server: %d %s", code, raw)
	}
	srv, _ := h.p.serverByID(context.Background(), sid)
	h.p.ingest(context.Background(), srv, &proto.Report{Instance: "i", Hello: &proto.Hello{AgentVersion: Version, IPv4: "203.0.113.70",
		Caps: proto.Caps{Certs: true, Limits: true, Console: false}}})
	h.p.db.Exec1(`UPDATE servers SET online = 1 WHERE id = ?`, sid)
	if code, _, raw := b.do("POST", fmt.Sprintf("/api/servers/%d/console", sid), map[string]any{"password": "owner-password-1"}); code != 409 ||
		!strings.Contains(string(raw), "turned off") {
		t.Errorf("a server that turned it off: %d %s", code, raw)
	}
	srv, _ = h.p.serverByID(context.Background(), sid)
	h.p.ingest(context.Background(), srv, &proto.Report{Instance: "i", Hello: &proto.Hello{AgentVersion: Version, IPv4: "203.0.113.70",
		Caps: proto.Caps{Certs: true, Limits: true, Console: true}}})
	h.p.db.Exec1(`UPDATE servers SET online = 1 WHERE id = ?`, sid)

	if code, _, raw := b.do("POST", fmt.Sprintf("/api/servers/%d/console", sid), map[string]any{}); code != 428 || !strings.Contains(string(raw), "password") {
		t.Errorf("no password: %d %s", code, raw)
	}
	if code, _, _ := b.do("POST", fmt.Sprintf("/api/servers/%d/console", sid), map[string]any{"password": "wrong"}); code != 403 {
		t.Errorf("a wrong password: %d", code)
	}
	o := b.must("POST", fmt.Sprintf("/api/servers/%d/console", sid), map[string]any{"password": "owner-password-1", "cols": 100, "rows": 30}, 200)
	session, ticket, wsURL := o["session"].(string), o["ticket"].(string), o["url"].(string)
	// within half an hour the password is not asked again
	again := b.must("POST", fmt.Sprintf("/api/servers/%d/console", sid), map[string]any{}, 200)
	h.p.dropConsole(again["session"].(string))

	st := stateOf(t, h, sid)
	var act *proto.Action
	for i := range st.Actions {
		if st.Actions[i].Kind == proto.ActionConsole && strings.Contains(string(st.Actions[i].Args), session) {
			act = &st.Actions[i]
		}
	}
	if act == nil || !strings.Contains(string(act.Args), session) || !strings.Contains(string(act.Args), `"cols":100`) {
		t.Fatalf("the agent is not asked: %+v", st.Actions)
	}

	// the browser: the cookie of the signed-in session, the panel's own Origin
	cookies := b.http.Jar.Cookies(&url.URL{Scheme: "http", Host: strings.TrimPrefix(h.srv.URL, "http://")})
	var cookie string
	for _, c := range cookies {
		cookie += c.Name + "=" + c.Value + "; "
	}
	host := strings.TrimPrefix(h.srv.URL, "http://")
	if _, resp, err := wsDial(t, h, wsURL, http.Header{"Cookie": {cookie}, "Origin": {"https://evil.example"}}); err == nil || resp == nil || resp.StatusCode != 403 {
		t.Errorf("another site's page opened the console: %v", err)
	}
	browser, _, err := wsDial(t, h, wsURL, http.Header{"Cookie": {cookie}, "Origin": {"http://" + host}})
	if err != nil {
		t.Fatalf("browser: %v", err)
	}
	defer browser.Close()
	browser.Write([]byte(ticket))

	// the agent dials back, signed with its server's key
	keys, _ := seal.Derive(srv.Secret)
	path := proto.ConsolePath + session
	ts, nonce := time.Now().Unix(), seal.Nonce()
	agent, _, err := wsDial(t, h, path, http.Header{seal.HeaderServer: {strconv.FormatInt(sid, 10)}, seal.HeaderTime: {strconv.FormatInt(ts, 10)},
		seal.HeaderNonce: {nonce}, seal.HeaderSign: {keys.Sign("GET", path, ts, nonce, nil)}})
	if err != nil {
		t.Fatalf("agent: %v", err)
	}
	defer agent.Close()
	if msg, err := browser.Read(); err != nil || len(msg) != 1 || msg[0] != proto.ConsoleReady {
		t.Fatalf("ready: %v %v", msg, err)
	}
	browser.Write(append([]byte{proto.ConsoleData}, "uptime\n"...))
	if msg, err := agent.Read(); err != nil || string(msg[1:]) != "uptime\n" || msg[0] != proto.ConsoleData {
		t.Errorf("typed: %q %v", msg, err)
	}
	agent.Write(append([]byte{proto.ConsoleData}, " 10:00 up 3 days\n"...))
	if msg, err := browser.Read(); err != nil || !strings.Contains(string(msg), "up 3 days") {
		t.Errorf("printed: %q %v", msg, err)
	}
	browser.Write([]byte{0x07, 'x'}) // not typing, not a size: the console ends
	if _, err := agent.Read(); err == nil {
		t.Error("a stray message reached the server")
	}
	for i := 0; i < 50; i++ {
		var n int
		h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind = 'console_closed'`).Scan(&n)
		if n == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var opened, closed int
	h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind = 'console_opened' AND message LIKE '%Tokyo%'`).Scan(&opened)
	h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind = 'console_closed'`).Scan(&closed)
	if opened != 1 || closed != 1 {
		t.Errorf("recorded: opened %d, closed %d", opened, closed)
	}

	// the same session cannot be joined twice; a ticket works once
	if _, resp, err := wsDial(t, h, wsURL, http.Header{"Cookie": {cookie}, "Origin": {"http://" + host}}); err == nil || resp == nil || resp.StatusCode != 404 {
		t.Errorf("a used session: %v", err)
	}
}
