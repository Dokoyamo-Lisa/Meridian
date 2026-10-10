package health

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"meridian/internal/proto"
)

// proc is one running process, as /proc shows it.
type proc struct {
	pid     int
	comm    string // the name the kernel shows (at most 15 characters)
	exe     string // its program file ("" for kernel threads)
	deleted bool   // the program file was deleted after the process started
	kthread bool
	ticks   uint64 // processor time used, in clock ticks
	start   uint64 // when it started, in ticks since boot: tells a reused pid apart
}

type procKey struct {
	pid   int
	start uint64
}

type cpuSample struct {
	ticks   uint64
	at      time.Time
	hotFrom time.Time // since when it has been busy without a break (zero: it is not)
}

const (
	maxProcs = 8192
	clkTck   = 100 // USER_HZ: what /proc counts processor time in on Linux
	// a process is busy when it uses this share of one core over a scan, and a finding when it was
	// busy for hotFor
	busyShare = 0.9
	hotFor    = 10 * time.Minute
)

// readProcs lists the running processes.
func (m *Monitor) readProcs() []*proc {
	dirs, _ := os.ReadDir(m.path("proc"))
	var out []*proc
	for _, d := range dirs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil || pid <= 0 {
			continue
		}
		if len(out) == maxProcs {
			break
		}
		p := m.readProc(pid)
		if p != nil {
			out = append(out, p)
		}
	}
	return out
}

func (m *Monitor) readProc(pid int) *proc {
	base := m.path(filepath.Join("proc", strconv.Itoa(pid)))
	stat, err := readSmall(filepath.Join(base, "stat"), 4096)
	if err != nil {
		return nil
	}
	s := string(stat)
	// the name may hold spaces and parentheses: the fields follow the last ')'
	i, j := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if i < 0 || j < i {
		return nil
	}
	f := strings.Fields(s[j+1:])
	if len(f) < 20 {
		return nil
	}
	p := &proc{pid: pid, comm: cleanText(s[i+1:j], 32)}
	flags, _ := strconv.ParseUint(f[6], 10, 64)
	ut, _ := strconv.ParseUint(f[11], 10, 64)
	st, _ := strconv.ParseUint(f[12], 10, 64)
	p.start, _ = strconv.ParseUint(f[19], 10, 64)
	ppid, _ := strconv.Atoi(f[1])
	p.ticks = ut + st
	p.kthread = flags&0x00200000 != 0 || pid == 2 || ppid == 2 // PF_KTHREAD, kthreadd and its children
	if c, err := readSmall(filepath.Join(base, "comm"), 64); err == nil {
		p.comm = cleanText(strings.TrimSpace(string(c)), 32)
	}
	if link, err := os.Readlink(filepath.Join(base, "exe")); err == nil {
		if cut, ok := strings.CutSuffix(link, " (deleted)"); ok {
			link, p.deleted = cut, true
		}
		p.exe = cleanText(link, 512)
	}
	return p
}

// isOwn says whether a process is one of Meridian's: the agent, or a core it installed.
func (sc *scan) isOwn(p *proc) bool {
	if p.exe == "" {
		return false
	}
	if sc.own.Agent != "" && p.exe == sc.own.Agent {
		return true
	}
	return sc.own.Data != "" && strings.HasPrefix(p.exe, filepath.Join(sc.own.Data, "cores")+"/")
}

// isCore says whether a process is one of the cores Meridian installed (Xray, Hysteria, realm).
func (sc *scan) isCore(p *proc) bool {
	return sc.isOwn(p) && p.exe != sc.own.Agent
}

// meridianNames are Meridian's programs: busy by design, never a finding for their processor time.
var meridianNames = []string{"meridian-agent", "xray", "hysteria", "realm", "mita", "snell"}

// systemNames are common system services: busy now and then, and listening by design.
var systemNames = []string{
	"systemd", "systemd-journal", "systemd-journald", "systemd-udevd", "systemd-network", "systemd-networkd",
	"systemd-resolve", "systemd-resolved", "systemd-logind", "systemd-timesyn", "systemd-timesyncd", "init", "dbus-daemon",
	"dbus-broker", "sshd", "sshd-session", "cron", "crond", "atd", "rsyslogd", "syslog-ng", "syslogd", "klogd", "chronyd",
	"ntpd", "ntpdate", "irqbalance", "snapd", "unattended-upgr", "unattended-upgrade", "apt", "apt-get", "apt-check",
	"dpkg", "packagekitd", "apk", "dnf", "yum", "rpm", "needrestart", "fwupd", "multipathd", "polkitd", "udisksd",
	"networkd-dispat", "NetworkManager", "dhclient", "dhcpcd", "udhcpc", "avahi-daemon", "rpcbind", "rpc.statd",
	"cupsd", "master", "exim4", "sendmail", "postfix", "qemu-ga", "google_guest_ag", "google_osconfig", "amazon-ssm-agen",
	"cloud-init", "walinuxagent", "waagent", "acpid", "agetty", "getty", "login", "containerd", "dockerd",
	"tuned", "auditd", "firewalld", "lxcfs", "mdadm", "smartd", "haveged", "rngd", "logrotate", "man-db", "mandb",
	"updatedb", "updatedb.mlocat", "plocate-build", "e2scrub", "fstrim", "xfs_scrub", "btrfs", "nscd", "sssd",
}

