package panel

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"

	"meridian/internal/proto"
)

// Agent relays. A server whose agent keeps losing the panel - a server in one country, the panel in
// another, a poor route between them - can reach the panel through another server the operator
// chooses (panel_relay): its agent dials that server's agent, which passes the connection on to the
// panel unread (TLS and the sealed bodies stay end to end). The panel notices servers that keep
// losing it, says so on the overview and in a notification, and - when Settings names an automatic
// relay - moves such a server there by itself, once. One hop only: a relay reaches the panel
// directly, and a relayed server relays nobody.

const (
	troubleWindow   = 24 * 3600 // seconds looked back
	troubleDrops    = 3         // a server that lost the panel this often within the window...
	troubleShare    = 0.01      // ...or was without it this share of the window, coming back in between
	troubleTogether = 120       // seconds: most servers dropping within this of each other is the panel's own trouble
	troubleRefresh  = 60        // seconds the computed troubles are kept
	relayHandsOff   = 7 * 86400 // the automatic relay leaves a server alone this long after its way was chosen
	relayAfterGone  = 7 * 86400 // a relay lets a deleted server in this long, so its agent hears it was deleted
)

// relayCache keeps which servers keep losing the panel, computed at most once a minute, and which of
// those the timeline was told about.
type relayCache struct {
	mu      sync.Mutex
	at      int64
	trouble map[int64]string // server id -> why it keeps losing the panel
	told    map[int64]bool
}

// ---------------------------------------------------------------- servers that keep losing the panel

// troubles says, for every server that keeps losing the panel, why.
func (p *Panel) troubles(ctx context.Context) map[int64]string {
	p.relays.mu.Lock()
	defer p.relays.mu.Unlock()
	if p.relays.trouble == nil || now()-p.relays.at >= troubleRefresh {
		p.relays.trouble, p.relays.at = p.computeTroubles(ctx), now()
	}
	return p.relays.trouble
}

// forgetTroubles has the next look compute them afresh.
func (p *Panel) forgetTroubles() {
	p.relays.mu.Lock()
	p.relays.trouble = nil
	p.relays.mu.Unlock()
}

type outage struct {
	start, end int64 // end 0: not back yet
	drop       bool  // it went offline within the window (not before it)
	own        bool  // the server's route: no reboot, no agent restart, not nearly every server at once
}

