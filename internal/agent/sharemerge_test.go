package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"meridian/internal/agent/nft"
	"meridian/internal/proto"
)

func inbound(t *testing.T, tag string, cfg map[string]any, clients ...proto.XrayClient) proto.XrayInbound {
	t.Helper()
	b, _ := json.Marshal(cfg)
	return proto.XrayInbound{Tag: tag, Config: b, Clients: clients}
}

func client(email string) proto.XrayClient {
	j, _ := json.Marshal(map[string]any{"id": "0b1f6e2a-7d3c-4e5f-9a8b-1c2d3e4f5a6b", "email": email})
	return proto.XrayClient{Email: email, JSON: j, Account: proto.XrayAccount{Kind: "vless", ID: "0b1f6e2a-7d3c-4e5f-9a8b-1c2d3e4f5a6b"}}
}

func homeState(t *testing.T) *proto.State {
	base, _ := json.Marshal(map[string]any{
		"outbounds": []any{map[string]any{"tag": "direct", "protocol": "freedom"}, map[string]any{"tag": "block", "protocol": "blackhole"}},
		"routing": map[string]any{"rules": []any{
			map[string]any{"ruleTag": "no-private", "ip": []string{"geoip:private"}, "outboundTag": "block"},
			map[string]any{"ruleTag": "r1", "inboundTag": []string{"n1"}, "domain": []string{"geosite:openai"}, "outboundTag": "block"},
			map[string]any{"ruleTag": "ipver", "network": "tcp,udp", "outboundTag": "direct"},
		}},
	})
	return &proto.State{Rev: "h1", ServerID: 1,
		Xray: &proto.Xray{Base: base, Inbounds: []proto.XrayInbound{
			inbound(t, "n1", map[string]any{"tag": "n1", "port": 443, "protocol": "vless"}, client("s1.n1")),
		}},
		Hysteria:  []proto.HyNode{{NodeID: 2, Port: 8443, HopPorts: "20000-20100"}},
		WireGuard: []proto.WGInterface{{NodeID: 3, Name: "mwg3", ListenPort: 51820, Address: []string{"10.66.0.1/20"}}},
		Forwards:  []proto.Forward{{ID: 1, ListenPort: 9000, Target: "203.0.113.5:80"}},
	}
}

// TestMergeSharedAlone: a server shared with nobody runs its own panel's state, untouched.
func TestMergeSharedAlone(t *testing.T) {
	home := homeState(t)
	got, errs := mergeShared(home, nil, []int{50000, 50001})
	if got != home || len(errs) != 0 {
		t.Fatalf("a server on its own: %p %v", got, errs)
	}
}

