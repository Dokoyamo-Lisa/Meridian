package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"meridian/internal/proto"
)

// hello reports a server's public addresses the way its agent does.
func hello(t *testing.T, h *harness, sid int64, v4, v6 string) {
	t.Helper()
	srv, err := h.p.serverByID(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.p.ingest(context.Background(), srv, &proto.Report{Instance: "i", Hello: &proto.Hello{AgentVersion: Version,
		IPv4: v4, IPv6: v6, Caps: proto.Caps{Certs: true, Relay: true}}}); err != nil {
		t.Fatal(err)
	}
}

// TestAddressChangesFollowed: a proxy pass to a server that is reached by its reported address
// follows a new IPv4 and, on a server set to IPv6 only, a new IPv6 - at once, with an event each.
func TestAddressChangesFollowed(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	exit := id(b.must("POST", "/api/servers", map[string]any{"name": "Exit"}, 201)["server"].(map[string]any)["id"])
	entry := id(b.must("POST", "/api/servers", map[string]any{"name": "Entry", "address": "203.0.113.5"}, 201)["server"].(map[string]any)["id"])
	hello(t, h, exit, "198.51.100.10", "2001:db8::10")
	hello(t, h, entry, "203.0.113.5", "2001:db8::5")
	xn := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", exit), map[string]any{"kind": "vless"}, 201)["id"])
	en := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", entry), map[string]any{"kind": "vless"}, 201)["id"])
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", en), map[string]any{"pass_node": xn}, 200)

	if c := compileX(t, h, entry); !strings.Contains(c.raw, "198.51.100.10") {
		t.Fatalf("the pass does not go to the exit's IPv4: %s", c.raw)
	}
	hello(t, h, exit, "198.51.100.11", "2001:db8::10")
	if c := compileX(t, h, entry); !strings.Contains(c.raw, "198.51.100.11") || strings.Contains(c.raw, "198.51.100.10") {
		t.Errorf("a new IPv4 is not followed: %s", c.raw)
	}

	b.must("PATCH", fmt.Sprintf("/api/servers/%d", exit), map[string]any{"ip_version": "ipv6"}, 200)
	if c := compileX(t, h, entry); !strings.Contains(c.raw, "2001:db8::10") {
		t.Fatalf("an IPv6-only exit is not reached at its IPv6: %s", c.raw)
	}
	hello(t, h, exit, "198.51.100.11", "2001:db8::11")
	if c := compileX(t, h, entry); !strings.Contains(c.raw, "2001:db8::11") || strings.Contains(c.raw, "2001:db8::10\"") {
		t.Errorf("a new IPv6 is not followed: %s", c.raw)
	}
	var n int
	h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE server_id = ? AND message LIKE '%IPv6%changed%'`, exit).Scan(&n)
	if n == 0 {
		t.Error("no event for the IPv6 change")
	}
}

// TestDynamicDNSName: a server whose IP address changes needs a name as its address; the panel says
// when the name points elsewhere or does not resolve, and only for such servers.
func TestDynamicDNSName(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	var mu sync.Mutex
	answer := map[string][]netip.Addr{}
	old := lookupIP
	lookupIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
		mu.Lock()
		defer mu.Unlock()
		if a, ok := answer[host]; ok {
			return a, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	t.Cleanup(func() { lookupIP = old })

	if code, _, raw := b.do("POST", "/api/servers", map[string]any{"name": "Home", "address": "203.0.113.9", "ddns": true}); code != 400 ||
		!strings.Contains(string(raw), "name") {
		t.Errorf("an IP as a dynamic DNS address: %d %s", code, raw)
	}
	if code, _, raw := b.do("POST", "/api/servers", map[string]any{"name": "Home", "address": "home.example.com", "ddns": true, "ddns_cloudflare": true}); code != 400 ||
		!strings.Contains(string(raw), "Cloudflare") {
		t.Errorf("Cloudflare without a token: %d %s", code, raw)
	}
	mu.Lock()
	answer["home.example.com"] = []netip.Addr{netip.MustParseAddr("198.51.100.7")}
	mu.Unlock()
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "Home", "address": "home.example.com", "ddns": true}, 201)["server"].(map[string]any)["id"])
	hello(t, h, sid, "203.0.113.9", "")
	srv, _ := h.p.serverByID(context.Background(), sid)
	h.p.checkDNS(context.Background(), srv)
	v := serverJSON(b, sid)
	dns, _ := v["dns"].(map[string]any)
	if dns == nil || !strings.Contains(fmt.Sprint(dns["problems"]), "198.51.100.7") || !strings.Contains(fmt.Sprint(dns["problems"]), "203.0.113.9") {
		t.Fatalf("a name pointing elsewhere is not reported: %v", v["dns"])
	}

	mu.Lock()
	answer["home.example.com"] = []netip.Addr{netip.MustParseAddr("203.0.113.9")}
	mu.Unlock()
	h.p.checkDNS(context.Background(), srv)
	if dns := serverJSON(b, sid)["dns"].(map[string]any); dns["problems"] != nil {
		t.Errorf("a name that points at the server still has problems: %v", dns["problems"])
	}

	mu.Lock()
	delete(answer, "home.example.com")
	mu.Unlock()
	h.p.checkDNS(context.Background(), srv)
	if dns := serverJSON(b, sid)["dns"].(map[string]any); !strings.Contains(fmt.Sprint(dns["problems"]), "resolve") {
		t.Errorf("a name that does not resolve: %v", dns["problems"])
	}

	// a server whose address is a name but not dynamic (a CDN name, say) is never checked
	other := id(b.must("POST", "/api/servers", map[string]any{"name": "CDN", "address": "cdn.example.com"}, 201)["server"].(map[string]any)["id"])
	if v := serverJSON(b, other); v["dns"] != nil {
		t.Errorf("a name that is not dynamic DNS got a check: %v", v["dns"])
	}
}

// fakeCloudflare is a stand-in for Cloudflare's API: one zone, its records, and every change made.
type fakeCloudflare struct {
	mu      sync.Mutex
	token   string
	zone    string
	records map[string]cfRecord
	changes []string
	refuse  bool
	next    int
}

func (f *fakeCloudflare) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(status int, ok bool, result any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		body := map[string]any{"success": ok, "errors": []any{}, "result": result}
		if !ok {
			body["errors"] = []any{map[string]any{"code": 9109, "message": "Invalid access token " + f.token}}
		}
		json.NewEncoder(w).Encode(body)
	}
	if f.refuse || r.Header.Get("Authorization") != "Bearer "+f.token {
		reply(http.StatusForbidden, false, nil)
		return
	}
	zid := strings.Repeat("a", 32)
	switch {
	case r.Method == "GET" && r.URL.Path == "/zones":
		if r.URL.Query().Get("name") == f.zone {
			reply(200, true, []cfZone{{ID: zid, Name: f.zone}})
			return
		}
		reply(200, true, []cfZone{})
	case r.Method == "GET" && r.URL.Path == "/zones/"+zid+"/dns_records":
		var out []cfRecord
		for _, rec := range f.records {
			if rec.Name == r.URL.Query().Get("name") {
				out = append(out, rec)
			}
		}
		reply(200, true, out)
	case r.Method == "POST" && r.URL.Path == "/zones/"+zid+"/dns_records":
		var in struct {
			Type, Name, Content string
			Proxied             bool
		}
		json.NewDecoder(r.Body).Decode(&in)
		f.next++
		rid := fmt.Sprintf("%032x", f.next)
		f.records[rid] = cfRecord{ID: rid, Type: in.Type, Name: in.Name, Content: in.Content, Proxied: in.Proxied}
		f.changes = append(f.changes, fmt.Sprintf("add %s %s %s proxied=%v", in.Type, in.Name, in.Content, in.Proxied))
		reply(200, true, f.records[rid])
	case strings.HasPrefix(r.URL.Path, "/zones/"+zid+"/dns_records/"):
		rid := strings.TrimPrefix(r.URL.Path, "/zones/"+zid+"/dns_records/")
		rec, ok := f.records[rid]
		if !ok {
			reply(404, false, nil)
			return
		}
		switch r.Method {
		case "PATCH":
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			if c, ok := in["content"].(string); ok {
				rec.Content = c
			}
			if p, ok := in["proxied"].(bool); ok {
				rec.Proxied = p
			}
			f.records[rid] = rec
			f.changes = append(f.changes, fmt.Sprintf("set %s %s %s proxied=%v", rec.Type, rec.Name, rec.Content, rec.Proxied))
			reply(200, true, rec)
		case "DELETE":
			delete(f.records, rid)
			f.changes = append(f.changes, fmt.Sprintf("delete %s %s %s", rec.Type, rec.Name, rec.Content))
			reply(200, true, map[string]string{"id": rid})
		}
	default:
		reply(404, false, nil)
	}
}

func (f *fakeCloudflare) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.changes
	f.changes = nil
	return out
}

// TestCloudflareDynamicDNS: the panel keeps exactly the server's name in Cloudflare - adding,
// changing and removing its A and AAAA records as the server's addresses change, never proxied,
// never another name - and the token never shows in answers, events or messages.
func TestCloudflareDynamicDNS(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	token := "cfTOKEN_" + strings.Repeat("x", 32)
	other := fmt.Sprintf("%032x", 999)
	f := &fakeCloudflare{token: token, zone: "example.com", records: map[string]cfRecord{
		other: {ID: other, Type: "A", Name: "www.example.com", Content: "192.0.2.1"},
	}}
	stand := httptest.NewServer(f)
	defer stand.Close()
	old := cloudflareAPI
	cloudflareAPI = stand.URL
	t.Cleanup(func() { cloudflareAPI = old })

	// a token needs the browser; an API token may not set it
	full := b.must("POST", "/api/tokens", map[string]any{"name": "rw", "scope": "full"}, 201)["token"].(string)
	if code, _, _ := h.bearer(full).do("PUT", "/api/settings/cloudflare", map[string]any{"token": token}); code != 403 {
		t.Errorf("an API token set the Cloudflare token: %d", code)
	}
	v := b.must("PUT", "/api/settings/cloudflare", map[string]any{"token": token}, 200)
	if v["token_set"] != true {
		t.Fatalf("token not saved: %v", v)
	}
	if _, _, raw := b.do("GET", "/api/settings/cloudflare", nil); strings.Contains(string(raw), token) {
		t.Fatal("the token came back")
	}

	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "Home", "address": "home.example.com", "ddns": true, "ddns_cloudflare": true}, 201)["server"].(map[string]any)["id"])
	sync := func() {
		t.Helper()
		srv, err := h.p.serverByID(context.Background(), sid)
		if err != nil {
			t.Fatal(err)
		}
		h.p.cloudflareSync(context.Background(), srv)
	}
	sync() // nothing reported yet: nothing touched
	if c := f.took(); len(c) != 0 {
		t.Fatalf("records changed before the server reported its addresses: %v", c)
	}

	hello(t, h, sid, "198.51.100.20", "2001:db8::20")
	sync()
	if c := strings.Join(f.took(), "; "); !strings.Contains(c, "add A home.example.com 198.51.100.20 proxied=false") ||
		!strings.Contains(c, "add AAAA home.example.com 2001:db8::20 proxied=false") {
		t.Fatalf("create: %s", c)
	}
	sync()
	if c := f.took(); len(c) != 0 {
		t.Errorf("records changed although they were right: %v", c)
	}

	hello(t, h, sid, "198.51.100.21", "")
	sync()
	if c := strings.Join(f.took(), "; "); !strings.Contains(c, "set A home.example.com 198.51.100.21") || strings.Contains(c, "AAAA") {
		t.Errorf("update (the agent reported no IPv6 this time, which keeps the known one): %s", c)
	}
	b.must("PATCH", fmt.Sprintf("/api/servers/%d", sid), map[string]any{"ip_version": "ipv4"}, 200)
	sync()
	if c := strings.Join(f.took(), "; "); !strings.Contains(c, "delete AAAA home.example.com 2001:db8::20") {
		t.Errorf("an IPv4-only server keeps its AAAA record: %s", c)
	}
	if _, ok := f.records[other]; !ok {
		t.Error("another name's record was touched")
	}

	// a proxied record becomes DNS only
	for k, r := range f.records {
		if r.Name == "home.example.com" {
			r.Proxied = true
			f.records[k] = r
		}
	}
	sync()
	if c := strings.Join(f.took(), "; "); !strings.Contains(c, "proxied=false") {
		t.Errorf("a proxied record stays proxied: %s", c)
	}

	// Cloudflare refusing: the server page and the timeline say so, without the token
	f.mu.Lock()
	f.refuse = true
	f.mu.Unlock()
	hello(t, h, sid, "198.51.100.22", "")
	sync()
	sv := serverJSON(b, sid)
	cf, _ := sv["dns"].(map[string]any)["cloudflare"].(map[string]any)
	if cf == nil || cf["state"] != "failed" {
		t.Fatalf("a refusal is not shown: %v", sv["dns"])
	}
	var leaked int
	h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE message LIKE ?`, "%"+token+"%").Scan(&leaked)
	if leaked > 0 || strings.Contains(fmt.Sprint(sv), token) {
		t.Error("the token leaked into an event or the server view")
	}
	var failed int
	h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind = 'ddns_failed'`).Scan(&failed)
	sync() // the same failure again records nothing new
	var again int
	h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind = 'ddns_failed'`).Scan(&again)
	if failed != 1 || again != 1 {
		t.Errorf("failure events: %d then %d", failed, again)
	}
}

