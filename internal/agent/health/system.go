package health

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"meridian/internal/proto"
)

// account is one line of /etc/passwd, as far as the check cares.
type account struct {
	UID   int    `json:"uid"`
	Home  string `json:"home"`
	Shell string `json:"shell"`
}

// canLogIn says whether an account has a shell people can sign in with.
func (a account) canLogIn() bool {
	s := filepath.Base(a.Shell)
	return a.Shell != "" && s != "nologin" && s != "false" && s != "sync" && s != "halt" && s != "shutdown"
}

const maxAccounts = 5000

func (m *Monitor) readAccounts() map[string]account {
	out := map[string]account{}
	b, err := readSmall(m.path("etc/passwd"), 4<<20)
	if err != nil {
		return out
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Split(l, ":")
		if len(f) < 7 || f[0] == "" || strings.HasPrefix(f[0], "#") || strings.HasPrefix(f[0], "+") || strings.HasPrefix(f[0], "-") {
			continue
		}
		uid, err := strconv.Atoi(f[2])
		if err != nil {
			continue
		}
		out[cleanText(f[0], 64)] = account{UID: uid, Home: cleanText(f[5], 256), Shell: cleanText(f[6], 256)}
		if len(out) == maxAccounts {
			break
		}
	}
	return out
}

// checkAccounts: a second account with uid 0 (critical), new accounts (high, or a warning when they
// cannot sign in), accounts that gained a login shell or uid 0, and passwords that changed.
func (sc *scan) checkAccounts() {
	users := sc.m.readAccounts()
	sc.next.Users = users
	names := make([]string, 0, len(users))
	for n := range users {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		a := users[n]
		if a.UID == 0 && n != "root" {
			sc.last(proto.Finding{Key: "uid0:" + n, Kind: proto.FindAccount, Severity: proto.SevCritical,
				Title:  "A second administrator account: " + n,
				Detail: n + " has uid 0 like root, so it has full control of the server. Remove it unless you made it."})
		}
		if sc.first {
			continue
		}
		old, had := sc.prev.Users[n]
		switch {
		case !had:
			sev, how := proto.SevWarning, "It has no login shell, so it cannot sign in."
			if a.canLogIn() {
				sev, how = proto.SevHigh, "It can sign in (shell "+a.Shell+")."
			}
			sc.event(proto.Finding{Key: "account:" + n, Kind: proto.FindAccount, Severity: sev,
				Title:  "A new account was added: " + n,
				Detail: fmt.Sprintf("uid %d, home %s. %s Remove it unless you or a program you installed made it.", a.UID, a.Home, how)})
		case !old.canLogIn() && a.canLogIn():
			sc.event(proto.Finding{Key: "account:" + n, Kind: proto.FindAccount, Severity: proto.SevHigh,
				Title:  n + " can sign in now",
				Detail: fmt.Sprintf("Its shell changed from %s to %s.", nz(old.Shell, "none"), a.Shell)})
		}
	}
	sc.checkPasswords(users)
}

// checkPasswords compares a digest of each account's password hash (the hash itself is never kept).
func (sc *scan) checkPasswords(users map[string]account) {
	sc.next.Shadow = map[string]string{}
	b, err := readSmall(sc.m.path("etc/shadow"), 4<<20)
	if err != nil {
		return
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Split(l, ":")
		if len(f) < 2 || f[0] == "" {
			continue
		}
		name := cleanText(f[0], 64)
		state := "locked"
		if usable(f[1]) {
			sum := sha256.Sum256([]byte(f[1]))
			state = hex.EncodeToString(sum[:12])
		}
		sc.next.Shadow[name] = state
		if len(sc.next.Shadow) == maxAccounts {
			break
		}
		old, had := sc.prev.Shadow[name]
		if sc.first || !had || old == state || state == "locked" {
			continue // unchanged, a new account (reported as such), or locked
		}
		sev := proto.SevWarning
		if users[name].UID == 0 {
			sev = proto.SevHigh
		}
		if old == "locked" {
			sc.event(proto.Finding{Key: "password:" + name, Kind: proto.FindAccount, Severity: proto.SevHigh,
				Title:  name + " can sign in with a password now",
				Detail: "The account had no usable password before. If you did not set one, someone else did."})
			continue
		}
		sc.event(proto.Finding{Key: "password:" + name, Kind: proto.FindAccount, Severity: sev,
			Title:  "The password of " + name + " changed",
			Detail: "If you did not change it, change it now and look at the SSH sign-ins."})
	}
}

