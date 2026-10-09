package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"meridian/internal/proto"
)

const (
	extVLESS = "vless://0b1f6e2a-7d3c-4e5f-9a8b-1c2d3e4f5a6b@jp.example.com:443?security=reality&sni=www.microsoft.com&pbk=Ugl3PzgaYPjwOw8wtbGCsLXQvhEyVsmKq0Ox9zAcRDU&sid=ab&type=tcp&flow=xtls-rprx-vision#Tokyo"
	extSS    = "ss://2022-blake3-aes-128-gcm:AAECAwQFBgcICQoLDA0ODw==@ss.example.com:8443#SS 2022"
	extHy2   = "hysteria2://pw@hy.example.com:443/?sni=hy.example.com&obfs=salamander&obfs-password=x#Hy2"
)

// compiledXray is a server's compiled Xray: outbounds by tag, rules in order, balancers, observatory,
// and each inbound's client emails.
type compiledXray struct {
	raw       string
	outbounds map[string]map[string]any
	rules     []map[string]any
	balancers []map[string]any
	observe   bool
	clients   map[string][]string
}

func compileX(t *testing.T, h *harness, srv int64) compiledXray {
	t.Helper()
	st, err := h.p.compileServer(context.Background(), srv)
	if err != nil {
		t.Fatal(err)
	}
	var base struct {
		Outbounds []map[string]any `json:"outbounds"`
		Routing   struct {
			Rules     []map[string]any `json:"rules"`
			Balancers []map[string]any `json:"balancers"`
		} `json:"routing"`
		Observatory map[string]any `json:"observatory"`
	}
	if err := json.Unmarshal(st.Xray.Base, &base); err != nil {
		t.Fatal(err)
	}
	c := compiledXray{raw: string(st.Xray.Base), outbounds: map[string]map[string]any{}, rules: base.Routing.Rules,
		balancers: base.Routing.Balancers, observe: base.Observatory != nil, clients: map[string][]string{}}
	for _, o := range base.Outbounds {
		c.outbounds[fmt.Sprint(o["tag"])] = o
	}
	for _, in := range st.Xray.Inbounds {
		for _, cl := range in.Clients {
			c.clients[in.Tag] = append(c.clients[in.Tag], cl.Email)
		}
	}
	return c
}

func (c compiledXray) rule(tag string) map[string]any {
	for _, r := range c.rules {
		if r["ruleTag"] == tag {
			return r
		}
	}
	return nil
}

func (c compiledXray) ruleIndex(tag string) int {
	return slices.IndexFunc(c.rules, func(r map[string]any) bool { return r["ruleTag"] == tag })
}

