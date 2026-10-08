package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"meridian/internal/proto"
	"meridian/internal/seal"
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
	// a public name is what users see everywhere, apps included
	pub := &Server{Name: "tyo-vultr-03", PublicName: "Tokyo", Country: "JP"}
	if got := endpointName(pub, &Node{Kind: "vless", Settings: json.RawMessage(`{}`)}); !strings.HasPrefix(got, "🇯🇵 Tokyo · VLESS") {
		t.Errorf("public name: %q", got)
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

// TestPassChains: a chain has two passes at most - entry, relay, exit - each on another server; the
// relay takes its entries' pass users and passes them on as its own; longer chains and chains back to
// a server they passed are refused (or, stored before, blocked with the reason); a change at the far
// end reaches the first entry's server too.
func TestPassChains(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	mk := func(name, addr string) int64 {
		return id(b.must("POST", "/api/servers", map[string]any{"name": name, "address": addr}, 201)["server"].(map[string]any)["id"])
	}
	a, bs, c, d := mk("A", "203.0.113.71"), mk("B", "198.51.100.71"), mk("C", "198.51.100.72"), mk("D", "198.51.100.73")
	node := func(srv int64, body map[string]any) (int, map[string]any) {
		code, m, _ := b.do("POST", fmt.Sprintf("/api/servers/%d/nodes", srv), body)
		return code, m
	}
	must := func(srv int64, body map[string]any) int64 {
		t.Helper()
		code, m := node(srv, body)
		if code != 201 {
			t.Fatalf("POST node on %d %v: %d %v", srv, body, code, m)
		}
		return id(m["id"])
	}
	refused := func(what string, code int, m map[string]any, want string) {
		t.Helper()
		if code != 400 || !strings.Contains(fmt.Sprint(m["error"]), want) {
			t.Errorf("%s: %d %v (want %q)", what, code, m, want)
		}
	}
	x := must(c, map[string]any{"kind": "vless"})
	m := must(bs, map[string]any{"kind": "vless", "pass_node": x})
	e := must(a, map[string]any{"kind": "vless", "pass_node": m}) // two passes: A -> B -> C
	b.must("POST", "/api/users", map[string]any{"name": "eve"}, 201)

	compiled := func(srv int64) (base string, clients map[int64][]string) {
		t.Helper()
		st, err := h.p.compileServer(context.Background(), srv)
		if err != nil {
			t.Fatal(err)
		}
		clients = map[int64][]string{}
		for _, in := range st.Xray.Inbounds {
			nid, _ := proto.ParseInboundTag(in.Tag)
			for _, cl := range in.Clients {
				clients[nid] = append(clients[nid], cl.Email)
			}
		}
		return string(st.Xray.Base), clients
	}
	baseA, _ := compiled(a)
	baseB, clientsB := compiled(bs)
	_, clientsC := compiled(c)
	if !strings.Contains(baseA, "198.51.100.71") || strings.Contains(baseA, "198.51.100.72") {
		t.Errorf("the entry's pass goes to the relay only: %s", baseA)
	}
	if !slices.Contains(clientsB[m], passEmail(e)) || !strings.Contains(baseB, "198.51.100.72") {
		t.Errorf("the relay takes the entry's pass user and passes on to the exit: %v %s", clientsB[m], baseB)
	}
	if !slices.Contains(clientsC[x], passEmail(m)) || slices.Contains(clientsC[x], passEmail(e)) {
		t.Errorf("the exit knows the relay only: %v", clientsC[x])
	}
	card := func(srv, nid int64) map[string]any {
		for _, n := range b.must("GET", fmt.Sprintf("/api/servers/%d", srv), nil, 200)["server"].(map[string]any)["nodes"].([]any) {
			if id(n.(map[string]any)["id"]) == nid {
				return n.(map[string]any)
			}
		}
		t.Fatalf("no protocol %d on %d", nid, srv)
		return nil
	}
	if got := card(a, e)["pass_name"]; got != "B · REALITY → C · REALITY" {
		t.Errorf("the entry shows its chain: %v", got)
	}

	// three passes are refused, whichever end grows
	f := must(d, map[string]any{"kind": "vless"})
	code, msg := node(d, map[string]any{"kind": "vless", "port": 8443, "pass_node": e})
	refused("an entry through a chain of two", code, msg, "passes on twice already")
	code, msg, _ = b.do("PATCH", fmt.Sprintf("/api/nodes/%d", x), map[string]any{"pass_node": f})
	refused("the far exit passing on", code, msg, "which passes through this one")
	z := must(d, map[string]any{"kind": "trojan", "pass_node": must(a, map[string]any{"kind": "vless", "port": 8443})})
	code, msg, _ = b.do("PATCH", fmt.Sprintf("/api/nodes/%d", m), map[string]any{"pass_node": z})
	refused("a relay passing to another relay", code, msg, "and the exit passes on too")
	// a chain never comes back to a server it passed
	code, msg = node(c, map[string]any{"kind": "vless", "port": 8443, "pass_node": m})
	refused("back to the exit's server", code, msg, "passes on back to C")
	q := must(c, map[string]any{"kind": "trojan"})
	must(a, map[string]any{"kind": "vless", "port": 9443, "pass_node": q})
	code, msg, _ = b.do("PATCH", fmt.Sprintf("/api/nodes/%d", q), map[string]any{"pass_node": must(a, map[string]any{"kind": "trojan", "port": 9444})})
	refused("back to an entry's server", code, msg, "every pass goes to another server")
	// a relay that serves only passes is fine, and moving the relay's pass elsewhere too
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", m), map[string]any{"pass_only": true}, 200)
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", m), map[string]any{"pass_node": f}, 200)
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", m), map[string]any{"pass_node": x}, 200)

	// the far exit turned off: the first entry blocks, with the reason, and its server is refreshed
	rev := func(srv int64) string {
		st, _ := h.p.compileServer(context.Background(), srv)
		return st.Rev
	}
	before := rev(a)
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", x), map[string]any{"enabled": false}, 200)
	if why := fmt.Sprint(card(a, e)["pass_broken"]); !strings.Contains(why, "passes on to C · REALITY, which is turned off") {
		t.Errorf("entry with its far exit off: %s", why)
	}
	if rev(a) == before {
		t.Error("the first entry's server did not change with the far exit")
	}
	if baseA, _ = compiled(a); !strings.Contains(baseA, `"outboundTag":"block","ruleTag":"`+passTag(e)+`"`) {
		t.Errorf("the first entry does not block: %s", baseA)
	}
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", x), map[string]any{"enabled": true}, 200)

	// stored before these checks: three passes - the first entry blocks, the relay drops its user
	if _, err := h.p.db.Exec(`UPDATE nodes SET pass_node = ? WHERE id = ?`, f, x); err != nil {
		t.Fatal(err)
	}
	if why := fmt.Sprint(card(a, e)["pass_broken"]); !strings.Contains(why, "passes on twice") {
		t.Errorf("a chain of three: %s", why)
	}
	if _, clientsB = compiled(bs); slices.Contains(clientsB[m], passEmail(e)) {
		t.Error("the relay still takes the pass user of a chain of three")
	}
}

// TestRotateKeepsPasses: rotating an entry server's token does not change its proxy-pass credentials
// (its old agent keeps passing until it is reinstalled); when the agent is back with the new token,
// the entry and its exit switch to new credentials together.
func TestRotateKeepsPasses(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	exit := b.must("POST", "/api/servers", map[string]any{"name": "Exit", "address": "198.51.100.81", "protocols": []string{"vless"}}, 201)["server"].(map[string]any)
	xid := id(exit["id"])
	exitNode := id(exit["nodes"].([]any)[0].(map[string]any)["id"])
	entry := b.must("POST", "/api/servers", map[string]any{"name": "Entry", "address": "203.0.113.81"}, 201)
	eid := id(entry["server"].(map[string]any)["id"])
	entryNode := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", eid), map[string]any{"kind": "vless", "pass_node": exitNode}, 201)["id"])
	passID := func() string {
		st, err := h.p.compileServer(context.Background(), xid)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range st.Xray.Inbounds[0].Clients {
			if c.Email == passEmail(entryNode) {
				return string(c.JSON)
			}
		}
		t.Fatal("the exit has no pass user")
		return ""
	}
	entryOut := func() string {
		st, err := h.p.compileServer(context.Background(), eid)
		if err != nil {
			t.Fatal(err)
		}
		return string(st.Xray.Base)
	}
	before, beforeOut := passID(), entryOut()
	r := b.must("POST", fmt.Sprintf("/api/servers/%d/rotate-token", eid), nil, 200)
	if passID() != before || entryOut() != beforeOut {
		t.Fatal("rotating the token changed the pass credentials before the agent is back")
	}
	// the reinstalled agent connects with the new token
	tok := regexp.MustCompile(`MERIDIAN_TOKEN='([^']+)'`).FindStringSubmatch(r["install"].(string))[1]
	sid, secret, err := seal.ParseToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := seal.Derive(secret)
	resp := agentRequest(t, h, keys, sid, "GET", "/agent/v1/state?rev=&wait=0", time.Now().Unix(), seal.Nonce())
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("state: %d", resp.StatusCode)
	}
	after := passID()
	if after == before {
		t.Fatal("the pass credentials did not change with the new token")
	}
	var c struct{ ID string }
	_ = json.Unmarshal([]byte(after), &c)
	if !strings.Contains(entryOut(), c.ID) {
		t.Errorf("the entry and its exit disagree: exit %s", after)
	}
}

