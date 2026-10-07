// Package nft owns the agent's nftables table ("inet meridian"): kernel port forwarding with exact
// per-rule byte counters, NAT for WireGuard clients, traffic counters for realm forwards, and
// manual IP blocks. The whole table is replaced in one atomic nft transaction; established
// connections keep flowing because conntrack keeps their NAT mappings.
package nft

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"meridian/internal/proto"
)

const (
	Table    = "meridian"
	markBase = 0x4d520000 // forward marks: base + forward id
)

// WGNat describes one WireGuard interface whose clients are NATed to the internet.
type WGNat struct {
	NodeID  int64
	Iface   string
	Subnet4 string
	Subnet6 string
}

// WGMark marks connections from WireGuard clients so the agent can read just those from conntrack.
const WGMark = 0x4d570000

// Spec is everything the table should contain.
type Spec struct {
	Forwards []proto.Forward
	WG       []WGNat
	Blocked  []string
	// ports the host serves (proxies, WireGuard, forwards) - blocked IPs are dropped only there,
	// so a mistaken block can never lock anyone out of SSH
	TCPPorts []int
	UDPPorts []int
	// LocalOnly are loopback TCP ports of control endpoints that only root may connect to
	LocalOnly []int
	// Geo is the country rule; nil = none
	Geo *GeoSpec
}

// GeoSpec limits who may reach the services, by address list: Allow = only these networks,
// otherwise these networks are refused. Except always gets in (private networks are added here).
type GeoSpec struct {
	Allow  bool
	V4, V6 []string // "a.b.c.d", "a.b.c.d/n" or "a.b.c.d-e.f.g.h" (and the IPv6 forms)
	Except []string // addresses or networks
}

// privateNets are never subject to the country rule: local networks, loopback, tunnels.
var privateNets = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "127.0.0.0/8",
	"169.254.0.0/16", "fc00::/7", "fe80::/10", "::1/128"}

// geoElem checks one list element and says its family.
func geoElem(s string) (string, bool) {
	if from, to, ok := strings.Cut(s, "-"); ok {
		a, err1 := netip.ParseAddr(from)
		b, err2 := netip.ParseAddr(to)
		if err1 != nil || err2 != nil || a.Zone() != "" || b.Zone() != "" || a.Is4() != b.Is4() || b.Less(a) {
			return "", false
		}
		return map[bool]string{true: "4", false: "6"}[a.Is4()], true
	}
	if p, err := netip.ParsePrefix(s); err == nil {
		return map[bool]string{true: "4", false: "6"}[p.Addr().Is4()], true
	}
	if a, err := netip.ParseAddr(s); err == nil && a.Zone() == "" {
		return map[bool]string{true: "4", false: "6"}[a.Unmap().Is4()], true
	}
	return "", false
}

// GeoCounts says how many list entries are usable (the rest are dropped as malformed).
func (g *GeoSpec) clean() (v4, v6, ex4, ex6 []string) {
	for _, list := range [][]string{g.V4, g.V6} {
		for _, e := range list {
			switch fam, ok := geoElem(e); {
			case ok && fam == "4":
				v4 = append(v4, e)
			case ok:
				v6 = append(v6, e)
			}
		}
	}
	for _, e := range append(append([]string{}, privateNets...), g.Except...) {
		fam, ok := geoElem(e)
		if !ok {
			continue
		}
		if a, err := netip.ParseAddr(e); err == nil {
			e = a.Unmap().String()
		}
		if fam == "4" {
			ex4 = append(ex4, e)
		} else {
			ex6 = append(ex6, e)
		}
	}
	return
}

type Counter struct {
	Up, Down, Conns int64
}

type Engine struct {
	mu         sync.Mutex
	applied    string
	last       map[string][2]uint64 // comment -> bytes, packets at last read
	pending    map[int64]*Counter   // deltas not yet collected
	resolved   map[string][]netip.Addr
	resolvedAt time.Time
	geoDrops   int64 // packets the country rule dropped, not yet collected
}

// TakeGeoDrops returns the packets the country rule dropped since the last call.
func (e *Engine) TakeGeoDrops() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.applied != "" {
		e.readCounters()
	}
	n := e.geoDrops
	e.geoDrops = 0
	return n
}

func New() *Engine {
	return &Engine{last: map[string][2]uint64{}, pending: map[int64]*Counter{}, resolved: map[string][]netip.Addr{}}
}

// Available reports whether nft is installed.
func Available() bool {
	_, err := exec.LookPath("nft")
	return err == nil
}

