package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"meridian/internal/proto"
)

// Ping monitors: addresses the servers measure the way to at fixed intervals - a round of three ICMP
// echoes, or three TCP connections - for the status page's charts. The agents deliver the rounds in
// their batches (kept until the panel has them, so an interrupted link loses none); the panel keeps
// them two days as they came and a month in five-minute steps.

type PingMonitor struct {
	ID        int64   `json:"id"`
	AccountID int64   `json:"-"`
	Name      string  `json:"name"`
	Target    string  `json:"target" doc:"A host name or IP address"`
	Kind      string  `json:"kind" doc:"icmp (echo) or tcp (the time a connection takes)"`
	Port      int     `json:"port" doc:"With tcp: the port"`
	Every     int     `json:"every_secs" doc:"Seconds between rounds (10 to 3600)"`
	Servers   []int64 `json:"servers" doc:"The servers that measure it; empty = all of them"`
	Public    bool    `json:"public" doc:"Shown on the status page (where it shows the servers' details to visitors)"`
	Enabled   bool    `json:"enabled"`
	Sort      int     `json:"sort"`
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
}

const pingCols = `id, account_id, name, target, kind, port, every_secs, servers, public, enabled, sort, created_at, updated_at`

const maxPingMonitors = 20

func scanPing(r interface{ Scan(...any) error }) (*PingMonitor, error) {
	m := &PingMonitor{}
	var servers string
	if err := r.Scan(&m.ID, &m.AccountID, &m.Name, &m.Target, &m.Kind, &m.Port, &m.Every, &servers, &m.Public, &m.Enabled, &m.Sort,
		&m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	if servers != "" {
		_ = json.Unmarshal([]byte(servers), &m.Servers)
	}
	if m.Servers == nil {
		m.Servers = []int64{}
	}
	return m, nil
}

func (p *Panel) pingMonitorsOf(ctx context.Context, acct int64) ([]*PingMonitor, error) {
	q, args := `SELECT `+pingCols+` FROM ping_monitors`, []any{}
	if acct > 0 {
		q += ` WHERE account_id = ?`
		args = append(args, acct)
	}
	rows, err := p.db.QueryContext(ctx, q+` ORDER BY sort, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PingMonitor
	for rows.Next() {
		m, err := scanPing(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (m *PingMonitor) measuredBy(serverID int64) bool {
	return len(m.Servers) == 0 || slices.Contains(m.Servers, serverID)
}

// pingTargetsFor are what a server measures (compile.go puts them in its state).
func (p *Panel) pingTargetsFor(ctx context.Context, srv *Server) []proto.PingTarget {
	list, err := p.pingMonitorsOf(ctx, srv.AccountID)
	if err != nil {
		return nil
	}
	var out []proto.PingTarget
	for _, m := range list {
		if !m.Enabled || !m.measuredBy(srv.ID) {
			continue
		}
		// a server shared with this panel measures public addresses only (its agent refuses the rest)
		if a, err := netip.ParseAddr(m.Target); err == nil && srv.Guest && !publicAddr(a.Unmap()) {
			continue
		}
		out = append(out, proto.PingTarget{ID: m.ID, Target: m.Target, Kind: m.Kind, Port: m.Port, Interval: m.Every})
	}
	return out
}

// hottest is a host's highest temperature (0: it has no sensors), kept within what sensors read.
func hottest(temps map[string]float64) float64 {
	t := 0.0
	for _, v := range temps {
		if v > t && v < 200 {
			t = v
		}
	}
	return roundTo(t, 1)
}

// storePings keeps a batch's rounds (of monitors this server measures; each once). Rounds measured
// while the panel could not be reached come late: their five-minute steps are folded again here.
func storePings(tx *sql.Tx, srv *Server, rs []proto.PingResult) error {
	if len(rs) == 0 {
		return nil
	}
	rows, err := tx.Query(`SELECT id FROM ping_monitors WHERE account_id = ?`, srv.AccountID)
	if err != nil {
		return err
	}
	ours := map[int64]bool{}
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ours[id] = true
		}
	}
	rows.Close()
	cut, recent := now()-32*86400, (now()/300-3)*300
	late := int64(0)
	clampMS := func(v float64) float64 { return min(max(v, 0), 60000) }
	for _, r := range rs {
		if !ours[r.ID] || r.TS < cut || r.TS > now()+300 || r.Sent < 1 || r.Sent > 10 || r.Lost < 0 || r.Lost > r.Sent {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO ping_samples (monitor_id, server_id, ts, sent, lost, avg_ms, min_ms, max_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(monitor_id, server_id, ts) DO NOTHING`, r.ID, srv.ID, r.TS, r.Sent, r.Lost, clampMS(r.Avg), clampMS(r.Min), clampMS(r.Max)); err != nil {
			return err
		}
		if r.TS < recent && (late == 0 || r.TS < late) {
			late = r.TS
		}
	}
	if late > 0 {
		return rollupPings(tx, `server_id = ? AND ts >= ?`, srv.ID, late/300*300)
	}
	return nil
}