// minerNames are crypto-miners by the names they usually run under.
var minerNames = []string{
	"xmrig", "xmr-stak", "xmrstak", "kinsing", "kdevtmpfsi", "minerd", "cpuminer", "nanominer", "lolminer", "t-rex",
	"nbminer", "phoenixminer", "ethminer", "gminer", "teamredminer", "srbminer", "ccminer", "cgminer", "bfgminer",
	"xmrminer", "nheqminer", "xmr-proxy", "moneroocean", "c3pool", "kthreaddi", "kthreaddk", "dbused",
}

// kernelNames are kernel threads' names: a program with a file that uses one hides behind it.
var kernelNames = []string{"kworker", "kswapd", "ksoftirqd", "kthreadd", "migration/", "rcu_", "kdevtmpfs", "khugepaged",
	"jbd2/", "kcompactd", "watchdog/", "kblockd", "writeback", "kauditd"}

// minerArgs give a miner away in its command line whatever it is called.
var minerArgs = []string{"stratum+tcp://", "stratum+ssl://", "stratum+tls://", "stratum2+tcp://", "--donate-level",
	"--randomx", "--coin=monero", "--algo=rx/0", "-a rx/0", "--cpu-max-threads-hint"}

var stratumRE = regexp.MustCompile(`stratum2?\+(?:tcp|ssl|tls)://([A-Za-z0-9.\-]{1,253}:[0-9]{1,5})`)

func nameIs(name string, list []string) bool {
	n := strings.ToLower(name)
	for _, x := range list {
		if strings.HasPrefix(n, strings.ToLower(x)) {
			return true
		}
	}
	return false
}

func exactName(name string, list []string) bool {
	return slices.Contains(list, name)
}

// tmpDirs are where nothing should run from.
var tmpDirs = []string{"/tmp/", "/var/tmp/", "/dev/shm/"}

func inTmp(path string) bool {
	for _, d := range tmpDirs {
		if strings.HasPrefix(path, d) {
			return true
		}
	}
	return false
}

