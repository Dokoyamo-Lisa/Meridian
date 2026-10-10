package panel

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"meridian/internal/proto"
	"meridian/internal/subgen"
)

// helloCaps is an agent's hello with what it can do.
func helloCaps(t *testing.T, h *harness, sid int64, v4 string, caps proto.Caps) {
	t.Helper()
	srv, err := h.p.serverByID(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.p.ingest(context.Background(), srv, &proto.Report{Instance: "i", Hello: &proto.Hello{AgentVersion: Version,
		IPv4: v4, Caps: caps}}); err != nil {
		t.Fatal(err)
	}
}

// TestUsersOwnServers: mieru, Snell and AnyTLS give every user their own port, inside the protocol's
// range, and the server the programs to run them with. AnyTLS needs an agent that runs it (1.3.1);
// its users' servers get the protocol's certificate - its own, a shared one, or the Let's Encrypt
// domain the agent keeps - and its links pin a self-signed one. A device limit turns devices away on
// these protocols too, not only on Hysteria2.
func TestUsersOwnServers(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "A", "address": "203.0.113.70"}, 201)["server"].(map[string]any)["id"])
	old := proto.Caps{Systemd: true, Nftables: true, Certs: true, Solo: true, RestartPending: true}
	helloCaps(t, h, sid, "203.0.113.70", old) // a 1.3.0 agent
	nodes := fmt.Sprintf("/api/servers/%d/nodes", sid)
	if m := b.must("POST", nodes, map[string]any{"kind": "anytls"}, 400); !strings.Contains(fmt.Sprint(m["error"]), "1.3.1") {
		t.Errorf("AnyTLS on an agent that cannot run it: %v", m["error"])
	}
	now := old
	now.AnyTLS = true
	helloCaps(t, h, sid, "203.0.113.70", now)
	if m := b.must("POST", nodes, map[string]any{"kind": "anytls", "settings": map[string]any{"transport": "ws"}}, 400); !strings.Contains(fmt.Sprint(m["error"]), "transport") {
		t.Errorf("AnyTLS over a transport: %v", m["error"])
	}
	anyID := id(b.must("POST", nodes, map[string]any{"kind": "anytls", "settings": map[string]any{"users": 5}}, 201)["id"])
	snellID := id(b.must("POST", nodes, map[string]any{"kind": "snell", "settings": map[string]any{"users": 5}}, 201)["id"])
	b.must("POST", "/api/users", map[string]any{"name": "ann", "ip_limit": 1, "device_mode": "refuse", "sign_in": false}, 201)
	b.must("POST", "/api/users", map[string]any{"name": "bob", "sign_in": false}, 201)
	var ann, bob int64
	h.p.db.QueryRow(`SELECT id FROM subs WHERE name = 'ann'`).Scan(&ann)
	h.p.db.QueryRow(`SELECT id FROM subs WHERE name = 'bob'`).Scan(&bob)
	anyNode, err := h.p.nodeByID(context.Background(), anyID)
	if err != nil {
		t.Fatal(err)
	}

	st := stateOf(t, h, sid)
	var at, sn *proto.SoloNode
	for i := range st.Solo {
		switch st.Solo[i].NodeID {
		case anyID:
			at = &st.Solo[i]
		case snellID:
			sn = &st.Solo[i]
		}
	}
	if at == nil || sn == nil || at.Kind != "anytls" || sn.Kind != "snell" {
		t.Fatalf("protocols: %+v", st.Solo)
	}
	if !strings.HasPrefix(at.CertPEM, "-----BEGIN CERTIFICATE-----") || at.KeyPEM == "" || at.ACME != "" || sn.CertPEM != "" {
		t.Errorf("AnyTLS's certificate: %q %q", at.CertPEM[:min(len(at.CertPEM), 40)], at.ACME)
	}
	ports := map[int]bool{}
	for _, u := range at.Users {
		if u.Port < anyNode.Port || u.Port > anyNode.Port+4 || ports[u.Port] || len(u.Secret) < 16 {
			t.Errorf("AnyTLS user %d: port %d (range from %d), secret of %d", u.Sub, u.Port, anyNode.Port, len(u.Secret))
		}
		ports[u.Port] = true
	}
	if len(at.Users) != 2 || len(sn.Users) != 2 {
		t.Errorf("users: %d on AnyTLS, %d on Snell", len(at.Users), len(sn.Users))
	}
	if st.Cores.SingBox != "1.14.2" || st.Cores.Mita == "" || st.Cores.Snell == "" {
		t.Errorf("programs: %+v", st.Cores)
	}

	// a user's AnyTLS entry: their port, the certificate's name, the pin
	sub, _ := h.p.subByID(context.Background(), ann)
	eps, _ := h.p.endpointsFor(context.Background(), sub)
	var e *subgen.Endpoint
	for i := range eps {
		if eps[i].Kind == subgen.KindAnyTLS {
			e = &eps[i]
		}
	}
	if e == nil || e.SNI != defaultSelfSignedName || len(e.PinSHA256) != 64 || e.CertPEM == "" || !ports[e.Port] {
		t.Fatalf("AnyTLS entry: %+v", e)
	}
	body, _, skipped := subgen.Render(subgen.FormatClash, eps, subgen.Info{}, "")
	if !strings.Contains(string(body), "type: anytls") || !strings.Contains(string(body), "fingerprint: "+e.PinSHA256) {
		t.Errorf("mihomo subscription: %s %v", body, skipped)
	}

	// a device limit: the devices over it are turned away on the users' own servers
	h.p.live.put(sid, proto.Live{Online: []proto.OnlineUser{{Sub: ann, Node: snellID,
		IPs: []proto.OnlineIP{{IP: "198.51.100.1", Since: 100}, {IP: "198.51.100.2", Since: 200}}}}})
	h.p.checkIPLimits(context.Background(), sub.AccountID)
	st = stateOf(t, h, sid)
	if len(st.Refuse) != 1 || st.Refuse[0].Sub != ann || !slices.Equal(st.Refuse[0].IPs, []string{"198.51.100.2"}) {
		t.Errorf("turned away: %+v", st.Refuse)
	}

	// Let's Encrypt: the agent keeps the certificate, the state names the domain
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", anyID), map[string]any{"settings": map[string]any{"cert_mode": "acme", "sni": "any.example.com"}}, 200)
	st = stateOf(t, h, sid)
	for _, n := range st.Solo {
		if n.NodeID == anyID && (n.ACME != "any.example.com" || n.CertPEM != "" || n.KeyPEM != "") {
			t.Errorf("AnyTLS with Let's Encrypt: %q %d", n.ACME, len(n.CertPEM))
		}
	}
	eps, _ = h.p.endpointsFor(context.Background(), sub)
	for _, e := range eps {
		if e.Kind == subgen.KindAnyTLS && (e.PinSHA256 != "" || e.SNI != "any.example.com") {
			t.Errorf("a domain certificate is not pinned: %+v", e)
		}
	}
	// settings never show the key
	_, _, raw := b.do("GET", fmt.Sprintf("/api/servers/%d", sid), nil)
	if !strings.Contains(string(raw), "anytls") || strings.Contains(string(raw), "PRIVATE KEY") {
		t.Error("the private key was shown")
	}
}
