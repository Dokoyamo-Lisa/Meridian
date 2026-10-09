package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
)

// What traffic rules and load balancers keep when a server, a protocol or an external node goes:
//
//   - a rule's scope drops it; a rule left with none keeps what it had and applies nowhere - never
//     everywhere, which an empty scope would mean;
//   - a load balancer loses the member;
//   - a rule that sends traffic there keeps it as its exit and blocks that traffic until it gets
//     another one, as a protocol's proxy pass does: users who chose to appear there must never leave
//     from somewhere else without anyone choosing that. Protocol ids are never handed out again, and
//     neither are external node ids (nextExtID), so such an exit never comes to mean a new one.

// pruneRoutes takes removed servers, protocols and other members (ext:<id> for external nodes,
// src:<id> for subscriptions) out of an account's rules and load balancers, in the removal's own
// transaction.
func pruneRoutes(tx *sql.Tx, acct int64, servers, nodes []int64, exits ...string) error {
	rows, err := tx.Query(`SELECT id, servers, nodes FROM routes WHERE account_id = ?`, acct)
	if err != nil {
		return err
	}
	type scope struct {
		id             int64
		servers, nodes []int64
	}
	var rules []scope
	for rows.Next() {
		var sc scope
		var sv, nv string
		if err := rows.Scan(&sc.id, &sv, &nv); err != nil {
			rows.Close()
			return err
		}
		_ = json.Unmarshal([]byte(sv), &sc.servers)
		_ = json.Unmarshal([]byte(nv), &sc.nodes)
		rules = append(rules, sc)
	}
	rows.Close()
	drop := func(list, gone []int64) []int64 {
		left := slices.DeleteFunc(slices.Clone(list), func(id int64) bool { return slices.Contains(gone, id) })
		if len(left) == 0 {
			return list // applies nowhere now, never everywhere
		}
		return left
	}
	for _, sc := range rules {
		s, n := drop(sc.servers, servers), drop(sc.nodes, nodes)
		if len(s) == len(sc.servers) && len(n) == len(sc.nodes) {
			continue
		}
		sv, _ := json.Marshal(s)
		nv, _ := json.Marshal(n)
		if _, err := tx.Exec(`UPDATE routes SET servers = ?, nodes = ? WHERE id = ?`, string(sv), string(nv), sc.id); err != nil {
			return err
		}
	}

	gone := slices.Clone(exits)
	for _, id := range nodes {
		gone = append(gone, fmt.Sprintf("node:%d", id))
	}
	if len(gone) == 0 {
		return nil
	}
	rows, err = tx.Query(`SELECT id, members FROM balancers WHERE account_id = ?`, acct)
	if err != nil {
		return err
	}
	type members struct {
		id   int64
		list []string
	}
	var bals []members
	for rows.Next() {
		var m members
		var raw string
		if err := rows.Scan(&m.id, &raw); err != nil {
			rows.Close()
			return err
		}
		_ = json.Unmarshal([]byte(raw), &m.list)
		bals = append(bals, m)
	}
	rows.Close()
	for _, b := range bals {
		left := slices.DeleteFunc(slices.Clone(b.list), func(m string) bool { return slices.Contains(gone, m) })
		if len(left) == len(b.list) {
			continue
		}
		if left == nil {
			left = []string{}
		}
		raw, _ := json.Marshal(left)
		if _, err := tx.Exec(`UPDATE balancers SET members = ?, updated_at = ? WHERE id = ?`, string(raw), now(), b.id); err != nil {
			return err
		}
	}
	return nil
}

// rulesSendingTo names the rules that send their traffic to exit target (node:<id> or ext:<id>):
// turning the exit off or removing it blocks that traffic.
func (p *Panel) rulesSendingTo(ctx context.Context, acct int64, target string) []string {
	return p.rulesByExit(ctx, acct)[target]
}

// rulesByExit names, per exit the account's rules send traffic to, those rules (the ones turned on).
func (p *Panel) rulesByExit(ctx context.Context, acct int64) map[string][]string {
	out := map[string][]string{}
	rules, err := p.routesOf(ctx, acct)
	if err != nil {
		return out
	}
	for _, rt := range rules {
		if rt.Enabled {
			out[rt.Target] = append(out[rt.Target], rt.title())
		}
	}
	return out
}