// usable says whether a password field lets someone sign in with a password.
func usable(field string) bool {
	return field != "" && !strings.HasPrefix(field, "!") && !strings.HasPrefix(field, "*")
}

// ---------------------------------------------------------------- SSH keys

// keyFiles are the authorized_keys files of root and of every account that can sign in.
func (sc *scan) keyFiles() map[string]string {
	out := map[string]string{}
	for name, a := range sc.next.Users {
		if a.Home == "" || a.Home == "/" || (!a.canLogIn() && a.UID != 0) {
			continue
		}
		for _, f := range []string{"authorized_keys", "authorized_keys2"} {
			out[filepath.Join(a.Home, ".ssh", f)] = name
		}
	}
	for _, f := range []string{"authorized_keys", "authorized_keys2"} {
		out[filepath.Join("/root/.ssh", f)] = "root"
	}
	return out
}

// sshKey reads one authorized_keys line: its fingerprint (as ssh-keygen -l shows it), type and
// comment. Options before the key ("command=...") are skipped.
func sshKey(line string) (fp, typ, comment string, ok bool) {
	f := strings.Fields(line)
	for i, x := range f {
		if !strings.HasPrefix(x, "ssh-") && !strings.HasPrefix(x, "ecdsa-") && !strings.HasPrefix(x, "sk-") {
			continue
		}
		if i+1 >= len(f) {
			return "", "", "", false
		}
		blob, err := base64.StdEncoding.DecodeString(f[i+1])
		if err != nil || len(blob) < 16 {
			continue
		}
		sum := sha256.Sum256(blob)
		return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), cleanText(x, 40),
			cleanText(strings.Join(f[i+2:], " "), 60), true
	}
	return "", "", "", false
}

// checkKeys: an SSH key added to an account (high), or removed (info).
func (sc *scan) checkKeys() {
	sc.next.Keys = map[string][]string{}
	files := sc.keyFiles()
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		user := files[p]
		// a link is followed only to another authorized_keys file: an account's own link never makes
		// this root-run check read and report some other file
		if fi, err := os.Lstat(sc.m.path(p)); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			if t, err := filepath.EvalSymlinks(sc.m.path(p)); err != nil || !strings.HasPrefix(filepath.Base(t), "authorized_keys") {
				continue
			}
		}
		b, err := readSmall(sc.m.path(p), 1<<20)
		if err != nil {
			continue
		}
		var fps []string
		type key struct{ fp, text string }
		var added []key
		old := sc.prev.Keys[p]
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			fp, typ, comment, ok := sshKey(l)
			if !ok || slices.Contains(fps, fp) {
				continue
			}
			fps = append(fps, fp)
			if !slices.Contains(old, fp) {
				added = append(added, key{fp, strings.TrimSpace(typ + " " + fp + " " + comment)})
			}
			if len(fps) == 500 {
				break
			}
		}
		sort.Strings(fps)
		sc.next.Keys[p] = fps
		if sc.first {
			continue
		}
		var removed int
		for _, fp := range old {
			if !slices.Contains(fps, fp) {
				removed++
			}
		}
		// one finding per key: one marked expected is never flagged again, a key added later still is
		for i, k := range added {
			if i == 5 {
				sc.event(proto.Finding{Key: "ssh_keys:" + user + ":more", Kind: proto.FindSSHKeys, Severity: proto.SevHigh,
					Title:  fmt.Sprintf("%d more new SSH keys can sign in as %s", len(added)-i, user),
					Detail: "Added to " + p + ". Remove them unless they are yours."})
				break
			}
			sc.event(proto.Finding{Key: "ssh_keys:" + user + ":" + k.fp, Kind: proto.FindSSHKeys, Severity: proto.SevHigh,
				Title:  "A new SSH key can sign in as " + user,
				Detail: "Added to " + p + ": " + k.text + ". Remove it unless it is yours."})
		}
		if removed > 0 {
			sc.event(proto.Finding{Key: "ssh_keys:" + user + ":removed", Kind: proto.FindSSHKeys, Severity: proto.SevInfo,
				Title:  fmt.Sprintf("%d SSH key(s) removed from %s", removed, user),
				Detail: "From " + p + ". If you did not remove them, someone may be locking you out."})
		}
	}
	// a file that is gone keeps its keys known: the same file back with the same keys is no news
	for p, old := range sc.prev.Keys {
		if _, ok := sc.next.Keys[p]; !ok && len(old) > 0 && len(sc.next.Keys) < maxWatched {
			sc.next.Keys[p] = old
		}
	}
}

