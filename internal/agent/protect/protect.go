// Package protect does what the supervisor confirmed about a health finding (proto.Fix): stop a
// process and put its program in quarantine, take an SSH key out, lock an account, turn a service
// off, move a file into quarantine, make SSH take keys only, refuse addresses on SSH - and undoes
// each of them on request, but for a stopped process (its program comes back from quarantine; the
// process stays stopped).
//
// Nothing here runs by itself: only ActionProtect does it, which the panel sends after a person
// confirmed it in the browser. Every target is checked again here - the fix as the health check saw
// it, against the process or file as it is now - and whatever is the system's own or Rosélune's is
// refused. What undoing needs stays on the server (the journal and the quarantine, root's alone):
// the lines of a key taken out never travel to the panel.
package protect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"meridian/internal/agent/sshd"
	"meridian/internal/proto"
)

// Engine does and undoes fixes. Set the fields before use; nil functions get the real thing.
type Engine struct {
	Root string // "/" on a server; tests use a folder
	Dir  string // the journal and the quarantine, e.g. /var/lib/meridian-agent/protect
	// Own says whether a path is Rosélune's own (the agent, the cores, their folders): never touched
	Own func(path string) bool
	// Trusted are addresses that signed in over SSH lately: never blocked
	Trusted func() []string
	// Run runs a program with fixed arguments and says what it printed
	Run func(ctx context.Context, name string, args ...string) (string, error)
	// SSHD reads the SSH server's effective settings, Test checks its files, Reload makes it read them
	SSHD   func(ctx context.Context) (sshd.Settings, error)
	Test   func(ctx context.Context) (string, error)
	Reload func(ctx context.Context) error
	// Init is the host's init system: "systemd" or "openrc"
	Init func() string
	// Kill sends a process a signal (tests never signal a real process)
	Kill func(pid int, sig syscall.Signal) error

	mu sync.Mutex
	j  *journal
}

// entry is one fix done: what it was, and what undoing it needs.
type entry struct {
	ID     int64     `json:"id"`
	Fix    proto.Fix `json:"fix"`
	At     int64     `json:"at"`
	Undone int64     `json:"undone,omitempty"`
	// a file moved into quarantine (quarantine, and the program of a stopped process)
	Quarantine string `json:"quarantine,omitempty"` // its folder in Dir/quarantine
	Path       string `json:"path,omitempty"`       // where it was
	Mode       uint32 `json:"mode,omitempty"`
	UID        int    `json:"uid,omitempty"`
	GID        int    `json:"gid,omitempty"`
	// remove_key: the lines taken out
	Lines []string `json:"lines,omitempty"`
	// lock_account: how the account was before
	WasLocked bool   `json:"was_locked,omitempty"`
	Expire    string `json:"expire,omitempty"`
	// disable_service: it was running
	WasActive bool `json:"was_active,omitempty"`
	// ssh_keys_only: the settings file written
	DropIn string `json:"drop_in,omitempty"`
}

type journal struct {
	Entries map[int64]*entry `json:"entries"`
}

const (
	maxEntries    = 500
	maxBlocked    = 500       // addresses refused on SSH at most
	maxQuarantine = 256 << 20 // the largest file moved across file systems
)

func (e *Engine) path(p string) string { return filepath.Join(e.Root, p) }