// computeTroubles reads the last day's timeline. A server keeps losing the panel when it went offline
// troubleDrops times or more, or was offline more than troubleShare of the time and came back in
// between. A drop that came with a reboot or an agent restart is the server's own doing, not its
// route's; one that nearly all of the account's servers shared is the panel's (or its network's) and
// counts for none of them.
func (p *Panel) computeTroubles(ctx context.Context) map[int64]string {
	out := map[int64]string{}
	t := now()
	from := t - troubleWindow
	servers, err := p.serversOf(ctx, 0)
	if err != nil {
		return out
	}
	var first int64 // the last event before the window: everything after it is in
	_ = p.db.QueryRowContext(ctx, `SELECT id FROM events WHERE ts < ? ORDER BY id DESC LIMIT 1`, from).Scan(&first)
	rows, err := p.db.QueryContext(ctx, `SELECT ts, kind, server_id FROM events WHERE id > ? AND server_id > 0 AND kind IN
		('server_offline', 'server_online', 'server_rebooted', 'agent_restarted', 'agent_reinstalled') ORDER BY id`, first)
	if err != nil {
		slog.Warn("panel trouble", "err", err)
		return out
	}
	type ev struct {
		ts   int64
		kind string
	}
	byServer := map[int64][]ev{}
	for rows.Next() {
		var e ev
		var sid int64
		if rows.Scan(&e.ts, &e.kind, &sid) == nil {
			byServer[sid] = append(byServer[sid], e)
		}
	}
	rows.Close()
	connected := map[int64][]int64{} // account -> its servers whose agent has connected
	for _, s := range servers {
		if s.FirstSeenAt > 0 {
			connected[s.AccountID] = append(connected[s.AccountID], s.ID)
		}
	}
	// shared says whether three quarters or more of the account's other servers (two at least) went
	// offline around ts as well
	shared := func(s *Server, ts int64) bool {
		others := len(connected[s.AccountID]) - 1
		if others < 2 {
			return false
		}
		n := 0
		for _, id := range connected[s.AccountID] {
			if id == s.ID {
				continue
			}
			for _, e := range byServer[id] {
				if e.kind == "server_offline" && e.ts >= ts-troubleTogether && e.ts <= ts+troubleTogether {
					n++
					break
				}
			}
		}
		return 4*n >= 3*others
	}
	for _, s := range servers {
		if s.FirstSeenAt == 0 {
			continue
		}
		evs := byServer[s.ID]
		var list []outage
		var cur *outage
		for _, e := range evs {
			switch e.kind {
			case "server_offline":
				if e.ts >= from {
					list = append(list, outage{start: e.ts, drop: true, own: !shared(s, e.ts)})
					cur = &list[len(list)-1]
				}
			case "server_online":
				switch {
				case cur != nil:
					cur.end, cur = e.ts, nil
				case len(list) == 0 && e.ts >= from: // offline since before the window
					list = append(list, outage{start: from, end: e.ts, own: true})
				}
			}
		}
		drops, offline, back := 0, int64(0), false
		for i := range list {
			o := &list[i]
			for _, e := range evs { // it came back from a reboot or an agent restart
				if e.kind != "server_offline" && e.kind != "server_online" && e.ts >= o.start && o.end > 0 && e.ts <= o.end+5 {
					o.own = false
				}
			}
			if !o.own {
				continue
			}
			if o.drop {
				drops++
			}
			if o.end > 0 {
				offline += o.end - max(o.start, from)
				back = true
			}
		}
		switch {
		case drops >= troubleDrops:
			out[s.ID] = fmt.Sprintf("Lost the panel %d times in the last 24 hours", drops)
		case back && s.Online && float64(offline) > troubleShare*troubleWindow:
			out[s.ID] = fmt.Sprintf("Could not reach the panel for %s of the last 24 hours", longDuration(offline))
		}
	}
	return out
}

// longDuration is "40 min" or "3 h 10 min".
func longDuration(sec int64) string {
	m := (sec + 59) / 60
	if m < 60 {
		return fmt.Sprintf("%d min", m)
	}
	if m%60 == 0 {
		return fmt.Sprintf("%d h", m/60)
	}
	return fmt.Sprintf("%d h %d min", m/60, m%60)
}

