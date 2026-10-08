package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"

	"meridian/internal/proto"
	"meridian/internal/subgen"
)

// usersOn lists the subscriptions an inbound or Hysteria node accepts, by protocol.
func usersOn(t *testing.T, h *harness, serverID int64) map[int64][]int64 {
	t.Helper()
	st, err := h.p.compileServer(context.Background(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int64][]int64{}
	for _, in := range st.Xray.Inbounds {
		node, _ := proto.ParseInboundTag(in.Tag)
		out[node] = []int64{}
		for _, c := range in.Clients {
			if sub, _, ok := proto.ParseEmail(c.Email); ok {
				out[node] = append(out[node], sub)
			}
		}
	}
	for _, hn := range st.Hysteria {
		out[hn.NodeID] = []int64{}
		for _, u := range hn.Users {
			if sub, _, ok := proto.ParseEmail(u.ID); ok {
				out[hn.NodeID] = append(out[hn.NodeID], sub)
			}
		}
	}
	for _, v := range out {
		sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	}
	return out
}

// linkNames lists the proxies in a user's sing-box subscription.
func linkNames(t *testing.T, h *harness, link string) []string {
	t.Helper()
	resp, err := http.Get(h.srv.URL + link[strings.Index(link, "/s/"):] + "?client=singbox")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var c struct {
		Outbounds []map[string]any `json:"outbounds"`
		Endpoints []map[string]any `json:"endpoints"`
	}
	if err := json.Unmarshal(body, &c); err != nil {
		t.Fatalf("sing-box: %v %s", err, body)
	}
	var out []string
	for _, o := range append(c.Outbounds, c.Endpoints...) {
		if o["server"] != nil || o["peers"] != nil {
			out = append(out, o["tag"].(string))
		}
	}
	sort.Strings(out)
	return out
}

// TestProtocolScope: a user can have whole servers, single protocols, or both; only those reach
// the servers and the link.
func TestProtocolScope(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	srvA := b.must("POST", "/api/servers", map[string]any{"name": "A", "address": "203.0.113.41", "protocols": []string{"vless", "shadowsocks"}}, 201)["server"].(map[string]any)
	srvB := b.must("POST", "/api/servers", map[string]any{"name": "B", "address": "203.0.113.42", "protocols": []string{"vless"}}, 201)["server"].(map[string]any)
	a, bID := id(srvA["id"]), id(srvB["id"])
	nodeOf := func(srv map[string]any, kind string) int64 {
		for _, n := range srv["nodes"].([]any) {
			if n := n.(map[string]any); n["kind"] == kind {
				return id(n["id"])
			}
		}
		t.Fatalf("no %s on %v", kind, srv["name"])
		return 0
	}
	aVless, aSS, bVless := nodeOf(srvA, "vless"), nodeOf(srvA, "shadowsocks"), nodeOf(srvB, "vless")

	create := func(body map[string]any) map[string]any {
		var list []map[string]any
		_, _, raw := b.do("POST", "/api/users", body)
		if err := json.Unmarshal(raw, &list); err != nil || len(list) != 1 {
			t.Fatalf("create %v: %s", body, raw)
		}
		return list[0]
	}
	everything := create(map[string]any{"name": "all"})
	onlyOne := create(map[string]any{"name": "one", "protocols": []int64{aVless}})
	mixed := create(map[string]any{"name": "mixed", "servers": []int64{bID}, "protocols": []int64{aSS}})

	onA, onB := usersOn(t, h, a), usersOn(t, h, bID)
	ev, one, mx := id(everything["id"]), id(onlyOne["id"]), id(mixed["id"])
	if fmt.Sprint(onA[aVless]) != fmt.Sprint([]int64{ev, one}) || fmt.Sprint(onA[aSS]) != fmt.Sprint([]int64{ev, mx}) ||
		fmt.Sprint(onB[bVless]) != fmt.Sprint([]int64{ev, mx}) {
		t.Errorf("users per protocol: A %v, B %v (all %d, one %d, mixed %d)", onA, onB, ev, one, mx)
	}
	if got := linkNames(t, h, onlyOne["link"].(string)); len(got) != 1 || !strings.Contains(got[0], "A · REALITY") {
		t.Errorf("one protocol: %v", got)
	}
	if got := linkNames(t, h, mixed["link"].(string)); len(got) != 2 || !strings.Contains(strings.Join(got, "|"), "A · SS") ||
		!strings.Contains(strings.Join(got, "|"), "B · REALITY") {
		t.Errorf("mixed: %v", got)
	}
	if got := linkNames(t, h, everything["link"].(string)); len(got) != 3 {
		t.Errorf("everything: %v", got)
	}
	// the scope as the API shows it, and an unknown protocol
	if sc := fmt.Sprint(onlyOne["scope"]); !strings.Contains(sc, fmt.Sprint(aVless)) {
		t.Errorf("scope: %s", sc)
	}
	if code, m, _ := b.do("PATCH", fmt.Sprintf("/api/users/%d", one), map[string]any{"protocols": []int64{987654}}); code != 400 ||
		!strings.Contains(fmt.Sprint(m["error"]), "no protocol 987654") {
		t.Errorf("unknown protocol: %d %v", code, m)
	}
	// changing only the servers keeps the single protocols
	b.must("PATCH", fmt.Sprintf("/api/users/%d", one), map[string]any{"servers": []int64{bID}}, 200)
	if got := linkNames(t, h, onlyOne["link"].(string)); len(got) != 2 {
		t.Errorf("servers added to a protocol: %v", got)
	}
	// back to everything
	b.must("PATCH", fmt.Sprintf("/api/users/%d", one), map[string]any{"servers": []int64{}, "protocols": []int64{}}, 200)
	if got := linkNames(t, h, onlyOne["link"].(string)); len(got) != 3 {
		t.Errorf("everything again: %v", got)
	}
}

// TestPassOnlyExit: a protocol that serves only proxy passes keeps the pass and loses its users.
func TestPassOnlyExit(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	exit := b.must("POST", "/api/servers", map[string]any{"name": "Exit", "address": "198.51.100.51", "protocols": []string{"vless"}}, 201)["server"].(map[string]any)
	exitNode := id(exit["nodes"].([]any)[0].(map[string]any)["id"])
	entry := b.must("POST", "/api/servers", map[string]any{"name": "Entry", "address": "203.0.113.51"}, 201)["server"].(map[string]any)
	entryNode := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", id(entry["id"])), map[string]any{"kind": "vless", "pass_node": exitNode}, 201)["id"])
	var users []map[string]any
	_, _, raw := b.do("POST", "/api/users", map[string]any{"name": "eve"})
	_ = json.Unmarshal(raw, &users)
	link := users[0]["link"].(string)

	if got := linkNames(t, h, link); len(got) != 2 {
		t.Fatalf("before: %v", got)
	}
	n := b.must("PATCH", fmt.Sprintf("/api/nodes/%d", exitNode), map[string]any{"pass_only": true}, 200)
	if n["pass_only"] != true {
		t.Fatalf("pass_only: %v", n)
	}
	if got := linkNames(t, h, link); len(got) != 1 || !strings.Contains(got[0], "Entry") {
		t.Errorf("the exit should leave the link: %v", got)
	}
	st, err := h.p.compileServer(context.Background(), id(exit["id"]))
	if err != nil {
		t.Fatal(err)
	}
	clients := st.Xray.Inbounds[0].Clients
	if len(clients) != 1 || clients[0].Email != passEmail(entryNode) {
		t.Errorf("the exit should accept only the pass: %+v", clients)
	}
	// the server page says what it is for
	v := b.must("GET", fmt.Sprintf("/api/servers/%d", id(exit["id"])), nil, 200)["server"].(map[string]any)
	if notes := fmt.Sprint(v["nodes"].([]any)[0].(map[string]any)["notes"]); !strings.Contains(notes, "Serves only proxy passes") {
		t.Errorf("notes: %s", notes)
	}
	// WireGuard cannot be an exit
	if code, m, _ := b.do("POST", fmt.Sprintf("/api/servers/%d/nodes", id(exit["id"])), map[string]any{"kind": "wireguard", "pass_only": true}); code != 400 ||
		!strings.Contains(fmt.Sprint(m["error"]), "WireGuard cannot be a proxy pass exit") {
		t.Errorf("wireguard: %d %v", code, m)
	}
}

// TestPassExitOff: an entry whose exit is turned off (or whose exit's server is removed) blocks its
// traffic instead of leaving from the entry's own server, and says why; turning the exit on again
// restores the pass. Every change to the exit reaches the entry's server.
func TestPassExitOff(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	exit := b.must("POST", "/api/servers", map[string]any{"name": "Exit", "address": "198.51.100.61", "protocols": []string{"vless"}}, 201)["server"].(map[string]any)
	exitNode := id(exit["nodes"].([]any)[0].(map[string]any)["id"])
	entry := b.must("POST", "/api/servers", map[string]any{"name": "Entry", "address": "203.0.113.61"}, 201)["server"].(map[string]any)
	eid := id(entry["id"])
	entryNode := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", eid), map[string]any{"kind": "vless", "pass_node": exitNode}, 201)["id"])
	b.must("POST", "/api/users", map[string]any{"name": "eve"}, 201)

	route := func() (outbound string, hasPassOut bool) {
		st, err := h.p.compileServer(context.Background(), eid)
		if err != nil {
			t.Fatal(err)
		}
		var base struct {
			Outbounds []map[string]any `json:"outbounds"`
			Routing   struct {
				Rules []map[string]any `json:"rules"`
			} `json:"routing"`
		}
		_ = json.Unmarshal(st.Xray.Base, &base)
		for _, r := range base.Routing.Rules {
			if r["ruleTag"] == passTag(entryNode) {
				outbound = fmt.Sprint(r["outboundTag"])
			}
		}
		for _, o := range base.Outbounds {
			if o["tag"] == passTag(entryNode) {
				hasPassOut = true
			}
		}
		return
	}
	broken := func() string {
		v := b.must("GET", fmt.Sprintf("/api/servers/%d", eid), nil, 200)["server"].(map[string]any)
		n := v["nodes"].([]any)[0].(map[string]any)
		return fmt.Sprint(n["pass_broken"], " | ", n["notes"])
	}
	if out, ok := route(); out != passTag(entryNode) || !ok {
		t.Fatalf("working pass: rule to %q, outbound %v", out, ok)
	}
	rev := func() string {
		st, _ := h.p.compileServer(context.Background(), eid)
		return st.Rev
	}
	before := rev()
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", exitNode), map[string]any{"enabled": false}, 200)
	if out, ok := route(); out != "block" || ok {
		t.Errorf("exit turned off: rule to %q, outbound %v - the entry must block, not leave directly", out, ok)
	}
	if got := broken(); !strings.Contains(got, "is turned off") || !strings.Contains(got, "Its traffic is blocked") {
		t.Errorf("the entry should say its pass does not work: %s", got)
	}
	if rev() == before {
		t.Error("the entry's state did not change when its exit was turned off")
	}
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", exitNode), map[string]any{"enabled": true}, 200)
	if out, ok := route(); out != passTag(entryNode) || !ok {
		t.Errorf("exit back on: rule to %q, outbound %v", out, ok)
	}
	// a new port on the exit reaches the entry's outbound
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", exitNode), map[string]any{"port": 8443}, 200)
	st, _ := h.p.compileServer(context.Background(), eid)
	if !strings.Contains(string(st.Xray.Base), "8443") {
		t.Errorf("the entry still points at the old port: %s", st.Xray.Base)
	}
	// the exit's server removed: the entry blocks
	b.must("DELETE", fmt.Sprintf("/api/servers/%d", id(exit["id"])), nil, 200)
	if out, _ := route(); out != "block" {
		t.Errorf("exit server removed: rule to %q", out)
	}
	if got := broken(); !strings.Contains(got, "server was removed") {
		t.Errorf("removed exit server: %s", got)
	}
}

func TestEndpointNames(t *testing.T) {
	srv := &Server{Name: "Tokyo", Country: "JP"}
	for _, c := range []struct{ name, kind, want string }{
		{"", "vless", "🇯🇵 Tokyo · VLESS"},
		{"IPLC 01", "vless", "🇯🇵 IPLC 01"},
		{"🇭🇰 HK relay", "vless", "🇭🇰 HK relay"},
	} {
		n := &Node{Name: c.name, Kind: c.kind, Settings: json.RawMessage(`{"security":"none","transport":"ws","cdn":true,"cdn_host":"x.example.com"}`)}
		if c.name == "" {
			n.Settings = json.RawMessage(`{}`)
		}
		if got := endpointName(srv, n); !strings.HasPrefix(got, c.want) {
			t.Errorf("%q: got %q, want %q", c.name, got, c.want)
		}
	}
	eps := []subgen.Endpoint{{Name: "IPLC"}, {Name: "IPLC"}, {Name: "IPLC 2"}, {Name: "IPLC"}, {Name: "Other"}}
	uniqueNames(eps)
	var got []string
	for _, e := range eps {
		got = append(got, e.Name)
	}
	if strings.Join(got, "|") != "IPLC|IPLC 3|IPLC 2|IPLC 4|Other" {
		t.Errorf("unique names: %v", got)
	}
}

// TestWGPeersOnce: links fetched while the server is compiled give each user one key pair and one
// address, the same ones the server gets.
func TestWGPeersOnce(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	srv := b.must("POST", "/api/servers", map[string]any{"name": "WG", "address": "203.0.113.61", "protocols": []string{"wireguard"}}, 201)["server"].(map[string]any)
	sid := id(srv["id"])
	var users []map[string]any
	_, _, raw := b.do("POST", "/api/users", map[string]any{"name": "w", "count": 8})
	_ = json.Unmarshal(raw, &users)
	done := make(chan struct{})
	for _, u := range users {
		go func(link string) {
			defer func() { done <- struct{}{} }()
			resp, err := http.Get(h.srv.URL + link[strings.Index(link, "/s/"):] + "?client=singbox")
			if err == nil {
				resp.Body.Close()
			}
		}(u["link"].(string))
		go func() {
			defer func() { done <- struct{}{} }()
			_, _ = h.p.compileServer(context.Background(), sid)
		}()
	}
	for range 2 * len(users) {
		<-done
	}
	st, err := h.p.compileServer(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	peers := st.WireGuard[0].Peers
	keys, ips := map[string]bool{}, map[string]bool{}
	for _, p := range peers {
		keys[p.PublicKey] = true
		ips[p.AllowedIPs[0]] = true
	}
	if len(peers) != len(users) || len(keys) != len(users) || len(ips) != len(users) {
		t.Fatalf("%d users: %d peers, %d keys, %d addresses", len(users), len(peers), len(keys), len(ips))
	}
	// each user's link carries the key the server knows
	stored, _ := h.p.wgPeersOf(context.Background(), st.WireGuard[0].NodeID)
	for _, pr := range stored {
		if !keys[pr.PublicKey] {
			t.Errorf("user %d: the server has another key", pr.SubID)
		}
	}
}

// TestRemovedAccess: removing a protocol or a server takes it out of users' access; a user left with
// nothing has no access (never everything), can still be edited, and a new protocol never takes a
// removed one's id - so it never reaches the people who had the old one.
func TestRemovedAccess(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	srv := b.must("POST", "/api/servers", map[string]any{"name": "S", "address": "203.0.113.51", "protocols": []string{"vless", "shadowsocks"}}, 201)["server"].(map[string]any)
	other := b.must("POST", "/api/servers", map[string]any{"name": "O", "address": "203.0.113.52", "protocols": []string{"vless"}}, 201)["server"].(map[string]any)
	sid, oid := id(srv["id"]), id(other["id"])
	var newest int64
	for _, n := range srv["nodes"].([]any) {
		newest = max(newest, id(n.(map[string]any)["id"]))
	}
	create := func(body map[string]any) int64 {
		var list []map[string]any
		_, _, raw := b.do("POST", "/api/users", body)
		if err := json.Unmarshal(raw, &list); err != nil || len(list) != 1 {
			t.Fatalf("create %v: %s", body, raw)
		}
		return id(list[0]["id"])
	}
	single := create(map[string]any{"name": "single", "protocols": []int64{newest}})
	whole := create(map[string]any{"name": "whole", "servers": []int64{sid}})
	both := create(map[string]any{"name": "both", "servers": []int64{oid}, "protocols": []int64{newest}})
	scopeOf := func(u int64) string {
		raw, _ := json.Marshal(b.must("GET", fmt.Sprintf("/api/users/%d", u), nil, 200)["user"].(map[string]any)["scope"])
		var sc Scope
		_ = json.Unmarshal(raw, &sc)
		return fmt.Sprintf("servers %v protocols %v none:%v", sc.Servers, sc.Nodes, sc.None)
	}

	b.must("DELETE", fmt.Sprintf("/api/nodes/%d", newest), nil, 200)
	if sc := scopeOf(single); !strings.Contains(sc, "none:true") {
		t.Errorf("single protocol removed: %s", sc)
	}
	if sc := scopeOf(both); sc != fmt.Sprintf("servers [%d] protocols [] none:false", oid) {
		t.Errorf("one of two removed: %s", sc)
	}
	added := b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": "trojan", "port": 8443,
		"settings": map[string]any{"security": "tls", "cert_mode": "self", "sni": "www.example.com"}}, 201)
	if id(added["id"]) <= newest {
		t.Errorf("a removed protocol's id was handed out again: %v", added["id"])
	}
	for node, users := range usersOn(t, h, sid) {
		for _, u := range users {
			if u == single {
				t.Errorf("a user with no access is on protocol %d", node)
			}
		}
	}
	// editing such a user keeps their access as it is (nothing), unless it is changed
	b.must("PATCH", fmt.Sprintf("/api/users/%d", single), map[string]any{"note": "still here"}, 200)
	if sc := scopeOf(single); !strings.Contains(sc, "none:true") {
		t.Errorf("after an edit: %s", sc)
	}

	b.must("DELETE", fmt.Sprintf("/api/servers/%d", sid), nil, 200)
	if sc := scopeOf(whole); !strings.Contains(sc, "none:true") {
		t.Errorf("server removed: %s", sc)
	}
	if sc := scopeOf(both); sc != fmt.Sprintf("servers [%d] protocols [] none:false", oid) {
		t.Errorf("both, after the server went: %s", sc)
	}
	var events int
	_ = h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind = 'user_no_access'`).Scan(&events)
	if events != 2 {
		t.Errorf("no-access events: %d", events)
	}
	// both lists sent empty is everything, on purpose
	b.must("PATCH", fmt.Sprintf("/api/users/%d", whole), map[string]any{"servers": []int64{}, "protocols": []int64{}}, 200)
	if sc := scopeOf(whole); sc != "servers [] protocols [] none:false" {
		t.Errorf("everything: %s", sc)
	}
}
