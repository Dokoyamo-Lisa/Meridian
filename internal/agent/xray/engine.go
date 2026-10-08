// Package xray runs Xray for the agent. Changes are applied to the running process through its API:
// users are added and removed one by one, a changed inbound is re-added on its own, outbounds and
// routing rules are swapped live. A restart happens only when nothing else can apply a change, and
// then only when an admin asked for it.
package xray

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"meridian/internal/agent/cores"
	"meridian/internal/agent/nft"
	"meridian/internal/agent/service"
	"meridian/internal/agent/sys"
	"meridian/internal/proto"
)

const Unit = "meridian-xray"

type Engine struct {
	Base    string // data dir, e.g. /var/lib/meridian-agent
	ConfDir string // e.g. /etc/meridian-agent/xray
	RunDir  string // e.g. /run/meridian-agent
	APIPort int
	Events  func(kind, level, msg string)
	// HasAddr says whether the host has an address (nil: its interfaces). An inbound that listens on
	// an address the host does not have would stop Xray from starting at all, taking every protocol
	// down with it: such an inbound is left out until the address is back.
	HasAddr func(netip.Addr) bool
	// Reserved are the agent's loopback control ports (the Xray API, Hysteria's hooks): no inbound may
	// send strangers there (nil: APIPort and the one after it).
	Reserved func() []int
	// KernelGuard says whether the kernel refuses what users must not reach (nftables, see
	// nft.DirectMark); without it, routing resolves names before the private-address rule (nil:
	// whether nft is installed).
	KernelGuard func() bool

	mu      sync.Mutex
	api     *API
	tail    *accessTail
	agg     *Aggregate
	since   map[string]int64
	lastPID int
	desired *proto.Xray
	logOn   bool
	waiting bool              // settings wait for a restart (see ApplyResult.Pending)
	aliases map[string]string // SOCKS5 / HTTP user name -> identity ("s1.n2"); those users have no email
}

// identity turns a stat or user name into a subscription and node, through the user-name aliases.
// The caller holds e.mu.
func (e *Engine) identity(name string) (sub, node int64, ok bool) {
	n := strings.TrimPrefix(name, "user>>>")
	if i := strings.Index(n, ">>>"); i >= 0 {
		n = n[:i]
	}
	if a, found := e.aliases[n]; found {
		n = a
	}
	return proto.ParseEmail(n)
}

// accessIdentity says whose connection an access log line records: the user from its email -
// through the aliases, as SOCKS5 and HTTP lines carry the user name - and the node from the inbound
// it arrived on, which stays exact even when an imported user name repeats across inbounds.
// The caller holds e.mu.
func (e *Engine) accessIdentity(ent AccessEntry) (sub, node int64, ok bool) {
	if !ent.OK || ent.Email == "" {
		return 0, 0, false
	}
	if sub, node, ok = e.identity(ent.Email); !ok {
		return 0, 0, false
	}
	if n, isNode := proto.ParseInboundTag(ent.Inbound); isNode {
		node = n
	}
	return sub, node, true
}

func (e *Engine) init() {
	if e.api == nil {
		e.api = NewAPI("127.0.0.1:" + strconv.Itoa(e.APIPort))
		e.agg = newAggregate()
		e.since = map[string]int64{}
		e.tail = &accessTail{path: e.accessPath(), api: e.api}
	}
}

func (e *Engine) configPath() string { return filepath.Join(e.ConfDir, "config.json") }
func (e *Engine) accessPath() string { return filepath.Join(e.RunDir, "xray-access.log") }
func (e *Engine) currentDir() string { return filepath.Join(e.Base, "cores", "xray", "current") }
func (e *Engine) bin() string        { return filepath.Join(e.currentDir(), "xray") }

func (e *Engine) event(kind, level, msg string) {
	if e.Events != nil {
		e.Events(kind, level, msg)
	}
}

// Installed reports whether an Xray binary is in place.
func (e *Engine) Installed() bool {
	_, err := os.Stat(e.bin())
	return err == nil
}

// Version of the installed binary.
func (e *Engine) Version() string {
	out, err := exec.Command(e.bin(), "version").Output()
	if err != nil {
		return ""
	}
	f := strings.Fields(string(out))
	if len(f) >= 2 {
		return f[1]
	}
	return ""
}

// ---------------------------------------------------------------- rendering

type rendered struct {
	full      map[string]any
	inbounds  map[string]map[string]any // tag -> inbound without clients
	clients   map[string]map[string]proto.XrayClient
	outbounds map[string]any
	outOrder  []string
	rules     any
	rest      map[string]any // everything else; changes here need a restart
	aliases   map[string]string
	missing   []string // inbounds left out: they listen on an address the host does not have
}

// missingListen returns the address an inbound listens on when the host does not have it. Any
// address, loopback, a domain or a socket path is never missing.
func (e *Engine) missingListen(obj map[string]any) (string, bool) {
	listen, _ := obj["listen"].(string)
	a, gone := sys.MissingAddr(listen, e.HasAddr)
	return a.String(), gone
}

