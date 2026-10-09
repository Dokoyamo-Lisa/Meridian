package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"meridian/internal/proto"
	"meridian/internal/subgen"
)

func TestParseHop(t *testing.T) {
	for in, want := range map[string]string{"20000-30000": "20000-30000", " 20000 : 30000 ": "20000-30000", "": "",
		"1024-65535": "1024-65535"} {
		if got, err := normHop(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"20000", "53-1000", "30000-20000", "20000-20000", "20000-70000", "a-b", "20000-30000-40000"} {
		if got, err := normHop(in); err == nil {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
}

// TestPortHopping: a Hysteria2 range reaches its server's state for the firewall, takes no UDP port
// another protocol or forward uses (in either direction), changes nothing that restarts Hysteria2,
// and is refused where the server cannot redirect ports.
func TestPortHopping(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "Hop", "address": "203.0.113.80"}, 201)["server"].(map[string]any)["id"])
	nodes := fmt.Sprintf("/api/servers/%d/nodes", sid)
	wg := b.must("POST", nodes, map[string]any{"kind": "wireguard", "port": 25000}, 201)

	// a range over WireGuard's port is refused, and the check says so before saving
	code, m, _ := b.do("POST", nodes, map[string]any{"kind": "hysteria2", "settings": map[string]any{"hop_ports": "20000-30000"}})
	if msg, _ := m["error"].(string); code != 409 || !strings.Contains(msg, "25000") {
		t.Fatalf("range over WireGuard: %d %q", code, msg)
	}
	chk := b.must("POST", "/api/protocols/check", map[string]any{"kind": "hysteria2", "server_id": sid,
		"settings": map[string]any{"hop_ports": "20000-30000"}}, 200)
	if chk["valid"] != false || !strings.Contains(fmt.Sprint(chk["error"]), "25000") {
		t.Fatalf("check: %v", chk)
	}
	for _, bad := range []string{"500-2000", "30000-20000", "20000"} {
		code, m, _ := b.do("POST", nodes, map[string]any{"kind": "hysteria2", "settings": map[string]any{"hop_ports": bad}})
		if code != 400 || !strings.Contains(fmt.Sprint(m["error"]), "range") {
			t.Errorf("%q: %d %v", bad, code, m["error"])
		}
	}

	hy := b.must("POST", nodes, map[string]any{"kind": "hysteria2", "settings": map[string]any{"hop_ports": "26000-27000"}}, 201)
	hyID := id(hy["id"])
	if s := hy["settings"].(map[string]any); s["hop_ports"] != "26000-27000" {
		t.Fatalf("settings: %v", s)
	}
	st, err := h.p.compileServer(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Hysteria) != 1 || st.Hysteria[0].HopPorts != "26000-27000" {
		t.Fatalf("state: %+v", st.Hysteria)
	}
	// links carry the range where the app reads it
	var users []map[string]any
	_, _, raw := b.do("POST", "/api/users", map[string]any{"name": "hopper"})
	_ = json.Unmarshal(raw, &users)
	link := users[0]["link"].(string)
	for client, want := range map[string]string{"clash": "ports: 26000-27000", "singbox": `"26000:27000"`, "surge": "port-hopping=26000-27000"} {
		resp, err := http.Get(h.srv.URL + link[strings.Index(link, "/s/"):] + "?client=" + client)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(body), want) {
			t.Errorf("%s: %d, no %q in\n%s", client, resp.StatusCode, want, body)
		}
	}

	// other protocols and forwards stay out of the range
	code, m, _ = b.do("POST", nodes, map[string]any{"kind": "shadowsocks", "port": 26500})
	if msg, _ := m["error"].(string); code != 409 || !strings.Contains(msg, "hops between") {
		t.Errorf("Shadowsocks (UDP) inside the range: %d %q", code, msg)
	}
	b.must("POST", nodes, map[string]any{"kind": "vless", "port": 26501}, 201) // TCP only: fine
	code, m, _ = b.do("POST", fmt.Sprintf("/api/servers/%d/forwards", sid), map[string]any{"listen_port": 26600, "network": "udp",
		"target": "198.51.100.9:53"})
	if msg, _ := m["error"].(string); code != 409 || !strings.Contains(msg, "hops between") {
		t.Errorf("UDP forward inside the range: %d %q", code, msg)
	}
	code, m, _ = b.do("PATCH", fmt.Sprintf("/api/nodes/%d", id(wg["id"])), map[string]any{"port": 26700})
	if code != 409 {
		t.Errorf("WireGuard moved into the range: %d %v", code, m["error"])
	}
	// a second range may not overlap the first
	code, m, _ = b.do("POST", nodes, map[string]any{"kind": "hysteria2", "port": 8443, "settings": map[string]any{"hop_ports": "26900-28000"}})
	if msg, _ := m["error"].(string); code != 409 || !strings.Contains(msg, "overlaps") {
		t.Errorf("overlapping ranges: %d %q", code, msg)
	}

	// changing the range: devices refresh, Hysteria2 does not restart
	chk = b.must("POST", "/api/protocols/check", map[string]any{"node_id": hyID, "settings": map[string]any{"hop_ports": "27000-28000"}}, 200)
	if chk["valid"] != true || chk["restarts"] == true || !slices.Contains(toStrings(chk["refresh"]), "port hopping") {
		t.Errorf("impact of a new range: %v", chk)
	}
	chk = b.must("POST", "/api/protocols/check", map[string]any{"node_id": hyID, "settings": map[string]any{"obfs": true}}, 200)
	if chk["restarts"] != true {
		t.Errorf("obfuscation must still restart Hysteria2: %v", chk)
	}
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", hyID), map[string]any{"settings": map[string]any{"hop_ports": ""}}, 200)
	if st, _ := h.p.compileServer(context.Background(), sid); st.Hysteria[0].HopPorts != "" {
		t.Error("turning port hopping off left the range in the state")
	}

	// where the server cannot redirect ports
	for _, c := range []struct {
		caps, ports, want string
	}{
		{`{"nftables":false,"port_hop":true}`, "", "apk add nftables"},
		{`{"nftables":true}`, "", "agent 1.0"},
		{`{"nftables":true,"port_hop":true}`, "20000-20100", "provider decides"},
	} {
		if _, err := h.p.db.Exec1(`UPDATE servers SET first_seen_at = ?, caps = ?, public_ports = ? WHERE id = ?`, now(), c.caps, c.ports, sid); err != nil {
			t.Fatal(err)
		}
		code, m, _ := b.do("PATCH", fmt.Sprintf("/api/nodes/%d", hyID), map[string]any{"settings": map[string]any{"hop_ports": "21000-22000"}})
		if msg, _ := m["error"].(string); code != 400 || !strings.Contains(msg, c.want) {
			t.Errorf("caps %s ports %q: %d %q", c.caps, c.ports, code, msg)
		}
	}
	if _, err := h.p.db.Exec1(`UPDATE servers SET caps = ?, public_ports = '' WHERE id = ?`, `{"nftables":true,"port_hop":true}`, sid); err != nil {
		t.Fatal(err)
	}
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", hyID), map[string]any{"settings": map[string]any{"hop_ports": "21000-22000"}}, 200)
	var caps proto.Caps
	_ = json.Unmarshal([]byte(`{"port_hop":true}`), &caps)
	if !caps.PortHop {
		t.Error("the capability does not read back")
	}
}