// ---------------------------------------------------------------- watched files

// watched is a file (or folder of files) whose changes are findings.
type watched struct {
	glob   string
	kind   string
	sev    string
	title  string // "<title>: <path>"
	lines  bool   // count the lines added and removed
	lasted bool   // (ld.so.preload) a condition while the file is there
}

var watchedFiles = []watched{
	{glob: "/etc/sudoers", kind: proto.FindAccount, sev: proto.SevHigh, title: "Administrator rights changed", lines: true},
	{glob: "/etc/sudoers.d/*", kind: proto.FindAccount, sev: proto.SevHigh, title: "Administrator rights changed", lines: true},
	{glob: "/etc/crontab", kind: proto.FindCron, sev: proto.SevWarning, title: "A scheduled task changed", lines: true},
	{glob: "/etc/anacrontab", kind: proto.FindCron, sev: proto.SevWarning, title: "A scheduled task changed", lines: true},
	{glob: "/etc/cron.d/*", kind: proto.FindCron, sev: proto.SevWarning, title: "A scheduled task changed", lines: true},
	{glob: "/etc/cron.hourly/*", kind: proto.FindCron, sev: proto.SevWarning, title: "A scheduled task changed", lines: true},
	{glob: "/etc/cron.daily/*", kind: proto.FindCron, sev: proto.SevWarning, title: "A scheduled task changed", lines: true},
	{glob: "/etc/cron.weekly/*", kind: proto.FindCron, sev: proto.SevWarning, title: "A scheduled task changed", lines: true},
	{glob: "/etc/cron.monthly/*", kind: proto.FindCron, sev: proto.SevWarning, title: "A scheduled task changed", lines: true},
	// people's own scheduled tasks: where malware puts itself (crontab -e)
	{glob: "/var/spool/cron/*", kind: proto.FindCron, sev: proto.SevHigh, title: "A user's scheduled tasks changed", lines: true},
	{glob: "/var/spool/cron/crontabs/*", kind: proto.FindCron, sev: proto.SevHigh, title: "A user's scheduled tasks changed", lines: true},
	{glob: "/etc/crontabs/*", kind: proto.FindCron, sev: proto.SevHigh, title: "A user's scheduled tasks changed", lines: true},
	{glob: "/etc/rc.local", kind: proto.FindService, sev: proto.SevWarning, title: "The start-up script changed", lines: true},
	// drop-ins change what an existing service runs, Meridian's included
	{glob: "/etc/systemd/system/*.d/*.conf", kind: proto.FindService, sev: proto.SevWarning, title: "A service's settings changed", lines: true},
	{glob: "/etc/ssh/sshd_config", kind: proto.FindSSH, sev: proto.SevWarning, title: "The SSH server's settings changed", lines: true},
	{glob: "/etc/ssh/sshd_config.d/*", kind: proto.FindSSH, sev: proto.SevWarning, title: "The SSH server's settings changed", lines: true},
	{glob: "/etc/ld.so.preload", kind: proto.FindPreload, sev: proto.SevCritical, lasted: true},
}

const maxWatched = 2000

