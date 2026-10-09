package panel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"meridian/internal/geo"
	"meridian/internal/proto"
	"meridian/internal/subgen"
)

// Traffic splitting: an ordered list of rules. Each applies to every server, some servers or some
// protocols, matches where traffic goes (sites, domains, countries, addresses, ports, BitTorrent) and
// sends it directly, through a proxy pass (a protocol on another server, or an external node), to a
// load balancer, or nowhere. The first rule that matches wins; what no rule takes goes the way it
// always did (its protocol's own proxy pass, or directly).
//
// Rules run in the servers' Xray, so they apply to the Xray protocols (VLESS, VMess, Trojan,
// Shadowsocks, SOCKS5, HTTP); Hysteria2 and WireGuard traffic is not split. They are applied live -
// no restart - except that a fastest-first load balancer needs Xray to measure latency, which
// starts with one restart of Xray on that server (shown as "restart needed", done on a click).
//
// Traffic from protocols on other servers passing through a server's protocol (proxy pass) never
// follows that server's rules: a chain stays as its own server chose it.

type RouteMatch struct {
	All        bool     `json:"all,omitempty" doc:"Everything (the protocols' traffic as a whole); the other conditions must then be empty"`
	Sites      []string `json:"sites,omitempty" doc:"Site lists by name (Xray's geosite: openai, netflix, google, telegram, cn, category-ads-all ...)"`
	Domains    []string `json:"domains,omitempty" doc:"Domains: example.com (and its subdomains), full:www.example.com, keyword:example, regexp:^ad\\."`
	Countries  []string `json:"countries,omitempty" doc:"Destination countries by IP address (two-letter codes, e.g. CN)"`
	IPs        []string `json:"ips,omitempty" doc:"Destination addresses or ranges, e.g. 1.1.1.1 or 91.108.4.0/22"`
	Ports      string   `json:"ports,omitempty" doc:"Destination ports, e.g. 443 or 80,443,8000-9000"`
	Network    string   `json:"network,omitempty" doc:"tcp | udp | empty for both"`
	BitTorrent bool     `json:"bittorrent,omitempty" doc:"BitTorrent connections (recognized by their first bytes)"`
}

type Route struct {
	ID        int64      `json:"id"`
	AccountID int64      `json:"-"`
	Sort      int        `json:"sort"`
	Name      string     `json:"name"`
	Enabled   bool       `json:"enabled"`
	Servers   []int64    `json:"servers" doc:"Servers it applies to; empty = every server (unless protocols are given)"`
	Nodes     []int64    `json:"nodes" doc:"Protocols it applies to; empty = every Xray protocol on its servers"`
	Match     RouteMatch `json:"match"`
	Target    string     `json:"target" doc:"direct | block | node:<protocol id> (a proxy pass to that protocol on another server) | ext:<external node id> | lb:<load balancer id>"`
	CreatedAt int64      `json:"created_at"`
	UpdatedAt int64      `json:"updated_at"`
}

// title is what lists call the rule: its name, or what it matches.
func (r *Route) title() string {
	if r.Name != "" {
		return r.Name
	}
	m := r.Match
	var parts []string
	if m.All {
		return "Everything"
	}
	parts = append(parts, m.Sites...)
	parts = append(parts, m.Domains...)
	for _, c := range m.Countries {
		parts = append(parts, strings.ToUpper(c))
	}
	parts = append(parts, m.IPs...)
	if m.Ports != "" {
		parts = append(parts, "port "+m.Ports)
	}
	if m.Network != "" {
		parts = append(parts, strings.ToUpper(m.Network))
	}
	if m.BitTorrent {
		parts = append(parts, "BitTorrent")
	}
	if len(parts) > 3 {
		parts = append(parts[:3], fmt.Sprintf("%d more", len(parts)-3))
	}
	return strings.Join(parts, ", ")
}

type Balancer struct {
	ID        int64    `json:"id"`
	AccountID int64    `json:"-"`
	Name      string   `json:"name"`
	Strategy  string   `json:"strategy" doc:"random | roundRobin (take turns) | leastPing (the fastest: Xray checks every member every minute; needs one Xray restart per server the first time)"`
	Members   []string `json:"members" doc:"node:<protocol id> | ext:<external node id> | src:<subscription link id> (all of its nodes) | direct (the server itself)"`
	Fallback  string   `json:"fallback" doc:"Fastest first only - when no member answers Xray's latency checks: block (default) | direct"`
	CreatedAt int64    `json:"created_at"`
	UpdatedAt int64    `json:"updated_at"`
}

const routeCols = `id, account_id, sort, name, enabled, servers, nodes, match, target, created_at, updated_at`

func scanRoute(row interface{ Scan(...any) error }) (*Route, error) {
	r := &Route{}
	var servers, nodes, match string
	if err := row.Scan(&r.ID, &r.AccountID, &r.Sort, &r.Name, &r.Enabled, &servers, &nodes, &match, &r.Target, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(servers), &r.Servers)
	_ = json.Unmarshal([]byte(nodes), &r.Nodes)
	_ = json.Unmarshal([]byte(match), &r.Match)
	if r.Servers == nil {
		r.Servers = []int64{}
	}
	if r.Nodes == nil {
		r.Nodes = []int64{}
	}
	return r, nil
}

// directFallback says whether traffic leaves directly when no member can be used. Only a
// fastest-first balancer has a fallback (its latency checks tell when members are down); any other
// blocks that traffic, as an exit that cannot be used does.
func (b *Balancer) directFallback() bool { return b.Strategy == "leastPing" && b.Fallback == "direct" }

const balancerCols = `id, account_id, name, strategy, members, fallback, created_at, updated_at`

func scanBalancer(row interface{ Scan(...any) error }) (*Balancer, error) {
	b := &Balancer{}
	var members string
	if err := row.Scan(&b.ID, &b.AccountID, &b.Name, &b.Strategy, &members, &b.Fallback, &b.CreatedAt, &b.UpdatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(members), &b.Members)
	if b.Members == nil {
		b.Members = []string{}
	}
	return b, nil
}