// TestIPv6OnlyReach: a server without IPv6 cannot pass through a protocol that is reached at an IPv6
// address only - refused with what to do - while a server with both kinds, or an exit with a domain
// name, works; a server with IPv6 only reaches a dual-stack exit at its IPv6 address.
func TestIPv6OnlyReach(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	v6only := id(b.must("POST", "/api/servers", map[string]any{"name": "Six", "ip_version": "ipv6"}, 201)["server"].(map[string]any)["id"])
	v4only := id(b.must("POST", "/api/servers", map[string]any{"name": "Four", "ip_version": "ipv4"}, 201)["server"].(map[string]any)["id"])
	dual := id(b.must("POST", "/api/servers", map[string]any{"name": "Both"}, 201)["server"].(map[string]any)["id"])
	hello(t, h, v6only, "", "2001:db8::6")
	hello(t, h, v4only, "198.51.100.4", "")
	hello(t, h, dual, "198.51.100.8", "2001:db8::8")
	exit6 := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", v6only), map[string]any{"kind": "vless"}, 201)["id"])
	exitDual := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", dual), map[string]any{"kind": "vless"}, 201)["id"])
	from4 := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", v4only), map[string]any{"kind": "vless"}, 201)["id"])
	from6 := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", v6only), map[string]any{"kind": "vless"}, 201)["id"])

	code, _, raw := b.do("PATCH", fmt.Sprintf("/api/nodes/%d", from4), map[string]any{"pass_node": exit6})
	if code != 400 || !strings.Contains(string(raw), "IPv6") {
		t.Errorf("an IPv4-only server passing to an IPv6-only exit: %d %s", code, raw)
	}
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", from6), map[string]any{"pass_node": exitDual}, 200)
	if c := compileX(t, h, v6only); !strings.Contains(c.raw, "2001:db8::8") {
		t.Errorf("an IPv6-only server does not reach a dual-stack exit at its IPv6: %s", c.raw)
	}
	if strings.Contains(string(raw), "domain name") {
		t.Errorf("a name cannot help a server with IPv6 only, yet it is suggested: %s", raw)
	}
	// a name of a server with IPv6 only still leads to IPv6 only
	b.must("PATCH", fmt.Sprintf("/api/servers/%d", v6only), map[string]any{"address": "six.example.com"}, 200)
	if code, _, _ := b.do("PATCH", fmt.Sprintf("/api/nodes/%d", from4), map[string]any{"pass_node": exit6}); code != 400 {
		t.Errorf("the name of a server with IPv6 only made it reachable over IPv4: %d", code)
	}
	// a server with both whose address was set to its IPv6: a name with both records is the fix
	b.must("PATCH", fmt.Sprintf("/api/servers/%d", dual), map[string]any{"address": "2001:db8::8"}, 200)
	code, _, raw = b.do("PATCH", fmt.Sprintf("/api/nodes/%d", from4), map[string]any{"pass_node": exitDual})
	if code != 400 || !strings.Contains(string(raw), "domain name") {
		t.Errorf("an IPv6 address set by hand: %d %s", code, raw)
	}
	b.must("PATCH", fmt.Sprintf("/api/servers/%d", dual), map[string]any{"address": "both.example.com"}, 200)
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", from4), map[string]any{"pass_node": exitDual}, 200)
	if c := compileX(t, h, v4only); !strings.Contains(c.raw, "both.example.com") {
		t.Errorf("the pass does not use the exit's name: %s", c.raw)
	}
}
