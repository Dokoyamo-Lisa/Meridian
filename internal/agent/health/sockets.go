package health

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"meridian/internal/proto"
)

// sock is one socket from /proc/net.
type sock struct {
	net    string // tcp | udp
	local  netip.AddrPort
	remote netip.AddrPort
	state  string // hex: 0A = TCP listening, 01 = established, 07 = UDP unconnected
	inode  uint64
}

const (
	maxSockets   = 20000   // sockets the check keeps from one scan (it keeps only listening ones and those to pool ports)
	maxSockLines = 2000000 // lines it reads from each /proc/net file at most
	maxFDs       = 200000  // file descriptors it looks at to find a socket's program
)

// readSockets reads /proc/net/{tcp,tcp6,udp,udp6} line by line and keeps the sockets the check looks
// at: listening ones, and TCP connections to mining pool ports. A busy server has hundreds of
// thousands of connections; none of the others is ever held in memory.
func (m *Monitor) readSockets() []sock {
	var out []sock
	for _, f := range []struct{ file, net string }{{"tcp", "tcp"}, {"tcp6", "tcp"}, {"udp", "udp"}, {"udp6", "udp"}} {
		fh, err := os.Open(m.path(filepath.Join("proc", "net", f.file)))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 4096), 4096)
		for n := 0; sc.Scan() && n < maxSockLines; n++ {
			if n == 0 {
				continue // the header
			}
			x := strings.Fields(sc.Text())
			if len(x) < 10 {
				continue
			}
			switch st := x[3]; {
			case f.net == "tcp" && st == "0A", f.net == "udp" && st == "07":
			case f.net == "tcp" && st == "01":
				_, p, _ := strings.Cut(x[2], ":")
				if port, err := strconv.ParseUint(p, 16, 16); err != nil || !slices.Contains(poolPorts, uint16(port)) {
					continue
				}
			default:
				continue
			}
			local, ok1 := parseHexAddr(x[1])
			remote, ok2 := parseHexAddr(x[2])
			inode, err := strconv.ParseUint(x[9], 10, 64)
			if !ok1 || !ok2 || err != nil {
				continue
			}
			out = append(out, sock{net: f.net, local: local, remote: remote, state: x[3], inode: inode})
			if len(out) == maxSockets {
				fh.Close()
				return out
			}
		}
		fh.Close()
	}
	return out
}

// parseHexAddr reads "0100007F:0016": the address in the kernel's byte order, 32 bits at a time.
func parseHexAddr(s string) (netip.AddrPort, bool) {
	a, p, ok := strings.Cut(s, ":")
	if !ok {
		return netip.AddrPort{}, false
	}
	port, err := strconv.ParseUint(p, 16, 16)
	if err != nil {
		return netip.AddrPort{}, false
	}
	raw, err := hex.DecodeString(a)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return netip.AddrPort{}, false
	}
	for i := 0; i+4 <= len(raw); i += 4 { // each 32-bit word is little-endian
		raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	addr, _ := netip.AddrFromSlice(raw)
	return netip.AddrPortFrom(addr.Unmap(), uint16(port)), true
}

// listening says whether a socket waits for others: a listening TCP socket, or an unconnected UDP one.
func (s sock) listening() bool {
	if s.net == "tcp" {
		return s.state == "0A"
	}
	return s.state == "07" && s.remote.Port() == 0 && s.remote.Addr().IsUnspecified()
}

// poolPorts are ports crypto-mining pools take connections on.
var poolPorts = []uint16{3333, 3334, 3335, 3336, 3357, 4444, 5555, 5588, 6666, 7777, 9999, 10128, 10343, 14433, 14444,
	20128, 20580, 45560, 45700}

// ephemeral is the range the kernel picks client ports from: an unconnected UDP socket there is a
// program sending, not a service listening.
func (m *Monitor) ephemeral() (int, int) {
	b, err := readSmall(m.path("proc/sys/net/ipv4/ip_local_port_range"), 64)
	if err == nil {
		f := strings.Fields(string(b))
		if len(f) == 2 {
			lo, err1 := strconv.Atoi(f[0])
			hi, err2 := strconv.Atoi(f[1])
			if err1 == nil && err2 == nil && lo > 0 && hi >= lo {
				return lo, hi
			}
		}
	}
	return 32768, 60999
}

