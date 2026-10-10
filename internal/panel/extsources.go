package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"meridian/internal/subgen"
)

// Subscription links: a provider's subscription address the panel reads again on a schedule. Its
// nodes become external nodes that stay as the provider has them - added, changed and taken away
// on each refresh - so they can be used directly: as the exit of a proxy pass or a traffic rule, in
// a load balancer (one member stands for every node of the subscription), and, when the operator
// says so, in users' own subscriptions, written out by Meridian for each user's app like its own
// protocols. Whatever form the provider sends - share links, base64, Clash or sing-box - is read
// into Meridian's own description of each node, so every app gets them in the form it understands.
//
// A failed refresh changes nothing: the nodes stay as they were and the source shows why. A refresh
// that finds no usable node at all is a failure too - a provider's error page never empties the
// list. A node that leaves the subscription while a protocol, rule or load balancer still names it
// is kept (marked as gone from the subscription) until the operator points that traffic elsewhere.

type ExtSource struct {
	ID         int64
	AccountID  int64
	Name       string
	URL        string
	Client     string
	EveryHours int
	Enabled    bool
	Offer      bool
	OfferTo    offerTo
	Prefix     string
	Include    string
	Exclude    string
	Note       string
	FetchedAt  int64
	OkAt       int64
	Error      string
	Skipped    []subgen.ParseError
	Usage      *sourceUsage
	CreatedAt  int64
	UpdatedAt  int64
}

// offerTo says which users get a subscription's nodes: these users and the users on these plans;
// both empty = every user.
type offerTo struct {
	Users []int64 `json:"users,omitempty" doc:"These users"`
	Plans []int64 `json:"plans,omitempty" doc:"And the users on these plans (the plan they were last given)"`
}

func (o offerTo) all() bool { return len(o.Users) == 0 && len(o.Plans) == 0 }

func (o offerTo) has(s *Sub) bool {
	return o.all() || slices.Contains(o.Users, s.ID) || (s.PlanID > 0 && slices.Contains(o.Plans, s.PlanID))
}

// sourceUsage is what the provider says about the subscription itself (its Subscription-Userinfo).
type sourceUsage struct {
	Upload   int64 `json:"upload"`
	Download int64 `json:"download"`
	Total    int64 `json:"total" doc:"0 = no limit given"`
	Expire   int64 `json:"expire" doc:"Unix seconds; 0 = none given"`
}

// sourceClients are the apps a source can ask as: providers often answer each app in its own form.
var sourceClients = map[string]string{
	"":        "Meridian/" + Version,
	"clash":   "clash.meta",
	"singbox": "sing-box",
	"v2rayn":  "v2rayN/7.0",
}

const (
	maxSources      = 50
	sourceSkipKept  = 50 // reasons kept from the last refresh
	sourceRetry     = time.Hour
	sourceMissingTo = 30 * 24 * time.Hour // a gone node still named somewhere is kept this long at most... while unused it goes at once
)

const sourceCols = `id, account_id, name, url, client, every_hours, enabled, offer, offer_to, prefix, include, exclude, note,
fetched_at, ok_at, error, skipped, usage, created_at, updated_at`

