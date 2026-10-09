package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"testing"

	"meridian/internal/db"
)

// TestUpgradeFrom074 opens a panel database made by Meridian 0.7.4 (testdata/upgrade-0.7.4: servers
// with every kind of protocol, a proxy pass, server and protocol code, protocols on their own
// addresses, an IPv4-only server, users - one paused -, a forward and a block) and compiles every
// server: each must get exactly what 0.7.4 sent it. An upgrade changes nothing on the servers - no
// restart, nobody disconnected - until the operator uses something new.
func TestUpgradeFrom074(t *testing.T) {
	fixture := filepath.Join("testdata", "upgrade-0.7.4")
	dir := t.TempDir()
	raw, err := os.ReadFile(filepath.Join(fixture, "meridian.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meridian.db"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	agentDir := filepath.Join(dir, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if err := os.WriteFile(filepath.Join(agentDir, "meridian-agent-linux-"+arch), []byte("fake agent "+arch), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d, err := db.Open(filepath.Join(dir, "meridian.db")) // runs the new migrations
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	p, err := New(Config{DataDir: dir, AgentDir: agentDir}, d)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]json.RawMessage
	b, err := os.ReadFile(filepath.Join(fixture, "states.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(want))
	for k := range want {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	for _, k := range ids {
		sid, _ := strconv.ParseInt(k, 10, 64)
		st, err := p.compileServer(context.Background(), sid)
		if err != nil {
			t.Fatalf("server %s: %v", k, err)
		}
		got, _ := json.Marshal(st)
		a, b := normState(t, want[k]), normState(t, got)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("server %s gets a different configuration after the upgrade:\n%s", k, firstDiff("", a, b))
		}
	}
}

// normState leaves out what changes with every version by design: the revision and the version
// agents are told is the newest (their own upgrade is separate), and the cores' download digests.
func normState(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "rev")
	if a, ok := m["agent"].(map[string]any); ok {
		delete(a, "latest_version")
	}
	if c, ok := m["cores"].(map[string]any); ok {
		delete(c, "digests")
	}
	return m
}

// firstDiff names the first place two decoded JSON values differ.
func firstDiff(path string, a, b any) string {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			return fmt.Sprintf("%s: %v -> %v", path, a, b)
		}
		keys := map[string]bool{}
		for k := range x {
			keys[k] = true
		}
		for k := range y {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			if !reflect.DeepEqual(x[k], y[k]) {
				return firstDiff(path+"."+k, x[k], y[k])
			}
		}
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return fmt.Sprintf("%s: %d items -> %v", path, len(x), b)
		}
		for i := range x {
			if !reflect.DeepEqual(x[i], y[i]) {
				return firstDiff(fmt.Sprintf("%s[%d]", path, i), x[i], y[i])
			}
		}
	case string:
		// configurations travel as JSON strings inside the state: compare them decoded
		var ja, jb any
		if json.Unmarshal([]byte(x), &ja) == nil {
			if s, ok := b.(string); ok && json.Unmarshal([]byte(s), &jb) == nil {
				return firstDiff(path+"(json)", ja, jb)
			}
		}
	}
	return fmt.Sprintf("%s: %v -> %v", path, a, b)
}
