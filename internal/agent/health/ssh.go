package health

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"meridian/internal/agent/protect"
	"meridian/internal/proto"
)

// sshPos is how far the check read the SSH server's messages: the journal's cursor, or a log file
// (its inode and offset).
type sshPos struct {
	Cursor string `json:"cursor,omitempty"`
	File   string `json:"file,omitempty"`
	Inode  uint64 `json:"inode,omitempty"`
	Off    int64  `json:"off,omitempty"`
}

var (
	acceptedRE = regexp.MustCompile(`Accepted (publickey|password|keyboard-interactive/pam|hostbased|gssapi-with-mic) for (\S+) from (\S+) port \d+(?: ssh2(?:: (\S+) (\S+))?)?`)
	// a failed attempt shows up in one to three lines (invalid user, failed password, connection
	// closed before signing in): they are counted once per connection - its address and port
	failedRE = regexp.MustCompile(`Failed \S+ for (?:invalid user )?\S* from (\S+) port (\d+)|Invalid user \S* from (\S+)(?: port (\d+))?|` +
		`(?:Connection closed by|Disconnected from) (?:authenticating|invalid) user \S* (\S+) port (\d+) \[preauth\]`)
	fingerprintRE = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
	cursorRE      = regexp.MustCompile(`^[A-Za-z0-9=;_-]{1,512}$`)
	userRE        = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)
)

// sshLogs are where systems without the journal keep the SSH server's messages.
var sshLogs = []string{"/var/log/auth.log", "/var/log/secure", "/var/log/messages"}

const (
	maxLogRead = 8 << 20 // a scan reads at most this much of a log (the newest part)
	maxLogins  = 20      // sign-ins one scan reports
	burstMin   = 60      // failed sign-ins in 5 minutes that make a burst
)

// Journal reads the SSH server's messages from the system journal: after cursor, or since since when
// there is no cursor yet. journalctl gets fixed arguments and a cursor that was checked first; it
// returns at most the newest 20000 lines.
func Journal(ctx context.Context, cursor string, since time.Time) ([]string, string, error) {
	args := []string{"--no-pager", "--quiet", "--output=short-unix", "--show-cursor", "--lines=20000",
		"--identifier=sshd", "--identifier=sshd-session"}
	if cursor != "" {
		if !cursorRE.MatchString(cursor) {
			return nil, "", errors.New("unexpected journal cursor")
		}
		args = append(args, "--after-cursor="+cursor)
	} else {
		args = append(args, "--since=@"+strconv.FormatInt(since.Unix(), 10))
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", err
	}
	if err := cmd.Start(); err != nil {
		return nil, "", err
	}
	var lines []string
	next := ""
	sc := bufio.NewScanner(io.LimitReader(out, 16<<20))
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		l := sc.Text()
		if c, ok := strings.CutPrefix(l, "-- cursor: "); ok {
			if c = strings.TrimSpace(c); cursorRE.MatchString(c) {
				next = c
			}
			continue
		}
		lines = append(lines, l)
	}
	_, _ = io.Copy(io.Discard, out)
	if err := cmd.Wait(); err != nil && len(lines) == 0 && next == "" {
		return nil, "", err
	}
	return lines, next, nil
}