// lowerFirst starts a sentence in the middle of another.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// relayJob tells the timeline - once a day at most for each server - that a server keeps losing the
// panel, and moves such a server to the automatic relay when Settings names one. Runs every minute.
func (p *Panel) relayJob(ctx context.Context) {
	p.forgetTroubles()
	trouble := p.troubles(ctx)
	servers, err := p.serversOf(ctx, 0)
	if err != nil {
		return
	}
	t := now()
	// a server deleted a week ago is let in no more (see relayAllowed)
	if rows, err := p.db.QueryContext(ctx, `SELECT DISTINCT panel_relay FROM servers WHERE panel_relay > 0 AND deleted_at > ?
		AND deleted_at <= ?`, t-relayAfterGone-180, t-relayAfterGone); err == nil {
		var ids []int64
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		p.touchServers(ids...)
	}
	happened := func(id int64, since int64, kinds ...string) bool {
		var n int
		_ = p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE server_id = ? AND ts >= ? AND kind IN (`+
			strings.TrimSuffix(strings.Repeat("?, ", len(kinds)), ", ")+`)`, append([]any{id, since}, anys(kinds)...)...).Scan(&n)
		return n > 0
	}
	for _, s := range servers {
		why := trouble[s.ID]
		p.relays.mu.Lock()
		if p.relays.told == nil {
			p.relays.told = map[int64]bool{}
		}
		told := p.relays.told[s.ID]
		p.relays.told[s.ID] = why != ""
		p.relays.mu.Unlock()
		if why == "" {
			continue
		}
		// the automatic relay takes it - unless a person chose its way lately
		if auto := p.settings().AutoRelay; auto > 0 && s.PanelRelay == 0 && auto != s.ID && !s.Guest &&
			!happened(s.ID, t-relayHandsOff, "panel_relay", "panel_relay_auto") {
			if r, err := p.serverByID(ctx, auto); err == nil && r.AccountID == s.AccountID && r.Online && trouble[r.ID] == "" {
				if err := p.setPanelRelay(ctx, s, r.ID, 0, why); err == nil {
					continue // the switch is in the timeline, with why
				} else if !told {
					why += " - the automatic relay through " + r.Name + " could not take it: " + err.Error()
				}
			}
		}
		if told || happened(s.ID, t-86400, "panel_trouble", "panel_relay_auto") {
			continue
		}
		msg := fmt.Sprintf("%s keeps losing the panel: %s", s.Name, lowerFirst(why))
		if s.PanelRelay > 0 {
			msg = fmt.Sprintf("%s keeps losing the panel, even through its relay: %s", s.Name, lowerFirst(why))
		} else if !strings.Contains(why, "automatic relay") {
			msg += " - choose a server for it to reach the panel through on its page (Connection to the panel)"
		}
		p.event(s.AccountID, "warn", "panel_trouble", s.ID, 0, 0, msg, nil)
	}
}

func anys(list []string) []any {
	out := make([]any, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}

// ---------------------------------------------------------------- choosing the way

// checkPanelRelay says whether server s may reach the panel through relayID (0 = directly), and
// returns the relay with the port it listens on for s.
func (p *Panel) checkPanelRelay(ctx context.Context, s *Server, relayID int64) (*Server, int, error) {
	bad := func(msg string, a ...any) error { return errStatus(http.StatusBadRequest, fmt.Sprintf(msg, a...)) }
	if relayID == 0 || relayID == s.PanelRelay { // directly, or no change
		return nil, 0, nil
	}
	if relayID < 0 {
		return nil, 0, bad("panel_relay is a server's id, or 0 for directly")
	}
	if relayID == s.ID {
		return nil, 0, bad("a server cannot reach the panel through itself - choose another server, or 0 for directly")
	}
	if err := ownerOnly(s, "how its agent reaches the panels"); err != nil { // sharing.go: the agent would ignore it
		return nil, 0, err
	}
	r, err := p.serverByID(ctx, relayID)
	if err != nil || r.DeletedAt > 0 || r.AccountID != s.AccountID {
		return nil, 0, bad("the relay must be another of your servers")
	}
	if r.Guest {
		return nil, 0, bad("%s is shared with you: it cannot pass other servers through to this panel - choose one of your own", r.Name)
	}
	switch {
	case s.FirstSeenAt == 0:
		return nil, 0, bad("%s has not connected yet: its relay needs to know its address - choose one once its agent has connected", s.Name)
	case !s.caps().Relay:
		return nil, 0, bad("%s's agent cannot reach the panel through a relay yet - upgrade it first (More actions › Upgrade agent)", s.Name)
	case len(relayAllow([]*Server{s})) == 0:
		return nil, 0, bad("the panel does not know %s's address yet - wait for its agent to report it", s.Name)
	case r.FirstSeenAt == 0:
		return nil, 0, bad("%s has not connected yet - install its agent first", r.Name)
	case !r.caps().Relay:
		return nil, 0, bad("%s's agent cannot relay yet - upgrade it first (More actions › Upgrade agent)", r.Name)
	case r.PanelRelay != 0:
		return nil, 0, bad("%s reaches the panel through another server itself - a relay must reach it directly (one hop only)", r.Name)
	}
	if names := p.relayedNames(ctx, s); len(names) > 0 {
		return nil, 0, bad("%s passes %s through to the panel, so it must reach the panel directly itself - move them first", s.Name, strings.Join(names, ", "))
	}
	if p.settings().AutoRelay == s.ID {
		return nil, 0, bad("%s is the automatic relay (Settings) and must reach the panel directly - choose another automatic relay first", s.Name)
	}
	if why := relayUnreachable(r, s); why != "" {
		return nil, 0, bad("%s", why)
	}
	port, err := p.relayPortFor(ctx, r)
	if err != nil {
		return nil, 0, err
	}
	r.RelayPort = port
	return r, port, nil
}

// setPanelRelay makes server s reach the panel through relayID (0 = directly). by is who chose it;
// 0 with why is the automatic relay. Nothing restarts: the agent switches within a minute and its
// users notice nothing.
func (p *Panel) setPanelRelay(ctx context.Context, s *Server, relayID, by int64, why string) error {
	if relayID == s.PanelRelay {
		return nil
	}
	r, port, err := p.checkPanelRelay(ctx, s, relayID)
	if err != nil {
		return err
	}
	old := s.PanelRelay
	err = p.db.Write(ctx, func(tx *sql.Tx) error {
		// once more inside the write: a relay never relayed itself, a relayed server relaying nobody
		if r != nil {
			var deleted, via int64
			if err := tx.QueryRow(`SELECT deleted_at, panel_relay FROM servers WHERE id = ?`, r.ID).Scan(&deleted, &via); err != nil {
				return err
			}
			var relaying int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM servers WHERE panel_relay = ? AND deleted_at = 0`, s.ID).Scan(&relaying); err != nil {
				return err
			}
			if deleted > 0 || via != 0 || relaying > 0 {
				return errStatus(http.StatusConflict, "the servers changed meanwhile - try again")
			}
			if _, err := tx.Exec(`UPDATE servers SET relay_port = ? WHERE id = ?`, port, r.ID); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`UPDATE servers SET panel_relay = ? WHERE id = ?`, relayID, s.ID)
		return err
	})
	if err != nil {
		return err
	}
	switch {
	case r == nil:
		p.event(s.AccountID, "info", "panel_relay", s.ID, 0, by, fmt.Sprintf("%s reaches the panel directly again - its agent switches within a minute; nothing changes for its users", s.Name), nil)
	case by == 0:
		p.event(s.AccountID, "warn", "panel_relay_auto", s.ID, 0, 0, fmt.Sprintf("%s kept losing the panel (%s) - the automatic relay switched it to reach the panel through %s (port %d). Nothing changed for its users; switch it back on its page at any time",
			s.Name, lowerFirst(why), r.Name, port), nil)
	default:
		p.event(s.AccountID, "info", "panel_relay", s.ID, 0, by, fmt.Sprintf("%s reaches the panel through %s now (port %d) - its agent switches within a minute; nothing changes for its users", s.Name, r.Name, port), nil)
	}
	p.touchServers(s.ID)
	for _, id := range []int64{old, relayID} { // their relays learn whom they pass through
		if id > 0 {
			p.touchServers(id)
		}
	}
	return nil
}

