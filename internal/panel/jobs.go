package panel

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// jobs runs the periodic bookkeeping. None of it pauses a subscription or touches a running core:
// cycle resets only zero counters, retention only deletes old log rows.
func (p *Panel) jobs(ctx context.Context) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	lastHourly, lastMinute := time.Time{}, time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		p.markOffline(ctx)
		if time.Since(lastMinute) >= 59*time.Second {
			lastMinute = time.Now()
			p.sampleUptime(ctx)
		}
		if time.Since(lastHourly) > time.Hour {
			lastHourly = time.Now()
			p.resetCycles(ctx)
			p.retention(ctx)
		}
	}
}

// markOffline flips servers that stopped reporting. Three missed reports plus slack.
func (p *Panel) markOffline(ctx context.Context) {
	set := p.settings()
	limit := now() - int64(set.ReportInterval*3+20)
	rows, err := p.db.QueryContext(ctx, `SELECT id, account_id, name FROM servers WHERE online = 1 AND last_seen_at < ? AND deleted_at = 0`, limit)
	if err != nil {
		slog.Error("offline check", "err", err)
		return
	}
	type srv struct {
		id, account int64
		name        string
	}
	var gone []srv
	for rows.Next() {
		var s srv
		if rows.Scan(&s.id, &s.account, &s.name) == nil {
			gone = append(gone, s)
		}
	}
	rows.Close()
	for _, s := range gone {
		_, err := p.db.Exec1(`UPDATE servers SET online = 0, status_changed_at = ? WHERE id = ? AND online = 1`, now(), s.id)
		logErr("mark offline", err)
		p.event(s.account, "crit", "server_offline", s.id, 0, 0,
			fmt.Sprintf("%s stopped reporting - the server or its network may be down", s.name), nil)
	}
}

// cycleStart returns the start of the current monthly cycle that resets on day (1-31).
func cycleStart(day int, t time.Time) time.Time {
	y, m, _ := t.Date()
	start := monthDay(y, m, day, t.Location())
	if start.After(t) {
		start = monthDay(y, m-1, day, t.Location())
	}
	return start
}

// monthDay is day of month m, clamped to the month's length (31 -> 30 Apr, 28/29 Feb).
func monthDay(y int, m time.Month, day int, loc *time.Location) time.Time {
	first := time.Date(y, m, 1, 0, 0, 0, 0, loc)
	last := first.AddDate(0, 1, -1).Day()
	return time.Date(first.Year(), first.Month(), min(day, last), 0, 0, 0, 0, loc)
}

// resetCycles zeroes monthly counters whose cycle rolled over.
func (p *Panel) resetCycles(ctx context.Context) {
	loc, err := time.LoadLocation(p.settings().Timezone)
	if err != nil {
		loc = time.UTC
	}
	t := time.Now().In(loc)
	err = p.db.Write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id, account_id, name, reset_day, cycle_start FROM subs WHERE reset_day > 0`)
		if err != nil {
			return err
		}
		type item struct {
			id, account, start int64
			name               string
			day                int
		}
		var due []item
		for rows.Next() {
			var x item
			if rows.Scan(&x.id, &x.account, &x.name, &x.day, &x.start) == nil {
				if cs := cycleStart(x.day, t).Unix(); x.start < cs {
					x.start = cs
					due = append(due, x)
				}
			}
		}
		rows.Close()
		for _, x := range due {
			if _, err := tx.Exec(`UPDATE subs SET cycle_up = 0, cycle_down = 0, cycle_start = ? WHERE id = ?`, x.start, x.id); err != nil {
				return err
			}
			eventTx(tx, x.account, "info", "cycle_reset", 0, x.id, fmt.Sprintf("%s: monthly usage reset", x.name))
		}

		rows, err = tx.Query(`SELECT id, account_id, name, bw_reset_day, cycle_start FROM servers WHERE deleted_at = 0`)
		if err != nil {
			return err
		}
		due = due[:0]
		for rows.Next() {
			var x item
			if rows.Scan(&x.id, &x.account, &x.name, &x.day, &x.start) == nil {
				if cs := cycleStart(max(x.day, 1), t).Unix(); x.start < cs {
					x.start = cs
					due = append(due, x)
				}
			}
		}
		rows.Close()
		for _, x := range due {
			if _, err := tx.Exec(`UPDATE servers SET cycle_rx = 0, cycle_tx = 0, bw_offset = 0, cycle_start = ? WHERE id = ?`,
				x.start, x.id); err != nil {
				return err
			}
		}
		return nil
	})
	logErr("reset cycles", err)
}

// retention deletes log rows older than the configured window.
func (p *Panel) retention(ctx context.Context) {
	set := p.settings()
	cutDay := p.dayKey(time.Now().AddDate(0, 0, -set.LogRetention))
	err := p.db.Write(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{`DELETE FROM ip_log WHERE day < ?`, `DELETE FROM dest_log WHERE day < ?`} {
			if _, err := tx.Exec(q, cutDay); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`DELETE FROM server_metrics WHERE ts < ?`, now()-49*3600); err != nil {
			return err
		}
		if err := p.cleanupUserSessions(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM sessions WHERE expires_at < ?`, now()); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM events WHERE ts < ?`, now()-180*86400); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM actions WHERE status != 'pending' AND done_at < ?`, now()-30*86400)
		return err
	})
	logErr("retention", err)
}
