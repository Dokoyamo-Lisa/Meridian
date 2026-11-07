package panel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The network profile of a server, for its details on the status page: where its address sits on the
// internet (the routes to it, its network, how that network is used) and how clean it and its /24 look.
//
// A server's address must not get out, to visitors or to the services asked:
//   - what is kept and served is a netProfile, made of counts, AS numbers and an address already
//     masked (maskAddr): the address itself has no field, here or in the status page's data;
//   - RIPEstat is asked about the /24 (IPv6: the /48) and then the announced prefix, never the address;
//     PeeringDB about the AS number alone;
//   - the threat lists are downloaded and read here, so looking an address up tells nobody;
//   - nothing that names a host or could carry the address is kept: no reverse DNS, no whois text, and
//     the names of networks pass scrubAddrs.
//
// It runs only with status_network on: the first request starts it, the jobs repeat it daily.

const (
	netEvery    = 24 * time.Hour
	netRetry    = 10 * time.Minute // between runs a visitor's request may start
	netListCap  = 128 << 20        // the largest list read
	netAnswer   = 8 << 20          // the largest answer of RIPEstat or PeeringDB read
	netMaxLevel = 3                // networks shown behind the server's own
)

var (
	ripeBase    = "https://stat.ripe.net/data"
	peeringBase = "https://www.peeringdb.com/api"
	netClient   = func() *http.Client { c := publicClient(); c.Timeout = 3 * time.Minute; return c }
)

// netKind is what a list says about an address.
type netKind int

const (
	netAbuse netKind = iota
	netProxy
	netVPN
	netTor
	netDrop
	netHosting
	netKinds
)

// netLists are the public lists read: reputable sources over HTTPS, IPv4 only.
var netLists = []struct {
	kind netKind
	url  string
}{
	{netAbuse, "https://raw.githubusercontent.com/firehol/blocklist-ipsets/master/firehol_level2.netset"},
	{netAbuse, "https://raw.githubusercontent.com/firehol/blocklist-ipsets/master/firehol_abusers_30d.netset"},
	{netProxy, "https://raw.githubusercontent.com/firehol/blocklist-ipsets/master/firehol_proxies.netset"},
	{netVPN, "https://raw.githubusercontent.com/X4BNet/lists_vpn/main/output/vpn/ipv4.txt"},
	{netTor, "https://check.torproject.org/torbulkexitlist"},
	{netDrop, "https://www.spamhaus.org/drop/drop_v4.json"},
	{netHosting, "https://raw.githubusercontent.com/X4BNet/lists_vpn/main/output/datacenter/ipv4.txt"},
}

type netNote struct {
	Text   string `json:"text"`
	Points int    `json:"points" doc:"What it takes off the score"`
}

type netBlock struct {
	Range string `json:"range" doc:"The /24, masked"`
	Abuse int    `json:"abuse" doc:"Addresses of the /24 on abuse lists"`
	Proxy int    `json:"proxy" doc:"... on lists of public proxies"`
	VPN   int    `json:"vpn" doc:"... on lists of VPN networks"`
	Tor   int    `json:"tor" doc:"... that are Tor exits"`
	Drop  int    `json:"drop" doc:"... on Spamhaus DROP"`
	Clean int    `json:"clean" doc:"... on none of these lists"`
}

type netNode struct {
	ASN   int64  `json:"asn"`
	Name  string `json:"name"`
	Level int    `json:"level" doc:"0: the server's network; 1: its upstreams; further: theirs"`
	Paths int    `json:"paths" doc:"Routes seen through it"`
}

type netEdge struct {
	From  int64 `json:"from" doc:"The network nearer the server"`
	To    int64 `json:"to"`
	Paths int   `json:"paths"`
}

// netProfile is everything kept and served. It has no field for the address itself.
type netProfile struct {
	State     string    `json:"state" doc:"ready, or pending while it is first looked up"`
	Address   string    `json:"address,omitempty" doc:"The address with its last four digits hidden (IPv6: its last four groups)"`
	Prefix    string    `json:"prefix,omitempty" doc:"The announced prefix, as masked as the address"`
	ASN       int64     `json:"asn,omitempty"`
	Holder    string    `json:"holder,omitempty" doc:"Who holds the AS number"`
	ASNSpace  int64     `json:"asn_space,omitempty" doc:"IPv4 addresses that network announces"`
	Usage     []string  `json:"usage,omitempty" doc:"How the address is used, as far as known"`
	Score     *int      `json:"score,omitempty" doc:"0-100, higher is cleaner; none when no list could be read, or for IPv6"`
	Notes     []netNote `json:"notes,omitempty" doc:"What took points off the score"`
	Block     *netBlock `json:"block,omitempty" doc:"The /24 around it (IPv4)"`
	Nodes     []netNode `json:"nodes,omitempty" doc:"The networks on the routes to it"`
	Edges     []netEdge `json:"edges,omitempty"`
	RouteSeen int       `json:"routes,omitempty" doc:"Routes seen by RIPE's collectors"`
	Updated   int64     `json:"updated,omitempty"`
}