// rollupPings folds the samples the condition picks into their five-minute steps.
func rollupPings(tx *sql.Tx, where string, args ...any) error {
	_, err := tx.Exec(`INSERT INTO ping_5m (monitor_id, server_id, ts, sent, lost, avg_ms, min_ms, max_ms)
		SELECT monitor_id, server_id, (ts / 300) * 300, SUM(sent), SUM(lost),
		CASE WHEN SUM(sent - lost) > 0 THEN SUM(avg_ms * (sent - lost)) / SUM(sent - lost) ELSE 0 END,
		COALESCE(MIN(CASE WHEN sent > lost THEN min_ms END), 0), MAX(max_ms)
		FROM ping_samples WHERE `+where+` GROUP BY monitor_id, server_id, (ts / 300) * 300
		ON CONFLICT(monitor_id, server_id, ts) DO UPDATE SET sent = excluded.sent, lost = excluded.lost, avg_ms = excluded.avg_ms,
		min_ms = excluded.min_ms, max_ms = excluded.max_ms`, args...)
	return err
}

// rollup5m folds the samples since from into five-minute steps (kept a month), and drops what is past
// keeping. It runs every few minutes; a step is written again while its minutes come in.
func (p *Panel) rollup5m(ctx context.Context, from int64) {
	err := p.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO server_metrics_5m (server_id, ts, cpu, mem_used, swap_used, disk_used, load1, load5, load15, rx_rate,
			tx_rate, disk_read, disk_write, tcp, udp, online, temp)
			SELECT server_id, (ts / 300) * 300, AVG(cpu), CAST(AVG(mem_used) AS INTEGER), CAST(AVG(swap_used) AS INTEGER),
			CAST(AVG(disk_used) AS INTEGER), AVG(load1), AVG(load5), AVG(load15), CAST(AVG(rx_rate) AS INTEGER), CAST(AVG(tx_rate) AS INTEGER),
			CAST(AVG(disk_read) AS INTEGER), CAST(AVG(disk_write) AS INTEGER), CAST(AVG(tcp) AS INTEGER), CAST(AVG(udp) AS INTEGER),
			CAST(AVG(online) AS INTEGER), MAX(temp)
			FROM server_metrics WHERE ts >= ? GROUP BY server_id, (ts / 300) * 300
			ON CONFLICT(server_id, ts) DO UPDATE SET cpu = excluded.cpu, mem_used = excluded.mem_used, swap_used = excluded.swap_used,
			disk_used = excluded.disk_used, load1 = excluded.load1, load5 = excluded.load5, load15 = excluded.load15, rx_rate = excluded.rx_rate,
			tx_rate = excluded.tx_rate, disk_read = excluded.disk_read, disk_write = excluded.disk_write, tcp = excluded.tcp, udp = excluded.udp,
			online = excluded.online, temp = excluded.temp`, from); err != nil {
			return err
		}
		if err := rollupPings(tx, `ts >= ?`, from); err != nil {
			return err
		}
		month := now() - 32*86400
		for _, q := range []string{`DELETE FROM server_metrics_5m WHERE ts < ?`, `DELETE FROM ping_5m WHERE ts < ?`} {
			if _, err := tx.Exec(q, month); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`DELETE FROM ping_samples WHERE ts < ?`, now()-49*3600)
		return err
	})
	if err != nil {
		slog.Error("five-minute steps", "err", err)
	}
}

// ---------------------------------------------------------------- the API

type pingInput struct {
	Name    *string  `json:"name"`
	Target  *string  `json:"target" doc:"A host name or IP address on the internet"`
	Kind    *string  `json:"kind" doc:"icmp (default) or tcp"`
	Port    *int     `json:"port" doc:"With tcp: the port (e.g. 443)"`
	Every   *int     `json:"every_secs" doc:"Seconds between rounds, 10 to 3600 (default 60)"`
	Servers *[]int64 `json:"servers" doc:"The servers that measure it; empty = all of them, new ones too"`
	Public  *bool    `json:"public" doc:"Shown on the status page (default true)"`
	Enabled *bool    `json:"enabled"`
	Sort    *int     `json:"sort"`
}

func (p *Panel) applyPingInput(ctx context.Context, m *PingMonitor, in *pingInput) error {
	if in.Name != nil {
		m.Name = cleanName(*in.Name, 48)
	}
	if in.Target != nil {
		t, err := normHost(*in.Target)
		if err != nil || t == "" {
			return errStatus(http.StatusBadRequest, "the target is a host name or an IP address")
		}
		m.Target = t
	}
	if m.Target == "" {
		return errStatus(http.StatusBadRequest, "give the address to measure")
	}
	if m.Name == "" {
		m.Name = m.Target
	}
	if in.Kind != nil {
		m.Kind = strings.ToLower(strings.TrimSpace(*in.Kind))
	}
	if m.Kind == "" {
		m.Kind = "icmp"
	}
	if in.Port != nil {
		m.Port = *in.Port
	}
	switch m.Kind {
	case "icmp":
		m.Port = 0
	case "tcp":
		if m.Port < 1 || m.Port > 65535 {
			return errStatus(http.StatusBadRequest, "a TCP monitor needs a port (1-65535)")
		}
	default:
		return errStatus(http.StatusBadRequest, "kind is icmp or tcp")
	}
	if in.Every != nil {
		m.Every = *in.Every
	}
	if m.Every == 0 {
		m.Every = 60
	}
	if m.Every < 10 || m.Every > 3600 {
		return errStatus(http.StatusBadRequest, "measure every 10 to 3600 seconds")
	}
	if in.Servers != nil {
		var list []int64
		for _, id := range *in.Servers {
			s, err := p.serverByID(ctx, id)
			if err != nil || s.AccountID != m.AccountID || s.DeletedAt > 0 {
				return errStatus(http.StatusBadRequest, fmt.Sprintf("there is no server %d", id))
			}
			if !slices.Contains(list, id) {
				list = append(list, id)
			}
		}
		if list == nil {
			list = []int64{}
		}
		m.Servers = list
	}
	if in.Public != nil {
		m.Public = *in.Public
	}
	if in.Enabled != nil {
		m.Enabled = *in.Enabled
	}
	if in.Sort != nil {
		m.Sort = *in.Sort
	}
	return nil
}

func (p *Panel) savePing(ctx context.Context, m *PingMonitor) error {
	servers := ""
	if len(m.Servers) > 0 {
		b, _ := json.Marshal(m.Servers)
		servers = string(b)
	}
	m.UpdatedAt = now()
	_, err := p.db.Exec1(`UPDATE ping_monitors SET name = ?, target = ?, kind = ?, port = ?, every_secs = ?, servers = ?, public = ?, enabled = ?,
		sort = ?, updated_at = ? WHERE id = ?`, m.Name, m.Target, m.Kind, m.Port, m.Every, servers, m.Public, m.Enabled, m.Sort, m.UpdatedAt, m.ID)
	return err
}

type pingLatest struct {
	ServerID int64   `json:"server_id"`
	TS       int64   `json:"ts"`
	Avg      float64 `json:"avg_ms"`
	Loss     float64 `json:"loss" doc:"Share of probes lost in the last hour (0-1)"`
}

type pingView struct {
	*PingMonitor
	Latest []pingLatest `json:"latest" doc:"Per server: the last round's average and the last hour's loss"`
}

func (p *Panel) apiPingMonitors(w http.ResponseWriter, r *http.Request, a *Account) error {
	list, err := p.pingMonitorsOf(r.Context(), scopeAccount(r, a))
	if err != nil {
		return err
	}
	out := []pingView{}
	for _, m := range list {
		out = append(out, pingView{PingMonitor: m, Latest: p.pingLatestOf(r.Context(), m.ID)})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// pingLatestOf is a monitor's last round per server, with the last hour's loss.
func (p *Panel) pingLatestOf(ctx context.Context, id int64) []pingLatest {
	out := []pingLatest{}
	rows, err := p.db.QueryContext(ctx, `SELECT server_id, MAX(ts), SUM(sent), SUM(lost) FROM ping_samples WHERE monitor_id = ? AND ts >= ?
		GROUP BY server_id ORDER BY server_id`, id, now()-3600)
	if err != nil {
		return out
	}
	for rows.Next() {
		var l pingLatest
		var sent, lost int64
		if rows.Scan(&l.ServerID, &l.TS, &sent, &lost) == nil {
			if sent > 0 {
				l.Loss = float64(lost) / float64(sent)
			}
			out = append(out, l)
		}
	}
	rows.Close()
	for i := range out {
		_ = p.db.QueryRowContext(ctx, `SELECT avg_ms FROM ping_samples WHERE monitor_id = ? AND server_id = ? AND ts = ?`,
			id, out[i].ServerID, out[i].TS).Scan(&out[i].Avg)
	}
	return out
}

func (p *Panel) apiCreatePing(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in pingInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	m := &PingMonitor{AccountID: a.ID, Public: true, Enabled: true, Servers: []int64{}}
	if err := p.applyPingInput(r.Context(), m, &in); err != nil {
		return err
	}
	list, err := p.pingMonitorsOf(r.Context(), a.ID)
	if err != nil {
		return err
	}
	if len(list) >= maxPingMonitors {
		return errStatus(http.StatusConflict, fmt.Sprintf("at most %d ping monitors - remove one first", maxPingMonitors))
	}
	t := now()
	res, err := p.db.Exec1(`INSERT INTO ping_monitors (account_id, name, target, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, a.ID, m.Name, m.Target, t, t)
	if err != nil {
		return err
	}
	m.ID, _ = res.LastInsertId()
	m.CreatedAt = t
	if err := p.savePing(r.Context(), m); err != nil {
		return err
	}
	p.event(a.ID, "info", "ping_added", 0, 0, a.ID, fmt.Sprintf("%s added the ping monitor %s (%s)", a.Username, m.Name, m.Target), nil)
	p.touchAccount(a.ID)
	writeJSON(w, http.StatusCreated, pingView{PingMonitor: m, Latest: []pingLatest{}})
	return nil
}

