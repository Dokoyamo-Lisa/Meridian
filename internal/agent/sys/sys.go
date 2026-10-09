// Package sys reads host facts and metrics from /proc. Linux only at runtime; on other systems the
// functions return zero values so the agent can still be built and unit-tested anywhere.
package sys

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
	h.Virt = detectVirt(readFile, fileExists)
	pub := LastPublic(context.Background())
	h.IPv4, h.IPv6, h.IPv4Gone, h.IPv6Gone = pub.IPv4, pub.IPv6, pub.IPv4Gone, pub.IPv6Gone
	h.Addrs = LocalAddrs()
	return h
}

// HasAddr reports whether one of the host's interfaces carries a. A core bound to an address the host
// does not have cannot start, so the agent leaves such a protocol out until the address is back.
func HasAddr(a netip.Addr) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return true // cannot tell: leave nothing out
	}
	for _, x := range addrs {
		if p, err := netip.ParsePrefix(x.String()); err == nil && p.Addr().Unmap() == a.Unmap() {
			return true
		}
	}
	return false
}

// MissingAddr reports whether a protocol bound to bind cannot start here: bind is a specific address
// (not "any", not loopback) that the host does not have. has nil means HasAddr.
func MissingAddr(bind string, has func(netip.Addr) bool) (netip.Addr, bool) {
	a, err := netip.ParseAddr(strings.TrimSpace(bind))
	if err != nil || a.IsUnspecified() || a.IsLoopback() {
		return netip.Addr{}, false
	}
	if has == nil {
		has = HasAddr
	}
	return a, !has(a)
}

// LocalAddrs lists the addresses on the host's own interfaces that a protocol can be bound to:
// global ones (public, or private behind a provider's NAT), not loopback, link-local, multicast or
// the addresses of Meridian's own WireGuard interfaces.
func LocalAddrs() []string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, ifc := range ifs {
		// containers' bridges, VPNs and tunnels are no address the internet reaches this server at
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || skipIface(ifc.Name) {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			pfx, err := netip.ParsePrefix(a.String())
			if err != nil {
				continue
			}
			ip := pfx.Addr().Unmap()
			if !ip.IsGlobalUnicast() || ip.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, ip.String())
		}
	}
	sort.Strings(out)
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

// IPv6Off says whether the kernel has IPv6 turned off: booted with ipv6.disable=1 (no IPv6 at all)
// or disable_ipv6 set for all interfaces.
func IPv6Off() bool {
	if !fileExists("/proc/net/if_inet6") {
		return true
	}
	return strings.TrimSpace(readFile("/proc/sys/net/ipv6/conf/all/disable_ipv6")) == "1"
}

// BootID tells boots apart: the kernel's counters start over with each one ("" off Linux).
func BootID() string { return strings.TrimSpace(readFile("/proc/sys/kernel/random/boot_id")) }

// LogPos is where reading a log stopped: the file (its inode) and how far into it.
type LogPos struct {
	ID  uint64 `json:"id"`
	Off int64  `json:"off"`
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

// detectVirt says what the host runs in - a container first (the machine under it does not show from
// inside), then a virtual machine by its firmware's maker, then the processor's hypervisor flag.
func detectVirt(read func(string) string, exists func(string) bool) string {
	if exists("/proc/vz") && !exists("/proc/bc") {
		return "openvz"
	}
	if c := virtWord(read("/run/systemd/container")); c != "" {
		return c
	}
	for _, kv := range strings.Split(read("/proc/1/environ"), "\x00") {
		if v, ok := strings.CutPrefix(kv, "container="); ok && virtWord(v) != "" {
			return virtWord(v)
		}
	}
	if exists("/.dockerenv") {
		return "docker"
	}
	if strings.Contains(strings.ToLower(read("/proc/sys/kernel/osrelease")), "microsoft") {
		return "wsl"
	}
	vendor := strings.ToLower(read("/sys/class/dmi/id/sys_vendor"))
	product := strings.ToLower(read("/sys/class/dmi/id/product_name"))
	dmi := vendor + " " + product + " " + strings.ToLower(read("/sys/class/dmi/id/bios_vendor"))
	if strings.Contains(vendor, "microsoft") && strings.Contains(product, "virtual machine") {
		return "hyper-v"
	}
	for _, m := range [][2]string{{"kvm", "kvm"}, {"qemu", "kvm"}, {"bochs", "kvm"}, {"openstack", "kvm"}, {"google compute", "kvm"},
		{"amazon ec2", "kvm"}, {"digitalocean", "kvm"}, {"hetzner", "kvm"}, {"vultr", "kvm"}, {"linode", "kvm"}, {"alibaba cloud", "kvm"},
		{"vmware", "vmware"}, {"virtualbox", "virtualbox"}, {"innotek", "virtualbox"}, {"xen", "xen"}, {"parallels", "parallels"},
		{"apple virtualization", "apple"}} {
		if strings.Contains(dmi, m[0]) {
			return m[1]
		}
	}
	if t := virtWord(read("/sys/hypervisor/type")); t != "" {
		return t
	}
	cpu := read("/proc/cpuinfo")
	for _, l := range strings.Split(cpu, "\n") {
		if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) == "flags" {
			if slices.Contains(strings.Fields(v), "hypervisor") {
				return "vm"
			}
			return "none"
		}
	}
	return ""
}

