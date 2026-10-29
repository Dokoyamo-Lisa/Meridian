package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"testing"

	"meridian/internal/proto"
)

// TestSecurityModeSetting: the security mode is turned on, changed and off from a signed-in browser
// only, with a confirmation, on a server whose agent runs it; the server's state carries it with the
// panel's own addresses trusted; off, the state has none.
func TestSecurityModeSetting(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.10"}, 201)["server"].(map[string]any)["id"])
	path := fmt.Sprintf("/api/servers/%d/guard", sid)
	on := map[string]any{"ssh_ban": true, "keys_only": true, "stop_miners": true, "confirm": true}

	helloCaps(t, h, sid, "203.0.113.10", proto.Caps{Systemd: true, Nftables: true, Protect: true}) // an agent before 1.4
	if m := b.must("PUT", path, on, 400); !strings.Contains(fmt.Sprint(m["error"]), "1.4") {
		t.Errorf("an agent that cannot: %v", m["error"])
	}
	helloCaps(t, h, sid, "203.0.113.10", proto.Caps{Systemd: true, Nftables: true, Protect: true, Guard: true})
	if m := b.must("PUT", path, map[string]any{"ssh_ban": true}, 400); !strings.Contains(fmt.Sprint(m["error"]), "confirm") {
		t.Errorf("without a confirmation: %v", m["error"])
	}
	full := b.must("POST", "/api/tokens", map[string]any{"name": "full", "scope": "full"}, 201)["token"].(string)
	if code, _, _ := h.bearer(full).do("PUT", path, on); code != 403 {
		t.Errorf("an API token turned the security mode on: %d", code)
	}
	g := b.must("PUT", path, on, 200)["guard"].(map[string]any)
	if g["ssh_ban"] != true || g["keys_only"] != true || g["stop_miners"] != true || g["by"] != "owner" {
		t.Fatalf("the security mode: %v", g)
	}
	st := stateOf(t, h, sid)
	if st.Guard == nil || !st.Guard.SSHBan || !st.Guard.KeysOnly || !st.Guard.StopMiners {
		t.Fatalf("the state: %+v", st.Guard)
	}
	b.must("PUT", path, map[string]any{"ssh_ban": true, "confirm": true}, 200)
	if st = stateOf(t, h, sid); st.Guard == nil || st.Guard.KeysOnly || !st.Guard.SSHBan {
		t.Errorf("changed: %+v", st.Guard)
	}
	if v := b.must("PUT", path, map[string]any{"confirm": true}, 200); v["guard"] != nil {
		t.Errorf("off: %v", v["guard"])
	}
	if st = stateOf(t, h, sid); st.Guard != nil {
		t.Errorf("the state while off: %+v", st.Guard)
	}
}

