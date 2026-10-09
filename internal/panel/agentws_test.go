package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"meridian/internal/proto"
	"meridian/internal/seal"
	"meridian/internal/ws"
)

// wsAgent is an agent's side of the WebSocket, as plain as it gets.
type wsAgent struct {
	t    *testing.T
	id   int64
	keys *seal.Keys
	conn *ws.Conn
	next uint32
}

func signed(r *http.Request, id int64, keys *seal.Keys, body []byte) {
	ts, nonce := time.Now().Unix(), seal.Nonce()
	r.Header.Set(seal.HeaderServer, strconv.FormatInt(id, 10))
	r.Header.Set(seal.HeaderTime, strconv.FormatInt(ts, 10))
	r.Header.Set(seal.HeaderNonce, nonce)
	r.Header.Set(seal.HeaderSign, keys.Sign(r.Method, r.URL.RequestURI(), ts, nonce, body))
}

func openAgentWS(t *testing.T, h *harness, id int64, secret string, sign bool) (*wsAgent, *http.Response, error) {
	t.Helper()
	keys, err := seal.Derive(secret)
	if err != nil {
		t.Fatal(err)
	}
	nc, err := net.Dial("tcp", h.srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nc.Close() })
	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+proto.WSPath, nil)
	if sign {
		signed(req, id, keys, nil)
	}
	conn, resp, err := ws.Client(nc, req, 64<<20)
	return &wsAgent{t: t, id: id, keys: keys, conn: conn}, resp, err
}

// send sends one request; head can be changed by edit before it goes (a replay, another server).
func (a *wsAgent) send(method, path string, plain []byte, edit func(*proto.WSRequest)) proto.WSRequest {
	a.t.Helper()
	var body []byte
	if plain != nil {
		body = a.keys.Seal(path, plain)
	}
	a.next++
	head := proto.WSRequest{ID: a.next, Method: method, Path: path, Time: time.Now().Unix(), Nonce: seal.Nonce()}
	head.Sign = a.keys.Sign(method, path, head.Time, head.Nonce, body)
	if body != nil {
		head.Type = seal.ContentType
	}
	if edit != nil {
		edit(&head)
	}
	msg, err := proto.WSPack(head, body)
	if err != nil {
		a.t.Fatal(err)
	}
	if err := a.conn.Write(msg); err != nil {
		a.t.Fatal(err)
	}
	return head
}

// answer reads the next answer and opens its body.
func (a *wsAgent) answer(sent map[uint32]proto.WSRequest) (proto.WSAnswer, []byte) {
	a.t.Helper()
	msg, err := a.conn.Read()
	if err != nil {
		a.t.Fatal(err)
	}
	var ans proto.WSAnswer
	body, err := proto.WSUnpack(msg, &ans)
	if err != nil {
		a.t.Fatal(err)
	}
	if ans.Status == http.StatusOK && ans.Type == seal.ContentType {
		req := sent[ans.ID]
		plain, err := a.keys.Open(seal.ReplyContext(req.Path, req.Nonce), body)
		if err != nil {
			a.t.Fatalf("answer %d: %v", ans.ID, err)
		}
		return ans, plain
	}
	return ans, body
}

