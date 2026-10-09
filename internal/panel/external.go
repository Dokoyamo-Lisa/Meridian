package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"meridian/internal/subgen"
)

// External nodes are proxies elsewhere - a provider's, a friend's - imported from their links. They
// are exits: a protocol can pass through one (users then appear at that node), and traffic splitting
// rules and load balancers can send traffic there. Meridian never connects users to them directly
// and cannot count what goes through them at the far end; it counts what users send through its own
// servers as always.

type ExtNode struct {
	ID           int64           `json:"id"`
	AccountID    int64           `json:"-"`
	Name         string          `json:"name"`
	Kind         string          `json:"kind"`
	Endpoint     subgen.Endpoint `json:"-"`
	Enabled      bool            `json:"enabled"`
	Note         string          `json:"note"`
	CreatedAt    int64           `json:"created_at"`
	UpdatedAt    int64           `json:"updated_at"`
	SourceID     int64           `json:"source_id"`     // the subscription link it comes from (0 = imported once)
	SourceKey    string          `json:"-"`             // the provider's name for it, as last read
	MissingSince int64           `json:"missing_since"` // gone from its subscription since then (kept while something names it)
}

const extCols = `id, account_id, name, kind, endpoint, enabled, note, created_at, updated_at, source_id, source_key, missing_since`

func scanExt(r interface{ Scan(...any) error }) (*ExtNode, error) {
	x := &ExtNode{}
	var ep string
	if err := r.Scan(&x.ID, &x.AccountID, &x.Name, &x.Kind, &ep, &x.Enabled, &x.Note, &x.CreatedAt, &x.UpdatedAt, &x.SourceID, &x.SourceKey,
		&x.MissingSince); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(ep), &x.Endpoint); err != nil {
		return nil, fmt.Errorf("external node %d: %w", x.ID, err)
	}
	return x, nil
}

func (p *Panel) extByID(ctx context.Context, id int64) (*ExtNode, error) {
	return scanExt(p.db.QueryRowContext(ctx, `SELECT `+extCols+` FROM ext_nodes WHERE id = ?`, id))
}

