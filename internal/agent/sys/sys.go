// Package sys reads host facts and metrics from /proc. Linux only at runtime; on other systems the
// functions return zero values so the agent can still be built and unit-tested anywhere.
package sys

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"meridian/internal/proto"
)

// Hello gathers the facts the panel shows about a host.
func Hello(version string, started time.Time) *proto.Hello {
	h := &proto.Hello{Version: proto.Version, AgentVersion: version, Arch: runtime.GOARCH, StartedAt: started.Unix()}
	h.Hostname, _ = os.Hostname()
	h.OS = osName()
	h.Kernel = strings.TrimSpace(readFile("/proc/sys/kernel/osrelease"))
	h.CPUModel, h.CPUCores = cpuInfo()
	h.MemTotal = memInfo()["MemTotal"]
	h.DiskTotal, _ = disk("/")
	h.BootTime = bootTime()
	h.IPv4, h.IPv6 = PublicIPs()
	return h
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

func osName() string {
	for _, l := range strings.Split(readFile("/etc/os-release"), "\n") {
		if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return runtime.GOOS
}

func cpuInfo() (string, int) {
	model := ""
	for _, l := range strings.Split(readFile("/proc/cpuinfo"), "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "model name" || k == "Model" || k == "Hardware" {
			model = strings.TrimSpace(v)
			break
		}
	}
	return model, runtime.NumCPU()
}

func memInfo() map[string]uint64 {
	out := map[string]uint64{}
	for _, l := range strings.Split(readFile("/proc/meminfo"), "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n, _ := strconv.ParseUint(f[0], 10, 64)
		if len(f) > 1 && f[1] == "kB" {
			n *= 1024
		}
		out[k] = n
	}
	return out
}

func disk(path string) (total, used uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	total = st.Blocks * uint64(st.Bsize)
	free := st.Bavail * uint64(st.Bsize)
	if total > free {
		used = total - free
	}
	return total, used
}

func bootTime() int64 {
	for _, l := range strings.Split(readFile("/proc/stat"), "\n") {
		if v, ok := strings.CutPrefix(l, "btime "); ok {
			n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			return n
		}
	}
	return 0
}

// PublicIPs finds the host's public addresses: first from interface addresses, then by asking
// the routing table which source address reaches the internet.
func PublicIPs() (v4, v6 string) {
	if c, err := net.Dial("udp4", "1.1.1.1:53"); err == nil {
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok && isPublic(a.IP) {
			v4 = a.IP.String()
		}
		c.Close()
	}
	if c, err := net.Dial("udp6", "[2606:4700:4700::1111]:53"); err == nil {
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok && isPublic(a.IP) {
			v6 = a.IP.String()
		}
		c.Close()
	}
	if v4 == "" {
		v4 = lookupPublic()
	}
	return v4, v6
}

func isPublic(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() &&
		!(ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1]&0xc0 == 64) // CGNAT
}

// lookupPublic asks the echo services in turn; used behind NAT, where no interface holds the
// public IP.
func lookupPublic() string {
	for _, s := range echoServices {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		ip := echoIP(ctx, s.url, s.parse)
		cancel()
		if ip != "" {
			return ip
		}
	}
	return ""
}

// ---------------------------------------------------------------- metrics

// Sampler computes rates between calls.
type Sampler struct {
	mu        sync.Mutex
	lastCPU   cpuTimes
	lastNet   netTotals
	lastAt    time.Time
	rxRate    int64
	txRate    int64
	cpu       float64
	nicDeltaR int64
	nicDeltaT int64
}

type cpuTimes struct{ idle, total uint64 }
type netTotals struct{ rx, tx uint64 }

func readCPU() cpuTimes {
	for _, l := range strings.Split(readFile("/proc/stat"), "\n") {
		if strings.HasPrefix(l, "cpu ") {
			f := strings.Fields(l)[1:]
			var t cpuTimes
			for i, v := range f {
				n, _ := strconv.ParseUint(v, 10, 64)
				t.total += n
				if i == 3 || i == 4 { // idle + iowait
					t.idle += n
				}
			}
			return t
		}
	}
	return cpuTimes{}
}