// relayPortFor is the TCP port relay r listens on for the servers it relays: the one it has while
// that is still free, else a new one picked like a protocol's - away from protocols, forwards, the
// agent's own ports and other programs, and on a server whose provider decides its ports, one the
// provider forwards.
func (p *Panel) relayPortFor(ctx context.Context, r *Server) (int, error) {
	nodes, err := p.nodesOf(ctx, r.ID)
	if err != nil {
		return 0, err
	}
	fwds, err := p.forwardsOf(ctx, r.ID)
	if err != nil {
		return 0, err
	}
	var host []int
	if ls := p.live.get(r.ID); ls != nil {
		host = ls.Live.Ports
	}
	if r.RelayPort > 0 {
		hp := host
		if len(p.relayAllowed(ctx, r)) > 0 { // it listens there already itself
			hp = slices.DeleteFunc(slices.Clone(host), func(x int) bool { return x == r.RelayPort })
		}
		if relayPortFree(r, r.RelayPort, nodes, fwds, hp) {
			return r.RelayPort, nil
		}
	}
	free := func(port int) bool { return relayPortFree(r, port, nodes, fwds, host) }
	port := 0
	if r.ports != nil {
		r.ports.locals(true, false, func(l int) bool {
			if free(l) {
				port = l
			}
			return port != 0
		})
	} else {
		for i := 0; i < 1000 && port == 0; i++ {
			if x := 20000 + int(randUint32()%40000); free(x) {
				port = x
			}
		}
	}
	if port == 0 {
		return 0, errStatus(http.StatusConflict, fmt.Sprintf("no free port left on %s for the relay - %s", r.Name, lowerFirst(noFreePort(r))))
	}
	return port, nil
}

