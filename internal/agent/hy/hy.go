// Package hy runs Hysteria2 nodes with the official Hysteria server. Users authenticate against
// the agent over HTTP, so adding, removing or pausing a user never restarts the server. Traffic per
// user, online devices and per-destination bytes come from the server's stats API.
package hy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"meridian/internal/agent/cores"
	"meridian/internal/agent/service"
	"meridian/internal/agent/sys"
	"meridian/internal/proto"
)

const template = "meridian-hy2@"

func unitName(id int64) string { return service.Instance(template, id) }

type Engine struct {
	Base     string
	ConfDir  string
	RunDir   string
	AuthPort int
	Events   func(kind, level, msg string)
	HasAddr  func(netip.Addr) bool // nil: the host's interfaces (see sys.MissingAddr)

	mu      sync.Mutex
	nodes   map[int64]proto.HyNode
	users   map[int64]map[string]string // node -> password -> id
	ports   map[int64]portInfo          // node -> stats API
	auths   []authHit
	devices map[string]map[string]int64 // node/id -> ip -> last auth (unix)
	streams map[string][2]int64         // node/conn/stream -> tx, rx
	srv     *http.Server
	lastPID map[int64]int
	logOff  map[int64]int64
	logID   map[int64]uint64 // the request log each offset is in (its inode)
	pending map[int64]bool   // nodes whose new configuration waits for a restart (see inputKey)
	// no record of where the last agent stopped reading: the logs as they are were read already
	adoptLogs bool
}

// LogPos is where reading each node's request log stopped.
func (e *Engine) LogPos() map[int64]sys.LogPos {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[int64]sys.LogPos{}
	for id, off := range e.logOff {
		out[id] = sys.LogPos{ID: e.logID[id], Off: off}
	}
	return out
}

// RestoreLogs makes reading go on where the last agent stopped - or, with nil (no record), at the end
// of each log as it is now.
func (e *Engine) RestoreLogs(pos map[int64]sys.LogPos) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	if pos == nil {
		e.adoptLogs = true
		return
	}
	for id, p := range pos {
		e.logOff[id], e.logID[id] = p.Off, p.ID
	}
}

type destKey struct {
	sub, node int64
	host      string
	port      int
}

type portInfo struct {
	Port   int    `json:"port"`
	Secret string `json:"secret"`
}

type authHit struct {
	node int64
	id   string
	ip   string
	ts   int64
}

func (e *Engine) init() {
	if e.nodes == nil {
		e.nodes = map[int64]proto.HyNode{}
		e.users = map[int64]map[string]string{}
		e.devices = map[string]map[string]int64{}
		e.streams = map[string][2]int64{}
		e.lastPID = map[int64]int{}
		e.ports = map[int64]portInfo{}
		e.logOff = map[int64]int64{}
		e.logID = map[int64]uint64{}
		e.pending = map[int64]bool{}
		if b, err := os.ReadFile(filepath.Join(e.ConfDir, "ports.json")); err == nil {
			_ = json.Unmarshal(b, &e.ports)
		}
	}
}

// reserved are the loopback control ports nothing may send strangers to: the Xray API (one below the
// auth hook), the auth hook and the stats APIs. The caller holds no lock.
func (e *Engine) reserved() []int {
	return append([]int{e.AuthPort - 1}, e.LocalPorts()...)
}

// LocalPorts are the loopback ports this engine serves or talks to (user auth, stats APIs). The
// agent lets only root reach them.
func (e *Engine) LocalPorts() []int {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := []int{e.AuthPort}
	for _, pi := range e.ports {
		if pi.Port > 0 {
			out = append(out, pi.Port)
		}
	}
	return out
}

func (e *Engine) savePorts() {
	b, _ := json.Marshal(e.ports)
	_ = os.WriteFile(filepath.Join(e.ConfDir, "ports.json"), b, 0o600)
}

// ---------------------------------------------------------------- auth endpoint

