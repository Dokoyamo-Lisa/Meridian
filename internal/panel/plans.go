package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Preset plans: templates of what a user gets - a quota and what counts toward it, how long it
// lasts, when usage resets, device and speed limits, and which servers and protocols. Applying a plan
// copies its values into the user, who keeps them: changing a plan changes nobody unless the operator
// asks for its users to follow (update_users), and removing one leaves its users as they are.

// Plan is a preset plan.
type Plan struct {
	ID           int64   `json:"id"`
	AccountID    int64   `json:"-"`
	Name         string  `json:"name"`
	Note         string  `json:"note"`
	Quota        int64   `json:"quota" doc:"Bytes per cycle; 0 = unlimited"`
	CountMode    string  `json:"count_mode" doc:"What counts toward the quota: both, down, up or max (whichever is larger)"`
	Duration     int     `json:"duration" doc:"How long a user's period lasts, in duration_unit; 0 = no end"`
	DurationUnit string  `json:"duration_unit" doc:"day or month"`
	ResetDay     int     `json:"reset_day" doc:"Usage resets on this day of the month (1-31); 0 = see reset_every"`
	ResetEvery   int     `json:"reset_every" doc:"Usage resets every this many days from the user's start; 0 = on reset_day (never when that is 0 too)"`
	IPLimit      int     `json:"ip_limit" doc:"Devices (IPs) online at once; 0 = no limit"`
	DeviceMode   string  `json:"device_mode" doc:"'' = an alert when there are more; refuse = devices beyond the limit are turned away"`
	SpeedLimit   int     `json:"speed_limit" doc:"Mbps for the user's devices together on each server; 0 = no limit"`
	Scope        Scope   `json:"scope" doc:"What users get: whole servers and single protocols; both empty = everything"`
	Price        float64 `json:"price" doc:"For your records"`
	Currency     string  `json:"currency"`
	Sort         int     `json:"sort"`
	CreatedAt    int64   `json:"created_at"`
	UpdatedAt    int64   `json:"updated_at"`
	Users        int     `json:"users" doc:"Users it was last applied to"`
	// limits per protocol, given to its users (nodequota.go)
	NodeQuotas    NodeQuotas `json:"node_quotas" doc:"Limits per protocol: protocol id -> bytes per cycle"`
	NodeQuotaMode string     `json:"node_quota_mode" doc:"'' = an alert only when one is used up; stop = that protocol stops serving the user until the cycle starts over"`
}

const planCols = `id, account_id, name, note, quota, count_mode, duration, duration_unit, reset_day, reset_every, ip_limit,
device_mode, speed_limit, scope, price, currency, sort, created_at, updated_at, node_quotas, node_quota_mode`

func scanPlan(r interface{ Scan(...any) error }) (*Plan, error) {
	pl := &Plan{}
	var scope, nq string
	if err := r.Scan(&pl.ID, &pl.AccountID, &pl.Name, &pl.Note, &pl.Quota, &pl.CountMode, &pl.Duration, &pl.DurationUnit,
		&pl.ResetDay, &pl.ResetEvery, &pl.IPLimit, &pl.DeviceMode, &pl.SpeedLimit, &scope, &pl.Price, &pl.Currency, &pl.Sort,
		&pl.CreatedAt, &pl.UpdatedAt, &nq, &pl.NodeQuotaMode); err != nil {
		return nil, err
	}
	pl.NodeQuotas = parseNodeQuotas(nq)
	if scope != "" {
		_ = json.Unmarshal([]byte(scope), &pl.Scope)
	}
	return pl, nil
}

