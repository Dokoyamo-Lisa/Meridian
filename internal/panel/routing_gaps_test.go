package panel

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"meridian/internal/proto"
)

// checkTags fails when a rule or load balancer of a compiled server refers to an outbound or
// balancer it does not have: Xray would then send that traffic through its first outbound instead.
func checkTags(t *testing.T, c compiledXray) {
	t.Helper()
	bals := map[string]bool{}
	for _, b := range c.balancers {
		bals[fmt.Sprint(b["tag"])] = true
		if fb, ok := b["fallbackTag"]; ok && c.outbounds[fmt.Sprint(fb)] == nil { // fastest first only
			t.Errorf("balancer %v falls back to a missing outbound", b)
		}
		for _, sel := range b["selector"].([]any) {
			found := false
			for tag := range c.outbounds {
				found = found || strings.HasPrefix(tag, fmt.Sprint(sel))
			}
			if !found {
				t.Errorf("balancer %v has no member", b)
			}
		}
	}
	for _, r := range c.rules {
		if tag, ok := r["outboundTag"]; ok && c.outbounds[fmt.Sprint(tag)] == nil {
			t.Errorf("rule %v: no outbound %v", r["ruleTag"], tag)
		}
		if tag, ok := r["balancerTag"]; ok && !bals[fmt.Sprint(tag)] {
			t.Errorf("rule %v: no balancer %v", r["ruleTag"], tag)
		}
	}
}

func ruleScope(t *testing.T, b *client, ruleID int64) (servers, nodes []int64, target string) {
	t.Helper()
	for _, r := range b.must("GET", "/api/routing", nil, 200)["rules"].([]any) {
		m := r.(map[string]any)
		if id(m["id"]) != ruleID {
			continue
		}
		for _, x := range m["servers"].([]any) {
			servers = append(servers, id(x))
		}
		for _, x := range m["nodes"].([]any) {
			nodes = append(nodes, id(x))
		}
		return servers, nodes, fmt.Sprint(m["target"])
	}
	t.Fatalf("rule %d not found", ruleID)
	return
}

func balancerMembers(t *testing.T, b *client, lb int64) []string {
	t.Helper()
	for _, x := range b.must("GET", "/api/routing", nil, 200)["balancers"].([]any) {
		m := x.(map[string]any)
		if id(m["id"]) == lb {
			out := []string{}
			for _, v := range m["members"].([]any) {
				out = append(out, fmt.Sprint(v))
			}
			return out
		}
	}
	t.Fatalf("load balancer %d not found", lb)
	return nil
}

