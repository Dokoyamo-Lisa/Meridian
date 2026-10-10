package panel

// Protective steps. A risk may offer a fix (proto.Fix): what its server's agent can do about it -
// stop a process and put its program in quarantine, take an SSH key out, lock an account, turn a
// service off, move a planted file into quarantine, make SSH take keys only, block the addresses that
// keep guessing SSH passwords. None of it ever happens by itself: the supervisor confirms each step
// in the browser - a signed-in session, never an API token or an assistant - the agent checks it all
// again before it acts, and every step but stopping a process can be undone here.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"meridian/internal/proto"
)

// fixView is a risk's protective step as the UI offers it: the button, and what pressing it does.
type fixView struct {
	Kind    string   `json:"kind" doc:"stop_process | remove_key | lock_account | disable_service | quarantine | ssh_keys_only | block_ssh"`
	Label   string   `json:"label" doc:"The button: what it does, in a few words"`
	Explain string   `json:"explain" doc:"What exactly happens, and whether it can be undone: what the confirmation says"`
	Target  string   `json:"target,omitempty" doc:"What it acts on: a program, a file, an account, a service or a key"`
	Addrs   []string `json:"addrs,omitempty" doc:"block_ssh: the addresses"`
	Undo    bool     `json:"undo" doc:"It can be undone afterwards"`
}

// protectionView is one protective step the supervisor confirmed.
type protectionView struct {
	ID        int64  `json:"id"`
	ServerID  int64  `json:"server_id"`
	Server    string `json:"server"`
	RiskID    int64  `json:"risk_id"`
	Kind      string `json:"kind"`
	Title     string `json:"title" doc:"The risk it was about"`
	What      string `json:"what" doc:"What was asked, in plain words"`
	State     string `json:"state" doc:"pending (on its way to the server) | done | failed | undoing | undone"`
	Output    string `json:"output" doc:"What the server said"`
	CanUndo   bool   `json:"can_undo" doc:"Done, and of a kind that can be undone"`
	CreatedAt int64  `json:"created_at"`
	CreatedBy string `json:"created_by"`
	DoneAt    int64  `json:"done_at"`
	UndoneAt  int64  `json:"undone_at"`
	UndoneBy  string `json:"undone_by"`
}

var (
	fixPathRE = regexp.MustCompile(`^/[^\x00-\x1f\x7f]+$`)
	fixKeyRE  = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
	fixUserRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,31}$`)
	fixUnitRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.@-]{0,190}$`)
	fixSumRE  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// cleanFix checks a fix an agent offers before it is kept (and one day sent back to that agent): a
// known kind with exactly what that kind needs, in plain shapes. The agent checks it all again.
func cleanFix(f *proto.Fix) bool {
	okPath := func(p string) bool { return len(p) < 4096 && fixPathRE.MatchString(p) && filepath.Clean(p) == p }
	switch f.Kind {
	case proto.FixStopProcess:
		return f.PID > 1 && f.Start > 0 && okPath(f.Path)
	case proto.FixRemoveKey:
		return okPath(f.Path) && fixKeyRE.MatchString(f.Key)
	case proto.FixLockAccount:
		return fixUserRE.MatchString(f.User) && f.User != "root"
	case proto.FixDisableService:
		return fixUnitRE.MatchString(f.Unit)
	case proto.FixQuarantine:
		return okPath(f.Path) && (f.SHA256 == "" || fixSumRE.MatchString(f.SHA256))
	case proto.FixKeysOnly:
		*f = proto.Fix{Kind: proto.FixKeysOnly}
		return true
	case proto.FixBlockSSH:
		var ok []string
		for _, s := range f.Addrs {
			if a, err := netip.ParseAddr(s); err == nil && a.Zone() == "" && !slices.Contains(ok, a.Unmap().String()) && len(ok) < 50 {
				ok = append(ok, a.Unmap().String())
			}
		}
		f.Addrs = ok
		return len(ok) > 0
	}
	return false
}

func fixJSON(f *proto.Fix) string {
	if f == nil {
		return ""
	}
	b, _ := json.Marshal(f)
	return string(b)
}

func storedFix(s string) *proto.Fix {
	if s == "" {
		return nil
	}
	var f proto.Fix
	if json.Unmarshal([]byte(s), &f) != nil || !cleanFix(&f) {
		return nil
	}
	return &f
}

