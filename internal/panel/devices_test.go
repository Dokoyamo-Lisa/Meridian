package panel

import (
	"encoding/json"
	"strings"
	"testing"

	"meridian/internal/proto"
)

// A device is one address, however many servers and protocols it uses: the user's page, the
// overview and the IP limit all count it once. Addresses from agents are cleaned first, and nothing
// is counted for users the panel does not have.
func TestDevicesCountedOncePerAddress(t *testing.T) {
	h := newHarness(t)
	owner := h.browser()
	owner.login("owner", "owner-password-1")
	srv := func(name, addr string, protos ...string) int64 {
		m := owner.must("POST", "/api/servers", map[string]any{"name": name, "address": addr, "protocols": protos}, 201)
		return id(m["server"].(map[string]any)["id"])
	}
	tokyo, osaka := srv("Tokyo", "203.0.113.1", "vless", "hysteria2"), srv("Osaka", "203.0.113.2", "vless")
	var made []map[string]any
	_, _, raw := owner.do("POST", "/api/users", map[string]any{"name": "Alice", "username": "alice", "password": "alice-password-1"})
	if err := json.Unmarshal(raw, &made); err != nil || len(made) != 1 {
		t.Fatalf("user: %s", raw)
	}
	alice := id(made[0]["id"])
	nodes := func(server int64) []int64 {
		var out []int64
		rows, err := h.p.db.Query(`SELECT id FROM nodes WHERE server_id = ? ORDER BY id`, server)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var n int64
			_ = rows.Scan(&n)
			out = append(out, n)
		}
		return out
	}
	tn, on := nodes(tokyo), nodes(osaka)
	if len(tn) != 2 || len(on) != 1 {
		t.Fatalf("nodes %v %v", tn, on)
	}
	if _, err := h.p.db.Exec(`UPDATE servers SET online = 1, first_seen_at = ? WHERE id IN (?, ?)`, now(), tokyo, osaka); err != nil {
		t.Fatal(err)
	}
	report := func(server int64, lv proto.Live) {
		sanitizeLive(&lv)
		h.p.live.put(server, lv)
	}
	// the phone (198.51.100.7) on both Tokyo protocols - once spelled as an IPv4-mapped address - and
	// on Osaka; the laptop (2001:db8::5) on Tokyo; a stranger (no such user) and garbage
	report(tokyo, proto.Live{Online: []proto.OnlineUser{
		{Sub: alice, Node: tn[0], IPs: []proto.OnlineIP{{IP: "::ffff:198.51.100.7", Since: 200}, {IP: "198.51.100.7", Since: 100}, {IP: "not an address"}}},
		{Sub: alice, Node: tn[1], IPs: []proto.OnlineIP{{IP: "198.51.100.7", Since: 300}, {IP: "2001:db8::5", Since: 400}}},
		{Sub: 9999, Node: tn[0], IPs: []proto.OnlineIP{{IP: "192.0.2.50", Since: 50}}},
	}})
	report(osaka, proto.Live{Online: []proto.OnlineUser{{Sub: alice, Node: on[0], IPs: []proto.OnlineIP{{IP: "198.51.100.7", Since: 500}}}}})

	user := h.browser()
	user.login("alice", "alice-password-1")
	var me portalMe
	_, _, raw = user.do("GET", "/api/portal/me", nil)
	if err := json.Unmarshal(raw, &me); err != nil {
		t.Fatal(err)
	}
	if len(me.Devices) != 2 {
		t.Fatalf("devices: %s", raw)
	}
	phone := me.Devices[0]
	if phone.IP != "198.51.100.7" || phone.Since != 100 || len(phone.Via) != 3 || me.Devices[1].IP != "2001:db8::5" {
		t.Errorf("devices: %+v", me.Devices)
	}
	// named as the server list (and the user's apps) name them
	var via []string
	for _, v := range phone.Via {
		via = append(via, v.Server+" "+v.Protocol)
	}
	if strings.Join(via, ", ") != "Tokyo REALITY, Tokyo Hy2, Osaka REALITY" {
		t.Errorf("via: %v", via)
	}
	if len(me.Servers) != 2 {
		t.Fatalf("servers: %+v", me.Servers)
	}
	for _, s := range me.Servers {
		if want := map[int64]int{tokyo: 2, osaka: 1}[s.ID]; s.Devices != want {
			t.Errorf("%s: %d devices, want %d", s.Name, s.Devices, want)
		}
	}

	ov := owner.must("GET", "/api/overview", nil, 200)
	if id(ov["online_ips"]) != 2 || id(ov["online_users"]) != 1 {
		t.Errorf("overview: %v online IPs, %v users", ov["online_ips"], ov["online_users"])
	}
	if len(ov["map"].([]any)) != 2 {
		t.Fatalf("map: %v", ov["map"])
	}
	for _, m := range ov["map"].([]any) {
		m := m.(map[string]any)
		// per server: distinct addresses there (the stranger's included - it is connected)
		if want := map[int64]int64{tokyo: 3, osaka: 1}[id(m["id"])]; id(m["online"]) != want {
			t.Errorf("%v: %v online, want %d", m["name"], m["online"], want)
		}
	}
}

func TestLocateServer(t *testing.T) {
	p := &Panel{} // no IP database: every lookup comes back empty
	la, lo := 35.68, 139.69
	known := func() *Server {
		return &Server{IPv4: "203.0.113.1", Country: "JP", City: "Tokyo", Lat: &la, Lon: &lo}
	}
	// same address, complete: kept without asking
	if cc, city, lat, _ := p.locateServer(known(), &proto.Hello{IPv4: "203.0.113.1"}); cc != "JP" || city != "Tokyo" || lat == nil {
		t.Errorf("kept: %q %q %v", cc, city, lat)
	}
	// a new address that cannot be placed yet: no place rather than the old one
	if cc, city, lat, lon := p.locateServer(known(), &proto.Hello{IPv4: "198.51.100.9"}); cc != "" || city != "" || lat != nil || lon != nil {
		t.Errorf("moved: %q %q %v %v", cc, city, lat, lon)
	}
	// set by hand: never touched
	s := known()
	s.LocManual = true
	if cc, _, lat, _ := p.locateServer(s, &proto.Hello{IPv4: "198.51.100.9"}); cc != "JP" || lat == nil {
		t.Errorf("manual: %q %v", cc, lat)
	}
	// an IPv6-only host is placed by its IPv6 address
	s = &Server{IPv6: "2001:db8::1", Country: "DE", Lat: &la, Lon: &lo}
	if cc, _, lat, _ := p.locateServer(s, &proto.Hello{IPv6: "2001:db8::1"}); cc != "DE" || lat == nil {
		t.Errorf("IPv6 kept: %q %v", cc, lat)
	}
	if cc, _, lat, _ := p.locateServer(s, &proto.Hello{IPv6: "2001:db8::2"}); cc != "" || lat != nil {
		t.Errorf("IPv6 moved: %q %v", cc, lat)
	}
}
