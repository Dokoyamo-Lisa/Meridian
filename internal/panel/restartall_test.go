package panel

import (
	"fmt"
	"strings"
	"testing"

	"meridian/internal/proto"
)

// TestRestartAll: "restart everything" goes to a server only when its agent can do it (1.3.1); for
// every server at once, the ones it cannot reach are named with the reason - offline, an older agent
// - and nothing is queued for them. A read-only token cannot restart anything.
func TestRestartAll(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	server := func(name, ip string, caps *proto.Caps) int64 {
		sid := id(b.must("POST", "/api/servers", map[string]any{"name": name, "address": ip}, 201)["server"].(map[string]any)["id"])
		if caps != nil {
			helloCaps(t, h, sid, ip, *caps)
		}
		return sid
	}
	now := server("New", "203.0.113.71", &proto.Caps{Systemd: true, Nftables: true, RestartPending: true, Solo: true, AnyTLS: true, RestartAll: true})
	old := server("Old", "203.0.113.72", &proto.Caps{Systemd: true, Nftables: true, RestartPending: true, Solo: true})
	server("Waiting", "203.0.113.73", nil) // its agent never connected

	// one server
	if m := b.must("POST", fmt.Sprintf("/api/servers/%d/actions", old), map[string]any{"kind": "restart_all"}, 400); !strings.Contains(fmt.Sprint(m["error"]), "1.3.1") {
		t.Errorf("an agent that cannot: %v", m["error"])
	}
	b.must("POST", fmt.Sprintf("/api/servers/%d/actions", now), map[string]any{"kind": "restart_all"}, 202)
	queued := func(sid int64) int {
		n := 0
		for _, a := range stateOf(t, h, sid).Actions {
			if a.Kind == proto.ActionRestartAll {
				n++
			}
		}
		return n
	}
	if queued(now) != 1 || queued(old) != 0 {
		t.Fatalf("queued: %d on the new agent, %d on the old one", queued(now), queued(old))
	}

	// every server
	read := b.must("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"}, 201)["token"].(string)
	h.bearer(read).must("POST", "/api/servers/restart-all", nil, 403)
	m := b.must("POST", "/api/servers/restart-all", nil, 202)
	if fmt.Sprint(m["servers"]) != "[New]" {
		t.Errorf("restarted: %v", m["servers"])
	}
	skipped := fmt.Sprint(m["skipped"])
	if !strings.Contains(skipped, "Old") || !strings.Contains(skipped, "1.3.1") || !strings.Contains(skipped, "Waiting") {
		t.Errorf("left out: %s", skipped)
	}
	if queued(now) != 2 || queued(old) != 0 {
		t.Errorf("queued: %d on the new agent, %d on the old one", queued(now), queued(old))
	}
}
