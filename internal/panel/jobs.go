package panel

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// jobs runs the periodic bookkeeping. None of it pauses a user or restarts a core: cycle resets zero
// counters (and the servers serve users again whose data was used up, live), retention only deletes
// old log rows.
func (p *Panel) jobs(ctx context.Context) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	lastHourly, lastMinute, lastLimits, lastRollup := time.Time{}, time.Time{}, time.Time{}, time.Time{}
	lastSources := time.Now() // subscription links are first read again a few minutes after a start
	nextUpdateCheck := time.Now().Add(time.Minute)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		p.markOffline(ctx)
		p.expireBlocks(ctx)
		p.updateResult(ctx) // the updater leaves it once the new panel has stayed up a while
		if time.Since(lastLimits) >= 5*time.Minute {
			lastLimits = time.Now()
			p.limitEvents(ctx)
		}
		if time.Since(lastSources) >= 5*time.Minute {
			lastSources = time.Now()
			go p.sourcesJob(ctx) // slow providers never hold up the rest
		}
		p.notifyTick(ctx)
		p.pingJob(ctx, &lastRollup) // the charts' five-minute steps (pingmon.go)
		if time.Since(lastMinute) >= 59*time.Second {
			lastMinute = time.Now()
			p.sampleUptime(ctx)
			p.resetCycles(ctx) // each minute: a user whose data was used up is back right as it starts over
			p.autoUpdate(ctx)  // a night-time install, when automatic updates are on
			p.relayJob(ctx)    // servers that keep losing the panel (relay.go)
		}
		if time.Now().After(nextUpdateCheck) {
			nextUpdateCheck = time.Now().Add(6 * time.Hour)
			if os.Getenv("MERIDIAN_NO_UPDATE_CHECK") == "" {
				p.checkUpdate(ctx)
			}
		}
		if time.Since(lastHourly) > time.Hour {
			lastHourly = time.Now()
			p.retention(ctx)
			p.pruneMirror(ctx)
		}
	}
}

