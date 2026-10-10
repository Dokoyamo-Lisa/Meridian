// Package solo runs the protocols whose server takes one user per process - mieru (its server,
// mita), Snell (snell-server) and AnyTLS (sing-box): every user of such a protocol has their own
// process on their own port. Adding, changing or removing a user starts, restarts or stops theirs
// alone; nobody else is touched. What they use is counted per port by nftables, the devices connected
// to their port are theirs, and cutting them is stopping their process. A user taken off while in
// grace (State.Grace) keeps their process - and what they have open - with new connections refused
// by nftables, until the grace ends.
//
// The processes run as one system account without any rights (service.SoloAccount): nftables refuses
// its new connections to this server itself and to private, link-local and metadata networks
// (nft.Spec.SoloUID), so what users ask these servers for reaches the public internet alone - never
// the agent's endpoints, a panel on the same machine or the network behind the server. Processes an
// agent before 1.3.1 started as root keep running until the supervisor confirms a restart (Pending).
package solo

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"meridian/internal/agent/cores"
	"meridian/internal/agent/service"
	"meridian/internal/agent/sys"
	"meridian/internal/proto"
)

// Templates of the services, one instance per user and protocol ("<node>-<user>").
const (
	MitaUnit   = "meridian-mita@"
	SnellUnit  = "meridian-snell@"
	AnyTLSUnit = "meridian-anytls@"
)

// units are the templates by kind, and programs the cores they run.
var (
	units    = map[string]string{"mieru": MitaUnit, "snell": SnellUnit, "anytls": AnyTLSUnit}
	programs = map[string]string{"mieru": "mita", "snell": "snell", "anytls": "sing-box"}
)

// Inst is one user's process.
type Inst struct {
	Kind      string `json:"kind"` // mieru | snell | anytls
	Transport string `json:"transport,omitempty"`
	Bind      string `json:"bind,omitempty"`
	Node      int64  `json:"node"`
	Sub       int64  `json:"sub"`
	Port      int    `json:"port"`
	Name      string `json:"name,omitempty"`
	Secret    string `json:"secret"`
	// Limited: it runs as the account without rights. An agent before 1.3.1 started its processes
	// as root; they move to the account with a confirmed restart (Pending, RestartPending).
	Limited bool `json:"limited,omitempty"`
}

// Key is the instance's name within its template.
func (i Inst) Key() string { return fmt.Sprintf("%d-%d", i.Node, i.Sub) }

// TCP and UDP say what the process listens on: mieru one of them, Snell both (its QUIC mode),
// AnyTLS TCP.
func (i Inst) TCP() bool { return i.Kind != "mieru" || i.Transport != "udp" }
func (i Inst) UDP() bool { return i.Kind == "snell" || i.Kind == "mieru" && i.Transport == "udp" }

func (i Inst) unit() string { return service.InstanceOf(units[i.Kind], i.Key()) }

// same says whether two instances are the same process: how it runs is not part of it.
func same(a, b Inst) bool {
	a.Limited, b.Limited = false, false
	return a == b
}

type Engine struct {
	Base    string // e.g. /var/lib/meridian-agent
	ConfDir string // e.g. /etc/meridian-agent/solo: the agent writes, the account reads
	RunDir  string // e.g. /run/meridian-solo: mita's control sockets, the account's to write
	Events  func(kind, level, msg string)

	mu      sync.Mutex
	running map[string]Inst // key -> what runs (and is written in ConfDir)
	grace   map[int64]int64 // user -> until when a user taken off keeps their process
	defined map[string]bool // templates written this run (with the account)
	uid     int             // the account's ids, once it exists
	gid     int
	v4only  bool // the kernel has IPv6 turned off: AnyTLS listens on IPv4
}

var keyRE = regexp.MustCompile(`^[0-9]{1,15}-[0-9]{1,15}$`)

