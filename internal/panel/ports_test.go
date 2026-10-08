package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"meridian/internal/geo"
	"meridian/internal/proto"
)

func TestParsePortMap(t *testing.T) {
	for in, want := range map[string]string{
		"":                                  "",
		"20000-20019":                       "20000-20019",
		"40001-40010:10001-10010/udp":       "40001-40010:10001-10010/udp",
		"10022 -> 22, 20000 – 20009 / TCP":  "10022:22, 20000-20009/tcp",
		"40001-40010:10001":                 "40001-40010:10001-10010",
		"10022→22\n443=20001;8443 => 20002": "10022:22, 443:20001, 8443:20002",
		"20000-20019/tcp 20000-20019/udp":   "20000-20019/tcp, 20000-20019/udp",
		"30000/tcp+udp":                     "30000",
		"5000-5000":                         "5000",
	} {
		m, err := parsePortMap(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got := m.String(); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
		if again, err := parsePortMap(m.String()); err != nil || again.String() != want {
			t.Errorf("%q does not read back: %q %v", want, again.String(), err)
		}
	}
	many := make([]string, maxPortRanges+1)
	for i := range many {
		many[i] = fmt.Sprint(20000 + 2*i)
	}
	for in, want := range map[string]string{
		"abc":                          "not a port or range",
		"0":                            "from 1 to 65535",
		"70000":                        "from 1 to 65535",
		"65535:65535-65536":            "from 1 to 65535",
		"20010-20000":                  "low to high",
		"40001-40010:10001-10005":      "same number of ports",
		"20000-20010, 20005":           "public port 20005 is listed twice",
		"40001:10001, 40002:10001":     "port 10001 on the server is listed twice",
		"20000/udp, 30000:20000/udp":   "port 20000 on the server is listed twice",
		strings.Join(many, ","):        "at most 64",
		strings.Repeat("20000, ", 400): "too long",
		"1.2.3.4:443":                  "not a port or range",
		"443/sctp":                     "not a port or range",
		"-5":                           "not a port or range",
		"20000-":                       "not a port or range",
		"20000-20019:":                 "not a port or range",
	} {
		if _, err := parsePortMap(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%.40q: got %v, want %q", in, err, want)
		}
	}
	// the same numbers on another network are no clash
	if _, err := parsePortMap("20000-20019/tcp, 40000-40019:20000-20019/udp"); err != nil {
		t.Error(err)
	}
}

func TestPortMapLookups(t *testing.T) {
	m, err := parsePortMap("20000-20019, 40001-40010:10001-10010/udp, 10022:22/tcp, 8080:20080/tcp")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		local     int
		tcp, udp  bool
		pub       int
		reachable bool
	}{
		{20005, true, false, 20005, true},
		{20005, true, true, 20005, true},
		{10005, false, true, 40005, true},
		{10005, true, false, 0, false},
		{10005, true, true, 0, false}, // UDP is forwarded, TCP is not
		{22, true, false, 10022, true},
		{20080, true, false, 8080, true},
		{20080, false, true, 0, false},
		{443, true, false, 0, false},
	} {
		pub, ok := m.public(c.local, c.tcp, c.udp)
		if ok != c.reachable || (ok && pub != c.pub) {
			t.Errorf("public(%d, %v, %v) = %d %v, want %d %v", c.local, c.tcp, c.udp, pub, ok, c.pub, c.reachable)
		}
	}
	if l, ok := m.local(8080, true, false); !ok || l != 20080 {
		t.Errorf("local(8080) = %d %v", l, ok)
	}
	if l, ok := m.local(40003, false, true); !ok || l != 10003 {
		t.Errorf("local(40003/udp) = %d %v", l, ok)
	}
	if _, ok := m.local(443, true, false); ok {
		t.Error("local(443) should not be forwarded")
	}
	if msg := m.unreachable(8443, true, false); !strings.Contains(msg, "port 8443 is not one of the ports") {
		t.Errorf("8443: %q", msg)
	}
	if msg := m.unreachable(10005, true, false); !strings.Contains(msg, "needs TCP") {
		t.Errorf("10005/tcp: %q", msg)
	}
	if msg := m.unreachable(20001, true, true); msg != "" {
		t.Errorf("20001: %q", msg)
	}
	var free []int
	m.locals(true, false, func(p int) bool { free = append(free, p); return len(free) == 21 })
	if len(free) != 21 || free[0] != 20000 || free[19] != 20019 || free[20] != 20080 {
		t.Errorf("locals skip ports below 1024 and keep the listed order: %v", free)
	}

	// an ordinary server: every port, under its own number
	var none portMap
	if p, ok := none.public(443, true, true); !ok || p != 443 || none.unreachable(1, true, false) != "" ||
		none.acmePort() != 0 || none.acmeUnreachable("a.example.com") != "" {
		t.Error("an empty map must allow everything")
	}
	// Let's Encrypt's check: port 80 forwarded under its own number, another one, or not at all
	same, _ := parsePortMap("80, 20000-20009")
	other, _ := parsePortMap("80:20080/tcp, 20000-20009")
	if same.acmePort() != 0 || same.acmeUnreachable("a.example.com") != "" {
		t.Error("port 80 forwarded as 80")
	}
	if other.acmePort() != 20080 || other.acmeUnreachable("a.example.com") != "" {
		t.Errorf("port 80 forwarded to 20080: %d", other.acmePort())
	}
	if msg := m.acmeUnreachable("a.example.com"); !strings.Contains(msg, "does not forward") {
		t.Errorf("no port 80: %q", msg)
	}
}

