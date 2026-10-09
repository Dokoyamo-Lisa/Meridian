package panel

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"meridian/internal/proto"
	"meridian/internal/ws"
)

// The supervisor's console: a root shell on a server, in the panel's page. Only the supervisor's
// signed-in browser opens one - never an API token, MCP, a plugin or a user - after confirming the
// password (again after half an hour). The panel asks the server's agent for a shell with an action;
// the agent dials back with a WebSocket for that session, and the panel joins it to the browser's
// WebSocket, which must come from the panel's own page (its Origin) and bring the session's one-time
// ticket. Opening and closing are in the timeline and the security notifications; nothing typed or
// shown is kept.

const (
	consolePerServer = 3
	consoleAll       = 20
	consoleFrame     = 64 << 10
	consoleWaitAgent = 45 * time.Second
	consoleConfirm   = 30 * time.Minute
)

type consoleSession struct {
	id, ticket string
	serverID   int64
	server     string
	accountID  int64
	by         int64
	byName, ip string
	owner      string // the browser session (its token's hash) that opened it
	created    time.Time
	agentIn    chan *ws.Conn
	claimed    bool
}

type consoleHub struct {
	mu        sync.Mutex
	open      map[string]*consoleSession
	confirmed map[string]time.Time // browser session -> when its password was last confirmed
}

func (h *consoleHub) countFor(server int64) (n, all int) {
	for _, s := range h.open {
		if s.serverID == server {
			n++
		}
	}
	return n, len(h.open)
}

type consoleInput struct {
	Cols     int    `json:"cols"`
	Rows     int    `json:"rows"`
	Password string `json:"password" doc:"Your password, when it was not confirmed in the last 30 minutes"`
}

type consoleOpened struct {
	Session string `json:"session" doc:"The session to connect to"`
	Ticket  string `json:"ticket" doc:"Send it as the first message on the WebSocket; it works once"`
	URL     string `json:"url" doc:"The WebSocket to open (same origin)"`
}

// browserSession is the hash of the request's session cookie ("" when there is none).
func browserSession(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return ""
	}
	return tokenHash(c.Value)
}

// apiOpenConsole starts a console on a server: the agent is asked for a shell.
func (p *Panel) apiOpenConsole(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownServer(r.Context(), a, id)
	if err != nil {
		return err
	}
	var in consoleInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	sess := browserSession(r)
	if sess == "" || !a.IsOwner() {
		return errForbidden
	}
	if err := ownerOnly(s, "the console"); err != nil { // sharing.go
		return err
	}
	switch {
	case !s.Online:
		return errStatus(http.StatusConflict, s.Name+" is offline - its console opens when its agent reports again")
	case !s.caps().Console:
		if s.caps().Limits { // a 1.0 agent that says no: turned off on the server
			return errStatus(http.StatusConflict, "the console is turned off on "+s.Name+" (/etc/meridian-agent/no-console or MERIDIAN_NO_CONSOLE=1 there)")
		}
		return errStatus(http.StatusConflict, s.Name+"'s agent is older than 1.0 - upgrade it to open a console (nobody is disconnected)")
	}
	p.console.mu.Lock()
	last := p.console.confirmed[sess]
	p.console.mu.Unlock()
	if time.Since(last) > consoleConfirm {
		if in.Password == "" {
			// not 401: the session is fine, only the password is asked again
			return errStatus(http.StatusPreconditionRequired, "confirm your password to open a console")
		}
		if err := p.confirmPassword(a, in.Password); err != nil {
			return err
		}
	}
	if !p.limiter.allow(fmt.Sprintf("console:%d", a.ID), 30, time.Hour) {
		return errStatus(http.StatusTooManyRequests, "too many consoles opened in the last hour - wait a little")
	}
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	cs := &consoleSession{id: hex.EncodeToString(raw), ticket: randToken(24), serverID: s.ID, server: s.Name, accountID: s.AccountID,
		by: a.ID, byName: a.Username, ip: p.clientIP(r), owner: sess, created: time.Now(), agentIn: make(chan *ws.Conn, 1)}
	p.console.mu.Lock()
	if p.console.open == nil {
		p.console.open, p.console.confirmed = map[string]*consoleSession{}, map[string]time.Time{}
	}
	p.console.confirmed[sess] = time.Now()
	n, all := p.console.countFor(s.ID)
	if n >= consolePerServer || all >= consoleAll {
		p.console.mu.Unlock()
		return errStatus(http.StatusConflict, fmt.Sprintf("%d consoles are open on %s already - close one", n, s.Name))
	}
	p.console.open[cs.id] = cs
	p.console.mu.Unlock()
	cols, rows := in.Cols, in.Rows
	if cols == 0 || rows == 0 { // no size given: a classic terminal
		cols, rows = 80, 24
	}
	args, _ := json.Marshal(map[string]any{"session": cs.id, "cols": clamp(cols, 20, 500), "rows": clamp(rows, 5, 300)})
	if _, err := p.db.Exec1(`INSERT INTO actions (server_id, kind, args, created_at, created_by) VALUES (?, ?, ?, ?, ?)`,
		s.ID, proto.ActionConsole, string(args), now(), a.ID); err != nil {
		p.dropConsole(cs.id)
		return err
	}
	p.touchServers(s.ID)
	go func() { // a session nobody joined ends
		time.Sleep(consoleWaitAgent + 15*time.Second)
		p.console.mu.Lock()
		claimed := cs.claimed
		p.console.mu.Unlock()
		if !claimed {
			p.dropConsole(cs.id)
		}
	}()
	writeJSON(w, http.StatusOK, consoleOpened{Session: cs.id, Ticket: cs.ticket,
		URL: fmt.Sprintf("/api/servers/%d/console/ws?session=%s", s.ID, cs.id)})
	return nil
}

