package xray

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"testing"

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

// TestLiveRules: rules applied live take the running balancers along; a rule for a balancer that is
// not running yet waits; what failed stays as it was on disk.
func TestLiveRules(t *testing.T) {
	rules := []any{map[string]any{"ruleTag": "warp", "balancerTag": "lb", "domain": []any{"example.com"}},
		map[string]any{"ruleTag": "no-private", "ip": []any{"geoip:private"}, "outboundTag": "block"}}
	if tag := missingBalancer(rules, []any{map[string]any{"tag": "lb", "selector": []any{"a"}}}); tag != "" {
		t.Errorf("a running balancer counted as missing: %q", tag)
	}
	if tag := missingBalancer(rules, nil); tag != "lb" {
		t.Errorf("missing balancer: %q", tag)
	}
	rest := map[string]any{"routing": map[string]any{"domainStrategy": "AsIs", "balancers": []any{map[string]any{"tag": "lb"}}}}
	if len(balancersIn(rest)) != 1 || balancersIn(map[string]any{}) != nil {
		t.Error("balancersIn")
	}
	full := map[string]any{"outbounds": []any{"new"}, "routing": map[string]any{"rules": []any{"new"}, "domainStrategy": "AsIs"}, "dns": "D"}
	old := map[string]any{"outbounds": []any{"old"}, "routing": map[string]any{"rules": []any{"old"}}}
	got := keepRunning(full, old, true, false)
	if fmt.Sprint(got["outbounds"]) != "[old]" || fmt.Sprint(got["routing"].(map[string]any)["rules"]) != "[new]" || got["dns"] != "D" {
		t.Errorf("outbounds kept: %v", got)
	}
	got = keepRunning(full, old, false, true)
	if fmt.Sprint(got["outbounds"]) != "[new]" || fmt.Sprint(got["routing"].(map[string]any)["rules"]) != "[old]" ||
		got["routing"].(map[string]any)["domainStrategy"] != "AsIs" || fmt.Sprint(full["routing"].(map[string]any)["rules"]) != "[new]" {
		t.Errorf("rules kept: %v (desired %v)", got, full)
	}
	if keepRunning(full, nil, true, true)["outbounds"] == nil {
		t.Error("without a file there is nothing to keep")
	}
}
