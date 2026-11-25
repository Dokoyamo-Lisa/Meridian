package panel

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// TestDrawBridgeIP: a hop's IP version (pass_ip) reaches its outbound, a protocol's own (out_ip)
// becomes its way out - which a chain ending there follows too - and what could not work is refused.
func TestDrawBridgeIP(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	mk := func(name, addr, ipv string) int64 {
		return id(b.must("POST", "/api/servers", map[string]any{"name": name, "address": addr, "ip_version": ipv}, 201)["server"].(map[string]any)["id"])
	}
	a, c, v4only := mk("A", "203.0.113.81", ""), mk("C", "198.51.100.81", ""), mk("Four", "198.51.100.82", "ipv4")
	x := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", c), map[string]any{"kind": "vless", "out_ip": "v6"}, 201)["id"])
	e := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", a), map[string]any{"kind": "vless", "pass_node": x, "pass_ip": "prefer-v4"}, 201)["id"])
	base := func(srv int64) string {
		t.Helper()
		st, err := h.p.compileServer(context.Background(), srv)
		if err != nil {
			t.Fatal(err)
		}
		return string(st.Xray.Base)
	}
	if got := base(a); !strings.Contains(got, `"domainStrategy":"UseIPv4v6"`) || !strings.Contains(got, passTag(e)) {
		t.Errorf("the hop prefers IPv4: %s", got)
	}
	if got := base(c); !strings.Contains(got, bindTag(x)) || !strings.Contains(got, `"domainStrategy":"ForceIPv6"`) {
		t.Errorf("the exit leaves over IPv6 only: %s", got)
	}
	// changing it applies; clearing it goes back to the server's way
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", x), map[string]any{"out_ip": "prefer-v4"}, 200)
	if got := base(c); !strings.Contains(got, `"domainStrategy":"UseIPv4v6"`) {
		t.Errorf("prefer IPv4: %s", got)
	}
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", x), map[string]any{"out_ip": ""}, 200)
	if got := base(c); strings.Contains(got, bindTag(x)) {
		t.Errorf("no way of its own any more: %s", got)
	}
	refused := func(what, method, path string, body map[string]any, want string) {
		t.Helper()
		if code, m, _ := b.do(method, path, body); code != 400 || !strings.Contains(fmt.Sprint(m["error"]), want) {
			t.Errorf("%s: %d %v (want %q)", what, code, m, want)
		}
	}
	refused("unknown value", "PATCH", fmt.Sprintf("/api/nodes/%d", x), map[string]any{"out_ip": "ipv9"}, "prefer-v4")
	refused("IPv6 only on an IPv4 server", "POST", fmt.Sprintf("/api/servers/%d/nodes", v4only), map[string]any{"kind": "vless", "out_ip": "v6"}, "does not use IPv6")
	refused("not Xray", "POST", fmt.Sprintf("/api/servers/%d/nodes", c), map[string]any{"kind": "hysteria2", "out_ip": "v4"}, "only Xray protocols")
}

