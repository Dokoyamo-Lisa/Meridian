// Package solo runs the protocols whose server takes one user per process - mieru (its server,
// mita) and Snell (snell-server): every user of such a protocol has their own process on their own
// port. Adding, changing or removing a user starts, restarts or stops theirs alone; nobody else is
// touched. What they use is counted per port by nftables, the devices connected to their port are
// theirs, and cutting them is stopping their process. A user taken off while in grace (State.Grace)
// keeps their process - and what they have open - with new connections refused by nftables, until
// the grace ends.
package solo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"meridian/internal/agent/cores"
	"meridian/internal/agent/service"
	"meridian/internal/proto"
)

// Templates of the services, one instance per user and protocol ("<node>-<user>").
const (
	MitaUnit  = "meridian-mita@"
	SnellUnit = "meridian-snell@"
)

// Inst is one user's process.
type Inst struct {
	Kind      string `json:"kind"` // mieru | snell
	Transport string `json:"transport,omitempty"`
	Bind      string `json:"bind,omitempty"`
	Node      int64  `json:"node"`
	Sub       int64  `json:"sub"`
	Port      int    `json:"port"`
	Name      string `json:"name,omitempty"`
	Secret    string `json:"secret"`
}

// Key is the instance's name within its template.
func (i Inst) Key() string { return fmt.Sprintf("%d-%d", i.Node, i.Sub) }

// TCP and UDP say what the process listens on: mieru one of them, Snell both (its QUIC mode).
func (i Inst) TCP() bool { return i.Kind == "snell" || i.Transport != "udp" }
func (i Inst) UDP() bool { return i.Kind == "snell" || i.Transport == "udp" }

func (i Inst) unit() string {
	if i.Kind == "snell" {
		return service.InstanceOf(SnellUnit, i.Key())
	}
	return service.InstanceOf(MitaUnit, i.Key())
}

type Engine struct {
	Base    string // e.g. /var/lib/meridian-agent
	ConfDir string // e.g. /etc/meridian-agent/solo
	RunDir  string // e.g. /run/meridian-agent/solo
	Events  func(kind, level, msg string)

	mu      sync.Mutex
	running map[string]Inst // key -> what runs (and is written in ConfDir)
	grace   map[int64]int64 // user -> until when a user taken off keeps their process
	defined map[string]bool // templates written this run
}

var keyRE = regexp.MustCompile(`^[0-9]{1,15}-[0-9]{1,15}$`)

// clean checks an instance before anything of it reaches a file, a unit name or a command line.
func clean(i Inst) error {
	switch {
	case i.Kind != "mieru" && i.Kind != "snell":
		return fmt.Errorf("unknown kind %q", i.Kind)
	case i.Port < 1024 || i.Port > 65535:
		return fmt.Errorf("port %d is not between 1024 and 65535", i.Port)
	case i.Node <= 0 || i.Sub <= 0 || !keyRE.MatchString(i.Key()):
		return errors.New("bad ids")
	case i.Kind == "mieru" && i.Transport != "tcp" && i.Transport != "udp":
		return fmt.Errorf("mieru transport %q", i.Transport)
	case len(i.Secret) < 16 || len(i.Secret) > 128 || strings.ContainsAny(i.Secret, "\r\n\"\\=[]"):
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

func (e *Engine) define(kind string) error {
	if e.defined[kind] {
		return nil
	}
	spec := service.Spec{Name: SnellUnit, Description: "Meridian Snell %i",
		Exec: filepath.Join(e.Base, "cores", "snell", "current", "snell"),
		Args: []string{"-c", filepath.Join(e.ConfDir, "%i.conf")},
		Caps: []string{"CAP_NET_BIND_SERVICE"}, NoFile: 65536, Sandbox: true}
	if kind == "mieru" {
		spec = service.Spec{Name: MitaUnit, Description: "Meridian mieru %i",
			Exec: filepath.Join(e.Base, "cores", "mita", "current", "mita"),
			Args: []string{"run"},
			Env: map[string]string{"MITA_CONFIG_JSON_FILE": filepath.Join(e.ConfDir, "%i.json"),
				"MITA_UDS_PATH": filepath.Join(e.RunDir, "mita-%i.sock"), "MITA_INSECURE_UDS": "1", "MITA_LOG_NO_TIMESTAMP": "1"},
			Caps: []string{"CAP_NET_BIND_SERVICE"}, NoFile: 65536, Sandbox: true,
			Private: []string{"/var/lib/mita"}} // its metrics file: one per process, not shared
	}
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

// Apply makes the processes match nodes: each user's started, restarted after a change of theirs,
// or stopped - unless in grace. mita and snell-server are fetched first when they are missing.
func (e *Engine) Apply(ctx context.Context, nodes []proto.SoloNode, mita, snell, mirror string) error {
	if e == nil {
		return nil
	}
	want := map[string]Inst{}
	var errs []string
	kinds := map[string]bool{}
	for _, n := range nodes {
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
	ready := map[string]bool{}
	for kind := range kinds {
		name, version := "mita", mita
		if kind == "snell" {
			name, version = "snell", snell
		}
		if _, err := cores.InUse(ctx, e.Base, name, version, mirror); err != nil {
			errs = append(errs, fmt.Sprintf("installing %s %s: %v", name, version, err))
			continue
		}
		ready[kind] = true
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	if err := os.MkdirAll(e.ConfDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(e.RunDir, 0o700); err != nil {
		return err
	}
	now := time.Now().Unix()
	for key, i := range want {
		if !ready[i.Kind] {
			continue
		}
		if err := e.define(i.Kind); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		path, body := e.config(i)
		old, err := os.ReadFile(path)
		changed := err != nil || string(old) != body
		if changed {
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				errs = append(errs, err.Error())
				continue
			}
		}
		prev, ran := e.running[key]
		switch {
		case !ran || !service.IsActive(i.unit()):
			err = service.EnableNow(i.unit())
		case changed || prev != i:
			err = service.Restart(i.unit()) // this user's process alone: their new port or key
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("starting %s for user %d: %v", i.Kind, i.Sub, err))
			continue
		}
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
	if err := e.save(); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
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
		name := "mieru"
		core := "mita"
		if i.Kind == "snell" {
			name, core = "snell", "snell"
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
