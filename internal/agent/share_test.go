package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meridian/internal/proto"
	"meridian/internal/seal"
)

// TestSplitShared: what the host collected goes to the panel it belongs to, in that panel's own ids;
// errors naming a guest's ids go to that guest.
func TestSplitShared(t *testing.T) {
	g := func(slot int, id int64) int64 { return toGuest(slot, id) }
	traffic := []proto.UserTraffic{{Sub: 1, Node: 2, Up: 10}, {Sub: g(1, 1), Node: g(1, 2), Up: 20}, {Sub: g(2, 5), Node: g(2, 6), Up: 30}}
	ips := []proto.IPSeen{{Sub: g(1, 1), Node: g(1, 2), IP: "198.51.100.1"}, {Sub: 3, Node: 4, IP: "198.51.100.2"}}
	dests := []proto.DestSeen{{Sub: g(2, 5), Node: g(2, 6), Host: "example.com"}}
	live := &proto.Live{Online: []proto.OnlineUser{{Sub: 1, Node: 2}, {Sub: g(1, 7), Node: g(1, 8)}},
		Certs: []proto.CertState{{ID: 4}, {ID: g(1, 4), Served: []proto.ServedCert{{Node: g(1, 9)}}}},
		Cores: map[string]proto.CoreStatus{"xray": {Running: true}, "hysteria-3": {Running: true}, fmt.Sprintf("hysteria-%d", g(2, 3)): {Running: false}}}
	fwds := []proto.FwdTraffic{{ID: 1}, {ID: g(1, 1)}}
	ht, hi, hd, hf, parts := splitCollected(traffic, ips, dests, live, fwds)
	if len(ht) != 1 || ht[0].Up != 10 || len(hi) != 1 || hi[0].IP != "198.51.100.2" || len(hd) != 0 || len(hf) != 1 || hf[0].ID != 1 {
		t.Errorf("home: %+v %+v %+v %+v", ht, hi, hd, hf)
	}
	if len(live.Online) != 1 || len(live.Certs) != 1 || len(live.Cores) != 2 {
		t.Errorf("home's live: %+v", live)
	}
	p1, p2 := parts[1], parts[2]
	if p1 == nil || p2 == nil {
		t.Fatalf("parts: %v", parts)
	}
	if p1.traffic[0].Sub != 1 || p1.traffic[0].Node != 2 || p1.traffic[0].Up != 20 || p1.ips[0].Sub != 1 || p1.online[0].Sub != 7 ||
		p1.fwds[0].ID != 1 || p1.certs[0].ID != 4 || p1.certs[0].Served[0].Node != 9 {
		t.Errorf("guest 1: %+v", p1)
	}
	if p2.traffic[0].Sub != 5 || p2.dests[0].Node != 6 || !strings.Contains(fmt.Sprint(p2.cores), "hysteria-3") {
		t.Errorf("guest 2: %+v", p2)
	}

	home, guests := splitErrs([]string{"xray: inbound n12 failed", fmt.Sprintf("xray: inbound n%d failed: port 443", g(1, 5)),
		fmt.Sprintf("certificate: waiting for example.com (protocol %d)", g(2, 9))})
	if len(home) != 1 || guests[1][0] != "xray: inbound n5 failed: port 443" || guests[2][0] != "certificate: waiting for example.com (protocol 9)" {
		t.Errorf("errors: %v %v", home, guests)
	}
}

// TestSharedTaken: each panel learns the ports the others use, never its own.
func TestSharedTaken(t *testing.T) {
	home := homeState(t)
	gs := &proto.State{Xray: &proto.Xray{Inbounds: []proto.XrayInbound{inbound(t, "n1", map[string]any{"tag": "n1", "port": 7000, "protocol": "vless"})}},
		Hysteria: []proto.HyNode{{NodeID: 2, Port: 7100, HopPorts: "30000-30002"}}}
	full, errs := mergeShared(home, []guestState{{slot: 1, st: gs}}, []int{50000, 50001})
	if len(errs[1]) != 0 {
		t.Fatal(errs)
	}
	a := &Agent{cfg: &Config{APIPort: 50000}, full: full}
	h := fmt.Sprint(a.takenFor(0))
	if h != "[[7000 7000] [7100 7100] [30000 30002] [50000 50001]]" {
		t.Errorf("the home's: %s", h)
	}
	g := fmt.Sprint(a.takenFor(1))
	for _, want := range []string{"[443 443]", "[8443 8443]", "[9000 9000]", "[20000 20100]", "[50000 50001]", "[51820 51820]"} {
		if !strings.Contains(g, want) {
			t.Errorf("the guest's lacks %s: %s", want, g)
		}
	}
	if strings.Contains(g, "7000") {
		t.Errorf("the guest is told its own port is taken: %s", g)
	}
}

