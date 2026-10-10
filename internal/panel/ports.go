package panel

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"meridian/internal/subgen"
)

// Servers whose provider decides their ports - NAT servers, LXC and Incus containers - can be
// reached only on the ports the provider forwards to them, sometimes under other numbers. A
// server's public ports list them: protocols and forwards listen on those ports, new ones are
// picked from them, and links carry the number devices connect to.

// portRange is a run of forwarded ports: Public..Public+Count-1 on the server's address reach
// Local..Local+Count-1 on the server, over TCP, UDP or both.
type portRange struct {
	Public, Local, Count int
	TCP, UDP             bool
}

// portMap is what a server's provider forwards; nil means every port reaches the server under its
// own number (an ordinary server).
type portMap []portRange

const maxPortRanges = 64

var (
	// "20000-20019", "40001-40010:10001-10010", "10022:22/tcp"
	portEntryRE = regexp.MustCompile(`^(\d{1,5})(?:-(\d{1,5}))?(?::(\d{1,5})(?:-(\d{1,5}))?)?(?:/(tcp|udp|tcp\+udp))?$`)
	// what people paste between the public port and the server's: "10022 -> 22", "10022 → 22"
	portArrowRE = regexp.MustCompile(`\s*(?:->|=>|→|=|:)\s*`)
	portDashRE  = regexp.MustCompile(`\s*[-–]\s*`)
	portSlashRE = regexp.MustCompile(`\s*/\s*`)
	portSplitRE = regexp.MustCompile(`[,;\s]+`)
)

// parsePortMap reads a list such as "20000-20019, 40001-40010:10001-10010/udp": the ranges the
// provider forwards; PUBLIC:LOCAL where the number on the server differs; /tcp or /udp where only
// one of them is forwarded. Empty means every port.
func parsePortMap(s string) (portMap, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return nil, nil
	}
	if len(s) > 2000 {
		return nil, fmt.Errorf("the port list is too long - use ranges such as 20000-20019")
	}
	s = portArrowRE.ReplaceAllString(s, ":")
	s = portDashRE.ReplaceAllString(s, "-")
	s = portSlashRE.ReplaceAllString(s, "/")
	var m portMap
	for _, e := range portSplitRE.Split(s, -1) {
		if e == "" {
			continue
		}
		g := portEntryRE.FindStringSubmatch(e)
		if g == nil {
			return nil, fmt.Errorf("%q is not a port or range - write ports like 20000-20019, or 40001:10001 when the provider's number differs from the server's", e)
		}
		num := func(x string) int { n, _ := strconv.Atoi(x); return n }
		pub, pubEnd := num(g[1]), num(g[1])
		if g[2] != "" {
			pubEnd = num(g[2])
		}
		loc, locEnd := pub, pubEnd
		if g[3] != "" {
			loc = num(g[3])
			locEnd = loc + (pubEnd - pub)
			if g[4] != "" {
				locEnd = num(g[4])
			}
		}
		switch {
		case pub < 1 || pubEnd > 65535 || loc < 1 || locEnd > 65535:
			return nil, fmt.Errorf("%s: ports go from 1 to 65535", e)
		case pubEnd < pub || locEnd < loc:
			return nil, fmt.Errorf("%s: write ranges low to high, e.g. 20000-20019", e)
		case locEnd-loc != pubEnd-pub:
			return nil, fmt.Errorf("%s: both sides need the same number of ports", e)
		}
		r := portRange{Public: pub, Local: loc, Count: pubEnd - pub + 1, TCP: g[5] != "udp", UDP: g[5] != "tcp"}
		for _, o := range m {
			if !(o.TCP && r.TCP) && !(o.UDP && r.UDP) {
				continue
			}
			if p := overlap(o.Public, o.Count, r.Public, r.Count); p > 0 {
				return nil, fmt.Errorf("public port %d is listed twice", p)
			}
			if p := overlap(o.Local, o.Count, r.Local, r.Count); p > 0 {
				return nil, fmt.Errorf("port %d on the server is listed twice", p)
			}
		}
		if m = append(m, r); len(m) > maxPortRanges {
			return nil, fmt.Errorf("at most %d ranges - join neighbouring ports into ranges such as 20000-20019", maxPortRanges)
		}
	}
	return m, nil
}

// overlap is the first port two runs share, or 0.
func overlap(a, an, b, bn int) int {
	lo, hi := max(a, b), min(a+an, b+bn)
	if lo < hi {
		return lo
	}
	return 0
}