// TestDrawBridgeBackup: a pass with a backup exit gets both lines and a balancer that falls back to
// the backup; both exits take the entry's pass user; the main line gone, the backup carries it alone.
// A protocol may leave from one of its server's addresses.
func TestDrawBridgeBackup(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	mk := func(name, addr string) int64 {
		return id(b.must("POST", "/api/servers", map[string]any{"name": name, "address": addr}, 201)["server"].(map[string]any)["id"])
	}
	a, m, k := mk("A", "203.0.113.91"), mk("Main", "198.51.100.91"), mk("Backup", "198.51.100.92")
	xm := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", m), map[string]any{"kind": "vless"}, 201)["id"])
	xk := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", k), map[string]any{"kind": "trojan"}, 201)["id"])
	e := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", a), map[string]any{"kind": "vless", "pass_node": xm, "pass_backups": []int64{xk}}, 201)["id"])
	compiled := func(srv int64) (string, map[string]bool) {
		t.Helper()
		st, err := h.p.compileServer(context.Background(), srv)
		if err != nil {
			t.Fatal(err)
		}
		users := map[string]bool{}
		for _, in := range st.Xray.Inbounds {
			for _, c := range in.Clients {
				users[c.Email] = true
			}
		}
		return string(st.Xray.Base), users
	}
	base, _ := compiled(a)
	for _, want := range []string{failLine(e, 0), failLine(e, 1), `"fallbackTag":"` + failLine(e, 1) + `"`, `"balancerTag":"` + failTag(e, 0) + `"`, `"observatory"`} {
		if !strings.Contains(base, want) {
			t.Errorf("the entry has no %s: %s", want, base)
		}
	}
	if _, u := compiled(m); !u[passEmail(e)] {
		t.Error("the main exit does not take the pass user")
	}
	if _, u := compiled(k); !u[passEmail(e)] {
		t.Error("the backup exit does not take the pass user")
	}
	// the main exit off: the backup alone, no balancer
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", xm), map[string]any{"enabled": false}, 200)
	if base, _ = compiled(a); strings.Contains(base, failTag(e, 0)) || !strings.Contains(base, "198.51.100.92") {
		t.Errorf("the backup should carry it alone: %s", base)
	}
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", xm), map[string]any{"enabled": true}, 200)
	refused := func(what string, body map[string]any, want string) {
		t.Helper()
		if code, r, _ := b.do("PATCH", fmt.Sprintf("/api/nodes/%d", e), body); code != 400 || !strings.Contains(fmt.Sprint(r["error"]), want) {
			t.Errorf("%s: %d %v (want %q)", what, code, r, want)
		}
	}
	refused("the main exit as its own backup", map[string]any{"pass_backups": []int64{xm}}, "another protocol")
	refused("a backup back to its own server", map[string]any{"pass_backups": []int64{id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", a), map[string]any{"kind": "trojan", "port": 9443}, 201)["id"])}}, "backup 1: the exit must be on another server")
	// leaving from one of its addresses
	reported(t, h, k, []string{"198.51.100.92", "198.51.100.93"}, "")
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", xk), map[string]any{"out_addr": "198.51.100.93"}, 200)
	if base, _ = compiled(k); !strings.Contains(base, `"sendThrough":"198.51.100.93"`) {
		t.Errorf("the backup exit should leave from its second address: %s", base)
	}
	if code, r, _ := b.do("PATCH", fmt.Sprintf("/api/nodes/%d", xk), map[string]any{"out_addr": "192.0.2.1"}); code != 400 || !strings.Contains(fmt.Sprint(r["error"]), "is not one of") {
		t.Errorf("an address it does not have: %d %v", code, r)
	}
}

// TestDrawBridgePriority: backups in their order become stages - each picks its line while it answers
// and otherwise brings the traffic in again for the next - and directly from here may come last.
func TestDrawBridgePriority(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	mk := func(name, addr string) int64 {
		return id(b.must("POST", "/api/servers", map[string]any{"name": name, "address": addr}, 201)["server"].(map[string]any)["id"])
	}
	a := mk("A", "203.0.113.95")
	var exits []int64
	for i := 0; i < 3; i++ {
		srv := mk(fmt.Sprintf("X%d", i), fmt.Sprintf("198.51.100.%d", 95+i))
		exits = append(exits, id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", srv), map[string]any{"kind": "vless"}, 201)["id"]))
	}
	e := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", a), map[string]any{"kind": "vless", "pass_node": exits[0], "pass_backups": []int64{exits[1], exits[2], -1}}, 201)["id"])
	st, err := h.p.compileServer(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	base := string(st.Xray.Base)
	for _, want := range []string{
		`"fallbackTag":"` + failLoop(e, 1) + `"`, `"fallbackTag":"` + failLoop(e, 2) + `"`, `"fallbackTag":"` + failLine(e, 3) + `"`,
		`"inboundTag":"` + failIn(e, 1) + `"`, `"balancerTag":"` + failTag(e, 2) + `"`, `"protocol":"loopback"`,
	} {
		if !strings.Contains(base, want) {
			t.Errorf("no %s: %s", want, base)
		}
	}
	if code, r, _ := b.do("PATCH", fmt.Sprintf("/api/nodes/%d", e), map[string]any{"pass_backups": []int64{-1, exits[1]}}); code != 400 || !strings.Contains(fmt.Sprint(r["error"]), "only be the last") {
		t.Errorf("directly first: %d %v", code, r)
	}
}