// relayPortFree says whether the relay on r may listen on port.
func relayPortFree(r *Server, port int, nodes []*Node, fwds []*Forward, hostPorts []int) bool {
	api := r.caps().APIPort
	if api == 0 {
		api = 50000 // agents before 0.4.2
	}
	if port < 1024 || port > 65535 || port == api || port == api+1 || port == r.ports.acmePort() {
		return false
	}
	if _, ok := r.ports.public(port, true, false); !ok {
		return false
	}
	return portConflict(port, true, false, nodes, fwds, 0, 0, hostPorts) == ""
}

// relayPorts is where server id listens for the servers it relays, while it relays any: no protocol
// or forward may take that port (see hostPorts).
func (p *Panel) relayPorts(id int64) []int {
	var port int
	if p.db.QueryRow(`SELECT relay_port FROM servers s WHERE id = ? AND relay_port > 0 AND EXISTS
		(SELECT 1 FROM servers x WHERE x.panel_relay = s.id AND x.id != s.id AND (x.deleted_at = 0 OR x.deleted_at > ?))`,
		id, now()-relayAfterGone).Scan(&port) != nil {
		return nil
	}
	return []int{port}
}

// checkAutoRelay checks Settings' automatic relay (0 = off).
func (p *Panel) checkAutoRelay(ctx context.Context, a *Account, id int64) error {
	if id == 0 {
		return nil
	}
	r, err := p.ownServer(ctx, a, id)
	if err != nil {
		return errStatus(http.StatusBadRequest, "the automatic relay must be one of your servers")
	}
	switch {
	case r.Guest:
		return errStatus(http.StatusBadRequest, r.Name+" is shared with you: it cannot relay for this panel - choose one of your own servers")
	case r.FirstSeenAt == 0:
		return errStatus(http.StatusBadRequest, r.Name+" has not connected yet - install its agent first")
	case !r.caps().Relay:
		return errStatus(http.StatusBadRequest, r.Name+"'s agent cannot relay yet - upgrade it first (its page, More actions › Upgrade agent)")
	case r.PanelRelay != 0:
		return errStatus(http.StatusBadRequest, r.Name+" reaches the panel through another server itself - a relay must reach it directly")
	case len(relayIPs(r)) == 0 && relayName(r) == "":
		return errStatus(http.StatusBadRequest, r.Name+" has no public IP address other servers could connect to - give it one, or a domain name as its address")
	}
	return nil
}

// relayGone tidies up after server s was deleted: the servers it relayed reach the panel directly
// again, its own relay forgets it, and an automatic relay that was s is off.
func (p *Panel) relayGone(ctx context.Context, s *Server, by int64) {
	relayed := p.relayedThrough(ctx, s)
	if _, err := p.db.Exec1(`UPDATE servers SET panel_relay = 0 WHERE panel_relay = ?`, s.ID); err != nil {
		slog.Warn("relay", "err", err)
		return
	}
	for _, x := range relayed {
		p.event(x.AccountID, "warn", "panel_relay_gone", x.ID, 0, by, fmt.Sprintf("%s reaches the panel directly again: its relay %s was deleted", x.Name, s.Name), nil)
		p.touchServers(x.ID)
	}
	if s.PanelRelay > 0 {
		p.touchServers(s.PanelRelay)
	}
	if set := p.settings(); set.AutoRelay == s.ID {
		set.AutoRelay = 0
		if err := p.saveSettings(set); err == nil {
			p.event(s.AccountID, "warn", "settings", 0, 0, by, "The automatic relay is off: "+s.Name+" was deleted - choose another one in Settings", nil)
		}
	}
}

// relayAddrsChanged follows a server's new addresses from its hello (srv holds those before it) at
// once: its relay lets them in (the allow list), and the servers it relays dial them (PanelVia).
func (p *Panel) relayAddrsChanged(ctx context.Context, srv *Server, h *proto.Hello) {
	if h == nil || (h.IPv4 == srv.IPv4 && h.IPv6 == srv.IPv6 && (h.Addrs == nil || slices.Equal(h.Addrs, srv.Addrs))) {
		return
	}
	p.touchServers(p.relayNeighbours(ctx, srv)...)
}

