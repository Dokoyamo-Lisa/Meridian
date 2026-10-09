package panel

// Dynamic DNS: a server whose IP address changes (a home connection, a provider that hands out new
// addresses) is reached by a domain name its updater keeps pointing at it. Such a server is marked
// (ddns): its address must then be the name, which every link to it uses - users' links and other
// servers' proxy passes and traffic rules. The panel resolves the name when it is saved and every few
// minutes and compares it with the addresses the agent reports; when they differ the server's page
// and the overview's attention list say so. Only marked servers are checked: a name in front of a
// CDN never points at the server itself. The panel can also be the updater itself, through
// Cloudflare (cloudflare.go).

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"
)

// lookupIP resolves a name with the system's resolver; tests replace it.
var lookupIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

const (
	dnsTimeout   = 3 * time.Second  // one lookup
	dnsEvery     = 5 * time.Minute  // each name is resolved again this often
	dnsAfterMove = time.Minute      // after a server's address changed: time for its updater to act
	cfEvery      = 10 * time.Minute // Cloudflare's records are checked this often
	dnsAtOnce    = 4                // lookups at the same time
	panelEvery   = 10 * time.Minute // the panel's own address
)

// dnsCheck is what a server's dynamic DNS name resolved to.
type dnsCheck struct {
	name  string
	addrs []netip.Addr
	err   string // why it does not resolve ("" = it does)
	at    int64
}

// cfState is how keeping a server's name in Cloudflare last went.
type cfState struct {
	name    string // the name it was about
	state   string // ok | failed | waiting ("" = not tried yet)
	msg     string // what the last update found, or why it failed or waits
	at      int64
	failed  string // the failure an event was recorded for (one event per failure, not per attempt)
	tried   time.Time
	pending bool // asked for now: an address changed or the server was saved
}

// dynState is the panel's view of dynamic DNS names, kept in memory (a restart checks again). Its
// zero value is ready to use.
type dynState struct {
	mu      sync.Mutex
	checks  map[int64]*dnsCheck
	recheck map[int64]time.Time // a check due soon (after an address changed)
	cf      map[int64]*cfState
	zones   map[string]cfZone // Cloudflare zones by name, for an hour
	token   *string           // the Cloudflare token as stored (nil = not read yet)
	kick    chan struct{}
	panel   panelAddrCache
}

func (d *dynState) kickCh() chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.kick == nil {
		d.kick = make(chan struct{}, 1)
	}
	return d.kick
}

func (d *dynState) wake() {
	select {
	case d.kickCh() <- struct{}{}:
	default:
	}
}

// moved notes that a server's addresses changed: its name is checked again shortly (its updater
// needs a moment) and its Cloudflare records are updated now.
func (d *dynState) moved(id int64) {
	d.mu.Lock()
	if d.recheck == nil {
		d.recheck = map[int64]time.Time{}
	}
	d.recheck[id] = time.Now().Add(dnsAfterMove)
	d.cfFor(id).pending = true
	d.mu.Unlock()
	d.wake()
}

// syncSoon asks for a server's Cloudflare records to be updated now.
func (d *dynState) syncSoon(ids ...int64) {
	d.mu.Lock()
	for _, id := range ids {
		d.cfFor(id).pending = true
	}
	d.mu.Unlock()
	d.wake()
}

// cfFor returns a server's Cloudflare state; d.mu is held.
func (d *dynState) cfFor(id int64) *cfState {
	if d.cf == nil {
		d.cf = map[int64]*cfState{}
	}
	st := d.cf[id]
	if st == nil {
		st = &cfState{}
		d.cf[id] = st
	}
	return st
}

func (d *dynState) check(id int64) *dnsCheck {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c := d.checks[id]; c != nil {
		cp := *c
		return &cp
	}
	return nil
}

// dnsDue says whether a server's name should be resolved now.
func (d *dynState) dnsDue(s *Server, t time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if at, ok := d.recheck[s.ID]; ok && !t.Before(at) {
		return true
	}
	c := d.checks[s.ID]
	return c == nil || c.name != s.Address || t.Unix()-c.at >= int64(dnsEvery/time.Second)
}

// cfDue says whether a server's Cloudflare records should be looked at now.
func (d *dynState) cfDue(s *Server, t time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.cfFor(s.ID)
	return st.pending || st.name != s.Address || t.Sub(st.tried) >= cfEvery
}

// ---------------------------------------------------------------- checking a name