// TestProviderPorts follows a NAT server: protocols and forwards get its forwarded ports, other
// ports are refused, links and proxy passes carry the public numbers, and Let's Encrypt works only
// where port 80 is forwarded.
func TestProviderPorts(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	out := b.must("POST", "/api/servers", map[string]any{"name": "NAT", "address": "198.51.100.7",
		"public_ports": "20000-20004, 443 -> 20010/tcp", "protocols": []string{"vless", "hysteria2", "shadowsocks"}}, 201)
	srv := out["server"].(map[string]any)
	sid := id(srv["id"])
	if srv["public_ports"] != "20000-20004, 443:20010/tcp" {
		t.Fatalf("stored as %q", srv["public_ports"])
	}
	ports := map[string]int64{}
	for _, n := range srv["nodes"].([]any) {
		n := n.(map[string]any)
		ports[n["kind"].(string)] = id(n["port"])
		if n["kind"] == "vless" && id(n["public_port"]) != 443 {
			t.Errorf("REALITY should take the port the provider forwards 443 to: %v", n)
		}
	}
	if ports["vless"] != 20010 || ports["hysteria2"] < 20000 || ports["hysteria2"] > 20004 ||
		ports["shadowsocks"] < 20000 || ports["shadowsocks"] > 20004 {
		t.Fatalf("ports: %v", ports)
	}

	// a port the provider does not forward, and one forwarded for TCP only to Hysteria2 (UDP)
	path := fmt.Sprintf("/api/servers/%d/nodes", sid)
	if code, m, _ := b.do("POST", path, map[string]any{"kind": "trojan", "port": 8443}); code != 400 ||
		!strings.Contains(fmt.Sprint(m["error"]), "not one of the ports") {
		t.Errorf("8443: %d %v", code, m)
	}
	if code, m, _ := b.do("POST", path, map[string]any{"kind": "hysteria2", "port": 20010}); code != 400 ||
		!strings.Contains(fmt.Sprint(m["error"]), "needs UDP") {
		t.Errorf("hysteria2 on a TCP-only port: %d %v", code, m)
	}
	// Let's Encrypt cannot check a domain without port 80
	acme := map[string]any{"kind": "trojan", "settings": map[string]any{"security": "tls", "cert_mode": "acme", "sni": "nat.example.com"}}
	if code, m, _ := b.do("POST", path, acme); code != 400 || !strings.Contains(fmt.Sprint(m["error"]), "TCP port 80") {
		t.Errorf("acme without port 80: %d %v", code, m)
	}

	// links carry the public number
	var subs []map[string]any
	_, _, raw := b.do("POST", "/api/users", map[string]any{"name": "dora"})
	_ = json.Unmarshal(raw, &subs)
	link := subs[0]["link"].(string)
	resp, err := http.Get(h.srv.URL + link[strings.Index(link, "/s/"):] + "?client=singbox")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var sb struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(body, &sb); err != nil {
		t.Fatalf("sing-box: %v %s", err, body)
	}
	seen := map[string]int64{}
	for _, o := range sb.Outbounds {
		if o["server"] == "198.51.100.7" {
			seen[o["type"].(string)] = id(o["server_port"])
		}
	}
	if seen["vless"] != 443 || seen["hysteria2"] != ports["hysteria2"] || seen["shadowsocks"] != ports["shadowsocks"] {
		t.Errorf("link ports: %v (server ports %v)", seen, ports)
	}

	// forwards: picked from the forwarded ports, others refused
	fpath := fmt.Sprintf("/api/servers/%d/forwards", sid)
	if code, m, _ := b.do("POST", fpath, map[string]any{"target": "203.0.113.9:443", "listen_port": 9000, "engine": "realm"}); code != 400 ||
		!strings.Contains(fmt.Sprint(m["error"]), "not one of the ports") {
		t.Errorf("forward on 9000: %d %v", code, m)
	}
	f := b.must("POST", fpath, map[string]any{"target": "203.0.113.9:443", "network": "tcp", "engine": "realm"}, 201)
	if lp := id(f["listen_port"]); lp < 20000 || lp > 20004 {
		t.Errorf("forward picked %d", lp)
	}

	// forwarding port 80 elsewhere: Let's Encrypt works, and the agent answers its check there
	sp := fmt.Sprintf("/api/servers/%d", sid)
	b.must("PATCH", sp, map[string]any{"public_ports": "20000-20004, 443:20010/tcp, 80:20080/tcp"}, 200)
	n := b.must("POST", path, acme, 201)
	if id(n["port"]) < 20000 || id(n["port"]) > 20004 {
		t.Errorf("trojan picked %v", n["port"])
	}
	if notes := fmt.Sprint(n["notes"]); !strings.Contains(notes, "TCP port 20080") {
		t.Errorf("notes should name the port the check arrives on: %s", notes)
	}
	st, err := h.p.compileServer(context.Background(), sid)
	if err != nil || st.Agent.ACMEPort != 20080 {
		t.Errorf("acme port in the state: %v %v", st.Agent.ACMEPort, err)
	}

	// the provider's ports change: what can no longer be reached is listed
	v := b.must("PATCH", sp, map[string]any{"public_ports": "443:20010/tcp, 80:20080/tcp"}, 200)["server"].(map[string]any)
	limits := fmt.Sprint(v["limits"])
	if !strings.Contains(limits, "on port 20000 can't be reached") || !strings.Contains(limits, "The forward on port") {
		t.Errorf("limits: %s", limits)
	}
	if code, m, _ := b.do("PATCH", sp, map[string]any{"public_ports": "20000-20010, 20005"}); code != 400 ||
		!strings.Contains(fmt.Sprint(m["error"]), "listed twice") {
		t.Errorf("bad list: %d %v", code, m)
	}
	// back to an ordinary server: every port, links with the server's own numbers
	v = b.must("PATCH", sp, map[string]any{"public_ports": ""}, 200)["server"].(map[string]any)
	if v["public_ports"] != "" || v["limits"] != nil {
		t.Errorf("cleared: %q %v", v["public_ports"], v["limits"])
	}
	for _, n := range v["nodes"].([]any) {
		if pp := n.(map[string]any)["public_port"]; pp != nil {
			t.Errorf("public_port %v without a map", pp)
		}
	}
}