// TestRouteRemovals: removed servers, protocols and external nodes leave the rules and load
// balancers that named them - a rule left with nothing applies nowhere, never everywhere - while
// rules that sent traffic there block it, and the servers that need to know are recompiled.
func TestRouteRemovals(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	mk := func(name, addr string) int64 {
		return id(b.must("POST", "/api/servers", map[string]any{"name": name, "address": addr, "protocols": []string{}}, 201)["server"].(map[string]any)["id"])
	}
	node := func(srv int64, kind string) int64 {
		t.Helper()
		return id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", srv), map[string]any{"kind": kind}, 201)["id"])
	}
	a, bs, c := mk("A", "203.0.113.81"), mk("B", "198.51.100.81"), mk("C", "198.51.100.82")
	a1, a2, b1, c1 := node(a, "vless"), node(a, "trojan"), node(bs, "vless"), node(c, "vless")
	x := id(b.must("POST", "/api/external-nodes", map[string]any{"text": extSS}, 200)["added"].([]any)[0].(map[string]any)["id"])
	rule := func(body map[string]any) int64 {
		t.Helper()
		rules := b.must("POST", "/api/routing/rules", body, 200)["rules"].([]any)
		return id(rules[len(rules)-1].(map[string]any)["id"])
	}
	lb := id(b.must("POST", "/api/routing/balancers", map[string]any{"name": "LB",
		"members": []string{fmt.Sprintf("node:%d", b1), fmt.Sprintf("node:%d", c1), fmt.Sprintf("ext:%d", x)}}, 200)["balancers"].([]any)[0].(map[string]any)["id"])
	r1 := rule(map[string]any{"nodes": []int64{a1, a2}, "match": map[string]any{"sites": []string{"openai"}}, "target": fmt.Sprintf("node:%d", b1)})
	r2 := rule(map[string]any{"servers": []int64{c}, "match": map[string]any{"sites": []string{"netflix"}}, "target": fmt.Sprintf("ext:%d", x)})
	r3 := rule(map[string]any{"servers": []int64{bs, c}, "match": map[string]any{"countries": []string{"cn"}}, "target": "direct"})
	r4 := rule(map[string]any{"nodes": []int64{a1}, "match": map[string]any{"all": true}, "target": fmt.Sprintf("lb:%d", lb)})
	r5 := rule(map[string]any{"servers": []int64{bs}, "match": map[string]any{"sites": []string{"youtube"}}, "target": fmt.Sprintf("ext:%d", x)})
	for _, s := range []int64{a, bs, c} {
		checkTags(t, compileX(t, h, s))
	}

	// a protocol goes: the rule's scope loses it
	h.p.hub.takeDirty()
	b.must("DELETE", fmt.Sprintf("/api/nodes/%d", a2), nil, 200)
	if _, nodes, _ := ruleScope(t, b, r1); !slices.Equal(nodes, []int64{a1}) {
		t.Errorf("scope after removing a protocol: %v", nodes)
	}
	if dirty := h.p.hub.takeDirty(); !slices.Contains(dirty, bs) || !slices.Contains(dirty, c) {
		t.Errorf("servers recompiled after a protocol went: %v", dirty)
	}
	// the last one goes: the rules keep it and apply nowhere - never everywhere
	b.must("DELETE", fmt.Sprintf("/api/nodes/%d", a1), nil, 200)
	if _, nodes, _ := ruleScope(t, b, r1); !slices.Equal(nodes, []int64{a1}) {
		t.Errorf("scope left empty: %v", nodes)
	}
	if ca := compileX(t, h, a); ca.rule(fmt.Sprintf("r%d", r1)) != nil || ca.rule(fmt.Sprintf("r%d", r4)) != nil {
		t.Errorf("a rule for removed protocols applies on A: %v", ca.rules)
	}
	if p := fmt.Sprint(b.must("GET", "/api/routing", nil, 200)["problems"]); !strings.Contains(p, "applies only to servers or protocols that were removed") {
		t.Errorf("problems: %s", p)
	}
	// such a rule can still be changed: turned off, renamed, given a new scope (the removed id drops
	// out), but no protocol that never existed can be added
	b.must("PATCH", fmt.Sprintf("/api/routing/rules/%d", r1), map[string]any{"enabled": false}, 200)
	b.must("PATCH", fmt.Sprintf("/api/routing/rules/%d", r1), map[string]any{"name": "AI", "nodes": []int64{a1}, "target": fmt.Sprintf("node:%d", b1)}, 200)
	if code, _, _ := b.do("PATCH", fmt.Sprintf("/api/routing/rules/%d", r1), map[string]any{"nodes": []int64{a1, 99999}}); code != 400 {
		t.Errorf("an unknown protocol: %d", code)
	}
	b.must("PATCH", fmt.Sprintf("/api/routing/rules/%d", r1), map[string]any{"nodes": []int64{a1, c1}}, 200)
	if _, nodes, _ := ruleScope(t, b, r1); !slices.Equal(nodes, []int64{c1}) {
		t.Errorf("new scope: %v", nodes)
	}

	// a balancer member's protocol goes: the balancer loses it
	b.must("DELETE", fmt.Sprintf("/api/nodes/%d", c1), nil, 200)
	if m := balancerMembers(t, b, lb); !slices.Equal(m, []string{fmt.Sprintf("node:%d", b1), fmt.Sprintf("ext:%d", x)}) {
		t.Errorf("members after a protocol went: %v", m)
	}
	// a load balancer is edited with the members it has (one turned off stays)
	b.must("PATCH", fmt.Sprintf("/api/external-nodes/%d", x), map[string]any{"enabled": false}, 200)
	b.must("PATCH", fmt.Sprintf("/api/routing/balancers/%d", lb), map[string]any{"name": "LB2", "members": balancerMembers(t, b, lb)}, 200)
	b.must("PATCH", fmt.Sprintf("/api/external-nodes/%d", x), map[string]any{"enabled": true}, 200)

	// a server goes: rules lose it; the other servers are recompiled
	h.p.hub.takeDirty()
	b.must("DELETE", fmt.Sprintf("/api/servers/%d", c), nil, 200)
	if servers, _, _ := ruleScope(t, b, r3); !slices.Equal(servers, []int64{bs}) {
		t.Errorf("scope after removing a server: %v", servers)
	}
	if servers, _, _ := ruleScope(t, b, r2); !slices.Equal(servers, []int64{c}) {
		t.Errorf("a rule for a removed server only: %v", servers)
	}
	if dirty := h.p.hub.takeDirty(); !slices.Contains(dirty, a) || !slices.Contains(dirty, bs) {
		t.Errorf("servers recompiled after a server went: %v", dirty)
	}

	// an external node goes: rules that sent traffic there block it, load balancers lose it, and its
	// id is never handed out again
	gone := b.must("DELETE", fmt.Sprintf("/api/external-nodes/%d", x), nil, 200)
	if !strings.Contains(fmt.Sprint(gone["blocked"]), "youtube") {
		t.Errorf("blocked: %v", gone)
	}
	if m := balancerMembers(t, b, lb); !slices.Equal(m, []string{fmt.Sprintf("node:%d", b1)}) {
		t.Errorf("members after the external node went: %v", m)
	}
	if _, _, target := ruleScope(t, b, r5); target != fmt.Sprintf("ext:%d", x) {
		t.Errorf("the rule's exit changed: %s", target)
	}
	cb := compileX(t, h, bs)
	if r := cb.rule(fmt.Sprintf("r%d", r5)); r == nil || r["outboundTag"] != "block" {
		t.Errorf("rule to a removed node: %v", r)
	}
	checkTags(t, cb)
	if !slices.ContainsFunc(h.p.routeNotesOf(bs), func(n string) bool { return strings.Contains(n, "external node was removed") }) {
		t.Errorf("notes on B: %v", h.p.routeNotesOf(bs))
	}
	again := b.must("POST", "/api/external-nodes", map[string]any{"text": extHy2}, 200)["added"].([]any)
	if len(again) != 1 || id(again[0].(map[string]any)["id"]) <= x {
		t.Fatalf("a removed external node's id was handed out again: %v (removed %d)", again, x)
	}
	if r := compileX(t, h, bs).rule(fmt.Sprintf("r%d", r5)); r["outboundTag"] != "block" {
		t.Errorf("a new node took over a removed one's rule: %v", r)
	}
	// the balancer cannot go while a rule uses it, the rule can
	b.must("DELETE", fmt.Sprintf("/api/routing/rules/%d", r4), nil, 200)
	b.must("DELETE", fmt.Sprintf("/api/routing/balancers/%d", lb), nil, 200)
}

