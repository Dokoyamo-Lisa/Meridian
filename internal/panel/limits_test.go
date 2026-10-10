package panel

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"meridian/internal/proto"
)

// TestUserLimits: speed limits go to the servers of the users who have them; a device limit that
// turns devices away keeps the first devices to connect (on any server) and turns the rest away -
// with an Xray rule for that user only and at Hysteria2's sign-in - and lets them back when the
// limit goes; users without limits change nothing.
func TestUserLimits(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "A", "address": "203.0.113.70"}, 201)["server"].(map[string]any)["id"])
	hello(t, h, sid, "203.0.113.70", "")
	vless := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": "vless"}, 201)["id"])
	hy := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": "hysteria2",
		"settings": map[string]any{"up_mbps": 500, "down_mbps": 500}}, 201)["id"])
	before := stateOf(t, h, sid)
	if len(before.Speed) != 0 || len(before.Refuse) != 0 {
		t.Fatalf("limits without limited users: %+v %+v", before.Speed, before.Refuse)
	}

	b.must("POST", "/api/users", map[string]any{"name": "lim", "speed_limit": 200, "ip_limit": 2, "device_mode": "refuse", "sign_in": false}, 201)
	b.must("POST", "/api/users", map[string]any{"name": "free", "sign_in": false}, 201)
	var lim, free int64
	h.p.db.QueryRow(`SELECT id FROM subs WHERE name = 'lim'`).Scan(&lim)
	h.p.db.QueryRow(`SELECT id FROM subs WHERE name = 'free'`).Scan(&free)

	st := stateOf(t, h, sid)
	if len(st.Speed) != 1 || st.Speed[0] != (proto.SpeedLimit{Sub: lim, Mbps: 200}) {
		t.Errorf("speed: %+v", st.Speed)
	}
	// Hysteria2 apps of a limited user declare a little less than the limit (without a declared rate
	// a Hysteria2 transfer stalls under the limit); a user without one keeps what the protocol says
	sub, _ := h.p.subByID(context.Background(), lim)
	eps, _ := h.p.endpointsFor(context.Background(), sub)
	for _, e := range eps {
		if e.Kind == "hysteria2" && (e.DownMbps != 190 || e.UpMbps != 190) {
			t.Errorf("hysteria2 bandwidth for a 200 Mbps user: %d/%d", e.UpMbps, e.DownMbps)
		}
	}
	fr, _ := h.p.subByID(context.Background(), free)
	feps, _ := h.p.endpointsFor(context.Background(), fr)
	for _, e := range feps {
		if e.Kind == "hysteria2" && (e.DownMbps == 190 || e.UpMbps == 190) {
			t.Errorf("hysteria2 bandwidth for a user without a limit: %d/%d", e.UpMbps, e.DownMbps)
		}
	}

	// three devices of the limited user online (two on VLESS, one on Hysteria2), one of the free user
	online := func(list ...proto.OnlineUser) {
		h.p.live.put(sid, proto.Live{Online: list})
		srv, _ := h.p.serverByID(context.Background(), sid)
		h.p.checkIPLimits(context.Background(), srv.AccountID)
	}
	online(
		proto.OnlineUser{Sub: lim, Node: vless, IPs: []proto.OnlineIP{{IP: "198.51.100.1", Since: 100}, {IP: "198.51.100.3", Since: 300}}},
		proto.OnlineUser{Sub: lim, Node: hy, IPs: []proto.OnlineIP{{IP: "198.51.100.2", Since: 200}}},
		proto.OnlineUser{Sub: free, Node: vless, IPs: []proto.OnlineIP{{IP: "198.51.100.3", Since: 50}}},
	)
	st = stateOf(t, h, sid)
	if len(st.Refuse) != 1 || st.Refuse[0].Sub != lim || !slices.Equal(st.Refuse[0].IPs, []string{"198.51.100.3"}) {
		t.Fatalf("refused: %+v", st.Refuse)
	}
	c := compileX(t, h, sid)
	rule := c.rule(fmt.Sprintf("dev-s%d", lim))
	if rule == nil || fmt.Sprint(rule["user"]) != fmt.Sprintf("[%s]", proto.Email(lim, vless)) || fmt.Sprint(rule["source"]) != "[198.51.100.3]" ||
		rule["outboundTag"] != "block" {
		t.Fatalf("Xray rule: %v", rule)
	}
	if c.ruleIndex(fmt.Sprintf("dev-s%d", lim)) != 1 { // right after no-private
		t.Errorf("the rule is not first in line: %d", c.ruleIndex(fmt.Sprintf("dev-s%d", lim)))
	}
	if c.rule(fmt.Sprintf("dev-s%d", free)) != nil {
		t.Error("a user without a limit got a rule")
	}

	// a device that comes back within the grace period keeps its place: the newest stays away
	online(
		proto.OnlineUser{Sub: lim, Node: vless, IPs: []proto.OnlineIP{{IP: "198.51.100.3", Since: 300}}},
		proto.OnlineUser{Sub: lim, Node: hy, IPs: []proto.OnlineIP{{IP: "198.51.100.2", Since: 200}}},
	)
	if st := stateOf(t, h, sid); len(st.Refuse) != 1 || st.Refuse[0].IPs[0] != "198.51.100.3" {
		t.Fatalf("a device that was quiet for a moment lost its place: %+v", st.Refuse)
	}
	// a device that went away (quiet for longer than deviceGone) lets the next one in
	h.p.devices.mu.Lock()
	h.p.devices.seen[lim]["198.51.100.1"].last = now() - deviceGone - 1
	h.p.devices.mu.Unlock()
	online(
		proto.OnlineUser{Sub: lim, Node: vless, IPs: []proto.OnlineIP{{IP: "198.51.100.3", Since: 300}}},
		proto.OnlineUser{Sub: lim, Node: hy, IPs: []proto.OnlineIP{{IP: "198.51.100.2", Since: 200}}},
	)
	if st := stateOf(t, h, sid); len(st.Refuse) != 0 || compileX(t, h, sid).rule(fmt.Sprintf("dev-s%d", lim)) != nil {
		t.Errorf("still turned away with two devices: %+v", st.Refuse)
	}

	// only an alert: nobody is turned away
	online(
		proto.OnlineUser{Sub: lim, Node: vless, IPs: []proto.OnlineIP{{IP: "198.51.100.1", Since: 100}, {IP: "198.51.100.3", Since: 300}}},
		proto.OnlineUser{Sub: lim, Node: hy, IPs: []proto.OnlineIP{{IP: "198.51.100.2", Since: 200}}},
	)
	if len(stateOf(t, h, sid).Refuse) != 1 {
		t.Fatal("not turned away again")
	}
	b.must("PATCH", fmt.Sprintf("/api/users/%d", lim), map[string]any{"device_mode": "alert"}, 200)
	online(
		proto.OnlineUser{Sub: lim, Node: vless, IPs: []proto.OnlineIP{{IP: "198.51.100.1", Since: 100}, {IP: "198.51.100.3", Since: 300}}},
	)
	if st := stateOf(t, h, sid); len(st.Refuse) != 0 {
		t.Errorf("an alert-only limit turned devices away: %+v", st.Refuse)
	}
}