// TestMergeShared: a guest's protocols, users, rules, forwards and WireGuard move into its own range
// and names; what would reach beyond its share is left out, with the reason.
func TestMergeShared(t *testing.T) {
	home := homeState(t)
	gbase, _ := json.Marshal(map[string]any{
		"outbounds": []any{
			map[string]any{"tag": "direct", "protocol": "freedom"},
			map[string]any{"tag": "block", "protocol": "blackhole"},
			map[string]any{"tag": "pass-9", "protocol": "vless", "settings": map[string]any{}, "proxySettings": map[string]any{"tag": "nowhere"}},
			map[string]any{"tag": "sneaky", "protocol": "freedom", "settings": map[string]any{"redirect": "127.0.0.1:50000"}},
			map[string]any{"tag": "dns-out", "protocol": "dns"},
			map[string]any{"tag": "lb4.ext2", "protocol": "trojan", "streamSettings": map[string]any{"sockopt": map[string]any{"mark": 7}}},
		},
		"routing": map[string]any{
			"rules": []any{
				map[string]any{"ruleTag": "no-private", "ip": []string{"geoip:private"}, "outboundTag": "block"},
				map[string]any{"ruleTag": "r7", "inboundTag": []string{"n5"}, "domain": []string{"geosite:netflix"}, "outboundTag": "pass-9"},
				map[string]any{"ruleTag": "r8", "inboundTag": []string{"n1"}, "outboundTag": "block"}, // the home's n1: not the guest's
				map[string]any{"ruleTag": "dev-s3", "inboundTag": []string{"n5"}, "user": []string{"s3.n5"}, "source": []string{"198.51.100.4"}, "outboundTag": "block"},
				map[string]any{"ruleTag": "r9", "inboundTag": []string{"n5"}, "outboundTag": "lb-4", "balancerTag": "lb-4"},
			},
			"balancers": []any{map[string]any{"tag": "lb-4", "selector": []any{"lb4."}, "strategy": map[string]any{"type": "leastPing"}, "fallbackTag": "block"}},
		},
		"observatory": map[string]any{"subjectSelector": []any{"lb"}},
		"dns":         map[string]any{"servers": []any{"127.0.0.1"}},
	})
	certIn := map[string]any{"tag": "n6", "port": 2083, "protocol": "trojan", "streamSettings": map[string]any{"security": "tls",
		"tlsSettings": map[string]any{"certificates": []any{map[string]any{"certificateFile": proto.CertRef(4, "cert"), "keyFile": proto.CertRef(4, "key")}}}}}
	guest := &proto.State{Rev: "g1", ServerID: 77, BlockedIPs: []string{"192.0.2.9"},
		Geo: &proto.GeoRule{Mode: "block", Countries: []string{"CN"}},
		Xray: &proto.Xray{Base: gbase, Inbounds: []proto.XrayInbound{
			inbound(t, "n5", map[string]any{"tag": "n5", "port": 8080, "protocol": "vless",
				"settings": map[string]any{"clients": []any{map[string]any{"email": "s1.n1"}}}}, client("s3.n5"), client("p12")),
			inbound(t, "n6", certIn),
			inbound(t, "n7", map[string]any{"tag": "n7", "port": 443, "protocol": "vless"}),          // the home's port
			inbound(t, "n8", map[string]any{"tag": "n8", "port": 50000, "protocol": "vless"}),        // the agent's
			inbound(t, "n9", map[string]any{"tag": "n9", "port": 9001, "protocol": "dokodemo-door"}), // forwards everyone
			inbound(t, "n10", map[string]any{"tag": "n10", "port": 9002, "protocol": "vless", "listen": "127.0.0.1"}),
			inbound(t, "n11", map[string]any{"tag": "n11", "port": 9003, "protocol": "vless", "streamSettings": map[string]any{
				"security": "reality", "realitySettings": map[string]any{"target": "127.0.0.1:50000"}}}),
			inbound(t, "n12", map[string]any{"tag": "n12", "port": 9004, "protocol": "trojan", "settings": map[string]any{
				"fallbacks": []any{map[string]any{"dest": 80}}}}),
			inbound(t, "n13", map[string]any{"tag": "n13", "port": 9005, "protocol": "trojan", "streamSettings": map[string]any{
				"tlsSettings": map[string]any{"certificates": []any{map[string]any{"certificateFile": "/etc/shadow", "keyFile": "/root/.ssh/id_rsa"}}}}}),
			inbound(t, "mine", map[string]any{"tag": "mine", "port": 9006, "protocol": "vless"}),
		}},
		Hysteria: []proto.HyNode{
			{NodeID: 5, Port: 8443},  // the home's
			{NodeID: 6, Port: 20050}, // in the home's hopping range
			{NodeID: 7, Port: 4433, Users: []proto.HyUser{{ID: "s3.n7", Password: "pw"}}, Custom: json.RawMessage(`{"masquerade":{}}`)},
		},
		WireGuard: []proto.WGInterface{
			{NodeID: 8, Name: "mwg8", ListenPort: 51821, Address: []string{"10.66.0.1/20"}},
			{NodeID: 9, Name: "mwg9", ListenPort: 51822, Address: []string{"10.77.0.1/24"}, Peers: []proto.WGPeer{{SubID: 3}}},
		},
		Forwards: []proto.Forward{
			{ID: 1, ListenPort: 9100, Target: "127.0.0.1:50000"},
			{ID: 2, ListenPort: 9101, Target: "example.com:80"},
			{ID: 3, ListenPort: 9102, Target: "198.51.100.20:443"},
			{ID: 4, ListenPort: 9000, Target: "198.51.100.21:443"},
		},
		Certs:  []proto.SharedCert{{ID: 4, CertPEM: "c", KeyPEM: "k"}},
		Speed:  []proto.SpeedLimit{{Sub: 3, Mbps: 50}},
		Refuse: []proto.Refusal{{Sub: 3, IPs: []string{"198.51.100.4"}}},
	}
	got, errs := mergeShared(home, []guestState{{slot: 1, st: guest}}, []int{50000, 50001})
	msgs := strings.Join(errs[1], "\n")
	for _, want := range []string{"n7: port 443 is used by the server's own panel", "n8: port 50000 is used by the agent itself",
		"n9: dokodemo-door cannot be used", "n10: it may not listen on 127.0.0.1", "n11: its camouflage site 127.0.0.1 is not a public address",
		"n12: a fallback 80 is a port on this host", "n13: a certificate file on the host (/etc/shadow)", "mine: an inbound of your own code",
		"outbound sneaky: a redirect cannot be used", "outbound dns-out: dns cannot be used",
		"Hysteria2 protocol 5: port 8443 is used", "Hysteria2 protocol 6: port 20050 is used",
		"Hysteria2 protocol 7: its advanced settings (code) are left out",
		"WireGuard protocol 8: its network 10.66.0.0/20 overlaps", "forward 1: on a shared server a forward goes to a public IP address",
		"forward 2: on a shared server", "forward 4: port 9000 is used", "its country rule is left out"} {
		if !strings.Contains(msgs, want) {
			t.Errorf("missing %q in:\n%s", want, msgs)
		}
	}

	// the inbounds the host runs: the home's, then the guest's two that can run, moved
	var tags []string
	for _, in := range got.Xray.Inbounds {
		tags = append(tags, in.Tag)
	}
	if strings.Join(tags, ",") != "n1,n1000000000005,n1000000000006" {
		t.Fatalf("inbounds: %v", tags)
	}
	g5 := got.Xray.Inbounds[1]
	if g5.Clients[0].Email != "s1000000000003.n1000000000005" || g5.Clients[1].Email != "p1000000000012" ||
		!strings.Contains(string(g5.Clients[0].JSON), "s1000000000003.n1000000000005") {
		t.Errorf("clients: %+v", g5.Clients)
	}
	if strings.Contains(string(g5.Config), "s1.n1") || !strings.Contains(string(g5.Config), `"tag":"n1000000000005"`) {
		t.Errorf("the guest's own clients list was kept, or the tag not moved: %s", g5.Config)
	}
	if g6 := got.Xray.Inbounds[2]; !strings.Contains(string(g6.Config), proto.CertRef(1000000000004, "cert")) || strings.Contains(string(g6.Config), "@cert/4/") {
		t.Errorf("certificate references: %s", g6.Config)
	}
	if len(got.Certs) != 1 || got.Certs[0].ID != 1000000000004 {
		t.Errorf("certificates: %+v", got.Certs)
	}

	// outbounds and rules: the guest's in its own names, kept to its own protocols, marked
	var base struct {
		Outbounds []map[string]any `json:"outbounds"`
		Routing   struct {
			Rules     []map[string]any `json:"rules"`
			Balancers []map[string]any `json:"balancers"`
		} `json:"routing"`
		Observatory map[string]any `json:"observatory"`
		DNS         any            `json:"dns"`
	}
	if err := json.Unmarshal(got.Xray.Base, &base); err != nil {
		t.Fatal(err)
	}
	outs := map[string]map[string]any{}
	for _, o := range base.Outbounds {
		outs[o["tag"].(string)] = o
	}
	for _, tag := range []string{"direct", "block", "g1.direct", "g1.block", "g1:direct", "g1:block", "g1.pass-9", "g1.lb4.ext2"} {
		if outs[tag] == nil {
			t.Errorf("outbound %s missing: %v", tag, outs)
		}
	}
	if outs["g1.sneaky"] != nil || outs["g1.dns-out"] != nil || outs["g1.pass-9"]["proxySettings"] != nil {
		t.Error("an outbound reaching beyond the share was kept")
	}
	mark := outs["g1.lb4.ext2"]["streamSettings"].(map[string]any)["sockopt"].(map[string]any)["mark"]
	if mark != float64(nft.DirectMark) {
		t.Errorf("the guest's own mark was kept: %v", mark)
	}
	var order []string
	rules := map[string]map[string]any{}
	for _, r := range base.Routing.Rules {
		order = append(order, r["ruleTag"].(string))
		rules[r["ruleTag"].(string)] = r
	}
	if strings.Join(order, ",") != "no-private,r1,g1:no-private,g1:blocked,g1.no-private,g1.r7,g1.dev-s3,g1.r9,g1:rest,ipver" {
		t.Errorf("rule order: %v", order)
	}
	if in := rules["g1.r7"]["inboundTag"]; len(in.([]any)) != 1 || in.([]any)[0] != "n1000000000005" || rules["g1.r7"]["outboundTag"] != "g1.pass-9" {
		t.Errorf("g1.r7: %v", rules["g1.r7"])
	}
	if u := rules["g1.dev-s3"]["user"].([]any); u[0] != "s1000000000003.n1000000000005" {
		t.Errorf("device rule users: %v", u)
	}
	for _, r := range base.Routing.Rules {
		if strings.HasPrefix(r["ruleTag"].(string), "g1") {
			for _, in := range r["inboundTag"].([]any) {
				if !strings.HasPrefix(in.(string), "n1000000000") {
					t.Errorf("a guest rule reaches the home's protocol %v: %v", in, r)
				}
			}
		}
	}
	if len(base.Routing.Balancers) != 1 || base.Routing.Balancers[0]["tag"] != "g1.lb-4" ||
		base.Routing.Balancers[0]["selector"].([]any)[0] != "g1.lb4." || base.Routing.Balancers[0]["fallbackTag"] != "g1.block" {
		t.Errorf("balancers: %v", base.Routing.Balancers)
	}
	if sel := base.Observatory["subjectSelector"].([]any); len(sel) != 1 || sel[0] != "g1.lb" {
		t.Errorf("observatory: %v", base.Observatory)
	}
	if base.DNS != nil {
		t.Error("the guest's DNS settings reached the host")
	}

	// Hysteria2, WireGuard, forwards, limits
	if len(got.Hysteria) != 2 || got.Hysteria[1].NodeID != 1000000000007 || got.Hysteria[1].Users[0].ID != "s1000000000003.n1000000000007" ||
		got.Hysteria[1].Custom != nil {
		t.Errorf("hysteria: %+v", got.Hysteria)
	}
	if len(got.WireGuard) != 2 || got.WireGuard[1].Name != "mg1w9" || got.WireGuard[1].NodeID != 1000000000009 ||
		got.WireGuard[1].Peers[0].SubID != 1000000000003 {
		t.Errorf("wireguard: %+v", got.WireGuard)
	}
	if len(got.Forwards) != 2 || got.Forwards[1].ID != 1000000000003 {
		t.Errorf("forwards: %+v", got.Forwards)
	}
	if got.Speed[0].Sub != 1000000000003 || got.Refuse[0].Sub != 1000000000003 || got.Geo != nil {
		t.Errorf("limits or country rule: %+v %+v %+v", got.Speed, got.Refuse, got.Geo)
	}
	// the home's own state was not changed under it
	if len(home.Xray.Inbounds) != 1 || len(home.Hysteria) != 1 || len(home.WireGuard) != 1 || len(home.Forwards) != 1 {
		t.Error("merging changed the home's state")
	}
	if slot, id := slotOf(1000000000005); slot != 1 || id != 5 {
		t.Errorf("slotOf: %d %d", slot, id)
	}
}