// TestRouteRecompiles: whatever changes how a rule reaches its exit recompiles the servers whose
// rules use it - new keys, a new server address, a token rotation taking effect.
func TestRouteRecompiles(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	a := id(b.must("POST", "/api/servers", map[string]any{"name": "A", "address": "203.0.113.91", "protocols": []string{"vless"}}, 201)["server"].(map[string]any)["id"])
	xs := id(b.must("POST", "/api/servers", map[string]any{"name": "X", "address": "198.51.100.91", "protocols": []string{}}, 201)["server"].(map[string]any)["id"])
	exit := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", xs), map[string]any{"kind": "vless"}, 201)["id"])
	b.must("POST", "/api/routing/rules", map[string]any{"match": map[string]any{"sites": []string{"openai"}}, "target": fmt.Sprintf("node:%d", exit)}, 200)
	before := compileX(t, h, a).outbounds[fmt.Sprintf("route-node-%d", exit)]
	// the exit's own server says which rules send traffic through it (its confirmations name them)
	xv := b.must("GET", fmt.Sprintf("/api/servers/%d", xs), nil, 200)["server"].(map[string]any)
	if uses := fmt.Sprint(xv["nodes"].([]any)[0].(map[string]any)["route_uses"]); uses != "[openai]" {
		t.Errorf("route uses: %s", uses)
	}

	h.p.hub.takeDirty()
	b.must("POST", fmt.Sprintf("/api/nodes/%d/regenerate", exit), map[string]any{}, 200)
	if dirty := h.p.hub.takeDirty(); !slices.Contains(dirty, a) {
		t.Errorf("new keys at the exit: %v", dirty)
	}
	if after := compileX(t, h, a).outbounds[fmt.Sprintf("route-node-%d", exit)]; fmt.Sprint(after) == fmt.Sprint(before) {
		t.Error("the route outbound kept the old keys")
	}
	b.must("PATCH", fmt.Sprintf("/api/servers/%d", xs), map[string]any{"address": "198.51.100.92"}, 200)
	if dirty := h.p.hub.takeDirty(); !slices.Contains(dirty, a) {
		t.Errorf("a new exit address: %v", dirty)
	}
	if ob := compileX(t, h, a).outbounds[fmt.Sprintf("route-node-%d", exit)]; !strings.Contains(fmt.Sprint(ob), "198.51.100.92") {
		t.Errorf("route outbound: %v", ob)
	}
	// the server's identity at the exit follows a rotated token once its agent is back with it
	srv, _ := h.p.serverByID(t.Context(), a)
	h.p.db.Exec1(`UPDATE servers SET secret = 'rotated' WHERE id = ?`, a)
	srv.Secret = "rotated"
	h.p.adoptPassSecret(t.Context(), srv)
	if dirty := h.p.hub.takeDirty(); !slices.Contains(dirty, xs) {
		t.Errorf("rotated identity: %v", dirty)
	}
}