func (p *Panel) plansOf(ctx context.Context, acct int64) ([]*Plan, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT `+planCols+` FROM plans WHERE account_id = ? ORDER BY sort, id`, acct)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Plan{}
	for rows.Next() {
		pl, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, pl)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	counts := map[int64]int{}
	cr, err := p.db.QueryContext(ctx, `SELECT plan_id, COUNT(*) FROM subs WHERE account_id = ? AND plan_id > 0 GROUP BY plan_id`, acct)
	if err == nil {
		for cr.Next() {
			var id int64
			var n int
			if cr.Scan(&id, &n) == nil {
				counts[id] = n
			}
		}
		cr.Close()
	}
	for _, pl := range out {
		pl.Users = counts[pl.ID]
	}
	return out, nil
}

// ownPlan loads a plan of account a.
func (p *Panel) ownPlan(ctx context.Context, a *Account, id int64) (*Plan, error) {
	pl, err := scanPlan(p.db.QueryRowContext(ctx, `SELECT `+planCols+` FROM plans WHERE id = ?`, id))
	if err != nil || pl.AccountID != a.ID {
		return nil, errStatus(http.StatusNotFound, "there is no such plan")
	}
	return pl, nil
}

// ends is when a period that starts at start ends under the plan; 0 = never.
func (pl *Plan) ends(start int64) int64 {
	if pl.Duration <= 0 {
		return 0
	}
	t := time.Unix(start, 0)
	if pl.DurationUnit == "month" {
		return t.AddDate(0, pl.Duration, 0).Unix()
	}
	return t.AddDate(0, 0, pl.Duration).Unix()
}

// fill copies what the plan sets into a user request wherever the request leaves it out: what is
// given explicitly wins. start is the user's period start.
func (pl *Plan) fill(in *subInput, start int64) {
	set := func(dst **int64, v int64) {
		if *dst == nil {
			*dst = &v
		}
	}
	seti := func(dst **int, v int) {
		if *dst == nil {
			*dst = &v
		}
	}
	sets := func(dst **string, v string) {
		if *dst == nil {
			*dst = &v
		}
	}
	set(&in.Quota, pl.Quota)
	sets(&in.CountMode, pl.CountMode)
	seti(&in.ResetDay, pl.ResetDay)
	seti(&in.ResetEvery, pl.ResetEvery)
	seti(&in.IPLimit, pl.IPLimit)
	sets(&in.DeviceMode, pl.DeviceMode)
	seti(&in.SpeedLimit, pl.SpeedLimit)
	set(&in.StartsAt, start)
	set(&in.ExpiresAt, pl.ends(start))
	if in.NodeQuotas == nil {
		q := map[string]int64{}
		for id, v := range pl.NodeQuotas {
			q[strconv.FormatInt(id, 10)] = v
		}
		in.NodeQuotas = &q
	}
	sets(&in.NodeQuotaMode, pl.NodeQuotaMode)
	if in.Servers == nil && in.Nodes == nil {
		servers, nodes := append([]int64{}, pl.Scope.Servers...), append([]int64{}, pl.Scope.Nodes...)
		in.Servers, in.Nodes = &servers, &nodes
	}
}

// periodStart is when user s's current usage cycle began at time t (whose location is the panel's
// time zone): every reset_every days from the user's start, or on reset_day each month; 0 when usage
// never resets, or the user's period has not begun.
func periodStart(s *Sub, t time.Time) int64 {
	switch {
	case s.ResetEvery > 0:
		start := s.start()
		if start <= 0 || t.Unix() < start {
			return 0
		}
		step := int64(s.ResetEvery) * 86400
		return start + (t.Unix()-start)/step*step
	case s.ResetDay > 0:
		return cycleStart(s.ResetDay, t).Unix()
	}
	return 0
}

// nextPeriod is when user s's usage next resets after t; 0 = never.
func nextPeriod(s *Sub, t time.Time) int64 {
	switch {
	case s.ResetEvery > 0:
		start := s.start()
		if t.Unix() < start {
			return start
		}
		return periodStart(s, t) + int64(s.ResetEvery)*86400
	case s.ResetDay > 0:
		return nextReset(s.ResetDay, t).Unix()
	}
	return 0
}

// ---------------------------------------------------------------- checks

var countModes = []string{"both", "down", "up", "max"}

func checkCountMode(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		v = "both"
	}
	for _, m := range countModes {
		if v == m {
			return v, nil
		}
	}
	return "", errStatus(http.StatusBadRequest, "count_mode is both (upload and download), down (download only), up (upload only) or max (whichever is larger)")
}

func checkDeviceMode(v string) (string, error) {
	switch v = strings.ToLower(strings.TrimSpace(v)); v {
	case "", "alert":
		return "", nil
	case "refuse":
		return v, nil
	}
	return "", errStatus(http.StatusBadRequest, "device_mode is alert (only an alert, the default) or refuse (devices beyond the limit are turned away)")
}

// maxSpeed is the highest speed limit, in Mbps (100 Gbps): anything above means none.
const maxSpeed = 100_000

func checkLimits(quota *int64, resetDay, resetEvery, ipLimit, speed *int) error {
	switch {
	case quota != nil && *quota < 0:
		return errStatus(http.StatusBadRequest, "quota cannot be negative")
	case resetDay != nil && (*resetDay < 0 || *resetDay > 31):
		return errStatus(http.StatusBadRequest, "reset day must be 0 (never) or 1-31")
	case resetEvery != nil && (*resetEvery < 0 || *resetEvery > 3650):
		return errStatus(http.StatusBadRequest, "reset_every is a number of days between 1 and 3650, or 0 for monthly or never")
	case ipLimit != nil && (*ipLimit < 0 || *ipLimit > 10000):
		return errStatus(http.StatusBadRequest, "IP limit must be between 0 (none) and 10000")
	case speed != nil && (*speed < 0 || *speed > maxSpeed):
		return errStatus(http.StatusBadRequest, fmt.Sprintf("speed limit is in Mbps, between 1 and %d (100 Gbps), or 0 for none", maxSpeed))
	}
	return nil
}

// ---------------------------------------------------------------- API

type planInput struct {
	Name          *string           `json:"name" doc:"Required on create"`
	Note          *string           `json:"note"`
	Quota         *int64            `json:"quota" doc:"Bytes per cycle; 0 = unlimited"`
	CountMode     *string           `json:"count_mode" doc:"both, down, up or max"`
	Duration      *int              `json:"duration" doc:"How long a user's period lasts, in duration_unit; 0 = no end"`
	DurationUnit  *string           `json:"duration_unit" doc:"day (default) or month"`
	ResetDay      *int              `json:"reset_day" doc:"Day of the month usage resets (1-31); 0 = none"`
	ResetEvery    *int              `json:"reset_every" doc:"Usage resets every this many days from the user's start; 0 = by reset_day"`
	IPLimit       *int              `json:"ip_limit" doc:"Devices (IPs) online at once; 0 = no limit"`
	DeviceMode    *string           `json:"device_mode" doc:"alert (default) or refuse"`
	SpeedLimit    *int              `json:"speed_limit" doc:"Mbps; 0 = no limit"`
	Servers       *[]int64          `json:"servers" doc:"Whole servers users get. Empty servers and empty protocols = everything"`
	Nodes         *[]int64          `json:"protocols" doc:"Single protocols users get, besides whole servers"`
	Price         *float64          `json:"price" doc:"For your records"`
	Currency      *string           `json:"currency"`
	Sort          *int              `json:"sort"`
	UpdateUsers   bool              `json:"update_users" doc:"On change: users on this plan take its new quota, count mode, reset, limits and access too (their start and end dates stay)"`
	NodeQuotas    *map[string]int64 `json:"node_quotas" doc:"Limits per protocol: protocol id -> bytes per cycle (0 takes one away); the whole set is replaced"`
	NodeQuotaMode *string           `json:"node_quota_mode" doc:"alert (default: an alert only when one is used up) or stop (that protocol stops serving the user until the cycle starts over)"`
}

type plansView struct {
	Plans []*Plan `json:"plans"`
}

type planApplyInput struct {
	PlanID     int64  `json:"plan_id" doc:"The plan to apply"`
	StartsAt   *int64 `json:"starts_at" doc:"When the new period starts (Unix seconds); default now. The end is the start plus the plan's duration"`
	ResetUsage *bool  `json:"reset_usage" doc:"Start the new period with usage at zero (default true)"`
}

func (p *Panel) apiPlans(w http.ResponseWriter, r *http.Request, a *Account) error {
	list, err := p.plansOf(r.Context(), a.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, plansView{Plans: list})
	return nil
}

// planFrom applies a request to a plan (pl is changed in place).
func (p *Panel) planFrom(ctx context.Context, a *Account, in *planInput, pl *Plan) error {
	if in.Name != nil {
		if pl.Name = cleanName(*in.Name, 64); pl.Name == "" {
			return errStatus(http.StatusBadRequest, "give the plan a name (up to 64 characters)")
		}
	}
	if in.Note != nil {
		pl.Note = cleanNote(*in.Note, 2000)
	}
	if err := checkLimits(in.Quota, in.ResetDay, in.ResetEvery, in.IPLimit, in.SpeedLimit); err != nil {
		return err
	}
	if in.Quota != nil {
		pl.Quota = *in.Quota
	}
	if in.CountMode != nil {
		m, err := checkCountMode(*in.CountMode)
		if err != nil {
			return err
		}
		pl.CountMode = m
	}
	if in.DurationUnit != nil {
		switch u := strings.ToLower(strings.TrimSpace(*in.DurationUnit)); u {
		case "", "day", "days":
			pl.DurationUnit = "day"
		case "month", "months":
			pl.DurationUnit = "month"
		default:
			return errStatus(http.StatusBadRequest, "duration_unit is day or month")
		}
	}
	if in.Duration != nil {
		limit := 3650
		if pl.DurationUnit == "month" {
			limit = 120
		}
		if *in.Duration < 0 || *in.Duration > limit {
			return errStatus(http.StatusBadRequest, fmt.Sprintf("duration is 0 (no end) to %d %ss", limit, pl.DurationUnit))
		}
		pl.Duration = *in.Duration
	}
	if in.ResetDay != nil {
		pl.ResetDay = *in.ResetDay
	}
	if in.ResetEvery != nil {
		pl.ResetEvery = *in.ResetEvery
	}
	if in.IPLimit != nil {
		pl.IPLimit = *in.IPLimit
	}
	if in.DeviceMode != nil {
		m, err := checkDeviceMode(*in.DeviceMode)
		if err != nil {
			return err
		}
		pl.DeviceMode = m
	}
	if in.SpeedLimit != nil {
		pl.SpeedLimit = *in.SpeedLimit
	}
	if in.NodeQuotas != nil {
		q, err := p.nodeQuotasFrom(ctx, a.ID, *in.NodeQuotas, pl.NodeQuotas)
		if err != nil {
			return err
		}
		pl.NodeQuotas = q
	}
	if in.NodeQuotaMode != nil {
		m, err := checkNodeQuotaMode(*in.NodeQuotaMode)
		if err != nil {
			return err
		}
		pl.NodeQuotaMode = m
	}
	if in.Servers != nil || in.Nodes != nil {
		sc, err := p.scopeFrom(ctx, a.ID, &subInput{Servers: in.Servers, Nodes: in.Nodes}, pl.Scope)
		if err != nil {
			return err
		}
		pl.Scope = sc
	}
	if in.Price != nil {
		if *in.Price < 0 || *in.Price > 1e9 || math.IsNaN(*in.Price) {
			return errStatus(http.StatusBadRequest, "price must be between 0 and 1000000000")
		}
		pl.Price = *in.Price
	}
	if in.Currency != nil {
		pl.Currency = strings.ToUpper(cleanName(*in.Currency, 8))
	}
	if in.Sort != nil {
		pl.Sort = *in.Sort
	}
	return nil
}

func (p *Panel) apiCreatePlan(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in planInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Name == nil {
		return errStatus(http.StatusBadRequest, "give the plan a name")
	}
	var n int
	p.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM plans WHERE account_id = ?`, a.ID).Scan(&n)
	if n >= 200 {
		return errStatus(http.StatusBadRequest, "there are 200 plans already - remove some first")
	}
	pl := &Plan{AccountID: a.ID, CountMode: "both", DurationUnit: "day"}
	if err := p.planFrom(r.Context(), a, &in, pl); err != nil {
		return err
	}
	t := now()
	res, err := p.db.Exec1(`INSERT INTO plans (account_id, name, note, quota, count_mode, duration, duration_unit, reset_day,
		reset_every, ip_limit, device_mode, speed_limit, scope, price, currency, sort, created_at, updated_at, node_quotas, node_quota_mode)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, a.ID, pl.Name, pl.Note, pl.Quota, pl.CountMode,
		pl.Duration, pl.DurationUnit, pl.ResetDay, pl.ResetEvery, pl.IPLimit, pl.DeviceMode, pl.SpeedLimit, pl.Scope.String(),
		pl.Price, pl.Currency, pl.Sort, t, t, pl.NodeQuotas.String(), pl.NodeQuotaMode)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	p.event(a.ID, "info", "plan_created", 0, 0, a.ID, fmt.Sprintf("%s added the plan %s", a.Username, pl.Name), nil)
	out, err := p.ownPlan(r.Context(), a, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, out)
	return nil
}

func (p *Panel) apiUpdatePlan(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	pl, err := p.ownPlan(r.Context(), a, id)
	if err != nil {
		return err
	}
	var in planInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := p.planFrom(r.Context(), a, &in, pl); err != nil {
		return err
	}
	pl.UpdatedAt = now()
	var moved int64
	err = p.db.Write(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE plans SET name = ?, note = ?, quota = ?, count_mode = ?, duration = ?, duration_unit = ?,
			reset_day = ?, reset_every = ?, ip_limit = ?, device_mode = ?, speed_limit = ?, scope = ?, price = ?, currency = ?,
			sort = ?, updated_at = ?, node_quotas = ?, node_quota_mode = ? WHERE id = ?`, pl.Name, pl.Note, pl.Quota, pl.CountMode, pl.Duration, pl.DurationUnit,
			pl.ResetDay, pl.ResetEvery, pl.IPLimit, pl.DeviceMode, pl.SpeedLimit, pl.Scope.String(), pl.Price, pl.Currency,
			pl.Sort, pl.UpdatedAt, pl.NodeQuotas.String(), pl.NodeQuotaMode, pl.ID); err != nil {
			return err
		}
		if !in.UpdateUsers {
			return nil
		}
		res, err := tx.Exec(`UPDATE subs SET quota = ?, count_mode = ?, reset_day = ?, reset_every = ?, ip_limit = ?,
			device_mode = ?, speed_limit = ?, scope = ?, updated_at = ?, node_quotas = ?, node_quota_mode = ? WHERE plan_id = ? AND account_id = ?`,
			pl.Quota, pl.CountMode, pl.ResetDay, pl.ResetEvery, pl.IPLimit, pl.DeviceMode, pl.SpeedLimit, pl.Scope.String(),
			pl.UpdatedAt, pl.NodeQuotas.String(), pl.NodeQuotaMode, pl.ID, a.ID)
		if err != nil {
			return err
		}
		moved, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("%s changed the plan %s", a.Username, pl.Name)
	if moved > 0 {
		msg += fmt.Sprintf(" and its %d user(s) with it", moved)
		p.realignCycles(r.Context(), a.ID)
		p.markOutAll(r.Context(), a.ID) // their new quotas (outofdata.go)
		p.touchAccount(a.ID)
	}
	p.event(a.ID, "info", "plan_changed", 0, 0, a.ID, msg, nil)
	out, err := p.ownPlan(r.Context(), a, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (p *Panel) apiDeletePlan(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	pl, err := p.ownPlan(r.Context(), a, id)
	if err != nil {
		return err
	}
	err = p.db.Write(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE subs SET plan_id = 0 WHERE plan_id = ?`, pl.ID); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM plans WHERE id = ?`, pl.ID)
		return err
	})
	if err != nil {
		return err
	}
	p.event(a.ID, "info", "plan_removed", 0, 0, a.ID, fmt.Sprintf("%s removed the plan %s - its users keep what it gave them", a.Username, pl.Name), nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}

