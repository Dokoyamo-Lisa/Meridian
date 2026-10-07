package panel

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"meridian/internal/subgen"
)

// combos lists every protocol draft the UI can build: kind x transport x security x certificate x
// CDN, plus the protocol-specific choices.
func combos() []protocolDraft {
	var out []protocolDraft
	str := func(s string) *string { return &s }
	yes, no := true, false
	for _, k := range []string{subgen.KindVLESS, subgen.KindVMess, subgen.KindTrojan, subgen.KindShadowsocks,
		subgen.KindSOCKS, subgen.KindHTTP} {
		for _, tr := range allTransports {
			for _, sec := range []string{secNone, secTLS, secReality} {
				for _, cdn := range []*bool{nil, &yes} {
					certs := []string{""}
					if sec == secTLS {
						certs = []string{certSelf, certACME}
					}
					for _, cm := range certs {
						in := &protoInput{Transport: str(tr), Security: str(sec), CDN: cdn}
						if cm != "" {
							in.CertMode = str(cm)
							in.SNI = str("proxy.example.com")
						}
						if cdn != nil {
							in.CDNHost = str("cdn.example.com")
						}
						out = append(out, protocolDraft{Kind: k, Settings: in})
					}
				}
			}
		}
	}
	for _, m := range ssMethods {
		out = append(out, protocolDraft{Kind: subgen.KindShadowsocks, Settings: &protoInput{Method: str(m)}})
	}
	for _, udp := range []bool{true, false} {
		u := udp
		out = append(out, protocolDraft{Kind: subgen.KindSOCKS, Settings: &protoInput{UDP: &u}})
	}
	for _, cm := range []string{certSelf, certACME} {
		for _, obfs := range []bool{false, true} {
			o := obfs
			out = append(out, protocolDraft{Kind: subgen.KindHysteria2, Settings: &protoInput{CertMode: str(cm),
				SNI: str("hy.example.com"), Obfs: &o}})
		}
	}
	out = append(out, protocolDraft{Kind: subgen.KindWireGuard, Settings: &protoInput{}},
		protocolDraft{Kind: subgen.KindVLESS, Settings: &protoInput{Security: str(secReality), OwnSite: &yes,
			SNI: str("www.example.com"), Target: str("127.0.0.1:8443")}},
		protocolDraft{Kind: subgen.KindVLESS, Settings: &protoInput{Security: str(secReality), OwnSite: &no,
			SNI: str("www.microsoft.com")}})
	return out
}

func describe(d protocolDraft) string {
	b, _ := json.Marshal(d.Settings)
	return d.Kind + " " + string(b)
}