// TestAddressesOfTheirOwn: a protocol can have a public address of the server, or a private one whose
// public address the agent found - its links then give that public address; a private one without a
// known public address is refused (agents 1.4), and the server lists the choices.
func TestAddressesOfTheirOwn(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "Cloud"}, 201)["server"].(map[string]any)["id"])
	srv, _ := h.p.serverByID(context.Background(), sid)
	caps := proto.Caps{Systemd: true, Nftables: true, Certs: true, Prepare: true}
	if _, err := h.p.ingest(context.Background(), srv, &proto.Report{Instance: "i", Hello: &proto.Hello{AgentVersion: Version,
		IPv4: "203.0.113.5", Caps: caps, Addrs: []string{"10.0.0.5", "10.0.0.6", "10.0.0.7", "198.51.100.8"},
		AddrPublic: map[string]string{"10.0.0.5": "203.0.113.5", "10.0.0.6": "203.0.113.6", "10.0.0.9": "203.0.113.9", "10.0.0.7": "10.1.1.1"}}}); err != nil {
		t.Fatal(err)
	}
	v := b.must("GET", fmt.Sprintf("/api/servers/%d", sid), nil, 200)["server"].(map[string]any)
	var choices []addrChoice
	raw, _ := json.Marshal(v["addr_choices"])
	_ = json.Unmarshal(raw, &choices)
	want := []addrChoice{{"10.0.0.5", "203.0.113.5"}, {"10.0.0.6", "203.0.113.6"}, {"198.51.100.8", "198.51.100.8"}}
	if fmt.Sprint(choices) != fmt.Sprint(want) {
		t.Fatalf("choices %v, want %v (an address the server does not have, or a private answer, left out)", choices, want)
	}
	nodes := fmt.Sprintf("/api/servers/%d/nodes", sid)
	if m := b.must("POST", nodes, map[string]any{"kind": "vless", "bind_ip": "10.0.0.7"}, 400); !strings.Contains(fmt.Sprint(m["error"]), "private address") {
		t.Errorf("a private address without a public one: %v", m["error"])
	}
	n := b.must("POST", nodes, map[string]any{"kind": "vless", "bind_ip": "10.0.0.6"}, 201)
	nid := id(n["id"])
	node, s, err := h.p.ownNode(context.Background(), &Account{ID: 1}, nid)
	if err != nil {
		t.Fatal(err)
	}
	if host := linkHost(node, s); host != "203.0.113.6" {
		t.Errorf("links give %s, want the public address in front of 10.0.0.6", host)
	}
	if !strings.Contains(strings.Join(h.p.currentAddrs(context.Background(), s), ","), "203.0.113.6") {
		t.Error("the server's addresses for allow-lists leave out where its private addresses' traffic comes from")
	}
}

// TestCloudflareWarnings: a REALITY camouflage site behind Cloudflare, the domain of a protocol with
// its own certificate proxied by Cloudflare, a CDN domain on Cloudflare and a link address that points
// at Cloudflare's proxy get a warning in the check; a site outside Cloudflare, a self-signed
// certificate's name and an IP address do not.
func TestCloudflareWarnings(t *testing.T) {
	old := lookupCF
	defer func() { lookupCF = old }()
	lookupCF = func(_ context.Context, name string) ([]netip.Addr, error) {
		switch name {
		case "behind.example.com", "proxied.example.com":
			return []netip.Addr{netip.MustParseAddr("104.16.1.1")}, nil
		case "plain.example.com":
			return []netip.Addr{netip.MustParseAddr("198.51.100.20")}, nil
		}
		return nil, fmt.Errorf("no such name")
	}
	reality := func(sni string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"transport": "raw", "security": "reality", "sni": sni})
		return b
	}
	tls := func(sni, mode string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"transport": "raw", "security": "tls", "sni": sni, "cert_mode": mode})
		return b
	}
	cdn := func(host string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"transport": "ws", "security": "none", "cdn": true, "cdn_host": host})
		return b
	}
	cases := []struct {
		kind, host string
		raw        json.RawMessage
		want       string
	}{
		{"vless", "203.0.113.10", reality("behind.example.com"), "behind Cloudflare"},
		{"vless", "203.0.113.10", reality("plain.example.com"), ""},
		{"trojan", "203.0.113.10", tls("proxied.example.com", "acme"), "orange cloud"},
		{"vless", "203.0.113.10", tls("proxied.example.com", "self"), ""},
		{"vless", "203.0.113.10", cdn("proxied.example.com"), "abuse"},
		{"vless", "proxied.example.com", cdn("plain.example.com"), ""}, // a CDN's links use the CDN domain
		{"hysteria2", "203.0.113.10", json.RawMessage(`{"sni":"proxied.example.com","cert_mode":"acme"}`), "orange cloud"},
		{"vless", "203.0.113.10", reality("nowhere.example.com"), ""},
		{"shadowsocks", "proxied.example.com", json.RawMessage(`{"method":"2022-blake3-aes-128-gcm"}`), "would not work"},
		{"wireguard", "plain.example.com", json.RawMessage(`{}`), ""},
		{"vless", "[2001:db8::1]", reality("plain.example.com"), ""},
	}
	for _, c := range cases {
		got := strings.Join(domainWarnings(context.Background(), c.kind, c.raw, c.host), " ")
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s %s at %s: %q, want %q", c.kind, c.raw, c.host, got, c.want)
		}
	}
	// the same name once: a TLS domain that is also the link address is one warning
	if got := domainWarnings(context.Background(), "trojan", tls("proxied.example.com", "acme"), "proxied.example.com"); len(got) != 1 {
		t.Errorf("one name, %d warnings: %q", len(got), got)
	}
	for _, s := range RealityTargets {
		if strings.Contains(s, "cloudflare") {
			t.Errorf("%s is picked automatically as a camouflage site", s)
		}
	}
}

