package agent

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"meridian/internal/agent/nft"
	"meridian/internal/proto"
)

// Shared servers: besides the panel it was installed from (its home), a server can serve up to two
// more panels its home shares it with (guests). Each guest runs its own protocols, users, forwards
// and traffic rules on it as if the server were its own - everything but the console, which is the
// home's alone. The host itself stays the home's: its cores and their versions, the agent and its
// upgrades, the country rule, relaying for other servers, ACME's port and the DNS settings.
//
// mergeShared lays the guests' states over the home's. A guest's ids move up by slot*shareBase, so
// nothing of it can name something of the home's or of another guest (users, protocols, forwards,
// certificates, WireGuard peers); its traffic rules apply to its own protocols only; and whatever of
// it would reach beyond its share - the host's loopback services, the agent's control ports, a port
// or WireGuard network already taken, files on the host - is left out with the reason, for that
// guest's panel to show.

const (
	shareBase = int64(1_000_000_000_000)
	maxGuests = 2
)

func toGuest(slot int, id int64) int64 {
	if id <= 0 {
		return id
	}
	return int64(slot)*shareBase + id
}

// slotOf says whose an id is: 0 for the home panel's, else the guest's slot (with the guest's own id).
func slotOf(id int64) (int, int64) {
	if id < shareBase {
		return 0, id
	}
	return int(id / shareBase), id % shareBase
}

type guestState struct {
	slot int
	name string // the guest panel's address, for messages
	st   *proto.State
}

// portSet is the TCP/UDP ports in use, with who uses them.
type portSet map[int]string

func (ps portSet) taken(lo, hi int) (int, string) {
	for p := lo; p <= hi; p++ {
		if who, ok := ps[p]; ok {
			return p, who
		}
	}
	return 0, ""
}

func (ps portSet) add(lo, hi int, who string) {
	for p := lo; p <= hi && hi-lo < 70000; p++ {
		ps[p] = who
	}
}

// portRanges reads an inbound's "port": a number, "443", "1000-2000" or "443,8443".
func portRanges(v any) [][2]int {
	var out [][2]int
	switch x := v.(type) {
	case float64:
		out = append(out, [2]int{int(x), int(x)})
	case string:
		for _, part := range strings.Split(x, ",") {
			part = strings.TrimSpace(part)
			a, b, rng := strings.Cut(part, "-")
			lo, err1 := strconv.Atoi(strings.TrimSpace(a))
			hi := lo
			var err2 error
			if rng {
				hi, err2 = strconv.Atoi(strings.TrimSpace(b))
			}
			if err1 == nil && err2 == nil && lo > 0 && hi >= lo && hi <= 65535 {
				out = append(out, [2]int{lo, hi})
			}
		}
	}
	return out
}

func hopRange(s string) (int, int, bool) {
	a, b, ok := strings.Cut(s, "-")
	lo, err1 := strconv.Atoi(strings.TrimSpace(a))
	hi, err2 := strconv.Atoi(strings.TrimSpace(b))
	if !ok || err1 != nil || err2 != nil || lo < 1 || hi < lo || hi > 65535 {
		return 0, 0, false
	}
	return lo, hi, true
}

// usedPorts is what a state's protocols and forwards listen on.
func usedPorts(st *proto.State, who string, ps portSet) {
	if st.Xray != nil {
		for _, in := range st.Xray.Inbounds {
			var obj map[string]any
			if json.Unmarshal(in.Config, &obj) == nil {
				for _, r := range portRanges(obj["port"]) {
					ps.add(r[0], r[1], who)
				}
			}
		}
	}
	for _, h := range st.Hysteria {
		ps.add(h.Port, h.Port, who)
		if lo, hi, ok := hopRange(h.HopPorts); ok {
			ps.add(lo, hi, who)
		}
	}
	for _, w := range st.WireGuard {
		ps.add(w.ListenPort, w.ListenPort, who)
	}
	for _, f := range st.Forwards {
		ps.add(f.ListenPort, f.ListenPort, who)
	}
	for _, n := range st.Solo {
		for _, u := range n.Users {
			ps.add(u.Port, u.Port, who)
		}
	}
}

