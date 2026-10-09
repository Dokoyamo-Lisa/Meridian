package panel

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestSettle: a view waits for the compile loop's round to end, at most as long as it may, and not
// at all without the loop.
func TestSettle(t *testing.T) {
	h := newHub()
	h.markDirty(1)
	start := time.Now()
	h.settle(context.Background(), time.Second)
	if time.Since(start) > 100*time.Millisecond {
		t.Errorf("without the loop it waited %v", time.Since(start))
	}

	h.startLoop()
	go func() {
		time.Sleep(50 * time.Millisecond)
		h.takeRound()
		time.Sleep(50 * time.Millisecond)
		h.roundDone()
	}()
	start = time.Now()
	h.settle(context.Background(), 2*time.Second)
	if d := time.Since(start); d < 90*time.Millisecond || d > time.Second {
		t.Errorf("waited %v for a round of about 100ms", d)
	}

	h.markDirty(2) // nobody compiles it: the wait ends at its limit
	start = time.Now()
	h.settle(context.Background(), 100*time.Millisecond)
	if d := time.Since(start); d < 90*time.Millisecond || d > time.Second {
		t.Errorf("waited %v, limit 100ms", d)
	}
}

// TestRoutingAnswerIsCurrent: with the compile loop running, the answer to a change already says
// what does not work because of it.
func TestRoutingAnswerIsCurrent(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.p.compileLoop(ctx)
	b := h.browser()
	b.login("owner", "owner-password-1")
	b.must("POST", "/api/servers", map[string]any{"name": "A", "address": "203.0.113.41", "protocols": []string{"vless"}}, 201)
	x := id(b.must("POST", "/api/external-nodes", map[string]any{"text": extSS}, 200)["added"].([]any)[0].(map[string]any)["id"])
	b.must("PATCH", fmt.Sprintf("/api/external-nodes/%d", x), map[string]any{"enabled": false}, 200)
	v := b.must("POST", "/api/routing/rules", map[string]any{"name": "Video", "match": map[string]any{"sites": []string{"youtube"}}, "target": fmt.Sprintf("ext:%d", x)}, 200)
	if p := fmt.Sprint(v["problems"]); !strings.Contains(p, `On A: Rule "Video" does not work: its external node (SS 2022) is turned off`) {
		t.Errorf("problems in the answer: %s", p)
	}
}
