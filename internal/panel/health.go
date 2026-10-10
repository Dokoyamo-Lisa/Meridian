package panel

// Health checks. Every agent scans its server every few minutes for signs that it was broken into or
// is abused (internal/agent/health) and reports what it finds. The panel keeps each finding as a
// risk, recognised again by its key, and the operator decides about every one: expected (this is
// mine - never flag it again, on this server or on all), acknowledged (seen - flag it again if it
// happens again) or open again. Nothing is ever paused or blocked because of a risk by itself: a
// risk may offer a protective step (protect.go), which happens only when the supervisor confirms it.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"meridian/internal/proto"
)

var severities = []string{proto.SevInfo, proto.SevWarning, proto.SevHigh, proto.SevCritical}

// sevRank orders severities: info 0 ... critical 3 (-1 for anything else).
func sevRank(s string) int { return slices.Index(severities, s) }

var findKinds = []string{proto.FindMiner, proto.FindProcess, proto.FindCPU, proto.FindPort, proto.FindAccount, proto.FindSSHKeys,
	proto.FindPreload, proto.FindCron, proto.FindService, proto.FindModule, proto.FindSSH, proto.FindTraffic, proto.FindLoad,
	proto.FindBinary, proto.FindFile, proto.FindUpdate}

var riskStatuses = []string{"open", "acknowledged", "expected"}

type riskView struct {
	ID         int64  `json:"id"`
	AccountID  int64  `json:"-"`
	ServerID   int64  `json:"server_id"`
	Server     string `json:"server" doc:"The server's name"`
	Key        string `json:"key" doc:"What was found, in a form that stays the same when it is found again, e.g. port:tcp:31337 or ssh-login:root:203.0.113.5"`
	Kind       string `json:"kind" doc:"miner, process, cpu, port, account, ssh_keys, preload, cron, service, module, ssh, traffic, load, binary, file or update"`
	Severity   string `json:"severity" doc:"info | warning | high | critical"`
	Title      string `json:"title"`
	Detail     string `json:"detail" doc:"What exactly, and what to do"`
	FirstSeen  int64  `json:"first_seen"`
	LastSeen   int64  `json:"last_seen"`
	Count      int    `json:"count" doc:"How many times it was found"`
	Active     bool   `json:"active" doc:"It still holds - a process still runs, a port is still open; false for something that happened once"`
	Status     string `json:"status" doc:"open, acknowledged (seen: flagged again if it happens again) or expected (never flagged again)"`
	DecidedBy  string `json:"decided_by" doc:"Who decided the status, empty when nobody did"`
	DecidedAt  int64  `json:"decided_at"`
	Everywhere bool   `json:"expected_everywhere" doc:"This key is expected on every server"`
	// what can be done about it (protect.go): offered by the agent, done only when the supervisor
	// confirms it in the browser
	Fix  *fixView        `json:"fix,omitempty" doc:"A protective step the server offers: done only when the supervisor confirms it in the panel"`
	Step *protectionView `json:"step,omitempty" doc:"The last protective step taken about it, and how it went"`
	fix  string          // the fix as stored (JSON)
}

type riskDecision struct {
	Decision string `json:"decision" doc:"expected (this is mine: never flag it again), acknowledged (seen: flag it again if it happens again) or open"`
	Scope    string `json:"scope" doc:"server (the default) or all: with expected, on every server - today's and future ones; with acknowledged or open, every server's risk with this key"`
}

type riskDecided struct {
	Risk    *riskView `json:"risk"`
	Changed int       `json:"changed" doc:"How many risks changed (several with scope all)"`
}

type serverHealth struct {
	BaselineAt int64       `json:"baseline_at" doc:"When the first scan recorded what is normal on the server; 0 = no scan yet (agents before 1.0 do not scan)"`
	ScannedAt  int64       `json:"scanned_at" doc:"The last scan that reached the panel"`
	Open       int         `json:"open" doc:"Open risks"`
	Worst      string      `json:"worst" doc:"The most serious open risk's severity; empty when none is open"`
	Risks      []*riskView `json:"risks" doc:"Every risk found on the server, open ones first, most serious first"`
}

const riskCols = `r.id, r.account_id, r.server_id, s.name, r.key, r.kind, r.severity, r.title, r.detail, r.first_seen, r.last_seen,
r.count, r.active, r.status, r.decided_by, r.decided_at,
EXISTS(SELECT 1 FROM risk_rules x WHERE x.account_id = r.account_id AND x.key = r.key), r.fix`

