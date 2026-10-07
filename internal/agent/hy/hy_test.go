package hy

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"meridian/internal/proto"
)

func TestPruneDevices(t *testing.T) {
	now := int64(10_000_000)
	e := &Engine{devices: map[string]map[string]int64{"1/s1.n1": {}, "1/s2.n1": {"192.0.2.1": now - 8*86400}}}
	for i := 0; i < 40; i++ {
		e.devices["1/s1.n1"][fmt.Sprintf("192.0.2.%d", i)] = now - int64(i)*60
	}
	e.pruneDevices(now)
	if len(e.devices["1/s1.n1"]) != 32 {
		t.Fatalf("kept %d", len(e.devices["1/s1.n1"]))
	}
	if _, ok := e.devices["1/s1.n1"]["192.0.2.0"]; !ok {
		t.Error("the newest address was dropped")
	}
	if _, ok := e.devices["1/s1.n1"]["192.0.2.39"]; ok {
		t.Error("the oldest address was kept")
	}
	if _, ok := e.devices["1/s2.n1"]; ok {
		t.Error("a user unseen for a week is still remembered")
	}
}

// TestRenderCustom: the operator's configuration is merged in; auth and trafficStats stay the agent's.
func TestRenderCustom(t *testing.T) {
	e := &Engine{ConfDir: t.TempDir(), AuthPort: 50001}
	n := proto.HyNode{NodeID: 7, Port: 443, Custom: json.RawMessage(`{"quic":{"maxIdleTimeout":"60s"},
		"auth":{"type":"password","password":"x"},"sniff":{"timeout":"5s"},"obfs":null}`)}
	out, err := e.render(n, portInfo{Port: 1234, Secret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	y := string(out)
	for _, want := range []string{"maxIdleTimeout: 60s", "type: http", "timeout: 5s", "enable: true"} {
		if !strings.Contains(y, want) {
			t.Errorf("missing %q in:\n%s", want, y)
		}
	}
	if strings.Contains(y, "password") {
		t.Errorf("the operator's auth replaced the agent's:\n%s", y)
	}
}
