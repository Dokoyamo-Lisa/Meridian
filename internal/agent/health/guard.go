package health

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"meridian/internal/agent/protect"
	"meridian/internal/agent/sshd"
	"meridian/internal/proto"
)

// checkSSHSettings: what the SSH server lets in - passwords (whoever guesses one gets in, and bots
// try all day) and accounts without a password. Both offer the fix that makes SSH take keys only;
// whether it can be done safely is checked again when the supervisor asks for it.
func (sc *scan) checkSSHSettings(ctx context.Context) {
	read := sc.m.SSHD
	if read == nil {
		if sc.m.Root != "/" || sshd.Binary("/") == "" {
			return // a test without it, or no SSH server here
		}
		read = sshd.Read
	}
	s, err := read(ctx)
	if err != nil || len(s) == 0 {
		return
	}
	fix := &proto.Fix{Kind: proto.FixKeysOnly}
	first := protect.KeysOnlyReady(sc.m.Root, s)
	if s.Passwords() {
		who := "Anyone"
		if s.Get("permitrootlogin") == "yes" {
			who = "Anyone - as root too -"
		}
		detail := who + " who guesses a password gets in, and bots try all day long. Sign in with an SSH key, then let SSH take keys only (sessions already open stay)."
		if first != "" {
			detail += " First: " + first + "."
		}
		sc.last(proto.Finding{Key: "ssh-passwords", Kind: proto.FindSSH, Severity: proto.SevWarning,
			Title: "SSH lets passwords sign in", Detail: detail, Fix: fix})
	}
	if s.Get("permitemptypasswords") == "yes" {
		sc.last(proto.Finding{Key: "ssh-empty-passwords", Kind: proto.FindSSH, Severity: proto.SevCritical,
			Title:  "SSH lets accounts without a password sign in",
			Detail: "PermitEmptyPasswords yes: an account that has no password needs nothing at all to get in. Let SSH take keys only.",
			Fix:    fix})
	}
}

// tmpFolders are where a program with administrator rights has no business.
var tmpFolders = []string{"/tmp", "/var/tmp", "/dev/shm"}

const maxWalk = 20000 // entries of the temporary folders one scan looks at

// checkSUID: a program with administrator rights (setuid or setgid) in a temporary folder - how
// someone who got in once gets root again. It offers to move it into quarantine.
func (sc *scan) checkSUID() {
	seen := 0
	root := filepath.Clean(sc.m.Root)
	for _, dir := range tmpFolders {
		_ = filepath.WalkDir(sc.m.path(dir), func(full string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if seen++; seen > maxWalk {
				return filepath.SkipAll
			}
			p := "/" + strings.TrimPrefix(filepath.ToSlash(strings.TrimPrefix(full, root)), "/")
			if d.IsDir() {
				if strings.Count(p, "/") > 8 {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			fi, err := d.Info()
			if err != nil || fi.Mode()&(fs.ModeSetuid|fs.ModeSetgid) == 0 {
				return nil
			}
			shown := cleanText(p, 300)
			owner := "its owner"
			if st, ok := fi.Sys().(*syscall.Stat_t); ok {
				owner = nz(sc.userName(strconv.Itoa(int(st.Uid))), "uid "+strconv.Itoa(int(st.Uid)))
			}
			f := proto.Finding{Key: "suid:" + shown, Kind: proto.FindFile, Severity: proto.SevHigh,
				Title: "A program with administrator rights is in a temporary folder: " + shown,
				Detail: "Whoever starts it gets the rights of " + owner + " - how someone who got in once becomes root again. " +
					"Move it into quarantine unless you put it there."}
			if shown == p && fi.Size() <= 256<<20 {
				if sum, err := hashFile(full, true); err == nil {
					f.Fix = &proto.Fix{Kind: proto.FixQuarantine, Path: p, SHA256: sum}
				}
			}
			sc.last(f)
			return nil
		})
	}
}

// checkReboot: updates installed that take effect only once the server restarts (Debian and Ubuntu
// say so in /var/run/reboot-required).
func (sc *scan) checkReboot() {
	if _, err := os.Stat(sc.m.path("/var/run/reboot-required")); err != nil {
		return
	}
	what := "Updates are"
	if b, err := readSmall(sc.m.path("/var/run/reboot-required.pkgs"), 64<<10); err == nil {
		var pkgs []string
		for _, f := range strings.Fields(string(b)) {
			if f = cleanText(f, 60); f != "" && !slices.Contains(pkgs, f) && len(pkgs) < 8 {
				pkgs = append(pkgs, f)
			}
		}
		if len(pkgs) > 0 {
			what = "Updates of " + strings.Join(pkgs, ", ") + " are"
		}
	}
	sc.last(proto.Finding{Key: "reboot-required", Kind: proto.FindUpdate, Severity: proto.SevInfo,
		Title: "Updates wait for a restart of the server",
		Detail: what + " installed but take effect only after the server restarts. Restart it when a short break suits " +
			"your users: every protocol comes back by itself."})
}

// quarantineFix offers to move a file the health check found into quarantine, where that may be done.
func quarantineFix(p, shown, sha string) *proto.Fix {
	if p != shown || sha == "" || !protect.QuarantineOK(p, false) {
		return nil
	}
	return &proto.Fix{Kind: proto.FixQuarantine, Path: p, SHA256: sha}
}

// stopFix offers to stop a process (and quarantine its program where it does not belong).
func stopFix(p *proc) *proto.Fix {
	if p == nil || p.exe == "" || p.pid <= 1 || p.start == 0 {
		return nil
	}
	return &proto.Fix{Kind: proto.FixStopProcess, PID: p.pid, Start: p.start, Path: p.exe}
}

// lockFix offers to lock an account - never root, never the agent's own.
func lockFix(name string) *proto.Fix {
	if name == "root" || strings.HasPrefix(name, "meridian") || !userRE.MatchString(name) {
		return nil
	}
	return &proto.Fix{Kind: proto.FixLockAccount, User: name}
}
