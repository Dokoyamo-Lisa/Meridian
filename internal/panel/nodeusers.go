package panel

import (
	"database/sql"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// Who can use a protocol, seen from the protocol: every user, with how they have it - everything
// (all servers, including new ones), the whole server (with protocols added to it later), this one
// protocol, or not at all - and one step that gives it to some users and takes it from others.
// Taking it from someone who has it through everything or the whole server needs split: their
// access is then written out as the servers and protocols they have now, less this one, and they
// no longer get new ones by themselves - the panel says so before it happens.

type nodeUser struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Paused bool   `json:"paused"`
	PlanID int64  `json:"plan_id"`
	Access string `json:"access" doc:"all (everything, including new servers) | server (the whole server, with protocols added later) | protocol (this protocol) | none"`
}

type nodeUsersView struct {
	Users    []nodeUser `json:"users"`
	PassOnly bool       `json:"pass_only" doc:"The protocol serves only proxy passes: users cannot connect to it directly, whatever their access"`
}

func accessOf(sc Scope, n *Node) string {
	switch {
	case sc.All():
		return "all"
	case slices.Contains(sc.Servers, n.ServerID):
		return "server"
	case slices.Contains(sc.Nodes, n.ID):
		return "protocol"
	}
	return "none"
}

func (p *Panel) apiNodeUsers(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	n, srv, err := p.ownNode(r.Context(), a, id)
	if err != nil {
		return err
	}
	subs, err := p.subsOf(r.Context(), srv.AccountID)
	if err != nil {
		return err
	}
	out := nodeUsersView{Users: []nodeUser{}, PassOnly: n.PassOnly}
	for _, s := range subs {
		out.Users = append(out.Users, nodeUser{ID: s.ID, Name: s.Name, Paused: s.Paused, PlanID: s.PlanID, Access: accessOf(s.Scope, n)})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type nodeUsersInput struct {
	Give  []int64 `json:"give" doc:"Users who get this protocol (those who have it already are left as they are)"`
	Take  []int64 `json:"take" doc:"Users who lose it"`
	Split bool    `json:"split" doc:"Also take it from users who have it through everything or the whole server: their access becomes the servers and protocols they have now, less this one - they no longer get new ones by themselves. Without it such users are refused (409)"`
}

type nodeUsersResult struct {
	Given []string `json:"given" doc:"Users who got it"`
	Taken []string `json:"taken" doc:"Users who lost it"`
	Split []string `json:"split" doc:"Users whose whole-server or everything access was written out to lose it"`
}

func (p *Panel) apiSetNodeUsers(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	n, srv, err := p.ownNode(r.Context(), a, id)
	if err != nil {
		return err
	}
	var in nodeUsersInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if len(in.Give)+len(in.Take) > 10000 {
		return errStatus(http.StatusBadRequest, "at most 10000 users at a time")
	}
	for _, x := range in.Give {
		if slices.Contains(in.Take, x) {
			return errStatus(http.StatusBadRequest, fmt.Sprintf("user %d is both given and taken the protocol", x))
		}
	}
	subs, err := p.subsOf(r.Context(), srv.AccountID)
	if err != nil {
		return err
	}
	byID := map[int64]*Sub{}
	for _, s := range subs {
		byID[s.ID] = s
	}
	for _, x := range append(slices.Clone(in.Give), in.Take...) {
		if byID[x] == nil {
			return errStatus(http.StatusBadRequest, fmt.Sprintf("there is no user %d", x))
		}
	}
	// what a written-out access holds: every server of the account, and this server's other protocols
	servers, err := p.serversOf(r.Context(), srv.AccountID)
	if err != nil {
		return err
	}
	siblings, err := p.nodesOf(r.Context(), srv.ID)
	if err != nil {
		return err
	}
	res := nodeUsersResult{Given: []string{}, Taken: []string{}, Split: []string{}}
	changed := map[int64]Scope{}
	for _, x := range in.Give {
		s := byID[x]
		if accessOf(s.Scope, n) != "none" {
			continue
		}
		sc := Scope{Servers: slices.Clone(s.Scope.Servers), Nodes: append(slices.Clone(s.Scope.Nodes), n.ID)}
		changed[x] = sc
		res.Given = append(res.Given, s.Name)
	}
	var refused []string
	for _, x := range in.Take {
		s := byID[x]
		sc := Scope{Servers: slices.Clone(s.Scope.Servers), Nodes: slices.Clone(s.Scope.Nodes)}
		switch accessOf(s.Scope, n) {
		case "none":
			continue
		case "protocol":
			sc.Nodes = slices.DeleteFunc(sc.Nodes, func(v int64) bool { return v == n.ID })
		case "all", "server":
			if !in.Split {
				refused = append(refused, s.Name)
				continue
			}
			if s.Scope.All() { // every server but this one, as whole servers
				sc = Scope{}
				for _, o := range servers {
					if o.ID != srv.ID && o.DeletedAt == 0 {
						sc.Servers = append(sc.Servers, o.ID)
					}
				}
			} else {
				sc.Servers = slices.DeleteFunc(sc.Servers, func(v int64) bool { return v == srv.ID })
			}
			for _, o := range siblings { // and this server's other protocols, one by one
				if o.ID != n.ID && !o.PassOnly && !slices.Contains(sc.Nodes, o.ID) {
					sc.Nodes = append(sc.Nodes, o.ID)
				}
			}
			res.Split = append(res.Split, s.Name)
		}
		sc.None = len(sc.Servers) == 0 && len(sc.Nodes) == 0
		changed[x] = sc
		res.Taken = append(res.Taken, s.Name)
	}
	if len(refused) > 0 {
		return errStatus(http.StatusConflict, fmt.Sprintf("%s %s it through everything or the whole server - send split=true to write their access out without it (they then no longer get new servers or protocols by themselves)",
			strings.Join(refused, ", "), map[bool]string{true: "has", false: "have"}[len(refused) == 1]))
	}
	if len(changed) > 0 {
		t := now()
		if err := p.db.Write(r.Context(), func(tx *sql.Tx) error {
			for x, sc := range changed {
				if _, err := tx.Exec(`UPDATE subs SET scope = ?, updated_at = ? WHERE id = ?`, sc.String(), t, x); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		title := p.nodeTitle(r.Context(), n)
		if len(res.Given) > 0 {
			p.event(srv.AccountID, "info", "access_changed", srv.ID, 0, a.ID, fmt.Sprintf("%s gave %s to %s", a.Username, title, listNames(res.Given)), nil)
		}
		if len(res.Taken) > 0 {
			msg := fmt.Sprintf("%s took %s from %s - their devices connected through it are disconnected", a.Username, title, listNames(res.Taken))
			p.event(srv.AccountID, "warn", "access_changed", srv.ID, 0, a.ID, msg, nil)
		}
		p.touchAccount(srv.AccountID)
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

// listNames shortens a long list of names for the timeline.
func listNames(names []string) string {
	if len(names) <= 6 {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:5], ", "), len(names)-5)
}