// Apply installs spec. Unchanged specs cost nothing.
func (e *Engine) Apply(spec Spec) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	essential := len(spec.Forwards) > 0 || len(spec.WG) > 0 || len(spec.Blocked) > 0 || spec.Geo != nil
	if !essential && !Available() {
		return nil // the loopback guard alone is a hardening extra, not worth an error without nft
	}
	empty := !essential && len(uniqPorts(spec.LocalOnly)) == 0
	if empty {
		if e.applied != "" || tableExists() {
			e.readCounters()
			run("delete table inet " + Table + "\n")
			e.applied = ""
			e.last = map[string][2]uint64{}
		}
		return nil
	}
	if !Available() {
		return fmt.Errorf("nftables (nft) is not installed - install the nftables package")
	}
	text := e.render(spec)
	if text == e.applied && tableExists() {
		return nil
	}
	if needsForwarding(spec) {
		enableForwarding()
	}
	e.readCounters() // keep the counts gathered under the old rules
	script := "add table inet " + Table + "\ndelete table inet " + Table + "\n" + text
	if err := run(script); err != nil {
		return err
	}
	e.applied = text
	e.last = map[string][2]uint64{}
	ensureForwardAccept(spec)
	return nil
}

func needsForwarding(spec Spec) bool {
	for _, f := range spec.Forwards {
		if f.Engine != "realm" {
			return true
		}
	}
	return len(spec.WG) > 0
}

// Remove deletes the table (decommission).
func (e *Engine) Remove() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if tableExists() {
		run("delete table inet " + Table + "\n")
	}
	e.applied = ""
}

func (e *Engine) resolve(host string) []netip.Addr {
	if a, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return []netip.Addr{a.Unmap()}
	}
	if time.Since(e.resolvedAt) > 5*time.Minute {
		e.resolved = map[string][]netip.Addr{}
		e.resolvedAt = time.Now()
	}
	if v, ok := e.resolved[host]; ok {
		return v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, _ := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	var out []netip.Addr
	have4, have6 := false, false
	for _, ip := range ips {
		ip = ip.Unmap()
		if ip.Is4() && !have4 {
			out, have4 = append(out, ip), true
		}
		if ip.Is6() && !have6 {
			out, have6 = append(out, ip), true
		}
	}
	e.resolved[host] = out
	return out
}

// Refresh re-resolves domain targets; it reports whether any address changed.
func (e *Engine) Refresh(spec Spec) error {
	e.mu.Lock()
	e.resolvedAt = time.Time{}
	e.mu.Unlock()
	return e.Apply(spec)
}

func comment(id int64, what string) string { return fmt.Sprintf("f%d:%s", id, what) }

