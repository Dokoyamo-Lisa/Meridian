// Package agent is the Meridian server agent. It keeps the host's proxy cores, WireGuard and
// nftables matching what the panel wants, and reports traffic, connecting IPs and destinations.
//
// It is built so that nothing it does can take traffic down on its own: cores run as their own
// systemd units (an agent restart or upgrade leaves them running), changes go through live APIs,
// a panel outage changes nothing, and the last known state is restored by itself after a reboot.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"meridian/internal/agent/acme"
	"meridian/internal/agent/cores"
	"meridian/internal/agent/hy"
	"meridian/internal/agent/nft"
	"meridian/internal/agent/realm"
	"meridian/internal/agent/scan"
	"meridian/internal/agent/sys"
	"meridian/internal/agent/systemd"
	"meridian/internal/agent/wg"
	"meridian/internal/agent/xray"
	"meridian/internal/proto"
)

var Version = "dev"

const (
	ConfDir  = "/etc/meridian-agent"
	ConfPath = "/etc/meridian-agent/agent.json"
	DataDir  = "/var/lib/meridian-agent"
	RunDir   = "/run/meridian-agent"
	Unit     = "meridian-agent.service"
	BinPath  = "/usr/local/bin/meridian-agent"
)

// DefaultAPIPort is where the agent's loopback-only ports start: the Xray API on it and Hysteria's
// auth hook one up. Away from the ports other panels use (x-ui and 3x-ui put Xray's API on 62789),
// so a server can run them side by side; the installer moves up when the pair is taken. An agent
// keeps the port it was installed with.
const DefaultAPIPort = 50000

type Config struct {
	Panel   string `json:"panel"`
	Token   string `json:"token"`
	APIPort int    `json:"xray_api_port"`
	// ACME overrides, for testing against a private certificate authority only
	ACMEDirectory string `json:"acme_directory,omitempty"`
	ACMECAFile    string `json:"acme_ca_file,omitempty"`
}

func LoadConfig() (*Config, error) {
	b, err := os.ReadFile(ConfPath)
	if err != nil {
		return nil, err
	}
	c := &Config{}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, err
	}
	if c.APIPort == 0 {
		c.APIPort = DefaultAPIPort
	}
	return c, nil
}

