package panel

import (
	"fmt"
	"testing"

	"meridian/internal/proto"
)

// TestLiveSpeed: Dana connects to Tokyo, whose protocol passes her traffic on to Osaka (a proxy
// pass). Tokyo measures her and her phone's address; Osaka sees only Tokyo's address and counts that
// traffic as the pass's own (no user). Her speed is Tokyo's, once - not doubled by the exit - and
// Tokyo's address never shows as a device. Eli, on the same phone network, shares the address. A
// server whose agent does not measure is listed; a stale report counts nothing.
func TestLiveSpeed(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	var ids []int64
	for i, name := range []string{"Tokyo", "Osaka", "Old"} {
		ids = append(ids, id(b.must("POST", "/api/servers", map[string]any{"name": name, "address": fmt.Sprintf("203.0.113.%d", 10+i)}, 201)["server"].(map[string]any)["id"]))
	}
	tokyo, osaka, old := ids[0], ids[1], ids[2]
	for _, sid := range ids {
		caps := `{"nftables":true,"rates":true}`
		if sid == old {
			caps = `{"nftables":true}`
		}
		h.p.db.Exec1(`UPDATE servers SET caps = ?, online = 1, first_seen_at = ? WHERE id = ?`, caps, now(), sid)
	}
	var subs []int64
	for _, name := range []string{"Dana", "Eli", "Fay"} {
		b.must("POST", "/api/users", map[string]any{"name": name}, 201)
		var sid int64
		h.p.db.QueryRow(`SELECT id FROM subs WHERE name = ?`, name).Scan(&sid)
		subs = append(subs, sid)
	}
	dana, eli := subs[0], subs[1]
	const phone, entry = "198.51.100.7", "203.0.113.10"
	h.p.live.put(tokyo, proto.Live{TS: now(),
		Online:  []proto.OnlineUser{{Sub: dana, Node: 1, IPs: []proto.OnlineIP{{IP: phone}}}, {Sub: eli, Node: 1, IPs: []proto.OnlineIP{{IP: phone}}}},
		Rates:   []proto.UserRate{{Sub: dana, Up: 2_000, Down: 500_000}, {Sub: eli, Up: 100, Down: 1_000}, {Sub: 9999, Up: 1, Down: 1}},
		IPRates: []proto.IPRate{{IP: phone, Up: 2_100, Down: 501_000}, {IP: "not an address", Up: 5, Down: 5}},
	})
	h.p.live.put(osaka, proto.Live{TS: now(), // the exit: the pass's traffic is no user's; Tokyo is a sender
		IPRates: []proto.IPRate{{IP: entry, Up: 2_100, Down: 501_000}},
	})
	h.p.live.put(old, proto.Live{TS: now()})

	v := b.must("GET", "/api/live/speed", nil, 200)
	users := v["users"].([]any)
	if len(users) != 2 {
		t.Fatalf("users with speed: %v", users)
	}
	d := users[0].(map[string]any)
	if d["user"] != "Dana" || id(d["up"]) != 2_000 || id(d["down"]) != 500_000 {
		t.Errorf("Dana, once: %v", d)
	}
	ips := d["ips"].([]any)
	if len(ips) != 1 {
		t.Fatalf("Dana's addresses: %v", ips)
	}
	ip := ips[0].(map[string]any)
	if ip["ip"] != phone || ip["server"] != "Tokyo" || id(ip["down"]) != 501_000 || id(ip["shared"]) != 1 {
		t.Errorf("her phone: %v", ip)
	}
	if id(v["up"]) != 2_100 || id(v["down"]) != 501_000 {
		t.Errorf("all users: up %v, down %v", v["up"], v["down"])
	}
	if fmt.Sprint(v["unmeasured"]) != "[Old]" {
		t.Errorf("servers that do not measure: %v", v["unmeasured"])
	}
	for _, u := range users {
		for _, x := range u.(map[string]any)["ips"].([]any) {
			if x.(map[string]any)["ip"] == entry {
				t.Errorf("the proxy pass's entry server shows as a device: %v", x)
			}
		}
	}

	// a report older than three intervals: nothing measured now
	h.p.live.mu.Lock()
	h.p.live.m[tokyo].At = now() - 600
	h.p.live.mu.Unlock()
	if v := b.must("GET", "/api/live/speed", nil, 200); len(v["users"].([]any)) != 0 {
		t.Errorf("a stale report still has speed: %v", v["users"])
	}
}