// routesOf lists an account's rules in order (0 = every account).
func (p *Panel) routesOf(ctx context.Context, acct int64) ([]*Route, error) {
	q, args := `SELECT `+routeCols+` FROM routes`, []any{}
	if acct > 0 {
		q += ` WHERE account_id = ?`
		args = append(args, acct)
	}
	rows, err := p.db.QueryContext(ctx, q+` ORDER BY sort, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Route
	for rows.Next() {
		r, err := scanRoute(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *Panel) balancersOf(ctx context.Context, acct int64) ([]*Balancer, error) {
	q, args := `SELECT `+balancerCols+` FROM balancers`, []any{}
	if acct > 0 {
		q += ` WHERE account_id = ?`
		args = append(args, acct)
	}
	rows, err := p.db.QueryContext(ctx, q+` ORDER BY name COLLATE NOCASE, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Balancer
	for rows.Next() {
		b, err := scanBalancer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- validation

var (
	siteRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9!@._-]{0,63}$`)
	domainRE  = regexp.MustCompile(`^[a-z0-9_*]([a-z0-9_*.-]{0,251}[a-z0-9_])?$`)
	keywordRE = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)
)

// maxRouteEntries bounds the sites, domains, countries and addresses of one rule, maxAllEntries
// those of all of an account's rules together: every server gets them in its configuration.
const (
	maxRouteEntries = 2000
	maxAllEntries   = 10000
)

func (m RouteMatch) entries() int {
	return len(m.Sites) + len(m.Domains) + len(m.Countries) + len(m.IPs)
}

func badRule(msg string) error { return errStatus(http.StatusBadRequest, msg) }

// cleanMatch checks what a rule matches and writes it the way Xray reads it.
func cleanMatch(m RouteMatch) (RouteMatch, error) {
	var out RouteMatch
	if m.entries() > maxRouteEntries {
		return out, badRule(fmt.Sprintf("a rule holds at most %d sites, domains, countries and addresses", maxRouteEntries))
	}
	out.All = m.All
	for _, s := range m.Sites {
		s = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "geosite:"))
		if s == "" {
			continue
		}
		if !siteRE.MatchString(s) {
			return out, badRule(fmt.Sprintf("%q is not a site list name (letters, digits and - only, e.g. netflix)", truncate(s, 64)))
		}
		if !slices.Contains(out.Sites, s) {
			out.Sites = append(out.Sites, s)
		}
	}
	for _, d := range m.Domains {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		v, err := cleanDomain(d)
		if err != nil {
			return out, err
		}
		if strings.HasPrefix(v, "geosite:") {
			if s := strings.TrimPrefix(v, "geosite:"); !slices.Contains(out.Sites, s) {
				out.Sites = append(out.Sites, s)
			}
			continue
		}
		if !slices.Contains(out.Domains, v) {
			out.Domains = append(out.Domains, v)
		}
	}
	for _, c := range m.Countries {
		c = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(c), "geoip:"))
		if c == "" {
			continue
		}
		if c == "private" {
			return out, badRule("private addresses are always blocked - the servers' own networks are never reachable through them")
		}
		// a code Xray's country list does not have would make every server refuse the configuration
		if !geo.Codes[strings.ToUpper(c)] {
			return out, badRule(fmt.Sprintf("%q is not a two-letter country code", truncate(c, 12)))
		}
		if !slices.Contains(out.Countries, c) {
			out.Countries = append(out.Countries, c)
		}
	}
	for _, ip := range m.IPs {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		v, err := cleanCIDR(ip)
		if err != nil {
			return out, err
		}
		if !slices.Contains(out.IPs, v) {
			out.IPs = append(out.IPs, v)
		}
	}
	ports, err := cleanPorts(m.Ports)
	if err != nil {
		return out, err
	}
	out.Ports = ports
	switch n := strings.ToLower(strings.TrimSpace(m.Network)); n {
	case "", "tcp,udp", "both":
	case "tcp", "udp":
		out.Network = n
	default:
		return out, badRule("network must be tcp, udp or empty for both")
	}
	out.BitTorrent = m.BitTorrent
	if out.entries() > maxRouteEntries {
		return out, badRule(fmt.Sprintf("a rule holds at most %d sites, domains, countries and addresses", maxRouteEntries))
	}
	some := out.entries() > 0 || out.Ports != "" || out.Network != "" || out.BitTorrent
	switch {
	case out.All && some:
		return out, badRule(`"everything" takes no other conditions`)
	case !out.All && !some:
		return out, badRule(`choose what the rule matches - sites, domains, countries, addresses, ports - or "everything"`)
	}
	return out, nil
}

// cleanDomain reads one domain entry: a plain domain matches it and its subdomains.
func cleanDomain(d string) (string, error) {
	kind, v, found := strings.Cut(d, ":")
	if !found {
		kind, v = "domain", d
	}
	kind = strings.ToLower(kind)
	switch kind {
	case "domain", "full":
		v = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(v, "*."), "."))
		if !domainRE.MatchString(v) || !strings.Contains(v, ".") && kind == "full" {
			return "", badRule(fmt.Sprintf("%q is not a domain", truncate(d, 80)))
		}
	case "keyword":
		v = strings.ToLower(v)
		if !keywordRE.MatchString(v) {
			return "", badRule(fmt.Sprintf("%q: a keyword is letters, digits, dots and dashes", truncate(d, 80)))
		}
	case "regexp":
		if len(v) == 0 || len(v) > 512 {
			return "", badRule("a regular expression must be 1 to 512 characters")
		}
		if _, err := regexp.Compile(v); err != nil {
			return "", badRule(fmt.Sprintf("%q is not a regular expression: %v", truncate(v, 80), err))
		}
	case "geosite":
		v = strings.ToLower(v)
		if !siteRE.MatchString(v) {
			return "", badRule(fmt.Sprintf("%q is not a site list name", truncate(v, 64)))
		}
	default:
		return "", badRule(fmt.Sprintf("%q: write a domain, or full:, keyword:, regexp: before it", truncate(d, 80)))
	}
	return kind + ":" + v, nil
}

func cleanCIDR(s string) (string, error) {
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap().String(), nil
	}
	pf, err := netip.ParsePrefix(s)
	if err != nil {
		return "", badRule(fmt.Sprintf("%q is not an IP address or range", truncate(s, 64)))
	}
	return pf.Masked().String(), nil
}

// cleanPorts reads "443" or "80,443,8000-9000".
func cleanPorts(s string) (string, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	if s == "" {
		return "", nil
	}
	parts := strings.Split(s, ",")
	if len(parts) > 50 {
		return "", badRule("at most 50 ports or ranges")
	}
	for _, p := range parts {
		a, b, isRange := strings.Cut(p, "-")
		x, err1 := strconv.Atoi(a)
		y := x
		var err2 error
		if isRange {
			y, err2 = strconv.Atoi(b)
		}
		if err1 != nil || err2 != nil || x < 1 || y > 65535 || x > y {
			return "", badRule(fmt.Sprintf("%q is not a port or a range like 8000-9000", truncate(p, 24)))
		}
	}
	return s, nil
}

// checkTarget validates where a rule sends traffic.
func (p *Panel) checkTarget(ctx context.Context, acct int64, target string, balancerOK bool) (string, error) {
	target = strings.TrimSpace(target)
	switch target {
	case "direct", "block":
		return target, nil
	}
	kind, idS, _ := strings.Cut(target, ":")
	id, err := strconv.ParseInt(idS, 10, 64)
	if err != nil || id <= 0 {
		return "", badRule("send it direct, to block, or to node:<id>, ext:<id> or lb:<id>")
	}
	switch kind {
	case "node":
		n, err := p.nodeByID(ctx, id)
		if err != nil {
			return "", badRule("that protocol does not exist")
		}
		s, err := p.serverByID(ctx, n.ServerID)
		if err != nil || s.DeletedAt > 0 || s.AccountID != acct {
			return "", badRule("that protocol does not exist")
		}
		if !canExit(n.Kind) {
			return "", badRule("WireGuard cannot be a proxy pass exit - pick any other protocol")
		}
	case "ext":
		x, err := p.extByID(ctx, id)
		if err != nil || x.AccountID != acct {
			return "", badRule("that external node does not exist")
		}
	case "src":
		if balancerOK {
			return "", badRule("a rule cannot send traffic to a subscription link - make a load balancer with it as a member, and send the rule there")
		}
		if s, err := p.sourceByID(ctx, id); err != nil || s.AccountID != acct {
			return "", badRule("that subscription link does not exist")
		}
	case "lb":
		if !balancerOK {
			return "", badRule("a load balancer cannot be a member of another one")
		}
		var a int64
		if p.db.QueryRowContext(ctx, `SELECT account_id FROM balancers WHERE id = ?`, id).Scan(&a) != nil || a != acct {
			return "", badRule("that load balancer does not exist")
		}
	default:
		return "", badRule("send it direct, to block, or to node:<id>, ext:<id> or lb:<id>")
	}
	return fmt.Sprintf("%s:%d", kind, id), nil
}

// ---------------------------------------------------------------- API

type routingView struct {
	Rules     []*Route        `json:"rules"`
	Balancers []*balancerView `json:"balancers"`
	Exits     []routeExitView `json:"exits" doc:"Where rules and load balancers can send traffic: protocols on the servers and external nodes"`
	Sources   []routeExitView `json:"sources" doc:"Subscription links: a load balancer member each, standing for all of the link's nodes"`
	Sites     []string        `json:"sites" doc:"Common site list names"`
	Problems  []string        `json:"problems" doc:"What does not work as written, on which servers, and what happens there instead (the traffic concerned is blocked - it never leaves directly instead)"`
}

type balancerView struct {
	*Balancer
	UsedBy []string `json:"used_by" doc:"Rules that send traffic to it"`
}

type routeExitView struct {
	Target  string `json:"target" doc:"node:<id>, ext:<id> or src:<id>"`
	Name    string `json:"name" doc:"'server · protocol' (with where it passes on to, after →) or 'External · name'"`
	Server  int64  `json:"server_id,omitempty"`
	Enabled bool   `json:"enabled"`
}

// commonSites are site lists people use most, offered first.
var commonSites = []string{"openai", "anthropic", "google", "youtube", "netflix", "disney", "spotify", "telegram", "twitter",
	"facebook", "instagram", "tiktok", "github", "apple", "microsoft", "steam", "cn", "geolocation-!cn", "category-ads-all",
	"private"}

func (p *Panel) apiRouting(w http.ResponseWriter, r *http.Request, a *Account) error {
	ctx := r.Context()
	acct := scopeAccount(r, a)
	if acct == 0 {
		acct = a.ID
	}
	rules, err := p.routesOf(ctx, acct)
	if err != nil {
		return err
	}
	bals, err := p.balancersOf(ctx, acct)
	if err != nil {
		return err
	}
	out := routingView{Rules: rules, Balancers: []*balancerView{}, Exits: []routeExitView{}, Sites: commonSites[:len(commonSites)-1],
		Problems: []string{}}
	if out.Rules == nil {
		out.Rules = []*Route{}
	}
	for _, b := range bals {
		bv := &balancerView{Balancer: b, UsedBy: []string{}}
		for _, rt := range rules {
			if rt.Target == fmt.Sprintf("lb:%d", b.ID) {
				bv.UsedBy = append(bv.UsedBy, rt.title())
			}
		}
		out.Balancers = append(out.Balancers, bv)
	}
	servers, err := p.serversOf(ctx, acct)
	if err != nil {
		return err
	}
	nodesBy := map[int64][]*Node{}
	var reach []string // links rules cannot make for IPv4 or IPv6 reasons (ipv6.go)
	for _, s := range servers {
		nodes, err := p.nodesOf(ctx, s.ID)
		if err != nil {
			return err
		}
		nodesBy[s.ID] = nodes
		reach = append(reach, p.ruleReachIssues(ctx, s, nodes)...)
		for _, n := range nodes {
			if canExit(n.Kind) {
				name := s.Name + " · " + nz(n.Name, protocolLabel(n.Kind, n.Settings))
				switch { // where it passes on to, as the protocol forms show a relay
				case n.PassExt > 0:
					name += " → " + p.extTitle(ctx, n.PassExt)
				case n.PassNode > 0:
					name += " → " + p.passName(ctx, n.PassNode)
				}
				out.Exits = append(out.Exits, routeExitView{Target: fmt.Sprintf("node:%d", n.ID), Name: name, Server: s.ID, Enabled: n.Enabled})
			}
		}
	}
	exts, err := p.extNodesOf(ctx, acct)
	if err != nil {
		return err
	}
	for _, x := range exts {
		out.Exits = append(out.Exits, routeExitView{Target: fmt.Sprintf("ext:%d", x.ID), Name: "External · " + x.Name, Enabled: x.Enabled})
	}
	out.Sources = []routeExitView{}
	if srcs, err := p.sourcesOf(ctx, acct); err == nil {
		for _, s := range srcs {
			n := 0
			for _, x := range exts {
				if x.SourceID == s.ID && x.Enabled && x.MissingSince == 0 {
					n++
				}
			}
			out.Sources = append(out.Sources, routeExitView{Target: fmt.Sprintf("src:%d", s.ID),
				Name: fmt.Sprintf("Subscription · %s (%s)", s.Name, countOf(n, "node", "nodes")), Enabled: s.Enabled})
		}
	}
	p.hub.settle(ctx, 3*time.Second) // the servers' compiles after a change just made find the problems
	out.Problems = p.routeProblems(rules, servers, nodesBy)
	out.Problems = append(out.Problems, reach...)
	writeJSON(w, http.StatusOK, out)
	return nil
}

// routeProblems lists what does not work as written: what the servers' last compiles found (grouped
// across servers), rules that reach no Xray protocol, and site lists or countries Xray refused.
func (p *Panel) routeProblems(rules []*Route, servers []*Server, nodesBy map[int64][]*Node) []string {
	out := []string{}
	var order []string
	where := map[string][]string{}
	add := func(text, server string) {
		if _, seen := where[text]; !seen {
			order = append(order, text)
		}
		if !slices.Contains(where[text], server) {
			where[text] = append(where[text], server)
		}
	}
	for _, s := range servers {
		for _, n := range p.routeNotesOf(s.ID) {
			add(n, s.Name)
		}
		if why := xrayRefusal(s.ApplyErrors); why != "" {
			add(fmt.Sprintf("Xray refuses a site list or country name (%s) and keeps the previous configuration until it is corrected - site lists are the names in Xray's geosite list.", why), s.Name)
		}
	}
	for _, text := range order {
		out = append(out, "On "+nameList(where[text])+": "+text)
	}
	alive := map[int64]bool{}
	for _, s := range servers {
		alive[s.ID] = true
	}
	for _, rt := range rules {
		if !rt.Enabled {
			continue
		}
		gone := len(rt.Servers) > 0 && !slices.ContainsFunc(rt.Servers, func(id int64) bool { return alive[id] })
		if len(rt.Nodes) > 0 {
			gone = !slices.ContainsFunc(rt.Nodes, func(id int64) bool {
				return slices.ContainsFunc(servers, func(s *Server) bool {
					return slices.ContainsFunc(nodesBy[s.ID], func(n *Node) bool { return n.ID == id })
				})
			})
		}
		reaches := slices.ContainsFunc(servers, func(s *Server) bool { return rt.reaches(s, nodesBy[s.ID]) })
		switch {
		case gone:
			out = append(out, fmt.Sprintf("Rule %q applies only to servers or protocols that were removed, so it does nothing - choose where it applies, or remove it.", rt.title()))
		case !reaches && len(servers) > 0:
			out = append(out, fmt.Sprintf("Rule %q reaches no Xray protocol that is turned on, so it does nothing - rules split the traffic of VLESS, VMess, Trojan, Shadowsocks, SOCKS5 and HTTP, not Hysteria2 or WireGuard.", rt.title()))
		}
	}
	return out
}

// xrayRefusal is why Xray refused a server's configuration, when that was a site list or country
// name its lists do not have ("" otherwise): the last part of its message.
func xrayRefusal(applyErrors string) string {
	for _, line := range strings.Split(applyErrors, "\n") {
		if !strings.Contains(line, "geosite") && !strings.Contains(line, "geoip") {
			continue
		}
		if i := strings.LastIndex(line, " > "); i >= 0 {
			line = line[i+3:]
		}
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "infra/conf:"))
		return truncate(line, 120)
	}
	return ""
}

// nameList is "A", "A and B", "A, B and C", or the first three and how many more.
func nameList(names []string) string {
	switch n := len(names); {
	case n == 0:
		return ""
	case n == 1:
		return names[0]
	case n <= 4:
		return strings.Join(names[:n-1], ", ") + " and " + names[n-1]
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:3], ", "), len(names)-3)
}

type routeInput struct {
	Name    *string     `json:"name"`
	Enabled *bool       `json:"enabled"`
	Servers *[]int64    `json:"servers" doc:"Servers it applies to; empty (and no protocols) = every server. Servers or protocols, not both: giving one list alone empties the other"`
	Nodes   *[]int64    `json:"nodes" doc:"Protocols it applies to (Xray protocols only); empty = every Xray protocol on its servers"`
	Match   *RouteMatch `json:"match"`
	Target  *string     `json:"target" doc:"direct | block | node:<protocol id> | ext:<external node id> | lb:<load balancer id>"`
	Before  *int64      `json:"before" doc:"On create: put it before this rule (default: last)"`
}

// cleanScope checks the servers and protocols a rule applies to. Ids the rule had (cur) that were
// removed since are dropped - unless nothing else is left: the rule then keeps them and applies
// nowhere, never everywhere, which empty lists would mean. Only a new id that does not exist is an
// error.
func (p *Panel) cleanScope(ctx context.Context, acct int64, cur *Route, servers, nodes []int64) ([]int64, []int64, error) {
	if len(servers) > 500 || len(nodes) > 500 {
		return nil, nil, badRule("a rule applies to at most 500 servers or protocols")
	}
	outS, outN, goneS, goneN := []int64{}, []int64{}, []int64{}, []int64{}
	for _, id := range servers {
		s, err := p.serverByID(ctx, id)
		if err != nil || s.DeletedAt > 0 || s.AccountID != acct {
			if slices.Contains(cur.Servers, id) {
				goneS = append(goneS, id)
				continue
			}
			return nil, nil, badRule(fmt.Sprintf("server %d does not exist", id))
		}
		if !slices.Contains(outS, id) {
			outS = append(outS, id)
		}
	}
	for _, id := range nodes {
		n, err := p.nodeByID(ctx, id)
		var s *Server
		if err == nil {
			s, err = p.serverByID(ctx, n.ServerID)
		}
		if err != nil || s.DeletedAt > 0 || s.AccountID != acct {
			if slices.Contains(cur.Nodes, id) {
				goneN = append(goneN, id)
				continue
			}
			return nil, nil, badRule(fmt.Sprintf("protocol %d does not exist", id))
		}
		if k, _ := kindOf(n.Kind); k.Engine != "xray" {
			return nil, nil, badRule(k.Label + " traffic cannot be split: rules apply to the Xray protocols (VLESS, VMess, Trojan, Shadowsocks, SOCKS5, HTTP)")
		}
		if !slices.Contains(outN, id) {
			outN = append(outN, id)
		}
	}
	if len(outS) == 0 && len(goneS) > 0 {
		outS = goneS
	}
	if len(outN) == 0 && len(goneN) > 0 {
		outN = goneN
	}
	switch {
	case len(outS) > 0 && len(outN) > 0:
		return nil, nil, badRule("a rule applies to some servers or to some protocols, not both - leave one of the two empty")
	case len(outS) > 500 || len(outN) > 500:
		return nil, nil, badRule("a rule applies to at most 500 servers or protocols")
	}
	return outS, outN, nil
}

func (p *Panel) ownRoute(ctx context.Context, a *Account, id int64) (*Route, error) {
	rt, err := scanRoute(p.db.QueryRowContext(ctx, `SELECT `+routeCols+` FROM routes WHERE id = ?`, id))
	if err != nil || (!a.IsOwner() && rt.AccountID != a.ID) {
		return nil, errNotFound
	}
	return rt, nil
}

func (p *Panel) saveRoute(rt *Route) error {
	sv, _ := json.Marshal(rt.Servers)
	nv, _ := json.Marshal(rt.Nodes)
	mv, _ := json.Marshal(rt.Match)
	_, err := p.db.Exec1(`UPDATE routes SET sort = ?, name = ?, enabled = ?, servers = ?, nodes = ?, match = ?, target = ?, updated_at = ?
		WHERE id = ?`, rt.Sort, rt.Name, rt.Enabled, string(sv), string(nv), string(mv), rt.Target, rt.UpdatedAt, rt.ID)
	return err
}

const maxRoutes = 500

func (p *Panel) apiCreateRoute(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in routeInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	rt := &Route{AccountID: a.ID, Enabled: true}
	if err := p.applyRouteInput(ctx, rt, &in, true); err != nil {
		return err
	}
	rules, err := p.routesOf(ctx, a.ID)
	if err != nil {
		return err
	}
	if len(rules) >= maxRoutes {
		return badRule(fmt.Sprintf("at most %d rules", maxRoutes))
	}
	t := now()
	res, err := p.db.Exec1(`INSERT INTO routes (account_id, sort, name, enabled, target, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.ID, len(rules), rt.Name, rt.Enabled, rt.Target, t, t)
	if err != nil {
		return err
	}
	rt.ID, _ = res.LastInsertId()
	rt.CreatedAt, rt.UpdatedAt = t, t
	if err := p.saveRoute(rt); err != nil {
		return err
	}
	// its place: before the given rule, else last
	order := []int64{}
	placed := false
	for _, x := range rules {
		if in.Before != nil && x.ID == *in.Before && !placed {
			order = append(order, rt.ID)
			placed = true
		}
		order = append(order, x.ID)
	}
	if !placed {
		order = append(order, rt.ID)
	}
	if err := p.reorderRoutes(a.ID, order); err != nil {
		return err
	}
	p.event(a.ID, "info", "route_added", 0, 0, a.ID, fmt.Sprintf("%s added the traffic rule %q", a.Username, rt.title()), nil)
	p.touchAccount(a.ID)
	return p.apiRouting(w, r, a)
}

// applyRouteInput takes the given fields into rt, checked.
func (p *Panel) applyRouteInput(ctx context.Context, rt *Route, in *routeInput, create bool) error {
	if in.Name != nil {
		rt.Name = cleanName(*in.Name, 60)
	}
	if in.Enabled != nil {
		rt.Enabled = *in.Enabled
	}
	servers, nodes := rt.Servers, rt.Nodes
	if in.Servers != nil {
		servers = *in.Servers
	}
	if in.Nodes != nil {
		nodes = *in.Nodes
	}
	// one list given alone replaces the other kind of scope
	if in.Nodes != nil && len(*in.Nodes) > 0 && in.Servers == nil {
		servers = nil
	}
	if in.Servers != nil && len(*in.Servers) > 0 && in.Nodes == nil {
		nodes = nil
	}
	var err error
	if rt.Servers, rt.Nodes, err = p.cleanScope(ctx, rt.AccountID, rt, servers, nodes); err != nil {
		return err
	}
	if in.Match != nil {
		if rt.Match, err = cleanMatch(*in.Match); err != nil {
			return err
		}
		total := rt.Match.entries()
		if others, err := p.routesOf(ctx, rt.AccountID); err == nil {
			for _, o := range others {
				if o.ID != rt.ID {
					total += o.Match.entries()
				}
			}
		}
		if total > maxAllEntries {
			return badRule(fmt.Sprintf("all rules together hold at most %d sites, domains, countries and addresses - every server gets them all; use site lists for long ones", maxAllEntries))
		}
	} else if create {
		return badRule(`choose what the rule matches - sites, domains, countries, addresses, ports - or "everything"`)
	}
	// an exit that was removed or turned off since stays as it is (its traffic is blocked) while the
	// rule's other fields change; only a new exit is checked
	if in.Target != nil && (create || strings.TrimSpace(*in.Target) != rt.Target) {
		if rt.Target, err = p.checkTarget(ctx, rt.AccountID, *in.Target, true); err != nil {
			return err
		}
	} else if create {
		return badRule("choose where the rule sends traffic")
	}
	if in.Target != nil || in.Servers != nil || in.Nodes != nil || in.Enabled != nil {
		return p.checkRuleReach(ctx, rt) // every server it applies to must reach its exit
	}
	return nil
}

func (p *Panel) apiUpdateRoute(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	rt, err := p.ownRoute(r.Context(), a, id)
	if err != nil {
		return err
	}
	var in routeInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := p.applyRouteInput(r.Context(), rt, &in, false); err != nil {
		return err
	}
	rt.UpdatedAt = now()
	if err := p.saveRoute(rt); err != nil {
		return err
	}
	p.event(rt.AccountID, "info", "route_changed", 0, 0, a.ID, fmt.Sprintf("%s changed the traffic rule %q", a.Username, rt.title()), nil)
	p.touchAccount(rt.AccountID)
	return p.apiRouting(w, r, a)
}

func (p *Panel) apiDeleteRoute(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	rt, err := p.ownRoute(r.Context(), a, id)
	if err != nil {
		return err
	}
	if _, err := p.db.Exec1(`DELETE FROM routes WHERE id = ?`, rt.ID); err != nil {
		return err
	}
	p.event(rt.AccountID, "info", "route_removed", 0, 0, a.ID, fmt.Sprintf("%s removed the traffic rule %q", a.Username, rt.title()), nil)
	p.touchAccount(rt.AccountID)
	return p.apiRouting(w, r, a)
}

type routeOrderInput struct {
	IDs []int64 `json:"ids" doc:"Every rule's id, in the new order (the first that matches wins)"`
}

func (p *Panel) apiOrderRoutes(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in routeOrderInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	rules, err := p.routesOf(r.Context(), a.ID)
	if err != nil {
		return err
	}
	if len(in.IDs) != len(rules) {
		return badRule("list every rule once")
	}
	for _, x := range rules {
		if !slices.Contains(in.IDs, x.ID) {
			return badRule("list every rule once")
		}
	}
	if err := p.reorderRoutes(a.ID, in.IDs); err != nil {
		return err
	}
	p.touchAccount(a.ID)
	return p.apiRouting(w, r, a)
}

func (p *Panel) reorderRoutes(acct int64, ids []int64) error {
	for i, id := range ids {
		if _, err := p.db.Exec1(`UPDATE routes SET sort = ? WHERE id = ? AND account_id = ?`, i, id, acct); err != nil {
			return err
		}
	}
	return nil
}

type balancerInput struct {
	Name     *string   `json:"name"`
	Strategy *string   `json:"strategy" doc:"random | roundRobin | leastPing"`
	Members  *[]string `json:"members" doc:"node:<protocol id> | ext:<external node id> | src:<subscription link id> (all of its nodes, as they come and go) | direct"`
	Fallback *string   `json:"fallback" doc:"Fastest first only - when no member answers: block | direct"`
}

func (p *Panel) applyBalancerInput(ctx context.Context, b *Balancer, in *balancerInput, create bool) error {
	if in.Name != nil {
		b.Name = cleanName(*in.Name, 60)
	}
	if b.Name == "" {
		return badRule("give the load balancer a name")
	}
	if in.Strategy != nil {
		b.Strategy = *in.Strategy
	}
	switch b.Strategy {
	case "random", "roundRobin", "leastPing":
	default:
		return badRule("strategy must be random, roundRobin (take turns) or leastPing (the fastest)")
	}
	if in.Fallback != nil {
		b.Fallback = *in.Fallback
	}
	if b.Fallback != "block" && b.Fallback != "direct" {
		return badRule("fallback is block or direct: where a fastest-first balancer sends traffic when no member answers")
	}
	if in.Members != nil {
		if len(*in.Members) > 100 {
			return badRule("at most 100 members")
		}
		var ms []string
		for _, m := range *in.Members {
			t := strings.TrimSpace(m)
			if !slices.Contains(b.Members, t) { // a member it has keeps its place even while it cannot be used
				var err error
				if t, err = p.checkTarget(ctx, b.AccountID, m, false); err != nil {
					return err
				}
			}
			if t == "block" {
				return badRule("block cannot be a member - members are exits")
			}
			if !slices.Contains(ms, t) {
				ms = append(ms, t)
			}
		}
		b.Members = ms
	} else if create {
		return badRule("choose the members: protocols on your servers, external nodes, or direct")
	}
	if len(b.Members) == 0 {
		return badRule("a load balancer needs at least one member")
	}
	if len(b.Members) > 100 {
		return badRule("at most 100 members")
	}
	return nil
}

func (p *Panel) ownBalancer(ctx context.Context, a *Account, id int64) (*Balancer, error) {
	b, err := scanBalancer(p.db.QueryRowContext(ctx, `SELECT `+balancerCols+` FROM balancers WHERE id = ?`, id))
	if err != nil || (!a.IsOwner() && b.AccountID != a.ID) {
		return nil, errNotFound
	}
	return b, nil
}

func (p *Panel) apiCreateBalancer(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in balancerInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	b := &Balancer{AccountID: a.ID, Strategy: "random", Fallback: "block"}
	if err := p.applyBalancerInput(r.Context(), b, &in, true); err != nil {
		return err
	}
	var n int
	_ = p.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM balancers WHERE account_id = ?`, a.ID).Scan(&n)
	if n >= 100 {
		return badRule("at most 100 load balancers")
	}
	mv, _ := json.Marshal(b.Members)
	t := now()
	if _, err := p.db.Exec1(`INSERT INTO balancers (account_id, name, strategy, members, fallback, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, a.ID, b.Name, b.Strategy, string(mv), b.Fallback, t, t); err != nil {
		return err
	}
	p.event(a.ID, "info", "balancer_added", 0, 0, a.ID, fmt.Sprintf("%s added the load balancer %s", a.Username, b.Name), nil)
	return p.apiRouting(w, r, a)
}

func (p *Panel) apiUpdateBalancer(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	b, err := p.ownBalancer(r.Context(), a, id)
	if err != nil {
		return err
	}
	var in balancerInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := p.applyBalancerInput(r.Context(), b, &in, false); err != nil {
		return err
	}
	mv, _ := json.Marshal(b.Members)
	if _, err := p.db.Exec1(`UPDATE balancers SET name = ?, strategy = ?, members = ?, fallback = ?, updated_at = ? WHERE id = ?`,
		b.Name, b.Strategy, string(mv), b.Fallback, now(), b.ID); err != nil {
		return err
	}
	p.event(b.AccountID, "info", "balancer_changed", 0, 0, a.ID, fmt.Sprintf("%s changed the load balancer %s", a.Username, b.Name), nil)
	p.touchAccount(b.AccountID)
	return p.apiRouting(w, r, a)
}

func (p *Panel) apiDeleteBalancer(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	b, err := p.ownBalancer(r.Context(), a, id)
	if err != nil {
		return err
	}
	rules, err := p.routesOf(r.Context(), b.AccountID)
	if err != nil {
		return err
	}
	for _, rt := range rules {
		if rt.Target == fmt.Sprintf("lb:%d", b.ID) {
			return errStatus(http.StatusConflict, fmt.Sprintf("the rule %q sends traffic to it - send that rule elsewhere first", rt.title()))
		}
	}
	if _, err := p.db.Exec1(`DELETE FROM balancers WHERE id = ?`, b.ID); err != nil {
		return err
	}
	p.event(b.AccountID, "info", "balancer_removed", 0, 0, a.ID, fmt.Sprintf("%s removed the load balancer %s", a.Username, b.Name), nil)
	p.touchAccount(b.AccountID)
	return p.apiRouting(w, r, a)
}

// ---------------------------------------------------------------- compiling

// routeCredentials are what server srv uses at exit protocol exit when its rules send traffic there
// (a protocol's own proxy pass has its own, see passCredentials).
func routeCredentials(srv *Server, exit *Node) (uuid, password string) {
	m := hmac.New(sha256.New, []byte(nz(srv.PassSecret, srv.Secret)))
	fmt.Fprintf(m, "route-pass:%d", exit.ID)
	sum := m.Sum(nil)
	b := append([]byte(nil), sum[:16]...)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	uuid = fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	return uuid, base64.RawURLEncoding.EncodeToString(sum)
}

// routeEmail is server srv's identity at an exit its rules send traffic to.
func routeEmail(srv int64) string { return fmt.Sprintf("r%d", srv) }

// applies says whether rule rt applies on server s, and to which of its Xray protocols' inbound tags.
func (rt *Route) applies(s *Server, xrayNodes []*Node) []string {
	var tags []string
	switch {
	case len(rt.Nodes) > 0:
		for _, n := range xrayNodes {
			if slices.Contains(rt.Nodes, n.ID) {
				tags = append(tags, proto.InboundTag(n.ID))
			}
		}
	case len(rt.Servers) == 0 || slices.Contains(rt.Servers, s.ID):
		for _, n := range xrayNodes {
			tags = append(tags, proto.InboundTag(n.ID))
		}
	}
	return tags
}

// routePlan is what traffic splitting adds to one server's Xray.
type routePlan struct {
	outbounds []map[string]any
	rules     []map[string]any
	balancers []map[string]any
	observe   bool              // a fastest-first balancer: Xray measures its members' latency
	notes     []string          // what cannot be used on this server as it stands, and what happens instead
	tags      map[string]bool   // outbounds already added
	exits     map[string]string // target -> outbound tag on this server ("" = cannot be used: blocked)
	why       map[string]string // target -> why it cannot be used here
	// how each enabled Xray protocol of the server sends its traffic when no rule takes it (an
	// outbound tag), and the outbounds behind those ways (its own address, its proxy pass)
	out map[int64]string
	own []map[string]any
}

// directTag is where "directly" leaves a server: by its IP version when it has only one.
func directTag(srv *Server) string {
	if domainStrategy(srv, "") != "" {
		return "direct-ipver"
	}
	return "direct"
}

// directOutbound leaves the server directly, under tag.
func directOutbound(srv *Server, tag string) map[string]any {
	ob := map[string]any{"tag": tag, "protocol": "freedom"}
	if ds := domainStrategy(srv, ""); ds != "" {
		ob["settings"] = map[string]any{"domainStrategy": ds}
	}
	return ob
}

// exitOutbound renders an exit for this server's rules under tag: the outbound, or why it cannot
// be used here.
func (p *Panel) exitOutbound(ctx context.Context, srv *Server, plan *routePlan, target, tag string) (map[string]any, string) {
	kind, idS, _ := strings.Cut(target, ":")
	id, _ := strconv.ParseInt(idS, 10, 64)
	switch kind {
	case "ext":
		x, why := p.extExit(ctx, srv.AccountID, id)
		if why != "" {
			return nil, why
		}
		ob, err := subgen.XrayOutbound(x.Endpoint, tag)
		if err != nil {
			return nil, err.Error()
		}
		return ob, ""
	case "node":
		if n, err := p.nodeByID(ctx, id); err == nil && n.ServerID == srv.ID {
			return p.ownExit(ctx, srv, plan, n, tag)
		}
		// the same checks as a protocol's proxy pass: the exit must be on another server, the chain at
		// most two passes, never back to this server
		fake := &Node{ID: -1, PassNode: id}
		exit, xs, why := p.passExit(ctx, srv, fake)
		if why != "" {
			return nil, strings.Replace(why, "its exit", "the exit", 1)
		}
		uuid, password := routeCredentials(srv, exit)
		pc := passClient{Email: routeEmail(srv.ID), UUID: uuid, Password: password}
		var s *xraySettings
		if exit.Kind != subgen.KindHysteria2 {
			var err error
			if s, err = parseXray(exit.Settings); err != nil {
				return nil, err.Error()
			}
		}
		e, err := clientEndpoint(exit, xs, pc.credsAt(exit, s), nil, "")
		if err != nil {
			return nil, err.Error()
		}
		e.Host = reachHost(srv, exit, xs, e.Host) // the exit's address of a kind this server has
		ob, err := subgen.XrayOutbound(e, tag)
		if err != nil {
			return nil, err.Error()
		}
		return ob, ""
	}
	return nil, "unknown exit"
}

// ownExit is a protocol of the server itself as an exit: there the traffic leaves the way that
// protocol's own traffic does - directly, from its own address, or through its proxy pass - since
// that is where it would come out anyway.
func (p *Panel) ownExit(ctx context.Context, srv *Server, plan *routePlan, n *Node, tag string) (map[string]any, string) {
	name := srv.Name + " · " + protocolLabel(n.Kind, n.Settings)
	if !n.Enabled {
		return nil, fmt.Sprintf("the exit (%s) is turned off", name)
	}
	if k, _ := kindOf(n.Kind); k.Engine != "xray" { // Hysteria2: directly, from its own address if it has one
		if n.BindIP != "" {
			ob := bindOutbound(srv, n)
			ob["tag"] = tag
			return ob, ""
		}
		return directOutbound(srv, tag), ""
	}
	switch way := plan.out[n.ID]; way {
	case "direct", "direct-ipver":
		return directOutbound(srv, tag), ""
	case "", "block": // its own proxy pass cannot be used now
		why := "its proxy pass cannot be used now"
		if n.PassExt > 0 {
			_, why = p.extExit(ctx, srv.AccountID, n.PassExt)
		} else if n.PassNode > 0 {
			_, _, why = p.passExit(ctx, srv, n)
		}
		return nil, fmt.Sprintf("the exit (%s) passes on, but %s", name, why)
	default: // the same outbound as its own address or proxy pass, under this tag
		for _, ob := range plan.own {
			if ob["tag"] == way {
				cp := maps.Clone(ob)
				cp["tag"] = tag
				return cp, ""
			}
		}
		return nil, fmt.Sprintf("the exit (%s) has no way out yet", name)
	}
}

// exitTag returns the outbound tag for target on this server, adding the outbound once ("" = it
// cannot be used here; the plan notes why).
func (p *Panel) exitTag(ctx context.Context, srv *Server, plan *routePlan, target, title string) string {
	t, done := plan.exits[target]
	why := plan.why[target]
	if !done {
		var ob map[string]any
		t = "route-" + strings.Replace(target, ":", "-", 1)
		if ob, why = p.exitOutbound(ctx, srv, plan, target, t); why == "" {
			plan.outbounds = append(plan.outbounds, ob)
			plan.tags[t] = true
		} else {
			t = ""
			plan.why[target] = why
		}
		plan.exits[target] = t
	}
	if t == "" {
		plan.notes = append(plan.notes, fmt.Sprintf("Rule %q does not work: %s. Its traffic is blocked - it never leaves directly instead - until the exit can be used again, or you send the rule elsewhere.", title, why))
	}
	return t
}

// balancerTag sets up load balancer b on this server: its members as outbounds "lb<id>.<member>",
// and the balancer itself. It returns the balancer's tag, or "" when no member can be used here (the
// rule then sends to its fallback).
func (p *Panel) balancerTag(ctx context.Context, srv *Server, plan *routePlan, b *Balancer) string {
	tag := fmt.Sprintf("lb-%d", b.ID)
	if plan.tags[tag] {
		return tag
	}
	if done, seen := plan.exits["lb:"+strconv.FormatInt(b.ID, 10)]; seen && done == "" {
		return "" // tried already: nothing works here
	}
	prefix := fmt.Sprintf("lb%d.", b.ID)
	used := 0
	var members []string // a subscription link stands for its nodes
	for _, m := range b.Members {
		id, ok := strings.CutPrefix(m, "src:")
		if !ok {
			members = append(members, m)
			continue
		}
		n, _ := strconv.ParseInt(id, 10, 64)
		exts, why := p.sourceNodes(ctx, srv.AccountID, n)
		if why != "" {
			plan.notes = append(plan.notes, fmt.Sprintf("Load balancer %q leaves out a member: %s.", b.Name, why))
		}
		for _, x := range exts {
			if !slices.Contains(b.Members, x) && !slices.Contains(members, x) {
				members = append(members, x)
			}
		}
	}
	for _, m := range members {
		mt := prefix + strings.Replace(m, ":", "", 1)
		if m == "direct" {
			plan.outbounds = append(plan.outbounds, directOutbound(srv, mt))
			used++
			continue
		}
		ob, why := p.exitOutbound(ctx, srv, plan, m, mt)
		if why != "" {
			plan.notes = append(plan.notes, fmt.Sprintf("Load balancer %q leaves out a member: %s.", b.Name, why))
			continue
		}
		plan.outbounds = append(plan.outbounds, ob)
		used++
	}
	if used == 0 {
		what := "is blocked - it never leaves directly instead -"
		if b.directFallback() {
			what = "leaves directly (its fallback)"
		}
		plan.notes = append(plan.notes, fmt.Sprintf("Load balancer %q has no member it can use: the traffic rules send to it %s until one can be used again.", b.Name, what))
		plan.exits["lb:"+strconv.FormatInt(b.ID, 10)] = ""
		return ""
	}
	bal := map[string]any{"tag": tag, "selector": []string{prefix}, "strategy": map[string]any{"type": b.Strategy}}
	// only a fastest-first balancer knows when its members are down (Xray's latency checks): it alone
	// has a fallback - in Xray a fallback needs those checks, and without them the configuration is
	// refused
	if b.Strategy == "leastPing" {
		fallback := "block"
		if b.directFallback() {
			fallback = directTag(srv)
		}
		bal["fallbackTag"] = fallback
		plan.observe = true
	}
	plan.balancers = append(plan.balancers, bal)
	plan.tags[tag] = true
	return tag
}

// routePlanFor builds what the account's rules do on server srv. passers are, per protocol on this
// server, the identities that pass through it (protocols elsewhere, other servers' rules); out is
// the way each Xray protocol's traffic leaves when no rule takes it, own the outbounds behind those
// ways (its own address, its proxy pass). What passes through keeps going that way: it skips this
// server's rules.
func (p *Panel) routePlanFor(ctx context.Context, srv *Server, nodes []*Node, passers map[int64][]string, out map[int64]string,
	own []map[string]any) (*routePlan, error) {
	plan := &routePlan{tags: map[string]bool{}, exits: map[string]string{}, why: map[string]string{}, out: out, own: own}
	rules, err := p.routesOf(ctx, srv.AccountID)
	if err != nil || len(rules) == 0 {
		return plan, err
	}
	var xrayNodes []*Node
	for _, n := range nodes {
		if k, _ := kindOf(n.Kind); n.Enabled && k.Engine == "xray" {
			xrayNodes = append(xrayNodes, n)
		}
	}
	bals := map[int64]*Balancer{}
	if list, err := p.balancersOf(ctx, srv.AccountID); err == nil {
		for _, b := range list {
			bals[b.ID] = b
		}
	}
	var routeRules []map[string]any
	for _, rt := range rules {
		if !rt.Enabled {
			continue
		}
		tags := rt.applies(srv, xrayNodes)
		if len(tags) == 0 {
			continue
		}
		// where it sends: outboundTag or balancerTag; "direct" from a protocol with its own address
		// leaves from that address
		type dest struct {
			key, tag string
			tags     []string
		}
		var dests []dest
		switch {
		case rt.Target == "block":
			dests = []dest{{"outboundTag", "block", tags}}
		case rt.Target == "direct":
			var plain []string
			for _, n := range xrayNodes {
				t := proto.InboundTag(n.ID)
				if !slices.Contains(tags, t) {
					continue
				}
				if n.BindIP == "" {
					plain = append(plain, t)
					continue
				}
				// its own address: the outbound is there unless the protocol passes through an exit
				if bt := bindTag(n.ID); (n.PassNode != 0 || n.PassExt != 0) && !plan.tags[bt] {
					plan.outbounds = append(plan.outbounds, bindOutbound(srv, n))
					plan.tags[bt] = true
				}
				dests = append(dests, dest{"outboundTag", bindTag(n.ID), []string{t}})
			}
			if len(plain) > 0 {
				dests = append(dests, dest{"outboundTag", directTag(srv), plain})
			}
		case strings.HasPrefix(rt.Target, "lb:"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(rt.Target, "lb:"), 10, 64)
			b := bals[id]
			if b == nil {
				plan.notes = append(plan.notes, fmt.Sprintf("Rule %q does not work: its load balancer was removed. Its traffic is blocked until you send the rule elsewhere.", rt.title()))
				dests = []dest{{"outboundTag", "block", tags}}
				break
			}
			if t := p.balancerTag(ctx, srv, plan, b); t != "" {
				dests = []dest{{"balancerTag", t, tags}}
			} else if b.directFallback() {
				dests = []dest{{"outboundTag", directTag(srv), tags}}
			} else {
				dests = []dest{{"outboundTag", "block", tags}}
			}
		default:
			t := p.exitTag(ctx, srv, plan, rt.Target, rt.title())
			if t == "" {
				t = "block"
			}
			dests = []dest{{"outboundTag", t, tags}}
		}
		m := rt.Match
		var domains, ips []string
		for _, s := range m.Sites {
			domains = append(domains, "geosite:"+s)
		}
		domains = append(domains, m.Domains...)
		for _, c := range m.Countries {
			ips = append(ips, "geoip:"+c)
		}
		ips = append(ips, m.IPs...)
		base := func(d dest, suffix string) map[string]any {
			r := map[string]any{"ruleTag": fmt.Sprintf("r%d%s", rt.ID, suffix), "inboundTag": d.tags, d.key: d.tag}
			if m.Ports != "" {
				r["port"] = m.Ports
			}
			if m.Network != "" {
				r["network"] = m.Network
			}
			if m.BitTorrent {
				r["protocol"] = []string{"bittorrent"}
			}
			return r
		}
		for i, d := range dests {
			suffix := ""
			if len(dests) > 1 {
				suffix = fmt.Sprintf(".%d", i)
			}
			// a name and an address are matched apart: a connection matches either list
			switch {
			case len(domains) > 0 && len(ips) > 0:
				rd, ri := base(d, suffix+"d"), base(d, suffix+"i")
				rd["domain"], ri["ip"] = domains, ips
				routeRules = append(routeRules, rd, ri)
			case len(domains) > 0:
				r := base(d, suffix)
				r["domain"] = domains
				routeRules = append(routeRules, r)
			case len(ips) > 0:
				r := base(d, suffix)
				r["ip"] = ips
				routeRules = append(routeRules, r)
			default:
				routeRules = append(routeRules, base(d, suffix))
			}
		}
	}
	if len(routeRules) == 0 {
		return plan, nil
	}
	// what passes through this server's protocols keeps going the way those protocols send it
	for _, n := range nodes {
		if emails := passers[n.ID]; len(emails) > 0 && out[n.ID] != "" {
			plan.rules = append(plan.rules, map[string]any{"ruleTag": fmt.Sprintf("passers-n%d", n.ID),
				"inboundTag": []string{proto.InboundTag(n.ID)}, "user": emails, "outboundTag": out[n.ID]})
		}
	}
	plan.rules = append(plan.rules, routeRules...)
	return plan, nil
}

// routeClients are the identities other servers' rules use at the protocols of server srv: per
// protocol, the servers whose rules (directly or through a load balancer) send traffic there.
func (p *Panel) routeClients(ctx context.Context, srv *Server, nodes []*Node) map[int64][]passClient {
	out := map[int64][]passClient{}
	rules, err := p.routesOf(ctx, srv.AccountID)
	if err != nil || len(rules) == 0 {
		return out
	}
	bals := map[string]*Balancer{}
	if list, err := p.balancersOf(ctx, srv.AccountID); err == nil {
		for _, b := range list {
			bals[fmt.Sprintf("lb:%d", b.ID)] = b
		}
	}
	mine := map[string]*Node{}
	for _, n := range nodes {
		if n.Enabled && canExit(n.Kind) {
			mine[fmt.Sprintf("node:%d", n.ID)] = n
		}
	}
	servers, err := p.serversOf(ctx, srv.AccountID)
	if err != nil {
		return out
	}
	nodesBy := map[int64][]*Node{} // each server's protocols, read once
	nodesOf := func(id int64) []*Node {
		if _, ok := nodesBy[id]; !ok {
			nodesBy[id], _ = p.nodesOf(ctx, id)
		}
		return nodesBy[id]
	}
	added := map[string]bool{}
	for _, rt := range rules {
		if !rt.Enabled {
			continue
		}
		targets := []string{rt.Target}
		if b := bals[rt.Target]; b != nil {
			targets = b.Members
		}
		for _, t := range targets {
			exit := mine[t]
			if exit == nil {
				continue
			}
			for _, s := range servers {
				if s.ID == srv.ID || s.DeletedAt > 0 || added[fmt.Sprintf("%d/%d", s.ID, exit.ID)] {
					continue
				}
				if !rt.reaches(s, nodesOf(s.ID)) {
					continue
				}
				added[fmt.Sprintf("%d/%d", s.ID, exit.ID)] = true
				uuid, password := routeCredentials(s, exit)
				out[exit.ID] = append(out[exit.ID], passClient{Email: routeEmail(s.ID), UUID: uuid, Password: password})
			}
		}
	}
	return out
}

// reaches says whether rule rt applies to some Xray protocol on server s, which has nodes.
func (rt *Route) reaches(s *Server, nodes []*Node) bool {
	if len(rt.Nodes) == 0 && len(rt.Servers) > 0 && !slices.Contains(rt.Servers, s.ID) {
		return false
	}
	for _, n := range nodes {
		if k, _ := kindOf(n.Kind); n.Enabled && k.Engine == "xray" && (len(rt.Nodes) == 0 || slices.Contains(rt.Nodes, n.ID)) {
			return true
		}
	}
	return false
}

// touchRoutes recompiles the account's servers when it has traffic rules: they name protocols as
// exits and as what they apply to, so a protocol's change can reach every server.
func (p *Panel) touchRoutes(ctx context.Context, acct int64) {
	var n int
	if p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM routes WHERE account_id = ?`, acct).Scan(&n) == nil && n > 0 {
		p.touchAccount(acct)
	}
}

// setRouteNotes keeps what the last compile of server id found about its traffic rules.
func (p *Panel) setRouteNotes(id int64, notes []string) {
	p.routeMu.Lock()
	defer p.routeMu.Unlock()
	if p.routeNotes == nil {
		p.routeNotes = map[int64][]string{}
	}
	if len(notes) == 0 {
		delete(p.routeNotes, id)
		return
	}
	p.routeNotes[id] = notes
}

// routeNotesOf returns them (nil when every rule works there).
func (p *Panel) routeNotesOf(id int64) []string {
	p.routeMu.Lock()
	defer p.routeMu.Unlock()
	return append([]string(nil), p.routeNotes[id]...)
}

// observatory lets Xray measure the latency of every load balancer member ("lb" outbounds) for the
// fastest-first ones; Google's no-content page answers from everywhere it is reachable.
var observatory = map[string]any{"subjectSelector": []string{"lb"}, "probeUrl": "https://www.gstatic.com/generate_204",
	"probeInterval": "1m", "enableConcurrency": true}