// TestAgentWebSocket: an agent's WebSocket carries the same signed requests as HTTP - the state
// (a long poll that wakes on a change while a report passes), reports, and nothing else - each checked
// like over HTTP: the connection's server, its signature, never twice.
func TestAgentWebSocket(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := newServer(t, b, "Tokyo")
	other := newServer(t, b, "Osaka")
	srv, _ := h.p.serverByID(context.Background(), sid)

	// the upgrade itself must be signed
	if _, resp, err := openAgentWS(t, h, sid, srv.Secret, false); !errors.Is(err, ws.ErrRefused) || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an unsigned upgrade: %v %v", err, resp)
	}
	a, _, err := openAgentWS(t, h, sid, srv.Secret, true)
	if err != nil {
		t.Fatal(err)
	}
	defer a.conn.Close()
	sent := map[uint32]proto.WSRequest{}

	r := a.send("GET", "/agent/v1/state?rev=&wait=0", nil, nil)
	sent[r.ID] = r
	ans, plain := a.answer(sent)
	var st proto.State
	if ans.Status != 200 || json.Unmarshal(plain, &st) != nil || st.ServerID != sid || st.Rev == "" {
		t.Fatalf("state: %d %s", ans.Status, plain)
	}

	// a long poll waits on the connection while a report goes through; a change wakes it
	r = a.send("GET", "/agent/v1/state?rev="+st.Rev+"&wait=50", nil, nil)
	sent[r.ID] = r
	rep, _ := json.Marshal(proto.Report{Instance: "ws-test", Batch: &proto.Batch{Seq: 1, From: now(), To: now()}})
	r = a.send("POST", "/agent/v1/report", rep, nil)
	sent[r.ID] = r
	ans, plain = a.answer(sent)
	if ans.ID != r.ID || ans.Status != 200 || !strings.Contains(string(plain), `"ack":1`) {
		t.Fatalf("report beside the long poll: %+v %s", ans, plain)
	}
	if v := serverJSON(b, sid); v["online"] != true {
		t.Errorf("the report was not taken: online %v", v["online"])
	}
	b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": "vless", "port": 443}, 201)
	if err := h.p.recompile(context.Background(), sid); err != nil {
		t.Fatal(err)
	}
	ans, plain = a.answer(sent)
	var st2 proto.State
	if ans.Status != 200 || json.Unmarshal(plain, &st2) != nil || st2.Rev == st.Rev {
		t.Fatalf("the long poll after a change: %d %s", ans.Status, plain)
	}

	// what is not served on it, a replay, another server's request, a path that is not what was signed
	r = a.send("GET", "/api/servers", nil, nil)
	sent[r.ID] = r
	if ans, _ := a.answer(sent); ans.Status != http.StatusNotFound {
		t.Errorf("another path: %d", ans.Status)
	}
	first := a.send("GET", "/agent/v1/state?rev=&wait=0", nil, nil)
	sent[first.ID] = first
	a.answer(sent)
	r = a.send("GET", "/agent/v1/state?rev=&wait=0", nil, func(h *proto.WSRequest) { h.Nonce, h.Sign, h.Time = first.Nonce, first.Sign, first.Time })
	sent[r.ID] = r
	if ans, body := a.answer(sent); ans.Status != http.StatusUnauthorized || !strings.Contains(string(body), "replayed") {
		t.Errorf("a replay: %d %s", ans.Status, body)
	}
	osaka, _ := h.p.serverByID(context.Background(), other)
	okeys, _ := seal.Derive(osaka.Secret)
	r = a.send("GET", "/agent/v1/state?rev=&wait=0", nil, func(h *proto.WSRequest) {
		h.Sign = okeys.Sign(h.Method, h.Path, h.Time, h.Nonce, nil) // Osaka's request on Tokyo's connection
	})
	sent[r.ID] = r
	if ans, _ := a.answer(sent); ans.Status != http.StatusUnauthorized {
		t.Errorf("another server's request: %d", ans.Status)
	}
	r = a.send("GET", "/agent/v1/state?rev=&wait=0", nil, func(h *proto.WSRequest) { h.Path = "/agent/v1/state?rev=&wait=0#x" })
	sent[r.ID] = r
	if ans, _ := a.answer(sent); ans.Status != http.StatusBadRequest {
		t.Errorf("a path other than signed: %d", ans.Status)
	}

	// a broken message ends the connection
	if err := a.conn.Write([]byte{0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.conn.Read(); err == nil || !(errors.Is(err, io.EOF) || strings.Contains(err.Error(), "closed") || strings.Contains(err.Error(), "reset")) {
		t.Errorf("after a broken message: %v", err)
	}
}

// TestAgentTransportSetting: Settings choose the WebSocket (the default: nothing in the state) or HTTP
// requests (the state says so), also through the MCP tool; what an agent says it uses shows on its
// server.
func TestAgentTransportSetting(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := newServer(t, b, "Tokyo")
	if s := b.must("GET", "/api/settings", nil, 200); s["agent_transport"] != "ws" {
		t.Errorf("the default: %v", s["agent_transport"])
	}
	if st := stateOf(t, h, sid); st.Agent.Transport != "" {
		t.Errorf("the default in the state: %q", st.Agent.Transport)
	}
	b.must("PUT", "/api/settings", map[string]any{"agent_transport": "http"}, 200)
	if st := stateOf(t, h, sid); st.Agent.Transport != "http" {
		t.Errorf("HTTP only: %q", st.Agent.Transport)
	}
	full := b.must("POST", "/api/tokens", map[string]any{"name": "rw", "scope": "full"}, 201)["token"].(string)
	if out, isErr := callTool(t, h, full, "set_agent_connection", map[string]any{"transport": "ws"}); isErr || !strings.Contains(out, `"agent_transport":"ws"`) {
		t.Errorf("set_agent_connection: %s", out)
	}
	if st := stateOf(t, h, sid); st.Agent.Transport != "" {
		t.Errorf("back to the WebSocket: %q", st.Agent.Transport)
	}
	if out, isErr := callTool(t, h, full, "set_agent_connection", map[string]any{"transport": "carrier pigeon"}); !isErr {
		t.Errorf("a made-up transport: %s", out)
	}
	if out, isErr := callTool(t, h, full, "set_agent_connection", map[string]any{"auto_relay_server_id": 99999}); !isErr ||
		!strings.Contains(out, "your servers") {
		t.Errorf("an unknown automatic relay: %s", out)
	}

	connectAgent(t, h, sid, "198.51.100.10", true)
	srv, _ := h.p.serverByID(context.Background(), sid)
	lv := &proto.Live{PanelPath: "direct", PanelConn: "http", ConnError: "the panel's address answered 400 instead of opening a WebSocket\n"}
	if _, err := h.p.ingest(context.Background(), srv, &proto.Report{Instance: "i", Live: lv}); err != nil {
		t.Fatal(err)
	}
	if v := serverJSON(b, sid); v["panel_conn"] != "http" || v["conn_error"] != "the panel's address answered 400 instead of opening a WebSocket" {
		t.Errorf("what the agent uses: %v %q", v["panel_conn"], v["conn_error"])
	}
	if _, err := h.p.ingest(context.Background(), srv, &proto.Report{Instance: "i", Live: &proto.Live{PanelConn: "carrier pigeon"}}); err != nil {
		t.Fatal(err)
	}
	if v := serverJSON(b, sid); v["panel_conn"] != nil {
		t.Errorf("a made-up connection: %v", v["panel_conn"])
	}
}