func TestExternalNodes(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "A", "address": "203.0.113.71"}, 201)["server"].(map[string]any)["id"])
	entry := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": "vless"}, 201)["id"])
	hy := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": "hysteria2"}, 201)["id"])
	b.must("POST", "/api/users", map[string]any{"name": "eve"}, 201)

	// import: links, with the ones that cannot be used and why
	res := b.must("POST", "/api/external-nodes", map[string]any{"text": strings.Join([]string{extVLESS, extSS, extHy2,
		"tuic://x@t.example.com:443#Tuic", "vless://0b1f6e2a-7d3c-4e5f-9a8b-1c2d3e4f5a6b@x.example.com:443?security=tls&allowInsecure=1#Bad"}, "\n")}, 200)
	added, skipped := res["added"].([]any), res["skipped"].([]any)
	if len(added) != 3 || len(skipped) != 2 {
		t.Fatalf("import: %v", res)
	}
	ext := map[string]int64{}
	for _, a := range added {
		m := a.(map[string]any)
		ext[fmt.Sprint(m["name"])] = id(m["id"])
		if m["password"] != nil || m["endpoint"] != nil || m["uuid"] != nil {
			t.Errorf("credentials shown: %v", m)
		}
	}
	if l := added[1].(map[string]any)["label"]; l != "Shadowsocks 2022" {
		t.Errorf("label: %v", l)
	}
	// the same links again are skipped
	if res := b.must("POST", "/api/external-nodes", map[string]any{"text": extVLESS}, 200); len(res["added"].([]any)) != 0 ||
		!strings.Contains(fmt.Sprint(res["skipped"]), "already imported as Tokyo") {
		t.Errorf("duplicate: %v", res)
	}
	// subscription addresses: https only, public only
	for _, u := range []string{"http://sub.example.com/x", "https://127.0.0.1/x", "https://10.0.0.1/sub"} {
		if code, m, _ := b.do("POST", "/api/external-nodes", map[string]any{"url": u}); code != 400 {
			t.Errorf("%s: %d %v", u, code, m)
		}
	}

	// a protocol passes through an external node
	path := fmt.Sprintf("/api/nodes/%d", entry)
	if code, m, _ := b.do("PATCH", path, map[string]any{"pass_ext": 999}); code != 400 {
		t.Errorf("unknown node: %d %v", code, m)
	}
	if code, m, _ := b.do("PATCH", fmt.Sprintf("/api/nodes/%d", hy), map[string]any{"pass_ext": ext["Tokyo"]}); code != 400 ||
		!strings.Contains(fmt.Sprint(m["error"]), "Xray protocol") {
		t.Errorf("Hysteria2 entry: %d %v", code, m)
	}
	b.must("PATCH", path, map[string]any{"pass_ext": ext["Tokyo"]}, 200)
	c := compileX(t, h, sid)
	ob := c.outbounds[passTag(entry)]
	if ob == nil || ob["protocol"] != "vless" || !strings.Contains(fmt.Sprint(ob), "jp.example.com") || !strings.Contains(fmt.Sprint(ob), "reality") {
		t.Fatalf("pass outbound: %v", ob)
	}
	if r := c.rule(passTag(entry)); r == nil || r["outboundTag"] != passTag(entry) {
		t.Errorf("pass rule: %v", r)
	}
	// off: blocked, never direct; the protocol says why
	b.must("PATCH", fmt.Sprintf("/api/external-nodes/%d", ext["Tokyo"]), map[string]any{"enabled": false}, 200)
	c = compileX(t, h, sid)
	if r := c.rule(passTag(entry)); r == nil || r["outboundTag"] != "block" || c.outbounds[passTag(entry)] != nil {
		t.Errorf("turned off: %v", r)
	}
	srv := b.must("GET", fmt.Sprintf("/api/servers/%d", sid), nil, 200)["server"].(map[string]any)
	nv := srv["nodes"].([]any)[0].(map[string]any)
	if !strings.Contains(fmt.Sprint(nv["pass_broken"]), "turned off") || !strings.Contains(fmt.Sprint(nv["pass_name"]), "External · Tokyo") {
		t.Errorf("view: %v / %v", nv["pass_name"], nv["pass_broken"])
	}
	b.must("PATCH", fmt.Sprintf("/api/external-nodes/%d", ext["Tokyo"]), map[string]any{"enabled": true, "name": "Tokyo 2"}, 200)
	// either a protocol or an external node
	if code, m, _ := b.do("PATCH", path, map[string]any{"pass_node": 12345, "pass_ext": ext["Tokyo"]}); code != 400 {
		t.Errorf("both: %d %v", code, m)
	}
	// what uses it, and removing it blocks those
	if _, _, raw := b.do("GET", "/api/external-nodes", nil); !strings.Contains(string(raw), `"type":"protocol"`) || !strings.Contains(string(raw), "A · ") {
		t.Errorf("used by: %s", raw)
	}
	gone := b.must("DELETE", fmt.Sprintf("/api/external-nodes/%d", ext["Tokyo"]), nil, 200)
	if len(gone["blocked"].([]any)) != 1 {
		t.Errorf("removed: %v", gone)
	}
	if r := compileX(t, h, sid).rule(passTag(entry)); r == nil || r["outboundTag"] != "block" {
		t.Errorf("after removal: %v", r)
	}
}

