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

// Users' speed limits (State.Speed). The agent limits a user's traffic on every protocol by dropping
// what goes over the limit (nft), which TCP and WireGuard take in their stride - but Hysteria2 apps
// that declare no bandwidth (BBR) stall completely under it, and so does one declaring more than the
// limit (Brutal pushes on). A limited user's links therefore declare a bandwidth below the limit
// (the panel's capHysteria), and here a sign-in is let in only when it does: Hysteria2 tells the
// sign-in hook the rate it is to send at (tx, bytes per second; 0 = no declared rate). A lower
// limit makes the user's sessions sign in again, so the new one holds at once.

// maxTx is the most a user with a limit of mbps may be sent, in bytes per second - the limit with a
// little room, as apps round.
func maxTx(mbps int) uint64 { return uint64(mbps) * 125_000 * 105 / 100 }

// SetSpeed takes the users' speed limits now. Users whose limit is new or lower sign in again.
func (e *Engine) SetSpeed(list []proto.SpeedLimit) {
	m := map[int64]int{}
	for _, l := range list {
		if l.Sub > 0 && l.Mbps > 0 {
			m[l.Sub] = l.Mbps
		}
	}
	e.mu.Lock()
	again := map[int64]map[string]bool{} // node -> ids to sign in again
	for sub, mbps := range m {
		if old, had := e.speed[sub]; had && old <= mbps {
			continue
		}
		for node, users := range e.users {
			for _, id := range users {
				if s, _, ok := proto.ParseEmail(id); ok && s == sub {
					if again[node] == nil {
						again[node] = map[string]bool{}
					}
					again[node][id] = true
				}
			}
		}
	}
	e.speed = m
	e.mu.Unlock()
	for node, ids := range again {
		list := make([]string, 0, len(ids))
		for id := range ids {
			list = append(list, id)
		}
		e.kickOnline(node, list)
	}
}

// overLimit says whether a sign-in of user id that asks to be sent tx bytes per second would break
// their speed limit (under e.mu).
func (e *Engine) overLimit(id string, tx uint64) bool {
	sub, _, ok := proto.ParseEmail(id)
	if !ok {
		return false
	}
	mbps, limited := e.speed[sub]
	return limited && (tx == 0 || tx > maxTx(mbps))
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