// String writes the map the way parsePortMap reads it.
func (m portMap) String() string {
	parts := make([]string, 0, len(m))
	for _, r := range m {
		s := strconv.Itoa(r.Public)
		if r.Count > 1 {
			s += "-" + strconv.Itoa(r.Public+r.Count-1)
		}
		if r.Local != r.Public {
			s += ":" + strconv.Itoa(r.Local)
			if r.Count > 1 {
				s += "-" + strconv.Itoa(r.Local+r.Count-1)
			}
		}
		switch {
		case !r.UDP:
			s += "/tcp"
		case !r.TCP:
			s += "/udp"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

// short is the map for a message, cut after a few ranges.
func (m portMap) short() string {
	if len(m) <= 4 {
		return m.String()
	}
	return m[:4].String() + ", …"
}

// find is the run forwarding to a port on the server over one network.
func (m portMap) find(local int, udp bool) (portRange, bool) {
	for _, r := range m {
		if (udp && r.UDP || !udp && r.TCP) && local >= r.Local && local < r.Local+r.Count {
			return r, true
		}
	}
	return portRange{}, false
}

// public is the port devices connect to for a port on the server, over the networks given. A
// protocol needing both must reach them under one number.
func (m portMap) public(local int, tcp, udp bool) (int, bool) {
	if m == nil {
		return local, true
	}
	pub := 0
	for _, want := range []bool{false, true} { // tcp, then udp
		if (!want && !tcp) || (want && !udp) {
			continue
		}
		r, ok := m.find(local, want)
		if !ok {
			return 0, false
		}
		p := r.Public + local - r.Local
		if pub != 0 && p != pub {
			return 0, false
		}
		pub = p
	}
	if pub == 0 {
		return local, true
	}
	return pub, true
}

// local is the port on the server a public port reaches over the networks given.
func (m portMap) local(public int, tcp, udp bool) (int, bool) {
	if m == nil {
		return public, true
	}
	for _, r := range m {
		if public >= r.Public && public < r.Public+r.Count && (!tcp || r.TCP) && (!udp || r.UDP) {
			l := r.Local + public - r.Public
			if _, ok := m.public(l, tcp, udp); ok {
				return l, true
			}
		}
	}
	return 0, false
}

// locals calls f with each port on the server reachable over the networks given, in the order the
// ranges were listed, until f returns true. Ports below 1024 are skipped: on a fresh container they
// are where SSH and the like listen.
func (m portMap) locals(tcp, udp bool, f func(port int) bool) {
	for _, r := range m {
		for l := max(r.Local, 1024); l < r.Local+r.Count; l++ {
			if _, ok := m.public(l, tcp, udp); ok && f(l) {
				return
			}
		}
	}
}

// netWord names the networks for a message.
func netWord(tcp, udp bool) string {
	switch {
	case tcp && udp:
		return "TCP and UDP"
	case udp:
		return "UDP"
	}
	return "TCP"
}

// unreachable explains why devices could not reach a port on the server over the networks given,
// or returns "".
func (m portMap) unreachable(port int, tcp, udp bool) string {
	if _, ok := m.public(port, tcp, udp); ok {
		return ""
	}
	_, t := m.find(port, false)
	_, u := m.find(port, true)
	if t || u {
		return fmt.Sprintf("port %d needs %s, and this server's provider forwards it differently (%s) - pick another port, or correct the server's ports from the provider", port, netWord(tcp, udp), m.short())
	}
	return fmt.Sprintf("port %d is not one of the ports this server's provider forwards (%s) - pick one of those, or correct the server's ports from the provider", port, m.short())
}

// reachNets are the networks devices need to reach a protocol: UDP for Hysteria2 and WireGuard,
// TCP for the Xray protocols (their optional UDP is checked separately: see portIssues).
func reachNets(kind string) (tcp, udp bool) {
	if kind == subgen.KindMieru {
		return true, true // its transport is the protocol's choice: either may be asked
	}
	if kind == subgen.KindSnell {
		return true, false // TCP; its QUIC mode on UDP is a bonus
	}
	if kind == subgen.KindHysteria2 || kind == subgen.KindWireGuard {
		return false, true
	}
	return true, false
}

// usesACME says whether a protocol gets its certificate from Let's Encrypt.
func usesACME(kind string, raw json.RawMessage) string {
	if kind == subgen.KindHysteria2 {
		var s hy2Settings
		if json.Unmarshal(raw, &s) == nil && s.CertMode == certACME {
			return s.SNI
		}
		return ""
	}
	if s, err := parseXray(raw); err == nil && acmeNeeded(s) {
		return s.SNI
	}
	return ""
}

// acmeUnreachable explains why Let's Encrypt could not check a domain on this server, or returns "".
func (m portMap) acmeUnreachable(domain string) string {
	if _, ok := m.local(80, true, false); ok || m == nil {
		return ""
	}
	return fmt.Sprintf("Let's Encrypt checks %s over TCP port 80, which this server's provider does not forward - choose a self-signed (pinned) certificate or paste your own", domain)
}

// acmePort is the port on the server where Let's Encrypt's check arrives (0 = 80 itself).
func (m portMap) acmePort() int {
	if l, ok := m.local(80, true, false); ok && l != 80 {
		return l
	}
	return 0
}

// checkNodePort refuses a protocol's port that devices could not reach on this server.
func checkNodePort(s *Server, kind string, raw json.RawMessage, port int) error {
	t, u := reachNets(kind)
	if msg := s.ports.unreachable(port, t, u); msg != "" {
		return errStatus(400, msg)
	}
	if d := usesACME(kind, raw); d != "" {
		if msg := s.ports.acmeUnreachable(d); msg != "" {
			return errStatus(400, msg)
		}
	}
	return nil
}

// pickPort chooses a free port for a new protocol: a usual one if free, otherwise any free one. On
// a server whose provider decides its ports, only those - a usual public number first.
func (p *Panel) pickPort(srv *Server, kind string, settings json.RawMessage, bind string, nodes []*Node, fwds []*Forward, hostPorts []int) int {
	tcp, udp := nodeNets(kind, settings)
	at := listenAddr(kind, bind)
	free := func(port int) bool { return portConflictAt(port, tcp, udp, at, nodes, fwds, 0, 0, hostPorts) == "" }
	if pm := srv.ports; pm != nil {
		rt, ru := reachNets(kind)
		for _, pub := range preferredPorts(kind, settings) {
			if l, ok := pm.local(pub, rt, ru); ok && free(l) {
				return l
			}
		}
		port := 0
		pm.locals(rt, ru, func(l int) bool {
			if free(l) {
				port = l
			}
			return port != 0
		})
		return port
	}
	for _, port := range preferredPorts(kind, settings) {
		if free(port) {
			return port
		}
	}
	for i := 0; i < 1000; i++ {
		if port := 20000 + int(randUint32()%40000); free(port) {
			return port
		}
	}
	return 0
}

// pickForwardPort chooses a free listen port for a forward.
func pickForwardPort(srv *Server, tcp, udp bool, nodes []*Node, fwds []*Forward, hostPorts []int) int {
	free := func(port int) bool { return portConflict(port, tcp, udp, nodes, fwds, 0, 0, hostPorts) == "" }
	if pm := srv.ports; pm != nil {
		port := 0
		pm.locals(tcp, udp, func(l int) bool {
			if free(l) {
				port = l
			}
			return port != 0
		})
		return port
	}
	for i := 0; i < 1000; i++ {
		if port := 30000 + int(randUint32()%30000); free(port) {
			return port
		}
	}
	return 0
}

// noFreePort is the message when no port is left.
func noFreePort(srv *Server) string {
	if srv.ports != nil {
		return fmt.Sprintf("every port this server's provider forwards (%s) is taken - enter one, free one, or add ports in the server's settings", srv.ports.short())
	}
	return "no free port found - enter one"
}

// portIssues lists the protocols and forwards devices cannot reach as configured.
func portIssues(srv *Server, nodes []*Node, fwds []*Forward) []string {
	pm := srv.ports
	if pm == nil {
		return nil
	}
	var out []string
	for _, n := range nodes {
		label := protocolLabel(n.Kind, n.Settings)
		t, u := reachNets(n.Kind)
		if _, ok := pm.public(n.Port, t, u); !ok {
			out = append(out, fmt.Sprintf("%s on port %d can't be reached: this server's provider forwards %s - change its port.", label, n.Port, pm.short()))
			continue
		}
		if nt, nu := nodeNets(n.Kind, n.Settings); nu && !u {
			if _, ok := pm.public(n.Port, nt, true); !ok {
				out = append(out, fmt.Sprintf("%s on port %d: the provider does not forward UDP there, so UDP traffic (calls, games) won't pass.", label, n.Port))
			}
		}
		if d := usesACME(n.Kind, n.Settings); d != "" {
			if msg := pm.acmeUnreachable(d); msg != "" {
				out = append(out, label+": "+msg+".")
			}
		}
	}
	for _, f := range fwds {
		t, u := fwdNets(f.Network)
		if _, ok := pm.public(f.ListenPort, t, u); !ok {
			out = append(out, fmt.Sprintf("The forward on port %d can't be reached over %s: this server's provider forwards %s - change its port.", f.ListenPort, netWord(t, u), pm.short()))
		}
	}
	return out
}

// publicPortOf is the port devices use for a forward, when it differs from the listen port.
func publicPortOf(srv *Server, f *Forward) int {
	t, u := fwdNets(f.Network)
	if p, ok := srv.ports.public(f.ListenPort, t, u); ok && p != f.ListenPort {
		return p
	}
	return 0
}