// netStore keeps the profiles, in memory only: nothing about addresses is written down.
type netStore struct {
	mu      sync.Mutex
	byID    map[int64]*netProfile
	names   map[int64]string
	running bool
	last    time.Time
}

// maskAddr hides the end of an address: the last four digits of an IPv4 address, whichever octets they
// are in, and the last four groups of an IPv6 address. An address it cannot read is hidden whole.
func maskAddr(a netip.Addr) string {
	a = a.Unmap()
	switch {
	case a.Is4():
		b := []byte(a.String())
		for i, left := len(b)-1, 4; i >= 0 && left > 0; i-- {
			if b[i] != '.' {
				b[i] = '*'
				left--
			}
		}
		return string(b)
	case a.Is6():
		w := a.WithZone("").As16()
		var g []string
		for i := 0; i < 8; i += 2 {
			g = append(g, fmt.Sprintf("%x", int(w[i])<<8|int(w[i+1])))
		}
		return strings.Join(g, ":") + ":****:****:****:****"
	}
	return "****"
}

// addrLike finds what could be an address in a network's name.
var addrLike = regexp.MustCompile(`\b\d{1,3}(?:[.-]\d{1,3}){3}\b|\b[0-9a-fA-F]{1,4}(?::[0-9a-fA-F]{0,4}){2,7}\b`)

// scrubAddrs takes anything shaped like an address out of a name from elsewhere.
func scrubAddrs(s string) string {
	return strings.TrimSpace(addrLike.ReplaceAllString(s, ""))
}

// profileAddr is the address of a server the profile is about: its first public IPv4, else IPv6.
func profileAddr(s *Server) (netip.Addr, bool) {
	var first netip.Addr
	for _, raw := range publicAddrsOf(s, nil) {
		a, err := netip.ParseAddr(raw)
		if err != nil {
			continue
		}
		if a.Is4() {
			return a, true
		}
		if !first.IsValid() {
			first = a
		}
	}
	return first, first.IsValid()
}

// outerPrefix is what RIPEstat is asked about: never the address, its /24 or /48.
func outerPrefix(a netip.Addr) netip.Prefix {
	bits := 48
	if a.Is4() {
		bits = 24
	}
	p, _ := a.Prefix(bits)
	return p
}

// ---------------------------------------------------------------- the lists

// netHits is what the lists say about one /24: per kind, its 256 addresses.
type netHits struct {
	marks [netKinds][256]bool
	read  [netKinds]bool
}

func (h *netHits) count(k netKind) int {
	n := 0
	for _, on := range h.marks[k] {
		if on {
			n++
		}
	}
	return n
}

// mark adds a list's entry to the /24s it touches. By /24 for what is smaller, and through all of
// them for what is larger, which few entries are.
func markEntry(targets map[netip.Prefix]*netHits, k netKind, e netip.Prefix) {
	if !e.Addr().Is4() {
		return
	}
	if e.Bits() < 24 {
		for t, h := range targets {
			if e.Overlaps(t) {
				for i := range h.marks[k] {
					h.marks[k][i] = true
				}
			}
		}
		return
	}
	base, _ := e.Addr().Prefix(24)
	h := targets[base]
	if h == nil {
		return
	}
	first := int(e.Masked().Addr().As4()[3])
	for i := 0; i < 1<<(32-e.Bits()); i++ {
		h.marks[k][first+i] = true
	}
}

// parseEntry reads a line of a list: an address, a network, or Spamhaus's {"cidr": ...}.
func parseEntry(line string) (netip.Prefix, bool) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] == '#' || line[0] == ';' {
		return netip.Prefix{}, false
	}
	if line[0] == '{' {
		var e struct {
			CIDR string `json:"cidr"`
		}
		if json.Unmarshal([]byte(line), &e) != nil {
			return netip.Prefix{}, false
		}
		line = e.CIDR
	}
	if p, err := netip.ParsePrefix(line); err == nil {
		return p, true
	}
	if a, err := netip.ParseAddr(line); err == nil {
		return netip.PrefixFrom(a, a.BitLen()), true
	}
	return netip.Prefix{}, false
}

