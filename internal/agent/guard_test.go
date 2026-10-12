package agent

import (
	"net/netip"
	"testing"
	"time"

	"meridian/internal/proto"
)

// TestGuardBans: an address that fails guardFails times within guardWindow is blocked for guardBan -
// for a day after guardStrikes blocks - each connection counted once; private and trusted addresses
// never; nothing at all while the security mode's SSH watch is off; blocks end when they are over.
func TestGuardBans(t *testing.T) {
	var g guardian
	t0 := time.Unix(1_800_000_000, 0)
	attacker, friend := netip.MustParseAddr("198.51.100.66"), netip.MustParseAddr("203.0.113.9")
	spared := func(a netip.Addr) bool { return a == friend || a.IsPrivate() }
	fails := func(a netip.Addr, n, from int, at time.Time) []guardFail {
		var out []guardFail
		for i := 0; i < n; i++ {
			out = append(out, guardFail{addr: a, conn: a.String() + ":" + string(rune('a'+from+i)), at: at})
		}
		return out
	}
	if got := g.count(fails(attacker, 9, 0, t0), t0, spared); got != nil {
		t.Fatalf("blocked %v while the security mode is off", got)
	}
	g.set(&proto.Guard{SSHBan: true})
	if got := g.count(fails(attacker, guardFails-1, 0, t0), t0, spared); len(got) != 0 {
		t.Fatalf("blocked after %d failures: %v", guardFails-1, got)
	}
	// the same connections again (a failure takes up to three lines) count nothing
	if got := g.count(fails(attacker, guardFails-1, 0, t0), t0, spared); len(got) != 0 {
		t.Fatalf("connections counted twice: %v", got)
	}
	if got := g.count(fails(attacker, 1, 10, t0.Add(time.Minute)), t0.Add(time.Minute), spared); len(got) != 1 || got[0] != attacker.String() {
		t.Fatalf("not blocked after %d failures: %v", guardFails, got)
	}
	if b := g.status().Banned; len(b) != 1 || b[0].Until != t0.Add(time.Minute+guardBan).Unix() {
		t.Errorf("block: %+v", b)
	}
	for _, a := range []netip.Addr{friend, netip.MustParseAddr("10.0.0.7")} {
		if got := g.count(fails(a, 20, 30, t0), t0, spared); len(got) != 0 {
			t.Errorf("blocked %s: %v", a, got)
		}
	}
	// failures spread wider than the window do not add up
	slow := netip.MustParseAddr("192.0.2.77")
	for i := 0; i < guardFails+2; i++ {
		at := t0.Add(time.Duration(i) * (guardWindow / 2))
		if got := g.count(fails(slow, 1, 40+i, at), at, spared); len(got) != 0 {
			t.Fatalf("blocked failures %s apart: %v", guardWindow/2, got)
		}
	}
	// the block ends; after guardStrikes blocks in a day, the next is for a day
	if gone := g.expire(t0.Add(time.Minute + guardBan)); len(gone) != 1 {
		t.Fatalf("the block did not end: %v", gone)
	}
	at := t0.Add(2 * guardBan)
	for k := 1; k < guardStrikes-1; k++ { // the first block was one of them
		g.count(fails(attacker, guardFails, 100+k*10, at), at, spared)
		at = at.Add(guardBan + time.Minute)
		g.expire(at)
	}
	b := g.status().Banned
	if len(b) != 0 {
		t.Fatalf("still blocked: %+v", b)
	}
	g.count(fails(attacker, guardFails, 200, at), at, spared)
	if b = g.status().Banned; len(b) != 1 || time.Unix(b[0].Until, 0).Sub(at) != guardLong {
		t.Errorf("after %d blocks in a day: %+v", guardStrikes, b)
	}
	// off: no blocks are kept
	g.set(&proto.Guard{KeysOnly: true})
	if len(g.banned()) != 0 {
		t.Errorf("blocks kept with the SSH watch off: %v", g.banned())
	}
	if g.status().KeysOnly != "" || g.status() == nil {
		t.Errorf("status: %+v", g.status())
	}
	g.set(nil)
	if g.status() != nil {
		t.Error("a status while the security mode is off")
	}
}

// TestGuardLines: the SSH server's failed sign-ins, whatever their wording, each with its address and
// connection.
func TestGuardLines(t *testing.T) {
	lines := []string{
		"1791800000.000000 host sshd[123]: Failed password for root from 198.51.100.66 port 51234 ssh2",
		"1791800000.000000 host sshd[123]: Failed password for invalid user admin from 198.51.100.66 port 51235 ssh2",
		"1791800000.000000 host sshd[124]: Invalid user test from 2001:db8::66 port 40000",
		"1791800000.000000 host sshd[125]: Connection closed by authenticating user root 203.0.113.5 port 2222 [preauth]",
		"1791800000.000000 host sshd[126]: Accepted publickey for root from 203.0.113.9 port 50000 ssh2: ED25519 SHA256:x",
	}
	got := guardFails2(lines, time.Unix(1_800_000_000, 0))
	want := []string{"198.51.100.66:51234", "198.51.100.66:51235", "2001:db8::66:40000", "203.0.113.5:2222"}
	if len(got) != len(want) {
		t.Fatalf("found %d failures: %+v", len(got), got)
	}
	for i, f := range got {
		if f.conn != want[i] {
			t.Errorf("failure %d: %s, want %s", i, f.conn, want[i])
		}
	}
}
