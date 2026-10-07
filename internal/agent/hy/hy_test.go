package hy

import (
	"fmt"
	"testing"
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
