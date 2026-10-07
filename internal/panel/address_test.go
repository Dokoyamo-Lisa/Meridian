package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// reported makes a server look connected, with the given addresses and capabilities.
func reported(t *testing.T, h *harness, id int64, addrs []string, caps string) {
	t.Helper()
	b, _ := json.Marshal(addrs)
	if _, err := h.p.db.Exec1(`UPDATE servers SET addrs = ?, caps = ?, first_seen_at = ?, ipv4 = ?, ipv6 = ? WHERE id = ?`,
		string(b), caps, now(), "203.0.113.70", "2001:db8::70", id); err != nil {
		t.Fatal(err)
	}
}

func singbox(t *testing.T, h *harness, link string) map[string]map[string]any {
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
	out := map[string]map[string]any{}
	for _, o := range append(c.Outbounds, c.Endpoints...) {
		out[fmt.Sprint(o["type"])+"|"+fmt.Sprint(o["tag"])] = o
	}
	return out
}

// TestProtocolAddresses: each protocol can have one of the server's addresses to itself - it
// listens there, leaves from there, links use it - and protocols on different addresses share a port.
func TestProtocolAddresses(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	srv := b.must("POST", "/api/servers", map[string]any{"name": "Multi"}, 201)["server"].(map[string]any)
	sid := id(srv["id"])
	reported(t, h, sid, []string{"203.0.113.71", "203.0.113.72", "2001:db8::71"}, `{"nftables":true,"wireguard":true,"wg6":true}`)
	path := fmt.Sprintf("/api/servers/%d/nodes", sid)

	a := b.must("POST", path, map[string]any{"kind": "vless", "port": 443, "bind_ip": "203.0.113.71", "name": "A"}, 201)
	b.must("POST", path, map[string]any{"kind": "trojan", "port": 443, "bind_ip": "203.0.113.72", "name": "B"}, 201)
	if code, m, _ := b.do("POST", path, map[string]any{"kind": "vmess", "port": 443}); code != 409 || !strings.Contains(fmt.Sprint(m["error"]), "port 443 is already used") {
		t.Errorf("all addresses on a taken port: %d %v", code, m)
	}
	if code, m, _ := b.do("POST", path, map[string]any{"kind": "vless", "bind_ip": "198.51.100.9"}); code != 400 || !strings.Contains(fmt.Sprint(m["error"]), "not an address of Multi") {
		t.Errorf("a foreign address: %d %v", code, m)
	}

	st, err := h.p.compileServer(context.Background(), sid)
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
	listens := map[string]bool{}
	for _, in := range st.Xray.Inbounds {
		var obj map[string]any
		_ = json.Unmarshal(in.Config, &obj)
		listens[fmt.Sprint(obj["listen"])] = true
	}
	if !listens["203.0.113.71"] || !listens["203.0.113.72"] {
		t.Errorf("inbounds listen on %v", listens)
	}
	bound := fmt.Sprint(base.Outbounds)
	if !strings.Contains(bound, "sendThrough:203.0.113.71") || !strings.Contains(bound, "domainStrategy:UseIPv4") ||
		!strings.Contains(fmt.Sprint(base.Routing.Rules), fmt.Sprintf("bind-n%d", id(a["id"]))) {
		t.Errorf("bound outbounds %v rules %v", base.Outbounds, base.Routing.Rules)
	}

	var users []map[string]any
	_, _, raw := b.do("POST", "/api/users", map[string]any{"name": "u"})
	_ = json.Unmarshal(raw, &users)
	link := users[0]["link"].(string)
	for k, o := range singbox(t, h, link) {
		want, ok := map[string]string{"vless": "203.0.113.71", "trojan": "203.0.113.72"}[strings.Split(k, "|")[0]]
		if ok && o["server"] != want {
			t.Errorf("%s: server %v, want %s", k, o["server"], want)
		}
	}

	// an IPv4-only server: no IPv6 address can be bound, and sites are reached by IPv4
	b.must("PATCH", fmt.Sprintf("/api/servers/%d", sid), map[string]any{"ip_version": "ipv4"}, 200)
	if code, m, _ := b.do("POST", path, map[string]any{"kind": "vless", "bind_ip": "2001:db8::71"}); code != 400 || !strings.Contains(fmt.Sprint(m["error"]), "does not use IPv6") {
		t.Errorf("IPv6 on an IPv4 server: %d %v", code, m)
	}
	st, _ = h.p.compileServer(context.Background(), sid)
	_ = json.Unmarshal(st.Xray.Base, &base)
	if s := fmt.Sprint(base.Outbounds); !strings.Contains(s, "direct-ipver") || !strings.Contains(fmt.Sprint(base.Routing.Rules), "ipver") {
		t.Errorf("IPv4 only: %v", base.Outbounds)
	}
	if code, _, _ := b.do("PATCH", fmt.Sprintf("/api/servers/%d", sid), map[string]any{"ip_version": "ipv5"}); code != 400 {
		t.Errorf("a bad IP version: %d", code)
	}
}

// TestWireGuardIPv6: a tunnel is IPv4 only unless IPv6 is asked for and the server routes it -
// no more ::/0 swallowing the devices' IPv6.
func TestWireGuardIPv6(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	srv := b.must("POST", "/api/servers", map[string]any{"name": "WG6"}, 201)["server"].(map[string]any)
	sid := id(srv["id"])
	reported(t, h, sid, []string{"203.0.113.70", "2001:db8::70"}, `{"nftables":true,"wireguard":true,"wg6":true}`)
	path := fmt.Sprintf("/api/servers/%d/nodes", sid)
	plain := b.must("POST", path, map[string]any{"kind": "wireguard", "name": "four"}, 201)
	dual := b.must("POST", path, map[string]any{"kind": "wireguard", "name": "six", "settings": map[string]any{"ipv6": true}}, 201)

	var users []map[string]any
	_, _, raw := b.do("POST", "/api/users", map[string]any{"name": "u"})
	_ = json.Unmarshal(raw, &users)
	link := users[0]["link"].(string)
	check := func(name string, want6 bool) {
		t.Helper()
		for k, o := range singbox(t, h, link) {
			if !strings.Contains(k, name) {
				continue
			}
			s := fmt.Sprint(o)
			if has := strings.Contains(s, "::/0"); has != want6 {
				t.Errorf("%s: ::/0 in the tunnel = %v, want %v (%s)", name, has, want6, s)
			}
			return
		}
		t.Errorf("%s: not in the link", name)
	}
	check("four", false)
	check("six", true)

	st, _ := h.p.compileServer(context.Background(), sid)
	for _, w := range st.WireGuard {
		six := w.NodeID == id(dual["id"])
		if has := len(w.Address) == 2; has != six {
			t.Errorf("node %d: addresses %v", w.NodeID, w.Address)
		}
	}
	_ = plain

	// the server set to IPv4 only: IPv6 leaves the tunnel, and the page says why
	v := b.must("PATCH", fmt.Sprintf("/api/servers/%d", sid), map[string]any{"ip_version": "ipv4"}, 200)["server"].(map[string]any)
	check("six", false)
	if !strings.Contains(fmt.Sprint(v["limits"]), "IPv6 stays out of the tunnel") {
		t.Errorf("limits: %v", v["limits"])
	}
	if code, m, _ := b.do("POST", path, map[string]any{"kind": "wireguard", "settings": map[string]any{"ipv6": true}}); code != 400 || !strings.Contains(fmt.Sprint(m["error"]), "IPv4 only") {
		t.Errorf("IPv6 tunnel on an IPv4 server: %d %v", code, m)
	}
}