// TestOwnServerExits: a rule or load balancer member naming a protocol on the server itself leaves
// the way that protocol's own traffic does there - directly, from its own address, or through its
// proxy pass - and "direct" for a protocol with its own address leaves from it, pass or not.
func TestOwnServerExits(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	a := id(b.must("POST", "/api/servers", map[string]any{"name": "A", "protocols": []string{}}, 201)["server"].(map[string]any)["id"])
	reported(t, h, a, []string{"203.0.113.71", "203.0.113.72"}, `{"nftables":true,"certs":true}`)
	bs := id(b.must("POST", "/api/servers", map[string]any{"name": "B", "address": "198.51.100.71", "protocols": []string{}}, 201)["server"].(map[string]any)["id"])
	node := func(srv int64, body map[string]any) int64 {
		t.Helper()
		return id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", srv), body, 201)["id"])
	}
	exitB := node(bs, map[string]any{"kind": "trojan"})
	plain := node(a, map[string]any{"kind": "vless", "port": 443})
	passing := node(a, map[string]any{"kind": "vmess", "port": 8443, "pass_node": exitB})
	bound := node(a, map[string]any{"kind": "shadowsocks", "port": 9443, "bind_ip": "203.0.113.72", "pass_node": exitB})
	hy := node(a, map[string]any{"kind": "hysteria2", "port": 443})

	r := func(body map[string]any) int64 {
		t.Helper()
		rules := b.must("POST", "/api/routing/rules", body, 200)["rules"].([]any)
		return id(rules[len(rules)-1].(map[string]any)["id"])
	}
	toPass := r(map[string]any{"match": map[string]any{"sites": []string{"openai"}}, "target": fmt.Sprintf("node:%d", passing)})
	toPlain := r(map[string]any{"match": map[string]any{"sites": []string{"google"}}, "target": fmt.Sprintf("node:%d", plain)})
	toHy := r(map[string]any{"match": map[string]any{"sites": []string{"github"}}, "target": fmt.Sprintf("node:%d", hy)})
	direct := r(map[string]any{"match": map[string]any{"countries": []string{"cn"}}, "target": "direct"})
	lb := id(b.must("POST", "/api/routing/balancers", map[string]any{"name": "Both", "members": []string{fmt.Sprintf("node:%d", passing), "direct"}},
		200)["balancers"].([]any)[0].(map[string]any)["id"])
	r(map[string]any{"match": map[string]any{"sites": []string{"netflix"}}, "target": fmt.Sprintf("lb:%d", lb)})

	ca := compileX(t, h, a)
	checkTags(t, ca)
	pass := ca.outbounds[passTag(passing)]
	if ob := ca.outbounds[fmt.Sprintf("route-node-%d", passing)]; ob == nil || ob["protocol"] != pass["protocol"] ||
		fmt.Sprint(ob["settings"]) != fmt.Sprint(pass["settings"]) {
		t.Errorf("through its own proxy pass: %v (pass %v)", ob, pass)
	}
	if m := ca.outbounds[fmt.Sprintf("lb%d.node%d", lb, passing)]; m == nil || fmt.Sprint(m["settings"]) != fmt.Sprint(pass["settings"]) {
		t.Errorf("balancer member through its proxy pass: %v", m)
	}
	if ob := ca.outbounds[fmt.Sprintf("route-node-%d", plain)]; ob == nil || ob["protocol"] != "freedom" || ob["sendThrough"] != nil {
		t.Errorf("a protocol that leaves directly: %v", ob)
	}
	if ob := ca.outbounds[fmt.Sprintf("route-node-%d", hy)]; ob == nil || ob["protocol"] != "freedom" {
		t.Errorf("Hysteria2 on the server itself: %v", ob)
	}
	for _, rt := range []int64{toPass, toPlain, toHy} {
		if ca.rule(fmt.Sprintf("r%d", rt)) == nil {
			t.Errorf("rule %d missing: %v", rt, ca.rules)
		}
	}
	// "direct" for the bound protocol that passes elsewhere: from its own address
	found := false
	for _, rule := range ca.rules {
		if strings.HasPrefix(fmt.Sprint(rule["ruleTag"]), fmt.Sprintf("r%d.", direct)) && rule["outboundTag"] == bindTag(bound) {
			found = true
		}
	}
	if ob := ca.outbounds[bindTag(bound)]; !found || ob == nil || ob["sendThrough"] != "203.0.113.72" {
		t.Errorf("direct from its own address: %v / %v", ob, ca.rules)
	}
	if len(h.p.routeNotesOf(a)) != 0 {
		t.Errorf("notes: %v", h.p.routeNotesOf(a))
	}
	// the exit turned off: blocked, with the reason
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", plain), map[string]any{"enabled": false}, 200)
	ca = compileX(t, h, a)
	checkTags(t, ca)
	if rule := ca.rule(fmt.Sprintf("r%d", toPlain)); rule == nil || rule["outboundTag"] != "block" ||
		!strings.Contains(strings.Join(h.p.routeNotesOf(a), " "), "is turned off") {
		t.Errorf("own exit turned off: %v / %v", rule, h.p.routeNotesOf(a))
	}
}