const riskOrder = ` ORDER BY CASE r.status WHEN 'open' THEN 0 WHEN 'acknowledged' THEN 1 ELSE 2 END,
CASE r.severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'warning' THEN 2 ELSE 3 END, r.last_seen DESC, r.id DESC`

func scanRisk(row interface{ Scan(...any) error }) (*riskView, error) {
	v := &riskView{}
	err := row.Scan(&v.ID, &v.AccountID, &v.ServerID, &v.Server, &v.Key, &v.Kind, &v.Severity, &v.Title, &v.Detail, &v.FirstSeen,
		&v.LastSeen, &v.Count, &v.Active, &v.Status, &v.DecidedBy, &v.DecidedAt, &v.Everywhere, &v.fix)
	if f := storedFix(v.fix); f != nil {
		v.Fix = describeFix(*f)
	}
	return v, err
}

// riskFilter narrows a list of risks.
type riskFilter struct {
	account  int64  // 0 = every account
	server   int64  // 0 = every server
	status   string // "" = every status
	severity string // the least severe to include; "" = all
	limit    int
}

func (p *Panel) risks(ctx context.Context, f riskFilter) ([]*riskView, error) {
	q := `SELECT ` + riskCols + ` FROM risks r JOIN servers s ON s.id = r.server_id WHERE s.deleted_at = 0`
	var args []any
	if f.account > 0 {
		q += ` AND r.account_id = ?`
		args = append(args, f.account)
	}
	if f.server > 0 {
		q += ` AND r.server_id = ?`
		args = append(args, f.server)
	}
	if f.status != "" {
		q += ` AND r.status = ?`
		args = append(args, f.status)
	}
	if i := sevRank(f.severity); i > 0 {
		q += ` AND r.severity IN ('` + strings.Join(severities[i:], "','") + `')`
	}
	q += riskOrder + ` LIMIT ?`
	args = append(args, clamp(f.limit, 1, 1000))
	rows, err := p.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*riskView{}
	for rows.Next() {
		v, err := scanRisk(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	return out, p.attachSteps(ctx, out)
}

func (p *Panel) riskByID(ctx context.Context, id int64) (*riskView, error) {
	return scanRisk(p.db.QueryRowContext(ctx, `SELECT `+riskCols+` FROM risks r JOIN servers s ON s.id = r.server_id WHERE r.id = ?`, id))
}

// ownRisk loads a risk of a server the caller may see.
func (p *Panel) ownRisk(ctx context.Context, a *Account, id int64) (*riskView, error) {
	v, err := p.riskByID(ctx, id)
	if err != nil {
		return nil, errNotFound
	}
	if _, err := p.ownServer(ctx, a, v.ServerID); err != nil {
		return nil, errNotFound
	}
	return v, nil
}

// ---------------------------------------------------------------- API

func (p *Panel) apiRisks(w http.ResponseWriter, r *http.Request, a *Account) error {
	q := r.URL.Query()
	f := riskFilter{account: scopeAccount(r, a), status: q.Get("status"), severity: q.Get("severity"), limit: 1000}
	switch f.status {
	case "", "open":
		f.status = "open"
	case "all":
		f.status = ""
	default:
		if !slices.Contains(riskStatuses, f.status) {
			return errStatus(http.StatusBadRequest, "status is open, acknowledged, expected or all")
		}
	}
	if f.severity != "" && sevRank(f.severity) < 0 {
		return errStatus(http.StatusBadRequest, "severity is info, warning, high or critical")
	}
	if s := q.Get("server"); s != "" {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return errStatus(http.StatusBadRequest, "server must be a server id")
		}
		if _, err := p.ownServer(r.Context(), a, id); err != nil {
			return err
		}
		f.server = id
	}
	list, err := p.risks(r.Context(), f)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, list)
	return nil
}