func scanSource(r interface{ Scan(...any) error }) (*ExtSource, error) {
	s := &ExtSource{}
	var to, skipped, usage string
	if err := r.Scan(&s.ID, &s.AccountID, &s.Name, &s.URL, &s.Client, &s.EveryHours, &s.Enabled, &s.Offer, &to, &s.Prefix,
		&s.Include, &s.Exclude, &s.Note, &s.FetchedAt, &s.OkAt, &s.Error, &skipped, &usage, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	if to != "" {
		_ = json.Unmarshal([]byte(to), &s.OfferTo)
	}
	if skipped != "" {
		_ = json.Unmarshal([]byte(skipped), &s.Skipped)
	}
	if usage != "" {
		s.Usage = &sourceUsage{}
		if json.Unmarshal([]byte(usage), s.Usage) != nil {
			s.Usage = nil
		}
	}
	return s, nil
}

func (p *Panel) sourceByID(ctx context.Context, id int64) (*ExtSource, error) {
	return scanSource(p.db.QueryRowContext(ctx, `SELECT `+sourceCols+` FROM ext_sources WHERE id = ?`, id))
}

// sourcesOf lists an account's subscription links (0 = every account).
func (p *Panel) sourcesOf(ctx context.Context, acct int64) ([]*ExtSource, error) {
	q, args := `SELECT `+sourceCols+` FROM ext_sources`, []any{}
	if acct > 0 {
		q += ` WHERE account_id = ?`
		args = append(args, acct)
	}
	rows, err := p.db.QueryContext(ctx, q+` ORDER BY name COLLATE NOCASE, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ExtSource
	for rows.Next() {
		s, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *Panel) ownSource(ctx context.Context, a *Account, id int64) (*ExtSource, error) {
	s, err := p.sourceByID(ctx, id)
	if err != nil || (!a.IsOwner() && s.AccountID != a.ID) {
		return nil, errNotFound
	}
	return s, nil
}

// due says whether a source is read again now: its interval after a good refresh, an hour (or its
// interval, if shorter) after a failed one.
func (s *ExtSource) due(t time.Time) bool {
	if !s.Enabled || s.EveryHours <= 0 {
		return false
	}
	every := time.Duration(s.EveryHours) * time.Hour
	if s.Error != "" && every > sourceRetry {
		every = sourceRetry
	}
	return t.Unix() >= s.FetchedAt+int64(every/time.Second)
}

// keywords splits a filter into its words: separated by commas or |, compared without case.
func keywords(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '|' || r == '\n' }) {
		if w = strings.ToLower(strings.TrimSpace(w)); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// keep says whether a node passes a source's filters: its name holds one of the include words (when
// there are any) and none of the exclude words.
func (s *ExtSource) keep(name string) bool {
	n := strings.ToLower(name)
	has := func(words []string) bool {
		return slices.ContainsFunc(words, func(w string) bool { return strings.Contains(n, w) })
	}
	inc := keywords(s.Include)
	return (len(inc) == 0 || has(inc)) && !has(keywords(s.Exclude))
}

// parseUserinfo reads a Subscription-Userinfo header: upload=1; download=2; total=3; expire=4.
func parseUserinfo(h string) *sourceUsage {
	if h == "" {
		return nil
	}
	u := &sourceUsage{}
	found := false
	for _, part := range strings.Split(h, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64) // some providers write 1.0E10
		if err != nil || n < 0 || n > 1<<62 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "upload":
			u.Upload, found = int64(n), true
		case "download":
			u.Download, found = int64(n), true
		case "total":
			u.Total, found = int64(n), true
		case "expire":
			u.Expire, found = int64(n), true
		}
	}
	if !found {
		return nil
	}
	return u
}

// fetchSource reads a provider's subscription as the source's app would.
var fetchSource = func(ctx context.Context, rawURL, client string) (string, *sourceUsage, error) {
	u, err := checkFetchURL(rawURL)
	if err != nil {
		return "", nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", nil, errStatus(http.StatusBadRequest, "the address cannot be used")
	}
	req.Header.Set("User-Agent", sourceClients[client])
	resp, err := publicClient().Do(req)
	if err != nil {
		return "", nil, errStatus(http.StatusBadGateway, "could not fetch it: "+plainNetErr(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, errStatus(http.StatusBadGateway, fmt.Sprintf("the address answered %d - check it in a browser", resp.StatusCode))
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, fetchLimit+1))
	if err != nil {
		return "", nil, errStatus(http.StatusBadGateway, "could not read the answer: "+plainNetErr(err))
	}
	if len(b) > fetchLimit {
		return "", nil, errStatus(http.StatusBadGateway, "the answer is larger than 4 MB")
	}
	return string(b), parseUserinfo(resp.Header.Get("Subscription-Userinfo")), nil
}

// sourceResult is what one refresh did.
type sourceResult struct {
	Added   int                 `json:"added"`
	Changed int                 `json:"changed"`
	Removed int                 `json:"removed" doc:"Nodes that left the subscription and were removed (nothing named them)"`
	Kept    []string            `json:"kept" doc:"Nodes that left the subscription but are kept: a protocol, rule or load balancer still names them"`
	Skipped []subgen.ParseError `json:"skipped" doc:"Entries that could not be used, each with the reason"`
}

func (r sourceResult) changed() bool { return r.Added+r.Changed+r.Removed > 0 }

func (r sourceResult) summary() string {
	var parts []string
	if r.Added > 0 {
		parts = append(parts, countOf(r.Added, "new node", "new nodes"))
	}
	if r.Changed > 0 {
		parts = append(parts, fmt.Sprintf("%d changed", r.Changed))
	}
	if r.Removed > 0 {
		parts = append(parts, fmt.Sprintf("%d gone", r.Removed))
	}
	if len(parts) == 0 {
		return "no changes"
	}
	return strings.Join(parts, ", ")
}

// refreshSource reads a source again and brings its nodes in line. by names who asked ("" = the
// schedule). Only one refresh of a source runs at a time.
func (p *Panel) refreshSource(ctx context.Context, src *ExtSource, by string) (sourceResult, error) {
	p.sourceMu.Lock()
	defer p.sourceMu.Unlock()
	res := sourceResult{Kept: []string{}, Skipped: []subgen.ParseError{}}
	fail := func(err error) (sourceResult, error) {
		msg := errText(err)
		_, _ = p.db.Exec1(`UPDATE ext_sources SET fetched_at = ?, error = ? WHERE id = ?`, now(), msg, src.ID)
		if src.Error == "" { // tell once when it starts failing, not at every try
			p.event(src.AccountID, "warn", "ext_source_failed", 0, 0, 0, fmt.Sprintf("The subscription link %s could not be read: %s. Its nodes stay as they were.", src.Name, msg), nil)
		}
		src.Error = msg
		return res, err
	}
	text, usage, err := fetchSource(ctx, src.URL, src.Client)
	if err != nil {
		return fail(err)
	}
	eps, skipped := subgen.ParseNodes(text)
	res.Skipped = append(res.Skipped, skipped...)
	var kept []subgen.Endpoint
	for _, e := range eps {
		if src.keep(e.Name) {
			kept = append(kept, e)
		}
	}
	if len(kept) == 0 {
		why := "it has no node Rosélune can use"
		if len(eps) > 0 {
			why = "no node passes its filters"
		} else if len(skipped) > 0 {
			why += " (" + skipped[0].Reason + ")"
		}
		return fail(errStatus(http.StatusBadGateway, why))
	}
	uses := p.extUsesOf(ctx, src.AccountID)
	all, err := p.extNodesOf(ctx, src.AccountID)
	if err != nil {
		return fail(err)
	}
	var mine []*ExtNode
	taken := map[string]bool{}
	for _, x := range all {
		if x.SourceID == src.ID {
			mine = append(mine, x)
		} else {
			taken[strings.ToLower(x.Name)] = true
		}
	}
	t := now()
	want := func(e subgen.Endpoint) string {
		return nz(cleanName(joinPrefix(src.Prefix, e.Name), 60), "External node")
	}
	// pair each listed node with the node it was: the same name from the provider, else the same
	// address and credentials under a new name (names first, so a renamed node cannot take another's place)
	type pair struct {
		e subgen.Endpoint
		x *ExtNode
	}
	var pairs []pair
	matched := map[int64]bool{}
	var seen []subgen.Endpoint
	for _, e := range kept {
		if slices.ContainsFunc(seen, func(o subgen.Endpoint) bool { return sameEndpoint(o, e) }) {
			res.Skipped = append(res.Skipped, subgen.ParseError{Name: e.Name, Reason: "the same node is listed twice"})
			continue
		}
		seen = append(seen, e)
		var x *ExtNode
		for _, m := range mine {
			if !matched[m.ID] && m.SourceKey == e.Name && m.Kind == e.Kind {
				x = m
				break
			}
		}
		if x != nil {
			matched[x.ID] = true
		}
		pairs = append(pairs, pair{e, x})
	}
	for i := range pairs {
		if pairs[i].x != nil {
			continue
		}
		for _, m := range mine {
			if !matched[m.ID] && sameEndpoint(m.Endpoint, pairs[i].e) {
				pairs[i].x, matched[m.ID] = m, true
				break
			}
		}
	}
	// nodes gone from the subscription: kept while something names them, else removed
	var gone, stay []*ExtNode
	for _, x := range mine {
		if matched[x.ID] {
			continue
		}
		if len(uses[x.ID]) > 0 && time.Since(time.Unix(nz64(x.MissingSince, t), 0)) < sourceMissingTo {
			stay = append(stay, x)
			taken[strings.ToLower(x.Name)] = true
		} else {
			gone = append(gone, x)
		}
	}
	// names that stay as they are are reserved first; new and renamed nodes get what is left
	for _, pr := range pairs {
		if pr.x != nil && sameBase(pr.x.Name, want(pr.e)) {
			taken[strings.ToLower(pr.x.Name)] = true
		}
	}
	room := maxExtNodes - (len(all) - len(mine)) - len(matched) - len(stay)
	err = p.db.Write(ctx, func(tx *sql.Tx) error {
		for _, pr := range pairs {
			e, x, key, name := pr.e, pr.x, pr.e.Name, want(pr.e)
			if x == nil {
				if room <= 0 {
					res.Skipped = append(res.Skipped, subgen.ParseError{Name: e.Name, Reason: fmt.Sprintf("at most %d external nodes - remove some first", maxExtNodes)})
					continue
				}
				room--
				e.Name = uniqueExtName(name, taken)
				taken[strings.ToLower(e.Name)] = true
				ep, _ := json.Marshal(e)
				if _, err := tx.Exec(`INSERT INTO ext_nodes (id, account_id, name, kind, endpoint, enabled, source_id, source_key, created_at, updated_at)
					VALUES (`+nextExtID+`, ?, ?, ?, ?, 1, ?, ?, ?, ?)`, src.AccountID, e.Name, e.Kind, string(ep), src.ID, key, t, t); err != nil {
					return err
				}
				res.Added++
				continue
			}
			e.Name = x.Name
			if !sameBase(x.Name, name) { // renamed by the provider, or a new prefix
				e.Name = uniqueExtName(name, taken)
				taken[strings.ToLower(e.Name)] = true
			}
			moved := !sameEndpoint(x.Endpoint, e)
			if !moved && x.Name == e.Name && x.SourceKey == key && x.Kind == e.Kind && x.MissingSince == 0 {
				continue
			}
			ep, _ := json.Marshal(e)
			if _, err := tx.Exec(`UPDATE ext_nodes SET name = ?, kind = ?, endpoint = ?, source_key = ?, missing_since = 0, updated_at = ? WHERE id = ?`,
				e.Name, e.Kind, string(ep), key, t, x.ID); err != nil {
				return err
			}
			if moved || x.MissingSince > 0 {
				res.Changed++
			}
		}
		for _, x := range stay {
			res.Kept = append(res.Kept, x.Name)
			if x.MissingSince == 0 {
				if _, err := tx.Exec(`UPDATE ext_nodes SET missing_since = ? WHERE id = ?`, t, x.ID); err != nil {
					return err
				}
			}
		}
		for _, x := range gone {
			if _, err := tx.Exec(`DELETE FROM ext_nodes WHERE id = ?`, x.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO id_floor (name, last) VALUES ('ext_nodes', ?)
				ON CONFLICT(name) DO UPDATE SET last = MAX(last, excluded.last)`, x.ID); err != nil {
				return err
			}
			if err := pruneRoutes(tx, x.AccountID, nil, nil, fmt.Sprintf("ext:%d", x.ID)); err != nil {
				return err
			}
			res.Removed++
		}
		if len(res.Skipped) > sourceSkipKept {
			res.Skipped = append(res.Skipped[:sourceSkipKept], subgen.ParseError{Reason: fmt.Sprintf("and %d more left out", len(res.Skipped)-sourceSkipKept)})
		}
		sk, _ := json.Marshal(res.Skipped)
		us := ""
		if usage != nil {
			b, _ := json.Marshal(usage)
			us = string(b)
		}
		_, err := tx.Exec(`UPDATE ext_sources SET fetched_at = ?, ok_at = ?, error = '', skipped = ?, usage = ? WHERE id = ?`, t, t, string(sk), us, src.ID)
		return err
	})
	if err != nil {
		res = sourceResult{Kept: []string{}, Skipped: res.Skipped}
		return fail(err)
	}
	if src.Error != "" {
		p.event(src.AccountID, "info", "ext_source_ok", 0, 0, 0, fmt.Sprintf("The subscription link %s can be read again (%s)", src.Name, res.summary()), nil)
	} else if res.changed() {
		who := "Refreshed"
		if by != "" {
			who = by + " refreshed"
		}
		p.event(src.AccountID, "info", "ext_source_refreshed", 0, 0, 0, fmt.Sprintf("%s the subscription link %s: %s", who, src.Name, res.summary()), nil)
	}
	src.Error, src.FetchedAt, src.OkAt = "", t, t
	if res.changed() {
		p.touchAccount(src.AccountID) // servers whose exits changed get them at once; the rest stay as they are
	}
	return res, nil
}

