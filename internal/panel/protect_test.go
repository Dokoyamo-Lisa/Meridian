package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"meridian/internal/proto"
)

// TestProtectiveSteps: a risk offers the fix its agent proposed - checked first, a malformed one
// dropped -, a person takes it in the browser (never with a token), one at a time; the server's
// result marks it done (and the risk acknowledged) or failed, and a done step can be undone. A server
// whose agent cannot, a server offline or another panel's: refused with the reason. Blocking addresses
// from SSH never blocks the one the supervisor uses.
func TestProtectiveSteps(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	var mine string // the address the supervisor uses, as the panel sees it
	if err := h.p.db.QueryRow(`SELECT last_login_ip FROM accounts WHERE username = 'owner'`).Scan(&mine); err != nil || mine == "" {
		t.Fatalf("the supervisor's address: %q %v", mine, err)
	}
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.10"}, 201)["server"].(map[string]any)["id"])
	helloCaps(t, h, sid, "203.0.113.10", proto.Caps{Systemd: true, Nftables: true, RestartPending: true}) // an agent before 1.3.1
	fp := "SHA256:pp75uX1Fr+1hJEZkR/C4S3R5IjIDL8sPE7YvcPEkQJo"
	healthReport(t, h, sid, &proto.Health{ID: "b1", BaselineAt: now() - 600, ScannedAt: now(), Active: []string{"ssh-failed"},
		Findings: []proto.Finding{
			{Seq: 1, Key: "ssh_keys:root:" + fp, Kind: "ssh_keys", Severity: "high", Title: "A new SSH key can sign in as root", At: now(),
				Fix: &proto.Fix{Kind: proto.FixRemoveKey, Path: "/root/.ssh/authorized_keys", Key: fp}},
			{Seq: 2, Key: "ssh-failed", Kind: "ssh", Severity: "warning", Title: "Many failed SSH sign-ins", At: now(), Lasting: true,
				Fix: &proto.Fix{Kind: proto.FixBlockSSH, Addrs: []string{"198.51.100.66", mine, "nonsense"}}},
			// what a broken or hostile agent might offer: kept as a risk, without the fix
			{Seq: 3, Key: "file:/etc/cron.d/x", Kind: "cron", Severity: "warning", Title: "A scheduled task changed: /etc/cron.d/x", At: now(),
				Fix: &proto.Fix{Kind: proto.FixQuarantine, Path: "/etc/cron.d/../shadow"}},
			{Seq: 4, Key: "account:root2", Kind: "account", Severity: "high", Title: "A new account was added: root2", At: now(),
				Fix: &proto.Fix{Kind: "format_disk"}},
		}})
	r := byKey(listRisks(t, b, ""))
	key, burst := r["ssh_keys:root:"+fp], r["ssh-failed"]
	if key.Fix == nil || key.Fix.Label != "Remove this key" || !strings.Contains(key.Fix.Explain, fp) || !key.Fix.Undo {
		t.Fatalf("the key's fix: %+v", key.Fix)
	}
	if burst.Fix == nil || strings.Join(burst.Fix.Addrs, ",") != "198.51.100.66,"+mine {
		t.Fatalf("the block's fix: %+v", burst.Fix)
	}
	if r["file:/etc/cron.d/x"].Fix != nil || r["account:root2"].Fix != nil {
		t.Error("a malformed fix was kept")
	}

	protect := func(c *client, risk int64, want int) map[string]any {
		return c.must("POST", fmt.Sprintf("/api/risks/%d/protect", risk), nil, want)
	}
	if m := protect(b, key.ID, 409); !strings.Contains(fmt.Sprint(m["error"]), "1.3.1") {
		t.Errorf("an agent that cannot: %v", m["error"])
	}
	helloCaps(t, h, sid, "203.0.113.10", proto.Caps{Systemd: true, Nftables: true, RestartPending: true, Protect: true})
	full := b.must("POST", "/api/tokens", map[string]any{"name": "full", "scope": "full"}, 201)["token"].(string)
	if code, _, _ := h.bearer(full).do("POST", fmt.Sprintf("/api/risks/%d/protect", key.ID), nil); code != 403 {
		t.Errorf("a token took a protective step: %d", code)
	}
	step := protect(b, key.ID, 202)
	if step["state"] != "pending" || step["what"] != "remove the SSH key "+fp {
		t.Fatalf("step: %v", step)
	}
	protect(b, key.ID, 409) // on its way already
	act := protectAction(t, h, sid)
	if act.Fix == nil || act.Fix.Kind != proto.FixRemoveKey || act.Fix.Key != fp || act.ID != id(step["id"]) {
		t.Fatalf("sent: %+v", act)
	}
	// the server did it: the step is done, the risk acknowledged by whoever asked
	actionResult(t, h, sid, proto.ActionResult{ID: actionID(t, h, sid), OK: true, Output: "the key " + fp + " can no longer sign in as root"})
	r = byKey(listRisks(t, b, "?status=all"))
	if s := r["ssh_keys:root:"+fp]; s.Status != "acknowledged" || s.DecidedBy != "owner" || s.Step == nil || s.Step.State != "done" || !s.Step.CanUndo {
		t.Fatalf("after: %+v %+v", s, s.Step)
	}
	protect(b, key.ID, 409) // done already: undo first
	// undo
	b.must("POST", fmt.Sprintf("/api/protections/%d/undo", id(step["id"])), nil, 202)
	if a := protectAction(t, h, sid); !a.Undo || a.Fix != nil || a.ID != id(step["id"]) {
		t.Fatalf("undo sent: %+v", a)
	}
	actionResult(t, h, sid, proto.ActionResult{ID: actionID(t, h, sid), OK: true, Output: "the key can sign in again"})
	var list []protectionView
	_, _, raw := b.do("GET", fmt.Sprintf("/api/protections?server=%d", sid), nil)
	if json.Unmarshal(raw, &list) != nil || len(list) != 1 || list[0].State != "undone" || list[0].UndoneBy != "owner" {
		t.Fatalf("protections: %s", raw)
	}

	// blocking from SSH leaves out the supervisor's own address (the test client's)
	protect(b, burst.ID, 202)
	if a := protectAction(t, h, sid); a.Fix == nil || strings.Join(a.Fix.Addrs, ",") != "198.51.100.66" {
		t.Fatalf("block sent: %+v", a.Fix)
	}
	// a failure is said as the server said it
	actionResult(t, h, sid, proto.ActionResult{ID: actionID(t, h, sid), OK: false, Output: "none of these addresses can be blocked"})
	if s := byKey(listRisks(t, b, "?status=all"))["ssh-failed"].Step; s == nil || s.State != "failed" || s.CanUndo {
		t.Errorf("failed step: %+v", s)
	}
}

// protectAction is the newest protect action queued for a server, as the agent gets it.
func protectAction(t *testing.T, h *harness, sid int64) proto.Protect {
	t.Helper()
	var args string
	if err := h.p.db.QueryRow(`SELECT args FROM actions WHERE server_id = ? AND kind = ? ORDER BY id DESC LIMIT 1`, sid, proto.ActionProtect).
		Scan(&args); err != nil {
		t.Fatal(err)
	}
	var p proto.Protect
	if err := json.Unmarshal([]byte(args), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func actionID(t *testing.T, h *harness, sid int64) int64 {
	t.Helper()
	var id int64
	if err := h.p.db.QueryRow(`SELECT id FROM actions WHERE server_id = ? AND kind = ? ORDER BY id DESC LIMIT 1`, sid, proto.ActionProtect).
		Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// actionResult delivers an action's result from a server, as its agent would.
func actionResult(t *testing.T, h *harness, sid int64, ar proto.ActionResult) {
	t.Helper()
	srv, err := h.p.serverByID(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.p.ingest(context.Background(), srv, &proto.Report{Instance: "i", ActionResults: []proto.ActionResult{ar}}); err != nil {
		t.Fatal(err)
	}
}