// TestTrojanReality: Trojan takes REALITY like VLESS - its camouflage is tested from the server, and
// the apps that read REALITY for Trojan get it.
func TestTrojanReality(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "TR", "address": "203.0.113.81"}, 201)["server"].(map[string]any)["id"])
	n := b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": "trojan", "settings": map[string]any{"security": "reality"}}, 201)
	if n["label"] != "Trojan REALITY" {
		t.Errorf("label %v", n["label"])
	}
	var queued int
	if err := h.p.db.QueryRow(`SELECT COUNT(*) FROM actions WHERE server_id = ? AND kind = ?`, sid, proto.ActionCheckTarget).Scan(&queued); err != nil || queued != 1 {
		t.Errorf("camouflage check queued: %d %v", queued, err)
	}
	b.must("POST", fmt.Sprintf("/api/nodes/%d/test-target", id(n["id"])), map[string]any{}, 202)
	apps := map[string]bool{}
	for _, a := range n["apps"].([]any) {
		m := a.(map[string]any)
		apps[fmt.Sprint(m["app"])] = m["ok"] == true
	}
	for app, want := range map[string]bool{"mihomo": true, "singbox": true, "xray": true, "stash": true, "loon": true, "quanx": true,
		"hiddify": false, "shadowrocket": false, "surge": false} {
		if apps[app] != want {
			t.Errorf("%s: works = %v, want %v", app, apps[app], want)
		}
	}
	// VMess and HTTP proxies still cannot
	for _, kind := range []string{subgen.KindVMess, subgen.KindHTTP} {
		code, m, _ := b.do("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": kind, "settings": map[string]any{"security": "reality"}})
		if code != 400 || !strings.Contains(fmt.Sprint(m["error"]), "VLESS and Trojan") {
			t.Errorf("%s with REALITY: %d %v", kind, code, m["error"])
		}
	}
}

func toStrings(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, x := range l {
		out = append(out, fmt.Sprint(x))
	}
	return out
}
