package panel

// What plugins hook into: the panel's events, its filters (what servers run, what users' apps
// receive, what the status page shows and what notifications say), its API (called by a plugin's
// program), notifications, timers and MCP tools.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"meridian/internal/proto"
)

// run is the host's loop: it follows the plugins table, hands new events to the programs that listen
// and fires their timers. When ctx ends it stops every program and returns.
func (h *pluginHost) run(ctx context.Context) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.ctx = ctx
	h.mu.Unlock()
	h.sync()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for n := 1; ; n++ {
		select {
		case <-ctx.Done():
			h.stopAll()
			return
		case <-h.kick:
			h.sync()
		case <-t.C:
			if n%3 == 0 { // `meridian plugins` changes the table from outside
				h.sync()
			}
			h.deliverEvents(ctx)
			h.fireTimers(ctx)
		}
	}
}

// ready waits - at most as long as a program has to register - until the programs of the plugins
// that may filter what servers run have registered (or failed), so the configurations the panel
// compiles when it starts already carry their changes: a panel restart never briefly sends servers
// a configuration without them.
func (h *pluginHost) ready(ctx context.Context) {
	if h == nil || h.off {
		return
	}
	deadline := time.Now().Add(h.registerWait + time.Second)
	for h.awaiting("filter:compile") && time.Now().Before(deadline) && ctx.Err() == nil {
		time.Sleep(20 * time.Millisecond)
	}
}

// awaiting says whether the program of a plugin that is on and has a permission is still to start
// or register.
func (h *pluginHost) awaiting(perm string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, pl := range h.list {
		if !pl.row.Enabled || pl.man == nil || pl.man.Server == nil || !slices.Contains(pl.man.Permissions, perm) {
			continue
		}
		switch pp := pl.proc; {
		case h.ctx == nil, pp == nil && pl.crashes == 0: // not started yet
			return true
		case pp != nil && pp.reg.Load() == nil && !pp.ended():
			return true
		}
	}
	return false
}

