package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Limits per protocol: besides their quota, a user (or a plan) can have a limit on single protocols -
// "at most 20 GB through Tokyo · REALITY each cycle" - counted the way the user's quota is counted
// (count mode) and starting over with the user's cycle. Reaching one raises an alert, as the quota
// does. Only when the operator chose node_quota_mode "stop" does the protocol stop serving that user
// until the cycle starts over; their other protocols keep working.

// NodeQuotas are bytes per cycle per protocol id.
type NodeQuotas map[int64]int64

func (q NodeQuotas) String() string {
	if len(q) == 0 {
		return ""
	}
	b, _ := json.Marshal(q)
	return string(b)
}

func parseNodeQuotas(s string) NodeQuotas {
	if s == "" {
		return nil
	}
	var q NodeQuotas
	if json.Unmarshal([]byte(s), &q) != nil {
		return nil
	}
	return q
}

const maxNodeQuotas = 100

// checkNodeQuotaMode accepts ” / alert (an alert only) and stop.
func checkNodeQuotaMode(m string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "", "alert":
		return "", nil
	case "stop":
		return "stop", nil
	}
	return "", errStatus(http.StatusBadRequest, "node_quota_mode is alert (an alert only) or stop (the protocol stops serving the user until the cycle starts over)")
}

// nodeQuotasFrom reads limits per protocol from a request: protocol id -> bytes; 0 takes a limit
// away. A protocol of another account, or one that does not exist, is refused - except one that
// had a limit already (it was removed since: the limit just goes).
func (p *Panel) nodeQuotasFrom(ctx context.Context, acct int64, in map[string]int64, cur NodeQuotas) (NodeQuotas, error) {
	if len(in) > maxNodeQuotas {
		return nil, errStatus(http.StatusBadRequest, fmt.Sprintf("at most %d limits per protocol", maxNodeQuotas))
	}
	out := NodeQuotas{}
	for k, v := range in {
		id, err := strconv.ParseInt(k, 10, 64)
		if err != nil || id <= 0 {
			return nil, errStatus(http.StatusBadRequest, fmt.Sprintf("%q is not a protocol id", truncate(k, 20)))
		}
		if v < 0 || v > 1<<60 {
			return nil, errStatus(http.StatusBadRequest, "a limit per protocol is 0 (none) or more bytes")
		}
		if v == 0 {
			continue
		}
		n, err := p.nodeByID(ctx, id)
		var s *Server
		if err == nil {
			s, err = p.serverByID(ctx, n.ServerID)
		}
		if err != nil || s.AccountID != acct || s.DeletedAt > 0 {
			if _, had := cur[id]; had {
				continue
			}
			return nil, errStatus(http.StatusBadRequest, fmt.Sprintf("there is no protocol %d", id))
		}
		out[id] = v
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// nodeLimit is how a user stands on one protocol with a limit, this cycle.
type nodeLimit struct {
	NodeID   int64  `json:"node_id"`
	ServerID int64  `json:"server_id"`
	Server   string `json:"server"`
	Protocol string `json:"protocol"`
	Quota    int64  `json:"quota" doc:"Bytes per cycle"`
	Used     int64  `json:"used" doc:"What counts this cycle (the user's count mode)"`
	Left     int64  `json:"left"`
	Stopped  bool   `json:"stopped" doc:"Used up while node_quota_mode is stop: the protocol does not serve the user until the cycle starts over"`
	Removed  bool   `json:"removed" doc:"The protocol was removed since"`
}

// nodeLimitsOf lists a user's limits per protocol with what they used of each, the closest to its
// end first.
func (p *Panel) nodeLimitsOf(ctx context.Context, s *Sub) []nodeLimit {
	out := []nodeLimit{}
	if len(s.NodeQuotas) == 0 {
		return out
	}
	used := map[int64]nodeUsage{}
	for _, u := range p.nodeUsageOf(ctx, s.ID) {
		used[u.NodeID] = u
	}
	for id, q := range s.NodeQuotas {
		l := nodeLimit{NodeID: id, Quota: q}
		if u, ok := used[id]; ok {
			l.ServerID, l.Server, l.Protocol, l.Removed = u.ServerID, u.Server, u.Protocol, u.Removed
			l.Used = countedUsage(s.CountMode, u.CycleUp, u.CycleDown)
		} else if n, err := p.nodeByID(ctx, id); err == nil {
			l.ServerID, l.Protocol = n.ServerID, nz(n.Name, protocolLabel(n.Kind, n.Settings))
			if srv, err := p.serverByID(ctx, n.ServerID); err == nil && srv.DeletedAt == 0 {
				l.Server = srv.Name
			} else {
				l.Server, l.Removed = "Removed server", true
			}
		} else {
			l.Server, l.Protocol, l.Removed = "Removed server", "removed protocol", true
		}
		l.Left = max(0, q-l.Used)
		l.Stopped = s.NodeQuotaMode == "stop" && l.Used >= q
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		fa, fb := float64(a.Left)/float64(a.Quota), float64(b.Left)/float64(b.Quota)
		if fa != fb {
			return fa < fb
		}
		return a.NodeID < b.NodeID
	})
	return out
}

// stoppedOn says, per protocol of a server, the users it does not serve now: they used up its limit
// and the operator chose to stop it then (node_quota_mode stop).
func (p *Panel) stoppedOn(ctx context.Context, nodes []*Node, subs []*Sub) map[int64]map[int64]bool {
	out := map[int64]map[int64]bool{}
	var ids []int64
	byID := map[int64]*Sub{}
	for _, s := range subs {
		if s.NodeQuotaMode != "stop" || len(s.NodeQuotas) == 0 {
			continue
		}
		for _, n := range nodes {
			if _, ok := s.NodeQuotas[n.ID]; ok {
				ids = append(ids, s.ID)
				byID[s.ID] = s
				break
			}
		}
	}
	if len(ids) == 0 {
		return out
	}
	for _, id := range ids {
		s := byID[id]
		rows, err := p.db.QueryContext(ctx, `SELECT node_id, cycle_up, cycle_down FROM sub_node_usage WHERE sub_id = ?`, id)
		if err != nil {
			continue
		}
		for rows.Next() {
			var node, up, down int64
			if rows.Scan(&node, &up, &down) != nil {
				continue
			}
			if q, ok := s.NodeQuotas[node]; ok && countedUsage(s.CountMode, up, down) >= q {
				if out[node] == nil {
					out[node] = map[int64]bool{}
				}
				out[node][id] = true
			}
		}
		rows.Close()
	}
	return out
}

// servingOf is usersOf less the users a protocol's limit stops now.
func servingOf(subs []*Sub, n *Node, stopped map[int64]map[int64]bool) []*Sub {
	us := usersOf(subs, n)
	if len(stopped[n.ID]) == 0 {
		return us
	}
	return slices.DeleteFunc(us, func(s *Sub) bool { return stopped[n.ID][s.ID] })
}

// stopModeSubs are an account's users with limits per protocol that stop the protocol when used up.
func (p *Panel) stopModeSubs(ctx context.Context, acct int64) map[int64]bool {
	out := map[int64]bool{}
	rows, err := p.db.QueryContext(ctx, `SELECT id FROM subs WHERE account_id = ? AND node_quota_mode = 'stop' AND node_quotas != ''`, acct)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out[id] = true
		}
	}
	return out
}