// TestEveryAcceptedProtocolWorks is the guarantee behind "only combinations that work can be
// saved": each accepted draft renders a server config and a client entry, and at least one app can
// use it. Each refused draft says why.
func TestEveryAcceptedProtocolWorks(t *testing.T) {
	srv := &Server{ID: 1, Name: "Lab", Address: "203.0.113.10", Secret: "s"}
	sub := &Sub{ID: 7, UUID: "00000000-0000-4000-8000-000000000007", Secret: "secret"}
	accepted, refused := 0, 0
	for _, d := range combos() {
		v := checkProtocol(d.Kind, d.Settings)
		if !v.Valid {
			refused++
			if v.Error == "" || strings.Contains(v.Error, "%!") {
				t.Errorf("%s: refused without a usable reason: %q", describe(d), v.Error)
			}
			continue
		}
		accepted++
		raw, err := newSettings(d.Kind, d.Settings, nil)
		if err != nil {
			t.Fatalf("%s: check said valid, create failed: %v", describe(d), err)
		}
		n := &Node{ID: 9, ServerID: 1, Kind: d.Kind, Port: v.Ports[0], Settings: raw, Enabled: true}
		switch k, _ := kindOf(d.Kind); k.Engine {
		case "xray":
			in, err := xrayInbound(n, []*Sub{sub}, nil, nil)
			if err != nil {
				t.Fatalf("%s: server config: %v", describe(d), err)
			}
			var obj map[string]any
			if json.Unmarshal(in.Config, &obj) != nil || obj["protocol"] == nil || obj["streamSettings"] == nil {
				t.Fatalf("%s: server config is not an inbound: %s", describe(d), in.Config)
			}
			if len(in.Clients) == 0 {
				t.Fatalf("%s: no users in the inbound", describe(d))
			}
		case "hysteria":
			if _, err := hyNode(n, []*Sub{sub}, nil, nil); err != nil {
				t.Fatalf("%s: hysteria config: %v", describe(d), err)
			}
		}
		var peer *wgPeer
		if d.Kind == subgen.KindWireGuard {
			peer = &wgPeer{PrivateKey: "k", PublicKey: "p", IP4: "10.66.0.2"}
		}
		e, err := endpoint(n, srv, sub, peer, "Lab", nil)
		if err != nil {
			t.Fatalf("%s: client entry: %v", describe(d), err)
		}
		works := 0
		for _, a := range subgen.Support(e) {
			if a.OK {
				works++
			}
		}
		if works == 0 {
			t.Errorf("%s: accepted, but no app can use it", describe(d))
		}
		if d.Kind != subgen.KindWireGuard {
			if _, err := subgen.XrayOutbound(e, "t"); err != nil {
				t.Errorf("%s: cannot be a proxy pass exit: %v", describe(d), err)
			}
		}
	}
	t.Logf("%d combinations accepted, %d refused with a reason", accepted, refused)
	if accepted < 40 {
		t.Fatalf("only %d combinations accepted - the rules are too strict", accepted)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	str := func(s string) *string { return &s }
	raw, err := newSettings(subgen.KindVLESS, &protoInput{Transport: str(tXHTTP), Security: str(secReality)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// changing only the transport keeps the keys and the camouflage
	before, _ := parseXray(raw)
	raw2, err := updateSettings(subgen.KindVLESS, raw, &protoInput{Transport: str(tGRPC)})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := parseXray(raw2)
	if after.PrivateKey != before.PrivateKey || after.SNI != before.SNI || after.ServiceName == "" || after.Path != "" {
		t.Fatalf("update lost keys or kept stale fields: %+v", after)
	}
	// switching to TLS drops REALITY keys and gets a pinned certificate
	raw3, err := updateSettings(subgen.KindVLESS, raw2, &protoInput{Security: str(secTLS), SNI: str("proxy.example.com")})
	if err != nil {
		t.Fatal(err)
	}
	tls, _ := parseXray(raw3)
	if tls.PrivateKey != "" || tls.CertSHA256 == "" || tls.CertMode != certSelf {
		t.Fatalf("security switch: %+v", tls)
	}
	// keys never leave through the API view
	pub := publicSettings(subgen.KindVLESS, raw)
	if _, ok := pub["private_key"]; ok {
		t.Fatal("private key in the public view")
	}
	if b, _ := json.Marshal(pub); strings.Contains(string(b), before.PrivateKey) {
		t.Fatal("private key leaked")
	}
}

func TestProtocolLabels(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := map[string]protocolDraft{
		"REALITY":         {Kind: subgen.KindVLESS, Settings: nil},
		"REALITY gRPC":    {Kind: subgen.KindVLESS, Settings: &protoInput{Transport: str(tGRPC)}},
		"VMess WS TLS":    {Kind: subgen.KindVMess, Settings: &protoInput{Transport: str(tWS), Security: str(secTLS)}},
		"Trojan gRPC TLS": {Kind: subgen.KindTrojan, Settings: &protoInput{Transport: str(tGRPC)}},
		"HTTPS":           {Kind: subgen.KindHTTP, Settings: &protoInput{Security: str(secTLS)}},
		"SOCKS5":          {Kind: subgen.KindSOCKS, Settings: nil},
	}
	for want, d := range cases {
		raw, err := newSettings(d.Kind, d.Settings, nil)
		if err != nil {
			t.Fatalf("%s: %v", want, err)
		}
		if got := protocolLabel(d.Kind, raw); got != want {
			t.Errorf("label = %q, want %q", got, want)
		}
	}
	_ = fmt.Sprint()
}
