package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"meridian/internal/proto"
)

// Agent upgrades that lost their result. An upgrade restarts the agent right after it reports the
// result, so an agent before 1.0 could lose it (1.0 keeps unreported results on disk); the action then
// stayed pending for good, and every later upgrade skipped the server. The panel now settles such an
// action when the agent says it runs the version the upgrade installed, and a new upgrade replaces
// one that waits with other binaries.

// upgradeArgs are an upgrade_agent action's arguments: the binaries' checksums (the agent checks the
// download against them) and the version they are, for the panel.
func upgradeArgs(sums map[string]string) string {
	b, _ := json.Marshal(map[string]any{"sha256": sums, "version": Version})
	return string(b)
}

type pendingUpgrade struct {
	id      int64
	sums    map[string]string
	version string
	created int64
}

func pendingUpgrades(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, serverID int64) []pendingUpgrade {
	rows, err := q.Query(`SELECT id, args, created_at FROM actions WHERE server_id = ? AND kind = ? AND status = 'pending' ORDER BY id`,
		serverID, proto.ActionUpgradeAgent)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []pendingUpgrade
	for rows.Next() {
		var u pendingUpgrade
		var args string
		if rows.Scan(&u.id, &args, &u.created) != nil {
			continue
		}
		var a struct {
			SHA256  map[string]string `json:"sha256"`
			Version string            `json:"version"`
		}
		_ = json.Unmarshal([]byte(args), &a)
		u.sums, u.version = a.SHA256, a.Version
		out = append(out, u)
	}
	return out
}

// settleAgentUpgrades marks a server's pending agent upgrades done when its agent's hello says it runs
// the version they install - or the panel's own: whatever became of the upgrade's report, there is
// nothing left to do. It says whether any was settled.
func settleAgentUpgrades(tx *sql.Tx, srv *Server, h *proto.Hello) bool {
	if h.AgentVersion == "" {
		return false
	}
	settled := false
	for _, u := range pendingUpgrades(tx, srv.ID) {
		if h.AgentVersion != u.version && h.AgentVersion != Version {
			continue
		}
		res, err := tx.Exec(`UPDATE actions SET status = 'done', output = ?, done_at = ? WHERE id = ? AND status = 'pending'`,
			"The agent runs "+h.AgentVersion+" now.", now(), u.id)
		if err != nil {
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			settled = true
			eventTx(tx, srv.AccountID, "info", "action_done", srv.ID, 0, fmt.Sprintf("%s: the agent runs %s now", srv.Name, h.AgentVersion))
		}
	}
	return settled
}

// queueAgentUpgrade asks a server's agent to upgrade to the panel's binaries, replacing upgrades that
// still wait for it. force is a request for this server alone (its page): it always goes out anew;
// otherwise (every agent at once) an upgrade to the same binaries that waits for an offline server,
// or went out to an online one in the last 15 minutes, stays - and why is returned.
func (p *Panel) queueAgentUpgrade(ctx context.Context, s *Server, sums map[string]string, by int64, force bool) (int64, string, error) {
	var aid int64
	var skip string
	err := p.db.Write(ctx, func(tx *sql.Tx) error {
		var stale []int64
		for _, u := range pendingUpgrades(tx, s.ID) {
			if !force && skip == "" && maps.Equal(u.sums, sums) {
				switch {
				case !s.Online:
					skip = "offline - the upgrade already waiting for it starts when it connects"
				case now()-u.created < 15*60:
					skip = "its upgrade went out " + humanDuration(now()-u.created) + " ago and is under way"
				}
				if skip != "" {
					continue
				}
			}
			stale = append(stale, u.id) // other binaries, or no sign of life from it: replaced
		}
		for _, id := range stale {
			if _, err := tx.Exec(`UPDATE actions SET status = 'failed', output = 'Replaced by a newer upgrade request.', done_at = ?
				WHERE id = ? AND status = 'pending'`, now(), id); err != nil {
				return err
			}
		}
		if skip != "" {
			return nil
		}
		res, err := tx.Exec(`INSERT INTO actions (server_id, kind, args, created_at, created_by) VALUES (?, ?, ?, ?, ?)`,
			s.ID, proto.ActionUpgradeAgent, upgradeArgs(sums), now(), by)
		if err != nil {
			return err
		}
		aid, _ = res.LastInsertId()
		return nil
	})
	if err == nil {
		p.touchServers(s.ID)
	}
	return aid, skip, err
}

// agentSkipped is a server an agent upgrade left out, and why.
type agentSkipped struct {
	Name string `json:"name" doc:"The server's name"`
	Why  string `json:"why" doc:"Why its agent was left out, in plain words"`
}

// skippedText names the servers left out, for the timeline.
func skippedText(list []agentSkipped) string {
	parts := make([]string, len(list))
	for i, s := range list {
		parts[i] = s.Name + " (" + s.Why + ")"
	}
	return strings.Join(parts, ", ")
}