// TestProviderPortsPass: a proxy pass to a NAT server connects to its public port.
func TestProviderPortsPass(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	exit := b.must("POST", "/api/servers", map[string]any{"name": "Exit", "address": "198.51.100.8",
		"public_ports": "443:20010/tcp", "protocols": []string{"vless"}}, 201)["server"].(map[string]any)
	exitNode := id(exit["nodes"].([]any)[0].(map[string]any)["id"])
	entry := b.must("POST", "/api/servers", map[string]any{"name": "Entry", "address": "203.0.113.30"}, 201)["server"].(map[string]any)
	b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", id(entry["id"])), map[string]any{"kind": "shadowsocks", "pass_node": exitNode}, 201)
	st, err := h.p.compileServer(context.Background(), id(entry["id"]))
	if err != nil {
		t.Fatal(err)
	}
	base := string(st.Xray.Base)
	if !strings.Contains(base, `"address":"198.51.100.8"`) || !strings.Contains(base, `"port":443`) {
		t.Errorf("pass outbound should reach the exit's public port: %s", base)
	}
	// the exit's ports change: the entry is rendered again with the new number
	b.must("PATCH", fmt.Sprintf("/api/servers/%d", id(exit["id"])), map[string]any{"public_ports": "8443:20010/tcp"}, 200)
	st, _ = h.p.compileServer(context.Background(), id(entry["id"]))
	if base := string(st.Xray.Base); !strings.Contains(base, `"port":8443`) {
		t.Errorf("after the change: %s", base)
	}
}

