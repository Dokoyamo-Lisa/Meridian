package panel

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

	"meridian/internal/proto"
)

// Reinstalling a server, on the supervisor's request (agents 1.4 and later).
//
// Reinstall: the agent downloads every core the server runs again from its official release
// (checked), clears what it generated for the proxies and builds it all again from the panel - the
// same protocols, ports and keys, so users' links keep working. Everyone connected reconnects once.
//
// Clean reinstall: the server starts over as if it were just added. Its protocols and forwards are
// removed - here, as deleting each would: users' links lose them, protocols on other servers that
// pass through them block until they get another exit - and so is its Xray configuration as code;
// the agent clears the cores and everything it set up for proxies, installs fresh cores and runs the
// setup script once more. What is the server's own stays: its name, address, plan, status page,
// country rule, security mode and setup script.

const needReinstall = "reinstalling needs agent 1.4 or later on this server - upgrade its agent first (nobody is disconnected by that)"

type reinstallInput struct {
	Clean   bool `json:"clean" doc:"false: reinstall, keeping every protocol (the same ports, keys and links). true: start over - the server's protocols and forwards are removed, in the panel too"`
	Confirm bool `json:"confirm" doc:"Must be true: everyone connected to the server is disconnected for a moment (and a clean reinstall removes its protocols and forwards)"`
}

type reinstallOutput struct {
	ID        int64 `json:"id" doc:"The action: poll GET /api/actions/{id} for how it went"`
	Protocols int   `json:"protocols_removed" doc:"Clean reinstall: how many protocols were removed"`
	Forwards  int   `json:"forwards_removed" doc:"Clean reinstall: how many forwards were removed"`
}

// apiReinstallServer reinstalls a server (POST /api/servers/{id}/reinstall).
func (p *Panel) apiReinstallServer(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownServer(r.Context(), a, id)
	if err != nil {
		return err
	}
	var in reinstallInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if s.Guest {
		return ownerOnly(s, "reinstalling the server")
	}
	if !s.caps().Reinstall {
		return errStatus(http.StatusBadRequest, needReinstall)
	}
	if !in.Confirm {
		if in.Clean {
			return errStatus(http.StatusBadRequest, "confirm the clean reinstall first (confirm: true): it removes the server's protocols and forwards and disconnects everyone on it")
		}
		return errStatus(http.StatusBadRequest, "confirm the reinstall first (confirm: true): everyone connected to the server is disconnected for a moment")
	}
	out, err := p.reinstallServer(r, a, s, in.Clean)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, out)
	return nil
}

// reinstallServer removes what a clean reinstall removes and queues the agent's part.
func (p *Panel) reinstallServer(r *http.Request, a *Account, s *Server, clean bool) (*reinstallOutput, error) {
	ctx := r.Context()
	out := &reinstallOutput{}
	touch := []int64{s.ID}
	var lost []string
	var passed, ruled int
	nodes, err := p.nodesOf(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	fwds, err := p.forwardsOf(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	var nodeIDs []int64
	if clean {
		for _, n := range nodes {
			nodeIDs = append(nodeIDs, n.ID)
			entries := p.passEntries(ctx, n.ID)
			touch = append(touch, entries...)
			for _, e := range entries {
				if e != s.ID {
					passed++
				}
			}
			if n.PassNode > 0 { // its exit drops the pass user
				if x, err := p.nodeByID(ctx, n.PassNode); err == nil {
					touch = append(touch, x.ServerID)
				}
			}
			ruled += len(p.rulesSendingTo(ctx, s.AccountID, fmt.Sprintf("node:%d", n.ID)))
		}
	}
	args, _ := json.Marshal(proto.Reinstall{Core: "all", Rebuild: !clean, Clean: clean})
	var aid int64
	if err := p.db.Write(ctx, func(tx *sql.Tx) error {
		if clean {
			// as deleting each protocol does: protocols that passed through one keep their pass and block
			for _, id := range nodeIDs {
				if _, err := tx.Exec(`DELETE FROM nodes WHERE id = ?`, id); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(`DELETE FROM forwards WHERE server_id = ?`, s.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE servers SET xray_code = '' WHERE id = ?`, s.ID); err != nil {
				return err
			}
			if len(nodeIDs) > 0 {
				var err error
				if lost, err = pruneScopes(tx, s.AccountID, 0, nodeIDs); err != nil {
					return err
				}
				if err := pruneRoutes(tx, s.AccountID, nil, nodeIDs); err != nil {
					return err
				}
			}
		}
		res, err := tx.Exec(`INSERT INTO actions (server_id, kind, args, created_at, created_by) VALUES (?, ?, ?, ?, ?)`,
			s.ID, proto.ActionReinstall, string(args), now(), a.ID)
		if err != nil {
			return err
		}
		aid, err = res.LastInsertId()
		return err
	}); err != nil {
		return nil, err
	}
	out.ID = aid
	var msg string
	if clean {
		out.Protocols, out.Forwards = len(nodeIDs), len(fwds)
		msg = fmt.Sprintf("%s started %s over (clean reinstall): %d %s and %d %s removed; the agent installs fresh cores", a.Username, s.Name,
			len(nodeIDs), plural(len(nodeIDs), "protocol", "protocols"), len(fwds), plural(len(fwds), "forward", "forwards"))
		if passed > 0 {
			msg += fmt.Sprintf(" - protocols on other servers passed through them and are blocked until they get another exit (%d)", passed)
		}
		if ruled > 0 {
			msg += fmt.Sprintf(" - %d traffic rule(s) sent traffic there and block it until they get another exit", ruled)
		}
	} else {
		msg = fmt.Sprintf("%s reinstalled %s: the agent installs its cores again and builds its %d %s again (the same ports, keys and links)",
			a.Username, s.Name, len(nodes), plural(len(nodes), "protocol", "protocols"))
	}
	p.event(s.AccountID, "warn", "server_reinstalled", s.ID, 0, a.ID, msg, nil)
	if clean {
		p.touchRoutes(ctx, s.AccountID)
		p.noAccessLeft(s.AccountID, a.ID, lost)
	}
	p.touchServers(touch...)
	return out, nil
}