func (e *Engine) run(ctx context.Context, name string, args ...string) (string, error) {
	if e.Run != nil {
		return e.Run(ctx, name, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		if s != "" {
			return s, fmt.Errorf("%s: %s", name, s)
		}
		return s, fmt.Errorf("%s: %v", name, err)
	}
	return s, nil
}

func (e *Engine) kill(pid int) error {
	if e.Kill != nil {
		return e.Kill(pid, syscall.SIGKILL)
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}

func (e *Engine) init() string {
	if e.Init != nil {
		return e.Init()
	}
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		return "systemd"
	}
	return "openrc"
}

func (e *Engine) sshdSettings(ctx context.Context) (sshd.Settings, error) {
	if e.SSHD != nil {
		return e.SSHD(ctx)
	}
	return sshd.Read(ctx)
}

func (e *Engine) sshdTest(ctx context.Context) (string, error) {
	if e.Test != nil {
		return e.Test(ctx)
	}
	return sshd.Test(ctx)
}

// reloadSSH makes the SSH server read its settings again: sessions already open stay.
func (e *Engine) reloadSSH(ctx context.Context) error {
	if e.Reload != nil {
		return e.Reload(ctx)
	}
	if e.init() == "systemd" {
		// reloaded where it runs; a server started per connection (ssh.socket) reads them at its next
		// start anyway
		var errs []error
		for _, u := range []string{"ssh.service", "sshd.service"} {
			if _, err := e.run(ctx, "systemctl", "try-reload-or-restart", u); err == nil {
				return nil
			} else {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
	_, err := e.run(ctx, "rc-service", "sshd", "reload")
	return err
}

func (e *Engine) load() {
	if e.j != nil {
		return
	}
	e.j = &journal{Entries: map[int64]*entry{}}
	if b, err := os.ReadFile(filepath.Join(e.Dir, "journal.json")); err == nil {
		var j journal
		if json.Unmarshal(b, &j) == nil && j.Entries != nil {
			e.j = &j
		}
	}
}

func (e *Engine) save() error {
	// the oldest undone ones go first when there are too many
	if len(e.j.Entries) > maxEntries {
		var ids []int64
		for id, en := range e.j.Entries {
			if en.Undone > 0 {
				ids = append(ids, id)
			}
		}
		slices.Sort(ids)
		for _, id := range ids[:max(0, min(len(ids), len(e.j.Entries)-maxEntries))] {
			delete(e.j.Entries, id)
		}
	}
	if err := os.MkdirAll(e.Dir, 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(e.j)
	tmp := filepath.Join(e.Dir, "journal.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(e.Dir, "journal.json"))
}

// Do does a fix the supervisor confirmed, or undoes one (p.Undo). It says what happened in plain
// words. A request repeated (its result was lost on the way) is not done twice.
func (e *Engine) Do(ctx context.Context, p proto.Protect) (string, error) {
	if e == nil {
		return "", errors.New("this agent cannot do that")
	}
	if p.ID <= 0 {
		return "", errors.New("no id")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.load()
	if p.Undo {
		return e.undo(ctx, p.ID)
	}
	if p.Fix == nil {
		return "", errors.New("nothing to do")
	}
	if old := e.j.Entries[p.ID]; old != nil && old.Undone == 0 {
		return "done already", nil
	}
	f := *p.Fix
	var en *entry
	var msg string
	var err error
	switch f.Kind {
	case proto.FixStopProcess:
		en, msg, err = e.stopProcess(p.ID, f)
	case proto.FixQuarantine:
		en, msg, err = e.quarantineFile(ctx, p.ID, f)
	case proto.FixRemoveKey:
		en, msg, err = e.removeKey(ctx, f)
	case proto.FixLockAccount:
		en, msg, err = e.lockAccount(ctx, f)
	case proto.FixDisableService:
		en, msg, err = e.disableService(ctx, f)
	case proto.FixKeysOnly:
		en, msg, err = e.keysOnly(ctx)
	case proto.FixBlockSSH:
		en, msg, err = e.blockSSH(f)
	default:
		return "", fmt.Errorf("unknown fix %q", f.Kind)
	}
	if err != nil {
		return "", err
	}
	if en.Fix.Kind == "" { // a fix narrowed while it was done (block_ssh) keeps what was really done
		en.Fix = f
	}
	en.ID, en.At = p.ID, time.Now().Unix()
	e.j.Entries[p.ID] = en
	if err := e.save(); err != nil {
		return msg, fmt.Errorf("%s - but what undoing it needs could not be kept: %v", msg, err)
	}
	return msg, nil
}

func (e *Engine) undo(ctx context.Context, id int64) (string, error) {
	en := e.j.Entries[id]
	if en == nil {
		return "", errors.New("this server has no record of that step (the agent's data was replaced?) - there is nothing to undo here")
	}
	if en.Undone > 0 {
		return "undone already", nil
	}
	var msg string
	var err error
	switch en.Fix.Kind {
	case proto.FixStopProcess:
		if en.Quarantine == "" {
			return "", errors.New("a stopped process cannot be started again from here, and its program was not moved")
		}
		msg, err = e.restore(ctx, en)
		if err == nil {
			msg += " (the process itself stays stopped)"
		}
	case proto.FixQuarantine:
		msg, err = e.restore(ctx, en)
	case proto.FixRemoveKey:
		msg, err = e.restoreKey(en)
	case proto.FixLockAccount:
		msg, err = e.unlockAccount(ctx, en)
	case proto.FixDisableService:
		msg, err = e.enableService(ctx, en)
	case proto.FixKeysOnly:
		msg, err = e.passwordsBack(ctx, en)
	case proto.FixBlockSSH:
		msg = addresses(len(en.Fix.Addrs)) + " may reach SSH again"
	default:
		return "", fmt.Errorf("unknown fix %q", en.Fix.Kind)
	}
	if err != nil {
		return "", err
	}
	en.Undone = time.Now().Unix()
	if err := e.save(); err != nil {
		return msg, err
	}
	return msg, nil
}

// Blocked are the addresses refused on the SSH server's port now (block_ssh steps not undone).
func (e *Engine) Blocked() []string {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.load()
	var out []string
	for _, en := range e.j.Entries {
		if en.Fix.Kind == proto.FixBlockSSH && en.Undone == 0 {
			for _, a := range en.Fix.Addrs {
				if !slices.Contains(out, a) {
					out = append(out, a)
				}
			}
		}
	}
	sort.Strings(out)
	if len(out) > maxBlocked {
		out = out[:maxBlocked]
	}
	return out
}

// ---------------------------------------------------------------- processes

// stopProcess stops the process the health check saw - the same one: its number, when it started
// and its program must all match - and, where its program file does not belong (a temporary folder,
// a home, /opt ...), every other process running that file too, then moves the file into quarantine.
// A program in a system folder stays where it is.
func (e *Engine) stopProcess(id int64, f proto.Fix) (*entry, string, error) {
	if f.PID <= 1 || f.Start == 0 || !cleanAbs(f.Path) {
		return nil, "", errors.New("not a process this can stop")
	}
	if e.Own != nil && e.Own(f.Path) {
		return nil, "", errors.New("that is one of Rosélune's own programs")
	}
	movable := quarantinable(f.Path)
	stopped := 0
	if start, exe, ok := e.procInfo(f.PID); ok && start == f.Start && exe == f.Path && f.PID != os.Getpid() {
		if err := e.kill(f.PID); err != nil && !errors.Is(err, syscall.ESRCH) {
			return nil, "", fmt.Errorf("stopping process %d: %v", f.PID, err)
		}
		stopped++
	}
	if movable { // its other processes: the same program file
		for _, pid := range e.runningProgram(f.Path) {
			if pid != f.PID && pid != os.Getpid() && e.kill(pid) == nil {
				stopped++
			}
		}
	}
	if stopped == 0 && !movable {
		return nil, "", errors.New("the process is not running any more - nothing was done")
	}
	e.waitGone(f.PID, f.Start)
	what := fmt.Sprintf("the process running %s is stopped", f.Path)
	switch {
	case stopped == 0:
		what = "the process had stopped already"
	case stopped > 1:
		what = fmt.Sprintf("%d processes running %s are stopped", stopped, f.Path)
	}
	en := &entry{}
	switch fi, err := os.Lstat(e.path(f.Path)); {
	case !movable:
		what += "; its program stays where it is (a system folder) - look at it"
	case err != nil:
		what += "; its program file was gone already"
	case !fi.Mode().IsRegular():
		what += "; its program is not a plain file and stays where it is"
	default:
		if err := e.moveToQuarantine(id, f.Path, en); err != nil {
			return en, what + "; its program could not be moved: " + err.Error(), nil
		}
		what += "; its program is in quarantine now"
	}
	return en, what, nil
}

// procInfo is a process's start time (clock ticks since boot) and program file.
func (e *Engine) procInfo(pid int) (start uint64, exe string, ok bool) {
	b, err := os.ReadFile(e.path(fmt.Sprintf("/proc/%d/stat", pid)))
	if err != nil {
		return 0, "", false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, "", false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return 0, "", false
	}
	start, err = strconv.ParseUint(f[19], 10, 64)
	if err != nil {
		return 0, "", false
	}
	link, err := os.Readlink(e.path(fmt.Sprintf("/proc/%d/exe", pid)))
	if err != nil {
		return 0, "", false
	}
	return start, strings.TrimSuffix(link, " (deleted)"), true
}

// runningProgram are the processes whose program is path.
func (e *Engine) runningProgram(path string) []int {
	ents, _ := os.ReadDir(e.path("/proc"))
	var out []int
	for _, d := range ents {
		pid, err := strconv.Atoi(d.Name())
		if err != nil || pid <= 1 {
			continue
		}
		if link, err := os.Readlink(e.path(fmt.Sprintf("/proc/%d/exe", pid))); err == nil && strings.TrimSuffix(link, " (deleted)") == path {
			out = append(out, pid)
		}
		if len(out) >= 1000 {
			break
		}
	}
	return out
}

func (e *Engine) waitGone(pid int, start uint64) {
	if e.Kill != nil {
		return // a test: nothing was signalled
	}
	for range 30 {
		if s, _, ok := e.procInfo(pid); !ok || s != start {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ---------------------------------------------------------------- quarantine

var (
	// where a program file may be moved away from: places programs are dropped, not installed
	movableDirs = []string{"/tmp/", "/var/tmp/", "/dev/shm/", "/run/user/", "/root/", "/home/", "/opt/", "/srv/", "/usr/local/",
		"/var/lib/", "/var/spool/", "/var/cache/", "/var/www/", "/mnt/", "/media/"}
	// files a quarantine fix may move: what malware plants to stay (a new one: a changed system file is
	// never moved - the finding says to look at it)
	quarantineGlobs = []string{"/etc/ld.so.preload", "/etc/cron.d/*", "/etc/cron.hourly/*", "/etc/cron.daily/*",
		"/etc/cron.weekly/*", "/etc/cron.monthly/*", "/var/spool/cron/*", "/var/spool/cron/crontabs/*", "/etc/crontabs/*",
		"/etc/sudoers.d/*", "/etc/profile.d/*", "/etc/rc.local", "/etc/systemd/system/*.d/*.conf"}
)

func cleanAbs(p string) bool {
	return p != "" && len(p) < 4096 && filepath.IsAbs(p) && filepath.Clean(p) == p && !strings.ContainsAny(p, "\x00\r\n")
}

// quarantinable says whether a program file may be moved into quarantine.
func quarantinable(p string) bool {
	if !cleanAbs(p) || strings.HasPrefix(p, "/var/lib/meridian") || strings.HasPrefix(p, "/var/lib/dpkg/") ||
		strings.HasPrefix(p, "/var/lib/apt/") || strings.HasPrefix(p, "/var/lib/rpm/") {
		return false
	}
	for _, d := range movableDirs {
		if strings.HasPrefix(p, d) {
			return true
		}
	}
	return false
}

// QuarantineOK says whether a file found by the health check may be offered for quarantine: one of
// the places malware plants itself, or a program with administrator rights in a temporary folder.
func QuarantineOK(p string, suid bool) bool {
	if !cleanAbs(p) {
		return false
	}
	if suid {
		return strings.HasPrefix(p, "/tmp/") || strings.HasPrefix(p, "/var/tmp/") || strings.HasPrefix(p, "/dev/shm/")
	}
	for _, g := range quarantineGlobs {
		if ok, _ := filepath.Match(g, p); ok && !strings.HasPrefix(filepath.Base(p), ".placeholder") {
			return true
		}
	}
	return false
}

// quarantineFile moves a planted file into quarantine - the file as the health check saw it.
func (e *Engine) quarantineFile(ctx context.Context, id int64, f proto.Fix) (*entry, string, error) {
	fi, err := os.Lstat(e.path(f.Path))
	suid := err == nil && fi.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0
	switch {
	case !QuarantineOK(f.Path, suid):
		return nil, "", errors.New("that file is not one this can move")
	case err != nil:
		return nil, "", errors.New("the file is gone already - nothing was done")
	case !fi.Mode().IsRegular():
		return nil, "", errors.New("it is not a plain file (a link or a folder) - look at it by hand")
	case e.Own != nil && e.Own(f.Path):
		return nil, "", errors.New("that is one of Rosélune's own files")
	}
	if f.SHA256 != "" {
		sum, err := fileSum(e.path(f.Path))
		if err != nil {
			return nil, "", err
		}
		if sum != f.SHA256 {
			return nil, "", errors.New("the file changed since it was found - look at it again; nothing was moved")
		}
	}
	en := &entry{}
	if err := e.moveToQuarantine(id, f.Path, en); err != nil {
		return nil, "", err
	}
	if strings.HasPrefix(f.Path, "/etc/systemd/system/") && e.init() == "systemd" {
		_, _ = e.run(ctx, "systemctl", "daemon-reload") // the service's own settings are back
	}
	return en, f.Path + " is in quarantine now", nil
}

func fileSum(p string) (string, error) {
	fh, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer fh.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(fh, maxQuarantine+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// moveToQuarantine moves a file into Dir/quarantine/<id>, readable by nobody, and records where it was.
func (e *Engine) moveToQuarantine(id int64, p string, en *entry) error {
	fi, err := os.Lstat(e.path(p))
	if err != nil {
		return err
	}
	dir := filepath.Join(e.Dir, "quarantine", strconv.FormatInt(id, 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	dst := filepath.Join(dir, "file")
	if err := moveFile(e.path(p), dst); err != nil {
		os.Remove(dir)
		return err
	}
	_ = os.Chmod(dst, 0)
	en.Quarantine, en.Path, en.Mode = dir, p, uint32(fi.Mode().Perm()|fi.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky))
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		en.UID, en.GID = int(st.Uid), int(st.Gid)
	}
	return nil
}

// moveFile renames a file, or copies it across file systems (/tmp is often its own) and removes it.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if fi, err := in.Stat(); err != nil || fi.Size() > maxQuarantine {
		return errors.New("the file is too large to move into quarantine")
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err2 := out.Sync(); err == nil {
		err = err2
	}
	if err2 := out.Close(); err == nil {
		err = err2
	}
	if err != nil {
		os.Remove(dst)
		return err
	}
	return os.Remove(src)
}

// restore puts a file back from quarantine - only where nothing else is now.
func (e *Engine) restore(ctx context.Context, en *entry) (string, error) {
	src := filepath.Join(en.Quarantine, "file")
	if _, err := os.Lstat(src); err != nil {
		return "", errors.New("the file is not in quarantine any more")
	}
	if _, err := os.Lstat(e.path(en.Path)); err == nil {
		return "", fmt.Errorf("something else is at %s now - it was not overwritten", en.Path)
	}
	if err := os.MkdirAll(filepath.Dir(e.path(en.Path)), 0o755); err != nil {
		return "", err
	}
	if err := moveFile(src, e.path(en.Path)); err != nil {
		return "", err
	}
	mode := os.FileMode(en.Mode).Perm()
	if en.Mode&uint32(os.ModeSetuid) != 0 {
		mode |= os.ModeSetuid
	}
	if en.Mode&uint32(os.ModeSetgid) != 0 {
		mode |= os.ModeSetgid
	}
	_ = os.Lchown(e.path(en.Path), en.UID, en.GID)
	_ = os.Chmod(e.path(en.Path), mode)
	os.Remove(en.Quarantine)
	if strings.HasPrefix(en.Path, "/etc/systemd/system/") && e.init() == "systemd" {
		_, _ = e.run(ctx, "systemctl", "daemon-reload")
	}
	return en.Path + " is back", nil
}

// ---------------------------------------------------------------- SSH keys

var fingerprintRE = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)

// keyFileOK says whether a path is an authorized_keys file of an account in /etc/passwd, which one
// (root before any other sharing its home), and whether it lets in with root's rights - any account
// with uid 0 whose home it is.
func (e *Engine) keyFileOK(p string) (user string, admin, ok bool) {
	if !cleanAbs(p) {
		return "", false, false
	}
	base := filepath.Base(p)
	if (base != "authorized_keys" && base != "authorized_keys2") || filepath.Base(filepath.Dir(p)) != ".ssh" {
		return "", false, false
	}
	home := filepath.Dir(filepath.Dir(p))
	if home == "/" || home == "" {
		return "", false, false
	}
	var names []string
	for name, a := range e.accounts() {
		if a.home == home {
			names = append(names, name)
			admin = admin || a.uid == 0
		}
	}
	if len(names) == 0 {
		return "", false, false
	}
	sort.Strings(names)
	user = names[0]
	if slices.Contains(names, "root") {
		user = "root"
	}
	return user, admin, true
}

type account struct {
	uid   int
	home  string
	shell string
}

func (e *Engine) accounts() map[string]account {
	out := map[string]account{}
	b, err := os.ReadFile(e.path("/etc/passwd"))
	if err != nil {
		return out
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Split(l, ":")
		if len(f) < 7 || f[0] == "" || strings.HasPrefix(f[0], "#") {
			continue
		}
		uid, err := strconv.Atoi(f[2])
		if err != nil {
			continue
		}
		out[f[0]] = account{uid: uid, home: f[5], shell: f[6]}
	}
	return out
}

func canLogIn(shell string) bool {
	s := filepath.Base(shell)
	return shell != "" && s != "nologin" && s != "false" && s != "sync" && s != "halt" && s != "shutdown"
}

// removeKey takes one key (by its fingerprint) out of an authorized_keys file. The last key that
// can sign in as root, where SSH takes no passwords, is refused: that would lock everyone out.
func (e *Engine) removeKey(ctx context.Context, f proto.Fix) (*entry, string, error) {
	user, admin, ok := e.keyFileOK(f.Path)
	if !ok || !fingerprintRE.MatchString(f.Key) {
		return nil, "", errors.New("not a key this can remove")
	}
	full := e.path(f.Path)
	fi, err := os.Lstat(full)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 1<<20 {
		return nil, "", errors.New("the file is not there, or not a plain file - nothing was done")
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return nil, "", err
	}
	var keep, gone []string
	left := 0
	for _, l := range strings.SplitAfter(string(b), "\n") {
		if fp, _, _, ok := sshd.KeyFingerprint(strings.TrimSpace(l)); ok && fp == f.Key {
			gone = append(gone, strings.TrimRight(l, "\n"))
			continue
		}
		if _, _, _, ok := sshd.KeyFingerprint(strings.TrimSpace(l)); ok {
			left++
		}
		keep = append(keep, l)
	}
	if len(gone) == 0 {
		return nil, "", errors.New("that key is not in the file any more - nothing was done")
	}
	if left == 0 && admin { // the last key with root's rights, where no password gets in: refused
		if s, err := e.sshdSettings(ctx); err != nil || !s.Passwords() {
			return nil, "", errors.New("it is the last key that can sign in as root, and SSH takes no passwords: add another key first, or you would be locked out")
		}
	}
	if err := writeLike(full, fi, []byte(strings.Join(keep, ""))); err != nil {
		return nil, "", err
	}
	return &entry{Lines: gone}, fmt.Sprintf("the key %s can no longer sign in as %s", f.Key, user), nil
}

// restoreKey puts the lines taken out back at the end of the file (unless they are there again).
func (e *Engine) restoreKey(en *entry) (string, error) {
	full := e.path(en.Fix.Path)
	fi, err := os.Lstat(full)
	var cur []byte
	switch {
	case err == nil && fi.Mode().IsRegular():
		cur, _ = os.ReadFile(full)
	case err == nil:
		return "", errors.New("the file is not a plain file now - put the key back by hand")
	default:
		return "", errors.New("the file is gone - put the key back by hand")
	}
	text := string(cur)
	for _, l := range en.Lines {
		if !strings.Contains(text, l) {
			if text != "" && !strings.HasSuffix(text, "\n") {
				text += "\n"
			}
			text += l + "\n"
		}
	}
	if err := writeLike(full, fi, []byte(text)); err != nil {
		return "", err
	}
	return "the key " + en.Fix.Key + " can sign in again", nil
}

// writeLike replaces a file in one step, with the old one's owner and mode.
func writeLike(path string, fi os.FileInfo, body []byte) error {
	tmp := path + ".meridian.tmp"
	if err := os.WriteFile(tmp, body, fi.Mode().Perm()); err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		_ = os.Lchown(tmp, int(st.Uid), int(st.Gid))
	}
	_ = os.Chmod(tmp, fi.Mode().Perm())
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// ---------------------------------------------------------------- accounts

var userRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,31}$`)

// lockAccount locks an account - its password, and the account itself (expired), which SSH keys
// do not get past either - and ends what it runs. root, and the agent's own account, are refused.
func (e *Engine) lockAccount(ctx context.Context, f proto.Fix) (*entry, string, error) {
	if !userRE.MatchString(f.User) || f.User == "root" || strings.HasPrefix(f.User, "meridian") {
		return nil, "", errors.New("not an account this can lock")
	}
	a, ok := e.accounts()[f.User]
	if !ok {
		return nil, "", errors.New("the account is gone already - nothing was done")
	}
	pw, expire := e.shadow(f.User)
	if _, err := e.run(ctx, "usermod", "-L", "-e", "1", f.User); err != nil {
		return nil, "", fmt.Errorf("locking %s: %v (usermod from the shadow tools is needed)", f.User, err)
	}
	what := f.User + " is locked: no password and no SSH key gets in"
	if a.uid != 0 { // what it runs ends with it (a uid 0 account's processes cannot be told from root's)
		n := 0
		ents, _ := os.ReadDir(e.path("/proc"))
		for _, d := range ents {
			pid, err := strconv.Atoi(d.Name())
			if err != nil || pid <= 1 || pid == os.Getpid() {
				continue
			}
			if uid, ok := e.procUID(pid); ok && uid == a.uid && e.kill(pid) == nil {
				n++
			}
		}
		if n > 0 {
			what += fmt.Sprintf("; %d of its processes stopped", n)
		}
	}
	return &entry{WasLocked: strings.HasPrefix(pw, "!"), Expire: expire}, what, nil
}

// shadow is an account's password field and expiry day in /etc/shadow.
func (e *Engine) shadow(user string) (pw, expire string) {
	b, err := os.ReadFile(e.path("/etc/shadow"))
	if err != nil {
		return "", ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Split(l, ":")
		if len(f) >= 8 && f[0] == user {
			return f[1], f[7]
		}
	}
	return "", ""
}

func (e *Engine) procUID(pid int) (int, bool) {
	b, err := os.ReadFile(e.path(fmt.Sprintf("/proc/%d/status", pid)))
	if err != nil {
		return 0, false
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "Uid:"); ok {
			if f := strings.Fields(v); len(f) > 0 {
				uid, err := strconv.Atoi(f[0])
				return uid, err == nil
			}
		}
	}
	return 0, false
}

func (e *Engine) unlockAccount(ctx context.Context, en *entry) (string, error) {
	user := en.Fix.User
	if _, ok := e.accounts()[user]; !ok {
		return "", errors.New("the account is gone")
	}
	expire := ""
	if d, err := strconv.Atoi(en.Expire); err == nil && d > 0 {
		expire = time.Unix(int64(d)*86400, 0).UTC().Format("2006-01-02")
	}
	if _, err := e.run(ctx, "usermod", "-e", expire, user); err != nil {
		return "", err
	}
	if !en.WasLocked {
		if _, err := e.run(ctx, "usermod", "-U", user); err != nil {
			return "", fmt.Errorf("%s can sign in with an SSH key again, but its password stays locked: %v", user, err)
		}
	}
	return user + " is unlocked", nil
}

// ---------------------------------------------------------------- services

var (
	systemdUnitRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.@-]{0,190}\.(service|timer|socket|path)$`)
	openrcNameRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,64}$`)
	// services the system needs: never turned off from here
	keepServices = []string{"ssh", "sshd", "systemd-", "dbus", "network", "NetworkManager", "networking", "cron", "crond",
		"rsyslog", "syslog", "getty", "serial-getty", "cloud-", "qemu-guest-agent", "chrony", "ntp", "nftables", "iptables",
		"netfilter-persistent", "ufw", "firewalld", "apparmor", "auditd", "polkit", "udev", "unattended-upgrades",
		"multipathd", "lvm2", "mdmonitor", "local", "sysfs", "devfs", "hostname", "bootmisc", "modules", "mount", "swap"}
	// Rosélune's own services (a look-alike name is not one of them)
	ownUnitRE = regexp.MustCompile(`^meridian-(agent|xray|demo-panel|demo-agents|(hy2|realm)[@.][0-9]{1,12}|(mita|snell|anytls)[@.][0-9]{1,15}-[0-9]{1,15})(\.service)?$|^meridian(\.service)?$`)
)

func keepService(name string) bool {
	if ownUnitRE.MatchString(name) {
		return true
	}
	for _, k := range keepServices {
		if strings.HasPrefix(name, k) {
			return true
		}
	}
	return false
}

// disableService stops a service and keeps it from starting at boot.
func (e *Engine) disableService(ctx context.Context, f proto.Fix) (*entry, string, error) {
	sd := e.init() == "systemd"
	if (sd && !systemdUnitRE.MatchString(f.Unit)) || (!sd && !openrcNameRE.MatchString(f.Unit)) || keepService(f.Unit) {
		return nil, "", errors.New("not a service this can turn off")
	}
	en := &entry{}
	if sd {
		out, _ := e.run(ctx, "systemctl", "is-active", f.Unit)
		en.WasActive = strings.TrimSpace(out) == "active"
		if _, err := e.run(ctx, "systemctl", "disable", "--now", f.Unit); err != nil {
			return nil, "", err
		}
	} else {
		if _, err := e.run(ctx, "rc-update", "del", f.Unit, "default"); err != nil {
			return nil, "", err
		}
		_, err := e.run(ctx, "rc-service", f.Unit, "stop")
		en.WasActive = err == nil
	}
	return en, f.Unit + " is stopped and no longer starts at boot", nil
}

func (e *Engine) enableService(ctx context.Context, en *entry) (string, error) {
	u := en.Fix.Unit
	if e.init() == "systemd" {
		if _, err := e.run(ctx, "systemctl", "enable", u); err != nil {
			return "", err
		}
		if en.WasActive {
			if _, err := e.run(ctx, "systemctl", "start", u); err != nil {
				return "", fmt.Errorf("%s starts at boot again but did not start now: %v", u, err)
			}
		}
	} else {
		if _, err := e.run(ctx, "rc-update", "add", u, "default"); err != nil {
			return "", err
		}
		if en.WasActive {
			_, _ = e.run(ctx, "rc-service", u, "start")
		}
	}
	return u + " starts at boot again", nil
}

// ---------------------------------------------------------------- SSH settings

// KeysOnlyReady says whether SSH can be made to take keys only without locking anyone out: its
// settings read DropInDir, and an account that can sign in has a key - "" when it can, else what to
// do first.
func KeysOnlyReady(root string, s sshd.Settings) string {
	if !sshd.ReadsDropIns(root) {
		return "this server's SSH settings do not read " + sshd.DropInDir + " - set PasswordAuthentication no in /etc/ssh/sshd_config by hand"
	}
	if s.Get("pubkeyauthentication") == "no" {
		return "SSH takes no keys either (PubkeyAuthentication no) - turn keys on first"
	}
	e := &Engine{Root: root}
	for name, a := range e.accounts() {
		if !canLogIn(a.shell) && a.uid != 0 {
			continue
		}
		if name == "root" && !s.RootKeys() {
			continue
		}
		for _, f := range []string{"authorized_keys", "authorized_keys2"} {
			p := filepath.Join(a.home, ".ssh", f)
			if a.home == "" || a.home == "/" {
				continue
			}
			if b, err := os.ReadFile(e.path(p)); err == nil && len(b) < 1<<20 && sshd.Keys(string(b)) > 0 {
				return ""
			}
		}
	}
	return "no account that may sign in over SSH has a key yet - add your key first, or you would be locked out"
}

// keysOnly makes SSH take keys only: no passwords, no keyboard-interactive, no empty passwords -
// written to a settings file of its own, checked with sshd -t and sshd -T, then read by the SSH
// server without dropping a session.
func (e *Engine) keysOnly(ctx context.Context) (*entry, string, error) {
	s, err := e.sshdSettings(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("reading the SSH server's settings: %v", err)
	}
	if !s.Passwords() && s.Get("permitemptypasswords") != "yes" {
		return nil, "", errors.New("SSH takes keys only already - nothing was done")
	}
	if why := KeysOnlyReady(e.Root, s); why != "" {
		return nil, "", errors.New(why)
	}
	file := e.path(sshd.KeysOnlyFile)
	write := func(kbd string) error {
		body := "# Rosélune: SSH takes keys only - turned on in the panel by the supervisor; undo it there (server › Security)\n" +
			"PasswordAuthentication no\n" + kbd + " no\nPermitEmptyPasswords no\n"
		return os.WriteFile(file, []byte(body), 0o644)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return nil, "", err
	}
	if err := write("KbdInteractiveAuthentication"); err != nil {
		return nil, "", err
	}
	if msg, err := e.sshdTest(ctx); err != nil {
		// before OpenSSH 8.7 the same setting was called ChallengeResponseAuthentication
		if err := write("ChallengeResponseAuthentication"); err != nil {
			os.Remove(file)
			return nil, "", err
		}
		if msg2, err := e.sshdTest(ctx); err != nil {
			os.Remove(file)
			return nil, "", fmt.Errorf("the SSH server refuses the settings (%s) - nothing was changed", nz(msg2, msg))
		}
	}
	after, err := e.sshdSettings(ctx)
	if err != nil || after.Passwords() {
		os.Remove(file)
		return nil, "", errors.New("another setting comes first (in /etc/ssh/sshd_config, before its Include line): set PasswordAuthentication no there - nothing was changed")
	}
	if err := e.reloadSSH(ctx); err != nil {
		os.Remove(file)
		return nil, "", fmt.Errorf("the SSH server did not take the settings: %v - nothing was changed", err)
	}
	return &entry{DropIn: sshd.KeysOnlyFile}, "SSH takes keys only now: passwords no longer sign in (sessions already open stay)", nil
}

func (e *Engine) passwordsBack(ctx context.Context, en *entry) (string, error) {
	file := e.path(nz(en.DropIn, sshd.KeysOnlyFile))
	old, err := os.ReadFile(file)
	if err != nil {
		return "SSH takes passwords as its own settings say (the file was gone already)", nil
	}
	if err := os.Remove(file); err != nil {
		return "", err
	}
	if msg, err := e.sshdTest(ctx); err != nil {
		_ = os.WriteFile(file, old, 0o644) // its own settings are broken: keys only stays
		return "", fmt.Errorf("the SSH server's own settings do not work (%s) - keys only stays", msg)
	}
	if err := e.reloadSSH(ctx); err != nil {
		return "", err
	}
	return "SSH takes passwords again where its own settings allow them", nil
}

func nz(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// ---------------------------------------------------------------- SSH addresses

// blockSSH refuses addresses on the SSH server's port (nftables, from Blocked): public addresses
// only, never one that signed in over SSH lately.
func (e *Engine) blockSSH(f proto.Fix) (*entry, string, error) {
	var trusted []string
	if e.Trusted != nil {
		trusted = e.Trusted()
	}
	var ok []string
	var skipped int
	for _, s := range f.Addrs {
		a, err := netip.ParseAddr(s)
		if err != nil || a.Zone() != "" {
			skipped++
			continue
		}
		a = a.Unmap()
		if !Public(a) || slices.Contains(trusted, a.String()) {
			skipped++
			continue
		}
		if !slices.Contains(ok, a.String()) && len(ok) < 100 {
			ok = append(ok, a.String())
		}
	}
	if len(ok) == 0 {
		return nil, "", errors.New("none of these addresses can be blocked (private, or one that signed in lately) - nothing was done")
	}
	f.Addrs = ok
	what := addresses(len(ok)) + " can no longer reach SSH on this server"
	if skipped > 0 {
		what += "; " + addresses(skipped) + " left out (private, or one that signed in lately)"
	}
	return &entry{Fix: f}, what, nil
}

// addresses says how many addresses, in words.
func addresses(n int) string {
	if n == 1 {
		return "1 address"
	}
	return fmt.Sprintf("%d addresses", n)
}

// Public says whether an address is on the public internet (what may be blocked from SSH).
func Public(a netip.Addr) bool {
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() {
		return false
	}
	return !netip.MustParsePrefix("100.64.0.0/10").Contains(a) && !netip.MustParsePrefix("198.18.0.0/15").Contains(a)
}

// ---------------------------------------------------------------- helpers for the agent

// Has says whether the journal holds steps of a kind that are not undone.
func (e *Engine) Has(kind string) bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.load()
	for _, en := range e.j.Entries {
		if en.Fix.Kind == kind && en.Undone == 0 {
			return true
		}
	}
	return false
}
