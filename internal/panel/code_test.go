package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"meridian/internal/proto"
)

func TestParseJSONC(t *testing.T) {
	doc, err := parseJSONC(`{
  // a WARP exit for some sites
  "outbounds": [ { "tag": "warp", "protocol": "freedom", }, ], /* trailing commas are fine */
  "note": "a // inside a string stays", "url": "http://x/*y*/",
}`)
	if err != nil {
		t.Fatal(err)
	}
	if doc["note"] != "a // inside a string stays" || doc["url"] != "http://x/*y*/" || len(doc["outbounds"].([]any)) != 1 {
		t.Errorf("parsed: %v", doc)
	}
	for src, want := range map[string]string{
		"{\n  \"a\": 1,\n  \"b\": oops\n}": "line 3, column 8",
		`{"a": 1} {"b": 2}`:                "more after it",
		`[1, 2]`:                           "one JSON object",
		`{"a": /* never closed`:            "line 1, column 7: a /* comment is not closed",
	} {
		if _, err := parseJSONC(src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", src, err, want)
		}
	}
	if doc, err := parseJSONC("  \n "); err != nil || doc != nil {
		t.Errorf("empty: %v %v", doc, err)
	}
	for src, want := range map[string]string{
		`{"log": {"loglevel": "debug"}}`:     `"log" is Meridian's own`,
		`{"outbounds": [{"protocol": "x"}]}`: `outbound 1 needs a "tag"`,
		`{"inbounds": {"tag": "x"}}`:         `must be a list`,
		`{"routing": {"rules": {}}}`:         `"routing.rules" must be a list`,
	} {
		if _, err := checkXrayCode(src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", src, err, want)
		}
	}
}

func TestMergeXray(t *testing.T) {
	base := json.RawMessage(`{"outbounds":[{"tag":"direct","protocol":"freedom"},{"tag":"block","protocol":"blackhole"}],
		"routing":{"domainStrategy":"AsIs","rules":[{"ruleTag":"no-private","outboundTag":"block"}]}}`)
	ins := []proto.XrayInbound{{Tag: "n12", Config: json.RawMessage(`{"tag":"n12","port":443,"protocol":"vless","sniffing":{"enabled":true,"routeOnly":true}}`)}}
	code := `{
	  "outbounds": [{"tag": "warp", "protocol": "wireguard"}, {"tag": "direct", "protocol": "freedom", "settings": {"domainStrategy": "UseIPv4"}}],
	  "routing": {"domainStrategy": "IPIfNonMatch", "rules": [{"domain": ["geosite:openai"], "outboundTag": "warp"}]},
	  "inbounds": [{"tag": "n12", "sniffing": {"enabled": false}}, {"tag": "mine", "port": 8443, "protocol": "trojan", "settings": {"clients": [{"password": "p"}]}}],
	  "dns": {"servers": ["1.1.1.1"]}
	}`
	out, merged, err := mergeXray(base, ins, code)
	if err != nil {
		t.Fatal(err)
	}
	var b map[string]any
	_ = json.Unmarshal(out, &b)
	obs := fmt.Sprint(b["outbounds"])
	rules := b["routing"].(map[string]any)["rules"].([]any)
	if !strings.Contains(obs, "tag:warp") || !strings.Contains(obs, "domainStrategy:UseIPv4") || strings.Count(obs, "tag:direct") != 1 {
		t.Errorf("outbounds: %s", obs)
	}
	if len(rules) != 2 || rules[0].(map[string]any)["outboundTag"] != "warp" || b["routing"].(map[string]any)["domainStrategy"] != "IPIfNonMatch" {
		t.Errorf("routing: %v", b["routing"])
	}
	if b["dns"] == nil {
		t.Error("dns not merged")
	}
	if len(merged) != 2 || !strings.Contains(string(merged[0].Config), `"enabled":false`) || !strings.Contains(string(merged[0].Config), `"routeOnly":true`) ||
		merged[1].Tag != "mine" || !strings.Contains(string(merged[1].Config), `"password":"p"`) {
		t.Errorf("inbounds: %+v", merged)
	}
	// a tag that looks like the panel's own cannot be added
	if _, merged, _ := mergeXray(base, ins, `{"inbounds": [{"tag": "n99", "port": 1}]}`); len(merged) != 1 {
		t.Errorf("a reserved tag was added: %+v", merged)
	}
}