// TestReinstallAndCoreUpgrades: installing the cores again needs an agent that can; the cores upgrade
// themselves only when that is turned on, at night, one server at a time, once a day at most.
func TestReinstallAndCoreUpgrades(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.10"}, 201)["server"].(map[string]any)["id"])
	helloCaps(t, h, sid, "203.0.113.10", proto.Caps{Systemd: true})
	acts := fmt.Sprintf("/api/servers/%d/actions", sid)
	if m := b.must("POST", acts, map[string]any{"kind": "reinstall"}, 400); !strings.Contains(fmt.Sprint(m["error"]), "1.4") {
		t.Errorf("an agent that cannot: %v", m["error"])
	}
	helloCaps(t, h, sid, "203.0.113.10", proto.Caps{Systemd: true, Reinstall: true})
	if m := b.must("POST", acts, map[string]any{"kind": "reinstall", "args": map[string]any{"core": "everything"}}, 400); !strings.Contains(fmt.Sprint(m["error"]), "core") {
		t.Errorf("an unknown core: %v", m["error"])
	}
	b.must("POST", acts, map[string]any{"kind": "reinstall", "args": map[string]any{"core": "xray"}}, 202)

	// automatic upgrades: off by default - nothing happens even with an older Xray
	if _, err := h.p.db.Exec1(`UPDATE servers SET xray_version = '1.0.0', online = 1 WHERE id = ?`, sid); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		var n int
		_ = h.p.db.QueryRow(`SELECT COUNT(*) FROM actions WHERE server_id = ? AND kind = ? AND created_by = 0`, sid, proto.ActionUpgradeXray).Scan(&n)
		return n
	}
	h.p.coreAutoUpgrade(context.Background())
	if count() != 0 {
		t.Fatal("an upgrade started while automatic core upgrades are off")
	}
	srv, _ := h.p.serverByID(context.Background(), sid)
	set := h.p.settings()
	if kind, from, to := h.p.coreBehind(srv, set); kind != proto.ActionUpgradeXray || from != "1.0.0" || to != set.XrayVersion {
		t.Errorf("behind: %s %s %s", kind, from, to)
	}
	if _, err := h.p.db.Exec1(`UPDATE servers SET xray_version = ? WHERE id = ?`, set.XrayVersion, sid); err != nil {
		t.Fatal(err)
	}
	srv, _ = h.p.serverByID(context.Background(), sid)
	if kind, _, _ := h.p.coreBehind(srv, set); kind != "" {
		t.Errorf("a server on the set version is behind: %s", kind)
	}
}

