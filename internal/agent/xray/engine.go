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
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"meridian/internal/agent/cores"
	"meridian/internal/agent/service"
	"meridian/internal/proto"
)

const Unit = "meridian-xray"

type Engine struct {
	Base    string // data dir, e.g. /var/lib/meridian-agent
	ConfDir string // e.g. /etc/meridian-agent/xray
	RunDir  string // e.g. /run/meridian-agent
	APIPort int
	Events  func(kind, level, msg string)

	mu      sync.Mutex
	api     *API
	tail    *accessTail
	agg     *Aggregate
	since   map[string]int64
	lastPID int
	desired *proto.Xray
	logOn   bool
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
	for _, in := range d.Inbounds {
		var obj map[string]any
		if err := json.Unmarshal(in.Config, &obj); err != nil {
			return nil, fmt.Errorf("inbound %s: %w", in.Tag, err)
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
// an installed Xray is only upgraded by an explicit action.
func (e *Engine) Apply(ctx context.Context, d *proto.Xray, version, mirror string, accessLog, allowRestart bool) (ApplyResult, error) {
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
	if old, err := os.ReadFile(e.configPath()); err == nil {
		if bytes.Equal(old, body) {
			// nothing changed on paper; still verify the live users and inbounds
			return res, e.reconcile(ctx, next)
		}
		var oldFull map[string]any
		if json.Unmarshal(old, &oldFull) == nil {
			prev = parseFull(oldFull)
		}
	}
	if prev == nil {
		prev = &rendered{inbounds: map[string]map[string]any{}, clients: map[string]map[string]proto.XrayClient{},
			outbounds: map[string]any{}, rest: next.rest}
	}

	if !same(prev.rest, next.rest) {
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

	var errs []string
	note := func(err error) {
		if err != nil {
			errs = append(errs, err.Error())
		}
	}

	// outbounds before inbounds and rules, so new rules never point at a missing outbound
	for tag, ob := range next.outbounds {
		if old, ok := prev.outbounds[tag]; ok && same(old, ob) {
			continue
		}
		if _, ok := prev.outbounds[tag]; ok {
			note(ignoreNotFound(e.api.RemoveOutbound(ctx, tag)))
		}
		note(e.cli(ctx, "ado", map[string]any{"outbounds": []any{ob}}))
		res.Changed = true
	}
	if !same(prev.rules, next.rules) {
		note(e.cli(ctx, "adrules", map[string]any{"routing": map[string]any{"rules": next.rules}}))
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
			note(ignoreNotFound(e.api.RemoveOutbound(ctx, tag)))
			res.Changed = true
		}
	}

	if err := e.writeConfig(body); err != nil {
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
	if err := e.cli(ctx, "adi", map[string]any{"inbounds": []any{withClients(next.inbounds[tag], clients)}}); err != nil {
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

// Install downloads version (when no Xray is installed yet) and points "current" at it.
func (e *Engine) Install(ctx context.Context, version, mirror string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.Installed() {
		return nil
	}
	return e.install(ctx, version, mirror)
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

// Restart restarts Xray (explicitly requested).
func (e *Engine) Restart(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	if err := service.Restart(Unit); err != nil {
		return err
	}
	e.waitAPI(ctx)
	return nil
}

// Upgrade installs version, checks the current config with it, switches and restarts. If the new
// version does not come up, it switches back.
func (e *Engine) Upgrade(ctx context.Context, version, mirror string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	old, _ := os.Readlink(e.currentDir())
	dir, err := cores.EnsureXray(ctx, e.Base, version, mirror)
	if err != nil {
		return "", err
	}
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
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	var out Collected
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

// DesiredEqual reports whether d is what was last applied (used to skip redundant work).
func (e *Engine) DesiredEqual(d *proto.Xray) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return reflect.DeepEqual(e.desired, d)
}

var _ = slog.Info
