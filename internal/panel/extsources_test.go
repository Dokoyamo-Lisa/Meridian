package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"meridian/internal/subgen"
)

// fakeProvider stands in for a provider's subscription address.
type fakeProvider struct {
	mu    sync.Mutex
	text  string
	usage *sourceUsage
	err   error
	asked []string // the apps it was asked as
}

func (f *fakeProvider) set(text string) {
	f.mu.Lock()
	f.text, f.err = text, nil
	f.mu.Unlock()
}

func useProvider(t *testing.T) *fakeProvider {
	f := &fakeProvider{}
	old := fetchSource
	fetchSource = func(ctx context.Context, rawURL, client string) (string, *sourceUsage, error) {
		if _, err := checkFetchURL(rawURL); err != nil {
			return "", nil, err
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.asked = append(f.asked, client)
		return f.text, f.usage, f.err
	}
	t.Cleanup(func() { fetchSource = old })
	return f
}

func ssLink(host, name string) string {
	return fmt.Sprintf("ss://2022-blake3-aes-128-gcm:AAECAwQFBgcICQoLDA0ODw==@%s:8443#%s", host, name)
}

// TestSubscriptionLinks: a provider's subscription read on a schedule keeps its nodes in line -
// new, changed, renamed and gone - without losing what uses them; a failed read changes nothing.
func TestSubscriptionLinks(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	f := useProvider(t)
	f.usage = &sourceUsage{Upload: 1 << 30, Download: 3 << 30, Total: 100 << 30, Expire: 2000000000}
	f.set(ssLink("hk1.example.com", "HK 01") + "\n" + ssLink("jp1.example.com", "JP 01") + "\n" + ssLink("sg1.example.com", "SG 01") + "\n" +
		extHy2 + "\nvless://bad\n" + ssLink("us1.example.com", "Remaining 50 GB"))

	for _, bad := range []map[string]any{
		{"name": "P", "url": "http://provider.example.com/sub"}, {"name": "P", "url": "https://127.0.0.1/sub"},
		{"name": "", "url": "https://provider.example.com/sub"}, {"name": "P", "url": "https://provider.example.com/sub", "every_hours": 500},
		{"name": "P", "url": "https://provider.example.com/sub", "client": "netscape"},
	} {
		if code, _, raw := b.do("POST", "/api/external-sources", bad); code != 400 {
			t.Errorf("%v: %d %s", bad, code, raw)
		}
	}
	read := b.must("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"}, 201)["token"].(string)
	if code, _, _ := h.bearer(read).do("POST", "/api/external-sources", map[string]any{"name": "P", "url": "https://provider.example.com/sub"}); code != http.StatusForbidden {
		t.Errorf("a read-only token added a subscription link: %d", code)
	}

	v := b.must("POST", "/api/external-sources", map[string]any{"name": "Provider", "url": "https://provider.example.com/sub?token=abc",
		"prefix": "P ·", "exclude": "remaining", "client": "clash"}, 201)
	src := v["source"].(map[string]any)
	sid := id(src["id"])
	if res := v["result"].(map[string]any); id(res["added"]) != 4 || len(res["skipped"].([]any)) != 1 || v["error"] != "" {
		t.Fatalf("first read: %v", v)
	}
	if id(src["nodes"]) != 4 || src["usage"].(map[string]any)["total"].(float64) != 100<<30 || id(src["next_at"]) == 0 {
		t.Errorf("source: %v", src)
	}
	if f.asked[0] != "clash" {
		t.Errorf("asked as %v", f.asked)
	}
	nodes := func() map[string]map[string]any {
		out := map[string]map[string]any{}
		var list []map[string]any
		_, _, raw := b.do("GET", "/api/external-nodes", nil)
		_ = json.Unmarshal(raw, &list)
		for _, x := range list {
			out[x["name"].(string)] = x
		}
		return out
	}
	got := nodes()
	for _, n := range []string{"P · HK 01", "P · JP 01", "P · SG 01", "P · Hy2"} {
		if got[n] == nil || id(got[n]["source_id"]) != sid {
			t.Errorf("node %q: %v", n, got[n])
		}
	}
	hk, jp, sg := id(got["P · HK 01"]["id"]), id(got["P · JP 01"]["id"]), id(got["P · SG 01"]["id"])

	// a node of a subscription follows the provider: not renamed, given a link or removed by hand
	if code, _, _ := b.do("PATCH", fmt.Sprintf("/api/external-nodes/%d", hk), map[string]any{"name": "Mine"}); code != 409 {
		t.Errorf("renamed by hand: %d", code)
	}
	if code, _, _ := b.do("DELETE", fmt.Sprintf("/api/external-nodes/%d", hk), nil); code != 409 {
		t.Errorf("removed by hand: %d", code)
	}
	b.must("PATCH", fmt.Sprintf("/api/external-nodes/%d", sg), map[string]any{"enabled": false, "note": "slow"}, 200) // these are its own

	// a rule uses JP; a load balancer uses all of them
	srv := id(b.must("POST", "/api/servers", map[string]any{"name": "A", "address": "203.0.113.90", "protocols": []string{"vless"}}, 201)["server"].(map[string]any)["id"])
	b.must("POST", "/api/routing/rules", map[string]any{"match": map[string]any{"sites": []string{"netflix"}}, "target": fmt.Sprintf("ext:%d", jp)}, 200)
	if code, _, _ := b.do("POST", "/api/routing/rules", map[string]any{"match": map[string]any{"sites": []string{"openai"}}, "target": fmt.Sprintf("src:%d", sid)}); code != 400 {
		t.Errorf("a rule sent straight to a subscription link: %d", code)
	}
	lb := id(b.must("POST", "/api/routing/balancers", map[string]any{"name": "Provider", "members": []string{fmt.Sprintf("src:%d", sid)}}, 200)["balancers"].([]any)[0].(map[string]any)["id"])
	b.must("POST", "/api/routing/rules", map[string]any{"match": map[string]any{"sites": []string{"openai"}}, "target": fmt.Sprintf("lb:%d", lb)}, 200)
	c := compileX(t, h, srv)
	for _, x := range []int64{hk, jp} {
		if c.outbounds[fmt.Sprintf("lb%d.ext%d", lb, x)] == nil {
			t.Errorf("the load balancer lacks node %d: %v", x, c.raw)
		}
	}
	if c.outbounds[fmt.Sprintf("lb%d.ext%d", lb, sg)] != nil {
		t.Error("a node turned off is in the load balancer")
	}
	routing := b.must("GET", "/api/routing", nil, 200)
	if s := fmt.Sprint(routing["sources"]); !strings.Contains(s, "Subscription · Provider (3 nodes)") {
		t.Errorf("sources: %s", s)
	}

	// the provider changes: HK gets a new address, SG is renamed, JP leaves (a rule names it), the
	// Hysteria2 node leaves (nothing names it), TW is new
	f.set(ssLink("hk2.example.com", "HK 01") + "\n" + ssLink("sg1.example.com", "Singapore 01") + "\n" + ssLink("tw1.example.com", "TW 01"))
	h.p.hub.takeDirty()
	v = b.must("POST", fmt.Sprintf("/api/external-sources/%d/refresh", sid), nil, 200)
	res := v["result"].(map[string]any)
	if id(res["added"]) != 1 || id(res["changed"]) != 1 || id(res["removed"]) != 1 || fmt.Sprint(res["kept"]) != "[P · JP 01]" {
		t.Errorf("refresh: %v", res)
	}
	got = nodes()
	if id(got["P · HK 01"]["id"]) != hk || got["P · HK 01"]["host"] != "hk2.example.com" {
		t.Errorf("HK changed in place: %v", got["P · HK 01"])
	}
	if x := got["P · Singapore 01"]; x == nil || id(x["id"]) != sg || x["enabled"] != false || x["note"] != "slow" {
		t.Errorf("SG renamed in place, keeping what was its own: %v", x)
	}
	if x := got["P · JP 01"]; x == nil || id(x["missing_since"]) == 0 {
		t.Errorf("JP kept while a rule names it: %v", x)
	}
	if got["P · Hy2"] != nil || got["P · TW 01"] == nil {
		t.Errorf("nodes: %v", got)
	}
	if dirty := h.p.hub.takeDirty(); !slices.Contains(dirty, srv) {
		t.Errorf("servers recompiled: %v", dirty)
	}
	c = compileX(t, h, srv)
	if c.outbounds[fmt.Sprintf("lb%d.ext%d", lb, jp)] != nil {
		t.Error("a node gone from its subscription is still in the load balancer")
	}

	// a failed read, or one with nothing usable, changes nothing - and says so once
	f.mu.Lock()
	f.err = errStatus(http.StatusBadGateway, "the address answered 503 - check it in a browser")
	f.mu.Unlock()
	v = b.must("POST", fmt.Sprintf("/api/external-sources/%d/refresh", sid), nil, 200)
	if !strings.Contains(fmt.Sprint(v["error"]), "503") || len(nodes()) != 4 {
		t.Errorf("failed read: %v", v)
	}
	f.set("<html>Service unavailable</html>")
	v = b.must("POST", fmt.Sprintf("/api/external-sources/%d/refresh", sid), nil, 200)
	if !strings.Contains(fmt.Sprint(v["error"]), "no node Meridian can use") || len(nodes()) != 4 {
		t.Errorf("an error page: %v", v)
	}
	var failed int
	h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind = 'ext_source_failed'`).Scan(&failed)
	if failed != 1 {
		t.Errorf("failure told %d times", failed)
	}
	f.set(ssLink("hk2.example.com", "HK 01") + "\n" + ssLink("sg1.example.com", "Singapore 01") + "\n" + ssLink("tw1.example.com", "TW 01"))
	v = b.must("POST", fmt.Sprintf("/api/external-sources/%d/refresh", sid), nil, 200)
	if v["error"] != "" || v["source"].(map[string]any)["error"] != "" {
		t.Errorf("back: %v", v)
	}

	// filters and the prefix: changing them reads it again at once
	b.must("PATCH", fmt.Sprintf("/api/external-sources/%d", sid), map[string]any{"include": "hk, tw", "prefix": "Q"}, 200)
	got = nodes()
	if got["Q HK 01"] == nil || got["Q TW 01"] == nil || got["P · Singapore 01"] != nil || id(got["Q HK 01"]["id"]) != hk {
		t.Errorf("after new filters: %v", got)
	}
	if code, _, _ := b.do("PATCH", fmt.Sprintf("/api/external-sources/%d", sid), map[string]any{"include": "nothing-matches"}); code != 200 {
		t.Errorf("a filter nothing passes: %d", code)
	}
	if len(nodes()) != 3 { // HK, TW and the kept JP: a filter nothing passes is a failed read
		t.Errorf("a filter nothing passes emptied it: %v", nodes())
	}
	b.must("PATCH", fmt.Sprintf("/api/external-sources/%d", sid), map[string]any{"include": ""}, 200)

	// turned off: what uses its nodes is blocked
	b.must("PATCH", fmt.Sprintf("/api/external-sources/%d", sid), map[string]any{"enabled": false}, 200)
	c = compileX(t, h, srv)
	if c.outbounds[fmt.Sprintf("lb%d.ext%d", lb, hk)] != nil || !strings.Contains(fmt.Sprint(b.must("GET", "/api/routing", nil, 200)["problems"]), "turned off") {
		t.Errorf("a subscription link turned off: %v", c.raw)
	}
	b.must("PATCH", fmt.Sprintf("/api/external-sources/%d", sid), map[string]any{"enabled": true}, 200)

	// removed, keeping its nodes: they stay as imported nodes, and the load balancer loses the member
	gone := b.must("DELETE", fmt.Sprintf("/api/external-sources/%d?keep_nodes=1", sid), nil, 200)
	if len(gone["blocked"].([]any)) != 0 {
		t.Errorf("removed with its nodes kept: %v", gone)
	}
	for name, x := range nodes() {
		if id(x["source_id"]) != 0 || id(x["missing_since"]) != 0 {
			t.Errorf("%s still follows a removed link: %v", name, x)
		}
	}
	bals := b.must("GET", "/api/routing", nil, 200)["balancers"].([]any)
	if m := bals[0].(map[string]any)["members"].([]any); len(m) != 0 {
		t.Errorf("the load balancer kept the removed link: %v", m)
	}
}

// TestSubscriptionLinkOffered: users get a subscription link's nodes when the operator says so -
// every user, chosen users or the users on chosen plans - written for each app, never WireGuard.
func TestSubscriptionLinkOffered(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	f := useProvider(t)
	wg := "wireguard://YNXtAzepDqRv9H52osJVDQnznT5AL11ePlVbgHnU0Ws%3D@wg.example.com:51820?publickey=Gy1xgoD3JJt5cz7S1cwVFUeA4H8jhy3d7ylxBG%2BqyBE%3D&address=10.0.0.2%2F32#WG"
	f.set(ssLink("hk1.example.com", "HK 01") + "\n" + extVLESS + "\n" + wg)
	b.must("POST", "/api/servers", map[string]any{"name": "A", "address": "203.0.113.91", "protocols": []string{"vless"}}, 201)
	mkUser := func(body map[string]any) *Sub {
		t.Helper()
		_, _, raw := b.do("POST", "/api/users", body)
		var made []map[string]any
		if json.Unmarshal(raw, &made) != nil || len(made) != 1 {
			t.Fatalf("user: %s", raw)
		}
		s, err := h.p.subByID(context.Background(), id(made[0]["id"]))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	plan := id(b.must("POST", "/api/plans", map[string]any{"name": "Gold"}, 201)["id"])
	alice, bob, carol := mkUser(map[string]any{"name": "alice"}), mkUser(map[string]any{"name": "bob"}), mkUser(map[string]any{"name": "carol", "plan_id": plan})
	sid := id(b.must("POST", "/api/external-sources", map[string]any{"name": "Provider", "url": "https://provider.example.com/sub", "prefix": "P"}, 201)["source"].(map[string]any)["id"])

	names := func(s *Sub) []string {
		t.Helper()
		s, _ = h.p.subByID(context.Background(), s.ID)
		eps, err := h.p.endpointsFor(context.Background(), s)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range eps {
			out = append(out, e.Name)
		}
		return out
	}
	if n := names(alice); slices.Contains(n, "P HK 01") {
		t.Errorf("nodes given out before the operator said so: %v", n)
	}
	b.must("PATCH", fmt.Sprintf("/api/external-sources/%d", sid), map[string]any{"offer": true}, 200)
	for _, s := range []*Sub{alice, bob, carol} {
		n := names(s)
		if !slices.Contains(n, "P HK 01") || !slices.Contains(n, "P Tokyo") || slices.Contains(n, "P WG") {
			t.Errorf("%s gets %v", s.Name, n)
		}
	}
	b.must("PATCH", fmt.Sprintf("/api/external-sources/%d", sid), map[string]any{"offer_to": map[string]any{"users": []int64{bob.ID}, "plans": []int64{plan}}}, 200)
	if n := names(alice); slices.Contains(n, "P HK 01") {
		t.Errorf("alice is not chosen but gets %v", n)
	}
	if !slices.Contains(names(bob), "P HK 01") || !slices.Contains(names(carol), "P HK 01") {
		t.Error("bob (chosen) or carol (on the plan) lack the nodes")
	}
	if code, _, _ := b.do("PATCH", fmt.Sprintf("/api/external-sources/%d", sid), map[string]any{"offer_to": map[string]any{"users": []int64{99999}}}); code != 400 {
		t.Errorf("an unknown user: %d", code)
	}
	// written for the app that asks, like the panel's own protocols
	bob, _ = h.p.subByID(context.Background(), bob.ID)
	req, _ := http.NewRequest("GET", "/s/"+bob.Token+"?client=clash", nil)
	body, _, _, err := h.p.renderSub(req, bob, subgen.FormatClash)
	if err != nil || !strings.Contains(string(body), "P HK 01") || !strings.Contains(string(body), "hk1.example.com") {
		t.Errorf("clash: %v\n%s", err, body)
	}
	// a node turned off, or the link turned off, leaves users' subscriptions
	got := map[string]int64{}
	var list []map[string]any
	_, _, raw := b.do("GET", "/api/external-nodes", nil)
	_ = json.Unmarshal(raw, &list)
	for _, x := range list {
		got[x["name"].(string)] = id(x["id"])
	}
	b.must("PATCH", fmt.Sprintf("/api/external-nodes/%d", got["P HK 01"]), map[string]any{"enabled": false}, 200)
	if n := names(bob); slices.Contains(n, "P HK 01") || !slices.Contains(n, "P Tokyo") {
		t.Errorf("a node turned off: %v", n)
	}
	b.must("PATCH", fmt.Sprintf("/api/external-sources/%d", sid), map[string]any{"enabled": false}, 200)
	if n := names(bob); slices.Contains(n, "P Tokyo") {
		t.Errorf("a link turned off: %v", n)
	}
}

func TestParseUserinfo(t *testing.T) {
	u := parseUserinfo("upload=455727941; download=6174315083; total=1073741824000; expire=1671815872")
	if u == nil || u.Upload != 455727941 || u.Download != 6174315083 || u.Total != 1073741824000 || u.Expire != 1671815872 {
		t.Errorf("%+v", u)
	}
	if u := parseUserinfo("upload=1.5E9;download=0"); u == nil || u.Upload != 1500000000 {
		t.Errorf("%+v", u)
	}
	if parseUserinfo("") != nil || parseUserinfo("nonsense") != nil {
		t.Error("nothing read as something")
	}
	if u := parseUserinfo("upload=-5; download=7"); u == nil || u.Upload != 0 || u.Download != 7 {
		t.Errorf("%+v", u)
	}

}