// joinPrefix puts a source's prefix in front of a node's name.
func joinPrefix(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + " " + name
}

// sameBase says whether name is want, or want as uniqueExtName numbered it (and maybe shortened it).
func sameBase(name, want string) bool {
	if name == want {
		return true
	}
	i := strings.LastIndexByte(name, ' ')
	if i < 0 {
		return false
	}
	if _, err := strconv.Atoi(name[i+1:]); err != nil {
		return false
	}
	base := name[:i]
	return base == want || (len([]rune(base)) >= 50 && strings.HasPrefix(want, base))
}

func nz64(v, def int64) int64 {
	if v == 0 {
		return def
	}
	return v
}

// sourcesJob reads again the subscription links that are due, one after another (a run still going
// when the next is due makes that one wait its turn).
func (p *Panel) sourcesJob(ctx context.Context) {
	if !p.sourcesBusy.CompareAndSwap(false, true) {
		return
	}
	defer p.sourcesBusy.Store(false)
	list, err := p.sourcesOf(ctx, 0)
	if err != nil {
		return
	}
	for _, s := range list {
		if ctx.Err() != nil || !s.due(time.Now()) {
			continue
		}
		if s, err = p.sourceByID(ctx, s.ID); err != nil || !s.due(time.Now()) { // refreshed by hand meanwhile, or removed
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		if _, err := p.refreshSource(rctx, s, ""); err != nil {
			slog.Info("subscription link", "source", s.ID, "err", errText(err))
		}
		cancel()
	}
}

// offeredEndpoints are the nodes of the subscription links given to a user, as their apps list them.
// WireGuard nodes are never given out: one key cannot serve many devices.
func (p *Panel) offeredEndpoints(ctx context.Context, sub *Sub) []subgen.Endpoint {
	list, err := p.sourcesOf(ctx, sub.AccountID)
	if err != nil {
		return nil
	}
	var out []subgen.Endpoint
	for _, src := range list {
		if !src.Enabled || !src.Offer || !src.OfferTo.has(sub) {
			continue
		}
		rows, err := p.db.QueryContext(ctx, `SELECT `+extCols+` FROM ext_nodes WHERE source_id = ? AND enabled = 1 AND missing_since = 0 ORDER BY id`, src.ID)
		if err != nil {
			continue
		}
		for rows.Next() {
			x, err := scanExt(rows)
			if err != nil || x.Kind == subgen.KindWireGuard {
				continue
			}
			e := x.Endpoint
			e.Name, e.NodeID = x.Name, 0
			out = append(out, e)
		}
		rows.Close()
	}
	return out
}

// sourceNodes are the ext:<id> exits a src:<id> load balancer member stands for: the subscription's
// nodes that are on and still in it.
func (p *Panel) sourceNodes(ctx context.Context, acct, id int64) ([]string, string) {
	src, err := p.sourceByID(ctx, id)
	switch {
	case err != nil || src.AccountID != acct:
		return nil, "its subscription link was removed"
	case !src.Enabled:
		return nil, fmt.Sprintf("its subscription link (%s) is turned off", src.Name)
	}
	rows, err := p.db.QueryContext(ctx, `SELECT id FROM ext_nodes WHERE source_id = ? AND enabled = 1 AND missing_since = 0 ORDER BY id`, id)
	if err != nil {
		return nil, "its nodes cannot be read"
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n int64
		if rows.Scan(&n) == nil {
			out = append(out, fmt.Sprintf("ext:%d", n))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Sprintf("its subscription link (%s) has no node turned on", src.Name)
	}
	return out, ""
}

// ---------------------------------------------------------------- API

type sourceView struct {
	ID         int64               `json:"id"`
	Name       string              `json:"name"`
	URL        string              `json:"url" doc:"The provider's subscription address"`
	Client     string              `json:"client" doc:"Which app the panel asks as: '' (Rosélune), clash, singbox or v2rayn"`
	EveryHours int                 `json:"every_hours" doc:"Read again every this many hours; 0 = only when asked"`
	Enabled    bool                `json:"enabled"`
	Offer      bool                `json:"offer" doc:"Whether users get its nodes in their own subscriptions"`
	OfferTo    offerTo             `json:"offer_to" doc:"Which users get them (both empty = every user)"`
	Prefix     string              `json:"prefix" doc:"Put in front of each node's name (with a space)"`
	Include    string              `json:"include" doc:"Only nodes whose name holds one of these words (separated by commas)"`
	Exclude    string              `json:"exclude" doc:"Leave out nodes whose name holds one of these words"`
	Note       string              `json:"note"`
	FetchedAt  int64               `json:"fetched_at" doc:"Last attempt (Unix seconds)"`
	OkAt       int64               `json:"ok_at" doc:"Last good refresh"`
	NextAt     int64               `json:"next_at" doc:"The next scheduled refresh (0 = none)"`
	Error      string              `json:"error" doc:"Why the last attempt failed; empty when it worked"`
	Skipped    []subgen.ParseError `json:"skipped" doc:"Entries the last refresh could not use"`
	Usage      *sourceUsage        `json:"usage" doc:"What the provider says about the subscription itself (null when it says nothing)"`
	Nodes      int                 `json:"nodes"`
	Missing    int                 `json:"missing" doc:"Nodes gone from the subscription but kept because something still names them"`
	UsedBy     []extUse            `json:"used_by" doc:"Load balancers with all of its nodes as one member"`
	CreatedAt  int64               `json:"created_at"`
	UpdatedAt  int64               `json:"updated_at"`
}

func (p *Panel) viewOfSource(ctx context.Context, s *ExtSource) sourceView {
	v := sourceView{ID: s.ID, Name: s.Name, URL: s.URL, Client: s.Client, EveryHours: s.EveryHours, Enabled: s.Enabled, Offer: s.Offer,
		OfferTo: s.OfferTo, Prefix: s.Prefix, Include: s.Include, Exclude: s.Exclude, Note: s.Note, FetchedAt: s.FetchedAt, OkAt: s.OkAt,
		Error: s.Error, Skipped: s.Skipped, Usage: s.Usage, UsedBy: []extUse{}, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
	if v.Skipped == nil {
		v.Skipped = []subgen.ParseError{}
	}
	if s.Enabled && s.EveryHours > 0 {
		every := int64(s.EveryHours) * 3600
		if s.Error != "" {
			every = min(every, int64(sourceRetry/time.Second))
		}
		v.NextAt = s.FetchedAt + every
	}
	_ = p.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN missing_since > 0 THEN 1 ELSE 0 END), 0) FROM ext_nodes WHERE source_id = ?`,
		s.ID).Scan(&v.Nodes, &v.Missing)
	if bals, err := p.balancersOf(ctx, s.AccountID); err == nil {
		for _, b := range bals {
			if slices.Contains(b.Members, fmt.Sprintf("src:%d", s.ID)) {
				v.UsedBy = append(v.UsedBy, extUse{Type: "balancer", ID: b.ID, Name: b.Name})
			}
		}
	}
	return v
}

func (p *Panel) apiSources(w http.ResponseWriter, r *http.Request, a *Account) error {
	list, err := p.sourcesOf(r.Context(), scopeAccount(r, a))
	if err != nil {
		return err
	}
	out := []sourceView{}
	for _, s := range list {
		out = append(out, p.viewOfSource(r.Context(), s))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type sourceInput struct {
	Name       *string  `json:"name"`
	URL        *string  `json:"url" doc:"The provider's subscription address: https:// only, public addresses only"`
	Client     *string  `json:"client" doc:"Ask as: '' (Rosélune), clash, singbox or v2rayn - providers often answer each app in its own form; every form is read"`
	EveryHours *int     `json:"every_hours" doc:"Read it again every this many hours (1 to 168; 0 = only when asked; default 12)"`
	Enabled    *bool    `json:"enabled" doc:"Off: it is not read again, and its nodes cannot be used (what uses them is blocked, as for a node turned off)"`
	Offer      *bool    `json:"offer" doc:"Give its nodes to users too: they appear in users' subscriptions, written for each app by Rosélune. Rosélune cannot count or limit what users send through them"`
	OfferTo    *offerTo `json:"offer_to" doc:"Which users get them: these users and the users on these plans (both empty = every user)"`
	Prefix     *string  `json:"prefix" doc:"Put in front of each node's name, with a space: e.g. 'Provider ·' gives 'Provider · Tokyo 1'"`
	Include    *string  `json:"include" doc:"Only nodes whose name holds one of these words (separated by commas)"`
	Exclude    *string  `json:"exclude" doc:"Leave out nodes whose name holds one of these words"`
	Note       *string  `json:"note"`
}

type sourceSaved struct {
	Source sourceView   `json:"source"`
	Result sourceResult `json:"result" doc:"What reading it did (on creation, a new address or a refresh)"`
	Error  string       `json:"error" doc:"Why it could not be read; the link is saved anyway and tried again later"`
}

func (p *Panel) applySourceInput(ctx context.Context, s *ExtSource, in *sourceInput) error {
	if in.Name != nil {
		s.Name = cleanName(*in.Name, 60)
	}
	if s.Name == "" {
		return errStatus(http.StatusBadRequest, "give the subscription link a name")
	}
	if in.URL != nil {
		u, err := checkFetchURL(*in.URL)
		if err != nil {
			return err
		}
		if len(u.String()) > 2048 {
			return errStatus(http.StatusBadRequest, "the address is longer than 2048 characters")
		}
		s.URL = u.String()
	}
	if s.URL == "" {
		return errStatus(http.StatusBadRequest, "enter the provider's subscription address")
	}
	if in.Client != nil {
		if _, ok := sourceClients[*in.Client]; !ok {
			return errStatus(http.StatusBadRequest, "ask as '' (Rosélune), clash, singbox or v2rayn")
		}
		s.Client = *in.Client
	}
	if in.EveryHours != nil {
		if *in.EveryHours < 0 || *in.EveryHours > 168 {
			return errStatus(http.StatusBadRequest, "read it again every 1 to 168 hours, or 0 for only when asked")
		}
		s.EveryHours = *in.EveryHours
	}
	if in.Enabled != nil {
		s.Enabled = *in.Enabled
	}
	if in.Offer != nil {
		s.Offer = *in.Offer
	}
	if in.OfferTo != nil {
		to := offerTo{}
		subs, err := p.subsOf(ctx, s.AccountID)
		if err != nil {
			return err
		}
		for _, id := range in.OfferTo.Users {
			if !slices.ContainsFunc(subs, func(x *Sub) bool { return x.ID == id }) {
				return errStatus(http.StatusBadRequest, fmt.Sprintf("user %d does not exist", id))
			}
			if !slices.Contains(to.Users, id) {
				to.Users = append(to.Users, id)
			}
		}
		plans, err := p.plansOf(ctx, s.AccountID)
		if err != nil {
			return err
		}
		for _, id := range in.OfferTo.Plans {
			if !slices.ContainsFunc(plans, func(x *Plan) bool { return x.ID == id }) {
				return errStatus(http.StatusBadRequest, fmt.Sprintf("plan %d does not exist", id))
			}
			if !slices.Contains(to.Plans, id) {
				to.Plans = append(to.Plans, id)
			}
		}
		s.OfferTo = to
	}
	if in.Prefix != nil {
		s.Prefix = cleanName(*in.Prefix, 24)
	}
	if in.Include != nil {
		s.Include = cleanNote(*in.Include, 500)
	}
	if in.Exclude != nil {
		s.Exclude = cleanNote(*in.Exclude, 500)
	}
	if in.Note != nil {
		s.Note = cleanNote(*in.Note, 500)
	}
	return nil
}

func (p *Panel) saveSource(ctx context.Context, s *ExtSource) error {
	to := ""
	if !s.OfferTo.all() {
		b, _ := json.Marshal(s.OfferTo)
		to = string(b)
	}
	s.UpdatedAt = now()
	_, err := p.db.Exec1(`UPDATE ext_sources SET name = ?, url = ?, client = ?, every_hours = ?, enabled = ?, offer = ?, offer_to = ?, prefix = ?,
		include = ?, exclude = ?, note = ?, updated_at = ? WHERE id = ?`, s.Name, s.URL, s.Client, s.EveryHours, s.Enabled, s.Offer, to, s.Prefix,
		s.Include, s.Exclude, s.Note, s.UpdatedAt, s.ID)
	return err
}

func (p *Panel) apiCreateSource(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in sourceInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	s := &ExtSource{AccountID: a.ID, EveryHours: 12, Enabled: true}
	if err := p.applySourceInput(r.Context(), s, &in); err != nil {
		return err
	}
	list, err := p.sourcesOf(r.Context(), a.ID)
	if err != nil {
		return err
	}
	if len(list) >= maxSources {
		return errStatus(http.StatusConflict, fmt.Sprintf("at most %d subscription links - remove one first", maxSources))
	}
	for _, o := range list {
		if strings.EqualFold(o.Name, s.Name) {
			return errStatus(http.StatusConflict, fmt.Sprintf("another subscription link is called %s - choose another name", o.Name))
		}
		if o.URL == s.URL {
			return errStatus(http.StatusConflict, fmt.Sprintf("that address is the subscription link %s already", o.Name))
		}
	}
	t := now()
	ins, err := p.db.Exec1(`INSERT INTO ext_sources (account_id, name, url, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, a.ID, s.Name, s.URL, t, t)
	if err != nil {
		return err
	}
	s.ID, _ = ins.LastInsertId()
	s.CreatedAt = t
	if err := p.saveSource(r.Context(), s); err != nil {
		return err
	}
	p.event(a.ID, "info", "ext_source_added", 0, 0, a.ID, fmt.Sprintf("%s added the subscription link %s", a.Username, s.Name), nil)
	out := sourceSaved{}
	if s.Enabled {
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		res, err := p.refreshSource(ctx, s, a.Username)
		out.Result = res
		if err != nil {
			out.Error = errText(err)
		}
	}
	if s, err = p.sourceByID(r.Context(), s.ID); err != nil {
		return err
	}
	out.Source = p.viewOfSource(r.Context(), s)
	writeJSON(w, http.StatusCreated, out)
	return nil
}

func (p *Panel) apiUpdateSource(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownSource(r.Context(), a, id)
	if err != nil {
		return err
	}
	var in sourceInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	oldURL, oldPrefix, wasOn, oldInc, oldExc := s.URL, s.Prefix, s.Enabled, s.Include, s.Exclude
	if err := p.applySourceInput(r.Context(), s, &in); err != nil {
		return err
	}
	list, err := p.sourcesOf(r.Context(), s.AccountID)
	if err != nil {
		return err
	}
	for _, o := range list {
		if o.ID == s.ID {
			continue
		}
		if strings.EqualFold(o.Name, s.Name) {
			return errStatus(http.StatusConflict, fmt.Sprintf("another subscription link is called %s - choose another name", o.Name))
		}
		if o.URL == s.URL {
			return errStatus(http.StatusConflict, fmt.Sprintf("that address is the subscription link %s already", o.Name))
		}
	}
	if err := p.saveSource(r.Context(), s); err != nil {
		return err
	}
	msg := fmt.Sprintf("%s changed the subscription link %s", a.Username, s.Name)
	switch {
	case wasOn && !s.Enabled:
		msg = fmt.Sprintf("%s turned the subscription link %s off - what uses its nodes is blocked until it is on again", a.Username, s.Name)
	case !wasOn && s.Enabled:
		msg = fmt.Sprintf("%s turned the subscription link %s on", a.Username, s.Name)
	}
	p.event(s.AccountID, "info", "ext_source_changed", 0, 0, a.ID, msg, nil)
	out := sourceSaved{Result: sourceResult{Kept: []string{}, Skipped: []subgen.ParseError{}}}
	if s.Enabled && (s.URL != oldURL || s.Prefix != oldPrefix || s.Include != oldInc || s.Exclude != oldExc || !wasOn) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		res, err := p.refreshSource(ctx, s, a.Username)
		out.Result = res
		if err != nil {
			out.Error = errText(err)
		}
	}
	if wasOn != s.Enabled {
		p.touchAccount(s.AccountID)
	}
	if s, err = p.sourceByID(r.Context(), s.ID); err != nil {
		return err
	}
	out.Source = p.viewOfSource(r.Context(), s)
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (p *Panel) apiRefreshSource(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownSource(r.Context(), a, id)
	if err != nil {
		return err
	}
	if !p.limiter.allow(fmt.Sprintf("source:%d", s.ID), 20, time.Hour) {
		return errStatus(http.StatusTooManyRequests, "it was refreshed many times in the last hour - wait a little")
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	out := sourceSaved{}
	res, err := p.refreshSource(ctx, s, a.Username)
	out.Result = res
	if err != nil {
		out.Error = errText(err)
	}
	if s, err = p.sourceByID(r.Context(), s.ID); err != nil {
		return err
	}
	out.Source = p.viewOfSource(r.Context(), s)
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (p *Panel) apiDeleteSource(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownSource(r.Context(), a, id)
	if err != nil {
		return err
	}
	keep := r.URL.Query().Get("keep_nodes") == "1"
	var nodes []*ExtNode
	all, err := p.extNodesOf(r.Context(), s.AccountID)
	if err != nil {
		return err
	}
	for _, x := range all {
		if x.SourceID == s.ID {
			nodes = append(nodes, x)
		}
	}
	uses := p.extUsesOf(r.Context(), s.AccountID)
	out := extRemoved{OK: true, Blocked: []string{}}
	if err := p.db.Write(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM ext_sources WHERE id = ?`, s.ID); err != nil {
			return err
		}
		if err := pruneRoutes(tx, s.AccountID, nil, nil, fmt.Sprintf("src:%d", s.ID)); err != nil {
			return err
		}
		if keep { // they stay as nodes imported once
			_, err := tx.Exec(`UPDATE ext_nodes SET source_id = 0, source_key = '', missing_since = 0 WHERE source_id = ?`, s.ID)
			return err
		}
		for _, x := range nodes {
			if _, err := tx.Exec(`DELETE FROM ext_nodes WHERE id = ?`, x.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO id_floor (name, last) VALUES ('ext_nodes', ?)
				ON CONFLICT(name) DO UPDATE SET last = MAX(last, excluded.last)`, x.ID); err != nil {
				return err
			}
			if err := pruneRoutes(tx, s.AccountID, nil, nil, fmt.Sprintf("ext:%d", x.ID)); err != nil {
				return err
			}
			for _, u := range uses[x.ID] {
				if u.Type != "balancer" {
					out.Blocked = append(out.Blocked, u.Name)
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}
	msg := fmt.Sprintf("%s removed the subscription link %s", a.Username, s.Name)
	if keep {
		msg += fmt.Sprintf(" and kept its %s as imported nodes", countOf(len(nodes), "node", "nodes"))
	} else if len(out.Blocked) > 0 {
		msg += " - the traffic of " + strings.Join(out.Blocked, ", ") + " is blocked until they get another exit"
	}
	p.event(s.AccountID, "warn", "ext_source_removed", 0, 0, a.ID, msg, nil)
	p.touchAccount(s.AccountID)
	writeJSON(w, http.StatusOK, out)
	return nil
}

// errText is an error as the panel shows it.
func errText(err error) string {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Msg
	}
	return err.Error()
}