// resolveName looks a name up once. A name that does not exist or has no address is a problem worth
// showing; a lookup that got no answer is not known either way (temporary is true).
func resolveName(ctx context.Context, name string) (addrs []netip.Addr, why string, temporary bool) {
	ctx, cancel := context.WithTimeout(ctx, dnsTimeout)
	defer cancel()
	ips, err := lookupIP(ctx, name)
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return nil, "it does not exist", false
	case err != nil:
		return nil, "", true
	}
	for _, ip := range ips {
		if ip = ip.Unmap(); ip.IsValid() && !slices.Contains(addrs, ip) {
			addrs = append(addrs, ip)
		}
	}
	slices.SortFunc(addrs, func(a, b netip.Addr) int { return a.Compare(b) })
	if len(addrs) == 0 {
		return nil, "it has no address", false
	}
	return addrs, "", false
}

// checkDNS resolves a dynamic DNS server's name now and keeps the result. A lookup that got no
// answer keeps the last result for that name.
func (p *Panel) checkDNS(ctx context.Context, s *Server) {
	if !s.DDNS || s.Address == "" {
		return
	}
	addrs, why, temporary := resolveName(ctx, s.Address)
	p.dyn.mu.Lock()
	defer p.dyn.mu.Unlock()
	delete(p.dyn.recheck, s.ID)
	if p.dyn.checks == nil {
		p.dyn.checks = map[int64]*dnsCheck{}
	}
	if old := p.dyn.checks[s.ID]; temporary && old != nil && old.name == s.Address {
		old.at = now()
		return
	}
	if temporary {
		return
	}
	p.dyn.checks[s.ID] = &dnsCheck{name: s.Address, addrs: addrs, err: why, at: now()}
}

// checkDNSAll resolves several names, a few at a time.
func (p *Panel) checkDNSAll(ctx context.Context, servers []*Server) {
	sem := make(chan struct{}, dnsAtOnce)
	var wg sync.WaitGroup
	for _, s := range servers {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			p.checkDNS(ctx, s)
		}()
	}
	wg.Wait()
}

// dnsProblems compares a server's last check with the addresses its agent reports: records that
// point elsewhere (devices that pick them fail), and a kind of address the name lacks while it is
// the only way some devices reach the server. Nothing before the first check of the current name.
func dnsProblems(s *Server, c *dnsCheck) []string {
	if !s.DDNS || c == nil || c.name != s.Address {
		return nil
	}
	name := s.Address
	if c.err != "" {
		return []string{fmt.Sprintf("%s does not resolve (%s) - links to %s go nowhere until your dynamic DNS sets the name", name, c.err, s.Name)}
	}
	if s.FirstSeenAt == 0 { // nothing reported yet to compare with
		return nil
	}
	mine := map[netip.Addr]bool{}
	for _, x := range append([]string{s.IPv4, s.IPv6}, s.Addrs...) {
		if a, err := netip.ParseAddr(x); err == nil {
			mine[a.Unmap()] = true
		}
	}
	var a4, a6 []netip.Addr
	for _, a := range c.addrs {
		if a.Is4() {
			a4 = append(a4, a)
		} else {
			a6 = append(a6, a)
		}
	}
	var out []string
	stale := func(list []netip.Addr, have, label, record string) {
		var others []netip.Addr
		for _, a := range list {
			if !mine[a] {
				others = append(others, a)
			}
		}
		switch {
		case len(others) == 0:
		case len(others) < len(list):
			out = append(out, fmt.Sprintf("%s also points at %s, which is not this server's address - devices that pick it fail until your dynamic DNS removes it", name, addrList(others)))
		case have != "":
			out = append(out, fmt.Sprintf("%s points at %s but the server's address is now %s - links go to the old address until your dynamic DNS updates the name", name, addrList(others), have))
		default:
			out = append(out, fmt.Sprintf("%s points at %s, but the server has no %s address - devices that try it fail: remove its %s record", name, addrList(others), label, record))
		}
	}
	stale(a4, s.IPv4, "IPv4", "A")
	stale(a6, s.IPv6, "IPv6", "AAAA")
	has4, has6 := s.v4() && s.IPv4 != "", s.v6() && s.IPv6 != ""
	switch {
	case has4 && len(a4) == 0:
		out = append(out, fmt.Sprintf("%s has no IPv4 address (A record) for %s - devices on IPv4 cannot connect until your dynamic DNS adds it", name, s.IPv4))
	case has6 && !has4 && len(a6) == 0:
		out = append(out, fmt.Sprintf("%s has no IPv6 address (AAAA record) for %s - nothing can connect until your dynamic DNS adds it", name, s.IPv6))
	}
	return out
}