// TestRouteChecks: what a rule may hold.
func TestRouteChecks(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "A", "address": "203.0.113.95", "protocols": []string{"vless"}}, 201)["server"].(map[string]any)["id"])
	srv := b.must("GET", fmt.Sprintf("/api/servers/%d", sid), nil, 200)["server"].(map[string]any)
	vl := id(srv["nodes"].([]any)[0].(map[string]any)["id"])
	for _, body := range []map[string]any{
		{"match": map[string]any{"countries": []string{"ZZ"}}, "target": "direct"},
		{"match": map[string]any{"sites": []string{"openai"}}, "target": "direct", "servers": []int64{sid}, "nodes": []int64{vl}},
		{"match": map[string]any{"sites": []string{strings.Repeat("a", 5000)}}, "target": "direct"},
	} {
		if code, m, _ := b.do("POST", "/api/routing/rules", body); code != 400 || len(fmt.Sprint(m["error"])) > 300 {
			t.Errorf("%.80v: %d %v", body, code, m)
		}
	}
	rules := b.must("POST", "/api/routing/rules", map[string]any{"servers": []int64{sid}, "match": map[string]any{"countries": []string{"cn"}, "network": "udp"}, "target": "block"}, 200)["rules"].([]any)
	rid := id(rules[0].(map[string]any)["id"])
	if t0 := rules[0].(map[string]any); fmt.Sprint(t0["match"]) != "map[countries:[cn] network:udp]" {
		t.Errorf("match: %v", t0["match"])
	}
	// protocols given alone replace the servers
	b.must("PATCH", fmt.Sprintf("/api/routing/rules/%d", rid), map[string]any{"nodes": []int64{vl}}, 200)
	if servers, nodes, _ := ruleScope(t, b, rid); len(servers) != 0 || !slices.Equal(nodes, []int64{vl}) {
		t.Errorf("scope: %v %v", servers, nodes)
	}
	// every rule together: the servers get them all
	domains := make([]string, 2000)
	for i := range domains {
		domains[i] = fmt.Sprintf("d%d.example.com", i)
	}
	for i := 0; i < 4; i++ {
		b.must("POST", "/api/routing/rules", map[string]any{"match": map[string]any{"domains": domains}, "target": "direct"}, 200)
	}
	if code, m, _ := b.do("POST", "/api/routing/rules", map[string]any{"match": map[string]any{"domains": domains}, "target": "direct"}); code != 400 ||
		!strings.Contains(fmt.Sprint(m["error"]), "all rules together") {
		t.Errorf("over the total: %d %v", code, m)
	}
	// rules reaching no Xray protocol, and site lists Xray refused, are problems
	hyOnly := id(b.must("POST", "/api/servers", map[string]any{"name": "H", "address": "203.0.113.96", "protocols": []string{"hysteria2"}}, 201)["server"].(map[string]any)["id"])
	b.must("POST", "/api/routing/rules", map[string]any{"servers": []int64{hyOnly}, "match": map[string]any{"sites": []string{"openai"}}, "target": "block"}, 200)
	h.p.db.Exec1(`UPDATE servers SET apply_errors = ? WHERE id = ?`,
		"the configuration was rejected by Xray: Failed to start: main: failed to load config > infra/conf: failed to load geosite: NOPE > infra/conf: list not found in geosite.dat: NOPE", sid)
	p := fmt.Sprint(b.must("GET", "/api/routing", nil, 200)["problems"])
	if !strings.Contains(p, "reaches no Xray protocol") || !strings.Contains(p, "On A: Xray refuses a site list or country name (list not found in geosite.dat: NOPE)") {
		t.Errorf("problems: %s", p)
	}
}

