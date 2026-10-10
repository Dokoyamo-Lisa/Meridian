package main

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"meridian/internal/proto"
)

// run plays every demo server's agent until it is stopped: a live report every ten seconds, what
// accumulated every minute, and a look at the panel's state every ten minutes.
func run(ctx context.Context, a *api, panel, dataDir string, st *state) error {
	setupTime = st.SetupAt
	w, err := loadWorld(a, st.Tokens)
	if err != nil {
		return err
	}
	var db *sql.DB
	if dataDir != "" {
		if db, err = openDB(dataDir); err != nil {
			return err
		}
		defer db.Close()
	}
	version, apiPort, err := panelFacts(a)
	if err != nil {
		return err
	}
	var agents []*demoAgent
	for _, s := range w.servers {
		g, err := newAgent(w, s, panel, version, apiPort, st.Boot[s.id])
		if err != nil {
			return err
		}
		agents = append(agents, g)
	}
	t := time.Now().Unix()
	for _, g := range agents {
		if err := g.report(ctx, &proto.Report{Hello: g.hello(), Live: g.live(t)}); err != nil {
			slog.Warn("demo agent", "server", g.s.m.Name, "err", err)
		}
		if err := g.sync(ctx); err != nil {
			slog.Warn("demo agent state", "server", g.s.m.Name, "err", err)
		}
	}
	slog.Info("demo agents running", "servers", len(agents), "version", version)
	last, synced, freshened := t, t, int64(0)
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		t := time.Now().Unix()
		if db != nil && t-freshened >= 3600 {
			if err := freshen(ctx, db, w); err != nil {
				slog.Warn("demo dates", "err", err)
			}
			freshened = t
		}
		if t-synced >= 600 { // people paused or added, monitors changed: follow the panel
			if nw, err := loadWorld(a, st.Tokens); err == nil {
				for _, g := range agents {
					for _, s := range nw.servers {
						if s.id == g.s.id {
							g.w, g.s, g.kinds = nw, s, nw.kinds()
						}
					}
					if err := g.sync(ctx); err != nil {
						slog.Warn("demo agent state", "server", g.s.m.Name, "err", err)
					}
				}
				w = nw
			} else {
				slog.Warn("demo world", "err", err)
			}
			synced = t
		}
		var batch bool
		if t/60 != last/60 {
			batch = true
		}
		for _, g := range agents {
			rep := &proto.Report{Live: g.live(t)}
			if batch {
				rep.Batch = g.batch(last, t, true)
			}
			if err := g.report(ctx, rep); err != nil && ctx.Err() == nil {
				slog.Warn("demo agent", "server", g.s.m.Name, "err", err)
			}
		}
		if batch {
			last = t
		}
	}
}