// addrList writes addresses for a sentence: "a", "a and b", "a, b and c".
func addrList(list []netip.Addr) string {
	s := make([]string, len(list))
	for i, a := range list {
		s[i] = a.String()
	}
	if len(s) < 2 {
		return strings.Join(s, "")
	}
	return strings.Join(s[:len(s)-1], ", ") + " and " + s[len(s)-1]
}

// ---------------------------------------------------------------- views

// dnsView is a dynamic DNS server's name as the panel last saw it.
type dnsView struct {
	Name       string   `json:"name" doc:"The name: the server's address"`
	Addrs      []string `json:"addrs" doc:"What it resolved to at the last check"`
	CheckedAt  int64    `json:"checked_at" doc:"When the panel last resolved it (Unix seconds); 0 = not yet"`
	Problems   []string `json:"problems,omitempty" doc:"What is wrong, in plain words: the name points elsewhere, lacks an address of the server, or does not resolve"`
	Cloudflare *cfView  `json:"cloudflare,omitempty" doc:"With ddns_cloudflare: how keeping the name up to date in Cloudflare last went"`
}

type cfView struct {
	State   string `json:"state" doc:"ok | failed | waiting (not tried yet)"`
	Message string `json:"message,omitempty" doc:"What the last update did, or why it failed and what to do"`
	At      int64  `json:"at,omitempty" doc:"When (Unix seconds)"`
}

// dnsViewOf is a server's name for the API (nil when the server is not marked dynamic).
func (p *Panel) dnsViewOf(s *Server) *dnsView {
	if !s.DDNS {
		return nil
	}
	v := &dnsView{Name: s.Address, Addrs: []string{}}
	if c := p.dyn.check(s.ID); c != nil && c.name == s.Address {
		v.CheckedAt = c.at
		for _, a := range c.addrs {
			v.Addrs = append(v.Addrs, a.String())
		}
		v.Problems = dnsProblems(s, c)
	}
	if s.DDNSCloudflare {
		v.Cloudflare = &cfView{State: "waiting", Message: "not updated yet"}
		p.dyn.mu.Lock()
		if st := p.dyn.cf[s.ID]; st != nil && st.state != "" && st.name == s.Address {
			v.Cloudflare = &cfView{State: st.state, Message: st.msg, At: st.at}
		}
		p.dyn.mu.Unlock()
	}
	return v
}

// dnsAlerts are a server's dynamic DNS problems for the overview's attention list.
func (p *Panel) dnsAlerts(s *Server) []alert {
	v := p.dnsViewOf(s)
	if v == nil {
		return nil
	}
	var out []alert
	for _, msg := range v.Problems {
		out = append(out, alert{Level: "warn", Kind: "dns_stale", ServerID: s.ID, Message: s.Name + ": " + msg})
	}
	if v.Cloudflare != nil && v.Cloudflare.State == "failed" {
		out = append(out, alert{Level: "warn", Kind: "ddns_failed", ServerID: s.ID, Message: s.Name + ": " + v.Cloudflare.Message})
	}
	return out
}

// ---------------------------------------------------------------- saving a server

// dynFields checks a server's dynamic DNS settings as they will be after a change: s is the server
// before it (nil on create), addr its address afterwards.
func (p *Panel) dynFields(in *serverInput, s *Server, addr string) (ddns, cf bool, err error) {
	wasCF := false
	if s != nil {
		ddns, cf, wasCF = s.DDNS, s.DDNSCloudflare, s.DDNSCloudflare
	}
	if in.DDNS != nil {
		ddns = *in.DDNS
	}
	if in.Cloudflare != nil {
		cf = *in.Cloudflare
	}
	switch {
	case !ddns && in.Cloudflare != nil && *in.Cloudflare:
		return false, false, errStatus(http.StatusBadRequest, "keeping the name up to date in Cloudflare is for servers whose IP address changes - turn that on (ddns) with the name as the address")
	case !ddns:
		return false, false, nil
	case !publicName(addr):
		return false, false, errStatus(http.StatusBadRequest, "a server whose IP address changes needs its dynamic DNS name as its address, e.g. home.example.com - links use the name, which follows the server")
	case cf && !wasCF && p.cfToken() == "":
		return false, false, errStatus(http.StatusBadRequest, "save a Cloudflare API token first (Settings › Dynamic DNS) - or keep the name up to date with your own updater")
	}
	return ddns, cf, nil
}

