package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/net/idna"

	"meridian/internal/proto"
	"meridian/internal/subgen"
)

// A protocol's blocked sites (Node.Blocked): what it carries never reaches these domains or their
// subdomains, whatever traffic rules say. Each core does it its own way, on the name the app asks for
// or, where it reads connections, the one in them:
//
//   - Xray: a rule on the protocol's inbound, first of all the panel's rules, applied live;
//   - Hysteria2: reject() lines in its ACL, which it reads when it starts: like any change of its own
//     settings, saving restarts it once (the panel's window asks first);
//   - mieru and AnyTLS: rules in each user's configuration (agents with Caps.BlockSites), waiting as
//     pending restart for a click (internal/agent/solo).
//
// Snell and WireGuard have nothing to do it with, so they are refused: a list that does nothing would
// be worse than none. Whole addresses and countries stay with the traffic rules (routing.go).

const maxBlocked = 200

var blockedRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// canBlockSites says whether a kind of protocol can tell sites apart.
func canBlockSites(kind string) bool {
	k, ok := kindOf(kind)
	return ok && k.Engine != "wireguard" && kind != subgen.KindSnell
}

// cleanSite reads one entry: a domain, however it was pasted (a whole address, *.example.com).
func cleanSite(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if strings.Contains(v, "://") {
		if u, err := url.Parse(v); err == nil {
			v = u.Hostname()
		}
	}
	v, _, _ = strings.Cut(v, "/")
	v = strings.TrimSuffix(strings.TrimPrefix(v, "*."), ".")
	if h, port, found := strings.Cut(v, ":"); found && !strings.Contains(port, ":") {
		v = h
	}
	if _, err := netip.ParseAddr(strings.Trim(v, "[]")); err == nil {
		return "", errStatus(http.StatusBadRequest, fmt.Sprintf("%q is an address, not a site: block addresses with a traffic rule (Routing)", truncate(raw, 80)))
	}
	if a, err := idna.Lookup.ToASCII(v); err == nil {
		v = a
	}
	v = strings.ToLower(v)
	if len(v) > 253 || !blockedRE.MatchString(v) {
		return "", errStatus(http.StatusBadRequest, fmt.Sprintf("%q is not a site: write a domain, such as anthropic.com", truncate(raw, 80)))
	}
	return v, nil
}

// blockedFor checks a protocol's list for its server and returns it clean.
func blockedFor(s *Server, kind string, in []string) ([]string, error) {
	out := []string{}
	for _, raw := range in {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		v, err := cleanSite(raw)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return out, nil
	}
	if len(out) > maxBlocked {
		return nil, errStatus(http.StatusBadRequest, fmt.Sprintf("a protocol can block %d sites at most", maxBlocked))
	}
	k, _ := kindOf(kind)
	if !canBlockSites(kind) {
		return nil, errStatus(http.StatusBadRequest, k.Label+" cannot block sites: it carries traffic without knowing which site it is for. Use a protocol that can, or block the addresses with a traffic rule")
	}
	if k.Engine == "hysteria" && !s.agent06() {
		return nil, needAgent06("Blocking sites on Hysteria2")
	}
	if k.Engine == "solo" && s.FirstSeenAt > 0 {
		var caps proto.Caps
		_ = json.Unmarshal([]byte(s.Caps), &caps)
		if !caps.BlockSites {
			return nil, errStatus(http.StatusBadRequest, "Blocking sites on "+k.Label+" needs agent 1.4.4 or later on this server: upgrade its agent first (Settings › Updates › Upgrade all agents)")
		}
	}
	return out, nil
}

func blockedJSON(sites []string) string {
	if len(sites) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(sites)
	return string(b)
}

// blockedRules are the Xray protocols' rules, one each.
func blockedRules(nodes []*Node) []map[string]any {
	var out []map[string]any
	for _, n := range nodes {
		if k, ok := kindOf(n.Kind); !ok || k.Engine != "xray" || !n.Enabled || len(n.Blocked) == 0 {
			continue
		}
		domains := make([]string, len(n.Blocked))
		for i, d := range n.Blocked {
			domains[i] = "domain:" + d
		}
		out = append(out, map[string]any{"ruleTag": fmt.Sprintf("sites-n%d", n.ID),
			"inboundTag": []string{proto.InboundTag(n.ID)}, "domain": domains, "outboundTag": "block"})
	}
	return out
}

// hyBlocked adds a Hysteria2 protocol's blocked sites to its own configuration, ahead of its own ACL.
// The rest goes out directly, as it does with no ACL. The agent still puts its private-address rules
// in front of all of it.
func hyBlocked(custom json.RawMessage, sites []string) json.RawMessage {
	if len(sites) == 0 {
		return custom
	}
	cfg := map[string]any{}
	if len(custom) > 0 && json.Unmarshal(custom, &cfg) != nil {
		return custom
	}
	acl, _ := cfg["acl"].(map[string]any)
	if acl == nil {
		acl = map[string]any{}
	}
	own, _ := acl["inline"].([]any)
	if len(own) == 0 {
		own = []any{"direct(all)"}
	}
	rules := make([]any, 0, len(sites)+len(own))
	for _, d := range sites {
		rules = append(rules, "reject(suffix:"+d+")")
	}
	acl["inline"] = append(rules, own...)
	cfg["acl"] = acl
	b, err := json.Marshal(cfg)
	if err != nil {
		return custom
	}
	return b
}

// hyBlockedChange says whether update_protocol's list changes a Hysteria2 protocol's: that restarts it.
func (c *mcpCall) hyBlockedChange(id int64, a map[string]any) bool {
	sites, ok := argStrs(a, "blocked_sites")
	if !ok {
		return false
	}
	n, err := c.p.nodeByID(c.r.Context(), id)
	if err != nil || n.Kind != subgen.KindHysteria2 {
		return false
	}
	want := []string{}
	for _, raw := range sites {
		if v, err := cleanSite(raw); err == nil && !slices.Contains(want, v) {
			want = append(want, v)
		}
	}
	return !slices.Equal(want, n.Blocked)
}
