// Package hy runs Hysteria2 nodes with the official Hysteria server. Users authenticate against
// the agent over HTTP, so adding, removing or pausing a user never restarts the server. Traffic per
// user, online devices and per-destination bytes come from the server's stats API.
package hy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"meridian/internal/agent/cores"
	"meridian/internal/agent/systemd"
	"meridian/internal/proto"
)

const template = "meridian-hy2@.service"

func unitName(id int64) string { return fmt.Sprintf("meridian-hy2@%d.service", id) }

type Engine struct {
	Base     string
	ConfDir  string
	RunDir   string
	AuthPort int
	Events   func(kind, level, msg string)

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
		if b, err := os.ReadFile(filepath.Join(e.ConfDir, "ports.json")); err == nil {
			_ = json.Unmarshal(b, &e.ports)
		}
	}
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

func (e *Engine) render(n proto.HyNode, pi portInfo) ([]byte, error) {
	dir := e.ConfDir
	cfg := map[string]any{
		"listen": ":" + strconv.Itoa(n.Port),
		"tls": map[string]any{"cert": filepath.Join(dir, fmt.Sprintf("%d.crt", n.NodeID)),
			"key": filepath.Join(dir, fmt.Sprintf("%d.key", n.NodeID))},
		"auth": map[string]any{"type": "http", "http": map[string]any{
			"url": fmt.Sprintf("http://127.0.0.1:%d/hy2/%d/auth", e.AuthPort, n.NodeID)}},
		"trafficStats": map[string]any{"listen": "127.0.0.1:" + strconv.Itoa(pi.Port), "secret": pi.Secret},
		"sniff":        map[string]any{"enable": true, "timeout": "2s"},
		// prefer IPv4 for outgoing connections and fall back to IPv6
		"outbounds": []any{map[string]any{"name": "direct", "type": "direct", "direct": map[string]any{"mode": "46"}}},
		"acl": map[string]any{"inline": []string{
			"reject(127.0.0.0/8)", "reject(10.0.0.0/8)", "reject(172.16.0.0/12)", "reject(192.168.0.0/16)",
			"reject(169.254.0.0/16)", "reject(100.64.0.0/10)", "reject(fc00::/7)", "reject(fe80::/10)",
			"reject(::1/128)", "direct(all)"}},
	}
	if n.ObfsPassword != "" {
		cfg["obfs"] = map[string]any{"type": "salamander", "salamander": map[string]any{"password": n.ObfsPassword}}
	}
	if n.UpMbps > 0 && n.DownMbps > 0 {
		cfg["bandwidth"] = map[string]any{"up": fmt.Sprintf("%d mbps", n.UpMbps), "down": fmt.Sprintf("%d mbps", n.DownMbps)}
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
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
		bin, err := cores.EnsureHysteria(ctx, e.Base, version, mirror)
		if err != nil {
			return fmt.Errorf("install Hysteria %s: %w", version, err)
		}
		unit := fmt.Sprintf(`[Unit]
Description=Meridian Hysteria2 node %%i
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
ExecStart=%s server -c %s/%%i.yaml
Restart=always
RestartSec=2
LimitNOFILE=1048576
Environment=HYSTERIA_LOG_LEVEL=debug
Environment=HYSTERIA_LOG_FORMAT=json
StandardOutput=append:%s/hy2-%%i.log
StandardError=append:%s/hy2-%%i.log
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_BIND_SERVICE CAP_NET_RAW
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_BIND_SERVICE CAP_NET_RAW
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=true

[Install]
WantedBy=multi-user.target
`, bin, e.ConfDir, e.RunDir, e.RunDir)
		if _, err := systemd.WriteUnit(template, unit); err != nil {
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
		changed := false
		for path, content := range map[string][]byte{
			filepath.Join(e.ConfDir, fmt.Sprintf("%d.crt", id)):  []byte(n.CertPEM),
			filepath.Join(e.ConfDir, fmt.Sprintf("%d.key", id)):  []byte(n.KeyPEM),
			filepath.Join(e.ConfDir, fmt.Sprintf("%d.yaml", id)): body,
		} {
			if old, err := os.ReadFile(path); err != nil || !bytes.Equal(old, content) {
				if err := os.WriteFile(path, content, 0o600); err != nil {
					errs = append(errs, err.Error())
				}
				changed = true
			}
		}
		switch {
		case !systemd.IsActive(unitName(id)):
			if err := systemd.EnableNow(unitName(id)); err != nil {
				errs = append(errs, err.Error())
			}
		case changed: // the admin changed this node's own settings
			if err := systemd.Restart(unitName(id)); err != nil {
				errs = append(errs, err.Error())
			}
		}
		e.mu.Lock()
		e.nodes[id] = n
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
		systemd.RemoveUnit(unitName(id))
		for _, ext := range []string{".yaml", ".crt", ".key"} {
			os.Remove(filepath.Join(e.ConfDir, fmt.Sprintf("%d%s", id, ext)))
		}
		e.mu.Lock()
		delete(e.nodes, id)
		delete(e.users, id)
		delete(e.ports, id)
		e.savePorts()
		e.mu.Unlock()
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
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
	os.Remove(filepath.Join(systemd.UnitDir, template))
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
		active := systemd.IsActive(unitName(id))
		pid, since := systemd.Status(unitName(id))
		st := proto.CoreStatus{Running: active, PID: pid, Since: since}
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
	return out
}

// readLog follows a node's request log: one line per proxied connection, with the user and the
// destination. It keeps the file small by starting it over once everything in it has been read.
func (e *Engine) readLog(node int64, destLog bool) []proto.DestSeen {
	path := filepath.Join(e.RunDir, fmt.Sprintf("hy2-%d.log", node))
	e.mu.Lock()
	off := e.logOff[node]
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
	if st.Size() < off {
		off = 0 // started over
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
	e.logOff[node] = off
	e.mu.Unlock()
	out := make([]proto.DestSeen, 0, len(agg))
	for _, d := range agg {
		out = append(out, *d)
	}
	return out
}