// clean checks an instance before anything of it reaches a file, a unit name or a command line.
func clean(i Inst) error {
	switch {
	case units[i.Kind] == "":
		return fmt.Errorf("unknown kind %q", i.Kind)
	case i.Port < 1024 || i.Port > 65535:
		return fmt.Errorf("port %d is not between 1024 and 65535", i.Port)
	case i.Node <= 0 || i.Sub <= 0 || !keyRE.MatchString(i.Key()):
		return errors.New("bad ids")
	case i.Kind == "mieru" && i.Transport != "tcp" && i.Transport != "udp":
		return fmt.Errorf("mieru transport %q", i.Transport)
	case i.Kind != "mieru" && i.Transport != "":
		return fmt.Errorf("%s has no transport %q", i.Kind, i.Transport)
	case len(i.Secret) < 16 || len(i.Secret) > 128 || strings.ContainsAny(i.Secret, "\"\\=[]") ||
		strings.IndexFunc(i.Secret, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0:
		return errors.New("bad secret")
	case i.Kind == "mieru" && (len(i.Name) == 0 || len(i.Name) > 64 || strings.ContainsAny(i.Name, "\r\n\"\\")):
		return errors.New("bad user name")
	}
	if i.Bind != "" {
		if _, err := parseAddr(i.Bind); err != nil {
			return err
		}
	}
	return nil
}

// config is the instance's configuration file and its name.
func (e *Engine) config(i Inst) (path, body string) {
	switch i.Kind {
	case "snell":
		host := "::0" // IPv4 and IPv6
		if i.Bind != "" {
			host = i.Bind
		}
		return filepath.Join(e.ConfDir, i.Key()+".conf"), fmt.Sprintf("[snell-server]\nlisten = %s:%d\npsk = %s\nipv6 = false\n",
			host, i.Port, i.Secret)
	case "anytls":
		listen := "::"
		if i.Bind != "" {
			listen = i.Bind
		} else if e.v4only {
			listen = "0.0.0.0"
		}
		cert, key := e.certPaths(i.Node)
		cfg := map[string]any{
			"log": map[string]any{"level": "warn", "timestamp": false},
			// the host's resolver as /etc/resolv.conf names it (nftables lets the account ask that one
			// alone): never systemd-resolved's own upstream servers, often on a private network
			"dns": map[string]any{"servers": []any{map[string]any{"type": "local", "tag": "local", "prefer_go": true}}},
			"inbounds": []any{map[string]any{
				"type": "anytls", "tag": "in", "listen": listen, "listen_port": i.Port,
				"users": []any{map[string]any{"name": fmt.Sprintf("u%d", i.Sub), "password": i.Secret}},
				// sing-box reads the files again when they change: a renewed certificate needs no restart
				"tls": map[string]any{"enabled": true, "certificate_path": cert, "key_path": key},
			}},
			"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
			"route":     map[string]any{"final": "direct", "default_domain_resolver": "local"},
		}
		b, _ := json.Marshal(cfg)
		return filepath.Join(e.ConfDir, i.Key()+".json"), string(b)
	}
	cfg := map[string]any{
		"portBindings": []any{map[string]any{"port": i.Port, "protocol": strings.ToUpper(i.Transport)}},
		"users":        []any{map[string]any{"name": i.Name, "password": i.Secret}},
		"loggingLevel": "WARN",
		"mtu":          1400,
	}
	if i.Bind != "" {
		cfg["listenIPAddress"] = i.Bind
	}
	b, _ := json.Marshal(cfg)
	return filepath.Join(e.ConfDir, i.Key()+".json"), string(b)
}

// certPaths are where an AnyTLS protocol's certificate and key are, for all its users' processes.
func (e *Engine) certPaths(node int64) (cert, key string) {
	base := filepath.Join(e.ConfDir, fmt.Sprintf("anytls-%d", node))
	return base + ".crt", base + ".key"
}

// checkCert: an AnyTLS protocol's certificate and key, in PEM, that belong together.
func checkCert(n proto.SoloNode) error {
	if n.CertPEM == "" || n.KeyPEM == "" {
		return errors.New("no certificate")
	}
	if len(n.CertPEM) > 64<<10 || len(n.KeyPEM) > 16<<10 {
		return errors.New("the certificate is too large")
	}
	if _, err := tls.X509KeyPair([]byte(n.CertPEM), []byte(n.KeyPEM)); err != nil {
		return fmt.Errorf("the certificate and its key do not work: %v", err)
	}
	return nil
}

func (e *Engine) define(kind string) error {
	if e.defined[kind] {
		return nil
	}
	// no capabilities: the ports are 1024 and up, and the account has no rights at all
	var spec service.Spec
	switch kind {
	case "snell":
		spec = service.Spec{Name: SnellUnit, Description: "Meridian Snell %i",
			Exec: filepath.Join(e.Base, "cores", "snell", "current", "snell"),
			Args: []string{"-c", filepath.Join(e.ConfDir, "%i.conf")}}
	case "mieru":
		spec = service.Spec{Name: MitaUnit, Description: "Meridian mieru %i",
			Exec: filepath.Join(e.Base, "cores", "mita", "current", "mita"),
			Args: []string{"run"},
			Env: map[string]string{"MITA_CONFIG_JSON_FILE": filepath.Join(e.ConfDir, "%i.json"),
				"MITA_UDS_PATH": filepath.Join(e.RunDir, "mita-%i.sock"), "MITA_INSECURE_UDS": "1", "MITA_LOG_NO_TIMESTAMP": "1"},
			Private: []string{"/var/lib/mita:mode=1777"}} // its metrics file: one per process, not shared
	case "anytls":
		spec = service.Spec{Name: AnyTLSUnit, Description: "Meridian AnyTLS %i",
			Exec: filepath.Join(e.Base, "cores", "sing-box", "current", "sing-box"),
			Args: []string{"run", "-c", filepath.Join(e.ConfDir, "%i.json"), "--disable-color"}}
	default:
		return fmt.Errorf("unknown kind %q", kind)
	}
	spec.User, spec.NoFile, spec.Sandbox = service.SoloAccount, 65536, true
	if _, err := service.Define(spec); err != nil {
		return err
	}
	if e.defined == nil {
		e.defined = map[string]bool{}
	}
	e.defined[kind] = true
	return nil
}

func (e *Engine) init() {
	if e.running != nil {
		return
	}
	e.running = map[string]Inst{}
	// what runs: the instances written on disk by this agent or the one before it
	if b, err := os.ReadFile(filepath.Join(e.ConfDir, "instances.json")); err == nil {
		var list []Inst
		if json.Unmarshal(b, &list) == nil {
			for _, i := range list {
				if clean(i) == nil {
					e.running[i.Key()] = i
				}
			}
		}
	}
}

func (e *Engine) save() error {
	list := make([]Inst, 0, len(e.running))
	for _, i := range e.running {
		list = append(list, i)
	}
	sort.Slice(list, func(a, b int) bool { return list[a].Key() < list[b].Key() })
	b, _ := json.Marshal(list)
	return os.WriteFile(filepath.Join(e.ConfDir, "instances.json"), b, 0o600)
}

// SetGrace says until when users taken off because their data ran out keep their process.
func (e *Engine) SetGrace(g map[int64]int64) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.grace = g
}