// procsWith lists the running programs that registered a hook (with "": every running program), in
// the order of their plugins' ids.
func (h *pluginHost) procsWith(hook string) []*pluginProc {
	if h == nil || h.off {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []*pluginProc
	for _, id := range h.ids() {
		pp := h.list[id].proc
		if pp == nil {
			continue
		}
		if r := pp.reg.Load(); r != nil && (hook == "" || r.has(hook)) {
			out = append(out, pp)
		}
	}
	return out
}

// ---------------------------------------------------------------- events

// deliverEvents hands the events recorded since the last round to the programs that listen, each
// from where it stands (a program hears what happens from the moment it registers). A program that
// does not keep up misses events rather than holding anything up; the timeline says so.
func (h *pluginHost) deliverEvents(ctx context.Context) {
	subs := h.procsWith("event")
	if len(subs) == 0 {
		return
	}
	from := subs[0].evFrom.Load()
	for _, pp := range subs[1:] {
		from = min(from, pp.evFrom.Load())
	}
	rows, err := h.db.QueryContext(ctx, `SELECT id, ts, account_id, level, kind, server_id, sub_id, actor_id, message, data
		FROM events WHERE id > ? ORDER BY id LIMIT 500`, from)
	if err != nil {
		return
	}
	for rows.Next() {
		var e Event
		var data string
		if err := rows.Scan(&e.ID, &e.TS, &e.AccountID, &e.Level, &e.Kind, &e.ServerID, &e.SubID, &e.ActorID, &e.Message, &data); err != nil {
			break
		}
		if e.Data = json.RawMessage(data); !json.Valid(e.Data) {
			e.Data = json.RawMessage("{}")
		}
		b, err := json.Marshal(pluginOut{JSONRPC: "2.0", Method: "event", Params: e})
		if err != nil {
			continue
		}
		b = append(b, '\n')
		for _, pp := range subs {
			if e.ID > pp.evFrom.Load() {
				pp.event(b)
				pp.evFrom.Store(e.ID)
			}
		}
	}
	rows.Close()
	for _, pp := range subs {
		if n := pp.dropped.Load(); n > 0 {
			h.note("lag:"+pp.id, "warn", "plugin_lagging", pp.id, fmt.Sprintf("The plugin %s is not keeping up: %d events were not handed to it", pp.name, n))
		}
	}
}

// event queues an event for the program, or counts it as missed when the queue is full.
func (pp *pluginProc) event(b []byte) {
	select {
	case pp.evq <- b:
	default:
		pp.dropped.Add(1)
	}
}

// ---------------------------------------------------------------- timers

// fireTimers calls the programs whose timers are due. A timer whose last call has not come back
// waits for its next turn.
func (h *pluginHost) fireTimers(ctx context.Context) {
	for _, pp := range h.procsWith("") {
		r := pp.reg.Load()
		t := time.Now()
		for _, s := range r.Schedules {
			pp.mu.Lock()
			due := !t.Before(pp.due[s.Name]) && !pp.ticking[s.Name]
			if due {
				pp.ticking[s.Name] = true
				pp.due[s.Name] = t.Add(time.Duration(s.Every) * time.Second)
			}
			pp.mu.Unlock()
			if !due {
				continue
			}
			go func(name string) {
				_, err := pp.call(ctx, "tick", map[string]string{"name": name}, h.tickWait)
				pp.mu.Lock()
				pp.ticking[name] = false
				pp.mu.Unlock()
				if err != nil && !pp.stopping.Load() && ctx.Err() == nil {
					pp.log.write(pp.id, fmt.Sprintf("the panel: the timer %s: %s", name, err))
				}
			}(s.Name)
		}
	}
}

// ---------------------------------------------------------------- filters

// chain passes a value through every running program that registered a filter, in the order of their
// plugins' ids, each getting what the one before made of it. A program that fails or does not answer
// in time is passed over - what it was given goes on unchanged - and left out for half a minute; the
// timeline says so. Servers' configurations are compiled again after that half minute, so a filter
// that answers again applies again without waiting for the next change.
func (h *pluginHost) chain(ctx context.Context, hook, what string, params func() any, take func(json.RawMessage) error) {
	for _, pp := range h.procsWith(hook) {
		if pp.skipped(hook) {
			continue
		}
		res, err := pp.call(ctx, hook, params(), h.filterWait)
		if ctx.Err() != nil {
			return // whoever asked is gone (an app hung up): nothing to hold against the plugin
		}
		if err == nil {
			err = take(res)
		}
		if err != nil {
			pp.passOver(hook)
			pp.log.write(pp.id, fmt.Sprintf("the panel: %s failed: %s", hook, err))
			h.note("filter:"+pp.id+":"+hook, "warn", "plugin_filter_failed", pp.id,
				fmt.Sprintf("The plugin %s could not filter %s: %s. It went on unfiltered, and the plugin is passed over for half a minute.", pp.name, what, err))
			if hook == "filter.compile" && h.p != nil {
				time.AfterFunc(passOverFor+time.Second, h.p.touchAll)
			}
		}
	}
}

// passOverFor is how long a filter that failed is left out.
const passOverFor = 30 * time.Second

func (pp *pluginProc) skipped(hook string) bool {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	return time.Now().Before(pp.skip[hook])
}

func (pp *pluginProc) passOver(hook string) {
	pp.mu.Lock()
	pp.skip[hook] = time.Now().Add(passOverFor)
	pp.mu.Unlock()
}

// filterCompile lets plugins change a server's desired state before it is sent (filter:compile). What
// keeps servers and their agents safe stays the panel's: the contract, the server, the agent's
// settings, the cores (versions and checksums) and the actions waiting for the server.
func (h *pluginHost) filterCompile(ctx context.Context, st *proto.State) *proto.State {
	if st == nil || st.Decommission || len(h.procsWith("filter.compile")) == 0 {
		return st
	}
	cur := st
	h.chain(ctx, "filter.compile", "a server's configuration", func() any {
		return map[string]any{"server_id": cur.ServerID, "state": cur}
	}, func(res json.RawMessage) error {
		if t := bytes.TrimSpace(res); len(t) == 0 || t[0] != '{' {
			return errors.New("its answer is not a server's configuration (a JSON object)")
		}
		var next proto.State
		if err := json.Unmarshal(res, &next); err != nil {
			return errors.New("its answer is not a server's configuration: " + cleanName(err.Error(), 200))
		}
		next.Contract, next.ServerID, next.Rev, next.Decommission = cur.Contract, cur.ServerID, "", false
		next.Agent, next.Cores, next.Actions = cur.Agent, cur.Cores, cur.Actions
		cur = &next
		return nil
	})
	return cur
}

// filterSubscription lets plugins change what an app receives for a user's link (filter:subscription).
func (h *pluginHost) filterSubscription(ctx context.Context, format string, userID int64, ctype string, body []byte) []byte {
	if !utf8.Valid(body) || len(h.procsWith("filter.subscription")) == 0 {
		return body
	}
	cur := string(body)
	h.chain(ctx, "filter.subscription", "a subscription", func() any {
		return map[string]any{"format": format, "user_id": userID, "content_type": ctype, "body": cur}
	}, func(res json.RawMessage) error {
		var s string
		if err := json.Unmarshal(res, &s); err != nil {
			return errors.New("its answer is not the subscription's text (a JSON string)")
		}
		if len(s) > 8<<20 {
			return errors.New("its answer is larger than 8 MB")
		}
		cur = s
		return nil
	})
	return []byte(cur)
}

// statusFiltered is the status page's data as plugins left it, for the data they were given.
type statusFiltered struct {
	in   []byte
	body json.RawMessage
}

// filterStatus lets plugins change what the status page shows (filter:status): out is what the
// asker would get. The same data is filtered once, however many people look (the panel rebuilds it
// every few seconds; visitors and the supervisor get their own copies).
func (h *pluginHost) filterStatus(ctx context.Context, out *statusPayload) any {
	if len(h.procsWith("filter.status")) == 0 {
		return out
	}
	b, err := json.Marshal(out)
	if err != nil {
		return out
	}
	h.stMu.Lock()
	for _, c := range h.stCache {
		if c.body != nil && bytes.Equal(c.in, b) {
			h.stMu.Unlock()
			return c.body
		}
	}
	h.stMu.Unlock()
	cur := json.RawMessage(b)
	h.chain(ctx, "filter.status", "the status page's data", func() any {
		return map[string]any{"data": cur}
	}, func(res json.RawMessage) error {
		t := bytes.TrimSpace(res)
		if len(t) == 0 || t[0] != '{' {
			return errors.New("its answer is not the status page's data (a JSON object)")
		}
		if len(t) > 4<<20 {
			return errors.New("its answer is larger than 4 MB")
		}
		cur = append(json.RawMessage(nil), t...)
		return nil
	})
	h.stMu.Lock()
	h.stCache[1], h.stCache[0] = h.stCache[0], statusFiltered{in: b, body: cur}
	h.stMu.Unlock()
	return cur
}

// filterNotify lets plugins change a notification's text (filter:notify); an empty text holds the
// notification back.
func (h *pluginHost) filterNotify(ctx context.Context, evs []notifyEvent, text string) string {
	if len(h.procsWith("filter.notify")) == 0 {
		return text
	}
	cur := text
	h.chain(ctx, "filter.notify", "a notification", func() any {
		return map[string]any{"events": evs, "text": cur}
	}, func(res json.RawMessage) error {
		var s string
		if err := json.Unmarshal(res, &s); err != nil {
			return errors.New("its answer is not the notification's text (a JSON string)")
		}
		cur = truncate(s, 8000)
		return nil
	})
	return cur
}

// ---------------------------------------------------------------- the panel's API, for programs

type pluginCtxKey struct{}

// pluginCaller is a plugin's program calling the panel's API.
type pluginCaller struct {
	id    string
	scope string // read | full
}

// pluginAuth lets a plugin's API calls through (resolve, auth.go): they act for the supervisor like an
// API token of the plugin's scope - api:read can only read - and never where a signed-in browser is
// needed. Only the panel itself puts a pluginCaller in a request's context.
func (p *Panel) pluginAuth(r *http.Request, o authOpts) (*Account, authInfo, bool, error) {
	pc, ok := r.Context().Value(pluginCtxKey{}).(pluginCaller)
	if !ok {
		return nil, authInfo{}, false, nil
	}
	if o.sessionOnly {
		return nil, authInfo{}, true, errStatus(http.StatusForbidden, "this needs a signed-in browser session - plugins cannot do it")
	}
	if pc.scope != "full" && !readOnlyMethod(r.Method) && !(r.Method == http.MethodPost && readOnlyPost[r.URL.Path]) {
		return nil, authInfo{}, true, errStatus(http.StatusForbidden, "this plugin may only read (it has api:read, not api:write)")
	}
	a, err := p.accountByID(r.Context(), ownerID(p.db))
	if err != nil || !a.Enabled {
		return nil, authInfo{}, true, errStatus(http.StatusUnauthorized, "there is no supervisor account to act for")
	}
	return a, authInfo{Token: true, Scope: pc.scope}, true, nil
}

// pluginAPIOff are the parts of the API a plugin's program never calls: the plugins themselves (their
// management, and their own APIs), the users' own pages and signing in and out.
var pluginAPIOff = []string{"/api/plugins/", "/api/settings/plugins", "/api/portal/", "/api/login", "/api/logout"}

// apiCall performs a program's call to the panel's API in-process, as the supervisor, within the
// plugin's api:read or api:write permission. It answers {status, body} for any status.
func (pp *pluginProc) apiCall(raw json.RawMessage) (any, *rpcError) {
	var in struct {
		Method string          `json:"method"`
		Path   string          `json:"path"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, &rpcError{-32602, "api needs {method, path, body}"}
	}
	scope := ""
	switch {
	case pp.perms["api:write"]:
		scope = "full"
	case pp.perms["api:read"]:
		scope = "read"
	default:
		return nil, &rpcError{-32000, "calling the API needs the api:read or api:write permission in plugin.json"}
	}
	method := strings.ToUpper(nz(in.Method, "GET"))
	if !slices.Contains([]string{"GET", "POST", "PUT", "PATCH", "DELETE"}, method) {
		return nil, &rpcError{-32602, "api: method must be GET, POST, PUT, PATCH or DELETE"}
	}
	u, err := url.Parse(in.Path)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || !strings.HasPrefix(u.Path, "/api/") || path.Clean(u.Path) != u.Path {
		return nil, &rpcError{-32602, "api: path must be a path of the panel's API, like /api/servers"}
	}
	for _, off := range pluginAPIOff {
		if strings.HasPrefix(u.Path, off) {
			return nil, &rpcError{-32000, "api: plugins cannot call " + off}
		}
	}
	mux := pp.h.mux.Load()
	if pp.h.p == nil || mux == nil {
		return nil, &rpcError{-32000, "the panel is still starting - try again in a moment"}
	}
	var body io.Reader
	if len(in.Body) > 0 && string(in.Body) != "null" {
		body = bytes.NewReader(in.Body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ctx = context.WithValue(ctx, pluginCtxKey{}, pluginCaller{id: pp.id, scope: scope})
	req, err := http.NewRequestWithContext(ctx, method, u.RequestURI(), body)
	if err != nil {
		return nil, &rpcError{-32602, "api: " + err.Error()}
	}
	req.RemoteAddr = "127.0.0.1:0"
	req.Host = "localhost"
	if pu, err := url.Parse(pp.h.p.settings().PublicURL); err == nil && pu.Host != "" {
		req.Host = pu.Host
	}
	req.Header.Set("User-Agent", "Meridian plugin "+pp.id)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := &recorder{header: http.Header{}, status: http.StatusOK}
	mux.ServeHTTP(rec, req)
	var out any
	if b := rec.buf.Bytes(); len(b) > 0 && json.Unmarshal(b, &out) != nil {
		out = string(b)
	}
	return map[string]any{"status": rec.status, "body": out}, nil
}

// notifyCall records a program's message in the timeline; the notifications job sends it with the
// next batch, whatever groups are chosen (notify.go).
func (pp *pluginProc) notifyCall(raw json.RawMessage) (any, *rpcError) {
	if !pp.perms["notify"] {
		return nil, &rpcError{-32000, "sending notifications needs the notify permission in plugin.json"}
	}
	var in struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &in)
	text := cleanNote(in.Text, 2000)
	if text == "" {
		return nil, &rpcError{-32602, "notify needs a text"}
	}
	p := pp.h.p
	if p == nil || !p.notifyConfig().active() {
		return nil, &rpcError{-32000, "notifications are not set up - the supervisor sets them up in Settings › Notifications"}
	}
	if !p.limiter.allow("pluginnotify:"+pp.id, 30, time.Hour) {
		return nil, &rpcError{-32000, "a plugin can send at most 30 notifications an hour"}
	}
	pp.h.record(0, "info", "plugin_message", pp.id, pp.name+": "+text)
	return map[string]bool{"queued": true}, nil
}

// ---------------------------------------------------------------- MCP tools

// pluginToolName is how assistants see a plugin's tool: its plugin's id, two underscores, its name.
func pluginToolName(id, name string) string { return id + "__" + name }

// tools are the MCP tools of the running programs.
func (h *pluginHost) tools() []*mcpTool {
	var out []*mcpTool
	for _, pp := range h.procsWith("") {
		if !pp.perms["mcp"] {
			continue
		}
		for _, spec := range pp.reg.Load().Tools {
			out = append(out, pp.mcpTool(spec))
		}
	}
	return out
}

func (pp *pluginProc) mcpTool(spec pluginToolSpec) *mcpTool {
	props := map[string]any{}
	if p, ok := spec.InputSchema["properties"].(map[string]any); ok {
		props = p
	}
	var req []string
	if r, ok := spec.InputSchema["required"].([]any); ok {
		for _, x := range r {
			if s, ok := x.(string); ok {
				req = append(req, s)
			}
		}
	}
	return &mcpTool{Name: pluginToolName(pp.id, spec.Name), Title: spec.Title, Description: spec.Description + " (from the plugin " + pp.name + ")",
		Props: props, Required: req, Write: spec.Write, Destructive: spec.Destructive,
		Run: func(c *mcpCall, args map[string]any) (any, error) {
			a := maps.Clone(args)
			delete(a, "confirm") // the panel's, not the tool's
			res, err := pp.call(c.r.Context(), "mcp", map[string]any{"tool": spec.Name, "args": a, "scope": c.auth.Scope}, pp.h.toolWait)
			if err != nil {
				return nil, fmt.Errorf("the plugin %s: %v", pp.name, err)
			}
			var s string
			if json.Unmarshal(res, &s) == nil {
				return s, nil
			}
			return res, nil
		}}
}

// pluginToolList describes the plugins' tools for tools/list (mcp.go): write tools only for
// full-access tokens, like the panel's own.
func (p *Panel) pluginToolList(scope string) []map[string]any {
	var out []map[string]any
	for _, t := range p.plugins.tools() {
		if t.Write && scope != "full" {
			continue
		}
		out = append(out, t.describe())
	}
	return out
}

// pluginTool finds a plugin's tool by the name assistants call it (mcp.go).
func (p *Panel) pluginTool(name string) *mcpTool {
	if !strings.Contains(name, "__") {
		return nil
	}
	for _, t := range p.plugins.tools() {
		if t.Name == name {
			return t
		}
	}
	return nil
}