// apiApplyPlan starts a new period for a user under a plan: its quota, count mode, reset, limits and
// access, from the start given (default now) to the start plus the plan's duration.
func (p *Panel) apiApplyPlan(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownSub(r.Context(), a, id)
	if err != nil {
		return err
	}
	var in planApplyInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	pl, err := p.ownPlan(r.Context(), a, in.PlanID)
	if err != nil {
		return err
	}
	t := now()
	start := t
	if in.StartsAt != nil && *in.StartsAt > 0 {
		start = *in.StartsAt
	}
	reset := in.ResetUsage == nil || *in.ResetUsage
	s.Quota, s.CountMode, s.ResetDay, s.ResetEvery = pl.Quota, pl.CountMode, pl.ResetDay, pl.ResetEvery
	s.IPLimit, s.DeviceMode, s.SpeedLimit, s.Scope, s.PlanID = pl.IPLimit, pl.DeviceMode, pl.SpeedLimit, pl.Scope, pl.ID
	s.NodeQuotas, s.NodeQuotaMode = pl.NodeQuotas, pl.NodeQuotaMode
	s.StartsAt, s.ExpiresAt, s.UpdatedAt = start, pl.ends(start), t
	cycle := periodStart(s, p.localNow())
	if cycle == 0 {
		cycle = min(start, t)
	}
	err = p.db.Write(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE subs SET quota = ?, count_mode = ?, reset_day = ?, reset_every = ?, ip_limit = ?,
			device_mode = ?, speed_limit = ?, scope = ?, plan_id = ?, starts_at = ?, expires_at = ?, cycle_start = ?, updated_at = ?,
			node_quotas = ?, node_quota_mode = ? WHERE id = ?`, s.Quota, s.CountMode, s.ResetDay, s.ResetEvery, s.IPLimit, s.DeviceMode, s.SpeedLimit, s.Scope.String(),
			s.PlanID, s.StartsAt, s.ExpiresAt, cycle, s.UpdatedAt, s.NodeQuotas.String(), s.NodeQuotaMode, s.ID); err != nil {
			return err
		}
		if !reset {
			return nil
		}
		if _, err := tx.Exec(`UPDATE subs SET cycle_up = 0, cycle_down = 0 WHERE id = ?`, s.ID); err != nil {
			return err
		}
		return resetNodeUsage(tx, s.ID)
	})
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("%s applied the plan %s to %s", a.Username, pl.Name, s.Name)
	if s.ExpiresAt > 0 {
		msg += " until " + time.Unix(s.ExpiresAt, 0).In(p.loc()).Format("2 Jan 2006")
	}
	p.event(a.ID, "info", "plan_applied", 0, s.ID, a.ID, msg, nil)
	if fresh, err := p.subByID(r.Context(), s.ID); err == nil {
		p.markOut(r.Context(), fresh) // the new period's data, or the old usage on a smaller quota (outofdata.go)
	}
	p.touchAccount(a.ID)
	s, err = p.subByID(r.Context(), s.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, p.subView(r, s, nil, false))
	return nil
}

// localNow is now in the panel's time zone.
func (p *Panel) localNow() time.Time { return time.Now().In(p.loc()) }

// realignCycles moves the cycle start of the account's users whose reset settings say their current
// cycle began later than recorded: after a change of those settings, the next reset follows the new
// schedule while the usage counted so far stays.
func (p *Panel) realignCycles(ctx context.Context, acct int64) {
	subs, err := p.subsOf(ctx, acct)
	if err != nil {
		return
	}
	t := p.localNow()
	for _, s := range subs {
		if cs := periodStart(s, t); cs > s.CycleStart {
			_, err := p.db.Exec1(`UPDATE subs SET cycle_start = ? WHERE id = ?`, cs, s.ID)
			logErr("realign cycle", err)
		}
	}
}