func TestTrafficRules(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	mk := func(name, addr string) int64 {
		return id(b.must("POST", "/api/servers", map[string]any{"name": name, "address": addr}, 201)["server"].(map[string]any)["id"])
	}
	a, bs, c := mk("A", "203.0.113.71"), mk("B", "198.51.100.71"), mk("C", "198.51.100.72")
	node := func(srv int64, body map[string]any) int64 {
		t.Helper()
		return id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", srv), body, 201)["id"])
	}
	na := node(a, map[string]any{"kind": "vless"})
	na2 := node(a, map[string]any{"kind": "trojan"})
	nb := node(bs, map[string]any{"kind": "vless"})
	nc := node(c, map[string]any{"kind": "shadowsocks"})
	// C passes through B: its pass user is on B
	entryC := node(c, map[string]any{"kind": "vmess", "pass_node": nb})
	b.must("POST", "/api/users", map[string]any{"name": "eve"}, 201)
	x := id(b.must("POST", "/api/external-nodes", map[string]any{"text": extSS}, 200)["added"].([]any)[0].(map[string]any)["id"])

	// a server without rules keeps exactly the configuration it had
	before := compileX(t, h, a).raw
	if strings.Contains(before, "balancers") || strings.Contains(before, "observatory") {
		t.Fatalf("without rules: %s", before)
	}

	// refusals
	bad := []map[string]any{
		{"match": map[string]any{}, "target": "direct"},
		{"match": map[string]any{"domains": []string{"exa mple.com"}}, "target": "direct"},
		{"match": map[string]any{"countries": []string{"private"}}, "target": "direct"},
		{"match": map[string]any{"countries": []string{"CHN"}}, "target": "direct"},
		{"match": map[string]any{"ips": []string{"300.1.1.1"}}, "target": "direct"},
		{"match": map[string]any{"ports": "0-80"}, "target": "direct"},
		{"match": map[string]any{"all": true, "ports": "443"}, "target": "direct"},
		{"match": map[string]any{"domains": []string{"regexp:("}}, "target": "direct"},
		{"match": map[string]any{"sites": []string{"netflix"}}, "target": "node:99999"},
		{"match": map[string]any{"sites": []string{"netflix"}}, "target": "somewhere"},
		{"match": map[string]any{"sites": []string{"netflix"}}},
		{"match": map[string]any{"sites": []string{"netflix"}}, "target": "direct", "nodes": []int64{99999}},
	}
	for _, body := range bad {
		if code, m, _ := b.do("POST", "/api/routing/rules", body); code != 400 {
			t.Errorf("%v: %d %v", body, code, m)
		}
	}

	// sites and domains through the external node; China directly; ads blocked - on every server
	b.must("POST", "/api/routing/rules", map[string]any{"name": "AI", "match": map[string]any{"sites": []string{"openai", "geosite:anthropic"},
		"domains": []string{"claude.ai", "full:chatgpt.com"}}, "target": fmt.Sprintf("ext:%d", x)}, 200)
	b.must("POST", "/api/routing/rules", map[string]any{"match": map[string]any{"sites": []string{"cn"}, "countries": []string{"CN"}}, "target": "direct"}, 200)
	view := b.must("POST", "/api/routing/rules", map[string]any{"match": map[string]any{"sites": []string{"category-ads-all"}},
		"target": "block", "before": 0}, 200)
	rules := view["rules"].([]any)
	if len(rules) != 3 {
		t.Fatalf("rules: %v", rules)
	}
	r1 := id(rules[0].(map[string]any)["id"])
	r2 := id(rules[1].(map[string]any)["id"])
	r3 := id(rules[2].(map[string]any)["id"])

	ca := compileX(t, h, a)
	rule := ca.rule(fmt.Sprintf("r%d", r1))
	tags := fmt.Sprint(rule["inboundTag"])
	if rule["outboundTag"] != fmt.Sprintf("route-ext-%d", x) || !strings.Contains(tags, proto.InboundTag(na)) || !strings.Contains(tags, proto.InboundTag(na2)) ||
		fmt.Sprint(rule["domain"]) != "[geosite:openai geosite:anthropic domain:claude.ai full:chatgpt.com]" {
		t.Errorf("AI rule: %v", rule)
	}
	if ob := ca.outbounds[fmt.Sprintf("route-ext-%d", x)]; ob == nil || ob["protocol"] != "shadowsocks" {
		t.Errorf("external outbound: %v", ob)
	}
	// a name and an address are matched apart
	if d, i := ca.rule(fmt.Sprintf("r%dd", r2)), ca.rule(fmt.Sprintf("r%di", r2)); fmt.Sprint(d["domain"]) != "[geosite:cn]" ||
		fmt.Sprint(i["ip"]) != "[geoip:cn]" || d["outboundTag"] != "direct" {
		t.Errorf("CN rules: %v / %v", d, i)
	}
	if r := ca.rule(fmt.Sprintf("r%d", r3)); r["outboundTag"] != "block" {
		t.Errorf("ads: %v", r)
	}
	// order: the private-address guard first, then the rules in their order, then the protocols' own way
	if ca.ruleIndex("no-private") != 0 || ca.ruleIndex(fmt.Sprintf("r%d", r1)) > ca.ruleIndex(fmt.Sprintf("r%dd", r2)) {
		t.Errorf("order: %v", ca.rules)
	}

	// B: C's pass users skip B's rules - their traffic leaves as it always did
	cb := compileX(t, h, bs)
	pass := cb.rule(fmt.Sprintf("passers-n%d", nb))
	if pass == nil || fmt.Sprint(pass["user"]) != fmt.Sprintf("[%s]", passEmail(entryC)) || pass["outboundTag"] != "direct" ||
		cb.ruleIndex(fmt.Sprintf("passers-n%d", nb)) > cb.ruleIndex(fmt.Sprintf("r%d", r1)) {
		t.Errorf("pass users on B: %v (rules %v)", pass, cb.rules)
	}

	// a rule for one protocol, to a protocol on another server: that server gets A's identity
	b.must("PATCH", fmt.Sprintf("/api/routing/rules/%d", r1), map[string]any{"nodes": []int64{na}, "target": fmt.Sprintf("node:%d", nc)}, 200)
	ca = compileX(t, h, a)
	rule = ca.rule(fmt.Sprintf("r%d", r1))
	if fmt.Sprint(rule["inboundTag"]) != fmt.Sprintf("[%s]", proto.InboundTag(na)) || rule["outboundTag"] != fmt.Sprintf("route-node-%d", nc) {
		t.Errorf("to a protocol: %v", rule)
	}
	if ob := ca.outbounds[fmt.Sprintf("route-node-%d", nc)]; ob == nil || ob["protocol"] != "shadowsocks" || !strings.Contains(fmt.Sprint(ob), "198.51.100.72") {
		t.Errorf("route outbound: %v", ob)
	}
	cc := compileX(t, h, c)
	if !slices.Contains(cc.clients[proto.InboundTag(nc)], routeEmail(a)) || slices.Contains(cc.clients[proto.InboundTag(nc)], routeEmail(bs)) {
		t.Errorf("exit clients on C: %v", cc.clients[proto.InboundTag(nc)])
	}
	// to a protocol on the server itself: there the traffic leaves the way that protocol's own does
	b.must("PATCH", fmt.Sprintf("/api/routing/rules/%d", r1), map[string]any{"nodes": []int64{}, "servers": []int64{a, c}}, 200)
	cc = compileX(t, h, c)
	own := fmt.Sprintf("route-node-%d", nc)
	if r := cc.rule(fmt.Sprintf("r%d", r1)); r == nil || r["outboundTag"] != own || cc.outbounds[own]["protocol"] != "freedom" {
		t.Errorf("to itself: %v / %v", r, cc.outbounds[own])
	}
	if v := b.must("GET", "/api/routing", nil, 200); len(v["problems"].([]any)) != 0 {
		t.Errorf("problems: %v", v["problems"])
	}

	// a load balancer, fastest first: members as outbounds, latency checks on
	if code, m, _ := b.do("POST", "/api/routing/balancers", map[string]any{"name": "LB", "members": []string{"block"}}); code != 400 {
		t.Errorf("block member: %d %v", code, m)
	}
	view = b.must("POST", "/api/routing/balancers", map[string]any{"name": "LB", "strategy": "leastPing",
		"members": []string{fmt.Sprintf("node:%d", nb), fmt.Sprintf("ext:%d", x), "direct"}}, 200)
	lb := id(view["balancers"].([]any)[0].(map[string]any)["id"])
	b.must("PATCH", fmt.Sprintf("/api/routing/rules/%d", r1), map[string]any{"servers": []int64{}, "target": fmt.Sprintf("lb:%d", lb)}, 200)
	ca = compileX(t, h, a)
	if !ca.observe || len(ca.balancers) != 1 || fmt.Sprint(ca.balancers[0]["selector"]) != fmt.Sprintf("[lb%d.]", lb) ||
		ca.rule(fmt.Sprintf("r%d", r1))["balancerTag"] != fmt.Sprintf("lb-%d", lb) {
		t.Errorf("balancer: %v / %v", ca.balancers, ca.rule(fmt.Sprintf("r%d", r1)))
	}
	for _, m := range []string{fmt.Sprintf("lb%d.node%d", lb, nb), fmt.Sprintf("lb%d.ext%d", lb, x), fmt.Sprintf("lb%d.direct", lb)} {
		if ca.outbounds[m] == nil {
			t.Errorf("member %s missing: %v", m, ca.outbounds)
		}
	}
	if ca.balancers[0]["fallbackTag"] != "block" {
		t.Errorf("fastest first has its fallback: %v", ca.balancers[0])
	}
	// taking turns needs no latency checks and has no fallback (Xray refuses one without the checks)
	b.must("PATCH", fmt.Sprintf("/api/routing/balancers/%d", lb), map[string]any{"strategy": "roundRobin"}, 200)
	if rr := compileX(t, h, a); rr.observe || len(rr.balancers) != 1 || rr.balancers[0]["fallbackTag"] != nil ||
		fmt.Sprint(rr.balancers[0]["strategy"]) != "map[type:roundRobin]" {
		t.Errorf("round robin: %v (observatory %v)", rr.balancers, rr.observe)
	}
	b.must("PATCH", fmt.Sprintf("/api/routing/balancers/%d", lb), map[string]any{"strategy": "leastPing"}, 200)
	// on B its own member leaves the way that protocol's traffic does (directly); the others balance as everywhere
	cb = compileX(t, h, bs)
	if m := cb.outbounds[fmt.Sprintf("lb%d.node%d", lb, nb)]; m == nil || m["protocol"] != "freedom" || cb.outbounds[fmt.Sprintf("lb%d.ext%d", lb, x)] == nil {
		t.Errorf("B's members: %v", cb.outbounds)
	}
	// a balancer a rule uses cannot go; removing the rule first lets it
	if code, _, _ := b.do("DELETE", fmt.Sprintf("/api/routing/balancers/%d", lb), nil); code != 409 {
		t.Errorf("balancer in use removed: %d", code)
	}
	b.must("PUT", "/api/routing/order", map[string]any{"ids": []int64{r2, r3, r1}}, 200)
	if ca = compileX(t, h, a); ca.ruleIndex(fmt.Sprintf("r%dd", r2)) > ca.ruleIndex(fmt.Sprintf("r%d", r3)) {
		t.Errorf("new order: %v", ca.rules)
	}
	for _, r := range []int64{r1, r2, r3} {
		b.must("DELETE", fmt.Sprintf("/api/routing/rules/%d", r), nil, 200)
	}
	b.must("DELETE", fmt.Sprintf("/api/routing/balancers/%d", lb), nil, 200)
	if after := compileX(t, h, a).raw; after != before {
		t.Errorf("rules gone, configuration differs:\n%s\n%s", before, after)
	}
	// a read-only token cannot change rules
	tok := b.must("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"}, 201)["token"].(string)
	if code, _, _ := h.bearer(tok).do("POST", "/api/routing/rules", map[string]any{"match": map[string]any{"all": true}, "target": "block"}); code != 403 {
		t.Errorf("read-only token: %d", code)
	}
}
