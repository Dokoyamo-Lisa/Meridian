package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"time"

	"meridian/internal/proto"
)

// The month before the demo began. What agents send - traffic, devices, destinations, network
// counters, ping rounds - goes through the agents' protocol like everything else (ping rounds and
// traffic may arrive late; the panel files them under their own time). What the panel measures
// itself every minute - the hosts' numbers and whether they were up - is written straight into its
// database, as it would have been then.

// outages: a few short ones, so the status page has something to show.
var outages = []struct {
	server  string
	daysAgo float64
	hour    float64
	minutes float64
	reboot  bool
}{
	{"São Paulo", 6, 3.2, 35, false},
	{"London", 13, 14.3, 12, false},
	{"Sydney", 22, 19, 8, true},
}

func outageAt(s *server, t int64) bool {
	for _, o := range outages {
		if o.server != s.m.Name {
			continue
		}
		start := (setupTime/86400-int64(o.daysAgo))*86400 + int64(o.hour*3600)
		if t >= start && t < start+int64(o.minutes*60) {
			return true
		}
	}
	return false
}

// expected is what moves through a server at t and how many devices are online, on average.
func expected(w *world, s *server, t int64) (float64, int) {
	through, devices := 0.0, 0.0
	for _, u := range s.people {
		through += w.rate(u, s, t)
		devices += w.presence(u, s, t) * (1 + float64(u.p.Devices-1)/2)
	}
	return through, int(math.Round(devices))
}

func backfill(ctx context.Context, db *sql.DB, w *world, agents []*demoAgent) error {
	end := time.Now().Unix()/60*60 - 60
	start := end - 30*86400

	// the hosts' numbers: every five minutes for 30 days, every minute for the last two
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cols := `(server_id, ts, cpu, mem_used, swap_used, disk_used, load1, load5, load15, rx_rate, tx_rate, disk_read, disk_write, tcp, udp, online, temp)
		VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	five, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO server_metrics_5m `+cols)
	if err != nil {
		return err
	}
	minute, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO server_metrics `+cols)
	if err != nil {
		return err
	}
	uptime, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO server_uptime (server_id, bucket, samples, up) VALUES (?, ?, 10, ?)`)
	if err != nil {
		return err
	}
	put := func(st *sql.Stmt, s *server, t int64) error {
		if outageAt(s, t) {
			return nil
		}
		through, devices := expected(w, s, t)
		m := measure(s, t, through, devices)
		_, err := st.ExecContext(ctx, s.id, t, round(m.cpu, 1), int64(m.mem), int64(m.disk), round(m.load1, 2), round(m.load5, 2),
			round(m.load15, 2), int64(m.rx), int64(m.tx), int64(m.diskRead), int64(m.diskWrite), m.tcp, m.udp, m.online, round(m.temp, 1))
		return err
	}
	for _, s := range w.servers {
		for t := start / 300 * 300; t < end; t += 300 {
			if err := put(five, s, t); err != nil {
				return err
			}
		}
		for t := end - 48*3600; t < end; t += 60 {
			if err := put(minute, s, t); err != nil {
				return err
			}
		}
		for b := start / 600; b < end/600; b++ {
			up := 0
			for t := b * 600; t < b*600+600; t += 60 {
				if !outageAt(s, t) {
					up++
				}
			}
			if _, err := uptime.ExecContext(ctx, s.id, b, up); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	slog.Info("demo history: the hosts' numbers written", "days", 30)

	// what the agents would have sent, two hours at a time
	for _, g := range agents {
		n := 0
		for a := start; a < end; a += 7200 {
			b := min(a+7200, end)
			if err := g.report(ctx, &proto.Report{Batch: g.batch(a, b, false)}); err != nil {
				return fmt.Errorf("%s, history at %s: %w", g.s.m.Name, time.Unix(a, 0).UTC().Format(time.DateTime), err)
			}
			n++
		}
		slog.Info("demo history: agent reports sent", "server", g.s.m.Name, "reports", n)
	}

	// the timeline: the month's outages instead of the setup's own events; and the servers and
	// people as old as the demo says they are
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM events`); err != nil {
		return err
	}
	type ev struct {
		ts          int64
		level, kind string
		server      int64
		msg         string
	}
	var evs []ev
	for _, o := range outages {
		for _, s := range w.servers {
			if s.m.Name != o.server {
				continue
			}
			t := (setupTime/86400-int64(o.daysAgo))*86400 + int64(o.hour*3600)
			back := t + int64(o.minutes*60)
			if o.reboot {
				evs = append(evs, ev{back + 40, "warn", "server_rebooted", s.id,
					fmt.Sprintf("%s restarted: the machine booted again, and its protocols were down until it was back", s.m.Name)})
				continue
			}
			evs = append(evs, ev{t + 90, "crit", "server_offline", s.id, fmt.Sprintf("%s stopped reporting - the server or its network may be down", s.m.Name)},
				ev{back, "info", "server_online", s.id, fmt.Sprintf("%s is back online after %d minutes", s.m.Name, int(o.minutes))})
		}
	}
	for i := 0; i < len(evs); i++ { // oldest first, so the newest has the highest id
		for j := i + 1; j < len(evs); j++ {
			if evs[j].ts < evs[i].ts {
				evs[i], evs[j] = evs[j], evs[i]
			}
		}
	}
	for _, e := range evs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO events (ts, account_id, level, kind, server_id, message) VALUES (?, 1, ?, ?, ?, ?)`,
			e.ts, e.level, e.kind, e.server, e.msg); err != nil {
			return err
		}
	}
	for i, s := range w.servers {
		since := setupTime - int64(35+hash01(72, int64(i))*300)*86400
		if _, err := tx.ExecContext(ctx, `UPDATE servers SET created_at = ?, first_seen_at = ? WHERE id = ?`, since, since+600, s.id); err != nil {
			return err
		}
	}
	for _, u := range w.users {
		if _, err := tx.ExecContext(ctx, `UPDATE subs SET created_at = ? WHERE id = ?`, u.started(), u.id); err != nil {
			return err
		}
		if u.p.Plan == "" && u.p.Reset == "none" { // their one cycle began when they did (or when the history does)
			if _, err := tx.ExecContext(ctx, `UPDATE subs SET cycle_start = ? WHERE id = ?`, max(u.started(), start), u.id); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