// checkSSH reads the SSH server's messages since the last scan: who signed in from where (with a key:
// info; with a password: a warning, and high for root) and bursts of failed sign-ins.
//
// A sign-in with a key is recognised by the user and the key, wherever it comes from: marked
// expected, that key's sign-ins are never flagged again. Password sign-ins are recognised by the user
// and the address.
func (sc *scan) checkSSH(ctx context.Context) {
	lines := sc.sshLines(ctx)
	sc.goodSSH(lines) // addresses that signed in: never offered for blocking
	if sc.first {
		return
	}
	type login struct {
		id, user, method, key string // id: the user and the key's fingerprint, or the user and the address
		ips                   []string
		n                     int
	}
	logins := map[string]*login{}
	var order []string
	fails := map[string]int{} // failed attempts by address
	seen := map[string]bool{} // connections counted (address and port)
	total := 0
	for _, l := range lines {
		if m := acceptedRE.FindStringSubmatch(l); m != nil {
			user, ip := m[2], m[3]
			if !userRE.MatchString(user) {
				user = "an unknown account"
			}
			if a, err := netip.ParseAddr(ip); err == nil {
				ip = a.Unmap().String()
			} else {
				ip = "an unknown address"
			}
			method, key, fp := "password", "", ""
			switch m[1] {
			case "publickey":
				method = "key"
				if fingerprintRE.MatchString(m[5]) {
					fp = m[5]
					key = cleanText(m[4], 20) + " " + fp
				}
			case "hostbased", "gssapi-with-mic":
				method = "key" // no password was typed
			}
			id := user + ":" + ip
			if fp != "" {
				id = user + ":" + fp
			}
			k := method + "|" + id
			lg := logins[k]
			if lg == nil {
				if len(order) == maxLogins {
					continue
				}
				lg = &login{id: id, user: user, method: method, key: key}
				logins[k] = lg
				order = append(order, k)
			}
			lg.n++
			if !slices.Contains(lg.ips, ip) && len(lg.ips) < 5 {
				lg.ips = append(lg.ips, ip)
			}
			continue
		}
		if m := failedRE.FindStringSubmatch(l); m != nil {
			ip, port := m[1]+m[3]+m[5], m[2]+m[4]+m[6]
			a, err := netip.ParseAddr(ip)
			if err != nil {
				continue
			}
			ip = a.Unmap().String()
			if port != "" {
				if seen[ip+"|"+port] {
					continue // the same attempt, logged again
				}
				if len(seen) < 100000 {
					seen[ip+"|"+port] = true
				}
			}
			total++
			if _, ok := fails[ip]; ok || len(fails) < 10000 {
				fails[ip]++
			}
		}
	}
	for _, k := range order {
		lg := logins[k]
		times := ""
		if lg.n > 1 {
			times = fmt.Sprintf(" (%d times)", lg.n)
		}
		from := strings.Join(lg.ips, ", ")
		if lg.method == "key" {
			detail := "With an SSH key" + times + "."
			if lg.key != "" {
				detail = "With the SSH key " + lg.key + times + "."
			}
			sc.event(proto.Finding{Key: "ssh-login:" + lg.id, Kind: proto.FindSSH, Severity: proto.SevInfo,
				Title:  lg.user + " signed in over SSH from " + from,
				Detail: detail + " Mark it expected if this is you: sign-ins like it are not flagged again."})
			continue
		}
		sev := proto.SevWarning
		if lg.user == "root" {
			sev = proto.SevHigh
		}
		sc.event(proto.Finding{Key: "ssh-password:" + lg.id, Kind: proto.FindSSH, Severity: sev,
			Title: lg.user + " signed in over SSH with a password from " + from,
			Detail: "With a password" + times + ". Passwords can be guessed: sign in with SSH keys and turn passwords off " +
				"(PasswordAuthentication no in /etc/ssh/sshd_config)."})
	}
	// a burst: as many failures as burstMin in 5 minutes, on average since the scan before (a long
	// gap - the agent was stopped - spreads them out: never a burst that did not happen)
	window := defaultScan
	if sc.prev.ScannedAt > 0 {
		window = sc.at.Sub(time.Unix(sc.prev.ScannedAt, 0))
	}
	window = max(window, time.Minute)
	if float64(total)*float64(5*time.Minute)/float64(window) >= burstMin {
		top, topN := "", 0
		ips := make([]string, 0, len(fails))
		for ip := range fails {
			ips = append(ips, ip)
		}
		sort.Strings(ips)
		for _, ip := range ips {
			if fails[ip] > topN {
				top, topN = ip, fails[ip]
			}
		}
		detail := fmt.Sprintf("%d failed sign-ins in the last %s", total, minutes(window))
		switch {
		case top != "" && len(fails) == 1:
			detail += fmt.Sprintf(", all from %s", top)
		case top != "":
			detail += fmt.Sprintf(", from %d addresses - most from %s (%d)", len(fails), top, topN)
		}
		f := proto.Finding{Key: "ssh-failed", Kind: proto.FindSSH, Severity: proto.SevWarning,
			Title:  "Many failed SSH sign-ins",
			Detail: detail + ". Someone is guessing passwords: use SSH keys and turn passwords off, or let only your own addresses reach SSH."}
		// the addresses that tried most - none that ever signed in - may be refused on SSH
		sort.SliceStable(ips, func(i, j int) bool { return fails[ips[i]] > fails[ips[j]] })
		var worst []string
		for _, ip := range ips {
			if fails[ip] < blockMin || len(worst) == maxBlock {
				break
			}
			if _, ok := sc.next.SSHGood[ip]; !ok && protect.Public(netip.MustParseAddr(ip)) {
				worst = append(worst, ip)
			}
		}
		if len(worst) > 0 {
			f.Fix = &proto.Fix{Kind: proto.FixBlockSSH, Addrs: worst}
		}
		sc.last(f)
	}
}