func (e *Engine) render(d *proto.Xray, accessLog bool) (*rendered, error) {
	r := &rendered{inbounds: map[string]map[string]any{}, clients: map[string]map[string]proto.XrayClient{},
		outbounds: map[string]any{}, aliases: map[string]string{}}
	base := map[string]any{}
	if len(d.Base) > 0 {
		if err := json.Unmarshal(d.Base, &base); err != nil {
			return nil, fmt.Errorf("base config: %w", err)
		}
	}
	// The access log stays on even when logging is switched off in the panel (the agent then just
	// discards it): turning it on or off would otherwise need a restart. It lives in /run (RAM) and
	// is rotated at a few MB.
	_ = accessLog
	base["log"] = map[string]any{"loglevel": "warning", "access": e.accessPath(), "dnsLog": false}
	base["api"] = map[string]any{"tag": "api", "listen": "127.0.0.1:" + strconv.Itoa(e.APIPort),
		"services": []string{"HandlerService", "StatsService", "LoggerService", "RoutingService"}}
	base["stats"] = map[string]any{}
	base["policy"] = map[string]any{
		"levels": map[string]any{"0": map[string]any{"statsUserUplink": true, "statsUserDownlink": true,
			"statsUserOnline": true}},
		"system": map[string]any{"statsInboundUplink": true, "statsInboundDownlink": true}}

	var inbounds []any
	reserved := e.reserved()
	for _, in := range d.Inbounds {
		var obj map[string]any
		if err := json.Unmarshal(in.Config, &obj); err != nil {
			return nil, fmt.Errorf("inbound %s: %w", in.Tag, err)
		}
		if addr, gone := e.missingListen(obj); gone {
			r.missing = append(r.missing, fmt.Sprintf("protocol %s is left out: this server has no address %s (it comes back by itself when the address does)", in.Tag, addr))
			continue
		}
		if where := strangersTo(obj, reserved); where != "" {
			r.missing = append(r.missing, fmt.Sprintf("protocol %s is left out: it would send strangers to %s, one of the agent's own control ports - choose another port for your site", in.Tag, where))
			continue
		}
		r.inbounds[in.Tag] = obj
		cm := map[string]proto.XrayClient{}
		var list []any
		for _, c := range in.Clients {
			key := c.Email
			if userless(obj) && c.Account.Username != "" {
				key = c.Account.Username
				r.aliases[key] = c.Email
			}
			cm[key] = c
			var co any
			if err := json.Unmarshal(c.JSON, &co); err != nil {
				return nil, fmt.Errorf("client %s: %w", c.Email, err)
			}
			list = append(list, co)
		}
		r.clients[in.Tag] = cm
		if operatorOwn(in.Tag) { // an inbound from the operator's own code keeps the users written in it
			inbounds = append(inbounds, obj)
			continue
		}
		withClients := withClients(obj, list)
		inbounds = append(inbounds, withClients)
	}
	if inbounds == nil {
		inbounds = []any{}
	}
	base["inbounds"] = inbounds

	if obs, ok := base["outbounds"].([]any); ok {
		for _, o := range obs {
			if m, ok := o.(map[string]any); ok {
				tag, _ := m["tag"].(string)
				r.outbounds[tag] = m
				r.outOrder = append(r.outOrder, tag)
			}
		}
	}
	e.guard(base)
	if rt, ok := base["routing"].(map[string]any); ok {
		r.rules = rt["rules"]
	}
	r.rest = map[string]any{}
	for k, v := range base {
		switch k {
		case "inbounds", "outbounds":
			continue
		case "routing":
			if rt, ok := v.(map[string]any); ok {
				cp := map[string]any{}
				for k2, v2 := range rt {
					if k2 != "rules" {
						cp[k2] = v2
					}
				}
				r.rest[k] = cp
			}
			continue
		}
		r.rest[k] = v
	}
	r.full = base
	return r, nil
}

// reserved are the loopback ports no inbound may send strangers to.
func (e *Engine) reserved() []int {
	if e.Reserved != nil {
		return e.Reserved()
	}
	return []int{e.APIPort, e.APIPort + 1}
}