// TestExitProtocolRemoved: removing an exit protocol blocks the protocols that passed through it
// (with the reason on their cards) - they never leave from their own servers instead.
func TestExitProtocolRemoved(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	exit := b.must("POST", "/api/servers", map[string]any{"name": "Exit", "address": "198.51.100.91", "protocols": []string{"vless"}}, 201)["server"].(map[string]any)
	exitNode := id(exit["nodes"].([]any)[0].(map[string]any)["id"])
	eid := id(b.must("POST", "/api/servers", map[string]any{"name": "Entry", "address": "203.0.113.91"}, 201)["server"].(map[string]any)["id"])
	entryNode := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", eid), map[string]any{"kind": "vless", "pass_node": exitNode}, 201)["id"])
	b.must("DELETE", fmt.Sprintf("/api/nodes/%d", exitNode), nil, 200)
	st, err := h.p.compileServer(context.Background(), eid)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(st.Xray.Base), `"outboundTag":"block","ruleTag":"`+passTag(entryNode)+`"`) {
		t.Errorf("the entry does not block: %s", st.Xray.Base)
	}
	n := b.must("GET", fmt.Sprintf("/api/servers/%d", eid), nil, 200)["server"].(map[string]any)["nodes"].([]any)[0].(map[string]any)
	if fmt.Sprint(n["pass_broken"]) != "its exit protocol was removed" || n["pass_name"] != "a removed protocol" {
		t.Errorf("entry card: %v / %v", n["pass_broken"], n["pass_name"])
	}
}