// TestReinstallServer: a reinstall keeps every protocol and asks the agent to build them again; a
// clean reinstall removes the server's protocols, forwards and configuration as code, here too. Both
// need agent 1.4 and a confirmation, and a read-only token reaches neither. The cores a server lacks
// are installed on request.
func TestReinstallServer(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.10"}, 201)["server"].(map[string]any)["id"])
	path := fmt.Sprintf("/api/servers/%d/reinstall", sid)
	helloCaps(t, h, sid, "203.0.113.10", proto.Caps{Systemd: true, Nftables: true})
	if m := b.must("POST", path, map[string]any{"confirm": true}, 400); !strings.Contains(fmt.Sprint(m["error"]), "1.4") {
		t.Errorf("an agent that cannot: %v", m["error"])
	}
	acts := fmt.Sprintf("/api/servers/%d/actions", sid)
	if m := b.must("POST", acts, map[string]any{"kind": "install_cores"}, 400); !strings.Contains(fmt.Sprint(m["error"]), "1.4") {
		t.Errorf("installing cores on an agent that cannot: %v", m["error"])
	}
	helloCaps(t, h, sid, "203.0.113.10", proto.Caps{Systemd: true, Nftables: true, Reinstall: true, Prepare: true})
	b.must("POST", acts, map[string]any{"kind": "install_cores"}, 202)
	if m := b.must("POST", acts, map[string]any{"kind": "reinstall", "args": map[string]any{"core": "all", "clean": true}}, 400); !strings.Contains(fmt.Sprint(m["error"]), "/reinstall") {
		t.Errorf("a clean reinstall through actions: %v", m["error"])
	}

	b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": "vless", "settings": map[string]any{"security": "reality"}}, 201)
	b.must("POST", fmt.Sprintf("/api/servers/%d/forwards", sid), map[string]any{"target": "203.0.113.9:443"}, 201)
	if _, err := h.p.db.Exec1(`UPDATE servers SET xray_code = '{"outbounds":[]}' WHERE id = ?`, sid); err != nil {
		t.Fatal(err)
	}
	count := func(q string) int {
		var n int
		if err := h.p.db.QueryRow(q, sid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	lastArgs := func() proto.Reinstall {
		var raw string
		if err := h.p.db.QueryRow(`SELECT args FROM actions WHERE server_id = ? AND kind = ? ORDER BY id DESC LIMIT 1`, sid, proto.ActionReinstall).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var r proto.Reinstall
		_ = json.Unmarshal([]byte(raw), &r)
		return r
	}

	// confirm first; a read-only token never
	if m := b.must("POST", path, map[string]any{}, 400); !strings.Contains(fmt.Sprint(m["error"]), "confirm") {
		t.Errorf("without confirm: %v", m["error"])
	}
	read := b.must("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"}, 201)["token"].(string)
	if code, _, _ := h.bearer(read).do("POST", path, map[string]any{"confirm": true, "clean": true}); code != 403 {
		t.Errorf("a read-only token: %d", code)
	}

	// reinstall: everything stays, the agent rebuilds it
	if m := b.must("POST", path, map[string]any{"confirm": true}, 202); m["id"] == nil || m["protocols_removed"].(float64) != 0 {
		t.Errorf("reinstall answer: %v", m)
	}
	if r := lastArgs(); r.Core != "all" || !r.Rebuild || r.Clean {
		t.Errorf("reinstall asks the agent for %+v", r)
	}
	if count(`SELECT COUNT(*) FROM nodes WHERE server_id = ?`) != 1 || count(`SELECT COUNT(*) FROM forwards WHERE server_id = ?`) != 1 {
		t.Error("a reinstall removed something")
	}

	// clean reinstall: the server starts over
	m := b.must("POST", path, map[string]any{"confirm": true, "clean": true}, 202)
	if m["protocols_removed"].(float64) != 1 || m["forwards_removed"].(float64) != 1 {
		t.Errorf("clean reinstall answer: %v", m)
	}
	if r := lastArgs(); !r.Clean || r.Rebuild {
		t.Errorf("a clean reinstall asks the agent for %+v", r)
	}
	if count(`SELECT COUNT(*) FROM nodes WHERE server_id = ?`) != 0 || count(`SELECT COUNT(*) FROM forwards WHERE server_id = ?`) != 0 ||
		count(`SELECT COUNT(*) FROM servers WHERE id = ? AND xray_code != ''`) != 0 {
		t.Error("a clean reinstall left protocols, forwards or configuration as code")
	}
	if count(`SELECT COUNT(*) FROM events WHERE server_id = ? AND kind = 'server_reinstalled'`) != 2 {
		t.Error("the reinstalls are not in the timeline")
	}
}