// TestRouteNotesOnServer: the server's page says what of the rules does not work there.
func TestRouteNotesOnServer(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "A", "address": "203.0.113.97", "protocols": []string{"vless"}}, 201)["server"].(map[string]any)["id"])
	x := id(b.must("POST", "/api/external-nodes", map[string]any{"text": extVLESS}, 200)["added"].([]any)[0].(map[string]any)["id"])
	b.must("POST", "/api/routing/rules", map[string]any{"name": "AI", "match": map[string]any{"sites": []string{"openai"}}, "target": fmt.Sprintf("ext:%d", x)}, 200)
	b.must("PATCH", fmt.Sprintf("/api/external-nodes/%d", x), map[string]any{"enabled": false}, 200)
	compileX(t, h, sid)
	srv := b.must("GET", fmt.Sprintf("/api/servers/%d", sid), nil, 200)["server"].(map[string]any)
	if notes := fmt.Sprint(srv["route_notes"]); !strings.Contains(notes, `Rule "AI" does not work: its external node (Tokyo) is turned off`) {
		t.Errorf("route notes: %s", notes)
	}
	if p := fmt.Sprint(b.must("GET", "/api/routing", nil, 200)["problems"]); !strings.Contains(p, `On A: Rule "AI" does not work`) {
		t.Errorf("problems: %s", p)
	}
	// the timeline says what turning it off meant
	var msg string
	_ = h.p.db.QueryRow(`SELECT message FROM events WHERE kind = 'ext_changed' ORDER BY id DESC LIMIT 1`).Scan(&msg)
	if msg != `owner turned the external node Tokyo off - the traffic of the rule "AI" is blocked until it is on again` {
		t.Errorf("event: %q", msg)
	}
}

// TestExternalImportLimits: names stay unique, huge or broken input gets a short answer, local
// names are refused.
func TestExternalImportLimits(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	link := func(host, name string) string {
		return "trojan://pw@" + host + ":443?security=tls#" + name
	}
	res := b.must("POST", "/api/external-nodes", map[string]any{"text": link("a.example.com", "HK") + "\n" + link("b.example.com", "HK") + "\n" +
		link("c.example.com", "hk") + "\n" + link("node.internal", "Local")}, 200)
	var names []string
	for _, a := range res["added"].([]any) {
		names = append(names, fmt.Sprint(a.(map[string]any)["name"]))
	}
	if strings.Join(names, ",") != "HK,HK 2,hk 3" {
		t.Errorf("names: %v", names)
	}
	if s := fmt.Sprint(res["skipped"]); !strings.Contains(s, "local network") {
		t.Errorf("local name: %s", s)
	}
	first := id(res["added"].([]any)[0].(map[string]any)["id"])
	second := id(res["added"].([]any)[1].(map[string]any)["id"])
	if code, _, _ := b.do("PATCH", fmt.Sprintf("/api/external-nodes/%d", second), map[string]any{"name": "hk"}); code != 409 {
		t.Errorf("rename to a taken name: %d", code)
	}
	if code, _, _ := b.do("PATCH", fmt.Sprintf("/api/external-nodes/%d", second), map[string]any{"link": link("a.example.com", "x")}); code != 409 {
		t.Errorf("a link another node has: %d", code)
	}
	b.must("PATCH", fmt.Sprintf("/api/external-nodes/%d", first), map[string]any{"name": "Hong Kong"}, 200)

	// many broken lines: a short answer
	bad := strings.Repeat("vless://broken\n", 300)
	res = b.must("POST", "/api/external-nodes", map[string]any{"text": bad}, 200)
	skipped := res["skipped"].([]any)
	if len(skipped) != maxExtSkipped+1 || !strings.Contains(fmt.Sprint(skipped[maxExtSkipped]), "100 more") {
		t.Errorf("skipped: %d entries, last %v", len(skipped), skipped[len(skipped)-1])
	}
	// more than 4 MB
	if code, m, _ := b.do("POST", "/api/external-nodes", map[string]any{"text": strings.Repeat("x", 5<<20)}); code != 413 ||
		!strings.Contains(fmt.Sprint(m["error"]), "4 MB") {
		t.Errorf("huge text: %d %v", code, m)
	}
}

