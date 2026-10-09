package panel

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"meridian/internal/proto"
	"meridian/internal/seal"
)

// Shared servers: a server's owner can share it with up to two more panels. The other panel adds
// the server as shared with it and gets a share code - its address and the agent token of its record
// - which the owner pastes on the server's page; the agent then reports to that panel too and runs
// what it sets up: protocols, users, forwards, traffic rules - everything but the console. The host
// stays the owner's: the agent's and the cores' upgrades, the country rule, relaying, scans and
// takeovers. The agent enforces all of that (internal/agent/sharemerge.go); the panel only keeps
// people from asking for what would be refused.

const sharePrefix = "meridian-share:"

type shareCode struct {
	V     int    `json:"v"`
	Panel string `json:"panel"`
	Token string `json:"token"`
	Name  string `json:"name,omitempty"`
}

// shareCodeOf is the code a guest record gives its owner: like the install command, it carries the
// agent's secret for this record.
func (p *Panel) shareCodeOf(r *http.Request, s *Server) string {
	b, _ := json.Marshal(shareCode{V: 1, Panel: p.baseURL(r), Token: seal.Token(s.ID, s.Secret), Name: p.settings().SiteTitle})
	return sharePrefix + base64.RawURLEncoding.EncodeToString(b)
}

func parseShareCode(code string) (shareCode, error) {
	var c shareCode
	raw, ok := strings.CutPrefix(strings.TrimSpace(code), sharePrefix)
	if !ok {
		return c, errStatus(http.StatusBadRequest, "that is not a share code - it starts with "+sharePrefix)
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(b, &c) != nil || c.V != 1 {
		return c, errStatus(http.StatusBadRequest, "the share code cannot be read - copy it again, whole")
	}
	u, err := url.Parse(c.Panel)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return c, errStatus(http.StatusBadRequest, "the share code's panel address cannot be used")
	}
	if _, _, err := seal.ParseToken(c.Token); err != nil {
		return c, errStatus(http.StatusBadRequest, "the share code's token cannot be read - make a new code on the other panel")
	}
	c.Name = cleanName(c.Name, 64)
	return c, nil
}

type shareInput struct {
	Code string `json:"code" doc:"The share code the other panel gave (meridian-share:...)"`
}

type shareRemoveInput struct {
	Panel string `json:"panel" doc:"The panel to stop sharing with, as shares[].panel names it"`
}

// apiShareServer shares one of this panel's servers with another panel.
func (p *Panel) apiShareServer(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownServer(r.Context(), a, id)
	if err != nil {
		return err
	}
	var in shareInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if s.Guest {
		return errStatus(http.StatusConflict, "only the server's own panel can share it")
	}
	c, err := parseShareCode(in.Code)
	if err != nil {
		return err
	}
	if !s.caps().Share {
		return errStatus(http.StatusConflict, s.Name+"'s agent cannot be shared yet - upgrade it to 1.0 (nobody is disconnected)")
	}
	ours, _ := url.Parse(p.baseURL(r))
	theirs, _ := url.Parse(c.Panel)
	if ours != nil && strings.EqualFold(ours.Host, theirs.Host) {
		return errStatus(http.StatusBadRequest, "that share code is this panel's own - it comes from the other panel's server list (Add server › Shared with me)")
	}
	if theirs.Scheme != "https" && (ours == nil || ours.Scheme != "http") {
		return errStatus(http.StatusBadRequest, "the other panel must be reached over https")
	}
	if ls := p.live.get(s.ID); ls != nil {
		for _, sh := range ls.Live.Shares {
			if strings.EqualFold(sh.Panel, theirs.Scheme+"://"+theirs.Host) {
				return errStatus(http.StatusConflict, "the server is shared with that panel already")
			}
		}
		if len(ls.Live.Shares) >= 2 {
			return errStatus(http.StatusConflict, "a server can be shared with two panels at most - stop sharing it with one first")
		}
	}
	args, _ := json.Marshal(proto.ShareAdd{Panel: c.Panel, Token: c.Token, Name: c.Name})
	res, err := p.db.Exec1(`INSERT INTO actions (server_id, kind, args, created_at, created_by) VALUES (?, ?, ?, ?, ?)`,
		s.ID, proto.ActionShareAdd, string(args), now(), a.ID)
	if err != nil {
		return err
	}
	aid, _ := res.LastInsertId()
	p.touchServers(s.ID)
	who := theirs.Host
	if c.Name != "" {
		who = c.Name + " (" + theirs.Host + ")"
	}
	p.event(s.AccountID, "warn", "server_shared", s.ID, 0, a.ID, fmt.Sprintf("%s shared %s with the panel %s: it can run its own protocols and users there - not the console",
		a.Username, s.Name, who), nil)
	writeJSON(w, http.StatusAccepted, actionRef{ID: aid})
	return nil
}

// apiUnshareServer stops sharing a server with a panel.
func (p *Panel) apiUnshareServer(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownServer(r.Context(), a, id)
	if err != nil {
		return err
	}
	var in shareRemoveInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if s.Guest {
		return errStatus(http.StatusConflict, "this server is shared with you: remove it from your servers to leave it")
	}
	u, err := url.Parse(strings.TrimSpace(in.Panel))
	if err != nil || u.Host == "" {
		return errStatus(http.StatusBadRequest, "name the panel as the server's shares list it")
	}
	args, _ := json.Marshal(proto.ShareRemove{Panel: u.Scheme + "://" + u.Host})
	res, err := p.db.Exec1(`INSERT INTO actions (server_id, kind, args, created_at, created_by) VALUES (?, ?, ?, ?, ?)`,
		s.ID, proto.ActionShareRemove, string(args), now(), a.ID)
	if err != nil {
		return err
	}
	aid, _ := res.LastInsertId()
	p.touchServers(s.ID)
	p.event(s.AccountID, "warn", "server_unshared", s.ID, 0, a.ID, fmt.Sprintf("%s stopped sharing %s with the panel %s - its protocols there are removed and its users disconnected",
		a.Username, s.Name, u.Host), nil)
	writeJSON(w, http.StatusAccepted, actionRef{ID: aid})
	return nil
}

// ownerOnly refuses what only a server's own panel may do on a server shared with this one.
func ownerOnly(s *Server, what string) error {
	if s.Guest {
		return errStatus(http.StatusConflict, fmt.Sprintf("%s is shared with you: %s is its owner's panel's to do", s.Name, what))
	}
	return nil
}

// guestActions are the server actions a panel the server is shared with may ask for (the agent
// refuses the others).
var guestActionKinds = map[string]bool{proto.ActionRestartXray: true, proto.ActionRestartPending: true, proto.ActionCheckTarget: true,
	proto.ActionCheckExit: true}