// skipIface leaves out loopback and virtual interfaces so host traffic is counted once.
func skipIface(name string) bool {
	for _, p := range []string{"lo", "uwg", "wg", "docker", "veth", "br-", "virbr", "tun", "tap", "lxc", "cni", "flannel", "cali", "vnet", "incus"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func readNet() netTotals {
	var t netTotals
	sc := bufio.NewScanner(bytes.NewReader([]byte(readFile("/proc/net/dev"))))
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if skipIface(name) {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(f[0], 10, 64)
		tx, _ := strconv.ParseUint(f[8], 10, 64)
		t.rx += rx
		t.tx += tx
	}
	return t
}

// Sample refreshes rates; call it once per report.
func (s *Sampler) Sample() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	c := readCPU()
	n := readNet()
	if !s.lastAt.IsZero() {
		dt := now.Sub(s.lastAt).Seconds()
		if dt > 0 {
			if dTot := c.total - s.lastCPU.total; dTot > 0 && c.total >= s.lastCPU.total {
				s.cpu = 100 * (1 - float64(c.idle-s.lastCPU.idle)/float64(dTot))
			}
			if n.rx >= s.lastNet.rx && n.tx >= s.lastNet.tx {
				dr, dtx := int64(n.rx-s.lastNet.rx), int64(n.tx-s.lastNet.tx)
				s.rxRate, s.txRate = int64(float64(dr)/dt), int64(float64(dtx)/dt)
				s.nicDeltaR += dr
				s.nicDeltaT += dtx
			}
		}
	}
	s.lastCPU, s.lastNet, s.lastAt = c, n, now
}

// TakeNIC returns NIC bytes since the last call.
func (s *Sampler) TakeNIC() (rx, tx int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rx, tx = s.nicDeltaR, s.nicDeltaT
	s.nicDeltaR, s.nicDeltaT = 0, 0
	return
}

// Sys returns the current metrics.
func (s *Sampler) Sys() proto.Sys {
	s.mu.Lock()
	cpu, rx, tx := s.cpu, s.rxRate, s.txRate
	s.mu.Unlock()
	m := memInfo()
	out := proto.Sys{CPU: round1(cpu), MemTotal: m["MemTotal"], SwapTotal: m["SwapTotal"], RXRate: rx, TXRate: tx}
	if avail, ok := m["MemAvailable"]; ok && out.MemTotal >= avail {
		out.MemUsed = out.MemTotal - avail
	}
	if out.SwapTotal >= m["SwapFree"] {
		out.SwapUsed = out.SwapTotal - m["SwapFree"]
	}
	out.DiskTotal, out.DiskUsed = disk("/")
	if f := strings.Fields(readFile("/proc/loadavg")); len(f) >= 3 {
		out.Load1, _ = strconv.ParseFloat(f[0], 64)
		out.Load5, _ = strconv.ParseFloat(f[1], 64)
		out.Load15, _ = strconv.ParseFloat(f[2], 64)
	}
	if f := strings.Fields(readFile("/proc/uptime")); len(f) > 0 {
		v, _ := strconv.ParseFloat(f[0], 64)
		out.Uptime = int64(v)
	}
	out.TCP, out.UDP = sockets()
	return out
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

// sockets counts established TCP and open UDP sockets.
func sockets() (tcp, udp int) {
	for _, l := range strings.Split(readFile("/proc/net/sockstat")+readFile("/proc/net/sockstat6"), "\n") {
		f := strings.Fields(l)
		if len(f) >= 3 && (f[0] == "TCP:" || f[0] == "TCP6:") && f[1] == "inuse" {
			n, _ := strconv.Atoi(f[2])
			tcp += n
		}
		if len(f) >= 3 && (f[0] == "UDP:" || f[0] == "UDP6:") && f[1] == "inuse" {
			n, _ := strconv.Atoi(f[2])
			udp += n
		}
	}
	return
}

// ListeningPorts returns TCP ports in LISTEN state and bound UDP ports.
func ListeningPorts() []int {
	seen := map[int]bool{}
	parse := func(path string, listenOnly bool) {
		lines := strings.Split(readFile(path), "\n")
		for _, l := range lines[min(1, len(lines)):] {
			f := strings.Fields(l)
			if len(f) < 4 {
				continue
			}
			if listenOnly && f[3] != "0A" {
				continue
			}
			_, port, ok := strings.Cut(f[1], ":")
			if !ok {
				continue
			}
			n, err := strconv.ParseInt(port, 16, 32)
			if err == nil && n > 0 {
				seen[int(n)] = true
			}
		}
	}
	parse("/proc/net/tcp", true)
	parse("/proc/net/tcp6", true)
	parse("/proc/net/udp", false)
	parse("/proc/net/udp6", false)
	out := make([]int, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// Caps probes what the host supports.
func Caps() proto.Caps {
	c := proto.Caps{}
	_, err := os.Stat("/run/systemd/system")
	c.Systemd = err == nil
	c.WireGuard = hasWireGuard()
	_, err = os.Stat("/proc/net/nf_conntrack")
	c.Conntrack = err == nil || fileExists("/proc/sys/net/netfilter/nf_conntrack_acct")
	c.Nftables = lookPath("nft")
	c.Iptables = lookPath("iptables")
	return c
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func hasWireGuard() bool {
	if fileExists("/sys/module/wireguard") {
		return true
	}
	for _, l := range strings.Split(readFile("/proc/modules"), "\n") {
		if strings.HasPrefix(l, "wireguard ") {
			return true
		}
	}
	// built as a module but not loaded yet
	rel := strings.TrimSpace(readFile("/proc/sys/kernel/osrelease"))
	return rel != "" && (fileExists("/lib/modules/"+rel+"/kernel/drivers/net/wireguard/wireguard.ko") ||
		fileExists("/lib/modules/"+rel+"/kernel/drivers/net/wireguard/wireguard.ko.zst") ||
		fileExists("/lib/modules/"+rel+"/kernel/drivers/net/wireguard/wireguard.ko.xz") ||
		fileExists("/lib/modules/"+rel+"/kernel/drivers/net/wireguard/wireguard.ko.gz"))
}