// relayNeighbours are the servers whose state names server s's addresses: its relay, and the servers
// it relays.
func (p *Panel) relayNeighbours(ctx context.Context, s *Server) []int64 {
	var out []int64
	if s.PanelRelay > 0 {
		out = append(out, s.PanelRelay)
	}
	for _, x := range p.relayedThrough(ctx, s) {
		out = append(out, x.ID)
	}
	return out
}

// relayServerChanged follows a change of server s's address, IP version or ports from its provider
// (the server as it is now): a relay whose port its provider no longer forwards gets another one, and
// the servers that name its addresses get them anew.
func (p *Panel) relayServerChanged(ctx context.Context, id, by int64) {
	s, err := p.serverByID(ctx, id)
	if err != nil {
		return
	}
	if len(p.relayAllowed(ctx, s)) > 0 && s.RelayPort > 0 {
		if port, err := p.relayPortFor(ctx, s); err == nil && port != s.RelayPort {
			if _, err := p.db.Exec1(`UPDATE servers SET relay_port = ? WHERE id = ?`, port, s.ID); err == nil {
				p.event(s.AccountID, "warn", "panel_relay_port", s.ID, 0, by, fmt.Sprintf("%s listens for the servers it relays to the panel on TCP port %d now (port %d is no longer forwarded to it) - a firewall in front of it must let the new port in",
					s.Name, port, s.RelayPort), nil)
				p.touchServers(s.ID)
			}
		}
	}
	p.touchServers(p.relayNeighbours(ctx, s)...)
}

// ---------------------------------------------------------------- what the agents get

// relayState fills in how a server's agent reaches the panel - through a relay (State.PanelVia), on
// HTTP requests only (Settings) - and, on a relay, whom it passes through (State.Relay).
func (p *Panel) relayState(ctx context.Context, srv *Server, st *proto.State) {
	if p.settings().AgentTransport == "http" {
		st.Agent.Transport = "http"
	}
	if r := p.relayOf(ctx, srv); r != nil {
		st.PanelVia = relayAddrs(r, srv)
	}
	if srv.RelayPort > 0 {
		if list := p.relayAllowed(ctx, srv); len(list) > 0 {
			st.Relay = &proto.Relay{Port: srv.RelayPort, Allow: relayAllow(list)}
		}
	}
}

// relayOf is the server s reaches the panel through, while it can be used.
func (p *Panel) relayOf(ctx context.Context, s *Server) *Server {
	if s.PanelRelay == 0 {
		return nil
	}
	r, err := p.serverByID(ctx, s.PanelRelay)
	if err != nil || r.DeletedAt > 0 || r.AccountID != s.AccountID || r.RelayPort == 0 || r.PanelRelay != 0 {
		return nil
	}
	return r
}

// relayedThrough are the servers that reach the panel through r.
func (p *Panel) relayedThrough(ctx context.Context, r *Server) []*Server {
	return p.relayedQuery(ctx, r, 0)
}

// relayAllowed are the servers relay r lets in: those it relays, and those deleted lately while it
// relayed them - a deleted server's agent hears that it was deleted (and removes itself, its proxies
// with it) only on its way to the panel.
func (p *Panel) relayAllowed(ctx context.Context, r *Server) []*Server {
	return p.relayedQuery(ctx, r, now()-relayAfterGone)
}

// relayedQuery lists the servers set to reach the panel through r, and those deleted after goneSince
// (0: none of them).
func (p *Panel) relayedQuery(ctx context.Context, r *Server, goneSince int64) []*Server {
	rows, err := p.db.QueryContext(ctx, `SELECT `+serverCols+` FROM servers WHERE panel_relay = ? AND account_id = ? AND id != ?
		AND (deleted_at = 0 OR (? > 0 AND deleted_at > ?)) ORDER BY sort, id`, r.ID, r.AccountID, r.ID, goneSince, goneSince)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*Server
	for rows.Next() {
		if x, err := scanServer(rows); err == nil {
			out = append(out, x)
		}
	}
	return out
}

func (p *Panel) relayedNames(ctx context.Context, r *Server) []string {
	var out []string
	for _, x := range p.relayedThrough(ctx, r) {
		out = append(out, x.Name)
	}
	return out
}