const (
	blockMin = 10  // failed sign-ins from an address before blocking it is offered
	maxBlock = 20  // addresses one finding offers to block
	maxGood  = 200 // addresses that signed in, kept
	goodDays = 90  // days an address that signed in stays known
)

// goodSSH keeps the addresses that signed in over SSH (the last goodDays days, the newest maxGood).
func (sc *scan) goodSSH(lines []string) {
	cut := sc.at.Add(-goodDays * 24 * time.Hour).Unix()
	next := map[string]int64{}
	for a, t := range sc.prev.SSHGood {
		if t >= cut {
			next[a] = t
		}
	}
	for _, l := range lines {
		if m := acceptedRE.FindStringSubmatch(l); m != nil {
			if a, err := netip.ParseAddr(m[3]); err == nil {
				next[a.Unmap().String()] = sc.at.Unix()
			}
		}
	}
	if len(next) > maxGood {
		addrs := make([]string, 0, len(next))
		for a := range next {
			addrs = append(addrs, a)
		}
		sort.Slice(addrs, func(i, j int) bool { return next[addrs[i]] > next[addrs[j]] })
		for _, a := range addrs[maxGood:] {
			delete(next, a)
		}
	}
	sc.next.SSHGood = next
}

// minutes says how long a span is, in whole minutes or hours.
func minutes(d time.Duration) string {
	if d >= 2*time.Hour {
		return fmt.Sprintf("%d hours", int(d.Hours()+0.5))
	}
	return fmt.Sprintf("%d minutes", max(int(d.Minutes()+0.5), 1))
}

// sshLines are the SSH server's messages since the last scan (none on the first: it only takes the
// position).
func (sc *scan) sshLines(ctx context.Context) []string {
	m, pos := sc.m, sc.prev.SSH
	sc.next.SSH = pos
	if m.Journal != nil {
		lines, next, err := m.Journal(ctx, pos.Cursor, time.Unix(sc.prev.BaselineAt, 0))
		if err == nil {
			if next != "" {
				sc.next.SSH = sshPos{Cursor: next}
			}
			return lines
		}
	}
	for _, f := range sshLogs {
		full := m.path(f)
		fi, err := os.Stat(full)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		ino, size := inode(fi), fi.Size()
		off := pos.Off
		switch {
		case sc.first || pos.File != f:
			// the baseline (or a log the check did not read before): from its end
			sc.next.SSH = sshPos{File: f, Inode: ino, Off: size}
			return nil
		case ino != pos.Inode || size < off:
			off = 0 // rotated: the new file from its start
		}
		if size-off > maxLogRead {
			off = size - maxLogRead
		}
		fh, err := os.Open(full)
		if err != nil {
			return nil
		}
		defer fh.Close()
		buf := make([]byte, size-off)
		n, _ := fh.ReadAt(buf, off)
		buf = buf[:n]
		end := strings.LastIndexByte(string(buf), '\n') + 1 // whole lines only; the rest next time
		sc.next.SSH = sshPos{File: f, Inode: ino, Off: off + int64(end)}
		var out []string
		for _, l := range strings.Split(string(buf[:end]), "\n") {
			if strings.Contains(l, "sshd") {
				out = append(out, l)
			}
		}
		return out
	}
	return nil
}
