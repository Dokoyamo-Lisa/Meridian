package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"
)

func now() int64 { return time.Now().Unix() }

func (p *Panel) accountByID(ctx context.Context, id int64) (*Account, error) {
	return scanAccount(p.db.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts WHERE id = ?`, id))
}

func (p *Panel) accountByName(ctx context.Context, name string) (*Account, error) {
	return scanAccount(p.db.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts WHERE username = ?`, name))
}

// serverByID includes deleted servers (their agents still need the decommission state).
func (p *Panel) serverByID(ctx context.Context, id int64) (*Server, error) {
	return scanServer(p.db.QueryRowContext(ctx, `SELECT `+serverCols+` FROM servers WHERE id = ?`, id))
}

func (p *Panel) serversOf(ctx context.Context, accountID int64) ([]*Server, error) {
	q := `SELECT ` + serverCols + ` FROM servers WHERE deleted_at = 0`
	args := []any{}
	if accountID > 0 {
		q += ` AND account_id = ?`
		args = append(args, accountID)
	}
	q += ` ORDER BY sort, id`
	rows, err := p.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Server
	for rows.Next() {
		s, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *Panel) nodesOf(ctx context.Context, serverID int64) ([]*Node, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE server_id = ? ORDER BY sort, id`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (p *Panel) nodeByID(ctx context.Context, id int64) (*Node, error) {
	return scanNode(p.db.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id = ?`, id))
}

func (p *Panel) forwardsOf(ctx context.Context, serverID int64) ([]*Forward, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT `+forwardCols+` FROM forwards WHERE server_id = ? ORDER BY listen_port`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Forward
	for rows.Next() {
		f, err := scanForward(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (p *Panel) forwardByID(ctx context.Context, id int64) (*Forward, error) {
	return scanForward(p.db.QueryRowContext(ctx, `SELECT `+forwardCols+` FROM forwards WHERE id = ?`, id))
}

func (p *Panel) subsOf(ctx context.Context, accountID int64) ([]*Sub, error) {
	q := `SELECT ` + subCols + ` FROM subs`
	args := []any{}
	if accountID > 0 {
		q += ` WHERE account_id = ?`
		args = append(args, accountID)
	}
	q += ` ORDER BY id`
	rows, err := p.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Sub
	for rows.Next() {
		s, err := scanSub(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *Panel) subByID(ctx context.Context, id int64) (*Sub, error) {
	return scanSub(p.db.QueryRowContext(ctx, `SELECT `+subCols+` FROM subs WHERE id = ?`, id))
}

func (p *Panel) subByToken(ctx context.Context, token string) (*Sub, error) {
	return scanSub(p.db.QueryRowContext(ctx, `SELECT `+subCols+` FROM subs WHERE token = ?`, token))
}

func (p *Panel) wgPeersOf(ctx context.Context, nodeID int64) ([]*wgPeer, error) {
	return queryPeers(ctx, p.db, nodeID)
}

// queryPeers reads a WireGuard node's peers, from the database or inside a transaction.
func queryPeers(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, nodeID int64) ([]*wgPeer, error) {
	rows, err := q.QueryContext(ctx, `SELECT sub_id, node_id, private_key, public_key, psk, ip4, ip6 FROM wg_peers
		WHERE node_id = ?`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*wgPeer
	for rows.Next() {
		pr := &wgPeer{}
		if err := rows.Scan(&pr.SubID, &pr.NodeID, &pr.PrivateKey, &pr.PublicKey, &pr.PSK, &pr.IP4, &pr.IP6); err != nil {
			return nil, err
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- events

type Event struct {
	ID        int64           `json:"id"`
	TS        int64           `json:"ts"`
	AccountID int64           `json:"-"`
	Level     string          `json:"level"`
	Kind      string          `json:"kind"`
	ServerID  int64           `json:"server_id"`
	SubID     int64           `json:"user_id"`
	ActorID   int64           `json:"actor_id"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data"`
}

// event records something worth showing in the timeline.
func (p *Panel) event(accountID int64, level, kind string, serverID, subID, actorID int64, msg string, data any) {
	d := "{}"
	if data != nil {
		if b, err := json.Marshal(data); err == nil {
			d = string(b)
		}
	}
	if _, err := p.db.Exec1(`INSERT INTO events (ts, account_id, level, kind, server_id, sub_id, actor_id, message, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, now(), accountID, level, kind, serverID, subID, actorID, msg, d); err != nil {
		slog.Error("event", "err", err)
	}
}

func eventTx(tx *sql.Tx, accountID int64, level, kind string, serverID, subID int64, msg string) {
	if _, err := tx.Exec(`INSERT INTO events (ts, account_id, level, kind, server_id, sub_id, message)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, now(), accountID, level, kind, serverID, subID, msg); err != nil {
		slog.Error("event", "err", err)
	}
}