// virtWord keeps a name from the system as a plain word (lxc, docker, systemd-nspawn ...).
func virtWord(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" || len(v) > 24 {
		return ""
	}
	for _, r := range v {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return ""
		}
	}
	return v
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

func isPublic(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() &&
		!(ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1]&0xc0 == 64) // CGNAT
}

// ---------------------------------------------------------------- metrics

// Sampler computes rates between calls.
type Sampler struct {
	mu        sync.Mutex
	lastCPU   cpuTimes
	lastNet   netTotals
	lastDisk  diskTotals
	lastAt    time.Time
	rxRate    int64
	txRate    int64
	readRate  int64
	writeRate int64
	cpu       float64
	nicDeltaR int64
	nicDeltaT int64
}

type diskTotals struct{ read, write uint64 }

// wholeDisk says whether a /proc/diskstats name is a whole physical (or virtual) disk: not a
// partition, and not a loop, RAM, device-mapper or RAID device whose bytes the disks under it count.
func wholeDisk(name string) bool {
	for _, p := range []string{"loop", "ram", "zram", "dm-", "md", "sr", "fd", "nbd"} {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	switch {
	case strings.HasPrefix(name, "nvme"), strings.HasPrefix(name, "mmcblk"):
		return !strings.Contains(name[4:], "p") // nvme0n1p1, mmcblk0p1 are partitions
	case strings.HasPrefix(name, "sd"), strings.HasPrefix(name, "vd"), strings.HasPrefix(name, "xvd"), strings.HasPrefix(name, "hd"):
		last := name[len(name)-1]
		return last < '0' || last > '9' // sda1 is a partition
	}
	return false
}

// readDisk adds up the bytes the whole disks read and wrote (sectors of 512 bytes, as diskstats counts).
func readDisk() diskTotals {
	var t diskTotals
	for _, l := range strings.Split(readFile("/proc/diskstats"), "\n") {
		f := strings.Fields(l)
		if len(f) < 10 || !wholeDisk(f[2]) {
			continue
		}
		r, _ := strconv.ParseUint(f[5], 10, 64)
		w, _ := strconv.ParseUint(f[9], 10, 64)
		t.read += r * 512
		t.write += w * 512
	}
	return t
}

// temps reads the host's temperature sensors (hwmon, then thermal zones): °C by name, at most 12.
func temps() map[string]float64 {
	out := map[string]float64{}
	add := func(name string, milli string) {
		v, err := strconv.ParseFloat(strings.TrimSpace(milli), 64)
		if err != nil || v <= -50000 || v >= 200000 || len(out) >= 12 {
			return
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		for n, k := name, 2; ; k++ {
			if _, taken := out[n]; !taken {
				out[n] = round1(v / 1000)
				return
			}
			n = fmt.Sprintf("%s %d", name, k)
		}
	}
	hw, _ := filepath.Glob("/sys/class/hwmon/hwmon*/temp*_input")
	for _, p := range hw {
		dir := filepath.Dir(p)
		chip := strings.TrimSpace(readFile(filepath.Join(dir, "name")))
		label := strings.TrimSpace(readFile(strings.TrimSuffix(p, "_input") + "_label"))
		add(strings.TrimSpace(chip+" "+label), readFile(p))
	}
	if len(out) == 0 {
		tz, _ := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
		for _, p := range tz {
			add(readFile(filepath.Join(filepath.Dir(p), "type")), readFile(p))
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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

// skipIface leaves out loopback and virtual interfaces: host traffic is counted once, and their
// addresses (containers, VPNs, tunnels) are never offered as a protocol's own.
func skipIface(name string) bool {
	for _, p := range []string{"lo", "uwg", "wg", "docker", "veth", "br-", "virbr", "tun", "tap", "lxc", "lxd", "cni", "flannel", "cali",
		"vnet", "incus", "tailscale", "zt", "podman", "kube"} {
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
	d := readDisk()
	if !s.lastAt.IsZero() {
		dt := now.Sub(s.lastAt).Seconds()
		if dt > 0 {
			if d.read >= s.lastDisk.read && d.write >= s.lastDisk.write {
				s.readRate, s.writeRate = int64(float64(d.read-s.lastDisk.read)/dt), int64(float64(d.write-s.lastDisk.write)/dt)
			}
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
	s.lastCPU, s.lastNet, s.lastDisk, s.lastAt = c, n, d, now
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
	cpu, rx, tx, dr, dw := s.cpu, s.rxRate, s.txRate, s.readRate, s.writeRate
	s.mu.Unlock()
	m := memInfo()
	out := proto.Sys{CPU: round1(cpu), MemTotal: m["MemTotal"], SwapTotal: m["SwapTotal"], RXRate: rx, TXRate: tx, DiskRead: dr, DiskWrite: dw,
		Temps: temps()}
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
	c.NoIPv6 = IPv6Off()
	c.WG6 = c.Nftables && !c.NoIPv6 // IPv6 through WireGuard needs NAT66 and forwarding
	c.NAT64 = NAT64()
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