// readLists reads every list once, line by line, keeping only what touches targets. A list that
// cannot be read leaves its kind unread: its absence is not a clean bill.
func readLists(ctx context.Context, targets map[netip.Prefix]*netHits) {
	client := netClient()
	for _, l := range netLists {
		if err := readList(ctx, client, l.url, l.kind, targets); err != nil {
			slog.Warn("network profile: a list could not be read", "list", l.url, "err", err)
			continue
		}
		for _, h := range targets {
			h.read[l.kind] = true
		}
	}
}

func readList(ctx context.Context, client *http.Client, u string, k netKind, targets map[netip.Prefix]*netHits) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Meridian/"+Version)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("answered %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, netListCap))
	sc.Buffer(make([]byte, 4096), 1<<20)
	for sc.Scan() {
		if e, ok := parseEntry(sc.Text()); ok {
			markEntry(targets, k, e)
		}
	}
	return sc.Err()
}

// blockOf sums a /24 up, by the address's own mask.
func blockOf(a netip.Addr, h *netHits) *netBlock {
	b := &netBlock{Range: maskAddr(a) + "/24", Abuse: h.count(netAbuse), Proxy: h.count(netProxy), VPN: h.count(netVPN),
		Tor: h.count(netTor), Drop: h.count(netDrop)}
	for i := 0; i < 256; i++ {
		if !h.marks[netAbuse][i] && !h.marks[netProxy][i] && !h.marks[netVPN][i] && !h.marks[netTor][i] && !h.marks[netDrop][i] {
			b.Clean++
		}
	}
	return b
}

// scoreOf is the address's score: 100 less what the lists say about it and, by less, its /24. Hosting
// costs nothing: a server in a data centre is where a server should be.
func scoreOf(a netip.Addr, h *netHits) (*int, []netNote) {
	if !a.Is4() || !(h.read[netAbuse] || h.read[netProxy] || h.read[netVPN] || h.read[netTor] || h.read[netDrop]) {
		return nil, nil
	}
	i := int(a.As4()[3])
	score := 100
	var notes []netNote
	take := func(on bool, points int, text string) {
		if on {
			score -= points
			notes = append(notes, netNote{Text: text, Points: points})
		}
	}
	take(h.marks[netDrop][i], 60, "On Spamhaus DROP")
	take(h.marks[netAbuse][i], 40, "On an abuse list")
	take(h.marks[netProxy][i], 25, "Listed as a public proxy")
	take(h.marks[netTor][i], 25, "A Tor exit")
	take(h.marks[netVPN][i], 15, "In a VPN network")
	listed := 256 - blockOf(a, h).Clean
	switch {
	case listed >= 64:
		take(true, 15, "A quarter or more of its /24 is listed")
	case listed >= 13:
		take(true, 8, "Part of its /24 is listed")
	case listed > 0:
		take(true, 3, "A few of its /24 are listed")
	}
	return new(max(score, 0)), notes
}

// ---------------------------------------------------------------- the routes

// netFetch reads a JSON answer into out. Its errors never reach a visitor.
func netFetch(ctx context.Context, client *http.Client, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Meridian/"+Version)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("answered %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, netAnswer)).Decode(out)
}

func ripeCall(ctx context.Context, client *http.Client, call, resource string, out any) error {
	u := ripeBase + "/" + call + "/data.json?sourceapp=meridian&resource=" + url.QueryEscape(resource)
	return netFetch(ctx, client, u, &struct {
		Data any `json:"data"`
	}{out})
}

// asPath reads an AS path: the server's network first, repeats gone, sets and oddities ending it.
func asPath(raw string) []int64 {
	var out []int64
	for _, f := range strings.Fields(raw) {
		n, err := strconv.ParseInt(f, 10, 64)
		if err != nil || n <= 0 {
			break
		}
		if len(out) == 0 || out[len(out)-1] != n {
			out = append(out, n)
		}
	}
	slices.Reverse(out)
	return out
}