func (c *Config) Save() error {
	if err := os.MkdirAll(ConfDir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(ConfPath, b, 0o600)
}

type Agent struct {
	cfg     *Config
	client  *Client
	xray    *xray.Engine
	hy      *hy.Engine
	nft     *nft.Engine
	wg      *wg.Engine
	realm   *realm.Engine
	certs   *acme.Manager
	sampler *sys.Sampler
	out     *outbox
	started time.Time

	mu        sync.Mutex
	state     *proto.State
	applied   *proto.Applied
	results   []proto.ActionResult
	done      map[int64]bool
	events    []proto.AgentEvent
	lastHello time.Time
	cores     map[string]proto.CoreStatus
	kick      chan struct{}
	applyMu   sync.Mutex
	geo       *geoCache
	scanned   map[string]bool // units the last scan found: the only ones a takeover may stop
}

// geoCache keeps the address list of the country rule, by hash, in memory and on disk (so the rule
// is back right after a reboot, even while the panel is unreachable).
type geoCache struct {
	hash string
	spec *nft.GeoSpec
}

func New(cfg *Config) (*Agent, error) {
	c, err := NewClient(cfg.Panel, cfg.Token, Version)
	if err != nil {
		return nil, err
	}
	// connection logs (client IPs, destinations) live in RunDir: root only. DataDir holds cores
	// that realm's unprivileged user must be able to reach, but nobody may list it.
	for d, mode := range map[string]os.FileMode{DataDir: 0o711, RunDir: 0o700, ConfDir: 0o755} {
		if err := os.MkdirAll(d, mode); err != nil {
			return nil, err
		}
		_ = os.Chmod(d, mode)
	}
	a := &Agent{cfg: cfg, client: c, nft: nft.New(), wg: wg.New(), sampler: &sys.Sampler{},
		out: loadOutbox(filepath.Join(DataDir, "outbox.json")), started: time.Now(), done: map[int64]bool{},
		kick: make(chan struct{}, 1), cores: map[string]proto.CoreStatus{}}
	a.xray = &xray.Engine{Base: DataDir, ConfDir: filepath.Join(ConfDir, "xray"), RunDir: RunDir, APIPort: cfg.APIPort,
		Events: a.event}
	a.wg.Events = a.event
	a.hy = &hy.Engine{Base: DataDir, ConfDir: filepath.Join(ConfDir, "hy2"), RunDir: RunDir, AuthPort: cfg.APIPort + 1,
		Events: a.event}
	a.realm = &realm.Engine{Base: DataDir, ConfDir: filepath.Join(ConfDir, "realm")}
	a.certs = &acme.Manager{Dir: filepath.Join(DataDir, "certs"), Directory: cfg.ACMEDirectory, CAFile: cfg.ACMECAFile,
		Events: a.event, OnChange: a.reapply}
	a.loadDone()
	return a, nil
}

// reapply applies the current state again (a certificate arrived).
func (a *Agent) reapply() {
	if st := a.current(); st != nil {
		go a.apply(context.Background(), st, false)
	}
}

// withCerts resolves Let's Encrypt certificates: inbounds and Hysteria2 nodes whose certificate is
// ready get the files, the others are left out until it is (they are listed in waiting).
func (a *Agent) withCerts(st *proto.State) (*proto.Xray, []proto.HyNode, []string) {
	var domains, waiting []string
	if st.Xray != nil {
		for _, in := range st.Xray.Inbounds {
			if in.ACME != "" {
				domains = append(domains, in.ACME)
			}
		}
	}
	for _, n := range st.Hysteria {
		if n.ACME != "" {
			domains = append(domains, n.ACME)
		}
	}
	a.certs.Want(domains)
	wait := func(d string) {
		msg := "waiting for the Let's Encrypt certificate for " + d
		if p := a.certs.Problem(d); p != "" {
			msg += ": " + p
		}
		waiting = append(waiting, msg)
	}
	var xr *proto.Xray
	if st.Xray != nil {
		xr = &proto.Xray{Base: st.Xray.Base}
		for _, in := range st.Xray.Inbounds {
			if in.ACME == "" {
				xr.Inbounds = append(xr.Inbounds, in)
				continue
			}
			if !acme.ValidDomain(in.ACME) || !a.certs.Ready(in.ACME) {
				wait(in.ACME)
				continue
			}
			cert, key := a.certs.Paths(in.ACME)
			cfg := strings.NewReplacer(
				`"`+proto.ACMEPrefix+in.ACME+`/cert"`, strconvQuote(cert),
				`"`+proto.ACMEPrefix+in.ACME+`/key"`, strconvQuote(key)).Replace(string(in.Config))
			if strings.Contains(cfg, proto.ACMEPrefix) { // a reference to another domain: refuse
				waiting = append(waiting, "inbound "+in.Tag+" refers to a certificate it did not ask for")
				continue
			}
			in.Config = json.RawMessage(cfg)
			xr.Inbounds = append(xr.Inbounds, in)
		}
	}
	var hys []proto.HyNode
	for _, n := range st.Hysteria {
		if n.ACME != "" {
			if !acme.ValidDomain(n.ACME) || !a.certs.Ready(n.ACME) {
				wait(n.ACME)
				continue
			}
			cert, key := a.certs.Paths(n.ACME)
			cb, err1 := os.ReadFile(cert)
			kb, err2 := os.ReadFile(key)
			if err1 != nil || err2 != nil {
				wait(n.ACME)
				continue
			}
			n.CertPEM, n.KeyPEM = string(cb), string(kb)
		}
		hys = append(hys, n)
	}
	return xr, hys, waiting
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func (a *Agent) event(kind, level, msg string) {
	slog.Info("event", "kind", kind, "msg", msg)
	a.mu.Lock()
	a.events = append(a.events, proto.AgentEvent{TS: time.Now().Unix(), Kind: kind, Level: level, Message: msg})
	a.mu.Unlock()
}

// Run blocks until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	// restore the last known state at once (after a reboot WireGuard and nftables are empty)
	if st := a.loadState(); st != nil {
		slog.Info("restoring last known state", "rev", st.Rev)
		a.apply(ctx, st, false)
	}
	go a.watch(ctx)
	go a.reconcileLoop(ctx)
	go a.certs.Run(ctx)
	a.reportLoop(ctx)
	return nil
}

// ---------------------------------------------------------------- state

func (a *Agent) watch(ctx context.Context) {
	backoff := time.Second
	first := true
	for ctx.Err() == nil {
		rev := ""
		if st := a.current(); st != nil && !first {
			rev = st.Rev // after a restart, always fetch the full state once
		}
		st, err := a.client.State(ctx, rev, 25)
		if err != nil {
			var se *statusError
			if errors.As(err, &se) && se.code == http.StatusUnauthorized {
				slog.Error("panel refused this agent", "err", err)
			} else {
				slog.Warn("panel unreachable - keeping the current configuration", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		first = false
		if st != nil {
			a.apply(ctx, st, true)
		}
	}
}

func (a *Agent) current() *proto.State {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

// reconcileLoop re-applies the current state now and then, which repairs drift (a core that was
// restarted by hand, a port that became free) without any restart.
func (a *Agent) reconcileLoop(ctx context.Context) {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if st := a.current(); st != nil {
				a.apply(ctx, st, false)
			}
		}
	}
}

// apply makes the host match st. fresh is true for a state just received from the panel.
func (a *Agent) apply(ctx context.Context, st *proto.State, fresh bool) {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	if st.Contract > proto.Version {
		// a newer panel: keep running what we have, accept only the upgrade
		var acts []proto.Action
		for _, act := range st.Actions {
			if act.Kind == proto.ActionUpgradeAgent {
				acts = append(acts, act)
			}
		}
		a.runActions(st, acts)
		a.mu.Lock()
		a.applied = &proto.Applied{Rev: st.Rev, OK: false, At: time.Now().Unix(), Errors: []string{fmt.Sprintf(
			"the panel needs a newer agent (contract %d, this agent speaks %d) - upgrade the agent; the current configuration keeps running",
			st.Contract, proto.Version)}}
		a.mu.Unlock()
		return
	}
	if st.Decommission {
		a.decommission()
		return
	}
	var errs, pending []string
	note := func(what string, err error) {
		if err != nil {
			errs = append(errs, what+": "+err.Error())
		}
	}
	logOn := st.Agent.ConnLog || st.Agent.DestLog
	geo, gerr := a.geoSpec(ctx, st.Geo)
	note("country rule", gerr)

	cores.SetDigests(st.Cores.Digests)
	if !runtimeOK() {
		note("agent", errors.New("this host is not Linux with systemd"))
	} else if err := a.prepare(ctx, st); err != nil {
		// a core could not be downloaded: change nothing, the running configuration stays
		note("download", err)
	} else {
		xr, hys, waiting := a.withCerts(st)
		for _, w := range waiting {
			errs = append(errs, "certificate: "+w)
		}
		res, err := a.xray.Apply(ctx, xr, st.Cores.Xray, st.Cores.Mirror, logOn, false)
		note("xray", err)
		pending = append(pending, res.Pending...)
		note("hysteria", a.hy.Apply(ctx, hys, st.Cores.Hysteria, st.Cores.Mirror))
		note("wireguard", a.wg.Apply(st.WireGuard))
		note("realm", a.realm.Apply(ctx, st.Forwards, st.Cores.Realm, st.Cores.Mirror))
		spec := a.nftSpec(st)
		spec.Geo = geo
		note("nftables", a.nft.Apply(spec))
	}

	a.runActions(st, st.Actions)

	a.mu.Lock()
	a.state = st
	a.applied = &proto.Applied{Rev: st.Rev, OK: len(errs) == 0, Errors: errs, Pending: pending, At: time.Now().Unix()}
	a.mu.Unlock()
	if fresh {
		a.saveState(st)
		if len(errs) > 0 {
			slog.Warn("applied with errors", "rev", st.Rev, "errors", strings.Join(errs, "; "))
		} else {
			slog.Info("applied", "rev", st.Rev)
		}
		select {
		case a.kick <- struct{}{}:
		default:
		}
	}
}

// runActions starts each new action once, in the background: actions can take a while (downloads,
// probes) and must never hold up configuration.
func (a *Agent) runActions(st *proto.State, acts []proto.Action) {
	for _, act := range acts {
		a.mu.Lock()
		done := a.done[act.ID]
		a.done[act.ID] = true
		a.mu.Unlock()
		if done {
			continue
		}
		a.saveDone()
		go func(act proto.Action) {
			out, err := a.runAction(context.Background(), st, act)
			r := proto.ActionResult{ID: act.ID, OK: err == nil, Output: out}
			if err != nil {
				r.Output = err.Error()
			}
			a.mu.Lock()
			a.results = append(a.results, r)
			a.mu.Unlock()
			select {
			case a.kick <- struct{}{}:
			default:
			}
		}(act)
	}
}

// prepare downloads every core the state needs before anything is changed, so a slow or failed
// download never leaves a protocol half moved.
func (a *Agent) prepare(ctx context.Context, st *proto.State) error {
	if st.Xray != nil && len(st.Xray.Inbounds) > 0 && !a.xray.Installed() {
		if err := a.xray.Install(ctx, st.Cores.Xray, st.Cores.Mirror); err != nil {
			return fmt.Errorf("installing Xray %s: %w", st.Cores.Xray, err)
		}
	}
	if len(st.Hysteria) > 0 {
		if _, err := cores.EnsureHysteria(ctx, DataDir, st.Cores.Hysteria, st.Cores.Mirror); err != nil {
			return fmt.Errorf("installing Hysteria %s: %w", st.Cores.Hysteria, err)
		}
	}
	for _, f := range st.Forwards {
		if f.Engine == "realm" {
			if _, err := cores.EnsureRealm(ctx, DataDir, st.Cores.Realm, st.Cores.Mirror); err != nil {
				return fmt.Errorf("realm %s: %w", st.Cores.Realm, err)
			}
			break
		}
	}
	return nil
}

// geoSpec turns the country rule into firewall sets. When the list cannot be fetched, the last one
// that was applied stays in force, so a panel outage never opens the server up.
func (a *Agent) geoSpec(ctx context.Context, r *proto.GeoRule) (*nft.GeoSpec, error) {
	if r == nil || (r.Mode != "block" && r.Mode != "allow") {
		a.geo = nil
		return nil, nil
	}
	if a.geo != nil && a.geo.hash == r.List {
		g := *a.geo.spec
		g.Allow, g.Except = r.Mode == "allow", r.Except
		return &g, nil
	}
	path := filepath.Join(DataDir, "geo-"+r.List+".json")
	body, err := os.ReadFile(path)
	if err == nil {
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != r.List {
			body, err = nil, errors.New("damaged copy")
		}
	}
	if err != nil {
		fctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		body, err = a.client.GeoList(fctx, r.List)
		cancel()
		if err != nil {
			if a.geo != nil {
				return a.geo.spec, fmt.Errorf("keeping the previous country list: %w", err)
			}
			return nil, fmt.Errorf("cannot fetch the country list: %w", err)
		}
		tmp := path + ".tmp"
		if os.WriteFile(tmp, body, 0o600) == nil && os.Rename(tmp, path) == nil {
			old, _ := filepath.Glob(filepath.Join(DataDir, "geo-*.json"))
			for _, f := range old {
				if f != path {
					os.Remove(f)
				}
			}
		}
	}
	var list proto.GeoList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("country list: %w", err)
	}
	g := &nft.GeoSpec{Allow: r.Mode == "allow", V4: list.V4, V6: list.V6, Except: r.Except}
	a.geo = &geoCache{hash: r.List, spec: g}
	return g, nil
}

// nftSpec derives the nftables table from the state.
func (a *Agent) nftSpec(st *proto.State) nft.Spec {
	spec := nft.Spec{Forwards: st.Forwards, Blocked: st.BlockedIPs}
	// the Xray API and the Hysteria auth/stats endpoints listen on loopback without passwords:
	// only root (the agent and the cores) may connect to them
	spec.LocalOnly = append([]int{a.cfg.APIPort}, a.hy.LocalPorts()...)
	if st.Xray != nil {
		for _, in := range st.Xray.Inbounds {
			var obj struct {
				Port     int    `json:"port"`
				Protocol string `json:"protocol"`
				Settings struct {
					UDP bool `json:"udp"`
				} `json:"settings"`
			}
			if json.Unmarshal(in.Config, &obj) != nil || obj.Port == 0 {
				continue
			}
			switch {
			case obj.Protocol == "hysteria":
				spec.UDPPorts = append(spec.UDPPorts, obj.Port)
			case obj.Protocol == "shadowsocks", obj.Protocol == "socks" && obj.Settings.UDP:
				spec.TCPPorts = append(spec.TCPPorts, obj.Port)
				spec.UDPPorts = append(spec.UDPPorts, obj.Port)
			default:
				spec.TCPPorts = append(spec.TCPPorts, obj.Port)
			}
		}
	}
	spec.UDPPorts = append(spec.UDPPorts, hy.Ports(st.Hysteria)...)
	for _, w := range st.WireGuard {
		spec.UDPPorts = append(spec.UDPPorts, w.ListenPort)
		g := nft.WGNat{NodeID: w.NodeID, Iface: w.Name}
		for _, ad := range w.Address {
			if strings.Contains(ad, ".") {
				if _, n, err := net.ParseCIDR(ad); err == nil {
					g.Subnet4 = n.String()
				}
			}
		}
		spec.WG = append(spec.WG, g)
	}
	return spec
}

func runtimeOK() bool { return runtime.GOOS == "linux" }

// ---------------------------------------------------------------- actions

func (a *Agent) runAction(ctx context.Context, st *proto.State, act proto.Action) (string, error) {
	var args map[string]any
	_ = json.Unmarshal(act.Args, &args)
	switch act.Kind {
	case proto.ActionRestartXray:
		if err := a.xray.Restart(ctx); err != nil {
			return "", err
		}
		a.event("core_restarted", "warn", "Xray restarted on request")
		return "Xray restarted", nil
	case proto.ActionUpgradeXray:
		v, _ := args["version"].(string)
		if v == "" {
			v = st.Cores.Xray
		}
		return a.xray.Upgrade(ctx, strings.TrimPrefix(v, "v"), st.Cores.Mirror)
	case proto.ActionUpgradeAgent:
		sums, _ := args["sha256"].(map[string]any)
		want, _ := sums[runtime.GOARCH].(string)
		return a.upgradeSelf(ctx, want)
	case proto.ActionCheckTarget:
		var req proto.TargetCheck
		_ = json.Unmarshal(act.Args, &req)
		if len(req.Targets) > 16 {
			req.Targets = req.Targets[:16]
		}
		out := map[string]any{"node_id": req.NodeID, "auto": req.Auto}
		// test the sites side by side, four at a time
		results := make([]proto.TargetResult, len(req.Targets))
		sem := make(chan struct{}, 4)
		var wg sync.WaitGroup
		for i, t := range req.Targets {
			wg.Add(1)
			go func(i int, t proto.TargetSpec) {
				defer wg.Done()
				sem <- struct{}{}
				results[i] = a.xray.TestTarget(ctx, t)
				<-sem
			}(i, t)
		}
		wg.Wait()
		out["results"] = results
		b, _ := json.Marshal(out)
		return string(b), nil
	case proto.ActionScan:
		res := scan.Run(func(unit string) bool { return strings.HasPrefix(unit, "meridian-") })
		units := map[string]bool{}
		for _, f := range res.Found {
			if f.Unit != "" {
				units[f.Unit] = true
			}
		}
		a.mu.Lock()
		a.scanned = units
		a.mu.Unlock()
		b, err := json.Marshal(res)
		if err != nil {
			return "", err
		}
		if len(b) > 4<<20 {
			return "", errors.New("the existing configuration is too large to import (over 4 MB)")
		}
		return string(b), nil
	case proto.ActionStopService:
		unit, _ := args["unit"].(string)
		if !unitRE.MatchString(unit) || strings.HasPrefix(unit, "meridian-") {
			return "", fmt.Errorf("%q is not a service this agent may stop", unit)
		}
		a.mu.Lock()
		ok := a.scanned[unit]
		a.mu.Unlock()
		if !ok {
			return "", fmt.Errorf("%s was not found by the last scan - scan the server again first", unit)
		}
		if err := systemd.DisableNow(unit); err != nil {
			return "", err
		}
		a.event("service_stopped", "warn", unit+" stopped and disabled for the takeover")
		return unit + " stopped and disabled", nil
	}
	return "", fmt.Errorf("unknown action %q", act.Kind)
}

var unitRE = regexp.MustCompile(`^[A-Za-z0-9@_.:-]{1,120}\.service$`)

// upgradeSelf replaces the agent binary with the panel's and restarts the agent unit. Cores keep
// running; only the agent process restarts. want is the binary's SHA-256 as sent by the panel in
// the signed state - the download itself may travel over plain HTTP, the checksum never does.
func (a *Agent) upgradeSelf(ctx context.Context, want string) (string, error) {
	want = strings.ToLower(strings.TrimSpace(want))
	if len(want) != 64 {
		return "", errors.New("the panel sent no checksum for this agent build - upgrade the panel first")
	}
	name := "meridian-agent-linux-" + runtime.GOARCH
	base := strings.TrimRight(a.cfg.Panel, "/") + "/agent/v1/download/" + name
	get := func(u string) ([]byte, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: %s", u, resp.Status)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
	}
	bin, err := get(base)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(bin)
	if hex.EncodeToString(h[:]) != want {
		return "", errors.New("downloaded agent does not match the checksum the panel sent")
	}
	tmp := BinPath + ".new"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return "", err
	}
	if out, err := exec.Command(tmp, "version").CombinedOutput(); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("new agent does not run: %s", strings.TrimSpace(string(out)))
	}
	if err := os.Rename(tmp, BinPath); err != nil {
		return "", err
	}
	go func() {
		time.Sleep(3 * time.Second) // let the result reach the panel first
		_ = exec.Command("systemctl", "restart", Unit).Run()
	}()
	return "agent upgraded; restarting the agent (traffic is not affected)", nil
}

