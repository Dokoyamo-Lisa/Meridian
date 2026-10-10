package agent

import (
	"os/exec"
	"path/filepath"
	"strconv"

	"meridian/internal/agent/health"
	"meridian/internal/agent/service"
	"meridian/internal/proto"
)

// newHealth sets up the health check (internal/agent/health): a scan every few minutes for signs that
// the server was broken into or is abused, reported to the panel. It only tells; what it offers to
// do about a finding happens when the supervisor confirms it (protect/).
func (a *Agent) newHealth() *health.Monitor {
	m := &health.Monitor{Root: "/", File: filepath.Join(DataDir, "health.json"), Self: "/proc/self/exe", Own: a.healthOwn}
	if service.Init() == "systemd" {
		if _, err := exec.LookPath("journalctl"); err == nil {
			m.Journal = health.Journal
		}
	}
	return m
}

// healthOwn is what Meridian runs here, from the state: the ports its protocols, WireGuard and
// forwards listen on (WireGuard's belong to the kernel, so no program shows them as Meridian's), the
// agent's own, and Let's Encrypt's check.
func (a *Agent) healthOwn() (health.Own, bool) {
	st := a.current()
	if st == nil {
		return health.Own{}, false
	}
	ports := map[string]bool{}
	add := func(netw string, port int) {
		if port > 0 && port < 65536 {
			ports[netw+":"+strconv.Itoa(port)] = true
		}
	}
	spec := a.nftSpec(st)
	for _, p := range spec.TCPPorts {
		add("tcp", p)
	}
	for _, p := range spec.UDPPorts {
		add("udp", p)
	}
	for _, p := range spec.LocalOnly {
		add("tcp", p)
	}
	for _, f := range st.Forwards {
		if f.Network != "udp" {
			add("tcp", f.ListenPort)
		}
		if f.Network != "tcp" {
			add("udp", f.ListenPort)
		}
	}
	acme := false
	if st.Xray != nil {
		for _, in := range st.Xray.Inbounds {
			acme = acme || in.ACME != ""
		}
	}
	for _, n := range st.Hysteria {
		acme = acme || n.ACME != ""
	}
	for _, n := range st.Solo {
		acme = acme || n.ACME != "" // AnyTLS
	}
	if acme {
		add("tcp", 80)
		add("tcp", st.Agent.ACMEPort)
	}
	return health.Own{Ports: ports, Data: DataDir, Agent: BinPath, Digests: st.Cores.Digests}, true
}

// carriedBytes is what Meridian's protocols and forwards carried in one report: what a proxy sends.
func carriedBytes(traffic []proto.UserTraffic, fwds []proto.FwdTraffic) int64 {
	var n int64
	for _, t := range traffic {
		n += max(t.Up, 0) + max(t.Down, 0)
	}
	for _, f := range fwds {
		n += max(f.Up, 0) + max(f.Down, 0)
	}
	return n
}