// routeGraph turns the routes into levels: origin, then at most six networks a level, those most
// routes go through, each at the nearest place it appears. Only links between shown networks stay.
func routeGraph(paths [][]int64, origin int64) ([]netNode, []netEdge) {
	level := map[int64]int{origin: 0}
	seen := map[int64]int{}
	links := map[[2]int64]int{}
	for _, p := range paths {
		if len(p) == 0 || p[0] != origin {
			continue
		}
		seen[origin]++
		for i := 1; i < len(p) && i <= netMaxLevel; i++ {
			if l, ok := level[p[i]]; !ok || i < l {
				level[p[i]] = i
			}
			seen[p[i]]++
			links[[2]int64{p[i-1], p[i]}]++
		}
	}
	if seen[origin] == 0 {
		return nil, nil
	}
	nodes := []netNode{{ASN: origin, Paths: seen[origin]}}
	shown := map[int64]bool{origin: true}
	for l := 1; l <= netMaxLevel; l++ {
		// only networks a drawn one leads to: the rest would stand alone in the graph
		reached := map[int64]bool{}
		for k := range links {
			if shown[k[0]] && level[k[0]] < l && level[k[1]] == l {
				reached[k[1]] = true
			}
		}
		var row []netNode
		for asn := range reached {
			row = append(row, netNode{ASN: asn, Level: l, Paths: seen[asn]})
		}
		sort.Slice(row, func(i, j int) bool {
			if row[i].Paths != row[j].Paths {
				return row[i].Paths > row[j].Paths
			}
			return row[i].ASN < row[j].ASN
		})
		for _, n := range row[:min(len(row), 6)] {
			nodes = append(nodes, n)
			shown[n.ASN] = true
		}
	}
	var edges []netEdge
	for k, n := range links {
		if shown[k[0]] && shown[k[1]] && level[k[0]] < level[k[1]] {
			edges = append(edges, netEdge{From: k[0], To: k[1], Paths: n})
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})
	return nodes, edges
}

// usageLabels puts PeeringDB's words for a network in plain ones.
var usageLabels = map[string]string{
	"Cable/DSL/ISP": "Home and business internet (ISP)", "NSP": "Network provider", "Content": "Content and hosting",
	"Enterprise": "Company network", "Educational/Research": "Education and research", "Non-Profit": "Non-profit",
	"Government": "Government", "Route Server": "Route server", "Network Services": "Network services",
}

func (p *Panel) netName(ctx context.Context, client *http.Client, asn int64) string {
	p.nets.mu.Lock()
	name, ok := p.nets.names[asn]
	p.nets.mu.Unlock()
	if ok {
		return name
	}
	var d struct {
		Holder string `json:"holder"`
	}
	if ripeCall(ctx, client, "as-overview", strconv.FormatInt(asn, 10), &d) != nil {
		return ""
	}
	name = d.Holder
	if _, after, found := strings.Cut(name, " - "); found { // "GOOGLE - Google LLC"
		name = after
	}
	name = scrubAddrs(truncate(name, 60))
	p.nets.mu.Lock()
	if p.nets.names == nil {
		p.nets.names = map[int64]string{}
	}
	p.nets.names[asn] = name
	p.nets.mu.Unlock()
	return name
}

