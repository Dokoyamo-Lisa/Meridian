package main

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"meridian/internal/agent"
	"meridian/internal/proto"
)

// A demo agent: one for each demo server, speaking the agents' own protocol to the panel (signed
// and encrypted with the server's token, like a real one), about a host that does not exist.

type demoAgent struct {
	w       *world
	s       *server
	c       *agent.Client
	version string // the panel's own, so no agent is ever out of date
	apiPort int
	boot    int64
	started int64
	rev     string
	pings   []proto.PingTarget
	fwds    []int64
	cores   map[string]proto.CoreStatus
	ports   []int
	kinds   map[int64]string
	since   map[string]int64 // device address -> online since
}

func newAgent(w *world, s *server, panel, version string, apiPort int, boot int64) (*demoAgent, error) {
	if s.token == "" {
		return nil, fmt.Errorf("%s: no token - run setup again", s.m.Name)
	}
	c, err := agent.NewClient(panel, s.token, version)
	if err != nil {
		return nil, err
	}
	return &demoAgent{w: w, s: s, c: c, version: version, apiPort: apiPort, boot: boot, started: time.Now().Unix(),
		kinds: w.kinds(), since: map[string]int64{}}, nil
}

func (g *demoAgent) hostname() string {
	n := strings.NewReplacer(" ", "-", "ã", "a").Replace(strings.ToLower(g.s.m.Name))
	return n + "-01"
}

func (g *demoAgent) hello() *proto.Hello {
	m := g.s.m
	addrs := []string{m.IPv4}
	if m.IPv6 != "" {
		addrs = append(addrs, m.IPv6)
	}
	systemd := !strings.HasPrefix(m.OS, "Alpine")
	return &proto.Hello{
		Version: proto.Version, AgentVersion: g.version, Hostname: g.hostname(), OS: m.OS, Kernel: m.Kernel, Arch: m.Arch,
		CPUModel: m.CPU, CPUCores: m.Cores, MemTotal: uint64(m.MemGB * gb), DiskTotal: uint64(m.DiskGB * 1e9),
		IPv4: m.IPv4, IPv6: m.IPv6, IPv6Gone: m.IPv6 == "", BootTime: g.boot, StartedAt: g.started, Addrs: addrs, Virt: m.Virt,
		Caps: proto.Caps{Systemd: systemd, WireGuard: true, Conntrack: true, Nftables: true, Iptables: true, Certs: true,
			APIPort: g.apiPort, RestartPending: true, Relay: true, PortHop: true, Limits: true, NoIPv6: false, WG6: m.IPv6 != "",
			Cut: true, Solo: true, AnyTLS: true, RestartAll: true, Protect: true},
	}
}

func (g *demoAgent) report(ctx context.Context, rep *proto.Report) error {
	rep.Instance = instance(g.s.token)
	_, err := g.c.Report(ctx, rep)
	return err
}

// sync takes the newest state from the panel - which protocols, cores, forwards and pings it asks
// for - and says it is in place.
func (g *demoAgent) sync(ctx context.Context) error {
	st, err := g.c.State(ctx, "", 0)
	if err != nil || st == nil {
		return err
	}
	g.pings, g.fwds, g.ports = st.Ping, nil, nil
	for _, f := range st.Forwards {
		g.fwds = append(g.fwds, f.ID)
		g.ports = append(g.ports, f.ListenPort)
	}
	cores := map[string]proto.CoreStatus{}
	since := g.started - 40
	if st.Xray != nil && len(st.Xray.Inbounds) > 0 {
		cores["xray"] = proto.CoreStatus{Running: true, Version: st.Cores.Xray, PID: 700 + g.s.idx*37, Since: since}
	}
	for i, h := range st.Hysteria {
		cores[fmt.Sprintf("hysteria-%d", h.NodeID)] = proto.CoreStatus{Running: true, Version: st.Cores.Hysteria, PID: 900 + g.s.idx*41 + i, Since: since}
	}
	g.cores = cores
	for _, n := range g.s.nodes {
		g.ports = append(g.ports, n.Port)
	}
	g.ports = append(g.ports, 22)
	if st.Rev != g.rev {
		g.rev = st.Rev
		return g.report(ctx, &proto.Report{Applied: &proto.Applied{Rev: st.Rev, OK: true, At: time.Now().Unix()}})
	}
	return nil
}