// UID is the account's user id, for nftables: 0 while there is no account (no user's process ever
// ran here).
func (e *Engine) UID() int {
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.uid == 0 {
		if u, err := user.Lookup(service.SoloAccount); err == nil {
			if uid, err := strconv.Atoi(u.Uid); err == nil && uid > 0 {
				return uid
			}
		}
	}
	return e.uid
}

// prepare makes the account and its folders (under e.mu): the configurations and certificates are
// root's, readable by the account's group; mita's control sockets go in RunDir, the account's.
func (e *Engine) prepare() error {
	uid, gid, err := service.EnsureSystemUser(service.SoloAccount)
	if err != nil {
		return err
	}
	e.uid, e.gid = uid, gid
	for dir, mode := range map[string]os.FileMode{e.ConfDir: 0o750, e.RunDir: 0o770} {
		if err := os.MkdirAll(dir, mode); err != nil {
			return err
		}
		if err := e.own(dir, mode); err != nil {
			return err
		}
	}
	// what an agent before 1.3.1 wrote is root's alone: the account reads it from now on
	files, _ := filepath.Glob(filepath.Join(e.ConfDir, "*"))
	for _, f := range files {
		switch {
		case filepath.Base(f) == "instances.json":
		case strings.HasSuffix(f, ".tmp"):
			os.Remove(f)
		default:
			if err := e.own(f, 0o640); err != nil {
				return err
			}
		}
	}
	if service.Init() == "openrc" {
		// mita keeps a metrics file in /var/lib/mita (systemd gives each process its own)
		if err := os.MkdirAll("/var/lib/mita", 0o770); err == nil {
			_ = e.own("/var/lib/mita", 0o770)
		}
	}
	return nil
}

// own makes a file or folder root's, in the account's group, with mode.
func (e *Engine) own(path string, mode os.FileMode) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symbolic link", path)
	}
	if uid, gid, ok := fileOwner(st); !ok || uid != 0 || gid != e.gid {
		if err := os.Lchown(path, 0, e.gid); err != nil {
			return err
		}
	}
	if st.Mode().Perm() != mode {
		return os.Chmod(path, mode)
	}
	return nil
}

