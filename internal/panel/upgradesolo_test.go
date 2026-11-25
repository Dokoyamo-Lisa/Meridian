package panel

import (
	"fmt"
	"strings"
	"testing"

	"meridian/internal/proto"
)

// TestUpgradeSolo: upgrading mieru, Snell and AnyTLS goes to a server only when its agent can do it
// (1.5.3); a read-only token cannot ask for it. (The versions travel in the state of a server that
// runs those protocols: State.Cores.)
func TestUpgradeSolo(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	server := func(name, ip string, caps proto.Caps) int64 {
		sid := id(b.must("POST", "/api/servers", map[string]any{"name": name, "address": ip}, 201)["server"].(map[string]any)["id"])
		helloCaps(t, h, sid, ip, caps)
		return sid
	}
	base := proto.Caps{Systemd: true, Nftables: true, RestartPending: true, Solo: true, AnyTLS: true, RestartAll: true}
	old := server("Old", "203.0.113.75", base)
	base.UpgradeSolo = true
	now := server("New", "203.0.113.76", base)

	if m := b.must("POST", fmt.Sprintf("/api/servers/%d/actions", old), map[string]any{"kind": "upgrade_solo"}, 400); !strings.Contains(fmt.Sprint(m["error"]), "1.5.3") {
		t.Errorf("an agent that cannot: %v", m["error"])
	}
	read := b.must("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"}, 201)["token"].(string)
	h.bearer(read).must("POST", fmt.Sprintf("/api/servers/%d/actions", now), map[string]any{"kind": "upgrade_solo"}, 403)
	b.must("POST", fmt.Sprintf("/api/servers/%d/actions", now), map[string]any{"kind": "upgrade_solo"}, 202)
	st := stateOf(t, h, now)
	var n int
	for _, a := range st.Actions {
		if a.Kind == proto.ActionUpgradeSolo {
			n++
		}
	}
	if n != 1 {
		t.Errorf("queued %d", n)
	}
}