// describeFix says what a fix does, in the words of its button and its confirmation.
func describeFix(f proto.Fix) *fixView {
	v := &fixView{Kind: f.Kind, Undo: true}
	switch f.Kind {
	case proto.FixStopProcess:
		v.Label, v.Target = "Stop it", f.Path
		v.Explain = fmt.Sprintf("Stops process %d (%s) at once - and every other process running that program, when the program sits "+
			"where programs are not installed (a temporary folder, a home, /opt ...): then the program goes into quarantine too. "+
			"A program in a system folder stays where it is. The process cannot be started again from here; its program can be put back.", f.PID, f.Path)
	case proto.FixRemoveKey:
		v.Label, v.Target = "Remove this key", f.Key
		v.Explain = "Takes the SSH key " + f.Key + " out of " + f.Path + ": it can no longer sign in. If it is your own key, you lose that way in. It can be put back."
	case proto.FixLockAccount:
		v.Label, v.Target = "Lock "+f.User, f.User
		v.Explain = "Locks " + f.User + ": neither a password nor an SSH key gets in any more, and what it runs is stopped (an account with uid 0 " +
			"excepted: its processes cannot be told from root's). It can be unlocked."
	case proto.FixDisableService:
		v.Label, v.Target = "Turn it off", f.Unit
		v.Explain = "Stops " + f.Unit + " now and keeps it from starting at boot. It can be turned on again."
	case proto.FixQuarantine:
		v.Label, v.Target = "Move to quarantine", f.Path
		v.Explain = "Moves " + f.Path + " into the agent's quarantine on the server, where nothing can use it - only the file as it was found. " +
			"It can be put back, unless something else is in its place by then."
	case proto.FixKeysOnly:
		v.Label = "SSH: keys only"
		v.Explain = "SSH stops taking passwords - empty ones included: only SSH keys sign in. Sessions already open stay. The server first checks " +
			"that an account that may sign in has a key - make sure one is yours, or you lock yourself out. It can be undone."
	case proto.FixBlockSSH:
		v.Label, v.Addrs = fmt.Sprintf("Block %d address%s", len(f.Addrs), plural(len(f.Addrs), "", "es")), f.Addrs
		v.Explain = "These addresses can no longer reach SSH on this server: " + strings.Join(f.Addrs, ", ") + ". Never an address that signed in " +
			"lately, nor the one you use now. It can be undone."
	}
	return v
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// whatFix is a fix in a line, for the timeline.
func whatFix(f proto.Fix) string {
	switch f.Kind {
	case proto.FixStopProcess:
		return "stop " + f.Path
	case proto.FixRemoveKey:
		return "remove the SSH key " + f.Key
	case proto.FixLockAccount:
		return "lock " + f.User
	case proto.FixDisableService:
		return "turn off " + f.Unit
	case proto.FixQuarantine:
		return "move " + f.Path + " into quarantine"
	case proto.FixKeysOnly:
		return "make SSH take keys only"
	case proto.FixBlockSSH:
		return fmt.Sprintf("block %d address%s from SSH", len(f.Addrs), plural(len(f.Addrs), "", "es"))
	}
	return f.Kind
}

const protectionCols = `pr.id, pr.server_id, s.name, pr.risk_id, pr.kind, pr.title, pr.fix, pr.state, pr.output, pr.created_at, pr.created_by,
pr.done_at, pr.undone_at, pr.undone_by`

func scanProtection(row interface{ Scan(...any) error }) (*protectionView, error) {
	v := &protectionView{}
	var fix string
	if err := row.Scan(&v.ID, &v.ServerID, &v.Server, &v.RiskID, &v.Kind, &v.Title, &fix, &v.State, &v.Output, &v.CreatedAt,
		&v.CreatedBy, &v.DoneAt, &v.UndoneAt, &v.UndoneBy); err != nil {
		return nil, err
	}
	v.What = v.Kind
	if f := storedFix(fix); f != nil {
		v.What = whatFix(*f)
	}
	v.CanUndo = v.State == "done"
	return v, nil
}

// attachSteps gives each risk the last protective step taken about it.
func (p *Panel) attachSteps(ctx context.Context, list []*riskView) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]string, 0, len(list))
	byID := map[int64]*riskView{}
	for _, v := range list {
		ids = append(ids, strconv.FormatInt(v.ID, 10))
		byID[v.ID] = v
	}
	rows, err := p.db.QueryContext(ctx, `SELECT `+protectionCols+` FROM protections pr JOIN servers s ON s.id = pr.server_id
		WHERE pr.risk_id IN (`+strings.Join(ids, ",")+`) ORDER BY pr.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		pv, err := scanProtection(rows)
		if err != nil {
			return err
		}
		if v := byID[pv.RiskID]; v != nil {
			v.Step = pv // the newest wins
		}
	}
	return rows.Err()
}

func (p *Panel) ownProtection(ctx context.Context, a *Account, id int64) (*protectionView, *Server, string, error) {
	var fix string
	pv, err := scanProtection(p.db.QueryRowContext(ctx, `SELECT `+protectionCols+` FROM protections pr JOIN servers s ON s.id = pr.server_id
		WHERE pr.id = ?`, id))
	if err != nil {
		return nil, nil, "", errNotFound
	}
	srv, err := p.ownServer(ctx, a, pv.ServerID)
	if err != nil {
		return nil, nil, "", errNotFound
	}
	_ = p.db.QueryRowContext(ctx, `SELECT fix FROM protections WHERE id = ?`, id).Scan(&fix)
	return pv, srv, fix, nil
}

// ready says why a server cannot take a protective step now, or "".
func protectReady(srv *Server) string {
	switch {
	case srv.Guest:
		return "only the server's own panel can take protective steps on it"
	case srv.FirstSeenAt == 0 || !srv.Online:
		return "the server is offline: it cannot act now"
	case !srv.caps().Protect:
		return "protective steps need agent 1.3.1 or later on this server - upgrade its agent first (nobody is disconnected by that)"
	}
	return ""
}

// apiProtect takes the protective step a risk offers - the fix as its agent proposed it, never one
// the caller describes. Browser sessions only: a person confirms it.
func (p *Panel) apiProtect(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	risk, err := p.ownRisk(r.Context(), a, id)
	if err != nil {
		return err
	}
	srv, err := p.ownServer(r.Context(), a, risk.ServerID)
	if err != nil {
		return err
	}
	if why := protectReady(srv); why != "" {
		return errStatus(http.StatusConflict, why)
	}
	f := storedFix(risk.fix)
	if f == nil {
		return errStatus(http.StatusBadRequest, "nothing can be done about this from here - look at it on the server")
	}
	if f.Kind == proto.FixBlockSSH { // never the address the supervisor uses now
		mine := p.clientIP(r)
		f.Addrs = slices.DeleteFunc(f.Addrs, func(s string) bool { return s == mine })
		if len(f.Addrs) == 0 {
			return errStatus(http.StatusBadRequest, "the only address to block is the one you use now - nothing was done")
		}
	}
	var pid int64
	err = p.db.Write(r.Context(), func(tx *sql.Tx) error {
		var busy string
		if tx.QueryRow(`SELECT state FROM protections WHERE risk_id = ? AND state IN ('pending', 'undoing', 'done') ORDER BY id DESC LIMIT 1`,
			risk.ID).Scan(&busy) == nil {
			if busy == "done" {
				return errStatus(http.StatusConflict, "that step was taken already - undo it first to take it again")
			}
			return errStatus(http.StatusConflict, "that step is on its way to the server")
		}
		res, err := tx.Exec(`INSERT INTO protections (account_id, server_id, risk_id, kind, title, fix, state, created_at, created_by)
			VALUES (?, ?, ?, ?, ?, ?, 'pending', ?, ?)`, srv.AccountID, srv.ID, risk.ID, f.Kind, risk.Title, fixJSON(f), now(), a.Username)
		if err != nil {
			return err
		}
		pid, _ = res.LastInsertId()
		args, _ := json.Marshal(proto.Protect{ID: pid, Fix: f})
		res, err = tx.Exec(`INSERT INTO actions (server_id, kind, args, created_at, created_by) VALUES (?, ?, ?, ?, ?)`,
			srv.ID, proto.ActionProtect, string(args), now(), a.ID)
		if err != nil {
			return err
		}
		aid, _ := res.LastInsertId()
		_, err = tx.Exec(`UPDATE protections SET action_id = ? WHERE id = ?`, aid, pid)
		return err
	})
	if err != nil {
		return err
	}
	p.event(srv.AccountID, "warn", "protect", srv.ID, 0, a.ID, fmt.Sprintf("%s asked %s to %s", a.Username, srv.Name, whatFix(*f)), nil)
	p.touchServers(srv.ID)
	pv, _, _, err := p.ownProtection(r.Context(), a, pid)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, pv)
	return nil
}

// apiUndoProtection undoes a protective step that was done. Browser sessions only.
func (p *Panel) apiUndoProtection(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	pv, srv, fix, err := p.ownProtection(r.Context(), a, id)
	if err != nil {
		return err
	}
	if why := protectReady(srv); why != "" {
		return errStatus(http.StatusConflict, why)
	}
	if pv.State != "done" {
		return errStatus(http.StatusConflict, "only a step that was done can be undone")
	}
	f := storedFix(fix)
	if f == nil {
		return errStatus(http.StatusConflict, "this step cannot be undone from here")
	}
	err = p.db.Write(r.Context(), func(tx *sql.Tx) error {
		args, _ := json.Marshal(proto.Protect{ID: pv.ID, Undo: true})
		res, err := tx.Exec(`INSERT INTO actions (server_id, kind, args, created_at, created_by) VALUES (?, ?, ?, ?, ?)`,
			srv.ID, proto.ActionProtect, string(args), now(), a.ID)
		if err != nil {
			return err
		}
		aid, _ := res.LastInsertId()
		_, err = tx.Exec(`UPDATE protections SET state = 'undoing', undo_action = ?, undone_by = ? WHERE id = ? AND state = 'done'`,
			aid, a.Username, pv.ID)
		return err
	})
	if err != nil {
		return err
	}
	p.event(srv.AccountID, "warn", "protect", srv.ID, 0, a.ID, fmt.Sprintf("%s asked %s to undo: %s", a.Username, srv.Name, whatFix(*f)), nil)
	p.touchServers(srv.ID)
	pv, _, _, err = p.ownProtection(r.Context(), a, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, pv)
	return nil
}

// apiProtections lists the protective steps taken, newest first.
func (p *Panel) apiProtections(w http.ResponseWriter, r *http.Request, a *Account) error {
	q := `SELECT ` + protectionCols + ` FROM protections pr JOIN servers s ON s.id = pr.server_id WHERE s.deleted_at = 0`
	var args []any
	if acct := scopeAccount(r, a); acct > 0 {
		q += ` AND pr.account_id = ?`
		args = append(args, acct)
	}
	if v := r.URL.Query().Get("server"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return errStatus(http.StatusBadRequest, "server must be a server id")
		}
		if _, err := p.ownServer(r.Context(), a, id); err != nil {
			return err
		}
		q += ` AND pr.server_id = ?`
		args = append(args, id)
	}
	rows, err := p.db.QueryContext(r.Context(), q+` ORDER BY pr.id DESC LIMIT 200`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []*protectionView{}
	for rows.Next() {
		pv, err := scanProtection(rows)
		if err != nil {
			return err
		}
		out = append(out, pv)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// finishProtect records how a protective step went, from its action's result: done, failed, or
// undone. A step done marks its risk acknowledged by whoever asked for it - dealt with.
func finishProtect(tx *sql.Tx, srv *Server, ar proto.ActionResult) {
	var id, risk int64
	var state, by string
	err := tx.QueryRow(`SELECT id, risk_id, state, created_by FROM protections WHERE server_id = ? AND (action_id = ? OR undo_action = ?)`,
		srv.ID, ar.ID, ar.ID).Scan(&id, &risk, &state, &by)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			logErr("protective step result", err)
		}
		return
	}
	out := truncate(ar.Output, 2000)
	switch {
	case state == "pending" && ar.OK:
		_, err = tx.Exec(`UPDATE protections SET state = 'done', output = ?, done_at = ? WHERE id = ?`, out, now(), id)
		if err == nil && risk > 0 {
			_, err = tx.Exec(`UPDATE risks SET status = 'acknowledged', decided_by = ?, decided_at = ? WHERE id = ? AND status = 'open'`,
				by, now(), risk)
		}
	case state == "pending":
		_, err = tx.Exec(`UPDATE protections SET state = 'failed', output = ?, done_at = ? WHERE id = ?`, out, now(), id)
	case state == "undoing" && ar.OK:
		_, err = tx.Exec(`UPDATE protections SET state = 'undone', output = ?, undone_at = ? WHERE id = ?`, out, now(), id)
	case state == "undoing": // still done: the undo did not happen
		_, err = tx.Exec(`UPDATE protections SET state = 'done', output = ?, undo_action = 0, undone_by = '' WHERE id = ?`, "Undo failed: "+out, id)
	}
	logErr("protective step result", err)
}
