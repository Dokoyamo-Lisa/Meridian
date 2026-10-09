package hy

import (
	"net/netip"
	"strconv"
	"time"

	"meridian/internal/proto"
)

// Devices turned away over a user's device limit (State.Refuse): their sign-ins are refused. A
// refused device that keeps trying still counts as present for a while (tryWindow), so the panel's
// count stays put instead of letting it in and out again; and every device keeps the time it was
// first seen, so a device that reconnects never loses its place to a newer one.

const tryWindow = 2 * time.Minute

type tryHit struct {
	node, sub int64
	id        string
	last      int64
}

// SetRefused takes the devices turned away now. It restarts nothing.
func (e *Engine) SetRefused(list []proto.Refusal) {
	m := map[int64]map[string]bool{}
	for _, r := range list {
		if r.Sub <= 0 {
			continue
		}
		for _, s := range r.IPs {
			if a, err := netip.ParseAddr(s); err == nil && a.Zone() == "" {
				if m[r.Sub] == nil {
					m[r.Sub] = map[string]bool{}
				}
				m[r.Sub][a.Unmap().String()] = true
			}
		}
	}
	e.mu.Lock()
	e.refused = m
	e.mu.Unlock()
}

// refuse says whether a sign-in of user id from ip is turned away, and notes the attempt (under e.mu).
func (e *Engine) refuse(node int64, id, ip string, now int64) bool {
	sub, _, ok := proto.ParseEmail(id)
	if !ok || !e.refused[sub][ip] {
		return false
	}
	if e.tries == nil {
		e.tries = map[string]tryHit{}
	}
	e.tries[id+"|"+ip] = tryHit{node: node, sub: sub, id: id, last: now}
	e.seen(sub, ip, now)
	return true
}

// seen keeps when a user's device was first seen (under e.mu).
func (e *Engine) seen(sub int64, ip string, now int64) int64 {
	if e.first == nil {
		e.first = map[string]int64{}
	}
	k := firstKey(sub, ip)
	if t, ok := e.first[k]; ok {
		return t
	}
	e.first[k] = now
	return now
}

func firstKey(sub int64, ip string) string { return strconv.FormatInt(sub, 10) + "|" + ip }

// refusedOnline are the turned-away devices still trying, as online entries (under e.mu), and drops
// what went quiet - attempts, and first-seen times of devices that are gone (keep says which are
// still connected).
func (e *Engine) refusedOnline(now int64, keep map[string]bool) []proto.OnlineUser {
	var out []proto.OnlineUser
	for k, t := range e.tries {
		if now-t.last > int64(tryWindow/time.Second) {
			delete(e.tries, k)
			continue
		}
		ip := k[len(t.id)+1:]
		_, node, _ := proto.ParseEmail(t.id)
		keep[firstKey(t.sub, ip)] = true
		out = append(out, proto.OnlineUser{Sub: t.sub, Node: node, IPs: []proto.OnlineIP{{IP: ip, Since: e.first[firstKey(t.sub, ip)], Last: t.last}}})
	}
	for k := range e.first {
		if !keep[k] {
			delete(e.first, k)
		}
	}
	return out
}