func (e *Engine) startAuth() error {
	if e.srv != nil {
		return nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /hy2/{node}/auth", func(w http.ResponseWriter, r *http.Request) {
		node, _ := strconv.ParseInt(r.PathValue("node"), 10, 64)
		var req struct {
			Addr string `json:"addr"`
			Auth string `json:"auth"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)
		e.mu.Lock()
		id, ok := e.users[node][req.Auth]
		if ok {
			ip := req.Addr
			if h, _, err := net.SplitHostPort(req.Addr); err == nil {
				ip = h
			}
			if a, err := netip.ParseAddr(ip); err == nil {
				ip = a.Unmap().String()
			}
			now := time.Now().Unix()
			e.auths = append(e.auths, authHit{node: node, id: id, ip: ip, ts: now})
			if len(e.auths) > 10000 {
				e.auths = e.auths[len(e.auths)-10000:]
			}
			k := fmt.Sprintf("%d/%s", node, id)
			if e.devices[k] == nil {
				e.devices[k] = map[string]int64{}
			}
			e.devices[k][ip] = now
		}
		e.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if ok {
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": id})
		} else {
			json.NewEncoder(w).Encode(map[string]any{"ok": false})
		}
	})
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(e.AuthPort))
	if err != nil {
		return fmt.Errorf("auth endpoint: %w", err)
	}
	e.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go e.srv.Serve(l)
	return nil
}

// ---------------------------------------------------------------- apply

func freeLocalPort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// privateRejects keep Hysteria2 users away from this host's loopback services and from private,
// link-local and metadata addresses. They come first whatever a protocol's own acl says: Hysteria
// resolves a name before it matches the rules and then dials the address it matched, so a name that
// leads there is refused too.
var privateRejects = []string{"reject(0.0.0.0/8)", "reject(127.0.0.0/8)", "reject(10.0.0.0/8)", "reject(172.16.0.0/12)",
	"reject(192.168.0.0/16)", "reject(169.254.0.0/16)", "reject(100.64.0.0/10)", "reject(224.0.0.0/3)",
	"reject(::/127)", "reject(fc00::/7)", "reject(fe80::/10)", "reject(ff00::/8)"}

// guardMasquerade refuses a masquerade that hands strangers to one of the agent's loopback control
// ports (anything but a public address on such a port counts: a name can resolve to loopback).
func guardMasquerade(cfg map[string]any, reserved []int) error {
	m, _ := cfg["masquerade"].(map[string]any)
	px, _ := m["proxy"].(map[string]any)
	raw, _ := px["url"].(string)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("its masquerade address %q cannot be read", raw)
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443"}[u.Scheme]
		if port == "" {
			port = "80"
		}
	}
	pn, _ := strconv.Atoi(port)
	if !slices.Contains(reserved, pn) {
		return nil
	}
	if a, err := netip.ParseAddr(strings.Trim(u.Hostname(), "[]")); err == nil && a.IsGlobalUnicast() && !a.IsPrivate() {
		return nil
	}
	return fmt.Errorf("its masquerade would send strangers to %s, one of the agent's own control ports - choose another address", u.Host)
}

// guardACL puts the private-address rejects in front of the rules a configuration ends up with: the
// agent's own (everything else direct), or the protocol's own list. A rules file cannot be checked,
// so it is refused.
func guardACL(cfg map[string]any) error {
	acl, _ := cfg["acl"].(map[string]any)
	if acl == nil {
		acl = map[string]any{}
	}
	if _, ok := acl["file"]; ok {
		return fmt.Errorf("its own acl.file cannot be used - write the rules under acl.inline")
	}
	var rules []any
	switch l := acl["inline"].(type) {
	case []any:
		rules = l
	case []string:
		for _, r := range l {
			rules = append(rules, r)
		}
	}
	if len(rules) == 0 {
		rules = []any{"direct(all)"}
	}
	out := make([]any, 0, len(privateRejects)+len(rules))
	have := map[string]bool{}
	for _, r := range privateRejects {
		out = append(out, r)
		have[r] = true
	}
	for _, r := range rules {
		if s, ok := r.(string); !ok || !have[strings.ReplaceAll(s, " ", "")] {
			out = append(out, r)
		}
	}
	acl["inline"] = out
	cfg["acl"] = acl
	return nil
}

func (e *Engine) render(n proto.HyNode, pi portInfo) ([]byte, error) {
	dir := e.ConfDir
	// the node's own address: it listens there and its traffic leaves from there
	listen := ":" + strconv.Itoa(n.Port)
	direct := map[string]any{"mode": "46"} // IPv4 first, IPv6 when a site has no IPv4
	switch n.Mode {
	case "4", "6", "46", "64":
		direct["mode"] = n.Mode
	}
	if ip, err := netip.ParseAddr(n.Bind); err == nil && n.Bind != "" {
		listen = net.JoinHostPort(ip.String(), strconv.Itoa(n.Port))
		if ip.Is4() {
			direct["mode"], direct["bindIPv4"] = "4", ip.String()
		} else {
			direct["mode"], direct["bindIPv6"] = "6", ip.String()
		}
	}
	cfg := map[string]any{
		"listen": listen,
		"tls": map[string]any{"cert": filepath.Join(dir, fmt.Sprintf("%d.crt", n.NodeID)),
			"key": filepath.Join(dir, fmt.Sprintf("%d.key", n.NodeID))},
		"auth": map[string]any{"type": "http", "http": map[string]any{
			"url": fmt.Sprintf("http://127.0.0.1:%d/hy2/%d/auth", e.AuthPort, n.NodeID)}},
		"trafficStats": map[string]any{"listen": "127.0.0.1:" + strconv.Itoa(pi.Port), "secret": pi.Secret},
		"sniff":        map[string]any{"enable": true, "timeout": "2s"},
		"outbounds":    []any{map[string]any{"name": "direct", "type": "direct", "direct": direct}},
	}
	if n.ObfsPassword != "" {
		cfg["obfs"] = map[string]any{"type": "salamander", "salamander": map[string]any{"password": n.ObfsPassword}}
	}
	if n.UpMbps > 0 && n.DownMbps > 0 {
		cfg["bandwidth"] = map[string]any{"up": fmt.Sprintf("%d mbps", n.UpMbps), "down": fmt.Sprintf("%d mbps", n.DownMbps)}
	}
	// the operator's own configuration on top; users and traffic counting stay the agent's
	if len(n.Custom) > 0 {
		var custom map[string]any
		if err := json.Unmarshal(n.Custom, &custom); err != nil {
			return nil, fmt.Errorf("node %d: its own configuration: %w", n.NodeID, err)
		}
		for k, v := range custom {
			if k == "auth" || k == "trafficStats" {
				continue
			}
			if v == nil {
				delete(cfg, k)
				continue
			}
			cfg[k] = mergePatch(cfg[k], v)
		}
	}
	if err := guardACL(cfg); err != nil {
		return nil, fmt.Errorf("protocol n%d: %w", n.NodeID, err)
	}
	if err := guardMasquerade(cfg, e.reserved()); err != nil {
		return nil, fmt.Errorf("protocol n%d: %w", n.NodeID, err)
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// inputKey is what a node's files are made from, its users aside (they change live); the key last
// seen is kept next to them. When the files would come out different although the key is the same,
// nothing was asked for: this agent writes the configuration differently (it was upgraded), and the
// change waits for a restart someone confirms instead of dropping every device at the upgrade.
func inputKey(n proto.HyNode, pi portInfo, authPort int) string {
	n.Users = nil
	b, _ := json.Marshal(struct {
		Node proto.HyNode
		Port portInfo
		Auth int
	}{n, pi, authPort})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (e *Engine) keyPath(id int64) string { return filepath.Join(e.ConfDir, fmt.Sprintf("%d.in", id)) }

// files are a node's configuration, certificate and key as they should be on disk.
func (e *Engine) files(n proto.HyNode, body []byte) map[string][]byte {
	return map[string][]byte{
		filepath.Join(e.ConfDir, fmt.Sprintf("%d.crt", n.NodeID)):  []byte(n.CertPEM),
		filepath.Join(e.ConfDir, fmt.Sprintf("%d.key", n.NodeID)):  []byte(n.KeyPEM),
		filepath.Join(e.ConfDir, fmt.Sprintf("%d.yaml", n.NodeID)): body,
	}
}

// write puts a node's files in place, and the key of what they were made from.
func (e *Engine) write(n proto.HyNode, files map[string][]byte, key string) []string {
	var errs []string
	for path, content := range files {
		if old, err := os.ReadFile(path); err != nil || !bytes.Equal(old, content) {
			if err := os.WriteFile(path, content, 0o600); err != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	if err := os.WriteFile(e.keyPath(n.NodeID), []byte(key), 0o600); err != nil {
		errs = append(errs, err.Error())
	}
	return errs
}

// Pending lists the nodes whose configuration waits for a restart.
func (e *Engine) Pending() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var ids []int64
	for id := range e.pending {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = fmt.Sprintf("Hysteria2 protocol n%d: a safer configuration from the upgraded agent", id)
	}
	return out
}

// RestartPending writes the configuration of the nodes that wait for a restart and restarts them:
// their devices reconnect by themselves. It says how many restarted.
func (e *Engine) RestartPending(ctx context.Context) (int, error) {
	e.mu.Lock()
	e.init()
	var nodes []proto.HyNode
	for id := range e.pending {
		if n, ok := e.nodes[id]; ok {
			nodes = append(nodes, n)
		}
	}
	e.mu.Unlock()
	var errs []string
	done := 0
	for _, n := range nodes {
		e.mu.Lock()
		pi := e.ports[n.NodeID]
		e.mu.Unlock()
		body, err := e.render(n, pi)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		errs = append(errs, e.write(n, e.files(n, body), inputKey(n, pi, e.AuthPort))...)
		if err := service.Restart(unitName(n.NodeID)); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		e.mu.Lock()
		delete(e.pending, n.NodeID)
		e.mu.Unlock()
		done++
	}
	if len(errs) > 0 {
		return done, errors.New(strings.Join(errs, "; "))
	}
	return done, nil
}

// Apply runs exactly the given nodes. User changes only update the in-memory table the auth
// endpoint reads; a node's server restarts only when its own settings (port, certificate, obfs)
// change.
func (e *Engine) Apply(ctx context.Context, nodes []proto.HyNode, version, mirror string) error {
	e.mu.Lock()
	e.init()
	e.mu.Unlock()
	var errs []string
	if len(nodes) > 0 {
		if err := os.MkdirAll(e.ConfDir, 0o700); err != nil {
			return err
		}
		bin, err := cores.InUse(ctx, e.Base, "hysteria", version, mirror)
		if err != nil {
			return fmt.Errorf("install Hysteria %s: %w", version, err)
		}
		unit := service.Spec{
			Name:        template,
			Description: "Meridian Hysteria2 node %i",
			Exec:        bin,
			Args:        []string{"server", "-c", e.ConfDir + "/%i.yaml"},
			Env:         map[string]string{"HYSTERIA_LOG_LEVEL": "debug", "HYSTERIA_LOG_FORMAT": "json"},
			Log:         e.RunDir + "/hy2-%i.log",
			Caps:        []string{"CAP_NET_ADMIN", "CAP_NET_BIND_SERVICE", "CAP_NET_RAW"},
			NoFile:      1048576,
			Sandbox:     true,
		}
		if _, err := service.Define(unit); err != nil {
			return err
		}
		e.mu.Lock()
		err = e.startAuth()
		e.mu.Unlock()
		if err != nil {
			return err
		}
	}

	want := map[int64]proto.HyNode{}
	for _, n := range nodes {
		want[n.NodeID] = n
	}
	// users first: new users can authenticate before anything else happens
	var kick = map[int64][]string{}
	e.mu.Lock()
	for id, n := range want {
		next := map[string]string{}
		ids := map[string]bool{}
		for _, u := range n.Users {
			next[u.Password] = u.ID
			ids[u.ID] = true
		}
		for _, old := range e.users[id] {
			if !ids[old] {
				kick[id] = append(kick[id], old)
			}
		}
		e.users[id] = next
	}
	e.mu.Unlock()

	for id, n := range want {
		if a, gone := sys.MissingAddr(n.Bind, e.HasAddr); gone {
			// bound to an address this server does not have: it could not start, so it waits (stopped)
			// and starts by itself once the address is back
			_ = service.DisableNow(unitName(id))
			errs = append(errs, fmt.Sprintf("protocol n%d is left out: this server has no address %s (it comes back by itself when the address does)", id, a))
			continue
		}
		e.mu.Lock()
		pi, ok := e.ports[id]
		if !ok || pi.Port == 0 {
			pi = portInfo{Port: freeLocalPort(), Secret: randHex(16)}
			e.ports[id] = pi
			e.savePorts()
		}
		e.mu.Unlock()
		body, err := e.render(n, pi)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		files, key := e.files(n, body), inputKey(n, pi, e.AuthPort)
		changed := false
		for path, content := range files {
			if old, err := os.ReadFile(path); err != nil || !bytes.Equal(old, content) {
				changed = true
			}
		}
		stored, _ := os.ReadFile(e.keyPath(id))
		waits := false
		switch {
		case !service.IsActive(unitName(id)):
			errs = append(errs, e.write(n, files, key)...)
			if err := service.EnableNow(unitName(id)); err != nil {
				errs = append(errs, err.Error())
			}
		case !changed:
			if string(stored) != key {
				errs = append(errs, e.write(n, files, key)...)
			}
		case len(stored) == 0 || string(stored) == key:
			// only this agent's way of writing it changed: no restart without a click. The key is kept
			// so that a change the admin makes meanwhile still applies (and restarts) at once.
			waits = true
			if len(stored) == 0 {
				if err := os.WriteFile(e.keyPath(id), []byte(key), 0o600); err != nil {
					errs = append(errs, err.Error())
				}
			}
		default: // the admin changed this node's own settings: saving said it restarts
			errs = append(errs, e.write(n, files, key)...)
			if err := service.Restart(unitName(id)); err != nil {
				errs = append(errs, err.Error())
			}
		}
		e.mu.Lock()
		e.nodes[id] = n
		if waits {
			e.pending[id] = true
		} else {
			delete(e.pending, id)
		}
		e.mu.Unlock()
	}
	for id, ids := range kick {
		e.kick(id, ids)
	}

	// nodes that are gone
	e.mu.Lock()
	var gone []int64
	for id := range e.nodes {
		if _, ok := want[id]; !ok {
			gone = append(gone, id)
		}
	}
	e.mu.Unlock()
	entries, _ := os.ReadDir(e.ConfDir)
	for _, ent := range entries {
		if id, err := strconv.ParseInt(strings.TrimSuffix(ent.Name(), ".yaml"), 10, 64); err == nil && strings.HasSuffix(ent.Name(), ".yaml") {
			if _, ok := want[id]; !ok {
				gone = append(gone, id)
			}
		}
	}
	for _, id := range gone {
		service.Remove(unitName(id))
		for _, ext := range []string{".yaml", ".crt", ".key", ".in"} {
			os.Remove(filepath.Join(e.ConfDir, fmt.Sprintf("%d%s", id, ext)))
		}
		e.mu.Lock()
		delete(e.nodes, id)
		delete(e.users, id)
		delete(e.ports, id)
		delete(e.pending, id)
		delete(e.logOff, id)
		delete(e.logID, id)
		e.savePorts()
		e.mu.Unlock()
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// RestartAll restarts every Hysteria2 node (after an upgrade): their devices reconnect by themselves.
func (e *Engine) RestartAll() (int, error) {
	e.mu.Lock()
	var ids []int64
	for id := range e.nodes {
		ids = append(ids, id)
	}
	e.mu.Unlock()
	var errs []string
	for _, id := range ids {
		if err := service.Restart(unitName(id)); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return len(ids) - len(errs), errors.New(strings.Join(errs, "; "))
	}
	return len(ids), nil
}

// kick ends the sessions of removed users (they were paused or deleted on purpose).
//
// Hysteria remembers a kick until the user's next traffic, so kicking someone who is not online
// would cut their first connection after a resume. Only users that are online right now are
// kicked; a second look a moment later catches a session that was just being set up.
func (e *Engine) kick(node int64, ids []string) {
	if len(ids) == 0 {
		return
	}
	e.kickOnline(node, ids)
	go func() {
		time.Sleep(3 * time.Second)
		e.mu.Lock()
		var still []string
		for _, id := range ids {
			if !e.hasUser(node, id) { // not re-added meanwhile (e.g. resumed again)
				still = append(still, id)
			}
		}
		e.mu.Unlock()
		e.kickOnline(node, still)
	}()
}

// hasUser reports whether id may currently authenticate on node. Callers hold e.mu.
func (e *Engine) hasUser(node int64, id string) bool {
	for _, u := range e.users[node] {
		if u == id {
			return true
		}
	}
	return false
}

func (e *Engine) kickOnline(node int64, ids []string) {
	if len(ids) == 0 {
		return
	}
	e.mu.Lock()
	pi := e.ports[node]
	e.mu.Unlock()
	b, err := e.call(pi, http.MethodGet, "/online", nil)
	if err != nil {
		return
	}
	var online map[string]int
	if json.Unmarshal(b, &online) != nil {
		return
	}
	var now []string
	for _, id := range ids {
		if online[id] > 0 {
			now = append(now, id)
		}
	}
	if len(now) == 0 {
		return
	}
	body, _ := json.Marshal(now)
	_, _ = e.call(pi, http.MethodPost, "/kick", body)
}

func (e *Engine) call(pi portInfo, method, path string, body []byte) ([]byte, error) {
	if pi.Port == 0 {
		return nil, fmt.Errorf("no stats port")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://127.0.0.1:%d%s", pi.Port, path), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", pi.Secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hysteria stats: %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}

// Remove stops every node (decommission).
func (e *Engine) Remove() {
	_ = e.Apply(context.Background(), nil, "", "")
	service.Undefine(template)
	if e.srv != nil {
		e.srv.Close()
	}
}

// Ports lists the UDP ports in use.
func Ports(nodes []proto.HyNode) []int {
	var out []int
	for _, n := range nodes {
		out = append(out, n.Port)
	}
	return out
}

// ---------------------------------------------------------------- collection

type Collected struct {
	Traffic []proto.UserTraffic
	Online  []proto.OnlineUser
	IPs     []proto.IPSeen
	Dests   []proto.DestSeen
	Status  map[int64]proto.CoreStatus
}

// pruneDevices bounds the sign-in memory: addresses unused for a week go, and each user keeps the
// 32 most recent. The caller holds e.mu.
func (e *Engine) pruneDevices(now int64) {
	for k, devs := range e.devices {
		for ip, t := range devs {
			if now-t > 7*86400 {
				delete(devs, ip)
			}
		}
		if len(devs) > 32 {
			ts := make([]int64, 0, len(devs))
			for _, t := range devs {
				ts = append(ts, t)
			}
			sort.Slice(ts, func(i, j int) bool { return ts[i] > ts[j] })
			cut := ts[31]
			for ip, t := range devs {
				if t < cut {
					delete(devs, ip)
				}
			}
		}
		if len(devs) == 0 {
			delete(e.devices, k)
		}
	}
}

// Collect reads and clears per-user traffic, and reports online devices and destinations.
func (e *Engine) Collect(connLog, destLog bool) Collected {
	e.mu.Lock()
	e.init()
	nodes := make(map[int64]proto.HyNode, len(e.nodes))
	for k, v := range e.nodes {
		nodes[k] = v
	}
	ports := make(map[int64]portInfo, len(e.ports))
	for k, v := range e.ports {
		ports[k] = v
	}
	hits := e.auths
	e.auths = nil
	e.pruneDevices(time.Now().Unix())
	e.mu.Unlock()

	out := Collected{Status: map[int64]proto.CoreStatus{}}
	now := time.Now().Unix()
	if connLog {
		agg := map[string]*proto.IPSeen{}
		for _, h := range hits {
			sub, node, ok := proto.ParseEmail(h.id)
			if !ok {
				continue
			}
			k := fmt.Sprintf("%d/%d/%s", sub, node, h.ip)
			if a := agg[k]; a != nil {
				a.Conns++
				a.Last = max(a.Last, h.ts)
			} else {
				agg[k] = &proto.IPSeen{Sub: sub, Node: node, IP: h.ip, First: h.ts, Last: h.ts, Conns: 1}
			}
		}
		for _, v := range agg {
			out.IPs = append(out.IPs, *v)
		}
	}
	for id := range nodes {
		pi := ports[id]
		active := service.IsActive(unitName(id))
		pid, since := service.Status(unitName(id))
		st := proto.CoreStatus{Running: active, PID: pid, Since: since, Version: cores.Current(e.Base, "hysteria")}
		e.mu.Lock()
		if last := e.lastPID[id]; last != 0 && pid != 0 && pid != last && e.Events != nil {
			e.Events("core_restarted", "warn", fmt.Sprintf("Hysteria2 node %d process restarted", id))
		}
		if pid != 0 {
			e.lastPID[id] = pid
		}
		e.mu.Unlock()
		if !active {
			st.Error = "not running"
			out.Status[id] = st
			continue
		}
		out.Status[id] = st

		if b, err := e.call(pi, http.MethodGet, "/traffic?clear=1", nil); err == nil {
			var m map[string]struct {
				TX int64 `json:"tx"`
				RX int64 `json:"rx"`
			}
			if json.Unmarshal(b, &m) == nil {
				for uid, t := range m {
					sub, node, ok := proto.ParseEmail(uid)
					if !ok || (t.TX == 0 && t.RX == 0) {
						continue
					}
					// Hysteria counts from the client's side: tx is the upload, rx the download
					out.Traffic = append(out.Traffic, proto.UserTraffic{Sub: sub, Node: node, Up: t.TX, Down: t.RX})
				}
			}
		}
		if b, err := e.call(pi, http.MethodGet, "/online", nil); err == nil {
			var m map[string]int
			if json.Unmarshal(b, &m) == nil {
				for uid, count := range m {
					sub, node, ok := proto.ParseEmail(uid)
					if !ok || count <= 0 {
						continue
					}
					e.mu.Lock()
					devs := e.devices[fmt.Sprintf("%d/%s", id, uid)]
					type ipT struct {
						ip string
						t  int64
					}
					var list []ipT
					for ip, t := range devs {
						list = append(list, ipT{ip, t})
					}
					e.mu.Unlock()
					// the most recent authentications are the devices still connected
					sort.Slice(list, func(i, j int) bool { return list[i].t > list[j].t })
					if len(list) > count {
						list = list[:count]
					}
					u := proto.OnlineUser{Sub: sub, Node: node}
					for _, x := range list {
						u.IPs = append(u.IPs, proto.OnlineIP{IP: x.ip, Since: x.t, Last: now})
					}
					if len(u.IPs) > 0 {
						out.Online = append(out.Online, u)
					}
				}
			}
		}
	}
	for id := range nodes {
		out.Dests = append(out.Dests, e.readLog(id, destLog)...)
	}
	e.mu.Lock()
	e.adoptLogs = false // logs met from now on are new
	e.mu.Unlock()
	return out
}

// readLog follows a node's request log: one line per proxied connection, with the user and the
// destination. It keeps the file small by starting it over once everything in it has been read.
func (e *Engine) readLog(node int64, destLog bool) []proto.DestSeen {
	path := filepath.Join(e.RunDir, fmt.Sprintf("hy2-%d.log", node))
	e.mu.Lock()
	off, known := e.logOff[node]
	wantID, adopt := e.logID[node], e.adoptLogs && !known
	e.mu.Unlock()
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	id := sys.FileID(st)
	switch {
	case adopt:
		off = st.Size() // read by the agent before this one
	case st.Size() < off || (wantID != 0 && wantID != id):
		off = 0 // started over, or another file
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil
	}
	agg := map[destKey]*proto.DestSeen{}
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			break // a partial last line is read again next time
		}
		off += int64(len(line))
		if !destLog {
			continue
		}
		var ent struct {
			Msg     string `json:"msg"`
			ID      string `json:"id"`
			ReqAddr string `json:"reqAddr"`
		}
		if json.Unmarshal([]byte(line), &ent) != nil || (ent.Msg != "TCP request" && ent.Msg != "UDP request") {
			continue
		}
		sub, nd, ok := proto.ParseEmail(ent.ID)
		if !ok {
			continue
		}
		host, port, err := net.SplitHostPort(ent.ReqAddr)
		if err != nil {
			continue
		}
		pn, _ := strconv.Atoi(port)
		nw := "tcp"
		if ent.Msg == "UDP request" {
			nw = "udp"
		}
		k := destKey{sub, nd, strings.ToLower(host) + "|" + nw, pn}
		d := agg[k]
		if d == nil {
			d = &proto.DestSeen{Sub: sub, Node: nd, Host: strings.ToLower(host), Port: pn, Net: nw}
			agg[k] = d
		}
		d.Conns++
		d.Last = time.Now().Unix()
	}
	if cur, err := os.Stat(path); err == nil && cur.Size() == off && off > 4<<20 {
		if os.Truncate(path, 0) == nil {
			off = 0
		}
	}
	e.mu.Lock()
	e.logOff[node], e.logID[node] = off, id
	e.mu.Unlock()
	out := make([]proto.DestSeen, 0, len(agg))
	for _, d := range agg {
		out = append(out, *d)
	}
	return out
}

// mergePatch applies an RFC 7386 merge patch: objects merge key by key, null removes a key, anything
// else replaces.
func mergePatch(base, patch any) any {
	pm, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	bm, ok := base.(map[string]any)
	if !ok {
		bm = map[string]any{}
	}
	out := make(map[string]any, len(bm)+len(pm))
	for k, v := range bm {
		out[k] = v
	}
	for k, v := range pm {
		if v == nil {
			delete(out, k)
			continue
		}
		out[k] = mergePatch(out[k], v)
	}
	return out
}