// TestHandSetIPLocation: an IP set by hand as the server's address is looked up in DB-IP, right
// away and on later reports; a domain leaves the place to the agent's IP.
func TestHandSetIPLocation(t *testing.T) {
	h := newHarness(t)
	h.p.geo = geo.Static(map[string]*geo.Info{
		"198.51.100.20": {Country: "JP", City: "Tokyo", Lat: 35.69, Lon: 139.69},
		"203.0.113.50":  {Country: "DE", City: "Frankfurt am Main", Lat: 50.11, Lon: 8.68},
	})
	b := h.browser()
	b.login("owner", "owner-password-1")
	s := b.must("POST", "/api/servers", map[string]any{"name": "po0", "address": "198.51.100.20"}, 201)["server"].(map[string]any)
	sid := id(s["id"])
	if s["country"] != "JP" || s["city"] != "Tokyo" || s["lat"] == nil || s["loc_from"] != "198.51.100.20" {
		t.Fatalf("created: %v %v %v %v", s["country"], s["city"], s["lat"], s["loc_from"])
	}
	sp := fmt.Sprintf("/api/servers/%d", sid)
	v := b.must("PATCH", sp, map[string]any{"address": "203.0.113.50"}, 200)["server"].(map[string]any)
	if v["country"] != "DE" || v["city"] != "Frankfurt am Main" {
		t.Errorf("changed: %v %v", v["country"], v["city"])
	}
	// later reports keep the hand-set IP's place, whatever IP the agent sees
	srv, err := h.p.serverByID(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	srv.IPv4 = "198.51.100.20"
	if cc, _, _, _ := h.p.locateServer(srv, &proto.Hello{IPv4: "198.51.100.20"}); cc != "DE" {
		t.Errorf("report: %q", cc)
	}
	// a place set by hand wins
	b.must("PATCH", sp, map[string]any{"location": map[string]any{"cc": "JP", "city": "Osaka", "lat": 34.69, "lon": 135.5}}, 200)
	v = b.must("PATCH", sp, map[string]any{"address": "198.51.100.20"}, 200)["server"].(map[string]any)
	if v["city"] != "Osaka" || v["loc_from"] != nil {
		t.Errorf("manual place: %v %v", v["city"], v["loc_from"])
	}
	// back to the IP database: the hand-set IP again
	v = b.must("PATCH", sp, map[string]any{"location": map[string]any{}, "auto_location": true}, 200)["server"].(map[string]any)
	if v["country"] != "JP" || v["city"] != "Tokyo" {
		t.Errorf("auto: %v %v", v["country"], v["city"])
	}
	// a private IP is in no IP database: the agent's IP decides (none reported yet here, so no place)
	v = b.must("PATCH", sp, map[string]any{"address": "192.168.64.2"}, 200)["server"].(map[string]any)
	if v["country"] != "" || v["loc_from"] != nil {
		t.Errorf("private IP: %v %v", v["country"], v["loc_from"])
	}
	// a domain: the same
	v = b.must("PATCH", sp, map[string]any{"address": "nat.example.com"}, 200)["server"].(map[string]any)
	if v["country"] != "" || v["loc_from"] != nil {
		t.Errorf("domain: %v %v", v["country"], v["loc_from"])
	}
}

func TestTargetSummary(t *testing.T) {
	if _, ok := targetSummary(`{"auto":true,"node_id":3,"results":[{"target":"www.apple.com","ok":true,"ms":60}]}`); ok {
		t.Error("an automatic check has its own event")
	}
	for out, want := range map[string]string{
		`{"node_id":3,"results":[{"target":"www.apple.com","ok":true,"ms":60}]}`:                  "REALITY camouflage www.apple.com works from this server (60 ms)",
		`{"node_id":3,"results":[{"target":"www.apple.com","ok":false,"error":"timeout\nmore"}]}`: "REALITY camouflage www.apple.com does not work from this server: timeout",
		"not json\nsecond line": "not json",
	} {
		if got, ok := targetSummary(out); !ok || got != want {
			t.Errorf("%.30q: got %q %v, want %q", out, got, ok, want)
		}
	}
}

// TestOwnListener: a running forward or protocol is never "in use by another program" because of its
// own listener - editing its target, giving it an address or turning on UDP works.
func TestOwnListener(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	srv := b.must("POST", "/api/servers", map[string]any{"name": "Own", "address": "203.0.113.97", "protocols": []string{}}, 201)["server"].(map[string]any)
	sid := id(srv["id"])
	if _, err := h.p.db.Exec1(`UPDATE servers SET first_seen_at = ?, caps = ?, addrs = ? WHERE id = ?`, now(),
		`{"systemd":true,"nftables":true,"certs":true}`, `["203.0.113.97","203.0.113.98"]`, sid); err != nil {
		t.Fatal(err)
	}
	fwd := b.must("POST", fmt.Sprintf("/api/servers/%d/forwards", sid), map[string]any{"engine": "realm", "listen_port": 8080,
		"target": "10.0.0.5:80"}, 201)
	socks := b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": "socks", "port": 1080,
		"settings": map[string]any{"udp": false}}, 201)
	vless := b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": "vless", "port": 443,
		"settings": map[string]any{}}, 201)
	h.p.live.put(sid, proto.Live{Ports: []int{8080, 1080, 443, 22}}) // the server reports them listening
	b.must("PATCH", fmt.Sprintf("/api/forwards/%d", id(fwd["id"])), map[string]any{"target": "10.0.0.6:80", "listen_port": 8080,
		"network": "tcp+udp"}, 200)
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", id(socks["id"])), map[string]any{"settings": map[string]any{"udp": true}}, 200)
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", id(vless["id"])), map[string]any{"bind_ip": "203.0.113.98"}, 200)
	// another program's port is still refused
	if code, m, _ := b.do("PATCH", fmt.Sprintf("/api/forwards/%d", id(fwd["id"])), map[string]any{"listen_port": 22}); code != 409 ||
		!strings.Contains(fmt.Sprint(m["error"]), "another program") {
		t.Errorf("a port another program holds: %d %v", code, m)
	}
}

// TestACMEPortShown: while a protocol gets its certificate from Let's Encrypt, the server says which
// TCP port the check arrives on (80, or where the provider forwards it), so its firewall can let it in.
func TestACMEPortShown(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "ACME", "address": "203.0.113.99"}, 201)["server"].(map[string]any)["id"])
	get := func() any {
		return b.must("GET", fmt.Sprintf("/api/servers/%d", sid), nil, 200)["server"].(map[string]any)["acme_port"]
	}
	if v := get(); v != nil {
		t.Fatalf("no Let's Encrypt protocol: %v", v)
	}
	n := b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": "trojan",
		"settings": map[string]any{"security": "tls", "cert_mode": "acme", "sni": "proxy.example.com"}}, 201)
	if v := get(); v != float64(80) {
		t.Errorf("acme: %v", v)
	}
	b.must("PATCH", fmt.Sprintf("/api/servers/%d", sid), map[string]any{"public_ports": "80:10080, 443, 20000-20010"}, 200)
	if v := get(); v != float64(10080) {
		t.Errorf("forwarded 80: %v", v)
	}
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", id(n["id"])), map[string]any{"enabled": false}, 200)
	if v := get(); v != nil {
		t.Errorf("turned off: %v", v)
	}
}
