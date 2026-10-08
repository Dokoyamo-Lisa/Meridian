package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"
)

// Usage per protocol: each user's traffic is counted per protocol as the servers report it - this
// cycle (reset with the user's cycle) and all time - next to the user's totals.

type nodeUsage struct {
	NodeID    int64  `json:"node_id" doc:"The protocol (a negative id: one removed while its server still reported its traffic)"`
	ServerID  int64  `json:"server_id"`
	Server    string `json:"server" doc:"Its server's name"`
	Protocol  string `json:"protocol" doc:"The protocol's name, or what it is (REALITY, VLESS gRPC TLS, ...)"`
	Removed   bool   `json:"removed" doc:"The protocol (or its server) was removed since"`
	CycleUp   int64  `json:"cycle_up" doc:"Uploaded this cycle, bytes"`
	CycleDown int64  `json:"cycle_down" doc:"Downloaded this cycle, bytes"`
	TotalUp   int64  `json:"total_up" doc:"Uploaded since the user was created"`
	TotalDown int64  `json:"total_down"`
	LastAt    int64  `json:"last_at" doc:"When it last carried traffic (0: not since this was counted)"`
}

// addNodeUsage counts a report's traffic on the protocol it went through.
func addNodeUsage(tx *sql.Tx, sub, node, server, up, down, ts int64) error {
	_, err := tx.Exec(`INSERT INTO sub_node_usage (sub_id, node_id, server_id, cycle_up, cycle_down, total_up, total_down, last_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(sub_id, node_id) DO UPDATE SET cycle_up = cycle_up + excluded.cycle_up,
		cycle_down = cycle_down + excluded.cycle_down, total_up = total_up + excluded.total_up,
		total_down = total_down + excluded.total_down, server_id = excluded.server_id, last_at = excluded.last_at`,
		sub, node, server, up, down, up, down, ts)
	return err
}

// resetNodeUsage starts a user's cycle over on every protocol too.
func resetNodeUsage(tx interface {
	Exec(string, ...any) (sql.Result, error)
}, sub int64) error {
	_, err := tx.Exec(`UPDATE sub_node_usage SET cycle_up = 0, cycle_down = 0 WHERE sub_id = ?`, sub)
	return err
}

// nodeUsageOf is a user's usage per protocol, the most used this cycle first.
func (p *Panel) nodeUsageOf(ctx context.Context, sub int64) []nodeUsage {
	rows, err := p.db.QueryContext(ctx, `SELECT u.node_id, u.server_id, COALESCE(s.name, ''), COALESCE(s.deleted_at, 1),
		COALESCE(n.kind, ''), COALESCE(n.name, ''), COALESCE(n.settings, '{}'), u.cycle_up, u.cycle_down, u.total_up,
		u.total_down, u.last_at FROM sub_node_usage u LEFT JOIN nodes n ON n.id = u.node_id LEFT JOIN servers s ON s.id = u.server_id
		WHERE u.sub_id = ? ORDER BY u.cycle_up + u.cycle_down DESC, u.total_up + u.total_down DESC`, sub)
	out := []nodeUsage{}
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var x nodeUsage
		var deleted int64
		var kind, name, settings string
		if rows.Scan(&x.NodeID, &x.ServerID, &x.Server, &deleted, &kind, &name, &settings, &x.CycleUp, &x.CycleDown,
			&x.TotalUp, &x.TotalDown, &x.LastAt) != nil {
			continue
		}
		switch {
		case name != "":
			x.Protocol = name
		case kind != "":
			x.Protocol = protocolLabel(kind, json.RawMessage(settings))
		default:
			x.Protocol = "removed protocol"
		}
		if x.Server == "" {
			x.Server = "Removed server"
		}
		x.Removed = kind == "" || deleted > 0
		if x.CycleUp+x.CycleDown+x.TotalUp+x.TotalDown == 0 {
			continue
		}
		out = append(out, x)
	}
	return out
}

// fillNodeUsageCycle works out, once after the upgrade that brought per-protocol usage, how much of
// each protocol's traffic falls into the user's current cycle (from the daily records: whole days,
// in the panel's time zone).
func (p *Panel) fillNodeUsageCycle() {
	var state string
	if p.db.QueryRow(`SELECT value FROM settings WHERE key = 'node_usage_cycle'`).Scan(&state) != nil || state != "pending" {
		return
	}
	err := p.db.Write(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id, cycle_start FROM subs`)
		if err != nil {
			return err
		}
		type sub struct{ id, start int64 }
		var subs []sub
		for rows.Next() {
			var s sub
			if rows.Scan(&s.id, &s.start) == nil {
				subs = append(subs, s)
			}
		}
		rows.Close()
		for _, s := range subs {
			day := p.dayKey(time.Unix(s.start, 0))
			if _, err := tx.Exec(`UPDATE sub_node_usage SET
				cycle_up = COALESCE((SELECT SUM(t.up) FROM traffic_daily t WHERE t.sub_id = sub_node_usage.sub_id
					AND t.node_id = sub_node_usage.node_id AND t.day >= ?), 0),
				cycle_down = COALESCE((SELECT SUM(t.down) FROM traffic_daily t WHERE t.sub_id = sub_node_usage.sub_id
					AND t.node_id = sub_node_usage.node_id AND t.day >= ?), 0)
				WHERE sub_id = ?`, day, day, s.id); err != nil {
				return err
			}
		}
		_, err = tx.Exec(`UPDATE settings SET value = 'done' WHERE key = 'node_usage_cycle'`)
		return err
	})
	if err != nil {
		slog.Warn("usage per protocol", "err", err)
	}
}
