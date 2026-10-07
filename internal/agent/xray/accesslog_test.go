package xray

import "testing"

func TestParseAccess(t *testing.T) {
	cases := []struct {
		line, ip, host, inbound, email string
		port                           int
		ok                             bool
	}{
		{"2026/10/06 15:04:05.123456 from 1.2.3.4:5678 accepted tcp:www.example.com:443 [n5 -> direct] email: s1.n5",
			"1.2.3.4", "www.example.com", "n5", "s1.n5", 443, true},
		{"2026/10/06 15:04:05 from tcp:[2001:db8::7]:5678 accepted udp:8.8.8.8:53 [n9 >> direct] email: s2.n9",
			"2001:db8::7", "8.8.8.8", "n9", "s2.n9", 53, true},
		{"2026/10/06 15:04:05 from [::ffff:203.0.113.9]:40000 accepted tcp:Example.org:80 [n3 -> direct] email: alice",
			"203.0.113.9", "example.org", "n3", "alice", 80, true},
		{"2026/10/06 15:04:05 from 1.2.3.4:5678 rejected  proxy/vless/encoding: invalid request user id", "1.2.3.4", "", "", "", 0, false},
	}
	for _, c := range cases {
		e, parsed := ParseAccess(c.line)
		if !parsed {
			t.Fatalf("not parsed: %s", c.line)
		}
		if e.OK != c.ok || e.SrcIP != c.ip || (c.ok && (e.Host != c.host || e.Port != c.port || e.Inbound != c.inbound || e.Email != c.email)) {
			t.Errorf("%s\n got %+v", c.line, e)
		}
	}
}

// Every access log line is counted for the user and the node it belongs to - SOCKS5 and HTTP lines
// carry the user name, which only the aliases turn into a user.
func TestAccessIdentity(t *testing.T) {
	e := &Engine{aliases: map[string]string{"alice": "s3.n7", "s4.n8": "s4.n8"}}
	g := newAggregate()
	for _, line := range []string{
		"2026/10/06 15:04:05 from 198.51.100.4:1000 accepted tcp:a.example:443 [n7 -> direct] email: alice",
		"2026/10/06 15:04:06 from 198.51.100.4:1001 accepted tcp:a.example:443 [n12 -> direct] email: alice", // same name, other inbound
		"2026/10/06 15:04:07 from 198.51.100.5:1002 accepted tcp:b.example:443 [n8 -> direct] email: s4.n8",
		"2026/10/06 15:04:08 from 198.51.100.6:1003 accepted tcp:c.example:443 [n8 -> direct] email: mallory", // nobody
		"2026/10/06 15:04:09 from 198.51.100.6:1004 accepted tcp:c.example:443 [pass-n4 -> n4] email: p4",     // a proxy pass hop
	} {
		ent, ok := ParseAccess(line)
		if !ok {
			t.Fatalf("not parsed: %s", line)
		}
		if sub, node, ok := e.accessIdentity(ent); ok {
			g.add(ent, sub, node, true, true)
		}
	}
	want := map[ipKey]int64{{3, 7, "198.51.100.4"}: 1, {3, 12, "198.51.100.4"}: 1, {4, 8, "198.51.100.5"}: 1}
	if len(g.IPs) != len(want) {
		t.Fatalf("IPs %v", g.IPs)
	}
	for k, n := range want {
		if g.IPs[k] == nil || g.IPs[k].Conns != n {
			t.Errorf("%v: %+v", k, g.IPs[k])
		}
	}
	if len(g.Dests) != 3 {
		t.Errorf("dests %v", g.Dests)
	}
}