func (e *Engine) render(spec Spec) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...); b.WriteByte('\n') }
	var blk4, blk6 []string
	for _, s := range spec.Blocked {
		if p, err := netip.ParsePrefix(s); err == nil {
			if p.Addr().Is4() {
				blk4 = append(blk4, p.Masked().String())
			} else {
				blk6 = append(blk6, p.Masked().String())
			}
		} else if a, err := netip.ParseAddr(s); err == nil {
			if a.Unmap().Is4() {
				blk4 = append(blk4, a.Unmap().String())
			} else {
				blk6 = append(blk6, a.String())
			}
		}
	}
	tcp, udp := uniqPorts(spec.TCPPorts), uniqPorts(spec.UDPPorts)
	var fwds []proto.Forward
	for _, f := range spec.Forwards {
		if f.ListenPort > 0 && f.ListenPort < 65536 {
			fwds = append(fwds, f)
		}
	}
	spec.Forwards = fwds
	var wgs []WGNat
	for _, g := range spec.WG {
		if ifaceRE.MatchString(g.Iface) {
			if _, err := netip.ParsePrefix(g.Subnet4); err != nil {
				g.Subnet4 = ""
			}
			wgs = append(wgs, g)
		}
	}
	spec.WG = wgs
	for _, f := range spec.Forwards {
		if strings.Contains(f.Network, "tcp") {
			tcp = uniqPorts(append(tcp, f.ListenPort))
		}
		if strings.Contains(f.Network, "udp") {
			udp = uniqPorts(append(udp, f.ListenPort))
		}
	}

	w("table inet %s {", Table)
	w("  set block4 { type ipv4_addr; flags interval; auto-merge%s }", elems(blk4))
	w("  set block6 { type ipv6_addr; flags interval; auto-merge%s }", elems(blk6))
	w("  set svc_tcp { type inet_service%s }", elemsInt(tcp))
	w("  set svc_udp { type inet_service%s }", elemsInt(udp))
	if spec.Geo != nil {
		v4, v6, ex4, ex6 := spec.Geo.clean()
		w("  set geo4 { type ipv4_addr; flags interval; auto-merge%s }", bigElems(v4))
		w("  set geo6 { type ipv6_addr; flags interval; auto-merge%s }", bigElems(v6))
		w("  set geoex4 { type ipv4_addr; flags interval; auto-merge%s }", elems(ex4))
		w("  set geoex6 { type ipv6_addr; flags interval; auto-merge%s }", elems(ex6))
		// the country rule: exceptions first, then refuse what the rule says
		in, out := "!= ", "!= "
		if !spec.Geo.Allow {
			in, out = "", ""
		}
		w("  chain geo_in {")
		w("    ip saddr @geoex4 return")
		w("    ip6 saddr @geoex6 return")
		w("    meta nfproto ipv4 ip saddr %s@geo4 counter drop comment \"geo:in\"", in)
		w("    meta nfproto ipv6 ip6 saddr %s@geo6 counter drop comment \"geo:in\"", in)
		w("  }")
		// nothing flows back either, so connections already open when the rule changed stop at once
		w("  chain geo_out {")
		w("    ip daddr @geoex4 return")
		w("    ip6 daddr @geoex6 return")
		w("    meta nfproto ipv4 ip daddr %s@geo4 counter drop", out)
		w("    meta nfproto ipv6 ip6 daddr %s@geo6 counter drop", out)
		w("  }")
	}

	// blocked IPs never reach any of our services or forwards
	w("  chain guard {")
	w("    type filter hook prerouting priority -150; policy accept;")
	w("    ip saddr @block4 tcp dport @svc_tcp counter drop")
	w("    ip saddr @block4 udp dport @svc_udp counter drop")
	w("    ip6 saddr @block6 tcp dport @svc_tcp counter drop")
	w("    ip6 saddr @block6 udp dport @svc_udp counter drop")
	if spec.Geo != nil {
		w("    tcp dport @svc_tcp jump geo_in")
		w("    udp dport @svc_udp jump geo_in")
	}
	w("  }")
	// ...and nothing flows back to them either, so open sessions (QUIC especially, whose large
	// windows would let a server keep sending until its idle timeout) stop at once
	w("  chain guard_out {")
	w("    type filter hook output priority -150; policy accept;")
	w("    ip daddr @block4 tcp sport @svc_tcp counter drop")
	w("    ip daddr @block4 udp sport @svc_udp counter drop")
	w("    ip6 daddr @block6 tcp sport @svc_tcp counter drop")
	w("    ip6 daddr @block6 udp sport @svc_udp counter drop")
	if spec.Geo != nil {
		w("    tcp sport @svc_tcp jump geo_out")
		w("    udp sport @svc_udp jump geo_out")
	}
	w("  }")
	w("  chain guard_fwd {")
	w("    type filter hook forward priority -150; policy accept;")
	w("    ip daddr @block4 ct mark and 0xffff0000 == 0x%08x counter drop", markBase)
	w("    ip6 daddr @block6 ct mark and 0xffff0000 == 0x%08x counter drop", markBase)
	if spec.Geo != nil {
		w("    ct mark and 0xffff0000 == 0x%08x ct direction reply jump geo_out", markBase)
	}
	w("  }")

	w("  chain prerouting {")
	w("    type nat hook prerouting priority dstnat; policy accept;")
	for _, f := range spec.Forwards {
		if f.Engine == "realm" {
			continue
		}
		host, ps, err := net.SplitHostPort(f.Target)
		if err != nil {
			continue
		}
		pn, err := strconv.Atoi(ps)
		if err != nil || pn < 1 || pn > 65535 {
			continue
		}
		port := strconv.Itoa(pn)
		mark := fmt.Sprintf("0x%08x", markBase+uint32(f.ID&0xffff))
		for _, ip := range e.resolve(host) {
			fam, l3 := "ipv4", "ip"
			to := ip.String() + ":" + port
			if ip.Is6() {
				fam, l3, to = "ipv6", "ip6", "["+ip.String()+"]:"+port
			}
			for _, proto := range []string{"tcp", "udp"} {
				if !strings.Contains(f.Network, proto) {
					continue
				}
				w("    meta nfproto %s %s dport %d counter ct mark set %s dnat %s to %s comment %q", fam, proto,
					f.ListenPort, mark, l3, to, comment(f.ID, "conn"))
			}
		}
	}
	w("  }")

	w("  chain postrouting {")
	w("    type nat hook postrouting priority srcnat; policy accept;")
	for _, f := range spec.Forwards {
		if f.Engine == "realm" {
			continue
		}
		w("    ct mark 0x%08x masquerade", markBase+uint32(f.ID&0xffff))
	}
	for _, g := range spec.WG {
		if g.Subnet4 != "" {
			w("    ip saddr %s oifname != %q masquerade", g.Subnet4, g.Iface)
		}
	}
	w("  }")

	w("  chain forward {")
	w("    type filter hook forward priority filter - 5; policy accept;")
	for _, f := range spec.Forwards {
		if f.Engine == "realm" {
			continue
		}
		mark := markBase + uint32(f.ID&0xffff)
		w("    ct mark 0x%08x ct direction original counter comment %q", mark, comment(f.ID, "up"))
		w("    ct mark 0x%08x ct direction reply counter comment %q", mark, comment(f.ID, "down"))
		w("    ct mark 0x%08x accept", mark)
	}
	for _, g := range spec.WG {
		w("    iifname %q ct state new ct mark set 0x%08x", g.Iface, WGMark+uint32(g.NodeID&0xffff))
		w("    iifname %q accept", g.Iface)
		w("    oifname %q ct state established,related accept", g.Iface)
	}
	w("  }")

	// realm listens itself: count what passes its port in and out
	w("  chain input {")
	w("    type filter hook input priority filter - 5; policy accept;")
	for _, g := range spec.WG {
		if g.Subnet4 != "" { // the logging DNS resolver answers WireGuard clients only
			w("    ip daddr %s meta l4proto { tcp, udp } th dport 53 iifname != %q drop", g.Subnet4, g.Iface)
		}
	}
	for _, f := range spec.Forwards {
		if f.Engine != "realm" {
			continue
		}
		for _, proto := range []string{"tcp", "udp"} {
			if strings.Contains(f.Network, proto) {
				w("    %s dport %d ct state new counter comment %q", proto, f.ListenPort, comment(f.ID, "conn"))
				w("    %s dport %d counter comment %q", proto, f.ListenPort, comment(f.ID, "up"))
			}
		}
	}
	w("  }")
	w("  chain output {")
	w("    type filter hook output priority filter - 5; policy accept;")
	if local := uniqPorts(spec.LocalOnly); len(local) > 0 {
		w("    oifname \"lo\" tcp dport { %s } meta skuid != 0 counter reject with tcp reset", joinInts(local))
	}
	for _, f := range spec.Forwards {
		if f.Engine != "realm" {
			continue
		}
		for _, proto := range []string{"tcp", "udp"} {
			if strings.Contains(f.Network, proto) {
				w("    %s sport %d counter comment %q", proto, f.ListenPort, comment(f.ID, "down"))
			}
		}
	}
	w("  }")
	w("}")
	return b.String()
}

