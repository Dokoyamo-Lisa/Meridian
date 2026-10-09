package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"meridian/internal/proto"
	"meridian/internal/subgen"
)

// Checking an external node from one of the servers: the agent opens a connection to it - and, for
// a node behind TLS or REALITY, makes a TLS handshake checked against its server name - and reports
// whether that worked and how long it took. Nothing passes through the node; its credentials stay
// on the panel. Hysteria2 and WireGuard nodes speak UDP and cannot be checked this way.

type extCheckInput struct {
	ServerID int64 `json:"server_id" doc:"The server to check from (it must have connected)"`
}

// exitCheckOf is what the agent checks for a node, or why it cannot be checked.
func exitCheckOf(e subgen.Endpoint) (proto.ExitCheck, string) {
	switch e.Kind {
	case subgen.KindHysteria2, subgen.KindWireGuard:
		return proto.ExitCheck{}, extLabel(e) + " nodes speak UDP: a check from a server cannot reach them this way - try them in a rule or a protocol's proxy pass"
	}
	c := proto.ExitCheck{Host: e.Host, Port: e.Port}
	// a self-signed certificate is pinned, which only Xray checks: then the connection alone
	if (e.Security == subgen.SecurityTLS || e.Security == subgen.SecurityReality) && e.PinSHA256 == "" && e.SNI != "" {
		c.TLS, c.SNI = true, e.SNI
	}
	return c, ""
}

func (p *Panel) apiCheckExt(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	x, err := p.ownExt(r.Context(), a, id)
	if err != nil {
		return err
	}
	var in extCheckInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	s, err := p.ownServer(r.Context(), a, in.ServerID)
	if err != nil || s.AccountID != x.AccountID {
		return errStatus(http.StatusBadRequest, "choose one of your servers to check from")
	}
	if s.FirstSeenAt == 0 {
		return errStatus(http.StatusConflict, s.Name+"'s agent has not connected yet - check from another server")
	}
	check, why := exitCheckOf(x.Endpoint)
	if why != "" {
		return errStatus(http.StatusBadRequest, why)
	}
	args, _ := json.Marshal(check)
	res, err := p.db.Exec1(`INSERT INTO actions (server_id, kind, args, created_at, created_by) VALUES (?, ?, ?, ?, ?)`,
		s.ID, proto.ActionCheckExit, string(args), now(), a.ID)
	if err != nil {
		return err
	}
	aid, _ := res.LastInsertId()
	p.touchServers(s.ID)
	writeJSON(w, http.StatusAccepted, actionRef{ID: aid})
	return nil
}

// exitCheckText says in words what a finished check found (the action's output), for the assistant.
func exitCheckText(node, server, status, output string) string {
	var res proto.ExitResult
	switch {
	case status == "pending":
		return fmt.Sprintf("The check of %s from %s is still running - ask again in a few seconds (action_status).", node, server)
	case json.Unmarshal([]byte(output), &res) != nil:
		if status == "failed" && len(output) > 0 {
			return fmt.Sprintf("%s could not run the check: %s (an agent older than 1.0 cannot - upgrade it).", server, output)
		}
		return fmt.Sprintf("%s could not run the check.", server)
	case !res.OK:
		return fmt.Sprintf("%s cannot use %s: %s.", server, node, res.Error)
	case res.TLS:
		return fmt.Sprintf("%s reaches %s at %s in %d ms, with a valid certificate for its server name.", server, node, res.Addr, res.MS)
	}
	return fmt.Sprintf("%s reaches %s at %s in %d ms.", server, node, res.Addr, res.MS)
}

// exitCheckWait is how long the MCP tool waits for a check's answer before handing back its action.
var exitCheckWait = 15 * time.Second

// waitExitCheck waits for check action id and says what it found.
func (p *Panel) waitExitCheck(ctx context.Context, id int64, node, server string) string {
	deadline := time.Now().Add(exitCheckWait)
	for {
		var status, output string
		if err := p.db.QueryRowContext(ctx, `SELECT status, output FROM actions WHERE id = ?`, id).Scan(&status, &output); err != nil {
			return fmt.Sprintf("The check cannot be found (action %d).", id)
		}
		if status != "pending" || !time.Now().Before(deadline) {
			text := exitCheckText(node, server, status, output)
			if status == "pending" {
				text = fmt.Sprintf("%s Action id %d.", text, id)
			}
			return text
		}
		select {
		case <-ctx.Done():
			return fmt.Sprintf("The check of %s from %s is still running - ask action_status %d.", node, server, id)
		case <-time.After(500 * time.Millisecond):
		}
	}
}
