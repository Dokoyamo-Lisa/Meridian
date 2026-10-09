package agent

import (
	"context"
	"sync"
	"time"

	"meridian/internal/agent/sys"
	"meridian/internal/proto"
)

// publicEvery is how often the agent looks at its public addresses between hellos.
const publicEvery = 2 * time.Minute

// publicAddrs follows the host's public addresses between hellos. They are checked every two
// minutes, and a change sends a hello at once: users' links, other servers' links to this one, the
// country rules that let it in and its dynamic DNS name follow within seconds of the check.
type publicAddrs struct {
	mu   sync.Mutex
	sent *sys.PublicAddrs // what the last hello the panel received said
	now  sys.PublicAddrs  // the last check
}

// watchPublic checks the public addresses every two minutes and asks for a report when they moved.
func (a *Agent) watchPublic(ctx context.Context) {
	t := time.NewTicker(publicEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if a.pub.update(sys.CheckPublic(ctx)) {
			select {
			case a.kick <- struct{}{}:
			default:
			}
		}
	}
}

// update takes a check's result and says whether the panel has to hear it now.
func (p *publicAddrs) update(now sys.PublicAddrs) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.now = now
	return p.movedLocked()
}

// moved says whether the last check found addresses the panel has not heard of.
func (p *publicAddrs) moved() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.movedLocked()
}

// movedLocked compares the last check with the last hello: a new address of either kind, or one
// that is gone. An address the check could not find this time (an echo service that did not answer)
// is no change - the panel keeps the one it knows.
func (p *publicAddrs) movedLocked() bool {
	if p.sent == nil { // the first hello carries them anyway
		return false
	}
	s, n := *p.sent, p.now
	return (n.IPv4 != "" && n.IPv4 != s.IPv4) || (n.IPv6 != "" && n.IPv6 != s.IPv6) ||
		(n.IPv4Gone && !s.IPv4Gone) || (n.IPv6Gone && !s.IPv6Gone)
}

// delivered records what a hello the panel received said about the addresses.
func (p *publicAddrs) delivered(h *proto.Hello) {
	if h == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = &sys.PublicAddrs{IPv4: h.IPv4, IPv6: h.IPv6, IPv4Gone: h.IPv4Gone, IPv6Gone: h.IPv6Gone}
}