// ---------------------------------------------------------------- reports

func (a *Agent) reportLoop(ctx context.Context) {
	a.sampler.Sample()
	interval := func() time.Duration {
		if st := a.current(); st != nil && st.Agent.ReportInterval >= 5 {
			return time.Duration(st.Agent.ReportInterval) * time.Second
		}
		return 10 * time.Second
	}
	timer := time.NewTimer(2 * time.Second)
	for {
		select {
		case <-ctx.Done():
			a.out.save()
			return
		case <-timer.C:
		case <-a.kick:
		}
		a.report(ctx)
		timer.Reset(interval())
	}
}

func (a *Agent) report(ctx context.Context) {
	st := a.current()
	connLog, destLog := true, true
	if st != nil {
		connLog, destLog = st.Agent.ConnLog, st.Agent.DestLog
	}
	a.sampler.Sample()

	live := &proto.Live{TS: time.Now().Unix(), Sys: a.sampler.Sys(), Ports: sys.ListeningPorts(),
		Cores: map[string]proto.CoreStatus{}}
	var traffic []proto.UserTraffic
	var ips []proto.IPSeen
	var dests []proto.DestSeen
	if runtimeOK() {
		xc := a.xray.Collect(ctx, connLog, destLog)
		if a.xray.Installed() {
			live.Cores["xray"] = xc.Status
		}
		traffic = append(traffic, xc.Traffic...)
		ips = append(ips, xc.IPs...)
		dests = append(dests, xc.Dests...)
		live.Online = append(live.Online, xc.Online...)

		hc := a.hy.Collect(connLog, destLog)
		for id, cs := range hc.Status {
			live.Cores[fmt.Sprintf("hysteria-%d", id)] = cs
		}
		traffic = append(traffic, hc.Traffic...)
		ips = append(ips, hc.IPs...)
		dests = append(dests, hc.Dests...)
		live.Online = append(live.Online, hc.Online...)

		wc := a.wg.Collect(connLog, destLog)
		traffic = append(traffic, wc.Traffic...)
		ips = append(ips, wc.IPs...)
		dests = append(dests, wc.Dests...)
		live.Online = append(live.Online, wc.Online...)
	}
	var fwds []proto.FwdTraffic
	for id, c := range a.nft.Take() {
		fwds = append(fwds, proto.FwdTraffic{ID: id, Up: c.Up, Down: c.Down, Conns: c.Conns})
	}
	rx, tx := a.sampler.TakeNIC()

	a.mu.Lock()
	events := a.events
	a.events = nil
	results := append([]proto.ActionResult(nil), a.results...)
	applied := a.applied
	sendHello := time.Since(a.lastHello) > 10*time.Minute
	a.mu.Unlock()

	a.out.add(traffic, fwds, ips, dests, proto.NICDelta{RX: rx, TX: tx}, events, a.nft.TakeGeoDrops())
	rep := &proto.Report{Instance: a.out.Instance, Batch: a.out.next(), Live: live, Applied: applied,
		ActionResults: results}
	if sendHello {
		h := sys.Hello(Version, a.started)
		h.Caps = sys.Caps()
		h.Caps.APIPort = a.cfg.APIPort
		rep.Hello = h
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	ack, err := a.client.Report(rctx, rep)
	cancel()
	if err != nil {
		slog.Warn("report not delivered - will resend", "err", err)
		a.out.save()
		return
	}
	a.out.ack(ack)
	a.out.save()
	a.mu.Lock()
	if sendHello {
		a.lastHello = time.Now()
	}
	// results that reached the panel are done
	keep := a.results[:0]
	for _, r := range a.results {
		delivered := false
		for _, s := range results {
			if s.ID == r.ID {
				delivered = true
			}
		}
		if !delivered {
			keep = append(keep, r)
		}
	}
	a.results = keep
	a.mu.Unlock()
}

// ---------------------------------------------------------------- persistence

func (a *Agent) statePath() string { return filepath.Join(DataDir, "state.json") }

func (a *Agent) saveState(st *proto.State) {
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	tmp := a.statePath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, a.statePath())
	}
}