// dropConsole forgets a session, closing an agent's connection that nobody joined.
func (p *Panel) dropConsole(id string) {
	p.console.mu.Lock()
	cs := p.console.open[id]
	delete(p.console.open, id)
	p.console.mu.Unlock()
	if cs != nil {
		select {
		case c := <-cs.agentIn:
			c.Close()
		default:
		}
	}
}

// sameOrigin says whether a WebSocket request comes from the panel's own page.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	u, err := url.Parse(o)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}

// apiConsoleWS is the browser's side of a console.
func (p *Panel) apiConsoleWS(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if !sameOrigin(r) || !a.IsOwner() {
		return errForbidden
	}
	p.console.mu.Lock()
	cs := p.console.open[r.URL.Query().Get("session")]
	ok := cs != nil && cs.serverID == id && cs.by == a.ID && cs.owner == browserSession(r) && !cs.claimed
	if ok {
		cs.claimed = true
	}
	p.console.mu.Unlock()
	if !ok {
		return errStatus(http.StatusNotFound, "no such console - open it again")
	}
	defer p.dropConsole(cs.id)
	browser, err := ws.Accept(w, r, consoleFrame)
	if err != nil {
		return nil // Accept answered
	}
	defer browser.Close()
	browser.SetIdle(10 * time.Second)
	first, err := browser.Read()
	if err != nil || subtle.ConstantTimeCompare(first, []byte(cs.ticket)) != 1 {
		return nil
	}
	notice := func(msg string) {
		b, _ := json.Marshal(map[string]any{"error": msg})
		_ = browser.Write(append([]byte{proto.ConsoleEnd}, b...))
	}
	var agent *ws.Conn
	select {
	case agent = <-cs.agentIn:
	case <-time.After(consoleWaitAgent):
		notice(cs.server + "'s agent did not open the console - is it still connected? Try again in a moment.")
		return nil
	case <-r.Context().Done():
		return nil
	}
	defer agent.Close()
	start := time.Now()
	p.event(cs.accountID, "warn", "console_opened", cs.serverID, 0, cs.by, fmt.Sprintf("%s opened the console on %s from %s", cs.byName, cs.server, cs.ip), nil)
	if browser.Write([]byte{proto.ConsoleReady}) != nil {
		return nil
	}
	browser.SetIdle(0) // the agent ends a console nobody types in
	done := make(chan struct{}, 2)
	go func() { // what the shell prints
		for {
			msg, err := agent.Read()
			if err != nil || browser.Write(msg) != nil {
				break
			}
		}
		done <- struct{}{}
	}()
	go func() { // what is typed, and the window's size - nothing else goes to the server
		for {
			msg, err := browser.Read()
			if err != nil || len(msg) == 0 || (msg[0] != proto.ConsoleData && msg[0] != proto.ConsoleSize) || agent.Write(msg) != nil {
				break
			}
		}
		done <- struct{}{}
	}()
	<-done
	browser.Close()
	agent.Close()
	p.event(cs.accountID, "info", "console_closed", cs.serverID, 0, cs.by, fmt.Sprintf("%s closed the console on %s after %s", cs.byName, cs.server,
		time.Since(start).Round(time.Second)), nil)
	return nil
}

// handleConsoleAgent is the agent's side of a console: it dials back for a session it was asked for.
func (p *Panel) handleConsoleAgent(w http.ResponseWriter, r *http.Request) {
	srv, _, err := p.agentAuth(r, nil)
	if err != nil {
		agentDeny(w, err)
		return
	}
	p.console.mu.Lock()
	cs := p.console.open[r.PathValue("session")]
	p.console.mu.Unlock()
	if cs == nil || cs.serverID != srv.ID {
		http.NotFound(w, r)
		return
	}
	conn, err := ws.Accept(w, r, consoleFrame)
	if err != nil {
		return
	}
	select {
	case cs.agentIn <- conn:
	default: // a second dial for the same session
		conn.Close()
	}
}