// TestMergeSharedTwoGuests: two guests cannot take each other's ports or networks either.
func TestMergeSharedTwoGuests(t *testing.T) {
	home := homeState(t)
	a := &proto.State{Xray: &proto.Xray{Inbounds: []proto.XrayInbound{inbound(t, "n1", map[string]any{"tag": "n1", "port": 7000, "protocol": "vless"})}}}
	b := &proto.State{Xray: &proto.Xray{Inbounds: []proto.XrayInbound{inbound(t, "n1", map[string]any{"tag": "n1", "port": 7000, "protocol": "vless"}),
		inbound(t, "n2", map[string]any{"tag": "n2", "port": 7001, "protocol": "vless"})}}}
	got, errs := mergeShared(home, []guestState{{slot: 1, st: a}, {slot: 2, st: b}}, nil)
	if len(errs[1]) != 0 || len(errs[2]) != 1 || !strings.Contains(errs[2][0], "port 7000 is used by another panel") {
		t.Errorf("errors: %v", errs)
	}
	var tags []string
	for _, in := range got.Xray.Inbounds {
		tags = append(tags, in.Tag)
	}
	if strings.Join(tags, ",") != "n1,n1000000000001,n2000000000002" {
		t.Errorf("inbounds: %v", tags)
	}
}
