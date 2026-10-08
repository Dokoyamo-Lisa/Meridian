package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"meridian/internal/proto"
)

// TestUsageByProtocol: a user's usage is counted per protocol exactly as the servers report it - this
// cycle and all time; resetting the cycle keeps the totals; a removed protocol keeps its traffic; the
// user sees the same on their own page.
func TestUsageByProtocol(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	ctx := context.Background()
	srvJSON := b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.95", "protocols": []string{"vless", "shadowsocks"}}, 201)["server"].(map[string]any)
	sid := id(srvJSON["id"])
	var vless, ss int64
	for _, n := range srvJSON["nodes"].([]any) {
		n := n.(map[string]any)
		if n["kind"] == "vless" {
			vless = id(n["id"])
		} else {
			ss = id(n["id"])
		}
	}
	var list []map[string]any
	_, _, raw := b.do("POST", "/api/users", map[string]any{"name": "dora", "username": "dora", "password": "dora-password-1"})
	if err := json.Unmarshal(raw, &list); err != nil || len(list) != 1 {
		t.Fatalf("user: %s", raw)
	}
	u := id(list[0]["id"])
	srv, _ := h.p.serverByID(ctx, sid)
	report := func(seq int64, traffic ...proto.UserTraffic) {
		if err := h.p.db.Write(ctx, func(tx *sql.Tx) error {
			return h.p.applyBatch(tx, srv, &proto.Batch{Seq: seq, To: now(), Traffic: traffic}, h.p.settings())
		}); err != nil {
			t.Fatal(err)
		}
	}
	report(1, proto.UserTraffic{Sub: u, Node: vless, Up: 100, Down: 1000}, proto.UserTraffic{Sub: u, Node: ss, Up: 10, Down: 20})
	report(2, proto.UserTraffic{Sub: u, Node: vless, Up: 1, Down: 2})
	usageOf := func() []nodeUsage {
		var out []nodeUsage
		raw, _ := json.Marshal(b.must("GET", fmt.Sprintf("/api/users/%d", u), nil, 200)["usage"])
		_ = json.Unmarshal(raw, &out)
		return out
	}
	got := usageOf()
	if len(got) != 2 || got[0].NodeID != vless || got[0].CycleUp != 101 || got[0].CycleDown != 1002 || got[0].TotalDown != 1002 ||
		got[1].NodeID != ss || got[1].CycleDown != 20 || got[0].Server != "Tokyo" || got[0].Protocol != "REALITY" {
		t.Fatalf("usage: %+v", got)
	}
	// a new cycle starts every protocol over, and keeps what came before in the totals
	b.must("POST", fmt.Sprintf("/api/users/%d/reset-usage", u), nil, 200)
	report(3, proto.UserTraffic{Sub: u, Node: ss, Up: 5, Down: 5})
	got = usageOf()
	if got[0].NodeID != ss || got[0].CycleUp != 5 || got[0].TotalUp != 15 || got[1].CycleUp != 0 || got[1].TotalUp != 101 {
		t.Errorf("after the reset: %+v", got)
	}
	// a removed protocol keeps its traffic, marked removed
	b.must("DELETE", fmt.Sprintf("/api/nodes/%d", vless), nil, 200)
	got = usageOf()
	if len(got) != 2 || !got[1].Removed || got[1].TotalUp != 101 {
		t.Errorf("after removing a protocol: %+v", got)
	}
	// the user sees the same on their page
	dora := h.browser()
	dora.login("dora", "dora-password-1")
	me := dora.must("GET", "/api/portal/me", nil, 200)
	protos, _ := me["protocols"].([]any)
	if len(protos) != 2 || protos[0].(map[string]any)["name"] != "SS" || protos[1].(map[string]any)["removed"] != true {
		t.Errorf("own page: %v", protos)
	}
}