// expireBlocks lifts the timed IP blocks whose time is up: the servers let the address in again
// within seconds (until then a block stayed in place until something else changed the servers).
func (p *Panel) expireBlocks(ctx context.Context) {
	t := now()
	rows, err := p.db.QueryContext(ctx, `SELECT id, account_id, ip FROM ip_blocks WHERE expires_at > 0 AND expires_at <= ?`, t)
	if err != nil {
		return
	}
	type block struct {
		id, account int64
		ip          string
	}
	var due []block
	for rows.Next() {
		var b block
		if rows.Scan(&b.id, &b.account, &b.ip) == nil {
			due = append(due, b)
		}
	}
	rows.Close()
	accounts := map[int64]bool{}
	for _, b := range due {
		res, err := p.db.Exec1(`DELETE FROM ip_blocks WHERE id = ? AND expires_at > 0 AND expires_at <= ?`, b.id, t)
		if err != nil {
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			accounts[b.account] = true
			p.event(b.account, "info", "ip_unblocked", 0, 0, 0, fmt.Sprintf("%s is no longer blocked: its time was up", b.ip), nil)
		}
	}
	for acct := range accounts {
		p.touchAccount(acct)
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

// nextReset is when a cycle that resets on day of the month next starts over - clamped the way the
// reset itself is (day 31 resets on Feb 28, Apr 30, ...).
func nextReset(day int, t time.Time) time.Time {
	y, m, _ := cycleStart(day, t).Date()
	return monthDay(y, m+1, day, t.Location())
}

// monthDay is day of month m, clamped to the month's length (31 -> 30 Apr, 28/29 Feb).
func monthDay(y int, m time.Month, day int, loc *time.Location) time.Time {
	first := time.Date(y, m, 1, 0, 0, 0, 0, loc)
	last := first.AddDate(0, 1, -1).Day()
	return time.Date(first.Year(), first.Month(), min(day, last), 0, 0, 0, 0, loc)
}

// shifted says whether a cycle boundary only moved: a new time zone puts the same calendar day up to
// 26 hours later, while a new cycle starts at least 28 days after the last one. The counters stay;
// only the start moves to the boundary in the new zone.
func shifted(start, boundary int64) bool { return boundary-start < 27*3600 }

// resetCycles zeroes monthly counters whose cycle rolled over. Users whose data was used up are
// served again (outofdata.go).
func (p *Panel) resetCycles(ctx context.Context) {
	loc, err := time.LoadLocation(p.settings().Timezone)
	if err != nil {
		loc = time.UTC
	}
	t := time.Now().In(loc)
	restarted := map[int64]bool{} // accounts whose users started a new cycle
	back := map[int64]bool{}      // ... among them one whose data was used up
	err = p.db.Write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id, account_id, name, reset_day, reset_every, starts_at, created_at, cycle_start,
			quota, count_mode, cycle_up, cycle_down, paused FROM subs WHERE reset_day > 0 OR reset_every > 0`)
		if err != nil {
			return err
		}
		type item struct {
			id, account, start int64
			name               string
			day                int
			out                bool
		}
		var due, moved []item
		for rows.Next() {
			var x item
			var s Sub
			if rows.Scan(&x.id, &x.account, &x.name, &s.ResetDay, &s.ResetEvery, &s.StartsAt, &s.CreatedAt, &x.start,
				&s.Quota, &s.CountMode, &s.CycleUp, &s.CycleDown, &s.Paused) == nil {
				x.day, x.out = s.ResetDay, !s.Paused && s.outOfData()
				if cs := periodStart(&s, t); cs > 0 && x.start < cs {
					// a monthly boundary that only moved with the time zone; every-N-days cycles keep their instant
					if s.ResetEvery == 0 && shifted(x.start, cs) {
						x.start = cs
						moved = append(moved, x)
						continue
					}
					x.start = cs
					due = append(due, x)
				}
			}
		}
		rows.Close()
		for _, x := range moved {
			if _, err := tx.Exec(`UPDATE subs SET cycle_start = ? WHERE id = ?`, x.start, x.id); err != nil {
				return err
			}
		}
		for _, x := range due {
			if _, err := tx.Exec(`UPDATE subs SET cycle_up = 0, cycle_down = 0, cycle_start = ?, out_at = 0 WHERE id = ?`, x.start, x.id); err != nil {
				return err
			}
			if err := resetNodeUsage(tx, x.id); err != nil {
				return err
			}
			restarted[x.account] = true
			msg := fmt.Sprintf("%s: usage reset - a new cycle started", x.name)
			if x.out {
				back[x.account] = true
				msg += " and the servers serve them again"
			}
			eventTx(tx, x.account, "info", "cycle_reset", 0, x.id, msg)
		}

		rows, err = tx.Query(`SELECT id, account_id, name, bw_reset_day, cycle_start FROM servers WHERE deleted_at = 0`)
		if err != nil {
			return err
		}
		due, moved = due[:0], moved[:0]
		for rows.Next() {
			var x item
			if rows.Scan(&x.id, &x.account, &x.name, &x.day, &x.start) == nil {
				if cs := cycleStart(max(x.day, 1), t).Unix(); x.start < cs {
					if shifted(x.start, cs) {
						x.start = cs
						moved = append(moved, x)
						continue
					}
					x.start = cs
					due = append(due, x)
				}
			}
		}
		rows.Close()
		for _, x := range moved {
			if _, err := tx.Exec(`UPDATE servers SET cycle_start = ? WHERE id = ?`, x.start, x.id); err != nil {
				return err
			}
		}
		for _, x := range due {
			if _, err := tx.Exec(`UPDATE servers SET cycle_rx = 0, cycle_tx = 0, bw_offset = 0, cycle_start = ? WHERE id = ?`,
				x.start, x.id); err != nil {
				return err
			}
		}
		return nil
	})
	logErr("reset cycles", err)
	if err == nil {
		for acct := range restarted { // users whose data was used up, and protocols a limit stopped, serve them again (nodequota.go)
			if back[acct] || len(p.stopModeSubs(ctx, acct)) > 0 {
				p.touchAccount(acct)
			}
		}
	}
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