// extNodesOf lists an account's external nodes (0 = every account).
func (p *Panel) extNodesOf(ctx context.Context, accountID int64) ([]*ExtNode, error) {
	q, args := `SELECT `+extCols+` FROM ext_nodes`, []any{}
	if accountID > 0 {
		q += ` WHERE account_id = ?`
		args = append(args, accountID)
	}
	rows, err := p.db.QueryContext(ctx, q+` ORDER BY name COLLATE NOCASE, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ExtNode
	for rows.Next() {
		x, err := scanExt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ownExt loads an external node the caller may see.
func (p *Panel) ownExt(ctx context.Context, a *Account, id int64) (*ExtNode, error) {
	x, err := p.extByID(ctx, id)
	if err != nil {
		return nil, errNotFound
	}
	if !a.IsOwner() && x.AccountID != a.ID {
		return nil, errNotFound
	}
	return x, nil
}

// extExit is external node id as an exit for a protocol (or rule) of account acct: the node, or why
// it cannot be used right now (traffic sent there is blocked meanwhile).
func (p *Panel) extExit(ctx context.Context, acct, id int64) (*ExtNode, string) {
	x, err := p.extByID(ctx, id)
	switch {
	case err != nil:
		return nil, "its external node was removed"
	case x.AccountID != acct:
		return nil, "its external node belongs to another account"
	case !x.Enabled:
		return nil, fmt.Sprintf("its external node (%s) is turned off", x.Name)
	}
	if x.SourceID > 0 {
		if src, err := p.sourceByID(ctx, x.SourceID); err == nil && !src.Enabled {
			return nil, fmt.Sprintf("the subscription link of its external node (%s) is turned off", src.Name)
		}
	}
	return x, ""
}

// extLabel names an external node's kind the way the panel names protocols.
func extLabel(e subgen.Endpoint) string {
	name := map[string]string{subgen.KindVLESS: "VLESS", subgen.KindVMess: "VMess", subgen.KindTrojan: "Trojan",
		subgen.KindShadowsocks: "Shadowsocks", subgen.KindHysteria2: "Hysteria2", subgen.KindWireGuard: "WireGuard",
		subgen.KindHTTP: "HTTPS proxy"}[e.Kind]
	if name == "" {
		name = e.Kind
	}
	parts := []string{name}
	switch e.Kind {
	case subgen.KindVLESS, subgen.KindVMess, subgen.KindTrojan:
		if t := e.Transport; t != "" && t != subgen.TransportRaw {
			parts = append(parts, map[string]string{subgen.TransportWS: "WebSocket", subgen.TransportGRPC: "gRPC",
				subgen.TransportHTTPUpgrade: "HTTPUpgrade", subgen.TransportXHTTP: "XHTTP"}[t])
		}
		switch e.Security {
		case subgen.SecurityReality:
			parts = append(parts, "REALITY")
		case subgen.SecurityTLS:
			parts = append(parts, "TLS")
		}
	case subgen.KindShadowsocks:
		if strings.HasPrefix(e.Method, "2022-") {
			parts[0] = "Shadowsocks 2022"
		}
	case subgen.KindHysteria2:
		if e.Obfs != "" {
			parts = append(parts, "Salamander")
		}
	}
	return strings.Join(parts, " · ")
}

// ---------------------------------------------------------------- views

type extUse struct {
	Type string `json:"type" doc:"protocol | rule | balancer"`
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type extView struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Kind         string   `json:"kind" doc:"vless | vmess | trojan | shadowsocks | hysteria2 | wireguard | http (HTTPS)"`
	Label        string   `json:"label" doc:"e.g. VLESS · REALITY or Shadowsocks 2022"`
	Host         string   `json:"host"`
	Port         int      `json:"port"`
	Enabled      bool     `json:"enabled"`
	Note         string   `json:"note"`
	UsedBy       []extUse `json:"used_by" doc:"Protocols that pass through it, rules that send traffic to it, load balancers it is in"`
	SourceID     int64    `json:"source_id" doc:"The subscription link it comes from and follows (0 = imported once)"`
	MissingSince int64    `json:"missing_since" doc:"Gone from its subscription link since then (Unix seconds; 0 = it is there): kept because something still names it"`
	CreatedAt    int64    `json:"created_at"`
	UpdatedAt    int64    `json:"updated_at"`
}

func (p *Panel) viewOfExt(ctx context.Context, x *ExtNode) extView {
	return extViewWith(x, p.exitUses(ctx, x.AccountID, "ext", x.ID))
}

func extViewWith(x *ExtNode, uses []extUse) extView {
	if uses == nil {
		uses = []extUse{}
	}
	return extView{ID: x.ID, Name: x.Name, Kind: x.Kind, Label: extLabel(x.Endpoint), Host: x.Endpoint.Host, Port: x.Endpoint.Port,
		Enabled: x.Enabled, Note: x.Note, UsedBy: uses, SourceID: x.SourceID, MissingSince: x.MissingSince, CreatedAt: x.CreatedAt, UpdatedAt: x.UpdatedAt}
}

// extUsesOf is exitUses for every external node of account acct at once, for the list (an account
// may hold 2000 of them).
func (p *Panel) extUsesOf(ctx context.Context, acct int64) map[int64][]extUse {
	out := map[int64][]extUse{}
	if rows, err := p.db.QueryContext(ctx, `SELECT `+prefixCols("n.", nodeCols)+` FROM nodes n JOIN servers s ON s.id = n.server_id
		WHERE n.pass_ext > 0 AND s.deleted_at = 0 AND s.account_id = ? ORDER BY s.name, n.id`, acct); err == nil {
		var nodes []*Node
		for rows.Next() {
			if n, err := scanNode(rows); err == nil {
				nodes = append(nodes, n)
			}
		}
		rows.Close()
		for _, n := range nodes {
			out[n.PassExt] = append(out[n.PassExt], extUse{Type: "protocol", ID: n.ID, Name: p.nodeTitle(ctx, n)})
		}
	}
	if routes, err := p.routesOf(ctx, acct); err == nil {
		for _, r := range routes {
			if id, ok := extTarget(r.Target); ok {
				out[id] = append(out[id], extUse{Type: "rule", ID: r.ID, Name: r.title()})
			}
		}
	}
	if bals, err := p.balancersOf(ctx, acct); err == nil {
		for _, b := range bals {
			seen := map[int64]bool{}
			for _, m := range b.Members {
				if id, ok := extTarget(m); ok && !seen[id] {
					seen[id] = true
					out[id] = append(out[id], extUse{Type: "balancer", ID: b.ID, Name: b.Name})
				}
			}
		}
	}
	return out
}

// extTarget reads ext:<id>.
func extTarget(t string) (int64, bool) {
	s, ok := strings.CutPrefix(t, "ext:")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil
}

// exitUses lists what sends traffic to an exit: kind "ext" (an external node) or "node" (a protocol).
func (p *Panel) exitUses(ctx context.Context, acct int64, kind string, id int64) []extUse {
	out := []extUse{}
	col := map[string]string{"ext": "pass_ext", "node": "pass_node"}[kind]
	if rows, err := p.db.QueryContext(ctx, `SELECT `+prefixCols("n.", nodeCols)+` FROM nodes n JOIN servers s ON s.id = n.server_id
		WHERE n.`+col+` = ? AND s.deleted_at = 0 ORDER BY s.name, n.id`, id); err == nil {
		var nodes []*Node
		for rows.Next() {
			if n, err := scanNode(rows); err == nil {
				nodes = append(nodes, n)
			}
		}
		rows.Close()
		for _, n := range nodes {
			out = append(out, extUse{Type: "protocol", ID: n.ID, Name: p.nodeTitle(ctx, n)})
		}
	}
	target := fmt.Sprintf("%s:%d", kind, id)
	if routes, err := p.routesOf(ctx, acct); err == nil {
		for _, r := range routes {
			if r.Target == target {
				out = append(out, extUse{Type: "rule", ID: r.ID, Name: r.title()})
			}
		}
	}
	if bals, err := p.balancersOf(ctx, acct); err == nil {
		for _, b := range bals {
			for _, m := range b.Members {
				if m == target {
					out = append(out, extUse{Type: "balancer", ID: b.ID, Name: b.Name})
					break
				}
			}
		}
	}
	return out
}

// ---------------------------------------------------------------- API

func (p *Panel) apiExtNodes(w http.ResponseWriter, r *http.Request, a *Account) error {
	list, err := p.extNodesOf(r.Context(), scopeAccount(r, a))
	if err != nil {
		return err
	}
	out := []extView{}
	uses := map[int64]map[int64][]extUse{} // per account
	for _, x := range list {
		if uses[x.AccountID] == nil {
			uses[x.AccountID] = p.extUsesOf(r.Context(), x.AccountID)
		}
		out = append(out, extViewWith(x, uses[x.AccountID][x.ID]))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type extImportInput struct {
	Text    string `json:"text" doc:"Share links (one per line), a subscription's base64 content, or Clash / mihomo YAML with proxies (at most 4 MB)"`
	URL     string `json:"url" doc:"Or a provider's subscription address (https:// only, public addresses only) to fetch once"`
	Enabled *bool  `json:"enabled" doc:"Whether the new nodes can be used at once (default true)"`
}

type extImportResult struct {
	Added   []extView           `json:"added"`
	Skipped []subgen.ParseError `json:"skipped" doc:"Entries that could not be imported, each with the reason (the first 200; then one entry saying how many more)"`
}

// sameEndpoint says whether two nodes connect to the same place with the same credentials.
func sameEndpoint(a, b subgen.Endpoint) bool {
	a.Name, b.Name = "", ""
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

const (
	maxExtNodes   = 2000 // per account
	maxExtSkipped = 200  // reasons listed in one import's answer
)

// nextExtID is the id of a new external node: above every id ever used, so a protocol's proxy pass
// or a rule that named a removed node never comes to mean a new one (SQLite would hand out the
// highest id again once its row is gone; removals keep it in id_floor, as for protocols).
const nextExtID = `(SELECT MAX(v) + 1 FROM (SELECT COALESCE(MAX(id), 0) AS v FROM ext_nodes UNION ALL SELECT COALESCE(MAX(last), 0) FROM id_floor WHERE name = 'ext_nodes') floor_ids)`

// uniqueExtName is name, or name 2, name 3 ... when another node is called that already: lists,
// rules and protocol forms tell nodes apart by name.
func uniqueExtName(name string, taken map[string]bool) string {
	n := name
	for k := 2; taken[strings.ToLower(n)]; k++ {
		suffix := fmt.Sprintf(" %d", k)
		n = cleanName(name, 60-len(suffix)) + suffix
	}
	return n
}

func (p *Panel) apiImportExt(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in extImportInput
	// a pasted subscription or Clash file may be as large as a fetched one (4 MB), plus JSON escaping
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 2*fetchLimit))
	if err := dec.Decode(&in); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return errStatus(http.StatusRequestEntityTooLarge, "the text is larger than 4 MB - import it in parts")
		}
		return errStatus(http.StatusBadRequest, "invalid JSON: "+err.Error())
	}
	text := in.Text
	if len(text) > fetchLimit {
		return errStatus(http.StatusRequestEntityTooLarge, "the text is larger than 4 MB - import it in parts")
	}
	if strings.TrimSpace(in.URL) != "" {
		if strings.TrimSpace(text) != "" {
			return errStatus(http.StatusBadRequest, "give either the links or a subscription address, not both")
		}
		var err error
		if text, err = fetchText(r.Context(), in.URL); err != nil {
			return err
		}
	}
	eps, skipped := subgen.ParseNodes(text)
	existing, err := p.extNodesOf(r.Context(), a.ID)
	if err != nil {
		return err
	}
	taken := map[string]bool{}
	for _, x := range existing {
		taken[strings.ToLower(x.Name)] = true
	}
	enabled := in.Enabled == nil || *in.Enabled
	res := extImportResult{Added: []extView{}, Skipped: []subgen.ParseError{}}
	res.Skipped = append(res.Skipped, skipped...)
	t := now()
	var added []*ExtNode
	err = p.db.Write(r.Context(), func(tx *sql.Tx) error {
		for _, e := range eps {
			if dup := slicesFind(existing, func(x *ExtNode) bool { return sameEndpoint(x.Endpoint, e) }); dup != nil {
				res.Skipped = append(res.Skipped, subgen.ParseError{Name: e.Name, Reason: "already imported as " + dup.Name})
				continue
			}
			if len(existing) >= maxExtNodes {
				res.Skipped = append(res.Skipped, subgen.ParseError{Name: e.Name, Reason: fmt.Sprintf("at most %d external nodes - remove some first", maxExtNodes)})
				continue
			}
			name := uniqueExtName(nz(cleanName(e.Name, 60), "External node"), taken)
			e.Name = name
			ep, _ := json.Marshal(e)
			ins, err := tx.Exec(`INSERT INTO ext_nodes (id, account_id, name, kind, endpoint, enabled, created_at, updated_at)
				VALUES (`+nextExtID+`, ?, ?, ?, ?, ?, ?, ?)`, a.ID, name, e.Kind, string(ep), enabled, t, t)
			if err != nil {
				return err
			}
			id, _ := ins.LastInsertId()
			x := &ExtNode{ID: id, AccountID: a.ID, Name: name, Kind: e.Kind, Endpoint: e, Enabled: enabled, CreatedAt: t, UpdatedAt: t}
			taken[strings.ToLower(name)] = true
			existing = append(existing, x)
			added = append(added, x)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, x := range added { // new: nothing uses them yet
		v := p.viewOfExt(r.Context(), x)
		res.Added = append(res.Added, v)
	}
	if n := len(res.Skipped); n > maxExtSkipped {
		res.Skipped = append(res.Skipped[:maxExtSkipped], subgen.ParseError{Reason: fmt.Sprintf("and %d more left out", n-maxExtSkipped)})
	}
	if len(res.Added) > 0 {
		p.event(a.ID, "info", "ext_imported", 0, 0, a.ID, fmt.Sprintf("%s imported %s", a.Username, countOf(len(res.Added), "external node", "external nodes")), nil)
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

func slicesFind[T any](list []T, f func(T) bool) T {
	for _, x := range list {
		if f(x) {
			return x
		}
	}
	var zero T
	return zero
}

type extInput struct {
	Name    *string `json:"name" doc:"Unique among your external nodes"`
	Note    *string `json:"note"`
	Enabled *bool   `json:"enabled" doc:"Off: protocols and rules that use it block their traffic until it is back (they never leave from their own server instead)"`
	Link    *string `json:"link" doc:"A new share link for it (its address or credentials changed)"`
}

func (p *Panel) apiUpdateExt(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	x, err := p.ownExt(r.Context(), a, id)
	if err != nil {
		return err
	}
	var in extInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	wasOn, oldName := x.Enabled, x.Name
	if x.SourceID > 0 && (in.Name != nil && *in.Name != x.Name || in.Link != nil) {
		return errStatus(http.StatusConflict, "this node follows its subscription link: its name and link come from the provider at each refresh - change the link's prefix or filters instead")
	}
	others, err := p.extNodesOf(r.Context(), x.AccountID)
	if err != nil {
		return err
	}
	if in.Name != nil {
		if x.Name = cleanName(*in.Name, 60); x.Name == "" {
			return errStatus(http.StatusBadRequest, "give it a name")
		}
		x.Endpoint.Name = x.Name
		for _, o := range others {
			if o.ID != x.ID && strings.EqualFold(o.Name, x.Name) {
				return errStatus(http.StatusConflict, fmt.Sprintf("another external node is called %s - choose another name", o.Name))
			}
		}
	}
	if in.Note != nil {
		x.Note = cleanNote(*in.Note, 500)
	}
	if in.Enabled != nil {
		x.Enabled = *in.Enabled
	}
	if in.Link != nil {
		e, err := subgen.ParseLink(*in.Link)
		if err != nil {
			return errStatus(http.StatusBadRequest, err.Error())
		}
		for _, o := range others {
			if o.ID != x.ID && sameEndpoint(o.Endpoint, e) {
				return errStatus(http.StatusConflict, fmt.Sprintf("that link is the node %s already", o.Name))
			}
		}
		e.Name = x.Name
		x.Endpoint, x.Kind = e, e.Kind
	}
	ep, _ := json.Marshal(x.Endpoint)
	x.UpdatedAt = now()
	if _, err := p.db.Exec1(`UPDATE ext_nodes SET name = ?, note = ?, enabled = ?, kind = ?, endpoint = ?, updated_at = ? WHERE id = ?`,
		x.Name, x.Note, x.Enabled, x.Kind, string(ep), x.UpdatedAt, x.ID); err != nil {
		return err
	}
	// the timeline says what changed, and what it means for the traffic sent there
	msg := fmt.Sprintf("%s changed the external node %s", a.Username, x.Name)
	switch {
	case wasOn && !x.Enabled:
		msg = fmt.Sprintf("%s turned the external node %s off", a.Username, x.Name)
		if users := blockedBy(p.exitUses(r.Context(), x.AccountID, "ext", x.ID)); len(users) > 0 {
			msg += " - the traffic of " + strings.Join(users, ", ") + " is blocked until it is on again"
		}
	case !wasOn && x.Enabled:
		msg = fmt.Sprintf("%s turned the external node %s on", a.Username, x.Name)
	case in.Link != nil:
		msg = fmt.Sprintf("%s gave the external node %s a new link (%s)", a.Username, x.Name, net.JoinHostPort(x.Endpoint.Host, strconv.Itoa(x.Endpoint.Port)))
	case x.Name != oldName:
		msg = fmt.Sprintf("%s renamed the external node %s to %s", a.Username, oldName, x.Name)
	}
	p.event(x.AccountID, "info", "ext_changed", 0, 0, a.ID, msg, nil)
	p.touchAccount(x.AccountID)
	writeJSON(w, http.StatusOK, p.viewOfExt(r.Context(), x))
	return nil
}

// blockedBy names, for messages, what blocks its traffic while an exit cannot be used: the
// protocols passing through it and the rules sending traffic there (load balancers just leave it out).
func blockedBy(uses []extUse) []string {
	var out []string
	for _, u := range uses {
		switch u.Type {
		case "protocol":
			out = append(out, u.Name)
		case "rule":
			out = append(out, fmt.Sprintf("the rule %q", u.Name))
		}
	}
	return out
}

type extRemoved struct {
	OK      bool     `json:"ok"`
	Blocked []string `json:"blocked" doc:"What now blocks its traffic until it gets another exit: protocols that passed through it, rules that sent traffic there"`
}

func (p *Panel) apiDeleteExt(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	x, err := p.ownExt(r.Context(), a, id)
	if err != nil {
		return err
	}
	if x.SourceID > 0 && x.MissingSince == 0 {
		return errStatus(http.StatusConflict, "this node comes from a subscription link and would come back at its next refresh - turn it off, or leave it out with the link's filters")
	}
	uses := p.exitUses(r.Context(), x.AccountID, "ext", x.ID)
	// its id is never handed out again; load balancers lose it as a member
	if err := p.db.Write(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM ext_nodes WHERE id = ?`, x.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO id_floor (name, last) VALUES ('ext_nodes', ?)
			ON CONFLICT(name) DO UPDATE SET last = MAX(last, excluded.last)`, x.ID); err != nil {
			return err
		}
		return pruneRoutes(tx, x.AccountID, nil, nil, fmt.Sprintf("ext:%d", x.ID))
	}); err != nil {
		return err
	}
	out := extRemoved{OK: true, Blocked: []string{}}
	for _, u := range uses {
		if u.Type != "balancer" { // a load balancer just has one member less
			out.Blocked = append(out.Blocked, u.Name)
		}
	}
	msg := fmt.Sprintf("%s removed the external node %s", a.Username, x.Name)
	if users := blockedBy(uses); len(users) > 0 {
		msg += " - the traffic of " + strings.Join(users, ", ") + " is blocked until they get another exit"
	}
	p.event(x.AccountID, "warn", "ext_removed", 0, 0, a.ID, msg, nil)
	p.touchAccount(x.AccountID)
	writeJSON(w, http.StatusOK, out)
	return nil
}

// checkPassExt validates a proxy pass through an external node.
func (p *Panel) checkPassExt(ctx context.Context, entry *Node, entrySrv *Server, extID int64) error {
	if extID == 0 {
		return nil
	}
	if k, _ := kindOf(entry.Kind); k.Engine != "xray" {
		return errStatus(http.StatusBadRequest, "proxy pass starts from an Xray protocol (VLESS, VMess, Trojan, Shadowsocks, SOCKS5 or HTTP), not "+k.Label)
	}
	x, err := p.extByID(ctx, extID)
	if err != nil || x.AccountID != entrySrv.AccountID {
		return errStatus(http.StatusBadRequest, "that external node does not exist")
	}
	// protocols that pass through this one make the chain one pass longer (as checkPass says)
	if entry.ID > 0 {
		for _, e := range p.passEntryNodes(ctx, entry.ID) {
			if len(p.passEntryNodes(ctx, e.ID)) > 0 {
				return errStatus(http.StatusBadRequest, fmt.Sprintf("protocols pass through %s, which passes through this one - a chain has two passes at most", p.nodeTitle(ctx, e)))
			}
		}
	}
	return checkExtReach(entrySrv, x)
}

// extTitle names an external node for messages: "External · name".
func (p *Panel) extTitle(ctx context.Context, id int64) string {
	x, err := p.extByID(ctx, id)
	if err != nil {
		return "a removed external node"
	}
	return "External · " + x.Name
}