// TestConfigCode: the operator's code through the API - merged into what the server gets, shown with
// users left out, and kept from read-only tokens.
func TestConfigCode(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	srv := b.must("POST", "/api/servers", map[string]any{"name": "Code", "address": "203.0.113.90", "protocols": []string{"vless", "hysteria2"}}, 201)["server"].(map[string]any)
	sid := id(srv["id"])
	var vless, hy int64
	for _, n := range srv["nodes"].([]any) {
		n := n.(map[string]any)
		if n["kind"] == "vless" {
			vless = id(n["id"])
		} else {
			hy = id(n["id"])
		}
	}
	b.must("POST", "/api/users", map[string]any{"name": "u"}, 201)
	sp := fmt.Sprintf("/api/servers/%d", sid)
	if code, m, _ := b.do("PATCH", sp, map[string]any{"xray_code": "{\n \"outbounds\": [ {\"tag\": \"x\" ]\n}"}); code != 400 || !strings.Contains(fmt.Sprint(m["error"]), "line 2") {
		t.Errorf("a syntax error: %d %v", code, m)
	}
	xray := fmt.Sprintf(`{
  // block a site for everyone on this server
  "routing": {"rules": [{"domain": ["example.org"], "outboundTag": "block"}]},
  "inbounds": [{"tag": "n%d", "sniffing": {"enabled": false}}]
}`, vless)
	v := b.must("PATCH", sp, map[string]any{"xray_code": xray}, 200)["server"].(map[string]any)
	if !strings.Contains(fmt.Sprint(v["xray_code"]), "block a site") {
		t.Errorf("stored as typed: %v", v["xray_code"])
	}
	st, err := h.p.compileServer(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(st.Xray.Base), "example.org") || !strings.Contains(string(st.Xray.Inbounds[0].Config), `"enabled":false`) {
		t.Errorf("merged: %s / %s", st.Xray.Base, st.Xray.Inbounds[0].Config)
	}
	cfg := b.must("GET", sp+"/config", nil, 200)
	shown := fmt.Sprint(cfg["xray"])
	if !strings.Contains(shown, "example.org") || !strings.Contains(shown, "1 users, managed by Meridian") {
		t.Errorf("config view: %s", shown)
	}

	// Hysteria2 takes YAML; auth stays Meridian's; Xray protocols take theirs in the server's code
	n := b.must("PATCH", fmt.Sprintf("/api/nodes/%d", hy), map[string]any{"code": "quic:\n  maxIdleTimeout: 60s\n"}, 200)
	if !strings.Contains(fmt.Sprint(n["code"]), "maxIdleTimeout") {
		t.Errorf("hysteria code: %v", n["code"])
	}
	st, _ = h.p.compileServer(context.Background(), sid)
	if !strings.Contains(string(st.Hysteria[0].Custom), `"maxIdleTimeout":"60s"`) {
		t.Errorf("hysteria custom: %s", st.Hysteria[0].Custom)
	}
	if code, m, _ := b.do("PATCH", fmt.Sprintf("/api/nodes/%d", hy), map[string]any{"code": "auth:\n  type: password\n"}); code != 400 || !strings.Contains(fmt.Sprint(m["error"]), `"auth" is Meridian's own`) {
		t.Errorf("hysteria auth: %d %v", code, m)
	}
	// an Xray protocol's own settings: merged into its inbound, its outbounds, rules for its traffic only
	own := `{
  // this protocol only
  "sniffing": {"enabled": false},
  "streamSettings": {"sockopt": {"tcpFastOpen": true}},
  "outbounds": [{"tag": "warp", "protocol": "freedom"}],
  "rules": [{"domain": ["example.net"], "outboundTag": "warp"}]
}`
	n = b.must("PATCH", fmt.Sprintf("/api/nodes/%d", vless), map[string]any{"code": own}, 200)
	if !strings.Contains(fmt.Sprint(n["code"]), "this protocol only") {
		t.Errorf("stored: %v", n["code"])
	}
	st, _ = h.p.compileServer(context.Background(), sid)
	var in map[string]any
	_ = json.Unmarshal(st.Xray.Inbounds[0].Config, &in)
	ss := fmt.Sprint(in["streamSettings"])
	if in["sniffing"].(map[string]any)["enabled"] != false || !strings.Contains(ss, "tcpFastOpen:true") || !strings.Contains(ss, "realitySettings") ||
		in["tag"] != proto.InboundTag(vless) {
		t.Errorf("merged inbound: %v", in)
	}
	var base struct {
		Outbounds []map[string]any `json:"outbounds"`
		Routing   struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
	}
	_ = json.Unmarshal(st.Xray.Base, &base)
	if !strings.Contains(fmt.Sprint(base.Outbounds), "tag:warp") {
		t.Errorf("outbounds: %v", base.Outbounds)
	}
	found := false
	for _, r := range base.Routing.Rules {
		if fmt.Sprint(r["domain"]) == "[example.net]" {
			found = true
			if fmt.Sprint(r["inboundTag"]) != "["+proto.InboundTag(vless)+"]" {
				t.Errorf("a protocol's rule must apply to its traffic only: %v", r)
			}
		}
	}
	if !found {
		t.Errorf("rules: %v", base.Routing.Rules)
	}
	for body, want := range map[string]string{
		`{"port": 1}`:                   `"port" is set with the protocol's Port field`,
		`{"settings": {"clients": []}}`: `the panel manages them`,
		`{"outbounds": [{"tag": "direct", "protocol": "freedom"}]}`: `the outbound tag "direct" is the panel's`,
		`{"rules": "x"}`: `"rules" must be a list`,
	} {
		if code, m, _ := b.do("PATCH", fmt.Sprintf("/api/nodes/%d", vless), map[string]any{"code": body}); code != 400 || !strings.Contains(fmt.Sprint(m["error"]), want) {
			t.Errorf("%s: %d %v", body, code, m)
		}
	}

	// read-only tokens see neither the code nor the merged configuration
	ro := b.must("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"}, 201)["token"].(string)
	_, got, _ := h.bearer(ro).do("GET", sp, nil)
	if s := got["server"].(map[string]any); s["xray_code"] != "" {
		t.Errorf("read token saw the code: %v", s["xray_code"])
	}
	if code, _, _ := h.bearer(ro).do("GET", sp+"/config", nil); code != 403 {
		t.Errorf("read token config: %d", code)
	}
}