// publicIP says whether an address is one a guest may send traffic to: not this host's loopback, a
// private or link-local network, multicast or unspecified.
func publicIP(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsValid() && a.IsGlobalUnicast() && !a.IsPrivate() && !a.IsLoopback() && !a.IsLinkLocalUnicast() &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(a)
}

var hostRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+\.?$`)

// publicDest checks a "host:port" a guest's inbound sends strangers to (a REALITY site, a fallback):
// a public address, or a name (whose connections the kernel guard keeps off this host - see
// nft.DirectMark - and which is never one of the agent's ports by number alone).
func publicDest(v any) error {
	s := ""
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		s = x
	case float64:
		return fmt.Errorf("%v is a port on this host", x)
	default:
		return fmt.Errorf("%v cannot be read", x)
	}
	if s == "" {
		return nil
	}
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "@") {
		return fmt.Errorf("%s is a socket on this host", s)
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		if _, err := strconv.Atoi(s); err == nil {
			return fmt.Errorf("%s is a port on this host", s)
		}
		return fmt.Errorf("%q is not host:port", s)
	}
	if _, err := strconv.Atoi(port); err != nil {
		return fmt.Errorf("%q has no port", s)
	}
	if a, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		if !publicIP(a) {
			return fmt.Errorf("%s is not a public address", host)
		}
		return nil
	}
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") || strings.HasSuffix(strings.ToLower(strings.TrimSuffix(host, ".")), ".localhost") || !hostRE.MatchString(host) {
		return fmt.Errorf("%s is not a public name", host)
	}
	return nil
}

var (
	userEmailRE = regexp.MustCompile(`^s(\d+)\.n(\d+)$`)
	passEmailRE = regexp.MustCompile(`^([pr])(\d+)$`)
	certRefRE   = regexp.MustCompile(regexp.QuoteMeta(proto.CertPrefix) + `(\d+)/`)
)

// guestEmail moves a client's name into the guest's range: users "s<sub>.n<node>", proxy passes
// "p<protocol>" and other servers' traffic rules "r<server>"; anything else gets the slot in front.
func guestEmail(slot int, e string) string {
	if m := userEmailRE.FindStringSubmatch(e); m != nil {
		a, _ := strconv.ParseInt(m[1], 10, 64)
		b, _ := strconv.ParseInt(m[2], 10, 64)
		return proto.Email(toGuest(slot, a), toGuest(slot, b))
	}
	if m := passEmailRE.FindStringSubmatch(e); m != nil {
		n, _ := strconv.ParseInt(m[2], 10, 64)
		return m[1] + strconv.FormatInt(toGuest(slot, n), 10)
	}
	return fmt.Sprintf("g%d.%s", slot, e)
}

// guestTag names a guest's outbound, rule or balancer; ownTag one the agent adds for the guest.
func guestTag(slot int, t string) string { return fmt.Sprintf("g%d.%s", slot, t) }
func ownTag(slot int, t string) string   { return fmt.Sprintf("g%d:%s", slot, t) }

var guestInboundProtocols = []string{"vless", "vmess", "trojan", "shadowsocks", "socks", "http"}

// guestInbound checks and moves one of a guest's Xray inbounds. It returns the inbound as the host
// runs it, or why it cannot run.
func guestInbound(slot int, in proto.XrayInbound, ports portSet, who string) (proto.XrayInbound, error) {
	id, ok := proto.ParseInboundTag(in.Tag)
	if !ok {
		return in, fmt.Errorf("%s: an inbound of your own code cannot be used on a shared server", in.Tag)
	}
	var obj map[string]any
	if err := json.Unmarshal(in.Config, &obj); err != nil {
		return in, fmt.Errorf("%s: %w", in.Tag, err)
	}
	p, _ := obj["protocol"].(string)
	if !slices.Contains(guestInboundProtocols, p) {
		return in, fmt.Errorf("%s: %s cannot be used on a shared server", in.Tag, p)
	}
	if l, _ := obj["listen"].(string); l != "" {
		a, err := netip.ParseAddr(strings.Trim(l, "[]"))
		if err != nil || a.IsLoopback() || a.IsLinkLocalUnicast() {
			return in, fmt.Errorf("%s: it may not listen on %s on a shared server", in.Tag, l)
		}
	}
	ranges := portRanges(obj["port"])
	if len(ranges) == 0 {
		return in, fmt.Errorf("%s: it has no port", in.Tag)
	}
	for _, r := range ranges {
		if port, owner := ports.taken(r[0], r[1]); port != 0 {
			return in, fmt.Errorf("%s: port %d is used by %s on this server - choose another port", in.Tag, port, owner)
		}
	}
	// what strangers are sent to: a public address only
	if ss, ok := obj["streamSettings"].(map[string]any); ok {
		if rs, ok := ss["realitySettings"].(map[string]any); ok {
			for _, k := range []string{"target", "dest"} {
				if err := publicDest(rs[k]); err != nil {
					return in, fmt.Errorf("%s: its camouflage site %v", in.Tag, err)
				}
			}
		}
		if so, ok := ss["sockopt"].(map[string]any); ok {
			delete(so, "tproxy")
			delete(so, "mark")
		}
		for _, k := range []string{"tlsSettings", "xtlsSettings"} {
			ts, _ := ss[k].(map[string]any)
			cs, _ := ts["certificates"].([]any)
			for _, c := range cs {
				cm, _ := c.(map[string]any)
				for _, f := range []string{"certificateFile", "keyFile"} {
					if v, _ := cm[f].(string); v != "" && !strings.HasPrefix(v, proto.ACMEPrefix) && !strings.HasPrefix(v, proto.CertPrefix) {
						return in, fmt.Errorf("%s: a certificate file on the host (%s) cannot be used on a shared server", in.Tag, v)
					}
				}
			}
		}
	}
	if st, ok := obj["settings"].(map[string]any); ok {
		delete(st, "clients") // the users are the clients below, moved into the guest's range
		if fbs, ok := st["fallbacks"].([]any); ok {
			for _, fb := range fbs {
				if m, ok := fb.(map[string]any); ok {
					if err := publicDest(m["dest"]); err != nil {
						return in, fmt.Errorf("%s: a fallback %v", in.Tag, err)
					}
				}
			}
		}
	}
	tag := proto.InboundTag(toGuest(slot, id))
	obj["tag"] = tag
	b, _ := json.Marshal(obj)
	b = certRefRE.ReplaceAllFunc(b, func(m []byte) []byte {
		n, _ := strconv.ParseInt(string(m[len(proto.CertPrefix):len(m)-1]), 10, 64)
		return []byte(proto.CertPrefix + strconv.FormatInt(toGuest(slot, n), 10) + "/")
	})
	out := proto.XrayInbound{Tag: tag, Config: b, ACME: in.ACME, Cert: toGuest(slot, in.Cert)}
	for _, c := range in.Clients {
		c.Email = guestEmail(slot, c.Email)
		var cj map[string]any
		if json.Unmarshal(c.JSON, &cj) == nil {
			if _, ok := cj["email"]; ok {
				cj["email"] = c.Email
			}
			c.JSON, _ = json.Marshal(cj)
		}
		out.Clients = append(out.Clients, c)
	}
	for _, r := range ranges {
		ports.add(r[0], r[1], who)
	}
	return out, nil
}

var guestOutboundProtocols = []string{"freedom", "blackhole", "vless", "vmess", "trojan", "shadowsocks", "socks", "http", "wireguard", "hysteria"}

// guestBase moves a guest's outbounds, traffic rules and load balancers into its own names, keeps
// its rules to its own protocols, and refuses what would reach beyond its share. inbounds are the
// guest's inbound tags as the host runs them.
func guestBase(slot int, raw json.RawMessage, inbounds []string) (outs, rules, bals []any, observe []string, errs []string) {
	var base map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &base) != nil {
		return nil, nil, nil, nil, nil
	}
	tags := map[string]bool{}
	obs, _ := base["outbounds"].([]any)
	for _, o := range obs {
		if m, ok := o.(map[string]any); ok {
			if t, _ := m["tag"].(string); t != "" {
				tags[t] = true
			}
		}
	}
	for _, o := range obs {
		m, ok := o.(map[string]any)
		if !ok {
			continue
		}
		t, _ := m["tag"].(string)
		p, _ := m["protocol"].(string)
		if !slices.Contains(guestOutboundProtocols, p) {
			errs = append(errs, fmt.Sprintf("outbound %s: %s cannot be used on a shared server", t, p))
			continue
		}
		if st, ok := m["settings"].(map[string]any); ok && p == "freedom" {
			if r, _ := st["redirect"].(string); r != "" {
				errs = append(errs, fmt.Sprintf("outbound %s: a redirect cannot be used on a shared server", t))
				continue
			}
		}
		ss, _ := m["streamSettings"].(map[string]any)
		if ss == nil {
			ss = map[string]any{}
			m["streamSettings"] = ss
		}
		so, _ := ss["sockopt"].(map[string]any)
		if so == nil {
			so = map[string]any{}
			ss["sockopt"] = so
		}
		// every connection a guest's outbound makes is marked: the kernel keeps it off this host and
		// private networks, whatever name it was asked for (nft.DirectMark)
		so["mark"] = nft.DirectMark
		if dp, _ := so["dialerProxy"].(string); dp != "" {
			if !tags[dp] {
				delete(so, "dialerProxy")
			} else {
				so["dialerProxy"] = guestTag(slot, dp)
			}
		}
		if ps, ok := m["proxySettings"].(map[string]any); ok {
			if pt, _ := ps["tag"].(string); pt != "" && tags[pt] {
				ps["tag"] = guestTag(slot, pt)
			} else {
				delete(m, "proxySettings")
			}
		}
		m["tag"] = guestTag(slot, t)
		outs = append(outs, m)
	}
	routing, _ := base["routing"].(map[string]any)
	bl, _ := routing["balancers"].([]any)
	balTags := map[string]bool{}
	for _, b := range bl {
		m, ok := b.(map[string]any)
		if !ok {
			continue
		}
		t, _ := m["tag"].(string)
		balTags[t] = true
		var sel []any
		ls, _ := m["selector"].([]any)
		for _, x := range ls {
			if s, ok := x.(string); ok {
				sel = append(sel, guestTag(slot, s))
			}
		}
		m["selector"] = sel
		if ft, _ := m["fallbackTag"].(string); ft != "" {
			m["fallbackTag"] = guestTag(slot, ft)
		}
		m["tag"] = guestTag(slot, t)
		bals = append(bals, m)
	}
	rl, _ := routing["rules"].([]any)
	for _, r := range rl {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		// its own protocols only: tags moved into the guest's range; none named = all of its own
		var in []string
		if list, ok := m["inboundTag"].([]any); ok {
			for _, x := range list {
				s, _ := x.(string)
				if id, ok := proto.ParseInboundTag(s); ok {
					if t := proto.InboundTag(toGuest(slot, id)); slices.Contains(inbounds, t) {
						in = append(in, t)
					}
				}
			}
			if len(in) == 0 {
				continue // a rule for protocols the host does not run of it
			}
		} else {
			in = slices.Clone(inbounds)
		}
		if len(in) == 0 {
			continue
		}
		m["inboundTag"] = in
		if users, ok := m["user"].([]any); ok {
			var us []any
			for _, u := range users {
				if s, ok := u.(string); ok {
					us = append(us, guestEmail(slot, s))
				}
			}
			m["user"] = us
		}
		switch {
		case m["balancerTag"] != nil:
			bt, _ := m["balancerTag"].(string)
			if !balTags[bt] {
				continue
			}
			m["balancerTag"] = guestTag(slot, bt)
		default:
			ot, _ := m["outboundTag"].(string)
			if !tags[ot] {
				continue // to something the guest does not have
			}
			m["outboundTag"] = guestTag(slot, ot)
		}
		if rt, _ := m["ruleTag"].(string); rt != "" {
			m["ruleTag"] = guestTag(slot, rt)
		}
		rules = append(rules, m)
	}
	if ob, ok := base["observatory"].(map[string]any); ok {
		if ls, ok := ob["subjectSelector"].([]any); ok {
			for _, x := range ls {
				if s, ok := x.(string); ok {
					observe = append(observe, guestTag(slot, s))
				}
			}
		}
	}
	return outs, rules, bals, observe, errs
}

// wgName is a guest's WireGuard interface's name on the host (at most 15 characters).
func wgName(slot int, node int64) string { return fmt.Sprintf("mg%dw%d", slot, node%1_000_000_000) }

func wgNets(w proto.WGInterface) []netip.Prefix {
	var out []netip.Prefix
	for _, a := range w.Address {
		if p, err := netip.ParsePrefix(a); err == nil {
			out = append(out, p.Masked())
		}
	}
	return out
}

// mergeShared is the state the host runs: the home panel's, with each guest's laid over it (see the
// top of this file); and per guest slot, what of the guest's could not be used and why.
func mergeShared(home *proto.State, guests []guestState, reserved []int) (*proto.State, map[int][]string) {
	errs := map[int][]string{}
	if home == nil || len(guests) == 0 {
		return home, errs
	}
	out := *home
	ports := portSet{}
	for _, p := range reserved {
		ports.add(p, p, "the agent itself")
	}
	usedPorts(home, "the server's own panel", ports)
	var nets []netip.Prefix
	for _, w := range home.WireGuard {
		nets = append(nets, wgNets(w)...)
	}
	var xr proto.Xray
	var baseObj map[string]any
	if home.Xray != nil {
		xr.Inbounds = slices.Clone(home.Xray.Inbounds)
		_ = json.Unmarshal(home.Xray.Base, &baseObj)
	}
	if baseObj == nil {
		baseObj = map[string]any{}
	}
	out.Hysteria = slices.Clone(home.Hysteria)
	out.Solo = slices.Clone(home.Solo) // mieru and Snell: the home panel's alone
	out.WireGuard = slices.Clone(home.WireGuard)
	out.Forwards = slices.Clone(home.Forwards)
	out.Certs = slices.Clone(home.Certs)
	out.Speed = slices.Clone(home.Speed)
	out.Refuse = slices.Clone(home.Refuse)
	out.Grace = slices.Clone(home.Grace)
	out.Ping = slices.Clone(home.Ping)

	var gOuts, gRules, gBals []any
	var gObserve []string
	for _, g := range guests {
		if g.st == nil || g.slot < 1 || g.slot > maxGuests {
			continue
		}
		slot, st := g.slot, g.st
		who := "another panel this server is shared with"
		note := func(format string, a ...any) { errs[slot] = append(errs[slot], fmt.Sprintf(format, a...)) }
		if len(st.Solo) > 0 {
			note("mieru and Snell are not available on a server shared with another panel - they are left out")
		}
		var inTags []string
		if st.Xray != nil {
			for _, in := range st.Xray.Inbounds {
				gi, err := guestInbound(slot, in, ports, who)
				if err != nil {
					note("%v", err)
					continue
				}
				xr.Inbounds = append(xr.Inbounds, gi)
				inTags = append(inTags, gi.Tag)
			}
			if len(inTags) > 0 {
				outs, rules, bals, observe, berrs := guestBase(slot, st.Xray.Base, inTags)
				for _, e := range berrs {
					note("%s", e)
				}
				// the guest's own direct and block, whatever its configuration has
				// the agent's own for the guest (names no panel writes: "g1:"), whatever its configuration has
				outs = append(outs, map[string]any{"tag": ownTag(slot, "direct"), "protocol": "freedom",
					"streamSettings": map[string]any{"sockopt": map[string]any{"mark": nft.DirectMark}}},
					map[string]any{"tag": ownTag(slot, "block"), "protocol": "blackhole"})
				safety := []any{map[string]any{"ruleTag": ownTag(slot, "no-private"), "inboundTag": inTags,
					"ip": []string{"geoip:private"}, "outboundTag": ownTag(slot, "block")}}
				if len(st.BlockedIPs) > 0 { // its blocks keep those addresses from its own protocols
					safety = append(safety, map[string]any{"ruleTag": ownTag(slot, "blocked"), "inboundTag": inTags,
						"source": st.BlockedIPs, "outboundTag": ownTag(slot, "block")})
				}
				// what none of its rules takes leaves through its own direct way
				tail := map[string]any{"ruleTag": ownTag(slot, "rest"), "inboundTag": inTags, "network": "tcp,udp",
					"outboundTag": ownTag(slot, "direct")}
				gOuts = append(gOuts, outs...)
				gRules = append(gRules, safety...)
				gRules = append(gRules, rules...)
				gRules = append(gRules, tail)
				gBals = append(gBals, bals...)
				gObserve = append(gObserve, observe...)
			}
		}
		for _, h := range st.Hysteria {
			if port, owner := ports.taken(h.Port, h.Port); port != 0 {
				note("Hysteria2 protocol %d: port %d is used by %s on this server - choose another port", h.NodeID, port, owner)
				continue
			}
			if lo, hi, ok := hopRange(h.HopPorts); ok {
				if port, owner := ports.taken(lo, hi); port != 0 {
					note("Hysteria2 protocol %d: its port hopping range takes port %d, used by %s - choose another range", h.NodeID, port, owner)
					continue
				}
			}
			if a, err := netip.ParseAddr(h.Bind); h.Bind != "" && (err != nil || a.IsLoopback() || a.IsLinkLocalUnicast()) {
				note("Hysteria2 protocol %d: it may not listen on %s on a shared server", h.NodeID, h.Bind)
				continue
			}
			if len(h.Custom) > 0 && string(h.Custom) != "null" && string(h.Custom) != "{}" {
				note("Hysteria2 protocol %d: its advanced settings (code) are left out on a shared server", h.NodeID)
				h.Custom = nil
			}
			ports.add(h.Port, h.Port, who)
			if lo, hi, ok := hopRange(h.HopPorts); ok {
				ports.add(lo, hi, who)
			}
			h.NodeID = toGuest(slot, h.NodeID)
			us := make([]proto.HyUser, 0, len(h.Users))
			for _, u := range h.Users {
				us = append(us, proto.HyUser{ID: guestEmail(slot, u.ID), Password: u.Password})
			}
			h.Users = us
			out.Hysteria = append(out.Hysteria, h)
		}
		for _, w := range st.WireGuard {
			if port, owner := ports.taken(w.ListenPort, w.ListenPort); port != 0 {
				note("WireGuard protocol %d: port %d is used by %s on this server - choose another port", w.NodeID, port, owner)
				continue
			}
			clash := ""
			for _, n := range wgNets(w) {
				for _, o := range nets {
					if n.Overlaps(o) {
						clash = n.String()
					}
				}
			}
			if clash != "" {
				note("WireGuard protocol %d: its network %s overlaps one already on this server - choose another network for it", w.NodeID, clash)
				continue
			}
			nets = append(nets, wgNets(w)...)
			ports.add(w.ListenPort, w.ListenPort, who)
			w.Name = wgName(slot, w.NodeID)
			w.NodeID = toGuest(slot, w.NodeID)
			peers := make([]proto.WGPeer, 0, len(w.Peers))
			for _, pr := range w.Peers {
				pr.SubID = toGuest(slot, pr.SubID)
				peers = append(peers, pr)
			}
			w.Peers = peers
			out.WireGuard = append(out.WireGuard, w)
		}
		for _, f := range st.Forwards {
			if port, owner := ports.taken(f.ListenPort, f.ListenPort); port != 0 {
				note("forward %d: port %d is used by %s on this server - choose another port", f.ID, port, owner)
				continue
			}
			host, _, err := net.SplitHostPort(f.Target)
			a, perr := netip.ParseAddr(strings.Trim(host, "[]"))
			if err != nil || perr != nil || !publicIP(a) {
				note("forward %d: on a shared server a forward goes to a public IP address (%s is not one)", f.ID, f.Target)
				continue
			}
			ports.add(f.ListenPort, f.ListenPort, who)
			f.ID = toGuest(slot, f.ID)
			out.Forwards = append(out.Forwards, f)
		}
		for _, c := range st.Certs {
			c.ID = toGuest(slot, c.ID)
			out.Certs = append(out.Certs, c)
		}
		for _, sp := range st.Speed {
			sp.Sub = toGuest(slot, sp.Sub)
			out.Speed = append(out.Speed, sp)
		}
		for _, r := range st.Refuse {
			r.Sub = toGuest(slot, r.Sub)
			out.Refuse = append(out.Refuse, r)
		}
		for _, g := range st.Grace {
			g.Sub = toGuest(slot, g.Sub)
			out.Grace = append(out.Grace, g)
		}
		for _, pt := range st.Ping { // measured only to public addresses (the agent checks each round)
			pt.ID = toGuest(slot, pt.ID)
			out.Ping = append(out.Ping, pt)
		}
		if st.Geo != nil {
			note("its country rule is left out: on a shared server the server's own panel decides who may connect by country")
		}
	}

	// the guests' rules go before the home's rules that have no protocols of their own (those would
	// take the guests' traffic first), after the home's others
	if len(gOuts) > 0 || len(gRules) > 0 {
		hOuts, _ := baseObj["outbounds"].([]any)
		baseObj["outbounds"] = append(slices.Clone(hOuts), gOuts...)
		routing, _ := baseObj["routing"].(map[string]any)
		if routing == nil {
			routing = map[string]any{}
		}
		hRules, _ := routing["rules"].([]any)
		// the home's rules keep their order; the guests' go in before the first of them that would take
		// any traffic (no protocols of its own, not a block) - so the home's traffic meets them in no
		// other order, and the guests' never reaches the home's catch-all rules
		at := len(hRules)
		for i, r := range hRules {
			m, _ := r.(map[string]any)
			if _, scoped := m["inboundTag"]; !scoped && !isBlockRule(m) {
				at = i
				break
			}
		}
		rules := append(append(slices.Clone(hRules[:at]), gRules...), hRules[at:]...)
		routing["rules"] = rules
		if len(gBals) > 0 {
			hb, _ := routing["balancers"].([]any)
			routing["balancers"] = append(slices.Clone(hb), gBals...)
		}
		baseObj["routing"] = routing
		if len(gObserve) > 0 {
			ob, _ := baseObj["observatory"].(map[string]any)
			if ob == nil {
				ob = map[string]any{"probeUrl": "https://www.gstatic.com/generate_204", "probeInterval": "1m", "enableConcurrency": true} // as the panel writes it
			}
			ls, _ := ob["subjectSelector"].([]any)
			for _, s := range gObserve {
				ls = append(ls, s)
			}
			ob["subjectSelector"] = ls
			baseObj["observatory"] = ob
		}
	}
	xr.Base, _ = json.Marshal(baseObj)
	if home.Xray != nil || len(xr.Inbounds) > 0 {
		out.Xray = &xr
	}
	return &out, errs
}

// isBlockRule says whether a rule only blocks (the home's safety rules apply to everyone first).
func isBlockRule(m map[string]any) bool {
	t, _ := m["outboundTag"].(string)
	return t == "block"
}