// guard keeps users away from this host and from private networks. Every "freedom" outbound (what
// users reach sites through) marks its connections, so the kernel refuses those that lead here or to
// a private, link-local or metadata address - also when the user asked for a name that resolves
// there, which the routing rule against private addresses cannot see. An outbound that sets its own
// mark is the operator's choice and stays as written. Without nftables, routing resolves names before
// its rules instead (one more lookup per connection), so that rule sees them.
func (e *Engine) guard(base map[string]any) {
	obs, _ := base["outbounds"].([]any)
	for _, o := range obs {
		m, ok := o.(map[string]any)
		if !ok || m["protocol"] != "freedom" {
			continue
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
		if _, own := so["mark"]; !own {
			so["mark"] = nft.DirectMark
		}
	}
	kernel := nft.Available
	if e.KernelGuard != nil {
		kernel = e.KernelGuard
	}
	if rt, ok := base["routing"].(map[string]any); ok && !kernel() {
		if ds, _ := rt["domainStrategy"].(string); ds == "" || strings.EqualFold(ds, "AsIs") {
			rt["domainStrategy"] = "IPOnDemand"
		}
	}
}

// strangersTo says where an inbound sends connections that are not its users' - its REALITY site
// and its fallbacks - when that is one of the reserved loopback ports (anything but a public
// address on such a port counts: a name can resolve to loopback).
func strangersTo(obj map[string]any, reserved []int) string {
	var dests []any
	if ss, ok := obj["streamSettings"].(map[string]any); ok {
		if rs, ok := ss["realitySettings"].(map[string]any); ok {
			dests = append(dests, rs["target"], rs["dest"])
		}
	}
	if st, ok := obj["settings"].(map[string]any); ok {
		if p, _ := obj["protocol"].(string); p == "dokodemo-door" || p == "tunnel" { // forwards everyone
			if port, ok := st["port"]; ok {
				addr, _ := st["address"].(string)
				if addr == "" {
					addr = "127.0.0.1"
				}
				dests = append(dests, net.JoinHostPort(addr, fmt.Sprint(port)))
			}
		}
		if fbs, ok := st["fallbacks"].([]any); ok {
			for _, fb := range fbs {
				if m, ok := fb.(map[string]any); ok {
					dests = append(dests, m["dest"])
				}
			}
		}
	}
	for _, d := range dests {
		host, port := "127.0.0.1", 0
		switch v := d.(type) {
		case float64: // a port on this host
			port = int(v)
		case json.Number:
			n, _ := v.Int64()
			port = int(n)
		case string:
			if n, err := strconv.Atoi(v); err == nil {
				port = n
			} else if h, ps, err := net.SplitHostPort(v); err == nil {
				host = h
				port, _ = strconv.Atoi(ps)
			}
		}
		if port == 0 || !slices.Contains(reserved, port) {
			continue
		}
		if a, err := netip.ParseAddr(host); err == nil && a.IsGlobalUnicast() && !a.IsPrivate() {
			continue
		}
		return net.JoinHostPort(host, strconv.Itoa(port))
	}
	return ""
}

// userless says whether an inbound's users cannot change while it runs (SOCKS5 and HTTP keep them in
// settings.accounts, which Xray's API cannot edit): any user change re-opens the inbound.
func userless(obj map[string]any) bool {
	p, _ := obj["protocol"].(string)
	return p == "socks" || p == "http"
}

// usersKey is where an inbound keeps its users.
func usersKey(obj map[string]any) string {
	if userless(obj) {
		return "accounts"
	}
	return "clients"
}

// withClients returns a copy of an inbound with its users set (settings.clients, or
// settings.accounts for SOCKS5 and HTTP).
func withClients(obj map[string]any, clients []any) map[string]any {
	out := map[string]any{}
	for k, v := range obj {
		out[k] = v
	}
	settings := map[string]any{}
	if s, ok := obj["settings"].(map[string]any); ok {
		for k, v := range s {
			settings[k] = v
		}
	}
	if clients == nil {
		clients = []any{}
	}
	settings[usersKey(obj)] = clients
	out["settings"] = settings
	return out
}

func marshal(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	return b.Bytes()
}

func same(a, b any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

// ---------------------------------------------------------------- apply

// ApplyResult says what happened.
type ApplyResult struct {
	Pending []string // changes waiting for a confirmed restart
	Changed bool
}

// Apply makes the running Xray match d. version is the core version to install on first start;
// an installed Xray is only upgraded by an explicit action. Inbounds on an address the host does not
// have are left out (and reported), so the others keep running.
func (e *Engine) Apply(ctx context.Context, d *proto.Xray, version, mirror string, accessLog, allowRestart bool) (ApplyResult, error) {
	if d != nil && len(d.Inbounds) > 0 {
		if err := e.Install(ctx, version, mirror); err != nil {
			return ApplyResult{}, fmt.Errorf("install Xray %s: %w", version, err)
		}
	}
	var missing []string
	res, err := e.apply(ctx, d, version, mirror, accessLog, allowRestart, &missing)
	if len(missing) > 0 {
		err = errors.Join(err, errors.New(strings.Join(missing, "; ")))
	}
	return res, err
}

func (e *Engine) apply(ctx context.Context, d *proto.Xray, version, mirror string, accessLog, allowRestart bool, missing *[]string) (ApplyResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	var res ApplyResult
	if d == nil {
		d = &proto.Xray{}
	}
	e.desired = d
	e.logOn = accessLog
	if !e.Installed() {
		if len(d.Inbounds) == 0 {
			return res, nil // nothing needs Xray here yet
		}
		if err := e.install(ctx, version, mirror); err != nil {
			return res, fmt.Errorf("install Xray %s: %w", version, err)
		}
	}
	next, err := e.render(d, accessLog)
	if err != nil {
		return res, err
	}
	*missing = next.missing
	e.aliases = next.aliases
	body := marshal(next.full)
	if err := e.validate(body); err != nil {
		return res, err
	}
	if err := os.MkdirAll(e.RunDir, 0o700); err != nil {
		return res, err
	}

	running := service.IsActive(Unit)
	if !running {
		if err := e.writeConfig(body); err != nil {
			return res, err
		}
		if err := e.writeUnit(); err != nil {
			return res, err
		}
		if err := service.EnableNow(Unit); err != nil {
			return res, err
		}
		e.waitAPI(ctx)
		res.Changed = true
		e.event("core_started", "info", "Xray started")
		return res, nil
	}

	// baseline: what the config file on disk says is running
	var prev *rendered
	var oldFull map[string]any
	if old, err := os.ReadFile(e.configPath()); err == nil {
		if bytes.Equal(old, body) {
			// nothing changed on paper; still verify the live users and inbounds
			return res, e.reconcile(ctx, next)
		}
		if json.Unmarshal(old, &oldFull) == nil {
			prev = parseFull(oldFull)
		}
	}
	if prev == nil {
		prev = &rendered{inbounds: map[string]map[string]any{}, clients: map[string]map[string]proto.XrayClient{},
			outbounds: map[string]any{}, rest: next.rest}
	}

	restPending := !same(prev.rest, next.rest)
	e.waiting = restPending && !allowRestart
	if restPending {
		if allowRestart {
			if err := e.writeConfig(body); err != nil {
				return res, err
			}
			if err := service.Restart(Unit); err != nil {
				return res, err
			}
			e.waitAPI(ctx)
			e.event("core_restarted", "warn", "Xray restarted on request to apply settings")
			res.Changed = true
			return res, nil
		}
		res.Pending = append(res.Pending, "global Xray settings changed")
	}
	// the routing settings Xray runs with until a restart: rules applied live go with its balancers
	runs := next.rest
	if restPending {
		runs = prev.rest
	}
	balancers := balancersIn(runs)

	var errs []string
	note := func(err error) {
		if err != nil {
			errs = append(errs, err.Error())
		}
	}

	// outbounds before inbounds and rules, so new rules never point at a missing outbound
	// in configuration order: re-adding "direct" (the first) before any new outbound keeps it the
	// default one. Each is removed first even when the file does not list it: after a pass that
	// failed half-way it may run already.
	outsOK, rulesOK := true, true
	for _, tag := range next.outOrder {
		ob := next.outbounds[tag]
		if old, ok := prev.outbounds[tag]; ok && same(old, ob) {
			continue
		}
		note(ignoreNotFound(e.api.RemoveOutbound(ctx, tag)))
		if err := e.cli(ctx, "ado", map[string]any{"outbounds": []any{ob}}); err != nil {
			note(err)
			outsOK = false
		}
		res.Changed = true
	}
	if !same(prev.rules, next.rules) {
		// Xray replaces its rules and balancers together and stops at the first rule it cannot
		// build, so the running balancers always go along; a rule for a balancer that runs only after
		// the restart waits for it (the old rules keep running)
		if tag := missingBalancer(next.rules, balancers); tag != "" {
			res.Pending = append(res.Pending, fmt.Sprintf("routing rules that use the new balancer %q", tag))
			e.waiting = true
			rulesOK = false
		} else {
			rt := map[string]any{"rules": next.rules}
			if len(balancers) > 0 {
				rt["balancers"] = balancers
			}
			if err := e.cli(ctx, "adrules", map[string]any{"routing": rt}); err != nil {
				note(err)
				rulesOK = false
			}
		}
		res.Changed = true
	}

	for tag := range prev.inbounds {
		if _, ok := next.inbounds[tag]; !ok {
			note(ignoreNotFound(e.api.RemoveInbound(ctx, tag)))
			res.Changed = true
		}
	}
	for _, in := range d.Inbounds {
		tag := in.Tag
		if _, ok := next.inbounds[tag]; !ok {
			continue // left out (its address is missing); removed above if it ran
		}
		old, existed := prev.inbounds[tag]
		if !existed || !same(old, next.inbounds[tag]) {
			if existed {
				note(ignoreNotFound(e.api.RemoveInbound(ctx, tag)))
			}
			note(e.addInbound(ctx, next, tag))
			res.Changed = true
			continue
		}
		if userless(next.inbounds[tag]) {
			// SOCKS5 / HTTP: users cannot change live; re-open this inbound only when they differ
			if !sameUsers(prev.clients[tag], next.clients[tag]) {
				note(ignoreNotFound(e.api.RemoveInbound(ctx, tag)))
				note(e.addInbound(ctx, next, tag))
				res.Changed = true
			}
			continue
		}
		changed, err := e.syncUsers(ctx, tag, prev.clients[tag], next.clients[tag])
		note(err)
		res.Changed = res.Changed || changed
	}

	for tag := range prev.outbounds {
		if _, ok := next.outbounds[tag]; !ok {
			if err := ignoreNotFound(e.api.RemoveOutbound(ctx, tag)); err != nil {
				note(err)
				outsOK = false
			}
			res.Changed = true
		}
	}

	// on disk, what waits for the restart stays as Xray runs it (a crash restart runs what ran, and the
	// next check still sees the restart pending), and what could not be applied live stays as it was,
	// so the next pass tries again and keeps saying why instead of taking it as done
	onDisk := next.full
	if restPending {
		onDisk = withRunning(next.full, prev.rest)
	}
	if !outsOK || !rulesOK {
		onDisk = keepRunning(onDisk, oldFull, !outsOK, !rulesOK)
	}
	if err := e.writeConfig(marshal(onDisk)); err != nil {
		return res, err
	}
	note(e.reconcile(ctx, next))
	if len(errs) > 0 {
		return res, errors.New(strings.Join(uniq(errs), "; "))
	}
	return res, nil
}

func parseFull(full map[string]any) *rendered {
	r := &rendered{inbounds: map[string]map[string]any{}, clients: map[string]map[string]proto.XrayClient{},
		outbounds: map[string]any{}, rest: map[string]any{}}
	if ins, ok := full["inbounds"].([]any); ok {
		for _, x := range ins {
			m, ok := x.(map[string]any)
			if !ok {
				continue
			}
			tag, _ := m["tag"].(string)
			if operatorOwn(tag) {
				// kept whole, users included, as render keeps it: it is re-opened only when it changes
				r.inbounds[tag] = m
				r.clients[tag] = map[string]proto.XrayClient{}
				continue
			}
			stripped := map[string]any{}
			for k, v := range m {
				stripped[k] = v
			}
			cm := map[string]proto.XrayClient{}
			key := usersKey(m)
			if s, ok := m["settings"].(map[string]any); ok {
				cp := map[string]any{}
				for k, v := range s {
					if k == key {
						if list, ok := v.([]any); ok {
							for _, c := range list {
								cmap, _ := c.(map[string]any)
								email, _ := cmap["email"].(string)
								if key == "accounts" {
									email, _ = cmap["user"].(string)
								}
								raw, _ := json.Marshal(c)
								cm[email] = proto.XrayClient{Email: email, JSON: raw}
							}
						}
						continue
					}
					cp[k] = v
				}
				stripped["settings"] = cp
			}
			r.inbounds[tag] = stripped
			r.clients[tag] = cm
		}
	}
	if obs, ok := full["outbounds"].([]any); ok {
		for _, o := range obs {
			if m, ok := o.(map[string]any); ok {
				tag, _ := m["tag"].(string)
				r.outbounds[tag] = m
			}
		}
	}
	for k, v := range full {
		switch k {
		case "inbounds", "outbounds":
		case "routing":
			if rt, ok := v.(map[string]any); ok {
				r.rules = rt["rules"]
				cp := map[string]any{}
				for k2, v2 := range rt {
					if k2 != "rules" {
						cp[k2] = v2
					}
				}
				r.rest[k] = cp
			}
		default:
			r.rest[k] = v
		}
	}
	return r
}

// syncUsers adds and removes clients of one inbound, live.
func (e *Engine) syncUsers(ctx context.Context, tag string, old, next map[string]proto.XrayClient) (bool, error) {
	changed := false
	var errs []string
	for email, c := range old {
		n, keep := next[email]
		if keep && same(rawJSON(c.JSON), rawJSON(n.JSON)) {
			continue
		}
		if err := ignoreNotFound(e.api.RemoveUser(ctx, tag, email)); err != nil {
			errs = append(errs, err.Error())
		}
		changed = true
	}
	for email, c := range next {
		if o, ok := old[email]; ok && same(rawJSON(o.JSON), rawJSON(c.JSON)) {
			continue
		}
		if err := e.api.AddUser(ctx, tag, c); err != nil && !IsExists(err) {
			errs = append(errs, fmt.Sprintf("add %s to %s: %v", email, tag, err))
		}
		changed = true
	}
	if len(errs) > 0 {
		return changed, errors.New(strings.Join(errs, "; "))
	}
	return changed, nil
}

// sameUsers compares two user sets by their JSON.
func sameUsers(a, b map[string]proto.XrayClient) bool {
	if len(a) != len(b) {
		return false
	}
	for k, x := range a {
		y, ok := b[k]
		if !ok || !same(rawJSON(x.JSON), rawJSON(y.JSON)) {
			return false
		}
	}
	return true
}

func rawJSON(b json.RawMessage) any {
	var v any
	_ = json.Unmarshal(b, &v)
	return v
}

// reconcile compares the live process with the desired state and fixes the difference: inbounds
// that are missing are added, users that are missing are added, users that should not be there are
// removed. It never restarts anything.
func (e *Engine) reconcile(ctx context.Context, next *rendered) error {
	tags, err := e.api.InboundTags(ctx)
	if err != nil {
		return fmt.Errorf("read live inbounds: %w", err)
	}
	live := map[string]bool{}
	for _, t := range tags {
		live[t] = true
	}
	var errs []string
	for tag := range next.inbounds {
		if !live[tag] {
			if err := e.addInbound(ctx, next, tag); err != nil {
				errs = append(errs, err.Error())
			}
			continue
		}
		if operatorOwn(tag) { // its users are the operator's, written in its configuration
			continue
		}
		emails, err := e.api.InboundUsers(ctx, tag)
		if err != nil {
			continue
		}
		have := map[string]bool{}
		for _, m := range emails {
			have[m] = true
			if _, ok := next.clients[tag][m]; !ok && m != "" {
				if err := ignoreNotFound(e.api.RemoveUser(ctx, tag, m)); err != nil {
					errs = append(errs, err.Error())
				}
			}
		}
		for email, c := range next.clients[tag] {
			if !have[email] {
				if err := e.api.AddUser(ctx, tag, c); err != nil && !IsExists(err) {
					errs = append(errs, fmt.Sprintf("add %s to %s: %v", email, tag, err))
				}
			}
		}
	}
	for _, t := range tags {
		if _, want := next.inbounds[t]; !want && strings.HasPrefix(t, "n") {
			if err := ignoreNotFound(e.api.RemoveInbound(ctx, t)); err != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(uniq(errs), "; "))
	}
	return nil
}

// addInbound adds one inbound with its clients through `xray api adi`, so the running binary parses
// its own JSON.
func (e *Engine) addInbound(ctx context.Context, next *rendered, tag string) error {
	var clients []any
	keys := make([]string, 0, len(next.clients[tag]))
	for k := range next.clients[tag] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var c any
		_ = json.Unmarshal(next.clients[tag][k].JSON, &c)
		clients = append(clients, c)
	}
	in := next.inbounds[tag]
	if !operatorOwn(tag) {
		in = withClients(in, clients)
	}
	if err := e.cli(ctx, "adi", map[string]any{"inbounds": []any{in}}); err != nil {
		return fmt.Errorf("start %s: %w", tag, err)
	}
	return nil
}

// cli runs `xray api <cmd>` with a JSON config file.
func (e *Engine) cli(ctx context.Context, cmd string, cfg any) error {
	f, err := os.CreateTemp(e.RunDir, "api-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(marshal(cfg)); err != nil {
		f.Close()
		return err
	}
	f.Close()
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	c := exec.CommandContext(cctx, e.bin(), "api", cmd, "--server=127.0.0.1:"+strconv.Itoa(e.APIPort), f.Name())
	c.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+e.currentDir())
	out, err := c.CombinedOutput()
	s := strings.TrimSpace(string(out))
	// Xray logs warnings through its "common/errors" package (e.g. that VMess is deprecated), so the
	// word "error" in the output means nothing: the exit status and "failed ..." lines do.
	if f := failedLine(s); err != nil || f != "" {
		if f == "" {
			f = lastLine(s)
		}
		return fmt.Errorf("xray api %s: %s", cmd, f)
	}
	return nil
}