func (p *Panel) ownPing(ctx context.Context, a *Account, id int64) (*PingMonitor, error) {
	m, err := scanPing(p.db.QueryRowContext(ctx, `SELECT `+pingCols+` FROM ping_monitors WHERE id = ?`, id))
	if err != nil || (!a.IsOwner() && m.AccountID != a.ID) {
		return nil, errNotFound
	}
	return m, nil
}

func (p *Panel) apiUpdatePing(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	m, err := p.ownPing(r.Context(), a, id)
	if err != nil {
		return err
	}
	var in pingInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := p.applyPingInput(r.Context(), m, &in); err != nil {
		return err
	}
	if err := p.savePing(r.Context(), m); err != nil {
		return err
	}
	p.touchAccount(m.AccountID)
	writeJSON(w, http.StatusOK, pingView{PingMonitor: m, Latest: []pingLatest{}})
	return nil
}

func (p *Panel) apiDeletePing(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	m, err := p.ownPing(r.Context(), a, id)
	if err != nil {
		return err
	}
	if err := p.db.Write(r.Context(), func(tx *sql.Tx) error {
		for _, q := range []string{`DELETE FROM ping_monitors WHERE id = ?`, `DELETE FROM ping_samples WHERE monitor_id = ?`, `DELETE FROM ping_5m WHERE monitor_id = ?`} {
			if _, err := tx.Exec(q, m.ID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	p.event(m.AccountID, "info", "ping_removed", 0, 0, a.ID, fmt.Sprintf("%s removed the ping monitor %s", a.Username, m.Name), nil)
	p.touchAccount(m.AccountID)
	writeJSON(w, http.StatusOK, okResult{OK: true})
	return nil
}

// ---------------------------------------------------------------- series (the charts)

// seriesRanges: how far back, in steps of how many seconds, from which table.
var seriesRanges = map[string]struct {
	back, step int64
	fine       bool // from the minute samples (else the five-minute steps)
}{
	"1h": {3600, 60, true}, "6h": {6 * 3600, 120, true}, "24h": {86400, 300, true}, "7d": {7 * 86400, 1800, false}, "30d": {30 * 86400, 7200, false},
}

// chartKinds are the charts of a server's details; the status page shows visitors the ones the
// settings allow (status_charts).
var chartKinds = []string{"cpu", "memory", "disk", "diskio", "network", "load", "connections", "temperature", "ping"}

type seriesView struct {
	Range   string               `json:"range"`
	Step    int64                `json:"step" doc:"Seconds between points: a longer gap is a gap (the server did not report)"`
	T       []int64              `json:"t" doc:"The points' times (Unix seconds)"`
	Metrics map[string][]float64 `json:"metrics" doc:"Per series, a value per point: cpu (%), mem, swap, disk (bytes), rx, tx, dread, dwrite (bytes/s), load1/5/15, tcp, udp, online, temp (°C, 0 = none)"`
	Totals  map[string]int64     `json:"totals" doc:"mem, swap and disk totals (bytes)"`
	Charts  []string             `json:"charts" doc:"The charts the viewer may see"`
	Ping    []pingSeries         `json:"ping"`
}

type pingSeries struct {
	ID     int64     `json:"id"`
	Name   string    `json:"name"`
	Kind   string    `json:"kind"`
	Target string    `json:"target,omitempty" doc:"Left out for visitors"`
	Every  int       `json:"every_secs"`
	T      []int64   `json:"t"`
	Avg    []float64 `json:"avg" doc:"Milliseconds (0 where every probe was lost)"`
	Loss   []float64 `json:"loss" doc:"Share of probes lost (0-1)"`
}

// seriesFor builds a server's charts' data. charts are the ones to include; public leaves out what
// visitors never see (monitors not on the status page, targets).
func (p *Panel) seriesFor(ctx context.Context, srv *Server, rng string, charts []string, public bool) (*seriesView, error) {
	sr, ok := seriesRanges[rng]
	if !ok {
		rng, sr = "24h", seriesRanges["24h"]
	}
	out := &seriesView{Range: rng, Step: sr.step, T: []int64{}, Metrics: map[string][]float64{}, Totals: map[string]int64{}, Charts: charts, Ping: []pingSeries{}}
	if out.Charts == nil {
		out.Charts = []string{}
	}
	since := now() - sr.back
	table := "server_metrics"
	if !sr.fine {
		table = "server_metrics_5m"
	}
	rows, err := p.db.QueryContext(ctx, `SELECT (ts / ?) * ? AS b, AVG(cpu), AVG(mem_used), AVG(swap_used), AVG(disk_used), AVG(rx_rate), AVG(tx_rate),
		AVG(disk_read), AVG(disk_write), AVG(load1), AVG(load5), AVG(load15), AVG(tcp), AVG(udp), AVG(online), MAX(temp)
		FROM `+table+` WHERE server_id = ? AND ts >= ? GROUP BY b ORDER BY b`, sr.step, sr.step, srv.ID, since)
	if err != nil {
		return nil, err
	}
	keys := []string{"cpu", "mem", "swap", "disk", "rx", "tx", "dread", "dwrite", "load1", "load5", "load15", "tcp", "udp", "online", "temp"}
	for rows.Next() {
		var t int64
		v := make([]float64, len(keys))
		ptrs := []any{&t}
		for i := range v {
			ptrs = append(ptrs, &v[i])
		}
		if err := rows.Scan(ptrs...); err != nil {
			rows.Close()
			return nil, err
		}
		out.T = append(out.T, t)
		for i, k := range keys {
			out.Metrics[k] = append(out.Metrics[k], roundTo(v[i], 2))
		}
	}
	rows.Close()
	// what each chart needs; the rest is left out
	need := map[string][]string{"cpu": {"cpu"}, "memory": {"mem", "swap"}, "disk": {"disk"}, "diskio": {"dread", "dwrite"}, "network": {"rx", "tx"},
		"load": {"load1", "load5", "load15"}, "connections": {"tcp", "udp", "online"}, "temperature": {"temp"}}
	keep := map[string]bool{}
	for _, c := range charts {
		for _, k := range need[c] {
			keep[k] = true
		}
	}
	if public {
		keep["online"] = false // how many people are connected stays the operator's
	}
	for k := range out.Metrics {
		if !keep[k] {
			delete(out.Metrics, k)
		}
	}
	if ls := p.live.get(srv.ID); ls != nil {
		out.Totals["mem"], out.Totals["swap"], out.Totals["disk"] = int64(ls.Live.Sys.MemTotal), int64(ls.Live.Sys.SwapTotal), int64(ls.Live.Sys.DiskTotal)
	} else {
		out.Totals["mem"], out.Totals["disk"] = srv.MemTotal, srv.DiskTotal
	}
	if !slices.Contains(charts, "ping") {
		return out, nil
	}
	mons, err := p.pingMonitorsOf(ctx, srv.AccountID)
	if err != nil {
		return nil, err
	}
	ptable := "ping_samples"
	if !sr.fine {
		ptable = "ping_5m"
	}
	for _, m := range mons {
		if !m.Enabled || !m.measuredBy(srv.ID) || (public && !m.Public) {
			continue
		}
		ps := pingSeries{ID: m.ID, Name: m.Name, Kind: m.Kind, Every: m.Every, T: []int64{}, Avg: []float64{}, Loss: []float64{}}
		if !public {
			ps.Target = m.Target
		}
		step := sr.step
		if sr.fine && rng == "1h" {
			step = int64(max(m.Every, 60)) // the rounds themselves, when they come once a minute or less
		}
		prow, err := p.db.QueryContext(ctx, `SELECT (ts / ?) * ? AS b, SUM(sent), SUM(lost),
			CASE WHEN SUM(sent - lost) > 0 THEN SUM(avg_ms * (sent - lost)) / SUM(sent - lost) ELSE 0 END
			FROM `+ptable+` WHERE monitor_id = ? AND server_id = ? AND ts >= ? GROUP BY b ORDER BY b`,
			step, step, m.ID, srv.ID, since)
		if err != nil {
			return nil, err
		}
		for prow.Next() {
			var t, sent, lost int64
			var avg float64
			if prow.Scan(&t, &sent, &lost, &avg) != nil || sent == 0 {
				continue
			}
			ps.T = append(ps.T, t)
			ps.Avg = append(ps.Avg, roundTo(avg, 1))
			ps.Loss = append(ps.Loss, roundTo(float64(lost)/float64(sent), 3))
		}
		prow.Close()
		out.Ping = append(out.Ping, ps)
	}
	return out, nil
}

func roundTo(v float64, d int) float64 {
	p := 1.0
	for i := 0; i < d; i++ {
		p *= 10
	}
	return float64(int64(v*p+0.5)) / p
}

// apiServerSeries is a server's charts for the panel: everything.
func (p *Panel) apiServerSeries(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownServer(r.Context(), a, id)
	if err != nil {
		return err
	}
	v, err := p.seriesFor(r.Context(), s, r.URL.Query().Get("range"), chartKinds, false)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

// apiStatusSeries is a server's charts for the status page: to whoever may see its servers, the
// charts the settings show visitors (the supervisor: all), never what a server hidden from it has.
func (p *Panel) apiStatusSeries(w http.ResponseWriter, r *http.Request) {
	sup, err := p.statusViewer(w, r)
	if err != nil {
		writeErr(w, err)
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	s, err := p.serverByID(r.Context(), id)
	if err != nil || s.DeletedAt > 0 || s.FirstSeenAt == 0 || s.StatusHidden {
		writeErr(w, errNotFound)
		return
	}
	charts := chartKinds
	if !sup {
		charts = nil
		for _, c := range p.settings().StatusCharts {
			if slices.Contains(chartKinds, c) {
				charts = append(charts, c)
			}
		}
	}
	v, err := p.seriesFor(r.Context(), s, r.URL.Query().Get("range"), charts, !sup)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, v)
}

// pingJob folds samples into five-minute steps now and then (jobs.go calls it): the last quarter of an
// hour each time, everything kept the first time (the minutes before the panel last stopped).
func (p *Panel) pingJob(ctx context.Context, last *time.Time) {
	if time.Since(*last) < 4*time.Minute {
		return
	}
	from := (now()/300 - 3) * 300
	if last.IsZero() {
		from = now() - 49*3600
	}
	*last = time.Now()
	p.rollup5m(ctx, from)
}