// live is what the agent reports every few seconds: the host's numbers and who is online.
func (g *demoAgent) live(t int64) *proto.Live {
	var online []proto.OnlineUser
	through, devices := 0.0, 0
	seen := map[string]bool{}
	for _, u := range g.s.people {
		ok, n := g.w.online(u, g.s, t)
		if !ok {
			continue
		}
		through += g.w.rate(u, g.s, t) / g.w.presence(u, g.s, t)
		devices += n
		ips := make([]proto.OnlineIP, 0, n)
		for d := 0; d < n; d++ {
			ip := u.devices[d]
			seen[ip] = true
			if g.since[ip] == 0 {
				g.since[ip] = t - int64(hash01(50, int64(u.idx), int64(d), t/1200)*1100)
			}
			ips = append(ips, proto.OnlineIP{IP: ip, Since: g.since[ip], Last: t})
		}
		online = append(online, proto.OnlineUser{Sub: u.id, Node: u.nodes[g.s.id][0], IPs: ips})
	}
	for ip := range g.since {
		if !seen[ip] {
			delete(g.since, ip)
		}
	}
	m := measure(g.s, t, through, devices)
	sys := proto.Sys{CPU: round(m.cpu, 1), Load1: round(m.load1, 2), Load5: round(m.load5, 2), Load15: round(m.load15, 2),
		MemTotal: uint64(g.s.m.MemGB * gb), MemUsed: uint64(m.mem), DiskTotal: uint64(g.s.m.DiskGB * 1e9), DiskUsed: uint64(m.disk),
		Uptime: t - g.boot, TCP: m.tcp, UDP: m.udp, RXRate: int64(m.rx), TXRate: int64(m.tx),
		DiskRead: int64(m.diskRead), DiskWrite: int64(m.diskWrite)}
	if m.temp > 0 {
		sys.Temps = map[string]float64{"k10temp Tctl": round(m.temp, 1), "nvme Composite": round(m.temp-9, 1)}
	}
	return &proto.Live{TS: t, Sys: sys, Online: online, Cores: g.cores, Ports: g.ports, PanelPath: "direct"}
}

// batch is what accumulated from a to b: traffic per person and protocol, devices, destinations,
// the network counters, forwards and ping rounds. live says whether people's traffic follows who
// is online (live reports) or the average (the month written at setup).
func (g *demoAgent) batch(a, b int64, live bool) *proto.Batch {
	out := &proto.Batch{Seq: b, From: a, To: b}
	total := 0.0
	const step = 600
	for _, u := range g.s.people {
		nodes := u.nodes[g.s.id]
		bytes := 0.0
		for t := a; t < b; t += min(step, b-t) {
			dt := float64(min(step, b-t))
			mid := t + int64(dt/2)
			if live {
				if ok, _ := g.w.online(u, g.s, mid); ok {
					bytes += g.w.rate(u, g.s, mid) / g.w.presence(u, g.s, mid) * dt
				}
			} else if mid >= u.cycle {
				bytes += g.w.rate(u, g.s, mid) * dt
			}
		}
		if bytes < 1 {
			continue
		}
		total += bytes
		down := downShare(u)
		for id, v := range split(nodes, g.kinds, bytes) {
			out.Traffic = append(out.Traffic, proto.UserTraffic{Sub: u.id, Node: id, Down: int64(v * down), Up: int64(v * (1 - down))})
		}
		// devices and the sites they went to
		n := 1
		if !live && u.p.Devices > 1 && hash01(51, int64(u.idx), b) < 0.5 {
			n = 2
		}
		for d := 0; d < n && d < len(u.devices); d++ {
			out.IPs = append(out.IPs, proto.IPSeen{Sub: u.id, Node: nodes[0], IP: u.devices[d], First: a + 30, Last: b - 20,
				Conns: 1 + int64(bytes/float64(n)/4e6)})
		}
		for k := 0; k < 1+int(bytes/3e8) && k < 4; k++ {
			i := int(math.Pow(hash01(52, int64(u.idx), b, int64(k)), 1.8) * float64(len(destinations)))
			d := destinations[i]
			out.Dests = append(out.Dests, proto.DestSeen{Sub: u.id, Node: nodes[0], Host: d.Host, Port: d.Port, Net: d.Net,
				Conns: 1 + int64(hash01(53, int64(u.idx), b, int64(k))*12), Last: b - int64(hash01(54, b, int64(k))*60)})
		}
	}
	if live || a >= g.s.cycle {
		over := 900.0 + 600 // the host's own traffic, a second
		out.NIC = proto.NICDelta{RX: int64(total*1.03 + over*float64(b-a)), TX: int64(total + over*float64(b-a))}
	}
	for _, id := range g.fwds { // a game server's players, in the evening
		r := 30000 * rhythm(b, g.s.m.UTC) * noise(b, 3600, 55, id)
		out.Forwards = append(out.Forwards, proto.FwdTraffic{ID: id, Down: int64(r * float64(b-a)), Up: int64(r * 0.3 * float64(b-a)),
			Conns: 1 + int64(r/4000)})
	}
	for j, p := range g.pings {
		every := int64(max(p.Interval, 10))
		if !live {
			every = max(every, 300)
		}
		mon := monitorFor(p.Target)
		for t := (a/every + 1) * every; t <= b; t += every {
			out.Pings = append(out.Pings, pingRound(g.s, mon, p.ID, t, int64(j)))
		}
	}
	return out
}

func monitorFor(target string) monitor {
	for _, m := range monitors {
		if m.Target == target {
			return m
		}
	}
	return monitor{Anycast: true}
}

func pingRound(s *server, mon monitor, id, t, j int64) proto.PingResult {
	k := int64(s.idx)
	r := proto.PingResult{ID: id, TS: t, Sent: 3}
	if hash01(60, k, j, t) < 0.012 {
		r.Lost = 1
	}
	if hash01(61, k, j, t/86400) < 0.08 && hash01(62, k, j, t/600) < 0.25 { // a bad hour now and then
		r.Lost = 1 + int(hash01(63, k, t)*2)
	}
	if r.Lost == r.Sent {
		return r
	}
	avg := rtt(s.m, mon, t, k, j)
	r.Avg, r.Min, r.Max = round(avg, 2), round(avg*(0.94+0.04*hash01(64, k, t)), 2), round(avg*(1.04+0.2*hash01(65, k, t)), 2)
	return r
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}