// failedLine is the first line of xray api output that reports a failure, or "".
func failedLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(strings.ToLower(l), "failed") {
			return l
		}
	}
	return ""
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return s
}

func ignoreNotFound(err error) error {
	if IsNotFound(err) {
		return nil
	}
	return err
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// validate checks a config with the installed binary before anything is touched.
func (e *Engine) validate(body []byte) error {
	f, err := os.CreateTemp(e.RunDir, "test-*.json")
	if err != nil {
		if mkErr := os.MkdirAll(e.RunDir, 0o700); mkErr != nil {
			return err
		}
		if f, err = os.CreateTemp(e.RunDir, "test-*.json"); err != nil {
			return err
		}
	}
	defer os.Remove(f.Name())
	f.Write(body)
	f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, e.bin(), "run", "-test", "-config", f.Name())
	c.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+e.currentDir())
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("the configuration was rejected by Xray: %s", lastLine(string(out)))
	}
	return nil
}

func (e *Engine) writeConfig(body []byte) error {
	if err := os.MkdirAll(e.ConfDir, 0o700); err != nil {
		return err
	}
	tmp := e.configPath() + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, e.configPath())
}

func (e *Engine) writeUnit() error {
	_, err := service.Define(service.Spec{
		Name:        Unit,
		Description: "Meridian Xray",
		Exec:        e.currentDir() + "/xray",
		Args:        []string{"run", "-config", e.configPath()},
		Env:         map[string]string{"XRAY_LOCATION_ASSET": e.currentDir()},
		Caps:        []string{"CAP_NET_ADMIN", "CAP_NET_BIND_SERVICE", "CAP_NET_RAW"},
		NoFile:      1048576,
		Sandbox:     true,
	})
	return err
}

