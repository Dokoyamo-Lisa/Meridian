package panel

import (
	"slices"
	"sort"
	"strconv"
	"sync"

	"meridian/internal/proto"
	"meridian/internal/subgen"
)

// Users' speed limits and device limits that turn devices away - what each server is told to
// enforce. A speed limit is enforced on each server by its agent: it knows which addresses each user
// is connected from and limits the traffic to and from them in the kernel. A device limit counts a
// user's addresses on every server together; the first ones to connect keep working and the rest are
// turned away - by an Xray routing rule for that user only (others behind the same address are not
// touched) and at Hysteria2's sign-in - until one of the allowed ones goes offline.

// deviceGone is how long a device counts as still there after its last connection: apps open and
// close connections all the time, and a device that comes back within it keeps its place.
const deviceGone = 2 * 60

// deviceState is the last count: the addresses turned away, per user, and every device of a user
// with an enforced limit as the panel has seen it (when first, when last).
type deviceState struct {
	mu   sync.Mutex
	away map[int64][]string
	seen map[int64]map[string]*deviceSeen
}

type deviceSeen struct{ first, last int64 }

// awayOf are user id's devices turned away now (sorted), or nil.
func (d *deviceState) awayOf(id int64) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.away[id])
}

// any says whether any device is turned away now.
func (d *deviceState) any() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.away) > 0
}

// enforced says whether a user's device limit turns devices away.
func (s *Sub) enforced() bool { return s.DeviceMode == "refuse" && s.IPLimit > 0 && !s.Paused }

// countDevices works out which devices of the account's users over an enforced device limit are
// turned away, from who is connected now (online, per user) and who was within deviceGone. When that
// changes for anyone, the servers get the new lists.
func (p *Panel) countDevices(accountID int64, subs []*Sub, online map[int64][]onlineIP) {
	t := now()
	next := map[int64][]string{}
	mine := map[int64]bool{}
	p.devices.mu.Lock()
	if p.devices.seen == nil {
		p.devices.seen = map[int64]map[string]*deviceSeen{}
	}
	for _, s := range subs {
		mine[s.ID] = true
		if !s.enforced() {
			delete(p.devices.seen, s.ID)
			continue
		}
		seen := p.devices.seen[s.ID]
		if seen == nil {
			seen = map[string]*deviceSeen{}
			p.devices.seen[s.ID] = seen
		}
		for _, o := range online[s.ID] {
			first := o.Since
			if first <= 0 || first > t {
				first = t
			}
			if d := seen[o.IP]; d != nil {
				d.first, d.last = min(d.first, first), t
			} else {
				seen[o.IP] = &deviceSeen{first: first, last: t}
			}
		}
		for ip, d := range seen {
			if t-d.last > deviceGone {
				delete(seen, ip)
			}
		}
		if away := turnedAway(seen, s.IPLimit); len(away) > 0 {
			next[s.ID] = away
		}
	}
	if p.devices.away == nil {
		p.devices.away = map[int64][]string{}
	}
	changed := false
	for id, list := range p.devices.away {
		if mine[id] && !slices.Equal(list, next[id]) {
			changed = true
			delete(p.devices.away, id)
		}
	}
	for id, list := range next {
		if !slices.Equal(p.devices.away[id], list) {
			changed = true
		}
		p.devices.away[id] = list
	}
	p.devices.mu.Unlock()
	if changed {
		p.touchAccount(accountID)
	}
}

// turnedAway are the devices beyond the first limit, in the order they were first seen on any server.
func turnedAway(seen map[string]*deviceSeen, limit int) []string {
	if len(seen) <= limit {
		return nil
	}
	ips := make([]string, 0, len(seen))
	for ip := range seen {
		ips = append(ips, ip)
	}
	sort.Slice(ips, func(i, j int) bool {
		if seen[ips[i]].first != seen[ips[j]].first {
			return seen[ips[i]].first < seen[ips[j]].first
		}
		return ips[i] < ips[j]
	})
	away := ips[limit:]
	sort.Strings(away)
	return away
}

// limitsFor puts the users' limits into server srv's state: speed limits for the users who can use
// it, the devices turned away, and for those an Xray routing rule per user, first in line (returned:
// the compile puts it before every other rule).
func (p *Panel) limitsFor(srv *Server, nodes []*Node, subs []*Sub, st *proto.State) []map[string]any {
	var rules []map[string]any
	for _, s := range subs {
		if s.SpeedLimit > 0 {
			st.Speed = append(st.Speed, proto.SpeedLimit{Sub: s.ID, Mbps: s.SpeedLimit})
		}
		away := p.devices.awayOf(s.ID)
		if len(away) == 0 || !s.enforced() {
			continue
		}
		var emails []string
		hy := false
		for _, n := range nodes {
			if !n.Enabled || len(usersOf([]*Sub{s}, n)) == 0 {
				continue
			}
			switch k, _ := kindOf(n.Kind); {
			case n.Kind == subgen.KindHysteria2:
				hy = true
			case k.Engine == "xray":
				emails = append(emails, proto.Email(s.ID, n.ID))
			}
		}
		if hy {
			st.Refuse = append(st.Refuse, proto.Refusal{Sub: s.ID, IPs: away})
		}
		if len(emails) > 0 {
			rules = append(rules, map[string]any{"ruleTag": "dev-s" + strconv.FormatInt(s.ID, 10), "user": emails,
				"source": away, "outboundTag": "block"})
		}
	}
	return rules
}

// capHysteria has a limited user's Hysteria2 apps declare a bandwidth a little below the user's
// speed limit: the server then sends no faster than that by itself, and the agent's limit, which
// drops what goes over, seldom has to. Without a declared rate (BBR) a Hysteria2 transfer stalls
// completely under such a limit - tried in the lab, at 0.1 and at 40 ms - so the agent lets in only
// sign-ins that declare one within the limit (agent hy/limits.go). Users without a limit keep what
// the protocol says.
func capHysteria(eps []subgen.Endpoint, mbps int) {
	if mbps <= 0 {
		return
	}
	limit := max(1, mbps*95/100)
	for i := range eps {
		if eps[i].Kind != subgen.KindHysteria2 {
			continue
		}
		if eps[i].DownMbps == 0 || eps[i].DownMbps > limit {
			eps[i].DownMbps = limit
		}
		if eps[i].UpMbps == 0 || eps[i].UpMbps > limit {
			eps[i].UpMbps = limit
		}
	}
}