// checkSockets looks for ports that opened after the baseline and belong to nobody known, and for
// connections from programs other than Meridian's to mining pool ports.
func (sc *scan) checkSockets() {
	socks := sc.m.readSockets()
	lo, hi := sc.m.ephemeral()
	type listener struct {
		key    string
		addr   netip.AddrPort
		inodes []uint64
	}
	var listens []*listener
	byKey := map[string]*listener{}
	var pools []sock
	need := map[uint64]bool{}
	ports := map[string]bool{}
	for _, s := range socks {
		switch {
		case s.listening():
			if s.local.Addr().IsLoopback() {
				continue // reachable from this server only
			}
			port := int(s.local.Port())
			if s.net == "udp" && port >= lo && port <= hi {
				continue // a program's sending socket, not a service
			}
			key := s.net + ":" + strconv.Itoa(port)
			ports[key] = true
			if sc.first || slices.Contains(sc.prev.Ports, key) || sc.own.Ports[key] {
				continue
			}
			l := byKey[key]
			if l == nil {
				l = &listener{key: key, addr: s.local}
				byKey[key] = l
				listens = append(listens, l)
			}
			if s.inode != 0 {
				l.inodes = append(l.inodes, s.inode)
				need[s.inode] = true
			}
		case s.net == "tcp" && s.state == "01" && slices.Contains(poolPorts, s.remote.Port()):
			pools = append(pools, s)
			need[s.inode] = true
		}
	}
	if sc.first {
		for k := range ports {
			sc.next.Ports = append(sc.next.Ports, k)
		}
		slices.Sort(sc.next.Ports)
	}
	if len(listens) == 0 && len(pools) == 0 {
		return
	}
	owners := sc.socketOwners(need, false)
	// a new port nobody else holds may be a core's (Xray holds a socket per connection: only now)
	deep := map[uint64]bool{}
	for _, l := range listens {
		for _, ino := range l.inodes {
			if owners[ino] == nil {
				deep[ino] = true
			}
		}
	}
	if len(deep) > 0 {
		for ino, p := range sc.socketOwners(deep, true) {
			owners[ino] = p
		}
	}
	for _, l := range listens {
		var owner *proc
		for _, ino := range l.inodes {
			if owner = owners[ino]; owner != nil {
				break
			}
		}
		if owner != nil && (sc.isOwn(owner) || exactName(owner.comm, systemNames) || exactName(filepath.Base(owner.exe), systemNames)) {
			continue // Meridian's, or a system service (sshd, a resolver, a time server...): normal
		}
		netw, port, _ := strings.Cut(l.key, ":")
		where := "on all addresses"
		if !l.addr.Addr().IsUnspecified() {
			where = "on " + l.addr.Addr().String()
		}
		detail := "No program holds it - the kernel itself listens (a tunnel or a kernel module)"
		if owner != nil {
			detail = sc.describe(owner) + ", listening " + where
		}
		sc.last(proto.Finding{Key: "port:" + l.key, Kind: proto.FindPort, Severity: proto.SevWarning,
			Title:  fmt.Sprintf("A new port is open: %s/%s", port, netw),
			Detail: detail + ". It was closed when the health check started and is not one of Meridian's. Mark it expected if you opened it."})
	}
	for _, s := range pools {
		p := owners[s.inode]
		if p == nil || sc.isOwn(p) {
			continue // Meridian's own (users' traffic goes anywhere), or gone
		}
		name := nz(p.comm, filepath.Base(p.exe))
		sev := proto.SevHigh
		if nameIs(name, minerNames) {
			sev = proto.SevCritical
		}
		sc.last(proto.Finding{Key: "pool:" + strings.ToLower(name), Kind: proto.FindMiner, Severity: sev,
			Title:  name + " is connected to a mining pool port",
			Detail: fmt.Sprintf("%s, connected to %s - a port crypto-mining pools use.", sc.describe(p), s.remote)})
	}
}

// socketOwners finds the processes holding the given sockets, through /proc/<pid>/fd: those of
// every program but Meridian's cores (the agent's own included), or with cores only those of the
// cores - Xray may hold tens of thousands of connections, so they are looked through only for a
// socket nobody else holds.
func (sc *scan) socketOwners(need map[uint64]bool, cores bool) map[uint64]*proc {
	out := map[uint64]*proc{}
	budget := maxFDs
	for _, p := range sc.procs {
		if len(out) == len(need) {
			break
		}
		if p.kthread || sc.isCore(p) != cores {
			continue
		}
		dir := sc.m.path(filepath.Join("proc", strconv.Itoa(p.pid), "fd"))
		fds, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			if budget--; budget < 0 {
				return out
			}
			link, err := os.Readlink(filepath.Join(dir, fd.Name()))
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			ino, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]"), 10, 64)
			if err == nil && need[ino] && out[ino] == nil {
				out[ino] = p
			}
		}
	}
	return out
}
