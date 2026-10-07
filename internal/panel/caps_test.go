package panel

import (
	"strings"
	"testing"
)

// A server without nftables (a bare Alpine) runs everything else; what needs nftables is refused
// with the way to get it, forwards default to realm, and the server page says what does not apply.
func TestServerWithoutNftables(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	m := b.must("POST", "/api/servers", map[string]any{"name": "Alpine", "address": "203.0.113.5"}, 201)
	sid := itoa(id(m["server"].(map[string]any)["id"]))
	if _, err := h.p.db.Exec(`UPDATE servers SET first_seen_at = ?, caps = ? WHERE id = ?`, now(),
		`{"systemd":false,"wireguard":true,"conntrack":true,"nftables":false,"iptables":false}`, sid); err != nil {
		t.Fatal(err)
	}
	f := b.must("POST", "/api/servers/"+sid+"/forwards", map[string]any{"target": "203.0.113.9:443"}, 201)
	if f["engine"] != "realm" {
		t.Errorf("forward engine without nftables: %v", f["engine"])
	}
	for _, c := range []struct {
		path string
		body map[string]any
	}{
		{"/api/servers/" + sid + "/forwards", map[string]any{"target": "203.0.113.9:443", "engine": "nft"}},
		{"/api/servers/" + sid + "/nodes", map[string]any{"kind": "wireguard"}},
	} {
		code, m, _ := b.do("POST", c.path, c.body)
		if msg, _ := m["error"].(string); code != 400 || !strings.Contains(msg, "apk add nftables") {
			t.Errorf("%s %v: %d %q", c.path, c.body, code, msg)
		}
	}
	v := b.must("GET", "/api/servers/"+sid, nil, 200)["server"].(map[string]any)
	limits, _ := v["limits"].([]any)
	if len(limits) != 1 || !strings.Contains(limits[0].(string), "nftables is not installed") {
		t.Errorf("limits: %v", v["limits"])
	}
	// proxies are fine
	b.must("POST", "/api/servers/"+sid+"/nodes", map[string]any{"kind": "vless", "settings": map[string]any{"security": "reality"}}, 201)
}
