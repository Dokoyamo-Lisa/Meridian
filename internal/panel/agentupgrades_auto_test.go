package panel

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"meridian/internal/proto"
)

// TestAgentAutoUpgrade: an online agent older than the panel gets an upgrade with nobody asked, once;
// one that is offline, newer, on another panel's server or under a panel set not to is left alone.
func TestAgentAutoUpgrade(t *testing.T) {
	oldVersion := Version
	Version = "2.0.0"
	defer func() { Version = oldVersion }()
	h := newHarness(t)
	h.p.cfg.AgentDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(h.p.cfg.AgentDir, "meridian-agent-linux-amd64"), []byte("agent"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := h.browser()
	b.login("owner", "owner-password-1")
	add := func(name, version string, online, guest int) int64 {
		sid := id(b.must("POST", "/api/servers", map[string]any{"name": name, "address": "203.0.113.9"}, 201)["server"].(map[string]any)["id"])
		if _, err := h.p.db.Exec1(`UPDATE servers SET first_seen_at = ?, agent_version = ?, online = ?, guest = ? WHERE id = ?`, now(), version, online, guest, sid); err != nil {
			t.Fatal(err)
		}
		return sid
	}
	behind := add("Behind", "1.9.0", 1, 0)
	away := add("Away", "1.9.0", 0, 0)
	ahead := add("Ahead", "2.1.0", 1, 0)
	guest := add("Guest", "1.9.0", 1, 1)
	same := add("Same", "2.0.0", 1, 0)
	count := func(sid int64) (n int) {
		_ = h.p.db.QueryRow(`SELECT COUNT(*) FROM actions WHERE server_id = ? AND kind = ?`, sid, proto.ActionUpgradeAgent).Scan(&n)
		return n
	}

	set := b.must("GET", "/api/settings", nil, 200)
	if set["agent_auto_upgrade"] != true {
		t.Fatalf("off by default: %v", set["agent_auto_upgrade"])
	}
	set["agent_auto_upgrade"] = false
	b.must("PUT", "/api/settings", set, 200)
	h.p.agentAutoUpgrade(context.Background())
	if count(behind) != 0 {
		t.Error("upgraded with the setting off")
	}
	set["agent_auto_upgrade"] = true
	b.must("PUT", "/api/settings", set, 200)

	h.p.agentAutoUpgrade(context.Background())
	if count(behind) != 1 {
		t.Errorf("the agent behind: %d upgrades", count(behind))
	}
	for name, sid := range map[string]int64{"offline": away, "newer": ahead, "shared": guest, "current": same} {
		if n := count(sid); n != 0 {
			t.Errorf("%s: %d upgrades", name, n)
		}
	}
	// one that did not take is not tried again at once, however often the job runs
	if _, err := h.p.db.Exec1(`UPDATE actions SET status = 'failed' WHERE server_id = ?`, behind); err != nil {
		t.Fatal(err)
	}
	h.p.agentAutoUpgrade(context.Background())
	if count(behind) != 1 {
		t.Errorf("tried again at once: %d", count(behind))
	}
	if _, err := h.p.db.Exec1(`UPDATE actions SET created_at = ? WHERE server_id = ?`, now()-agentAutoRetry-1, behind); err != nil {
		t.Fatal(err)
	}
	h.p.agentAutoUpgrade(context.Background())
	if count(behind) != 2 {
		t.Errorf("never tried again: %d", count(behind))
	}
}