// TestRoutingMCP: changing an external node needs confirm=true only when it moves or blocks traffic;
// a rule that blocks everything needs it too.
func TestRoutingMCP(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	full := b.must("POST", "/api/tokens", map[string]any{"name": "rw", "scope": "full"}, 201)["token"].(string)
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "A", "address": "203.0.113.98", "protocols": []string{"vless"}}, 201)["server"].(map[string]any)["id"])
	x := id(b.must("POST", "/api/external-nodes", map[string]any{"text": extSS}, 200)["added"].([]any)[0].(map[string]any)["id"])

	if out, isErr := callTool(t, h, full, "update_external_node", map[string]any{"node_id": x, "enabled": false}); isErr {
		t.Errorf("turning off an unused node: %s", out)
	}
	callTool(t, h, full, "update_external_node", map[string]any{"node_id": x, "enabled": true})
	if out, isErr := callTool(t, h, full, "set_traffic_rule", map[string]any{"sites": []string{"netflix"}, "target": fmt.Sprintf("ext:%d", x)}); isErr {
		t.Fatalf("set_traffic_rule: %s", out)
	}
	if out, isErr := callTool(t, h, full, "update_external_node", map[string]any{"node_id": x, "name": "Renamed"}); isErr {
		t.Errorf("rename: %s", out)
	}
	if out, isErr := callTool(t, h, full, "update_external_node", map[string]any{"node_id": x, "enabled": false}); !isErr || !strings.Contains(out, "blocks the traffic") {
		t.Errorf("turning off a used node without confirm: %v %s", isErr, out)
	}
	if out, isErr := callTool(t, h, full, "update_external_node", map[string]any{"node_id": x, "enabled": false, "confirm": true}); isErr {
		t.Errorf("confirmed: %s", out)
	}
	if out, isErr := callTool(t, h, full, "set_traffic_rule", map[string]any{"everything": true, "target": "block", "servers": []int64{sid}}); !isErr || !strings.Contains(out, "blocks all traffic") {
		t.Errorf("block everything without confirm: %v %s", isErr, out)
	}
	if out, isErr := callTool(t, h, full, "set_traffic_rule", map[string]any{"everything": true, "target": "block", "servers": []int64{sid}, "confirm": true}); isErr {
		t.Errorf("block everything, confirmed: %s", out)
	}
	if out, isErr := callTool(t, h, full, "get_routing", map[string]any{}); isErr || !strings.Contains(out, `"rules"`) {
		t.Errorf("get_routing: %s", out)
	}
}

// TestBalancerFallback: only a fastest-first load balancer has a fallback. When no member can be used
// on a server, any other one blocks the traffic rules send to it - it never leaves directly instead.
func TestBalancerFallback(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	a := id(b.must("POST", "/api/servers", map[string]any{"name": "A", "address": "203.0.113.61", "protocols": []string{"vless"}}, 201)["server"].(map[string]any)["id"])
	x := id(b.must("POST", "/api/external-nodes", map[string]any{"text": extSS}, 200)["added"].([]any)[0].(map[string]any)["id"])
	lb := id(b.must("POST", "/api/routing/balancers", map[string]any{"name": "Streaming", "strategy": "random", "fallback": "direct",
		"members": []string{fmt.Sprintf("ext:%d", x)}}, 200)["balancers"].([]any)[0].(map[string]any)["id"])
	rules := b.must("POST", "/api/routing/rules", map[string]any{"match": map[string]any{"sites": []string{"netflix"}}, "target": fmt.Sprintf("lb:%d", lb)}, 200)["rules"].([]any)
	rule := fmt.Sprintf("r%d", id(rules[0].(map[string]any)["id"]))
	if ca := compileX(t, h, a); ca.rule(rule)["balancerTag"] != fmt.Sprintf("lb-%d", lb) || ca.balancers[0]["fallbackTag"] != nil {
		t.Errorf("random: %v / %v", ca.rule(rule), ca.balancers)
	}

	b.must("PATCH", fmt.Sprintf("/api/external-nodes/%d", x), map[string]any{"enabled": false}, 200)
	ca := compileX(t, h, a)
	checkTags(t, ca)
	if r := ca.rule(rule); r == nil || r["outboundTag"] != "block" || len(ca.balancers) != 0 {
		t.Errorf("random, no member: %v", r)
	}
	if notes := strings.Join(h.p.routeNotesOf(a), " "); !strings.Contains(notes, `"Streaming" has no member it can use: the traffic rules send to it is blocked`) {
		t.Errorf("notes: %s", notes)
	}

	// fastest first: its fallback decides
	b.must("PATCH", fmt.Sprintf("/api/routing/balancers/%d", lb), map[string]any{"strategy": "leastPing"}, 200)
	ca = compileX(t, h, a)
	checkTags(t, ca)
	if r := ca.rule(rule); r == nil || r["outboundTag"] != "direct" {
		t.Errorf("fastest first, no member: %v", r)
	}
	if notes := strings.Join(h.p.routeNotesOf(a), " "); !strings.Contains(notes, "leaves directly (its fallback)") {
		t.Errorf("notes: %s", notes)
	}
	if code, _, _ := b.do("PATCH", fmt.Sprintf("/api/routing/balancers/%d", lb), map[string]any{"fallback": "nowhere"}); code != 400 {
		t.Errorf("an unknown fallback: %d", code)
	}
}