// put writes a file the account reads: in one step, so a process reading it - sing-box reads its
// certificate again when it changes - never sees half of it. It says whether the file changed.
func (e *Engine) put(path string, body []byte) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, body) {
		return false, e.own(path, 0o640)
	}
	tmp := path + ".tmp"
	os.Remove(tmp)
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return false, err
	}
	if err := e.own(tmp, 0o640); err != nil {
		os.Remove(tmp)
		return false, err
	}
	return true, os.Rename(tmp, path)
}

// Apply makes the processes match nodes: each user's started, restarted after a change of theirs,
// or stopped - unless in grace. The programs (mita, snell-server, sing-box) are fetched first when
// they are missing. Processes that run as root (started by an agent before 1.3.1) are left as they
// are: Pending lists them for a confirmed restart.
func (e *Engine) Apply(ctx context.Context, nodes []proto.SoloNode, c proto.Cores) error {
	if e == nil {
		return nil
	}
	want := map[string]Inst{}
	var errs []string
	kinds := map[string]bool{}
	certs := map[int64]proto.SoloNode{} // AnyTLS protocols' certificates
	for _, n := range nodes {
		if n.Kind == "anytls" && len(n.Users) > 0 {
			if err := checkCert(n); err != nil {
				errs = append(errs, fmt.Sprintf("protocol n%d: %v", n.NodeID, err))
				continue
			}
			certs[n.NodeID] = n
		}
		for _, u := range n.Users {
			i := Inst{Kind: n.Kind, Transport: n.Transport, Bind: n.Bind, Node: n.NodeID, Sub: u.Sub, Port: u.Port, Name: u.Name, Secret: u.Secret}
			if err := clean(i); err != nil {
				errs = append(errs, fmt.Sprintf("n%d user %d: %v", n.NodeID, u.Sub, err))
				continue
			}
			want[i.Key()] = i
			kinds[i.Kind] = true
		}
	}
	// the programs, before anything changes
	versions := map[string]string{"mieru": c.Mita, "snell": c.Snell, "anytls": c.SingBox}
	ready := map[string]bool{}
	for kind := range kinds {
		name, version := programs[kind], versions[kind]
		if _, err := cores.InUse(ctx, e.Base, name, version, c.Mirror); err != nil {
			errs = append(errs, fmt.Sprintf("installing %s %s: %v", name, version, err))
			continue
		}
		ready[kind] = true
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	e.v4only = sys.IPv6Off()
	if len(want) > 0 {
		if err := e.prepare(); err != nil {
			// nothing starts with root's rights: what runs keeps running, the rest waits for the account
			errs = append(errs, "the account the users' servers run as: "+err.Error())
			for k := range ready {
				delete(ready, k)
			}
		}
	}
	for id, n := range certs {
		if !ready["anytls"] {
			break
		}
		cert, key := e.certPaths(id)
		_, err1 := e.put(key, []byte(n.KeyPEM))
		_, err2 := e.put(cert, []byte(n.CertPEM))
		if err := errors.Join(err1, err2); err != nil {
			errs = append(errs, fmt.Sprintf("protocol n%d: writing its certificate: %v", id, err))
			delete(certs, id)
		}
	}
	now := time.Now().Unix()
	for key, i := range want {
		if !ready[i.Kind] {
			continue
		}
		if _, ok := certs[i.Node]; i.Kind == "anytls" && !ok {
			continue
		}
		if err := e.define(i.Kind); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		path, body := e.config(i)
		changed, err := e.put(path, []byte(body))
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		prev, ran := e.running[key]
		started := true // (re)started now: with the account
		switch {
		case !ran || !service.IsActive(i.unit()):
			err = service.EnableNow(i.unit())
		case changed || !same(prev, i):
			err = service.Restart(i.unit()) // this user's process alone: their new port or key
		default:
			started = false
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("starting %s for user %d: %v", i.Kind, i.Sub, err))
			continue
		}
		i.Limited = started || prev.Limited
		e.running[key] = i
	}
	for key, i := range e.running {
		if _, ok := want[key]; ok {
			continue
		}
		if e.grace[i.Sub] > now { // their data ran out: what they have open may finish (nftables refuses new connections)
			continue
		}
		e.stop(i)
	}
	e.dropCerts()
	if err := e.save(); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// dropCerts removes the certificates of AnyTLS protocols no process uses any more (under e.mu).
func (e *Engine) dropCerts() {
	used := map[string]bool{}
	for _, i := range e.running {
		if i.Kind == "anytls" {
			cert, key := e.certPaths(i.Node)
			used[cert], used[key] = true, true
		}
	}
	files, _ := filepath.Glob(filepath.Join(e.ConfDir, "anytls-*"))
	for _, f := range files {
		if !used[f] {
			os.Remove(f)
		}
	}
}

// stop ends a user's process and forgets it (under e.mu): what they had open is cut with it.
func (e *Engine) stop(i Inst) {
	_ = service.DisableNow(i.unit())
	path, _ := e.config(i)
	os.Remove(path)
	if i.Kind == "mieru" {
		os.Remove(filepath.Join(e.RunDir, "mita-"+i.Key()+".sock"))
	}
	delete(e.running, i.Key())
}

// Pending lists the protocols whose users' processes still run as root, started by an agent before
// 1.3.1: a restart moves them to the account without rights.
func (e *Engine) Pending() []string {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	count := map[int64]int{}
	for _, i := range e.running {
		if !i.Limited && e.defined[i.Kind] {
			count[i.Node]++
		}
	}
	ids := make([]int64, 0, len(count))
	for id := range count {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
	out := make([]string, len(ids))
	for k, id := range ids {
		who := "1 user's server"
		if count[id] > 1 {
			who = fmt.Sprintf("%d users' servers", count[id])
		}
		out[k] = fmt.Sprintf("protocol n%d: %s without root rights, a safer setup from the upgraded agent", id, who)
	}
	return out
}

// RestartPending restarts the processes that run as root, now with the account: their users'
// devices reconnect by themselves. It says how many restarted.
func (e *Engine) RestartPending() (int, error) {
	return e.restart(false)
}

// RestartAll restarts every user's process (on the supervisor's request), each with the account.
func (e *Engine) RestartAll() (int, error) {
	return e.restart(true)
}

func (e *Engine) restart(all bool) (int, error) {
	if e == nil {
		return 0, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	var errs []string
	done := 0
	for key, i := range e.running {
		if !e.defined[i.Kind] || (i.Limited && !all) {
			continue // a template this agent has not written yet (with the account) would start it as root
		}
		if err := service.Restart(i.unit()); err != nil {
			errs = append(errs, fmt.Sprintf("%s for user %d: %v", i.Kind, i.Sub, err))
			continue
		}
		i.Limited = true
		e.running[key] = i
		done++
	}
	if done > 0 {
		if err := e.save(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return done, errors.New(strings.Join(errs, "; "))
	}
	return done, nil
}

// Expire stops the processes whose grace ended (called with every report).
func (e *Engine) Expire(want map[string]bool) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	now := time.Now().Unix()
	changed := false
	for key, i := range e.running {
		if !want[key] && e.grace[i.Sub] <= now {
			e.stop(i)
			changed = true
		}
	}
	if changed {
		_ = e.save()
	}
}

// Running lists the processes that run now, and which of them are in grace (new connections refused).
func (e *Engine) Running(want map[string]bool) (list []Inst, noNew map[string]bool) {
	if e == nil {
		return nil, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	noNew = map[string]bool{}
	for key, i := range e.running {
		list = append(list, i)
		if !want[key] {
			noNew[key] = true
		}
	}
	sort.Slice(list, func(a, b int) bool { return list[a].Key() < list[b].Key() })
	return list, noNew
}

// Status says how the processes of each kind are: running, and the program's version.
func (e *Engine) Status() map[string]proto.CoreStatus {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	out := map[string]proto.CoreStatus{}
	for _, i := range e.running {
		name, core := i.Kind, programs[i.Kind] // mieru (mita), snell, and AnyTLS's sing-box
		if i.Kind == "anytls" {
			name = "sing-box"
		}
		st, seen := out[name]
		if !seen {
			st = proto.CoreStatus{Running: true, Version: cores.Current(e.Base, core)}
		}
		if !service.IsActive(i.unit()) {
			st.Running = false
			st.Error = "a user's process is not running: " + service.Logs(i.unit())
		}
		out[name] = st
	}
	return out
}

// Wanted is the set of instance keys a state asks for.
func Wanted(nodes []proto.SoloNode) map[string]bool {
	out := map[string]bool{}
	for _, n := range nodes {
		for _, u := range n.Users {
			out[strconv.FormatInt(n.NodeID, 10)+"-"+strconv.FormatInt(u.Sub, 10)] = true
		}
	}
	return out
}
