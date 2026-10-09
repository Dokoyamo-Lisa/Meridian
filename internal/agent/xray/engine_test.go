package xray

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"meridian/internal/proto"
)

// TestWithRunning: until the restart, the file on disk keeps the sections that need one as Xray runs
// them, and takes everything that applies live.
func TestWithRunning(t *testing.T) {
	full := map[string]any{"inbounds": []any{"new-in"}, "outbounds": []any{"new-out"},
		"routing": map[string]any{"rules": []any{"new-rule"}, "domainStrategy": "IPIfNonMatch"},
		"dns":     map[string]any{"servers": []any{"1.1.1.1"}}, "log": "L"}
	running := map[string]any{"routing": map[string]any{"domainStrategy": "AsIs"}, "log": "L"}
	got := withRunning(full, running)
	rt := got["routing"].(map[string]any)
	if got["dns"] != nil || rt["domainStrategy"] != "AsIs" || fmt.Sprint(rt["rules"]) != "[new-rule]" ||
		fmt.Sprint(got["inbounds"]) != "[new-in]" || fmt.Sprint(got["outbounds"]) != "[new-out]" || got["log"] != "L" {
		t.Errorf("got %v", got)
	}
}

// TestMissingAddress: an inbound on an address the host does not have is left out (with a note), so
// it cannot stop Xray from starting and take every other protocol down; any address, loopback and
// addresses the host has stay.
func TestMissingAddress(t *testing.T) {
	e := &Engine{RunDir: t.TempDir(), APIPort: 50000, HasAddr: func(a netip.Addr) bool { return a.String() == "192.0.2.1" }}
	in := func(tag, listen string) proto.XrayInbound {
		cfg := fmt.Sprintf(`{"tag":%q,"port":443,"protocol":"vless","settings":{"decryption":"none"}}`, tag)
		if listen != "" {
			cfg = fmt.Sprintf(`{"tag":%q,"listen":%q,"port":443,"protocol":"vless","settings":{"decryption":"none"}}`, tag, listen)
		}
		return proto.XrayInbound{Tag: tag, Config: json.RawMessage(cfg)}
	}
	r, err := e.render(&proto.Xray{Inbounds: []proto.XrayInbound{in("n1", ""), in("n2", "192.0.2.1"), in("n3", "198.51.100.9"),
		in("n4", "0.0.0.0"), in("n5", "127.0.0.2"), in("n6", "::")}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.inbounds["n3"]; ok || len(r.inbounds) != 5 || len(r.full["inbounds"].([]any)) != 5 {
		t.Errorf("inbounds: %v", r.inbounds)
	}
	if len(r.missing) != 1 || !strings.Contains(r.missing[0], "n3") || !strings.Contains(r.missing[0], "198.51.100.9") {
		t.Errorf("missing: %v", r.missing)
	}
}

// TestGuard: every freedom outbound marks its connections for the kernel guard unless it sets a mark
// of its own; without the kernel guard, routing resolves names first so the private-address rule sees
// them; an inbound that would send strangers to a control port is left out.
func TestGuard(t *testing.T) {
	base := json.RawMessage(`{"outbounds":[{"tag":"direct","protocol":"freedom"},{"tag":"block","protocol":"blackhole"},
		{"tag":"mine","protocol":"freedom","streamSettings":{"sockopt":{"mark":7}}},
		{"tag":"bind-n3","protocol":"freedom","sendThrough":"192.0.2.1","settings":{"domainStrategy":"UseIPv4"}}],
		"routing":{"domainStrategy":"AsIs","rules":[]}}`)
	reality := func(tag, target string) proto.XrayInbound {
		return proto.XrayInbound{Tag: tag, Config: json.RawMessage(fmt.Sprintf(`{"tag":%q,"port":443,"protocol":"vless",
			"settings":{"decryption":"none"},"streamSettings":{"security":"reality","realitySettings":{"target":%q}}}`, tag, target))}
	}
	fallback := proto.XrayInbound{Tag: "n5", Config: json.RawMessage(`{"tag":"n5","port":8443,"protocol":"trojan",
		"settings":{"fallbacks":[{"dest":50000}]}}`)}
	for _, kernel := range []bool{true, false} {
		e := &Engine{RunDir: t.TempDir(), APIPort: 50000, KernelGuard: func() bool { return kernel },
			Reserved: func() []int { return []int{50000, 50001, 41234} }}
		r, err := e.render(&proto.Xray{Base: base, Inbounds: []proto.XrayInbound{reality("n1", "www.example.com:443"),
			reality("n2", "127.0.0.1:50000"), reality("n3", "localtest.me:41234"), reality("n4", "127.0.0.1:8443"), fallback}}, false)
		if err != nil {
			t.Fatal(err)
		}
		mark := func(tag string) any {
			ob, _ := r.outbounds[tag].(map[string]any)
			ss, _ := ob["streamSettings"].(map[string]any)
			so, _ := ss["sockopt"].(map[string]any)
			return so["mark"]
		}
		if fmt.Sprint(mark("direct")) != fmt.Sprint(0x4d580000) || fmt.Sprint(mark("bind-n3")) != fmt.Sprint(0x4d580000) ||
			fmt.Sprint(mark("mine")) != "7" || mark("block") != nil {
			t.Errorf("marks: direct %v, bind-n3 %v, mine %v, block %v", mark("direct"), mark("bind-n3"), mark("mine"), mark("block"))
		}
		ds := r.rest["routing"].(map[string]any)["domainStrategy"]
		if kernel && ds != "AsIs" || !kernel && ds != "IPOnDemand" {
			t.Errorf("kernel guard %v: domainStrategy %v", kernel, ds)
		}
		var left []string
		for tag := range r.inbounds {
			left = append(left, tag)
		}
		if len(r.inbounds) != 2 || r.inbounds["n1"] == nil || r.inbounds["n4"] == nil || len(r.missing) != 3 {
			t.Errorf("inbounds %v, refused %v", left, r.missing)
		}
	}
}

// TestLiveRules: rules and balancers apply live, together; a balancer that picks the fastest member
// picks at random until Xray runs its latency checks (they need a restart); what failed stays as it
// was on disk.
func TestLiveRules(t *testing.T) {
	bals := []any{map[string]any{"tag": "lb-1", "selector": []any{"lb1."}, "strategy": map[string]any{"type": "leastPing"}, "fallbackTag": "block"},
		map[string]any{"tag": "lb-2", "selector": []any{"lb2."}, "strategy": map[string]any{"type": "roundRobin"}}}
	got := runningBalancers(bals, map[string]any{"log": "L"}).([]any)
	if fmt.Sprint(got[0].(map[string]any)["strategy"]) != "map[type:random]" || got[0].(map[string]any)["fallbackTag"] != nil ||
		fmt.Sprint(got[1].(map[string]any)["strategy"]) != "map[type:roundRobin]" || bals[0].(map[string]any)["fallbackTag"] != "block" ||
		fmt.Sprint(bals[0].(map[string]any)["strategy"]) != "map[type:leastPing]" {
		t.Errorf("without latency checks: %v (desired %v)", got, bals)
	}
	if got := runningBalancers(bals, map[string]any{"observatory": map[string]any{}}); !same(got, bals) {
		t.Errorf("with latency checks: %v", got)
	}
	full := map[string]any{"outbounds": []any{"new"}, "routing": map[string]any{"rules": []any{"new"}, "domainStrategy": "AsIs",
		"balancers": bals}, "dns": "D"}
	if rt := withBalancers(full, got)["routing"].(map[string]any); fmt.Sprint(rt["balancers"]) != fmt.Sprint(got) ||
		fmt.Sprint(full["routing"].(map[string]any)["balancers"]) != fmt.Sprint(bals) {
		t.Errorf("withBalancers: %v", rt)
	}
	if rt := withBalancers(full, nil)["routing"].(map[string]any); rt["balancers"] != nil || rt["rules"] == nil {
		t.Errorf("without balancers: %v", rt)
	}
	old := map[string]any{"outbounds": []any{"old"}, "routing": map[string]any{"rules": []any{"old"}, "balancers": []any{"old-lb"}}}
	kept := keepRunning(full, old, true, false)
	if fmt.Sprint(kept["outbounds"]) != "[old]" || fmt.Sprint(kept["routing"].(map[string]any)["rules"]) != "[new]" || kept["dns"] != "D" {
		t.Errorf("outbounds kept: %v", kept)
	}
	kept = keepRunning(full, old, false, true)
	rt := kept["routing"].(map[string]any)
	if fmt.Sprint(kept["outbounds"]) != "[new]" || fmt.Sprint(rt["rules"]) != "[old]" || fmt.Sprint(rt["balancers"]) != "[old-lb]" ||
		rt["domainStrategy"] != "AsIs" || fmt.Sprint(full["routing"].(map[string]any)["rules"]) != "[new]" {
		t.Errorf("rules kept: %v (desired %v)", kept, full)
	}
	if keepRunning(full, nil, true, true)["outbounds"] == nil {
		t.Error("without a file there is nothing to keep")
	}
	// waiting for a restart, the file has the live parts as desired and the rest as running
	w := withRunning(full, map[string]any{"routing": map[string]any{"domainStrategy": "IPIfNonMatch", "balancers": []any{"stale"}}})
	wr := w["routing"].(map[string]any)
	if wr["domainStrategy"] != "IPIfNonMatch" || fmt.Sprint(wr["balancers"]) != fmt.Sprint(bals) || fmt.Sprint(wr["rules"]) != "[new]" {
		t.Errorf("withRunning: %v", wr)
	}
	if msg := restChange(map[string]any{"log": "L"}, map[string]any{"log": "L", "observatory": map[string]any{}}); !strings.Contains(msg, "latency checks for a load balancer") ||
		!strings.Contains(msg, "at random until then") {
		t.Errorf("restart reason: %q", msg)
	}
	// the last fastest-first balancer gone: its checks stop with the next restart, nothing breaks meanwhile
	if msg := restChange(map[string]any{"observatory": map[string]any{}}, map[string]any{}); !strings.Contains(msg, "stopping latency checks") {
		t.Errorf("checks no longer needed: %q", msg)
	}
	if msg := restChange(map[string]any{"dns": "a", "log": "x"}, map[string]any{"dns": "b", "log": "y"}); msg != "Xray's DNS settings; global Xray settings" {
		t.Errorf("several reasons: %q", msg)
	}
	// checks no longer wanted ask for no restart; anything else that changed with them still does
	obs := map[string]any{"subjectSelector": []any{"lb"}}
	if !checksGone(map[string]any{"log": "L", "observatory": obs}, map[string]any{"log": "L"}) {
		t.Error("only the checks gone: a restart is asked for")
	}
	for _, c := range [][2]map[string]any{
		{{"log": "L", "observatory": obs}, {"log": "M"}},
		{{"log": "L"}, {"log": "L", "observatory": obs}},
		{{"log": "L", "observatory": obs}, {"log": "L", "observatory": map[string]any{}}},
		{{"log": "L"}, {"log": "L"}},
	} {
		if checksGone(c[0], c[1]) {
			t.Errorf("%v -> %v: no restart asked for", c[0], c[1])
		}
	}
}

// TestOperatorInboundStays: an inbound from the operator's own code keeps its users on disk as in the
// rendered state, so it compares equal (and stays open) when users of the panel's protocols change;
// a protocol's inbound is still compared without its users.
func TestOperatorInboundStays(t *testing.T) {
	e := &Engine{RunDir: t.TempDir(), APIPort: 50000}
	own := proto.XrayInbound{Tag: "my-vless", Config: json.RawMessage(`{"tag":"my-vless","port":8443,"protocol":"vless",
		"settings":{"clients":[{"id":"6f1c2d3e-0000-4000-8000-000000000001","email":"me"}],"decryption":"none"}}`)}
	socks := proto.XrayInbound{Tag: "my-socks", Config: json.RawMessage(`{"tag":"my-socks","port":1080,"protocol":"socks",
		"settings":{"auth":"password","accounts":[{"user":"u","pass":"p"}]}}`)}
	panelIn := func(emails ...string) proto.XrayInbound {
		in := proto.XrayInbound{Tag: "n1", Config: json.RawMessage(`{"tag":"n1","port":443,"protocol":"vless","settings":{"decryption":"none"}}`)}
		for i, m := range emails {
			in.Clients = append(in.Clients, proto.XrayClient{Email: m,
				JSON: json.RawMessage(fmt.Sprintf(`{"id":"6f1c2d3e-0000-4000-8000-00000000001%d","email":%q}`, i, m))})
		}
		return in
	}
	before, err := e.render(&proto.Xray{Inbounds: []proto.XrayInbound{panelIn("a"), own, socks}}, false)
	if err != nil {
		t.Fatal(err)
	}
	after, err := e.render(&proto.Xray{Inbounds: []proto.XrayInbound{panelIn("a", "b"), own, socks}}, false)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(marshal(before.full), &onDisk); err != nil {
		t.Fatal(err)
	}
	prev := parseFull(onDisk)
	for _, tag := range []string{"my-vless", "my-socks", "n1"} {
		if !same(prev.inbounds[tag], after.inbounds[tag]) {
			t.Errorf("%s: on disk %v, rendered %v", tag, prev.inbounds[tag], after.inbounds[tag])
		}
	}
	if len(prev.clients["n1"]) != 1 || len(after.clients["n1"]) != 2 {
		t.Errorf("n1 users: %v -> %v", prev.clients["n1"], after.clients["n1"])
	}
	// a change to the operator's own users is a change to that inbound
	own2 := own
	own2.Config = json.RawMessage(strings.Replace(string(own.Config), `"me"`, `"me2"`, 1))
	changed, err := e.render(&proto.Xray{Inbounds: []proto.XrayInbound{panelIn("a"), own2, socks}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if same(prev.inbounds["my-vless"], changed.inbounds["my-vless"]) {
		t.Error("a changed operator inbound compares equal")
	}
}

// TestLockWithin: a report waits a short while for the engine, then goes without it.
func TestLockWithin(t *testing.T) {
	var mu sync.Mutex
	if !lockWithin(&mu, time.Millisecond) {
		t.Fatal("a free lock was not taken")
	}
	go func() { time.Sleep(100 * time.Millisecond); mu.Unlock() }()
	if !lockWithin(&mu, 2*time.Second) {
		t.Fatal("a lock freed in time was not taken")
	}
	start := time.Now()
	if lockWithin(&mu, 200*time.Millisecond) {
		t.Fatal("a held lock was taken")
	}
	if d := time.Since(start); d < 200*time.Millisecond || d > 2*time.Second {
		t.Errorf("waited %v", d)
	}
}
