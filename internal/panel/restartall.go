package panel

import (
	"fmt"
	"net/http"

	"meridian/internal/proto"
)

// Restarting everything: one click for what would otherwise wait for a restart server by server (the
// product never restarts cores by itself). Strict mode needs it after agents are upgraded: until Xray
// restarts, a long connection's data is counted only when it ends, so a user can run past their
// quota on it before anything is cut; the users' own servers move to their account without rights
// the same way. And a clean start whenever the supervisor wants one.

// needRestartAll is why a server cannot restart everything.
const needRestartAll = "restarting everything needs agent 1.3.1 or later on this server - upgrade its agent first (Settings › Updates › Upgrade all agents; nobody is disconnected by that)"

type restartAllResult struct {
	Servers []string       `json:"servers" doc:"The servers that restart everything now"`
	Skipped []agentSkipped `json:"skipped" doc:"Servers left out, each with the reason (offline, an older agent, shared with you)"`
}

// apiRestartAll restarts everything that carries traffic on every server of the account - Xray,
// Hysteria2, each user's own server (mieru, Snell, AnyTLS), realm forwards - and then each agent.
// Everyone reconnects by themselves within seconds.
func (p *Panel) apiRestartAll(w http.ResponseWriter, r *http.Request, a *Account) error {
	servers, err := p.serversOf(r.Context(), scopeAccount(r, a))
	if err != nil {
		return err
	}
	out := restartAllResult{Servers: []string{}, Skipped: []agentSkipped{}}
	var ids []int64
	for _, s := range servers {
		why := ""
		switch {
		case s.Guest:
			why = "shared with you: its own panel restarts it"
		case s.FirstSeenAt == 0:
			why = "its agent has not connected yet"
		case !s.Online:
			why = "offline"
		case !s.caps().RestartAll:
			why = "agent older than 1.3.1 - upgrade it first (nobody is disconnected by that)"
		}
		if why != "" {
			out.Skipped = append(out.Skipped, agentSkipped{Name: s.Name, Why: why})
			continue
		}
		if _, err := p.db.Exec1(`INSERT INTO actions (server_id, kind, args, created_at, created_by) VALUES (?, ?, '{}', ?, ?)`,
			s.ID, proto.ActionRestartAll, now(), a.ID); err != nil {
			return err
		}
		out.Servers = append(out.Servers, s.Name)
		ids = append(ids, s.ID)
	}
	if len(out.Servers) > 0 {
		msg := fmt.Sprintf("%s restarted everything on %d server%s: Xray, Hysteria2, users' own servers, realm forwards and the agents - devices reconnect by themselves",
			a.Username, len(out.Servers), plural(len(out.Servers), "", "s"))
		if len(out.Skipped) > 0 {
			msg += ". Left out: " + skippedText(out.Skipped)
		}
		p.event(a.ID, "warn", "restart_all", 0, 0, a.ID, msg, nil)
		p.touchServers(ids...)
	}
	writeJSON(w, http.StatusAccepted, out)
	return nil
}