// dynSaved follows a save of server id (before is the server as it was, nil when it is new): its
// name is resolved at once, its Cloudflare records are updated, and turning Cloudflare on or off is
// recorded.
func (p *Panel) dynSaved(ctx context.Context, before *Server, id, actor int64) {
	s, err := p.serverByID(ctx, id)
	if err != nil {
		return
	}
	wasCF := before != nil && before.DDNSCloudflare
	switch {
	case s.DDNSCloudflare && (!wasCF || before.Address != s.Address):
		p.event(s.AccountID, "info", "ddns_cloudflare", s.ID, 0, actor, fmt.Sprintf(
			"The panel keeps %s pointing at %s in Cloudflare: its A and AAAA records follow the server's addresses", s.Address, s.Name), nil)
	case wasCF && !s.DDNSCloudflare:
		p.event(s.AccountID, "info", "ddns_cloudflare", s.ID, 0, actor, fmt.Sprintf(
			"The panel no longer keeps %s up to date in Cloudflare - its records stay as they are", before.Address), nil)
	}
	p.checkDNS(ctx, s)
	if s.DDNSCloudflare {
		p.dyn.syncSoon(s.ID)
	}
}

// ---------------------------------------------------------------- the loop

// dynLoop keeps dynamic DNS names in view: each is resolved every few minutes, and the names the
// panel keeps in Cloudflare are checked every ten - both at once after an address change or a save.
func (p *Panel) dynLoop(ctx context.Context) {
	kick := p.dyn.kickCh()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		p.dynTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-kick:
		}
	}
}

func (p *Panel) dynTick(ctx context.Context) {
	servers, err := p.serversOf(ctx, 0)
	if err != nil {
		return
	}
	t := time.Now()
	var due []*Server
	for _, s := range servers {
		if s.DDNS && p.dyn.dnsDue(s, t) {
			due = append(due, s)
		}
	}
	p.checkDNSAll(ctx, due)
	for _, s := range servers {
		if s.DDNS && s.DDNSCloudflare && p.dyn.cfDue(s, t) && ctx.Err() == nil {
			p.cloudflareSync(ctx, s)
		}
	}
}

// ---------------------------------------------------------------- MCP

const (
	dynamicDNSHelp    = "The server's IP address changes (dynamic DNS, e.g. a home connection): its address must then be its domain name (e.g. home.example.com), which every link to it uses - users' links and other servers' proxy passes and traffic rules. The panel checks the name points at the addresses the agent reports (get_server: dns.problems)"
	cloudflareDNSHelp = "With dynamic_dns: the panel itself keeps the name's A and AAAA records in Cloudflare pointing at the server (DNS only, a short TTL). Needs the Cloudflare token saved in Settings › Dynamic DNS (only the operator can do that, in the panel). Turning it on replaces those two records of that name and removes one of a kind the server has no address of, so it needs confirm=true; no other name is touched"
)

// dynamicDNSArgs copies the MCP tools' dynamic DNS arguments into an API request body.
func dynamicDNSArgs(a, body map[string]any) {
	if v, ok := argBool(a, "dynamic_dns"); ok {
		body["ddns"] = v
	}
	if v, ok := argBool(a, "cloudflare"); ok {
		body["ddns_cloudflare"] = v
	}
}

// cloudflareTakeover says what a call does to a name's records in Cloudflare when it turns keeping
// them on, or moves it to another name, for the MCP tools' confirmation ("" otherwise). s is the
// server before the call (nil for a new one).
func cloudflareTakeover(s *Server, a map[string]any) string {
	on, set := argBool(a, "cloudflare")
	name, named := argStr(a, "address")
	if s != nil {
		if !set {
			on = s.DDNSCloudflare
		}
		if !named {
			name = s.Address
		}
		if h, err := normHost(name); err == nil && s.DDNSCloudflare && h == s.Address {
			return "" // already kept, for that name
		}
	}
	if !on {
		return ""
	}
	if h, err := normHost(name); err == nil && h != "" {
		name = h
	}
	return fmt.Sprintf("Turning on Cloudflare for %s makes the panel set that name's A and AAAA records to the server's addresses (DNS only, not proxied) - replacing what they point at now - and remove its A or AAAA record whenever the server has no address of that kind. No other name is touched.", name)
}
