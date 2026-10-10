package health

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	"meridian/internal/proto"
)

// trafficSample is the network counters, and what Meridian carried, at one scan.
type trafficSample struct {
	at      time.Time
	rx, tx  uint64
	carried int64
}

const (
	trafficSpan = 10 * time.Minute
	trafficMin  = 1 << 30 // sent beyond what is accounted for in trafficSpan before it counts (1 GiB)
	trafficX    = 3       // ... and this many times what Meridian carried and the server received
)

// checkLoad: a load average (over 15 minutes) above twice the processor cores.
func (sc *scan) checkLoad() {
	b, err := readSmall(sc.m.path("proc/loadavg"), 256)
	if err != nil {
		return
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return
	}
	load15, err := strconv.ParseFloat(f[2], 64)
	cores := sc.m.cores()
	if err != nil || load15 <= 2*float64(cores) {
		return
	}
	sc.last(proto.Finding{Key: "load", Kind: proto.FindLoad, Severity: proto.SevWarning,
		Title: "The server has been overloaded for 15 minutes",
		Detail: fmt.Sprintf("The load average over 15 minutes is %.1f with %d processor core(s): programs wait for the processor, "+
			"and users' connections slow down. Look at what uses it (top) - an unknown busy program may be a miner.", load15, cores)})
}

// skipIface leaves out loopback and virtual interfaces, so the server's traffic is counted once
// (the same list the agent's metrics use).
func skipIface(name string) bool {
	for _, p := range []string{"lo", "uwg", "wg", "docker", "veth", "br-", "virbr", "tun", "tap", "lxc", "lxd", "cni", "flannel", "cali",
		"vnet", "incus", "tailscale", "zt", "podman", "kube"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func (m *Monitor) readNIC() (rx, tx uint64, ok bool) {
	b, err := readSmall(m.path("proc/net/dev"), 1<<20)
	if err != nil {
		return 0, 0, false
	}
	s := bufio.NewScanner(bytes.NewReader(b))
	for s.Scan() {
		name, rest, found := strings.Cut(s.Text(), ":")
		if !found {
			continue
		}
		if name = strings.TrimSpace(name); skipIface(name) {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		r, _ := strconv.ParseUint(f[0], 10, 64)
		t, _ := strconv.ParseUint(f[8], 10, 64)
		rx, tx = rx+r, tx+t
	}
	return rx, tx, true
}

// checkTraffic: over ten minutes the server sent far more (over 1 GiB, and three times as much) than
// Meridian's protocols and forwards carried and than it received - a sign of a DDoS bot or another
// program sending on its own. A proxy sends about what it receives.
func (sc *scan) checkTraffic() {
	m := sc.m
	rx, tx, ok := m.readNIC()
	if !ok {
		return
	}
	m.mu.Lock()
	cur := trafficSample{at: sc.at, rx: rx, tx: tx, carried: m.carried}
	keep := m.traffic[:0]
	for _, s := range m.traffic {
		if sc.at.Sub(s.at) <= 3*trafficSpan && s.rx <= rx && s.tx <= tx {
			keep = append(keep, s) // counters that went down (a reboot) start over
		}
	}
	m.traffic = append(keep, cur)
	// the newest sample at least trafficSpan old
	var from *trafficSample
	for i := len(m.traffic) - 2; i >= 0; i-- {
		if sc.at.Sub(m.traffic[i].at) >= trafficSpan {
			from = &m.traffic[i]
			break
		}
	}
	var base trafficSample
	if from != nil {
		base = *from
	}
	m.mu.Unlock()
	if from == nil {
		return
	}
	sent, got := int64(tx-base.tx), int64(rx-base.rx)
	carried := cur.carried - base.carried
	expected := max(carried, got)
	if sent < trafficMin+expected || sent < trafficX*expected {
		return
	}
	mins := int(sc.at.Sub(base.at).Minutes() + 0.5)
	sc.last(proto.Finding{Key: "traffic", Kind: proto.FindTraffic, Severity: proto.SevHigh,
		Title: "The server sends far more than Rosélune carries",
		Detail: fmt.Sprintf("In the last %d minutes it sent %s, while Rosélune's protocols and forwards carried %s and it received %s. "+
			"A DDoS bot or another program is sending: look for unknown busy programs, and stop it before the provider suspends the server.",
			mins, gb(sent), gb(carried), gb(got))})
}

func gb(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(b)/(1<<20))
	}
	return fmt.Sprintf("%d KB", b>>10)
}