// nodeQuotaCrossed says whether a report's traffic took a user past the limit of a protocol that
// stops then: its server must stop serving them (checked after the traffic is counted).
func (p *Panel) nodeQuotaCrossed(ctx context.Context, sub, node, up, down int64) bool {
	s, err := p.subByID(ctx, sub)
	if err != nil || s.NodeQuotaMode != "stop" {
		return false
	}
	q, ok := s.NodeQuotas[node]
	if !ok {
		return false
	}
	var cu, cd int64
	if p.db.QueryRowContext(ctx, `SELECT cycle_up, cycle_down FROM sub_node_usage WHERE sub_id = ? AND node_id = ?`, sub, node).Scan(&cu, &cd) != nil {
		return false
	}
	after := countedUsage(s.CountMode, cu, cd)
	before := countedUsage(s.CountMode, cu-up, cd-down)
	return before < q && after >= q
}

// nodeQuotaEvents tells, once a cycle, that a user used up a protocol's limit.
func (p *Panel) nodeQuotaEvents(ctx context.Context, subs []*Sub) {
	for _, s := range subs {
		if len(s.NodeQuotas) == 0 {
			continue
		}
		var told []int64
		if rows, err := p.db.QueryContext(ctx, `SELECT data FROM events WHERE kind = 'node_quota_reached' AND sub_id = ? AND ts >= ?`, s.ID, s.CycleStart); err == nil {
			for rows.Next() {
				var raw string
				var d struct {
					Node int64 `json:"node"`
				}
				if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &d) == nil {
					told = append(told, d.Node)
				}
			}
			rows.Close()
		}
		for _, l := range p.nodeLimitsOf(ctx, s) {
			if l.Used < l.Quota || slices.Contains(told, l.NodeID) {
				continue
			}
			what := "nothing is stopped: take the protocol from them or raise the limit if needed"
			if l.Stopped {
				what = "it no longer serves them until their cycle starts over (their other protocols keep working)"
			}
			p.event(s.AccountID, "warn", "node_quota_reached", l.ServerID, s.ID, 0, fmt.Sprintf("%s used all of their %s on %s · %s this cycle - %s",
				s.Name, fmtBytes(l.Quota), l.Server, l.Protocol, what), map[string]int64{"node": l.NodeID})
		}
	}
}