func (e *Engine) waitAPI(ctx context.Context) {
	for i := 0; i < 50; i++ {
		c, cancel := context.WithTimeout(ctx, time.Second)
		err := e.api.Ping(c)
		cancel()
		if err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Install downloads version (when no Xray is installed yet) and points "current" at it. The download
// runs before the engine is locked, so reports go on meanwhile.
func (e *Engine) Install(ctx context.Context, version, mirror string) error {
	if e.Installed() {
		return nil
	}
	dir, err := cores.EnsureXray(ctx, e.Base, version, mirror)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.Installed() {
		return nil
	}
	return e.switchTo(dir)
}

// install downloads version and points "current" at it.
func (e *Engine) install(ctx context.Context, version, mirror string) error {
	dir, err := cores.EnsureXray(ctx, e.Base, version, mirror)
	if err != nil {
		return err
	}
	return e.switchTo(dir)
}

func (e *Engine) switchTo(dir string) error {
	link := e.currentDir()
	tmp := link + ".new"
	os.Remove(tmp)
	if err := os.Symlink(dir, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

// ---------------------------------------------------------------- actions

// LogPos is where reading the access log stopped.
func (e *Engine) LogPos() sys.LogPos {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	return e.tail.pos()
}

// RestoreLog makes the first read of the access log go on from p - or, with nil (no record of where
// the last agent stopped), from its end.
func (e *Engine) RestoreLog(p *sys.LogPos) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	if p == nil {
		e.tail.atEnd = true
		return
	}
	e.tail.start = p
}

// Waiting says whether settings wait for a restart.
func (e *Engine) Waiting() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.waiting
}

// Restart restarts Xray (explicitly requested).
func (e *Engine) Restart(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	e.waiting = false
	// the restart is what applies the settings that wait for one: write the desired configuration
	if e.desired != nil {
		next, err := e.render(e.desired, e.logOn)
		if err != nil {
			return err
		}
		body := marshal(next.full)
		if err := e.validate(body); err != nil {
			return err
		}
		if err := e.writeConfig(body); err != nil {
			return err
		}
	}
	if err := service.Restart(Unit); err != nil {
		return err
	}
	e.waitAPI(ctx)
	return nil
}

// balancersIn are the routing balancers of a configuration's restart-only sections (rendered.rest).
func balancersIn(rest map[string]any) []any {
	rt, _ := rest["routing"].(map[string]any)
	list, _ := rt["balancers"].([]any)
	return list
}

// missingBalancer is a balancer a rule uses that is not among balancers ("" when none is missing).
func missingBalancer(rules any, balancers []any) string {
	have := map[string]bool{}
	for _, b := range balancers {
		if m, ok := b.(map[string]any); ok {
			tag, _ := m["tag"].(string)
			have[tag] = true
		}
	}
	list, _ := rules.([]any)
	for _, r := range list {
		m, _ := r.(map[string]any)
		if tag, _ := m["balancerTag"].(string); tag != "" && !have[tag] {
			return tag
		}
	}
	return ""
}

// keepRunning puts the outbounds and/or the routing rules back as the file had them (old), so a
// later pass applies them again.
func keepRunning(full, old map[string]any, outbounds, rules bool) map[string]any {
	if old == nil {
		return full
	}
	out := make(map[string]any, len(full))
	for k, v := range full {
		out[k] = v
	}
	if outbounds {
		out["outbounds"] = old["outbounds"]
	}
	if rules {
		rt := map[string]any{}
		if r, ok := full["routing"].(map[string]any); ok {
			for k, v := range r {
				rt[k] = v
			}
		}
		delete(rt, "rules")
		if r, ok := old["routing"].(map[string]any); ok && r["rules"] != nil {
			rt["rules"] = r["rules"]
		}
		out["routing"] = rt
	}
	return out
}

// withRunning is the desired configuration with the sections that need a restart (everything but
// inbounds, outbounds and routing rules, which apply live) as Xray runs them now.
func withRunning(full map[string]any, running map[string]any) map[string]any {
	out := map[string]any{"inbounds": full["inbounds"], "outbounds": full["outbounds"]}
	for k, v := range running {
		if k != "routing" {
			out[k] = v
		}
	}
	rt := map[string]any{}
	if old, ok := running["routing"].(map[string]any); ok {
		for k, v := range old {
			rt[k] = v
		}
	}
	if nr, ok := full["routing"].(map[string]any); ok && nr["rules"] != nil {
		rt["rules"] = nr["rules"]
	}
	out["routing"] = rt
	return out
}

// Upgrade installs version, checks the current config with it, switches and restarts. If the new
// version does not come up, it switches back.
func (e *Engine) Upgrade(ctx context.Context, version, mirror string) (string, error) {
	// downloaded before the engine is locked: reports go on meanwhile
	dir, err := cores.EnsureXray(ctx, e.Base, version, mirror)
	if err != nil {
		return "", err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	old, _ := os.Readlink(e.currentDir())
	body, err := os.ReadFile(e.configPath())
	if err != nil {
		return "", err
	}
	c := exec.CommandContext(ctx, filepath.Join(dir, "xray"), "run", "-test", "-config", e.configPath())
	c.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+dir)
	if out, err := c.CombinedOutput(); err != nil {
		return "", fmt.Errorf("the current configuration is rejected by Xray %s: %s", version, lastLine(string(out)))
	}
	_ = body
	if err := e.switchTo(dir); err != nil {
		return "", err
	}
	if err := service.Restart(Unit); err != nil {
		return "", err
	}
	time.Sleep(1500 * time.Millisecond)
	if !service.IsActive(Unit) && old != "" {
		_ = e.switchTo(old)
		_ = service.Restart(Unit)
		return "", fmt.Errorf("upgraded Xray %s did not stay up; rolled back", version)
	}
	e.waitAPI(ctx)
	cores.Prune(e.Base, "xray", version, filepath.Base(old))
	return "Xray upgraded to " + version, nil
}

// Remove stops Xray and deletes its unit and config (decommission).
func (e *Engine) Remove() {
	e.mu.Lock()
	defer e.mu.Unlock()
	service.Remove(Unit)
	service.Undefine(Unit)
	os.RemoveAll(e.ConfDir)
}

// ---------------------------------------------------------------- collection

// Collected is what one collection pass found.
type Collected struct {
	Traffic []proto.UserTraffic
	Online  []proto.OnlineUser
	IPs     []proto.IPSeen
	Dests   []proto.DestSeen
	Status  proto.CoreStatus
}

// Collect reads and resets the traffic counters, the online lists and the access log.
func (e *Engine) Collect(ctx context.Context, connLog, destLog bool) Collected {
	var out Collected
	// an apply or upgrade that holds the engine longer (a restart waiting for Xray's API) must not hold
	// the report back until the server counts as offline: it then goes with Xray's state only, and
	// Xray keeps its counters for the next one
	if !lockWithin(&e.mu, collectWait) {
		if e.Installed() {
			active := service.IsActive(Unit)
			pid, since := service.Status(Unit)
			out.Status = proto.CoreStatus{Running: active, PID: pid, Since: since, Version: e.Version()}
			if !active {
				out.Status.Error = "not running"
			}
		}
		return out
	}
	defer e.mu.Unlock()
	e.init()
	if !e.Installed() {
		return out
	}
	active := service.IsActive(Unit)
	pid, since := service.Status(Unit)
	out.Status = proto.CoreStatus{Running: active, PID: pid, Since: since, Version: e.Version()}
	if e.lastPID != 0 && pid != 0 && pid != e.lastPID {
		e.event("core_restarted", "warn", fmt.Sprintf("Xray process restarted (pid %d -> %d)", e.lastPID, pid))
	}
	if pid != 0 {
		e.lastPID = pid
	}
	if !active {
		out.Status.Error = "not running"
		return out
	}

	if st, err := e.api.QueryStats(ctx, "user>>>", true); err == nil {
		agg := map[[2]int64]*proto.UserTraffic{}
		for _, s := range st {
			parts := strings.Split(s.Name, ">>>")
			if len(parts) != 4 || parts[2] != "traffic" {
				continue
			}
			sub, node, ok := e.identity(parts[1])
			if !ok || s.Value == 0 {
				continue
			}
			k := [2]int64{sub, node}
			t := agg[k]
			if t == nil {
				t = &proto.UserTraffic{Sub: sub, Node: node}
				agg[k] = t
			}
			if parts[3] == "uplink" {
				t.Up += s.Value
			} else {
				t.Down += s.Value
			}
		}
		for _, t := range agg {
			out.Traffic = append(out.Traffic, *t)
		}
	} else {
		out.Status.Error = "stats unavailable: " + err.Error()
	}

	now := time.Now().Unix()
	seen := map[string]bool{}
	if names, err := e.api.OnlineUsers(ctx); err == nil {
		for _, name := range names {
			sub, node, ok := e.identity(name)
			if !ok {
				continue
			}
			ips, err := e.api.OnlineIPs(ctx, name)
			if err != nil || len(ips) == 0 {
				continue
			}
			u := proto.OnlineUser{Sub: sub, Node: node}
			for ip, last := range ips {
				key := name + "|" + ip
				seen[key] = true
				first, ok := e.since[key]
				if !ok {
					first = now
					e.since[key] = now
				}
				u.IPs = append(u.IPs, proto.OnlineIP{IP: ip, Since: first, Last: last})
				if connLog {
					out.IPs = append(out.IPs, proto.IPSeen{Sub: sub, Node: node, IP: ip, First: first, Last: max(last, now)})
				}
			}
			sort.Slice(u.IPs, func(i, j int) bool { return u.IPs[i].Since < u.IPs[j].Since })
			out.Online = append(out.Online, u)
		}
	}
	for k := range e.since {
		if !seen[k] {
			delete(e.since, k)
		}
	}

	e.tail.read(func(line string) {
		if !connLog && !destLog {
			return
		}
		if ent, ok := ParseAccess(line); ok {
			if sub, node, ok := e.accessIdentity(ent); ok {
				e.agg.add(ent, sub, node, connLog, destLog)
			}
		}
	})
	{
		for _, v := range e.agg.IPs {
			out.IPs = append(out.IPs, *v)
		}
		for _, v := range e.agg.Dests {
			out.Dests = append(out.Dests, *v)
		}
		e.agg = newAggregate()
	}
	return out
}

// collectWait is how long a report waits for the engine.
var collectWait = 5 * time.Second

// lockWithin takes mu if it is free within d.
func lockWithin(mu *sync.Mutex, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for !mu.TryLock() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
	return true
}

// DesiredEqual reports whether d is what was last applied (used to skip redundant work).
func (e *Engine) DesiredEqual(d *proto.Xray) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return reflect.DeepEqual(e.desired, d)
}

var _ = slog.Info

// operatorOwn says whether an inbound comes from the operator's own code rather than from a protocol
// of the panel ("n12"): its users are the ones written in it.
func operatorOwn(tag string) bool {
	_, ok := proto.ParseInboundTag(tag)
	return !ok
}