func (a *Agent) loadState() *proto.State {
	b, err := os.ReadFile(a.statePath())
	if err != nil {
		return nil
	}
	var st proto.State
	if json.Unmarshal(b, &st) != nil || st.Rev == "" {
		return nil
	}
	return &st
}

func (a *Agent) donePath() string { return filepath.Join(DataDir, "actions-done.json") }

func (a *Agent) loadDone() {
	b, err := os.ReadFile(a.donePath())
	if err != nil {
		return
	}
	var ids []int64
	if json.Unmarshal(b, &ids) == nil {
		for _, id := range ids {
			a.done[id] = true
		}
	}
}

func (a *Agent) saveDone() {
	a.mu.Lock()
	ids := make([]int64, 0, len(a.done))
	for id := range a.done {
		ids = append(ids, id)
	}
	a.mu.Unlock()
	if len(ids) > 1000 {
		ids = ids[len(ids)-1000:]
	}
	b, _ := json.Marshal(ids)
	_ = os.WriteFile(a.donePath(), b, 0o600)
}

// decommission removes everything the agent manages and the agent itself: the panel deleted
// this server.
func (a *Agent) decommission() {
	slog.Warn("this server was deleted in the panel - removing Meridian from it")
	Uninstall(false)
	os.Exit(0)
}