// relayIPs are the public addresses other servers reach relay r at: its IPv4 first, then IPv6, then
// an IP set by hand as its address.
func relayIPs(r *Server) []netip.Addr {
	var out []netip.Addr
	add := func(ip string) {
		if a, err := netip.ParseAddr(ip); err == nil && publicAddr(a) && !slices.Contains(out, a.Unmap()) {
			out = append(out, a.Unmap())
		}
	}
	if r.v4() {
		add(r.IPv4)
	}
	if r.v6() {
		add(r.IPv6)
	}
	add(r.addrIP())
	return out
}

// relayName is relay r's address when it is a domain name - for a server whose IP changes (dynamic
// DNS), the way to it that follows the change - or "".
func relayName(r *Server) string {
	if _, err := netip.ParseAddr(r.Address); err == nil || !validDNSName(r.Address) {
		return ""
	}
	return r.Address
}

// families says which IP versions server s reaches the internet over, as its agent reports them
// (both while that is not known).
func families(s *Server) (v4, v6 bool) {
	v4, v6 = s.v4() && s.IPv4 != "", s.v6() && s.IPv6 != ""
	if !v4 && !v6 {
		return true, true
	}
	return v4, v6
}

// relayAddrs are what server s's agent dials to reach relay r: r's public addresses - those of the
// IP versions s has first - and then r's domain name, when its address is one, each with the port
// r's provider forwards to the one the relay listens on.
func relayAddrs(r, s *Server) []string {
	port, ok := r.ports.public(r.RelayPort, true, false)
	if !ok || port < 1 || port > 65535 {
		return nil
	}
	v4, v6 := families(s)
	var first, rest []string
	for _, a := range relayIPs(r) {
		ap := netip.AddrPortFrom(a, uint16(port)).String()
		if a.Is4() && v4 || a.Is6() && v6 {
			first = append(first, ap)
		} else {
			rest = append(rest, ap)
		}
	}
	out := append(first, rest...)
	if name := relayName(r); name != "" {
		out = append(out, net.JoinHostPort(name, strconv.Itoa(port)))
	}
	return out
}

// relayUnreachable says why server s could not connect to relay r, or "".
func relayUnreachable(r, s *Server) string {
	if relayName(r) != "" {
		return "" // whatever addresses the name has (a relay with dynamic DNS among them)
	}
	ips := relayIPs(r)
	if len(ips) == 0 {
		return fmt.Sprintf("%s has no public IP address that %s could connect to - give it one, or a domain name as its address", r.Name, s.Name)
	}
	v4, v6 := families(s)
	for _, a := range ips {
		if a.Is4() && v4 || a.Is6() && v6 {
			return ""
		}
	}
	if v6 {
		return fmt.Sprintf("%s reaches the internet over IPv6 only and %s has no IPv6 address - choose a relay with IPv6, or give %s a domain name with an IPv6 (AAAA) record as its address", s.Name, r.Name, r.Name)
	}
	return fmt.Sprintf("%s has no IPv6 and %s only an IPv6 address - choose a relay with IPv4, or give %s a domain name with an IPv4 (A) record as its address", s.Name, r.Name, r.Name)
}

