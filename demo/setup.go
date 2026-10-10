package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"time"

	"meridian/internal/proto"
	"meridian/internal/seal"

	_ "modernc.org/sqlite"
)

// setup fills a new panel through its API with the demo's servers, protocols, users, plans,
// monitors, a traffic rule or two and a forward; connects every demo agent; and writes a month of
// history. It runs once, on a panel nobody has used: the demo's data is the whole panel.

func setup(ctx context.Context, a *api, panel, dataDir, publicURL string) (*state, error) {
	var existing []apiServer
	if err := a.call("GET", "/api/servers", nil, &existing); err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		return nil, errors.New("this panel has servers already - setup fills only a new, empty panel")
	}
	setupTime = time.Now().Unix()
	st := &state{SetupAt: setupTime, Boot: map[int64]int64{}, Tokens: map[int64]string{}}

	// settings: the status page open to everyone, without addresses; the panel drawn somewhere else
	var set map[string]any
	if err := a.call("GET", "/api/settings", nil, &set); err != nil {
		return nil, err
	}
	for k, v := range map[string]any{
		"site_title": "Rosélune Demo", "public_url": publicURL, "timezone": "UTC", "conn_log": true, "dest_log": true,
		"auto_update": false, "status_page": "home", "status_public": true, "status_overview": true, "status_events": true,
		"status_ips": false, "status_hub": hub, "default_tone": "romance", "logo_mark": "rose",
		"status_about": "A demo of Rosélune: every server, person and number here is made up, and nothing can be changed.",
	} {
		set[k] = v
	}
	if err := a.call("PUT", "/api/settings", set, nil); err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}

	planIDs := map[string]int64{}
	for i, p := range plans {
		in := map[string]any{"name": p.Name, "note": p.Note, "quota": int64(p.QuotaGB * gb), "count_mode": "both",
			"reset_day": 1, "ip_limit": p.Devices, "device_mode": "alert", "price": p.Price, "currency": "USD", "sort": i}
		if p.Refuse {
			in["device_mode"] = "refuse"
		}
		var out struct {
			ID int64 `json:"id"`
		}
		if err := a.call("POST", "/api/plans", in, &out); err != nil {
			return nil, fmt.Errorf("plan %s: %w", p.Name, err)
		}
		planIDs[p.Name] = out.ID
	}

	// servers, with their places set by hand (their addresses are made up), then their agents
	day := time.Unix(setupTime, 0).UTC()
	for i, m := range fleet {
		in := map[string]any{"name": m.Name, "note": m.Note, "sort": i, "price": m.Price, "currency": "USD",
			"billing_cycle": m.Cycle, "bw_reset_day": 1, "bw_limit": int64(m.BwTB * 1e12), "bw_mode": "max",
			"expires_on": renewal(i, day)}
		var out struct {
			Server struct {
				ID int64 `json:"id"`
			} `json:"server"`
		}
		if err := a.call("POST", "/api/servers", in, &out); err != nil {
			return nil, fmt.Errorf("server %s: %w", m.Name, err)
		}
		st.Boot[out.Server.ID] = setupTime - int64(9+hash01(70, int64(i))*80)*86400 - int64(hash01(71, int64(i))*86400)
	}
	db, err := openDB(dataDir)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id, secret FROM servers WHERE deleted_at = 0`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var secret string
		if rows.Scan(&id, &secret) == nil {
			st.Tokens[id] = seal.Token(id, secret)
		}
	}
	rows.Close()

	w, err := loadWorld(a, st.Tokens)
	if err != nil {
		return nil, err
	}
	version, apiPort, err := panelFacts(a)
	if err != nil {
		return nil, err
	}
	var agents []*demoAgent
	for _, s := range w.servers {
		g, err := newAgent(w, s, panel, version, apiPort, st.Boot[s.id])
		if err != nil {
			return nil, err
		}
		if err := g.report(ctx, &proto.Report{Hello: g.hello(), Live: g.live(time.Now().Unix())}); err != nil {
			return nil, fmt.Errorf("%s says hello: %w", s.m.Name, err)
		}
		agents = append(agents, g)
	}
	slog.Info("demo servers connected", "servers", len(agents))
	// their places, by hand: no IP database knows the documentation addresses
	for _, s := range w.servers {
		loc := map[string]any{"location": map[string]any{"city": s.m.City, "cc": s.m.CC, "lat": s.m.Lat, "lon": s.m.Lon}}
		if err := a.call("PATCH", fmt.Sprintf("/api/servers/%d", s.id), loc, nil); err != nil {
			return nil, fmt.Errorf("%s's place: %w", s.m.Name, err)
		}
	}

	// their protocols, with the panel's defaults
	for _, s := range w.servers {
		for _, kind := range s.m.Protocols {
			if err := a.call("POST", fmt.Sprintf("/api/servers/%d/nodes", s.id), map[string]any{"kind": kind}, nil); err != nil {
				return nil, fmt.Errorf("%s on %s: %w", kind, s.m.Name, err)
			}
		}
	}
	if w, err = loadWorld(a, st.Tokens); err != nil {
		return nil, err
	}
	byName := map[string]*server{}
	for _, s := range w.servers {
		byName[s.m.Name] = s
	}

	// people
	for _, p := range people {
		in := map[string]any{"name": p.Name, "username": p.Username, "note": p.Note,
			"starts_at": setupTime - int64(p.Started*86400)}
		if p.Plan != "" {
			in["plan_id"] = planIDs[p.Plan]
		} else {
			in["quota"] = int64(p.QuotaGB * gb)
			in["ip_limit"] = p.Limit
			switch p.Reset {
			case "month":
				in["reset_day"] = 1
			case "30d":
				in["reset_every"] = 30
			}
		}
		if p.Expires != 0 {
			in["expires_at"] = setupTime + int64(p.Expires*86400)
		}
		if len(p.Servers) > 0 {
			var ids []int64
			for _, name := range p.Servers {
				ids = append(ids, byName[name].id)
			}
			in["servers"] = ids
		}
		var created []struct {
			ID int64 `json:"id"`
		}
		if err := a.call("POST", "/api/users", in, &created); err != nil || len(created) != 1 {
			return nil, fmt.Errorf("user %s: %v", p.Name, err)
		}
		out := created[0]
		if p.Paused {
			if err := a.call("POST", fmt.Sprintf("/api/users/%d/pause", out.ID), nil, nil); err != nil {
				return nil, fmt.Errorf("pause %s: %w", p.Name, err)
			}
		}
	}

	for i, m := range monitors {
		in := map[string]any{"name": m.Name, "target": m.Target, "kind": m.Kind, "every_secs": m.Every, "public": m.Public, "sort": i}
		if m.Port > 0 {
			in["port"] = m.Port
		}
		if err := a.call("POST", "/api/ping-monitors", in, nil); err != nil {
			return nil, fmt.Errorf("monitor %s: %w", m.Name, err)
		}
	}

	// a rule for everyone, one that sends streaming from Asia out through London, and a forward
	london := byName["London"]
	rules := []map[string]any{
		{"name": "No ads or trackers", "enabled": true, "match": map[string]any{"sites": []string{"category-ads-all"}}, "target": "block"},
		{"name": "BitTorrent stays off the servers", "enabled": true, "match": map[string]any{"bittorrent": true}, "target": "block"},
	}
	if london != nil && len(london.nodes) > 0 {
		rules = append(rules, map[string]any{"name": "Streaming leaves from London", "enabled": true,
			"servers": []int64{byName["Tokyo"].id, byName["Singapore"].id},
			"match":   map[string]any{"sites": []string{"netflix", "bbc"}}, "target": fmt.Sprintf("node:%d", london.nodes[0].ID)})
	}
	for _, r := range rules {
		if err := a.call("POST", "/api/routing/rules", r, nil); err != nil {
			return nil, fmt.Errorf("rule %v: %w", r["name"], err)
		}
	}
	if f := byName["Frankfurt"]; f != nil {
		in := map[string]any{"name": "Game server", "listen_port": 25565, "network": "tcp+udp", "target": "198.51.100.200:25565", "engine": "nft"}
		if err := a.call("POST", fmt.Sprintf("/api/servers/%d/forwards", f.id), in, nil); err != nil {
			return nil, fmt.Errorf("forward: %w", err)
		}
	}

	// everything in place on every server, then the month before today
	if w, err = loadWorld(a, st.Tokens); err != nil {
		return nil, err
	}
	agents = agents[:0]
	for _, s := range w.servers {
		g, err := newAgent(w, s, panel, version, apiPort, st.Boot[s.id])
		if err != nil {
			return nil, err
		}
		if err := g.sync(ctx); err != nil {
			return nil, fmt.Errorf("%s: %w", s.m.Name, err)
		}
		agents = append(agents, g)
	}
	if err := backfill(ctx, db, w, agents); err != nil {
		return nil, err
	}
	return st, nil
}

// renewal is when a demo server is paid next: the one paid by the year has a date (in June, so the
// status page is not always telling visitors that a payment is due); the monthly ones renew by
// themselves.
func renewal(i int, now time.Time) string {
	if fleet[i].Cycle != "year" {
		return ""
	}
	d := time.Date(now.Year(), time.June, 14, 0, 0, 0, 0, time.UTC)
	if !d.After(now) {
		d = d.AddDate(1, 0, 0)
	}
	return d.Format("2006-01-02")
}

// freshen keeps the demo's dates where its story has them: renewals ahead, Grace's access ending in
// three days and Jack's ended two days ago - every day, however long the demo runs.
func freshen(ctx context.Context, db *sql.DB, w *world) error {
	now := time.Now().UTC()
	for _, s := range w.servers {
		if _, err := db.ExecContext(ctx, `UPDATE servers SET expires_on = ? WHERE id = ?`, renewal(s.idx, now), s.id); err != nil {
			return err
		}
	}
	today := now.Unix() / 86400 * 86400
	for _, u := range w.users {
		if u.p.Expires == 0 {
			continue
		}
		at := today + int64(u.p.Expires*86400) + 15*3600
		if _, err := db.ExecContext(ctx, `UPDATE subs SET expires_at = ? WHERE id = ? AND expires_at != ?`, at, u.id, at); err != nil {
			return err
		}
	}
	return nil
}

// panelFacts are the panel's version (the agents claim the same) and the agents' loopback port.
func panelFacts(a *api) (string, int, error) {
	var me struct {
		Version string `json:"version"`
	}
	if err := a.call("GET", "/api/me", nil, &me); err != nil {
		return "", 0, err
	}
	var set struct {
		AgentPort int `json:"agent_port"`
	}
	if err := a.call("GET", "/api/settings", nil, &set); err != nil {
		return "", 0, err
	}
	return me.Version, set.AgentPort, nil
}

// openDB opens the panel's SQLite database the way the panel does, next to the running panel.
func openDB(dataDir string) (*sql.DB, error) {
	q := url.Values{}
	for _, p := range []string{"busy_timeout(10000)", "journal_mode(WAL)", "foreign_keys(1)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "meridian.db")+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, db.Ping()
}