func (p *Panel) apiServerHealth(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	srv, err := p.ownServer(r.Context(), a, id)
	if err != nil {
		return err
	}
	var sh serverHealth
	err = p.db.QueryRowContext(r.Context(), `SELECT baseline_at, scanned_at FROM server_health WHERE server_id = ?`, srv.ID).
		Scan(&sh.BaselineAt, &sh.ScannedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if sh.Risks, err = p.risks(r.Context(), riskFilter{server: srv.ID, limit: 500}); err != nil {
		return err
	}
	for _, v := range sh.Risks {
		if v.Status == "open" {
			sh.Open++
			if sevRank(v.Severity) > sevRank(sh.Worst) {
				sh.Worst = v.Severity
			}
		}
	}
	writeJSON(w, http.StatusOK, sh)
	return nil
}

func (p *Panel) apiDecideRisk(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in riskDecision
	if err := readJSON(r, &in); err != nil {
		return err
	}
	v, err := p.ownRisk(r.Context(), a, id)
	if err != nil {
		return err
	}
	n, err := p.decideRisk(r.Context(), v, in.Decision, in.Scope, p.actorName(r, a), a.ID)
	if err != nil {
		return err
	}
	v, err = p.riskByID(r.Context(), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, riskDecided{Risk: v, Changed: n})
	return nil
}

// actorName says who made a change: the supervisor, through the browser or one of their API tokens.
func (p *Panel) actorName(r *http.Request, a *Account) string {
	name := nz(a.DisplayName, a.Username)
	if ai := authOf(r); ai.Token {
		var tok string
		_ = p.db.QueryRowContext(r.Context(), `SELECT name FROM api_tokens WHERE id = ?`, ai.TokenID).Scan(&tok)
		return name + " (API token " + nz(cleanName(tok, 40), "?") + ")"
	}
	return name
}

// decideRisk records the operator's decision about a risk - from the panel, the API or Telegram -
// and returns how many risks it changed. who says who decided; actor is the account (0 for none).
func (p *Panel) decideRisk(ctx context.Context, v *riskView, decision, scope, who string, actor int64) (int, error) {
	if !slices.Contains(riskStatuses, decision) {
		return 0, errStatus(http.StatusBadRequest, "decision is expected, acknowledged or open")
	}
	switch scope {
	case "":
		scope = "server"
	case "server", "all":
	default:
		return 0, errStatus(http.StatusBadRequest, "scope is server or all")
	}
	t := now()
	who = cleanName(who, 120)
	changed := 0
	err := p.db.Write(ctx, func(tx *sql.Tx) error {
		count := func(res sql.Result, err error) error {
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			changed += int(n)
			return nil
		}
		if decision == "expected" && scope == "all" {
			if _, err := tx.Exec(`INSERT INTO risk_rules (account_id, key, decided_by, decided_at) VALUES (?, ?, ?, ?)
				ON CONFLICT(account_id, key) DO UPDATE SET decided_by = excluded.decided_by, decided_at = excluded.decided_at`,
				v.AccountID, v.Key, who, t); err != nil {
				return err
			}
		}
		if decision != "expected" {
			// a key expected on every server would come back as expected: it is not any more
			if _, err := tx.Exec(`DELETE FROM risk_rules WHERE account_id = ? AND key = ?`, v.AccountID, v.Key); err != nil {
				return err
			}
		}
		if scope == "all" {
			return count(tx.Exec(`UPDATE risks SET status = ?, decided_by = ?, decided_at = ? WHERE account_id = ? AND key = ?
				AND (status != ? OR id = ?)`, decision, who, t, v.AccountID, v.Key, decision, v.ID))
		}
		return count(tx.Exec(`UPDATE risks SET status = ?, decided_by = ?, decided_at = ? WHERE id = ?`, decision, who, t, v.ID))
	})
	if err != nil {
		return 0, err
	}
	where := "on " + v.Server
	if scope == "all" {
		where = "on every server"
	}
	what := map[string]string{"expected": "marked as expected", "acknowledged": "acknowledged", "open": "opened again"}[decision]
	p.event(v.AccountID, "info", "risk_decided", v.ServerID, 0, actor, fmt.Sprintf("%s %s %s: %s", who, what, where, v.Title),
		map[string]any{"risk": v.ID, "decision": decision, "scope": scope})
	return changed, nil
}

// ---------------------------------------------------------------- reports

var riskKeyRE = regexp.MustCompile(`^[a-z0-9_-]{1,24}(:.{0,200})?$`)

// cleanFinding bounds and checks what an agent says it found; false: drop it. Unprintable
// characters in a key are replaced, not refused: a file named to slip past would otherwise do so.
func cleanFinding(f *proto.Finding) bool {
	f.Key = truncate(strings.Map(func(r rune) rune {
		if r == utf8.RuneError || unicode.IsControl(r) || (!unicode.IsPrint(r) && r != ' ') {
			return '?'
		}
		return r
	}, f.Key), 200)
	if !riskKeyRE.MatchString(f.Key) || !slices.Contains(findKinds, f.Kind) || sevRank(f.Severity) < 0 {
		return false
	}
	f.Title = cleanName(f.Title, 200)
	f.Detail = cleanNote(f.Detail, 1000)
	if f.Fix != nil && !cleanFix(f.Fix) {
		f.Fix = nil // something this panel would not send back to the agent
	}
	return f.Title != ""
}

// ingestHealth applies the health part of a report: new findings become risks (or happen again), and
// the lasting ones the agent no longer sees stop being active. Each finding counts once: the agent
// numbers them, and resends what was not acknowledged.
func (p *Panel) ingestHealth(ctx context.Context, srv *Server, h *proto.Health) {
	if h == nil || len(h.ID) > 64 {
		return
	}
	if len(h.Findings) > 500 {
		h.Findings = h.Findings[:500]
	}
	if len(h.Active) > 5000 {
		h.Active = h.Active[:5000]
	}
	sort.Slice(h.Findings, func(i, j int) bool { return h.Findings[i].Seq < h.Findings[j].Seq })
	t := now()
	var build *proto.Finding
	if f := p.agentBuildFinding(srv, h.Agent); f != nil {
		build = f
		h.Active = append(h.Active, f.Key)
	}
	if h.Active == nil {
		h.Active = []string{}
	}
	active, _ := json.Marshal(h.Active)
	err := p.db.Write(ctx, func(tx *sql.Tx) error {
		var hid string
		var seq int64
		if err := tx.QueryRow(`SELECT health_id, seq FROM server_health WHERE server_id = ?`, srv.ID).Scan(&hid, &seq); err != nil &&
			!errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if hid != h.ID {
			seq = 0 // a new baseline numbers its findings from 1 again
		}
		top := seq
		for _, f := range h.Findings {
			if f.Seq <= seq {
				continue // had it already
			}
			top = max(top, f.Seq)
			if !cleanFinding(&f) {
				continue
			}
			if f.At < t-30*86400 || f.At > t+300 {
				f.At = t // the server's clock is off
			}
			if err := p.applyFinding(tx, srv, f); err != nil {
				return err
			}
		}
		if build != nil {
			var on bool
			_ = tx.QueryRow(`SELECT active FROM risks WHERE server_id = ? AND key = ?`, srv.ID, build.Key).Scan(&on)
			if !on {
				build.At = t
				if err := p.applyFinding(tx, srv, *build); err != nil {
					return err
				}
			}
		}
		scanned := h.ScannedAt
		if scanned <= 0 || scanned > t+300 {
			scanned = t
		}
		if _, err := tx.Exec(`UPDATE risks SET active = CASE WHEN key IN (SELECT value FROM json_each(?)) THEN 1 ELSE 0 END,
			last_seen = CASE WHEN key IN (SELECT value FROM json_each(?)) AND last_seen < ? THEN ? ELSE last_seen END
			WHERE server_id = ?`, string(active), string(active), scanned, scanned, srv.ID); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO server_health (server_id, health_id, seq, baseline_at, scanned_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(server_id) DO UPDATE SET health_id = excluded.health_id, seq = excluded.seq,
			baseline_at = excluded.baseline_at, scanned_at = excluded.scanned_at`,
			srv.ID, h.ID, top, max(h.BaselineAt, 0), scanned)
		return err
	})
	logErr("health report", err)
}

// applyFinding records one finding: a new risk, or the same one again. Expected ones stay quiet;
// acknowledged ones open again (it happened again); open ones are told again at most once an hour,
// or when they got worse.
func (p *Panel) applyFinding(tx *sql.Tx, srv *Server, f proto.Finding) error {
	var rule sql.NullString
	_ = tx.QueryRow(`SELECT decided_by FROM risk_rules WHERE account_id = ? AND key = ?`, srv.AccountID, f.Key).Scan(&rule)
	var id, notified int64
	var status, sev, decidedBy string
	var decidedAt int64
	err := tx.QueryRow(`SELECT id, status, severity, notified_at, decided_by, decided_at FROM risks WHERE server_id = ? AND key = ?`,
		srv.ID, f.Key).Scan(&id, &status, &sev, &notified, &decidedBy, &decidedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		status, decidedBy, decidedAt = "open", "", 0
		if rule.Valid {
			status, decidedBy, decidedAt = "expected", rule.String, f.At
		}
		res, err := tx.Exec(`INSERT INTO risks (account_id, server_id, key, kind, severity, title, detail, first_seen, last_seen,
			count, active, status, decided_by, decided_at, fix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?)`,
			srv.AccountID, srv.ID, f.Key, f.Kind, f.Severity, f.Title, f.Detail, f.At, f.At, f.Lasting, status, decidedBy, decidedAt, fixJSON(f.Fix))
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		if status == "open" {
			return p.riskEvent(tx, srv, id, f)
		}
		return nil
	case err != nil:
		return err
	}
	was := status
	switch {
	case rule.Valid && status != "expected":
		status, decidedBy, decidedAt = "expected", rule.String, f.At // expected on every server
	case rule.Valid:
	case status == "acknowledged":
		status, decidedBy, decidedAt = "open", "", 0 // it happened again
	}
	if _, err := tx.Exec(`UPDATE risks SET kind = ?, severity = ?, title = ?, detail = ?, last_seen = MAX(last_seen, ?), count = count + 1,
		status = ?, decided_by = ?, decided_at = ?, fix = ? WHERE id = ?`,
		f.Kind, f.Severity, f.Title, f.Detail, f.At, status, decidedBy, decidedAt, fixJSON(f.Fix), id); err != nil {
		return err
	}
	if status == "open" && (was != "open" || now()-notified >= 3600 || sevRank(f.Severity) > sevRank(sev)) {
		return p.riskEvent(tx, srv, id, f)
	}
	return nil
}

// riskEvent puts a risk in the timeline - and so, for high and critical ones, in the notifications
// (the "health" group).
func (p *Panel) riskEvent(tx *sql.Tx, srv *Server, id int64, f proto.Finding) error {
	level := "info"
	switch f.Severity {
	case proto.SevCritical, proto.SevHigh:
		level = "crit"
	case proto.SevWarning:
		level = "warn"
	}
	data, _ := json.Marshal(map[string]any{"risk": id, "severity": f.Severity, "key": f.Key})
	if _, err := tx.Exec(`INSERT INTO events (ts, account_id, level, kind, server_id, message, data) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		now(), srv.AccountID, level, "risk_"+f.Severity, srv.ID, srv.Name+": "+f.Title, string(data)); err != nil {
		return err
	}
	_, err := tx.Exec(`UPDATE risks SET notified_at = ? WHERE id = ?`, now(), id)
	return err
}

// agentBuildFinding: an agent of this panel's version whose program differs from the panel's build
// was changed after it was installed. (Other versions cannot be compared.)
func (p *Panel) agentBuildFinding(srv *Server, sum string) *proto.Finding {
	if sum == "" || Version == "dev" || srv.AgentVersion != Version {
		return nil
	}
	want := p.agentSHA256()[srv.Arch]
	if want == "" || strings.EqualFold(want, sum) {
		return nil
	}
	return &proto.Finding{Key: "binary:agent-build", Kind: proto.FindBinary, Severity: proto.SevCritical, Lasting: true,
		Title: "The agent's program is not this panel's build",
		Detail: fmt.Sprintf("The agent says it is version %s, but its program (SHA-256 %s…) differs from the panel's build of that version (%s…): "+
			"it was changed after it was installed. Reinstall the agent with the command from the server page.", Version, short12(sum), short12(want))}
}

func short12(s string) string {
	s = cleanName(s, 64)
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// ---------------------------------------------------------------- the overview

// riskAlerts: one alert per server with open high or critical risks. Nothing acts on them.
func (p *Panel) riskAlerts(ctx context.Context, accountID int64) []alert {
	list, err := p.risks(ctx, riskFilter{account: accountID, status: "open", severity: proto.SevHigh, limit: 1000})
	if err != nil {
		return nil
	}
	type agg struct {
		first *riskView
		n     int
	}
	by := map[int64]*agg{}
	var order []int64
	for _, v := range list {
		a := by[v.ServerID]
		if a == nil {
			a = &agg{first: v}
			by[v.ServerID] = a
			order = append(order, v.ServerID)
		}
		a.n++
	}
	var out []alert
	for _, id := range order {
		a := by[id]
		level := "warn"
		if a.first.Severity == proto.SevCritical {
			level = "crit"
		}
		msg := fmt.Sprintf("%s: %s", a.first.Server, a.first.Title)
		if a.n > 1 {
			msg += fmt.Sprintf(" - and %d more open health risk(s)", a.n-1)
		}
		out = append(out, alert{Level: level, Kind: "health_risk", ServerID: id, Message: msg})
	}
	return out
}
