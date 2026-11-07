package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
)

// TestMaskAddr: the last four digits of an IPv4 address go, whichever octets they are in, and the last
// four groups of an IPv6 address.
func TestMaskAddr(t *testing.T) {
	for in, want := range map[string]string{
		"45.196.216.188":        "45.196.21*.***",
		"203.0.113.7":           "203.0.***.*",
		"8.8.8.8":               "*.*.*.*",
		"100.20.3.40":           "100.2*.*.**",
		"::ffff:45.196.216.188": "45.196.21*.***",
		"2001:db8:1:2:3:4:5:6":  "2001:db8:1:2:****:****:****:****",
		"2606:4700:4700::1111":  "2606:4700:4700:0:****:****:****:****",
	} {
		if got := maskAddr(netip.MustParseAddr(in)); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestScrubAddrs(t *testing.T) {
	for in, want := range map[string]string{
		"Example Hosting 45.196.216.188": "Example Hosting",
		"host-45-196-216-188 Ltd":        "host- Ltd",
		"Net 2001:db8::1":                "Net",
		"AS-CHOOPA Vultr Holdings 2":     "AS-CHOOPA Vultr Holdings 2",
	} {
		if got := scrubAddrs(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// TestRouteGraph: routes read from the server's network outwards, prepends gone; a network sits at the
// nearest place it appears, and each level keeps the six most used.
func TestRouteGraph(t *testing.T) {
	if got := asPath("3333 1299 1299 64500 64500"); !slices.Equal(got, []int64{64500, 1299, 3333}) {
		t.Errorf("a path: %v", got)
	}
	if got := asPath("3333 {1299,174} 64500"); !slices.Equal(got, []int64{3333}) {
		t.Errorf("a path with a set: %v", got)
	}
	paths := [][]int64{{64500, 1299, 3333}, {64500, 1299, 6939}, {64500, 174, 1299}, {64511, 2914}}
	for i := 0; i < 9; i++ {
		paths = append(paths, []int64{64500, int64(100 + i)})
	}
	nodes, edges := routeGraph(paths, 64500)
	at := map[int64]netNode{}
	for _, n := range nodes {
		at[n.ASN] = n
	}
	if at[64500].Level != 0 || at[64500].Paths != 12 {
		t.Errorf("the origin: %+v", at[64500])
	}
	if at[1299].Level != 1 || at[1299].Paths != 3 || at[3333].Level != 2 {
		t.Errorf("levels: %+v", nodes)
	}
	if _, ok := at[2914]; ok {
		t.Error("a route to another origin was drawn")
	}
	level1 := 0
	for _, n := range nodes {
		if n.Level == 1 {
			level1++
		}
	}
	if level1 != 6 {
		t.Errorf("%d networks on the first level", level1)
	}
	for _, e := range edges {
		if e.From == 174 && e.To == 1299 {
			t.Error("a link within a level was kept")
		}
	}
	// a network behind one that did not make its level is not drawn alone
	for i := 0; i < 7; i++ {
		paths = append(paths, []int64{64500, int64(200 + i), int64(300 + i)}, []int64{64500, int64(200 + i), int64(300 + i)})
	}
	paths = append(paths, []int64{64500, 999, 998}, []int64{64500, 999, 998}, []int64{64500, 999, 998})
	nodes, edges = routeGraph(paths, 64500)
	linked := map[int64]bool{64500: true}
	for _, e := range edges {
		linked[e.To] = true
	}
	for _, n := range nodes {
		if !linked[n.ASN] {
			t.Errorf("AS%d stands alone", n.ASN)
		}
	}
	if n, e := routeGraph(paths, 1); n != nil || e != nil {
		t.Error("a graph for a network no route reaches")
	}
}

// TestNetScore: entries mark exactly the addresses they cover; the score falls with what is said about
// the address and, less, about its /24; hosting costs nothing; unread lists are no clean bill.
func TestNetScore(t *testing.T) {
	a := netip.MustParseAddr("45.196.216.188")
	base := outerPrefix(a)
	h := &netHits{}
	targets := map[netip.Prefix]*netHits{base: h}
	if s, _ := scoreOf(a, h); s != nil {
		t.Errorf("a score with no list read: %d", *s)
	}
	for k := netKind(0); k < netKinds; k++ {
		h.read[k] = true
	}
	if s, notes := scoreOf(a, h); *s != 100 || notes != nil {
		t.Errorf("a clean address: %d %v", *s, notes)
	}
	mark := func(k netKind, line string) {
		e, ok := parseEntry(line)
		if !ok {
			t.Fatalf("%q does not read", line)
		}
		markEntry(targets, k, e)
	}
	mark(netHosting, "45.196.0.0/16")
	if s, _ := scoreOf(a, h); *s != 100 {
		t.Errorf("hosting cost points: %d", *s)
	}
	mark(netAbuse, "45.196.216.8/30")
	mark(netAbuse, "9.9.9.9")
	if b := blockOf(a, h); b.Abuse != 4 || b.Clean != 252 || b.Range != "45.196.21*.***/24" {
		t.Errorf("the /24: %+v", b)
	}
	if s, _ := scoreOf(a, h); *s != 97 {
		t.Errorf("a few neighbours listed: %d", *s)
	}
	mark(netAbuse, "45.196.216.188")
	mark(netDrop, `{"cidr":"45.196.216.128/25","sblid":"SBL1"}`)
	s, notes := scoreOf(a, h)
	if *s != 0 || len(notes) != 3 {
		t.Errorf("an address on the worst lists: %d %v", *s, notes)
	}
	for _, line := range []string{"", "# a comment", "; another", `{"type":"metadata"}`, "not an address"} {
		if _, ok := parseEntry(line); ok {
			t.Errorf("%q read as an entry", line)
		}
	}
	if s, _ := scoreOf(netip.MustParseAddr("2001:db8::1"), h); s != nil {
		t.Error("a score for IPv6, which the lists do not cover")
	}
}

// TestNetProfileKeepsTheAddress is the point of the design: with a server at a real-looking address and
// a name that repeats it, nothing asked of the services ever contains the address, and nothing served
// - to a visitor or the supervisor, here or in the status page's own data - contains it or its /24.
func TestNetProfileKeepsTheAddress(t *testing.T) {
	const ip = "45.196.216.188"
	var mu sync.Mutex
	var asked []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.String())
		mu.Unlock()
		res := r.URL.Query().Get("resource")
		switch {
		case strings.HasSuffix(r.URL.Path, "/network-info/data.json"):
			fmt.Fprint(w, `{"data":{"asns":["64500"],"prefix":"45.196.216.0/22"}}`)
		case strings.HasSuffix(r.URL.Path, "/looking-glass/data.json"):
			fmt.Fprint(w, `{"data":{"rrcs":[{"peers":[{"as_path":"3333 1299 64500","prefix":"45.196.216.0/22"},{"as_path":"6939 1299 64500 64500"}]}]}}`)
		case strings.HasSuffix(r.URL.Path, "/as-overview/data.json"):
			fmt.Fprintf(w, `{"data":{"holder":"NET%s - Example %s Hosting"}}`, res, ip)
		case strings.HasSuffix(r.URL.Path, "/routing-status/data.json"):
			fmt.Fprint(w, `{"data":{"announced_space":{"v4":{"ips":1024}}}}`)
		case strings.HasSuffix(r.URL.Path, "/net"):
			fmt.Fprint(w, `{"data":[{"info_type":"Content","info_types":["Content"]}]}`)
		case r.URL.Path == "/abuse.txt":
			fmt.Fprint(w, "# a list\n45.196.216.7\n45.196.216.188\n1.2.3.4\n")
		case r.URL.Path == "/hosting.txt":
			fmt.Fprint(w, "45.196.0.0/16\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer fake.Close()
	oldRipe, oldPeering, oldLists, oldClient := ripeBase, peeringBase, netLists, netClient
	defer func() { ripeBase, peeringBase, netLists, netClient = oldRipe, oldPeering, oldLists, oldClient }()
	ripeBase, peeringBase = fake.URL+"/ripe", fake.URL+"/pdb"
	netClient = func() *http.Client { return fake.Client() }
	netLists = netLists[:0:0]
	for _, l := range []struct {
		k netKind
		u string
	}{{netAbuse, "/abuse.txt"}, {netHosting, "/hosting.txt"}, {netTor, "/gone.txt"}} {
		netLists = append(netLists, struct {
			kind netKind
			url  string
		}{l.k, fake.URL + l.u})
	}

	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": ip}, 201)["server"].(map[string]any)["id"])
	if _, err := h.p.db.Exec1(`UPDATE servers SET first_seen_at = ?, online = 1, ipv4 = ? WHERE id = ?`, now(), ip, sid); err != nil {
		t.Fatal(err)
	}
	set := b.must("GET", "/api/settings", nil, 200)
	if set["status_network"] != false {
		t.Fatalf("the profile is on by default: %v", set["status_network"])
	}
	set["status_page"] = "page"
	b.must("PUT", "/api/settings", set, 200)
	path := fmt.Sprintf("/api/status/servers/%d/network", sid)
	anon := h.browser()
	if code, _, _ := anon.do("GET", path, nil); code != 404 {
		t.Errorf("with the setting off: %d", code)
	}
	set["status_network"] = true
	b.must("PUT", "/api/settings", set, 200)

	h.p.refreshNets(context.Background())

	code, m, raw := anon.do("GET", path, nil)
	if code != 200 || m["state"] != "ready" {
		t.Fatalf("the profile: %d %s", code, raw)
	}
	var v netProfile
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if v.Address != "45.196.21*.***" || v.Prefix != "45.196.21*.***/22" || v.ASN != 64500 || v.ASNSpace != 1024 || v.RouteSeen != 2 {
		t.Errorf("the profile: %s", raw)
	}
	if !slices.Equal(v.Usage, []string{"Data centre", "Content and hosting"}) {
		t.Errorf("the usage: %v", v.Usage)
	}
	// on the abuse list itself, one neighbour too: 100 - 40 - 3; the Tor list could not be read
	if v.Score == nil || *v.Score != 57 || v.Block == nil || v.Block.Abuse != 2 || v.Block.Clean != 254 {
		t.Errorf("the score and the /24: %s", raw)
	}
	if len(v.Nodes) != 4 || v.Holder != "Example  Hosting" {
		t.Errorf("the routes: %s", raw)
	}

	_, _, sup := b.do("GET", path, nil)
	_, _, status := anon.do("GET", "/api/status", nil)
	_, _, statusSup := b.do("GET", "/api/status", nil)
	for name, body := range map[string][]byte{"a visitor's profile": raw, "the supervisor's profile": sup, "a visitor's status": status, "the supervisor's status": statusSup} {
		for _, secret := range []string{ip, "45.196.216.", "45-196-216", "45.196.216.0"} {
			if strings.Contains(string(body), secret) {
				t.Errorf("%s gives away %s: %s", name, secret, body)
			}
		}
	}
	mu.Lock()
	sent := slices.Clone(asked)
	mu.Unlock()
	if len(sent) < 5 {
		t.Fatalf("asked: %v", sent)
	}
	for _, u := range sent {
		if strings.Contains(u, ip) || strings.Contains(u, "216.188") || strings.Contains(u, "216%2E188") {
			t.Errorf("the address was sent out: %s", u)
		}
	}

	// a server left off the status page has no profile; one lookup failing keeps what is known
	b.must("PATCH", fmt.Sprintf("/api/servers/%d", sid), map[string]any{"status_hidden": true}, 200)
	if code, _, _ := anon.do("GET", path, nil); code != 404 {
		t.Errorf("a hidden server: %d", code)
	}
	b.must("PATCH", fmt.Sprintf("/api/servers/%d", sid), map[string]any{"status_hidden": false}, 200)
	ripeBase = fake.URL + "/nothing"
	h.p.refreshNets(context.Background())
	if _, m, raw := anon.do("GET", path, nil); m["asn"] == nil {
		t.Errorf("a failed lookup wiped the profile: %s", raw)
	}
}