// checkFiles hashes the watched files: one that is new or changed since the last scan is an event;
// /etc/ld.so.preload with anything in it is a critical condition of its own.
func (sc *scan) checkFiles() {
	sc.next.Files = map[string]fileSum{}
	for _, w := range watchedFiles {
		paths, _ := filepath.Glob(sc.m.path(w.glob))
		sort.Strings(paths)
		for _, full := range paths {
			if len(sc.next.Files) >= maxWatched {
				return
			}
			p := "/" + strings.TrimPrefix(filepath.ToSlash(strings.TrimPrefix(full, filepath.Clean(sc.m.Root))), "/")
			shown := cleanText(p, 300) // a name with a line break in it must not garble the finding
			fi, err := os.Stat(full)
			if err != nil || !fi.Mode().IsRegular() {
				continue
			}
			old, had := sc.prev.Files[p]
			sum, content := sumSmall(full, fi, w.lines)
			if sum.SHA == "" {
				continue
			}
			sc.next.Files[p] = sum
			if w.lasted {
				sc.preload(shown, content, had && old.SHA != sum.SHA)
				continue
			}
			if sc.first || (had && old.SHA == sum.SHA) {
				continue
			}
			what := "A new file."
			if had {
				what = linesChanged(old.Lines, sum.Lines)
			}
			sc.event(proto.Finding{Key: "file:" + shown, Kind: w.kind, Severity: w.sev, Title: w.title + ": " + shown,
				Detail: what + " Look at the file on the server unless you changed it."})
		}
	}
}

// preload: /etc/ld.so.preload makes every program load the libraries it lists - what rootkits use.
func (sc *scan) preload(p string, content []byte, changed bool) {
	var libs []string
	for _, l := range strings.Split(string(content), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			libs = append(libs, cleanText(l, 200))
		}
	}
	if len(libs) == 0 {
		return
	}
	f := proto.Finding{Key: "preload", Kind: proto.FindPreload, Severity: proto.SevCritical,
		Title:  p + " makes every program load a library",
		Detail: "It lists " + cleanText(strings.Join(libs, ", "), 400) + ". Rootkits hide like this - remove the file unless you put it there."}
	sc.last(f)
	if changed && !sc.first {
		sc.event(f) // changed while it was there: happened again
	}
}

// linesChanged says how many lines were added and removed, from the lines' digests.
func linesChanged(old, cur []string) string {
	count := func(a, b []string) int {
		n := 0
		left := map[string]int{}
		for _, x := range b {
			left[x]++
		}
		for _, x := range a {
			if left[x] > 0 {
				left[x]--
				continue
			}
			n++
		}
		return n
	}
	add, del := count(cur, old), count(old, cur)
	switch {
	case add == 0 && del == 0:
		return "Only its comments or layout changed."
	case del == 0:
		return fmt.Sprintf("%d line(s) added.", add)
	case add == 0:
		return fmt.Sprintf("%d line(s) removed.", del)
	}
	return fmt.Sprintf("%d line(s) added, %d removed.", add, del)
}

// ---------------------------------------------------------------- services and modules

// enabledServices lists the services set to start at boot: systemd's wanted units (the system's and
// people's own) and OpenRC's runlevels. Meridian's own are left out.
func (sc *scan) enabledServices() map[string]string {
	out := map[string]string{}
	globs := []string{"/etc/systemd/system/*.wants/*", "/etc/systemd/system/*.requires/*", "/etc/runlevels/*/*",
		"/root/.config/systemd/user/*.wants/*"}
	for _, a := range sc.next.Users {
		if a.canLogIn() && a.Home != "" && a.Home != "/" {
			globs = append(globs, filepath.Join(a.Home, ".config/systemd/user/*.wants/*"))
		}
	}
	for _, g := range globs {
		paths, _ := filepath.Glob(sc.m.path(g))
		for _, full := range paths {
			name := cleanText(filepath.Base(full), 200)
			target, _ := os.Readlink(full)
			if ownUnit(name, target) {
				continue
			}
			if _, ok := out[name]; !ok {
				out[name] = cleanText(target, 256)
			}
			if len(out) >= maxWatched {
				return out
			}
		}
	}
	return out
}

// meridianUnitRE are the names of Meridian's own services: the agent, Xray, and one Hysteria2 server
// or realm forward per id (systemd meridian-hy2@22.service, OpenRC meridian-hy2.22).
var meridianUnitRE = regexp.MustCompile(`^meridian-(agent|xray|(hy2|realm)[@.][0-9]{1,12})(\.service)?$`)

