package xray

import (
	"fmt"
	"testing"
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