// TestGuestActions: a guest panel may restart the cores and check sites on the internet, nothing
// that reaches the host itself.
func TestGuestActions(t *testing.T) {
	a := &Agent{done: map[int64]int64{}, kick: make(chan struct{}, 1), actions: filepath.Join(t.TempDir(), "a.json")}
	g := &guest{slot: 1, link: ShareLink{Panel: "https://guest.example.com"}, done: map[int64]bool{}}
	ctx := context.Background()
	for kind, want := range map[string]string{
		proto.ActionConsole: "the console of a shared server is its own panel's alone", proto.ActionUpgradeAgent: "upgrades the agent",
		proto.ActionUpgradeXray: "upgrades the agent and the cores", proto.ActionScan: "server's own panel's", proto.ActionStopService: "server's own panel's",
		proto.ActionShareAdd: "server's own panel's",
	} {
		if _, err := a.runGuestAction(ctx, g, proto.Action{ID: 1, Kind: kind}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", kind, err)
		}
	}
	for _, host := range []string{"127.0.0.1", "10.1.2.3", "localhost", "::1", "169.254.169.254"} {
		args, _ := json.Marshal(proto.ExitCheck{Host: host, Port: 50000})
		if _, err := a.runGuestAction(ctx, g, proto.Action{ID: 2, Kind: proto.ActionCheckExit, Args: args}); err == nil {
			t.Errorf("a check of %s was allowed", host)
		}
	}
	args, _ := json.Marshal(proto.TargetCheck{Targets: []proto.TargetSpec{{SNI: "x", Addr: "127.0.0.1:8443", Own: true}}})
	if _, err := a.runGuestAction(ctx, g, proto.Action{ID: 3, Kind: proto.ActionCheckTarget, Args: args}); err == nil {
		t.Error("a check of a site on this host was allowed")
	}
}

// TestShareAdd: only a panel's address with a token that reads, never the home's own, two at most.
func TestShareAdd(t *testing.T) {
	dir := t.TempDir()
	oldDir, oldConf, oldData := ConfDir, ConfPath, DataDir
	ConfDir, ConfPath, DataDir = dir, filepath.Join(dir, "agent.json"), dir
	t.Cleanup(func() { ConfDir, ConfPath, DataDir = oldDir, oldConf, oldData })
	homeTok := seal.Token(1, seal.NewSecret())
	a := &Agent{cfg: &Config{Panel: "https://home.example.com", Token: homeTok, APIPort: 50000}, done: map[int64]int64{}, kick: make(chan struct{}, 1)}
	tok := seal.Token(9, seal.NewSecret())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	add := func(panel, token string) error {
		b, _ := json.Marshal(proto.ShareAdd{Panel: panel, Token: token, Name: "Friend"})
		_, err := a.shareAdd(ctx, b)
		return err
	}
	for _, c := range []struct{ panel, token, want string }{
		{"ftp://x.example.com", tok, "not a panel's address"}, {"https://home.example.com", tok, "this server's own panel"},
		{"http://plain.example.com", tok, "over https"}, {"https://g.example.com", "nonsense", "token cannot be read"},
	} {
		if err := add(c.panel, c.token); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.panel, err)
		}
	}
	if err := add("https://g1.example.com", tok); err != nil {
		t.Fatal(err)
	}
	if err := add("https://g1.example.com/", tok); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("twice: %v", err)
	}
	if err := add("https://g2.example.com:8443", tok); err != nil {
		t.Fatal(err)
	}
	if err := add("https://g3.example.com", tok); err == nil || !strings.Contains(err.Error(), "2 panels at most") {
		t.Errorf("a third: %v", err)
	}
	b, _ := os.ReadFile(ConfPath)
	if !strings.Contains(string(b), "g1.example.com") || !strings.Contains(string(b), "g2.example.com:8443") {
		t.Errorf("not saved: %s", b)
	}
	if st := a.shareStatus(nil); len(st) != 2 || st[0].Panel != "https://g1.example.com" || st[1].Name != "Friend" {
		t.Errorf("status: %+v", st)
	}
	rm, _ := json.Marshal(proto.ShareRemove{Panel: "https://g1.example.com"})
	if _, err := a.shareRemove(rm); err != nil {
		t.Fatal(err)
	}
	if len(a.cfg.Shares) != 1 || len(a.guestList()) != 1 || a.guestList()[0].slot != 2 {
		t.Errorf("after removing one: %+v %d", a.cfg.Shares, len(a.guestList()))
	}
	if err := add("https://g3.example.com", tok); err != nil { // the freed place
		t.Fatal(err)
	}
	if a.guestList()[0].slot != 1 {
		t.Errorf("the freed place was not used: %d", a.guestList()[0].slot)
	}
	for _, g := range a.guestList() { // their links stop before the files go
		a.dropGuest(g.slot)
	}
	time.Sleep(100 * time.Millisecond)
}