// ownUnit says whether an enabled service is one of Meridian's: its name, and a link to Meridian's own
// unit (or OpenRC script) - a look-alike name pointing elsewhere is not.
func ownUnit(name, target string) bool {
	if !meridianUnitRE.MatchString(name) {
		return false
	}
	base := filepath.Base(target)
	template := name
	if at := strings.IndexByte(name, '@'); at > 0 {
		template = name[:at+1] + ".service" // meridian-hy2@22.service runs meridian-hy2@.service
	}
	return target == "" || base == name || base == template
}

func (sc *scan) checkServices() {
	cur := sc.enabledServices()
	names := make([]string, 0, len(cur))
	for n := range cur {
		names = append(names, n)
	}
	sort.Strings(names)
	sc.next.Services = names
	if sc.first {
		return
	}
	for _, n := range names {
		if slices.Contains(sc.prev.Services, n) {
			continue
		}
		detail := "It starts by itself whenever the server boots."
		if t := cur[n]; t != "" {
			detail = "It starts by itself whenever the server boots (" + t + ")."
		}
		sc.event(proto.Finding{Key: "service:" + n, Kind: proto.FindService, Severity: proto.SevWarning,
			Title:  "A service was set to start at boot: " + n,
			Detail: detail + " Malware stays on a server this way - disable it unless you installed it."})
	}
}

// quietModules are kernel modules loaded on demand all the time (firewall, tunnels, file systems,
// crypto): never a finding.
var quietModules = []string{"nf_", "nft_", "xt_", "x_tables", "ip_", "ip6_", "ipt_", "ip6t_", "ip6table", "iptable", "ebt",
	"arp", "br_netfilter", "bridge", "stp", "llc", "overlay", "wireguard", "tun", "tap", "veth", "vxlan", "geneve", "macvlan",
	"ipvlan", "dummy", "xfrm", "esp", "ah4", "ah6", "af_key", "tcp_", "sch_", "cls_", "act_", "udp_tunnel", "libchacha",
	"chacha", "poly1305", "curve25519", "libcurve25519", "aes", "ghash", "gcm", "ccm", "cmac", "sha", "crc", "crypto",
	"cryptd", "authenc", "echainiv", "seqiv", "dm_", "loop", "isofs", "nls_", "fuse", "binfmt_misc", "autofs4", "nfs",
	"lockd", "sunrpc", "grace", "ext4", "xfs", "btrfs", "vfat", "fat", "squashfs", "zstd", "lz4", "xor", "raid", "msdos",
	"cdrom", "sr_mod", "usb", "hid", "input", "evdev", "joydev", "virtio", "kvm", "irqbypass", "rapl", "intel_", "amd",
	"edac", "mei", "snd", "soundcore", "drm", "ttm", "i2c", "video", "button", "pcspkr", "serio", "psmouse", "ata", "ahci",
	"libata", "scsi", "sd_mod", "sg", "nvme", "efi", "ipmi", "tls", "bonding", "8021q", "garp", "mrp", "vhost", "nbd",
	"configfs", "ib_", "rdma", "mlx", "ena", "vmw", "hv_", "xen", "cfg80211", "rfkill", "bluetooth", "ecdh", "ecc"}

func (m *Monitor) readModules() []string {
	b, err := readSmall(m.path("proc/modules"), 1<<20)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) > 0 {
			out = append(out, cleanText(f[0], 64))
		}
		if len(out) == maxWatched {
			break
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}

func (sc *scan) checkModules() {
	mods := sc.m.readModules()
	sc.next.Modules = mods
	if sc.first || len(sc.prev.Modules) == 0 {
		return
	}
	for _, n := range mods {
		if slices.Contains(sc.prev.Modules, n) || nameIs(n, quietModules) {
			continue
		}
		sc.event(proto.Finding{Key: "module:" + n, Kind: proto.FindModule, Severity: proto.SevWarning,
			Title:  "A kernel module was loaded: " + n,
			Detail: "Modules run inside the kernel with full control. Rootkits load one - unless you installed a driver or a program that needs it."})
	}
}