// TestNewServerRuleExits: a new server that a rule applies to gets its identity at the rule's exit.
func TestNewServerRuleExits(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	xs := id(b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "198.51.100.61", "protocols": []string{}}, 201)["server"].(map[string]any)["id"])
	exit := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", xs), map[string]any{"kind": "vless"}, 201)["id"])
	b.must("POST", "/api/routing/rules", map[string]any{"match": map[string]any{"sites": []string{"openai"}}, "target": fmt.Sprintf("node:%d", exit)}, 200)

	h.p.hub.takeDirty()
	c := id(b.must("POST", "/api/servers", map[string]any{"name": "C", "address": "203.0.113.62", "protocols": []string{"vless"}}, 201)["server"].(map[string]any)["id"])
	if dirty := h.p.hub.takeDirty(); !slices.Contains(dirty, xs) {
		t.Errorf("the exit's server was not recompiled: %v", dirty)
	}
	if cl := compileX(t, h, xs).clients[proto.InboundTag(exit)]; !slices.Contains(cl, routeEmail(c)) {
		t.Errorf("exit clients: %v", cl)
	}
	if ob := compileX(t, h, c).outbounds[fmt.Sprintf("route-node-%d", exit)]; ob == nil {
		t.Error("the new server has no way to the exit")
	}
}

// TestPassExtChainDepth: a protocol two passes deep cannot pass on to an external node - a chain has
// two passes at most, as for a protocol exit.
func TestPassExtChainDepth(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	mk := func(name, addr string) int64 {
		return id(b.must("POST", "/api/servers", map[string]any{"name": name, "address": addr, "protocols": []string{}}, 201)["server"].(map[string]any)["id"])
	}
	node := func(srv int64, body map[string]any) int64 {
		t.Helper()
		return id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", srv), body, 201)["id"])
	}
	a, bs, c := mk("A", "203.0.113.31"), mk("B", "198.51.100.31"), mk("C", "198.51.100.32")
	x := id(b.must("POST", "/api/external-nodes", map[string]any{"text": extVLESS}, 200)["added"].([]any)[0].(map[string]any)["id"])
	r := node(bs, map[string]any{"kind": "vless"})
	e := node(a, map[string]any{"kind": "vless", "pass_node": r})
	node(c, map[string]any{"kind": "trojan", "pass_node": e})
	if code, m, _ := b.do("PATCH", fmt.Sprintf("/api/nodes/%d", r), map[string]any{"pass_ext": x}); code != 400 || !strings.Contains(fmt.Sprint(m["error"]), "two passes at most") {
		t.Errorf("three passes: %d %v", code, m)
	}
	// one pass deep: it may pass on to the node
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", e), map[string]any{"pass_ext": x}, 200)
}

// TestExternalNodeUses: the list says what uses each node - protocols passing through it, rules
// sending traffic there, load balancers it is in - exactly as each node's own answer does.
func TestExternalNodeUses(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	a := id(b.must("POST", "/api/servers", map[string]any{"name": "A", "address": "203.0.113.51", "protocols": []string{}}, 201)["server"].(map[string]any)["id"])
	added := b.must("POST", "/api/external-nodes", map[string]any{"text": extVLESS + "\n" + extSS + "\n" + extHy2}, 200)["added"].([]any)
	x1, x2 := id(added[0].(map[string]any)["id"]), id(added[1].(map[string]any)["id"])
	b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", a), map[string]any{"kind": "vless", "pass_ext": x1}, 201)
	b.must("POST", "/api/routing/rules", map[string]any{"name": "AI", "match": map[string]any{"sites": []string{"openai"}}, "target": fmt.Sprintf("ext:%d", x1)}, 200)
	b.must("POST", "/api/routing/balancers", map[string]any{"name": "Both", "members": []string{fmt.Sprintf("ext:%d", x1), fmt.Sprintf("ext:%d", x2)}}, 200)

	_, _, raw := b.do("GET", "/api/external-nodes", nil)
	var list []map[string]any
	if err := json.Unmarshal(raw, &list); err != nil || len(list) != 3 {
		t.Fatalf("list: %v %s", err, raw)
	}
	for _, m := range list {
		x, _ := h.p.extByID(t.Context(), id(m["id"]))
		var want any // as the list's JSON reads
		j, _ := json.Marshal(h.p.exitUses(t.Context(), x.AccountID, "ext", x.ID))
		_ = json.Unmarshal(j, &want)
		if got := fmt.Sprint(m["used_by"]); got != fmt.Sprint(want) {
			t.Errorf("%s: the list says %s, the node %v", x.Name, got, want)
		}
	}
	if uses := string(raw); !strings.Contains(uses, `"type":"protocol"`) || !strings.Contains(uses, `"type":"rule"`) ||
		!strings.Contains(uses, `"type":"balancer"`) || !strings.Contains(uses, `"used_by":[]`) {
		t.Errorf("uses: %s", uses)
	}
}
