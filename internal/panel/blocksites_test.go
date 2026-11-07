package panel

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"meridian/internal/proto"
)

func TestCleanSite(t *testing.T) {
	for in, want := range map[string]string{
		"anthropic.com":                     "anthropic.com",
		" Anthropic.COM. ":                  "anthropic.com",
		"*.google.com":                      "google.com",
		"https://www.google.com/search?q=1": "www.google.com",
		"claude.ai:443":                     "claude.ai",
		"bücher.example":                    "xn--bcher-kva.example",
	} {
		if got, err := cleanSite(in); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"1.1.1.1", "[2606:4700::1111]", "localhost", "exa mple.com", "keyword:google", "regexp:.*", "a..b", "-a.com", `"x".com`, "a,b.com", strings.Repeat("a", 64) + ".com"} {
		if got, err := cleanSite(in); err == nil {
			t.Errorf("%q read as %q", in, got)
		}
	}
}

// TestBlockedSites: each kind of protocol takes its list where it can act on it, nothing comes before
// it but the limits, and the kinds that cannot tell sites apart refuse it.
func TestBlockedSites(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.9"}, 201)["server"].(map[string]any)["id"])
	sites := []string{"Anthropic.com", "https://www.google.com/x", "anthropic.com", ""}
	want := []string{"anthropic.com", "www.google.com"}
	add := func(kind string, extra map[string]any) int64 {
		in := map[string]any{"kind": kind, "blocked_sites": sites}
		for k, v := range extra {
			in[k] = v
		}
		return id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), in, 201)["id"])
	}
	vless := add("vless", nil)
	hy := add("hysteria2", nil)
	plain := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": "shadowsocks"}, 201)["id"])

	for _, kind := range []string{"snell", "wireguard"} {
		code, _, raw := b.do("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": kind, "blocked_sites": []string{"google.com"}})
		if code != 400 || !strings.Contains(string(raw), "cannot block sites") {
			t.Errorf("%s: %d %s", kind, code, raw)
		}
	}
	// a server whose agent does not know how: refused, not quietly ignored
	if _, err := h.p.db.Exec1(`UPDATE servers SET first_seen_at = ?, caps = ? WHERE id = ?`, now(), `{"solo":true,"anytls":true,"nftables":true}`, sid); err != nil {
		t.Fatal(err)
	}
	code, _, raw := b.do("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": "mieru", "blocked_sites": []string{"google.com"}})
	if code != 400 || !strings.Contains(string(raw), "agent 1.4.4") {
		t.Errorf("an old agent: %d %s", code, raw)
	}
	if _, err := h.p.db.Exec1(`UPDATE servers SET caps = ? WHERE id = ?`, `{"solo":true,"anytls":true,"nftables":true,"block_sites":true}`, sid); err != nil {
		t.Fatal(err)
	}
	mieru := add("mieru", nil)

	n, err := h.p.nodeByID(t.Context(), vless)
	if err != nil || !slices.Equal(n.Blocked, want) {
		t.Fatalf("stored: %v %v", n, err)
	}
	s := fmt.Sprintf("/api/nodes/%d", plain)
	for _, bad := range []string{"1.1.1.1", "not a site"} {
		if code, _, raw := b.do("PATCH", s, map[string]any{"blocked_sites": []string{bad}}); code != 400 {
			t.Errorf("%s: %d %s", bad, code, raw)
		}
	}
	tooMany := make([]string, maxBlocked+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("s%d.example.com", i)
	}
	if code, _, _ := b.do("PATCH", s, map[string]any{"blocked_sites": tooMany}); code != 400 {
		t.Errorf("too many: %d", code)
	}

	// a traffic rule that sends everything elsewhere must not get around the list
	b.must("POST", "/api/routing/rules", map[string]any{"name": "All direct", "enabled": true, "match": map[string]any{"all": true}, "target": "direct"}, 200)
	st, err := h.p.compileServer(t.Context(), sid)
	if err != nil {
		t.Fatal(err)
	}
	var base struct {
		Routing struct {
			Rules []struct {
				RuleTag     string   `json:"ruleTag"`
				InboundTag  []string `json:"inboundTag"`
				Domain      []string `json:"domain"`
				OutboundTag string   `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(st.Xray.Base, &base); err != nil {
		t.Fatal(err)
	}
	mine, others, firstOther := -1, 0, -1
	for i, r := range base.Routing.Rules {
		switch {
		case r.RuleTag == fmt.Sprintf("sites-n%d", vless):
			mine = i
			if r.OutboundTag != "block" || !slices.Equal(r.InboundTag, []string{proto.InboundTag(vless)}) || !slices.Equal(r.Domain, []string{"domain:anthropic.com", "domain:www.google.com"}) {
				t.Errorf("the rule: %+v", r)
			}
		case strings.HasPrefix(r.RuleTag, "sites-"):
			others++
		case strings.HasPrefix(r.RuleTag, "r") && firstOther < 0:
			firstOther = i
		}
	}
	if mine < 0 || others != 0 || firstOther >= 0 && mine > firstOther {
		t.Errorf("mine %d, others %d, the traffic rule %d: %+v", mine, others, firstOther, base.Routing.Rules)
	}

	var hyNode *proto.HyNode
	for i := range st.Hysteria {
		if st.Hysteria[i].NodeID == hy {
			hyNode = &st.Hysteria[i]
		}
	}
	if hyNode == nil || !strings.Contains(string(hyNode.Custom), "reject(suffix:anthropic.com)") || !strings.Contains(string(hyNode.Custom), "direct(all)") {
		t.Errorf("Hysteria2: %+v", hyNode)
	}
	for _, sn := range st.Solo {
		if sn.NodeID == mieru && !slices.Equal(sn.Blocked, want) {
			t.Errorf("mieru: %v", sn.Blocked)
		}
	}

	// emptied: nothing left behind
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", vless), map[string]any{"blocked_sites": []string{}}, 200)
	st, _ = h.p.compileServer(t.Context(), sid)
	if strings.Contains(string(st.Xray.Base), fmt.Sprintf("sites-n%d", vless)) {
		t.Error("the rule stayed")
	}
}

func TestHyBlockedKeepsItsOwnACL(t *testing.T) {
	got := hyBlocked(json.RawMessage(`{"acl":{"inline":["reject(geoip:cn)","direct(all)"]},"speedTest":true}`), []string{"google.com"})
	var c struct {
		ACL struct {
			Inline []string `json:"inline"`
		} `json:"acl"`
		SpeedTest bool `json:"speedTest"`
	}
	if err := json.Unmarshal(got, &c); err != nil || !c.SpeedTest {
		t.Fatalf("%s %v", got, err)
	}
	if !slices.Equal(c.ACL.Inline, []string{"reject(suffix:google.com)", "reject(geoip:cn)", "direct(all)"}) {
		t.Errorf("%v", c.ACL.Inline)
	}
	if x := hyBlocked(nil, nil); x != nil {
		t.Errorf("nothing to block: %s", x)
	}
}