// checkProcesses looks at every process: known miners (critical), programs pretending to be kernel
// threads, run from a temporary folder or whose file was deleted (high), and programs that kept a
// processor busy for ten minutes (a warning). One finding per process, the most serious.
func (sc *scan) checkProcesses() {
	m := sc.m
	m.mu.Lock()
	prevCPU := m.cpu
	m.mu.Unlock()
	nextCPU := map[procKey]cpuSample{}
	for _, p := range sc.procs {
		k := procKey{p.pid, p.start}
		smp := cpuSample{ticks: p.ticks, at: sc.at}
		busy := 0.0
		if old, ok := prevCPU[k]; ok && sc.at.Sub(old.at) >= time.Minute && p.ticks >= old.ticks {
			busy = float64(p.ticks-old.ticks) / clkTck / sc.at.Sub(old.at).Seconds()
			if busy >= busyShare {
				smp.hotFrom = old.hotFrom
				if smp.hotFrom.IsZero() {
					smp.hotFrom = old.at
				}
			}
		}
		if len(nextCPU) < maxProcs {
			nextCPU[k] = smp
		}
		if p.kthread || sc.isOwn(p) {
			continue
		}
		if p.exe == "" && p.comm == "" {
			continue
		}
		base := filepath.Base(p.exe)
		who := sc.describe(p)
		switch {
		case nameIs(p.comm, minerNames) || (p.exe != "" && nameIs(base, minerNames)):
			sc.last(proto.Finding{Key: "miner:" + strings.ToLower(nz(p.comm, base)), Kind: proto.FindMiner, Severity: proto.SevCritical,
				Title:  "A crypto-miner is running: " + nz(p.comm, base),
				Detail: who + sc.pool(p) + ". Stop it, then look for how it got in: new SSH keys, accounts, scheduled tasks and services."})
		case sc.minerArgs(p):
			sc.last(proto.Finding{Key: "miner:" + strings.ToLower(nz(p.comm, base)), Kind: proto.FindMiner, Severity: proto.SevCritical,
				Title:  "A crypto-miner is running: " + nz(p.comm, base),
				Detail: who + sc.pool(p) + " - its command line is a miner's. Stop it, then look for how it got in: new SSH keys, accounts, scheduled tasks and services."})
		case p.exe != "" && nameIs(p.comm, kernelNames):
			sc.last(proto.Finding{Key: "disguise:" + p.exe, Kind: proto.FindProcess, Severity: proto.SevHigh,
				Title:  "A program poses as a kernel thread: " + p.comm,
				Detail: who + ". Real kernel threads have no program file - malware hides like this."})
		case inTmp(p.exe):
			sc.last(proto.Finding{Key: "tmp:" + p.exe, Kind: proto.FindProcess, Severity: proto.SevHigh,
				Title:  "A program runs from a temporary folder: " + p.exe,
				Detail: who + ". Programs are installed elsewhere; malware is often dropped in /tmp, /var/tmp or /dev/shm."})
		case p.deleted && p.exe != "" && !exists(sc.m.path(p.exe)):
			// a file that is gone (not an upgrade that replaced it: the new file is then in place)
			sc.last(proto.Finding{Key: "deleted:" + p.exe, Kind: proto.FindProcess, Severity: proto.SevHigh,
				Title:  "A program runs whose file was deleted: " + nz(p.comm, base),
				Detail: who + ". Its file " + p.exe + " no longer exists - malware deletes itself after it starts."})
		case !smp.hotFrom.IsZero() && sc.at.Sub(smp.hotFrom) >= hotFor && !exactName(p.comm, meridianNames) &&
			!exactName(p.comm, systemNames) && !exactName(base, systemNames):
			mins := int(sc.at.Sub(smp.hotFrom).Minutes())
			sc.last(proto.Finding{Key: "cpu:" + strings.ToLower(nz(p.comm, base)), Kind: proto.FindCPU, Severity: proto.SevWarning,
				Title: fmt.Sprintf("%s kept a processor busy for %d minutes", nz(p.comm, base), mins),
				Detail: fmt.Sprintf("%s, using %.0f%% of a processor core. If you do not know this program, look at it: crypto-miners do this.",
					who, busy*100)})
		}
	}
	m.mu.Lock()
	m.cpu = nextCPU
	m.mu.Unlock()
}

// describe says which process it is, in a few words.
func (sc *scan) describe(p *proc) string {
	s := fmt.Sprintf("Process %d", p.pid)
	if p.exe != "" {
		s += " runs " + p.exe
	} else {
		s += " (" + p.comm + ")"
	}
	if u := sc.processUser(p.pid); u != "" {
		s += " as " + u
	}
	return s
}

// processUser is the account a process runs as.
func (sc *scan) processUser(pid int) string {
	b, err := readSmall(sc.m.path(filepath.Join("proc", strconv.Itoa(pid), "status")), 8192)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "Uid:"); ok {
			f := strings.Fields(v)
			if len(f) == 0 {
				return ""
			}
			if name := sc.userName(f[0]); name != "" {
				return name
			}
			return "uid " + f[0]
		}
	}
	return ""
}

// userName is the account with a uid, from /etc/passwd as this scan read it.
func (sc *scan) userName(uid string) string {
	for name, a := range sc.next.Users {
		if strconv.Itoa(a.UID) == uid {
			return name
		}
	}
	for name, a := range sc.prev.Users {
		if strconv.Itoa(a.UID) == uid {
			return name
		}
	}
	return ""
}

// cmdline is a process's command line (at most 4 KB). It may hold secrets: it is only searched,
// never sent.
func (sc *scan) cmdline(pid int) string {
	b, err := readSmall(sc.m.path(filepath.Join("proc", strconv.Itoa(pid), "cmdline")), 4096)
	if err != nil {
		return ""
	}
	return strings.ReplaceAll(string(b), "\x00", " ")
}

func (sc *scan) minerArgs(p *proc) bool {
	c := strings.ToLower(sc.cmdline(p.pid))
	for _, a := range minerArgs {
		if strings.Contains(c, a) {
			return true
		}
	}
	return false
}

// pool names the mining pool in a miner's command line, if it has one.
func (sc *scan) pool(p *proc) string {
	if m := stratumRE.FindStringSubmatch(sc.cmdline(p.pid)); m != nil {
		return ", mining for the pool " + m[1]
	}
	return ""
}

func nz(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