// bigElems writes a long element list across lines.
func bigElems(list []string) string {
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("; elements = {")
	for i, e := range list {
		if i%48 == 0 {
			b.WriteString("\n    ")
		}
		b.WriteString(e)
		if i < len(list)-1 {
			b.WriteString(", ")
		}
	}
	b.WriteString("\n  ")
	b.WriteString("}")
	return b.String()
}

func elems(list []string) string {
	if len(list) == 0 {
		return ""
	}
	sort.Strings(list)
	return "; elements = { " + strings.Join(list, ", ") + " }"
}

var ifaceRE = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,15}$`)

func joinInts(list []int) string {
	s := make([]string, len(list))
	for i, p := range list {
		s[i] = strconv.Itoa(p)
	}
	return strings.Join(s, ", ")
}

func elemsInt(list []int) string {
	if len(list) == 0 {
		return ""
	}
	s := make([]string, len(list))
	for i, p := range list {
		s[i] = strconv.Itoa(p)
	}
	return "; elements = { " + strings.Join(s, ", ") + " }"
}

func uniqPorts(in []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, p := range in {
		if p > 0 && p < 65536 && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Ints(out)
	return out
}

// ---------------------------------------------------------------- counters

// readCounters folds the current counter values into pending deltas. Caller holds e.mu.
func (e *Engine) readCounters() {
	out, err := exec.Command("nft", "-j", "list", "table", "inet", Table).Output()
	if err != nil {
		return
	}
	var doc struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	if json.Unmarshal(out, &doc) != nil {
		return
	}
	sums := map[string][2]uint64{}
	for _, item := range doc.Nftables {
		raw, ok := item["rule"]
		if !ok {
			continue
		}
		var rule struct {
			Comment string           `json:"comment"`
			Expr    []map[string]any `json:"expr"`
		}
		if json.Unmarshal(raw, &rule) != nil || rule.Comment == "" {
			continue
		}
		for _, ex := range rule.Expr {
			if c, ok := ex["counter"].(map[string]any); ok {
				by, _ := c["bytes"].(float64)
				pk, _ := c["packets"].(float64)
				s := sums[rule.Comment]
				s[0] += uint64(by)
				s[1] += uint64(pk)
				sums[rule.Comment] = s
			}
		}
	}
	for key, cur := range sums {
		prev := e.last[key]
		if cur[0] < prev[0] || cur[1] < prev[1] {
			prev = [2]uint64{} // counters were reset
		}
		if key == "geo:in" {
			e.geoDrops += int64(cur[1] - prev[1])
			e.last[key] = cur
			continue
		}
		var id int64
		var what string
		if _, err := fmt.Sscanf(strings.Replace(key, ":", " ", 1), "f%d %s", &id, &what); err != nil {
			continue
		}
		c := e.pending[id]
		if c == nil {
			c = &Counter{}
			e.pending[id] = c
		}
		switch what {
		case "up":
			c.Up += int64(cur[0] - prev[0])
		case "down":
			c.Down += int64(cur[0] - prev[0])
		case "conn":
			c.Conns += int64(cur[1] - prev[1])
		}
		e.last[key] = cur
	}
}

// Take returns per-forward byte and connection deltas since the last call.
func (e *Engine) Take() map[int64]Counter {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.applied != "" {
		e.readCounters()
	}
	out := map[int64]Counter{}
	for id, c := range e.pending {
		if c.Up != 0 || c.Down != 0 || c.Conns != 0 {
			out[id] = *c
		}
	}
	e.pending = map[int64]*Counter{}
	return out
}

// ---------------------------------------------------------------- host plumbing

func tableExists() bool {
	return exec.Command("nft", "list", "table", "inet", Table).Run() == nil
}

func run(script string) error {
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nft: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

// enableForwarding turns on IPv4 forwarding now and on boot. IPv6 forwarding is left alone: on hosts
// that learn their IPv6 route by router advertisement it would drop that route.
func enableForwarding() {
	_ = os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0o644)
	_ = os.WriteFile("/proc/sys/net/netfilter/nf_conntrack_acct", []byte("1\n"), 0o644)
	conf := "# written by meridian-agent\nnet.ipv4.ip_forward = 1\nnet.netfilter.nf_conntrack_acct = 1\n"
	if old, _ := os.ReadFile("/etc/sysctl.d/99-meridian.conf"); string(old) != conf {
		_ = os.WriteFile("/etc/sysctl.d/99-meridian.conf", []byte(conf), 0o644)
	}
}

// ensureForwardAccept handles hosts whose iptables FORWARD policy is DROP (Docker sets that): our
// own accept cannot override a drop in another table, so we add explicit accepts there.
func ensureForwardAccept(spec Spec) {
	if _, err := exec.LookPath("iptables"); err != nil {
		return
	}
	out, err := exec.Command("iptables", "-S", "FORWARD").Output()
	if err != nil || !strings.Contains(string(out), "-P FORWARD DROP") {
		return
	}
	add := func(args ...string) {
		check := append([]string{"-C", "FORWARD"}, args...)
		if exec.Command("iptables", check...).Run() == nil {
			return
		}
		_ = exec.Command("iptables", append([]string{"-I", "FORWARD", "1"}, args...)...).Run()
	}
	for _, f := range spec.Forwards {
		if f.Engine == "realm" {
			continue
		}
		add("-m", "connmark", "--mark", fmt.Sprintf("0x%08x", markBase+uint32(f.ID&0xffff)), "-m", "comment",
			"--comment", "meridian", "-j", "ACCEPT")
	}
	for _, g := range spec.WG {
		add("-i", g.Iface, "-m", "comment", "--comment", "meridian", "-j", "ACCEPT")
		add("-o", g.Iface, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-m", "comment", "--comment",
			"meridian", "-j", "ACCEPT")
	}
}