// netRoutes fills in the network and the routes. The address itself is not sent: see outerPrefix.
func (p *Panel) netRoutes(ctx context.Context, client *http.Client, a netip.Addr, out *netProfile) error {
	var info struct {
		ASNs   []string `json:"asns"`
		Prefix string   `json:"prefix"`
	}
	if err := ripeCall(ctx, client, "network-info", outerPrefix(a).String(), &info); err != nil {
		return err
	}
	prefix, err := netip.ParsePrefix(info.Prefix)
	if err != nil || len(info.ASNs) == 0 {
		return nil // announced by nobody
	}
	asn, err := strconv.ParseInt(info.ASNs[0], 10, 64)
	if err != nil {
		return nil
	}
	out.ASN = asn
	out.Prefix = fmt.Sprintf("%s/%d", maskAddr(a), prefix.Bits())
	out.Holder = p.netName(ctx, client, asn)

	var lg struct {
		RRCs []struct {
			Peers []struct {
				Path string `json:"as_path"`
			} `json:"peers"`
		} `json:"rrcs"`
	}
	if ripeCall(ctx, client, "looking-glass", prefix.String(), &lg) == nil {
		var paths [][]int64
		for _, r := range lg.RRCs {
			for _, peer := range r.Peers {
				paths = append(paths, asPath(peer.Path))
			}
		}
		out.Nodes, out.Edges = routeGraph(paths, asn)
		if len(out.Nodes) > 0 {
			out.RouteSeen = out.Nodes[0].Paths
		}
		for i := range out.Nodes {
			out.Nodes[i].Name = p.netName(ctx, client, out.Nodes[i].ASN)
		}
	}
	var rs struct {
		Space struct {
			V4 struct {
				IPs int64 `json:"ips"`
			} `json:"v4"`
		} `json:"announced_space"`
	}
	if ripeCall(ctx, client, "routing-status", strconv.FormatInt(asn, 10), &rs) == nil {
		out.ASNSpace = rs.Space.V4.IPs
	}
	var pdb struct {
		Data []struct {
			Type  string   `json:"info_type"`
			Types []string `json:"info_types"`
		} `json:"data"`
	}
	if netFetch(ctx, client, peeringBase+"/net?fields=info_type,info_types&asn="+strconv.FormatInt(asn, 10), &pdb) == nil && len(pdb.Data) > 0 {
		kinds := pdb.Data[0].Types
		if len(kinds) == 0 && pdb.Data[0].Type != "" {
			kinds = []string{pdb.Data[0].Type}
		}
		for _, k := range kinds {
			if l := usageLabels[k]; l != "" {
				out.Usage = append(out.Usage, l)
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------- running it

// refreshNets looks up every server the status page may show, one at a time. Servers on one address
// or /24 share what is read about it.
func (p *Panel) refreshNets(ctx context.Context) {
	p.nets.mu.Lock()
	if p.nets.running {
		p.nets.mu.Unlock()
		return
	}
	p.nets.running, p.nets.last = true, time.Now()
	p.nets.mu.Unlock()
	defer func() {
		p.nets.mu.Lock()
		p.nets.running = false
		p.nets.mu.Unlock()
	}()
	servers, err := p.serversOf(ctx, 0)
	if err != nil {
		return
	}
	addrs := map[int64]netip.Addr{}
	targets := map[netip.Prefix]*netHits{}
	for _, s := range servers {
		if s.DeletedAt > 0 || s.FirstSeenAt == 0 || s.StatusHidden {
			continue
		}
		a, ok := profileAddr(s)
		if !ok {
			continue
		}
		addrs[s.ID] = a
		if a.Is4() {
			targets[outerPrefix(a)] = &netHits{}
		}
	}
	readLists(ctx, targets)
	client := netClient()
	fresh := map[int64]*netProfile{}
	for id, a := range addrs {
		v := &netProfile{State: "ready", Address: maskAddr(a), Updated: now()}
		if err := p.netRoutes(ctx, client, a, v); err != nil {
			if !errors.Is(err, context.Canceled) {
				slog.Warn("network profile: no routes", "server", id) // the error names the prefix: not logged
			}
			p.nets.mu.Lock()
			old := p.nets.byID[id]
			p.nets.mu.Unlock()
			if old != nil { // an outage elsewhere does not wipe what is known
				v = old
				fresh[id] = v
				continue
			}
		}
		if h := targets[outerPrefix(a)]; a.Is4() && h != nil {
			v.Block = blockOf(a, h)
			v.Score, v.Notes = scoreOf(a, h)
			if h.marks[netHosting][int(a.As4()[3])] {
				v.Usage = append([]string{"Data centre"}, v.Usage...)
			}
			if h.marks[netVPN][int(a.As4()[3])] {
				v.Usage = append(v.Usage, "VPN")
			}
		}
		fresh[id] = v
	}
	p.nets.mu.Lock()
	p.nets.byID = fresh
	p.nets.mu.Unlock()
}

// netJob repeats the lookup daily while the status page shows it (jobs.go calls it).
func (p *Panel) netJob(ctx context.Context) {
	if !p.settings().StatusNetwork {
		return
	}
	p.nets.mu.Lock()
	due := !p.nets.last.IsZero() && time.Since(p.nets.last) >= netEvery
	p.nets.mu.Unlock()
	if due {
		go p.refreshNets(ctx)
	}
}

// apiStatusNetwork serves a server's network profile, to whoever may see the server on the status
// page: the same masked profile for everyone, the supervisor included.
func (p *Panel) apiStatusNetwork(w http.ResponseWriter, r *http.Request) {
	if _, err := p.statusViewer(w, r); err != nil {
		writeErr(w, err)
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	s, err := p.serverByID(r.Context(), id)
	if err != nil || s.DeletedAt > 0 || s.FirstSeenAt == 0 || s.StatusHidden || !p.settings().StatusNetwork {
		writeErr(w, errNotFound)
		return
	}
	p.nets.mu.Lock()
	v := p.nets.byID[id]
	start := v == nil && !p.nets.running && time.Since(p.nets.last) >= netRetry
	p.nets.mu.Unlock()
	if start {
		go p.refreshNets(context.WithoutCancel(r.Context())) // outlives this request
	}
	if v == nil {
		v = &netProfile{State: "pending"}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, v)
}
