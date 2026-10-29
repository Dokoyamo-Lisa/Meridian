package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strings"

	"meridian/internal/proto"
)

// A server's security mode: what its agent does by itself to keep the server safe, once the
// supervisor turned it on - in a signed-in browser, after a confirmation that says what it does,
// never from an API token, an assistant or the bot - and until they turn it off:
//
//   - ssh_ban: addresses that keep failing to sign in over SSH are blocked from SSH for a while (as
//     fail2ban does) - never a private address, one that signed in lately, or a trusted one;
//   - keys_only: SSH takes keys only, once an account that may sign in has a key;
//   - stop_miners: a known crypto-miner the health check finds is stopped at once.
//
// The agent says what it does (guard_now on the server) and puts each block and each stop in the
// timeline. The panel's own addresses and the one the supervisor turned it on from are trusted.

const needGuard = "the security mode needs agent 1.4 or later on this server - upgrade its agent first (nobody is disconnected by that)"

// guardFor is the security mode for a server's state: nil while it is off, for a server shared with
// this panel (its host is its owner's), or for an agent too old for it.
func (p *Panel) guardFor(ctx context.Context, srv *Server) *proto.Guard {
	g := srv.Guard
	if g == nil || srv.Guest || !srv.caps().Guard || (!g.SSHBan && !g.KeysOnly && !g.StopMiners) {
		return nil
	}
	out := &proto.Guard{SSHBan: g.SSHBan, KeysOnly: g.KeysOnly, StopMiners: g.StopMiners}
	trusted := append([]string{}, g.Trusted...)
	pa := p.panelAddr(ctx, false)
	trusted = append(append(trusted, pa.IPv4...), pa.IPv6...)
	for _, t := range trusted {
		if a, err := netip.ParseAddr(t); err == nil && !slices.Contains(out.Trusted, a.Unmap().String()) {
			out.Trusted = append(out.Trusted, a.Unmap().String())
		}
	}
	return out
}

type guardInput struct {
	SSHBan     bool `json:"ssh_ban" doc:"Block addresses that keep failing to sign in over SSH, for a while (as fail2ban does)"`
	KeysOnly   bool `json:"keys_only" doc:"SSH takes keys only (the agent first checks that an account that may sign in has a key)"`
	StopMiners bool `json:"stop_miners" doc:"Stop a known crypto-miner the health check finds, at once"`
	Confirm    bool `json:"confirm" doc:"Must be true: the supervisor saw what the security mode does"`
}

type guardOutput struct {
	Guard *GuardSettings `json:"guard" doc:"The security mode now (null: off)"`
}

// apiPutGuard turns a server's security mode on, changes it or turns it off (all three false) - from a
// signed-in browser only (routes.go: sessionOnly), after a confirmation.
func (p *Panel) apiPutGuard(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownServer(r.Context(), a, id)
	if err != nil {
		return err
	}
	var in guardInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if !in.Confirm {
		return errStatus(http.StatusBadRequest, "confirm the security mode first (confirm: true)")
	}
	if s.Guest {
		return ownerOnly(s, "the security mode")
	}
	on := in.SSHBan || in.KeysOnly || in.StopMiners
	if on && !s.caps().Guard {
		return errStatus(http.StatusBadRequest, needGuard)
	}
	var next *GuardSettings
	raw := ""
	if on {
		next = &GuardSettings{SSHBan: in.SSHBan, KeysOnly: in.KeysOnly, StopMiners: in.StopMiners, At: now(), By: a.Username, Trusted: []string{}}
		if s.Guard != nil {
			next.Trusted = append(next.Trusted, s.Guard.Trusted...)
		}
		if ip, err := netip.ParseAddr(p.clientIP(r)); err == nil && publicAddr(ip) && !slices.Contains(next.Trusted, ip.Unmap().String()) {
			next.Trusted = append(next.Trusted, ip.Unmap().String())
			if len(next.Trusted) > 8 {
				next.Trusted = next.Trusted[len(next.Trusted)-8:]
			}
		}
		b, _ := json.Marshal(next)
		raw = string(b)
	}
	if _, err := p.db.Exec1(`UPDATE servers SET guard = ? WHERE id = ?`, raw, s.ID); err != nil {
		return err
	}
	msg := fmt.Sprintf("%s turned the security mode off on %s", a.Username, s.Name)
	if on {
		var parts []string
		if in.SSHBan {
			parts = append(parts, "blocking addresses that keep guessing SSH passwords")
		}
		if in.KeysOnly {
			parts = append(parts, "SSH by keys only")
		}
		if in.StopMiners {
			parts = append(parts, "stopping crypto-miners")
		}
		msg = fmt.Sprintf("%s turned the security mode on for %s: %s", a.Username, s.Name, strings.Join(parts, ", "))
	}
	p.event(s.AccountID, "warn", "guard", s.ID, 0, a.ID, msg, nil)
	p.touchServers(s.ID)
	writeJSON(w, http.StatusOK, guardOutput{Guard: next})
	return nil
}
