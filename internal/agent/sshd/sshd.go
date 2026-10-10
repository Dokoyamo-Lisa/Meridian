// Package sshd reads what the SSH server of this host does: its effective settings (sshd -T) and the
// keys in authorized_keys files - for the health check, which tells, and for the protective steps
// the supervisor confirms (agent/protect), which act.
package sshd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DropInDir is where settings of their own go, read before the main file's on systems that include
// it (Debian, Ubuntu and most others since OpenSSH 8.2): the first value an SSH server reads wins.
const DropInDir = "/etc/ssh/sshd_config.d"

// KeysOnlyFile is the settings file of the protective step that makes SSH take keys only. Its name
// sorts first, so its settings come before every other file's.
const KeysOnlyFile = DropInDir + "/00-meridian-keys-only.conf"

// KeyFingerprint reads one authorized_keys line: the key's fingerprint as ssh-keygen -l shows it,
// its type and its comment. Options before the key ("command=...", "from=...") are skipped.
func KeyFingerprint(line string) (fp, typ, comment string, ok bool) {
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
		return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), x, strings.Join(f[i+2:], " "), true
	}
	return "", "", "", false
}

// Keys counts the keys in an authorized_keys file's content.
func Keys(content string) int {
	n := 0
	for _, l := range strings.Split(content, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			if _, _, _, ok := KeyFingerprint(l); ok {
				n++
			}
		}
	}
	return n
}

// Settings are the SSH server's effective settings, as sshd -T prints them: lower-case names, every
// value of each (a name such as port may come more than once).
type Settings map[string][]string

// Get is a setting's first value ("" when it is not there).
func (s Settings) Get(name string) string {
	if v := s[name]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// Ports are the TCP ports the SSH server listens on (22 when it says none).
func (s Settings) Ports() []int {
	var out []int
	for _, v := range s["port"] {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n < 65536 && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		out = []int{22}
	}
	slices.Sort(out)
	return out
}

// Passwords says whether someone can sign in by typing a password: password authentication, or the
// keyboard-interactive kind (challengeresponseauthentication before OpenSSH 8.7).
func (s Settings) Passwords() bool {
	return s.Get("passwordauthentication") == "yes" || s.Get("kbdinteractiveauthentication") == "yes" ||
		s.Get("challengeresponseauthentication") == "yes"
}

// RootKeys says whether root may sign in with a key.
func (s Settings) RootKeys() bool {
	switch s.Get("permitrootlogin") {
	case "yes", "prohibit-password", "without-password":
		return true
	}
	return false
}

// Parse reads sshd -T's output.
func Parse(out string) Settings {
	s := Settings{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		name, val, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if !ok || name == "" || len(s) > 500 {
			continue
		}
		name = strings.ToLower(name)
		if len(s[name]) < 64 {
			s[name] = append(s[name], strings.TrimSpace(val))
		}
	}
	return s
}

// Binary is the SSH server's program, "" when there is none.
func Binary(root string) string {
	for _, p := range []string{"/usr/sbin/sshd", "/usr/bin/sshd", "/usr/local/sbin/sshd", "/sbin/sshd"} {
		if fi, err := os.Stat(filepath.Join(root, p)); err == nil && fi.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// Read runs sshd -T: the settings the SSH server would use now. It needs root (sshd reads its host
// keys); the arguments are fixed.
func Read(ctx context.Context) (Settings, error) {
	bin := Binary("/")
	if bin == "" {
		return nil, errors.New("no SSH server here")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "-T").Output()
	if err != nil {
		return nil, err
	}
	s := Parse(string(out))
	if len(s) == 0 {
		return nil, errors.New("sshd -T said nothing")
	}
	return s, nil
}

// Test runs sshd -t: whether the SSH server would start with its settings as they are on disk. The
// message is sshd's own.
func Test(ctx context.Context) (string, error) {
	bin := Binary("/")
	if bin == "" {
		return "", errors.New("no SSH server here")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "-t").CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// ReadsDropIns says whether the main settings file includes DropInDir's files: then settings written
// there apply (before the main file's own, when the Include line comes first - as on Debian and
// Ubuntu; sshd -T tells for sure).
func ReadsDropIns(root string) bool {
	b, err := os.ReadFile(filepath.Join(root, "/etc/ssh/sshd_config"))
	if err != nil || len(b) > 1<<20 {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || !strings.EqualFold(f[0], "include") {
			continue
		}
		for _, g := range f[1:] {
			if !filepath.IsAbs(g) {
				g = filepath.Join("/etc/ssh", g)
			}
			if g == DropInDir+"/*.conf" || g == DropInDir+"/*" {
				return true
			}
		}
	}
	return false
}