// relayAllow are the addresses the relayed servers' agents connect from: every address the panel
// knows of them.
func relayAllow(list []*Server) []string {
	var out []string
	for _, x := range list {
		for _, ip := range append([]string{x.IPv4, x.IPv6, x.Address}, x.Addrs...) {
			if a, err := netip.ParseAddr(ip); err == nil && a.Zone() == "" && !a.IsUnspecified() && !a.IsLoopback() {
				if v := a.Unmap().String(); !slices.Contains(out, v) {
					out = append(out, v)
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

// ---------------------------------------------------------------- what the panel shows

// panelLinkView is how a server's agent reaches the panel, on the server view.
type panelLinkView struct {
	PanelTrouble string          `json:"panel_trouble" doc:"Why the server keeps losing the panel - it went offline 3 times or more in the last 24 hours, or for more than 1% of them while it came back in between (reboots, agent restarts and drops most servers shared do not count) - in plain words; empty when it does not"`
	PanelPath    string          `json:"panel_path,omitempty" doc:"How its agent reaches the panel now: relay (through panel_relay) or direct - agents 1.0 and later, while it is online"`
	RelayError   string          `json:"relay_error,omitempty" doc:"Why the relay failed, when its agent went to the panel directly instead"`
	RelayName    string          `json:"relay_name,omitempty" doc:"The name of the server it reaches the panel through (panel_relay)"`
	RelayFor     []relayedServer `json:"relay_for,omitempty" doc:"The servers that reach the panel through this one (it listens for them on relay_port)"`
	RelayConns   int             `json:"relay_conns,omitempty" doc:"Their connections it passes through to the panel right now"`
	PanelConn    string          `json:"panel_conn,omitempty" doc:"What its agent talks to the panel over now: websocket or http - agents 1.0 and later, while it is online"`
	ConnError    string          `json:"conn_error,omitempty" doc:"Why its agent makes HTTP requests although it should use its WebSocket (agent_transport in Settings), e.g. a proxy in front of the panel that does not pass WebSockets"`
}

type relayedServer struct {
	ID   int64  `json:"id" doc:"The relayed server's id"`
	Name string `json:"name" doc:"Its name"`
}

func (p *Panel) panelLinkOf(ctx context.Context, s *Server) panelLinkView {
	v := panelLinkView{PanelTrouble: p.troubles(ctx)[s.ID]}
	if ls := p.live.get(s.ID); ls != nil && s.Online {
		v.PanelPath, v.RelayError, v.RelayConns = ls.Live.PanelPath, ls.Live.RelayError, ls.Live.RelayConns
		v.PanelConn, v.ConnError = ls.Live.PanelConn, ls.Live.ConnError
	}
	if s.PanelRelay > 0 {
		v.RelayName = "a deleted server"
		if r, err := p.serverByID(ctx, s.PanelRelay); err == nil && r.DeletedAt == 0 {
			v.RelayName = r.Name
		}
	}
	for _, x := range p.relayedThrough(ctx, s) {
		v.RelayFor = append(v.RelayFor, relayedServer{ID: x.ID, Name: x.Name})
	}
	return v
}

// relayAlerts are the overview's items about servers and the panel: those that keep losing it, and
// relayed ones whose relay failed.
func (p *Panel) relayAlerts(ctx context.Context, servers []*Server) []alert {
	trouble := p.troubles(ctx)
	names := map[int64]string{}
	for _, s := range servers {
		names[s.ID] = s.Name
	}
	var out []alert
	for _, s := range servers {
		relay := nz(names[s.PanelRelay], "its relay")
		if why := trouble[s.ID]; why != "" {
			msg := fmt.Sprintf("%s keeps losing the panel: %s - choose a server for it to reach the panel through", s.Name, lowerFirst(why))
			if s.PanelRelay > 0 {
				msg = fmt.Sprintf("%s keeps losing the panel, even through %s: %s", s.Name, relay, lowerFirst(why))
			}
			out = append(out, alert{Level: "warn", Kind: "panel_trouble", ServerID: s.ID, Message: msg})
		}
		if ls := p.live.get(s.ID); s.PanelRelay > 0 && s.Online && ls != nil && ls.Live.PanelPath == "direct" && ls.Live.RelayError != "" {
			out = append(out, alert{Level: "warn", Kind: "relay_failed", ServerID: s.ID,
				Message: fmt.Sprintf("%s: the relay through %s failed (%s) - it reaches the panel directly meanwhile", s.Name, relay, ls.Live.RelayError)})
		}
	}
	return out
}

// sanitizeRelayLive bounds what an agent says about its way to the panel.
func sanitizeRelayLive(lv *proto.Live) {
	if lv.PanelPath != "relay" && lv.PanelPath != "direct" {
		lv.PanelPath = ""
	}
	lv.RelayError = cleanName(lv.RelayError, 300)
	lv.RelayConns = clamp(lv.RelayConns, 0, 1<<16)
	if lv.PanelConn != "websocket" && lv.PanelConn != "http" {
		lv.PanelConn = ""
	}
	lv.ConnError = cleanName(lv.ConnError, 300)
}
