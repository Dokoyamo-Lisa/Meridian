package panel

import (
	"context"
	"time"
)

// Waiting for the compile loop: what a server's last compile found (its traffic rules' notes) is
// only current once the loop has compiled the servers a change marked. A view that reports those
// findings right after a change waits for that, briefly.

func (h *hub) startLoop() {
	h.mu.Lock()
	h.looping = true
	h.mu.Unlock()
}

// takeRound takes the dirty servers for one round of the compile loop.
func (h *hub) takeRound() []int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make([]int64, 0, len(h.dirty))
	for id := range h.dirty {
		ids = append(ids, id)
	}
	h.dirty = map[int64]bool{}
	h.busy = len(ids) > 0
	return ids
}

func (h *hub) roundDone() {
	h.mu.Lock()
	h.busy = false
	h.mu.Unlock()
}

// settle waits, at most max, until the compile loop has compiled every server marked so far. Without
// the loop (tests) it returns at once.
func (h *hub) settle(ctx context.Context, max time.Duration) {
	deadline := time.Now().Add(max)
	for {
		h.mu.Lock()
		done := !h.looping || len(h.dirty) == 0 && !h.busy
		h.mu.Unlock()
		if done || !time.Now().Before(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
}
