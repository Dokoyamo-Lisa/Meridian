package panel

// The panel is a Model Context Protocol server (Streamable HTTP transport, stateless, plain JSON
// responses). It authenticates with an API token and runs every tool through the same REST
// handlers the web UI uses, so permissions, validation and account isolation are identical.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const mcpLatest = "2025-06-18"

var mcpVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (p *Panel) handleMCPOther(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "POST")
	http.Error(w, "this MCP endpoint answers POST requests only", http.StatusMethodNotAllowed)
}

// originAllowed protects against DNS rebinding: a browser page may only talk to the endpoint
// from the panel's own origin. Non-browser MCP clients send no Origin header.
func (p *Panel) originAllowed(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	if pu, err := url.Parse(p.settings().PublicURL); err == nil && pu.Host != "" && strings.EqualFold(pu.Host, u.Host) {
		return true
	}
	return false
}

func (p *Panel) handleMCP(w http.ResponseWriter, r *http.Request) {
	if !p.originAllowed(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	tok, ok := bearerToken(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="meridian"`)
		writeErr(w, errStatus(http.StatusUnauthorized, "send an API token: Authorization: Bearer mrd_..."))
		return
	}
	ip := p.clientIP(r)
	if p.limiter.exceeded("tokfail:"+ip, 20, 15*time.Minute) {
		writeErr(w, errStatus(http.StatusTooManyRequests, "too many failed attempts - wait a few minutes"))
		return
	}
	a, ai, err := p.tokenAccount(r.Context(), tok, ip)
	if err != nil {
		p.limiter.allow("tokfail:"+ip, 20, 15*time.Minute)
		w.Header().Set("WWW-Authenticate", `Bearer realm="meridian", error="invalid_token"`)
		writeErr(w, errStatus(http.StatusUnauthorized, "invalid or expired API token"))
		return
	}
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !slices.Contains(mcpVersions, v) {
		writeErr(w, errStatus(http.StatusBadRequest, "unsupported MCP protocol version "+v))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeErr(w, errStatus(http.StatusRequestEntityTooLarge, "request too large"))
		return
	}
	c := &mcpCall{p: p, r: r, account: a, auth: ai}
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' { // JSON-RPC batch (protocol 2025-03-26)
		var reqs []rpcRequest
		if err := json.Unmarshal(body, &reqs); err != nil || len(reqs) == 0 || len(reqs) > 20 {
			writeJSON(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "invalid batch"}})
			return
		}
		var out []rpcResponse
		for _, q := range reqs {
			if resp := c.dispatch(q); resp != nil {
				out = append(out, *resp)
			}
		}
		if len(out) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	var q rpcRequest
	if err := json.Unmarshal(body, &q); err != nil {
		writeJSON(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
		return
	}
	resp := c.dispatch(q)
	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// mcpCall is one MCP request, made on behalf of a token's account.
type mcpCall struct {
	p       *Panel
	r       *http.Request
	account *Account
	auth    authInfo
}

func (c *mcpCall) dispatch(q rpcRequest) *rpcResponse {
	isNotification := len(q.ID) == 0 || string(q.ID) == "null"
	reply := func(res any, e *rpcError) *rpcResponse {
		if isNotification {
			return nil
		}
		return &rpcResponse{JSONRPC: "2.0", ID: q.ID, Result: res, Error: e}
	}
	if q.JSONRPC != "2.0" {
		return reply(nil, &rpcError{-32600, "jsonrpc must be 2.0"})
	}
	switch q.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(q.Params, &params)
		v := mcpLatest
		if slices.Contains(mcpVersions, params.ProtocolVersion) {
			v = params.ProtocolVersion
		}
		return reply(map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "meridian", "title": c.p.settings().SiteTitle, "version": Version},
			"instructions":    mcpInstructions(c.auth.Scope),
		}, nil)
	case "ping":
		return reply(map[string]any{}, nil)
	case "tools/list":
		list := []map[string]any{}
		for _, t := range mcpTools {
			if t.Write && c.auth.Scope != "full" {
				continue
			}
			list = append(list, t.describe())
		}
		list = append(list, c.p.pluginToolList(c.auth.Scope)...) // plugins' own tools (plugins_hooks.go)
		return reply(map[string]any{"tools": list}, nil)
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(q.Params, &params); err != nil {
			return reply(nil, &rpcError{-32602, "invalid params"})
		}
		t := findTool(params.Name)
		if t == nil {
			t = c.p.pluginTool(params.Name)
		}
		if t == nil {
			return reply(nil, &rpcError{-32602, "unknown tool " + params.Name})
		}
		if params.Arguments == nil {
			params.Arguments = map[string]any{}
		}
		return reply(c.run(t, params.Arguments), nil)
	case "resources/list":
		return reply(map[string]any{"resources": []any{}}, nil)
	case "prompts/list":
		return reply(map[string]any{"prompts": []any{}}, nil)
	}
	if strings.HasPrefix(q.Method, "notifications/") {
		return nil
	}
	return reply(nil, &rpcError{-32601, "method not found: " + q.Method})
}

func toolText(text string, isError bool) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isError}
}

func (c *mcpCall) run(t *mcpTool, args map[string]any) map[string]any {
	if t.Write && c.auth.Scope != "full" {
		return toolText("This token is read-only. Create a full-access token in Settings › API & MCP to make changes.", true)
	}
	if t.Destructive && args["confirm"] != true {
		return toolText("This action disconnects people or cannot be undone. Describe exactly what will happen, ask the user "+
			"to confirm, and only then call it again with confirm=true.", true)
	}
	if t.Disrupts != nil && args["confirm"] != true {
		if what := t.Disrupts(c, args); what != "" {
			return toolText(what+" Tell the user, and only after they agree call it again with confirm=true.", true)
		}
	}
	out, err := t.Run(c, args)
	if err != nil {
		return toolText(err.Error(), true)
	}
	if s, ok := out.(string); ok {
		return toolText(s, false)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return toolText(err.Error(), true)
	}
	return toolText(string(b), false)
}

// api calls a REST endpoint in-process with the caller's token and decodes the JSON answer.
func (c *mcpCall) api(method, path string, body any) (any, error) {
	if body == nil {
		return c.send(method, path, nil, "")
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return c.send(method, path, b, "application/json")
}

// send calls the API with a raw body of the given type (nil: no body).
func (c *mcpCall) send(method, path string, body []byte, ctype string) (any, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	ctx, cancel := context.WithTimeout(c.r.Context(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, path, rd)
	if err != nil {
		return nil, err
	}
	req.RemoteAddr = c.r.RemoteAddr
	req.Host = c.r.Host
	req.TLS = c.r.TLS
	for _, h := range []string{"Authorization", "X-Forwarded-For", "X-Real-IP", "X-Forwarded-Proto", "User-Agent"} {
		if v := c.r.Header.Values(h); len(v) > 0 {
			req.Header[h] = v
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", ctype)
	}
	rec := &recorder{header: http.Header{}, status: http.StatusOK}
	c.p.mux.ServeHTTP(rec, req)
	var out any
	if rec.buf.Len() > 0 {
		if err := json.Unmarshal(rec.buf.Bytes(), &out); err != nil {
			return nil, fmt.Errorf("unexpected answer from %s", path)
		}
	}
	if rec.status >= 400 {
		if m, ok := out.(map[string]any); ok {
			if msg, ok := m["error"].(string); ok {
				return nil, errors.New(msg)
			}
		}
		return nil, fmt.Errorf("%s %s failed with status %d", method, path, rec.status)
	}
	return out, nil
}

// recorder is a minimal in-memory http.ResponseWriter.
type recorder struct {
	header http.Header
	status int
	wrote  bool
	buf    bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(code int) {
	if !r.wrote {
		r.status, r.wrote = code, true
	}
}
func (r *recorder) Write(b []byte) (int, error) {
	r.wrote = true
	if r.buf.Len()+len(b) > 8<<20 {
		return 0, errors.New("response too large")
	}
	return r.buf.Write(b)
}

func mcpInstructions(scope string) string {
	s := `Meridian manages proxy and VPN servers (Xray: VLESS, VMess, Trojan, Shadowsocks, SOCKS5, HTTP; Hysteria2; WireGuard; port forwards) and the users who connect through them.

How to work with it:
- Start with "overview" to see what is running and what needs attention.
- Servers have protocols. A user has one subscription link that covers some or all servers, and usually a username and password to see their own usage on the panel's site.
- Before adding a protocol, "check_protocol" says whether a combination works and which apps can use it.
- To investigate abuse or sharing, use "find_sharing", then "ip_history" and "destinations" for the user in question.
- Health risks ("list_risks") are what the servers' health checks found that looks like a break-in or abuse. Explain them; decide about one ("decide_risk") only after the user says what it is - it may be a real break-in.
- A server that already runs Xray, x-ui, 3x-ui, sing-box or Hysteria2 can be scanned ("scan_server", then "get_scan") and its protocols imported with their keys, so devices keep working.
- The status page shows visitors only a sign-in; users see their own usage there, and only the supervisor sees the servers. Its texts are public: never put private information in them.
- Country rules ("set_country_rule") cut connections from blocked countries at once on every server that follows the global rule.
- Traffic rules ("get_routing", "set_traffic_rule") send chosen sites directly, through another server or an imported external node ("import_external_nodes", or a provider's subscription kept up to date: "add_subscription_link"), to a load balancer, or nowhere - live. After a change, read "problems" in get_routing: what cannot be used somewhere is blocked there, never sent out directly instead.
- Traffic is in bytes, rates in bytes per second, times in Unix seconds. Quotas and IP limits only raise alerts - nothing is ever paused automatically.
- Tools marked destructive (pause, delete, reset, block, restart) disconnect people. Never call them without the user's explicit confirmation in this conversation; pass confirm=true only after that.
- Never print subscription links, passwords, install commands or credentials unless the user asks for them.`
	if scope != "full" {
		s += "\n\nThis token is read-only: you can look at everything but change nothing."
	}
	return s
}

// ---------------------------------------------------------------- tool catalogue

type mcpTool struct {
	Name        string
	Title       string
	Description string
	Props       map[string]any // JSON schema properties
	Required    []string
	Write       bool
	Destructive bool
	// Disrupts says what a call would restart (and so who it disconnects) or block, "" when nothing:
	// such a call needs confirm=true like a destructive tool
	Disrupts func(c *mcpCall, args map[string]any) string
	Run      func(c *mcpCall, args map[string]any) (any, error)
}

func (t *mcpTool) describe() map[string]any {
	props := map[string]any{}
	for k, v := range t.Props {
		props[k] = v
	}
	req := append([]string(nil), t.Required...)
	if t.Destructive {
		props["confirm"] = map[string]any{"type": "boolean", "description": "Must be true, and only after the user explicitly confirmed this action."}
		req = append(req, "confirm")
	} else if t.Disrupts != nil {
		props["confirm"] = map[string]any{"type": "boolean", "description": "Needed when the call restarts or blocks something (the tool answers what, if so): true only after the user confirmed it."}
	}
	schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(req) > 0 {
		schema["required"] = req
	}
	return map[string]any{
		"name":        t.Name,
		"title":       t.Title,
		"description": t.Description,
		"inputSchema": schema,
		"annotations": map[string]any{
			"title":           t.Title,
			"readOnlyHint":    !t.Write,
			"destructiveHint": t.Destructive,
			"idempotentHint":  !t.Write,
			"openWorldHint":   false,
		},
	}
}

func findTool(name string) *mcpTool {
	for i := range mcpTools {
		if mcpTools[i].Name == name {
			return &mcpTools[i]
		}
	}
	return nil
}

// argument helpers ---------------------------------------------------------------

func pInt(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
func pStr(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

// protocolCodeHelp explains a protocol's own settings as code.
const protocolCodeHelp = "A protocol's own settings: for an Xray protocol JSON (comments allowed) - fields merged into its inbound (sniffing, streamSettings.sockopt, fallbacks, ...), \"outbounds\" to add (own tags, not the panel's direct/block) and \"rules\" routing this protocol's traffic only (e.g. [{\"domain\": [\"geosite:openai\"], \"outboundTag\": \"warp\"}]); its tag, port and users stay the panel's. For Hysteria2 YAML (auth and trafficStats stay the panel's; saving restarts it). Only the syntax is checked. Never change how apps connect here (streamSettings network or security, ports): links do not follow code, so every device would stop working - use update_protocol."

// publicPortsHelp explains the ports a NAT server's provider forwards, for the tools that take them.
const publicPortsHelp = "Only for servers whose provider decides their ports (NAT servers, LXC and Incus containers): the ports it forwards, as on the provider's page - e.g. '20000-20019'. Where the number on the server differs from the public one write PUBLIC:LOCAL, e.g. '40001-40010:10001-10010' or '10022:22'. Add /tcp or /udp when only one is forwarded. Omit for an ordinary server (every port)."

func pBool(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
func pNum(desc string) map[string]any  { return map[string]any{"type": "number", "description": desc} }
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func pEnum(desc string, v ...string) map[string]any {
	return map[string]any{"type": "string", "enum": v, "description": desc}
}
func pInts(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": desc}
}
func pStrs(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

// argInts reads a list of numbers.
func argInts(a map[string]any, k string) ([]int64, bool) {
	list, ok := a[k].([]any)
	if !ok {
		return nil, false
	}
	out := []int64{}
	for _, v := range list {
		if f, ok := v.(float64); ok && f == float64(int64(f)) {
			out = append(out, int64(f))
		}
	}
	return out, true
}

// argStrs reads a list of strings (or one string with lines or commas).
func argStrs(a map[string]any, k string) ([]string, bool) {
	switch v := a[k].(type) {
	case []any:
		out := []string{}
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out, true
	case string:
		return strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '\n' }), true
	}
	return nil, false
}

func argInt(a map[string]any, k string) (int64, bool) {
	switch v := a[k].(type) {
	case float64:
		if v == float64(int64(v)) {
			return int64(v), true
		}
	case string:
		var n int64
		if _, err := fmt.Sscan(v, &n); err == nil {
			return n, true
		}
	}
	return 0, false
}

func needInt(a map[string]any, k string) (int64, error) {
	v, ok := argInt(a, k)
	if !ok || v <= 0 {
		return 0, fmt.Errorf("%s must be a positive number", k)
	}
	return v, nil
}

func argStr(a map[string]any, k string) (string, bool) {
	v, ok := a[k].(string)
	return strings.TrimSpace(v), ok
}

func argBool(a map[string]any, k string) (bool, bool) {
	v, ok := a[k].(bool)
	return v, ok
}

func qs(kv ...any) string {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		switch x := kv[i+1].(type) {
		case string:
			if x != "" {
				v.Set(kv[i].(string), x)
			}
		case int64:
			if x != 0 {
				v.Set(kv[i].(string), fmt.Sprint(x))
			}
		}
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}

// pick keeps only the named keys of a JSON object (or of every object in a list).
func pick(v any, keys ...string) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for _, k := range keys {
			if val, ok := x[k]; ok {
				out[k] = val
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for _, e := range x {
			out = append(out, pick(e, keys...))
		}
		return out
	}
	return v
}

// dateArg turns YYYY-MM-DD into the last second of that day in the panel's timezone.
func (c *mcpCall) dateArg(a map[string]any, k string) (int64, bool, error) {
	s, ok := argStr(a, k)
	if !ok {
		return 0, false, nil
	}
	if s == "" || s == "never" {
		return 0, true, nil
	}
	loc, err := time.LoadLocation(c.p.settings().Timezone)
	if err != nil {
		loc = time.UTC
	}
	d, err := time.ParseInLocation("2006-01-02", s, loc)
	if err != nil {
		return 0, false, fmt.Errorf("%s must be a date like 2026-12-31", k)
	}
	return d.Add(24*time.Hour - time.Second).Unix(), true, nil
}

// subBody maps user tool arguments onto the REST input.
func (c *mcpCall) subBody(a map[string]any) (map[string]any, error) {
	b := map[string]any{}
	if v, ok := argStr(a, "name"); ok {
		b["name"] = v
	}
	if v, ok := argStr(a, "note"); ok {
		b["note"] = v
	}
	if v, ok := argStr(a, "username"); ok {
		b["username"] = v
	}
	if v, ok := argStr(a, "password"); ok && v != "" {
		b["password"] = v
	}
	if v, ok := a["quota_gb"].(float64); ok {
		if v < 0 {
			return nil, errors.New("quota_gb cannot be negative")
		}
		b["quota"] = int64(v * float64(1<<30))
	}
	if v, ok := argInt(a, "reset_day"); ok {
		b["reset_day"] = v
	}
	if v, ok := argInt(a, "ip_limit"); ok {
		b["ip_limit"] = v
	}
	if v, ok := argStr(a, "count_mode"); ok {
		b["count_mode"] = v
	}
	if v, ok := argInt(a, "reset_every_days"); ok {
		b["reset_every"] = v
	}
	if v, ok := argInt(a, "speed_limit_mbps"); ok {
		b["speed_limit"] = v
	}
	if v, ok := argStr(a, "device_mode"); ok {
		b["device_mode"] = v
	}
	if v, ok := argInt(a, "plan_id"); ok {
		b["plan_id"] = v
	}
	exp, ok, err := c.dateArg(a, "expires_on")
	if err != nil {
		return nil, err
	}
	if ok {
		b["expires_at"] = exp
	}
	start, ok, err := c.dateArg(a, "starts_on")
	if err != nil {
		return nil, err
	}
	if ok && start > 0 {
		b["starts_at"] = start - 24*3600 + 1 // the day's first second (dateArg gives its last)
	}
	for arg, field := range map[string]string{"server_ids": "servers", "protocol_ids": "protocols"} {
		if raw, ok := a[arg].([]any); ok {
			ids := []int64{}
			for _, x := range raw {
				if f, ok := x.(float64); ok && f > 0 {
					ids = append(ids, int64(f))
				}
			}
			b[field] = ids
		}
	}
	// everything only when asked for by name: two empty lists read like "none" but would be everything
	if all, _ := argBool(a, "everything"); all {
		b["servers"], b["protocols"] = []int64{}, []int64{}
	} else if s, ok := b["servers"].([]int64); ok && len(s) == 0 {
		if n, ok := b["protocols"].([]int64); ok && len(n) == 0 {
			return nil, errors.New("server_ids and protocol_ids are both empty: pass everything=true for every server, or pause the user to stop their access")
		}
	}
	return b, nil
}

var subSummaryKeys = []string{"id", "name", "username", "can_sign_in", "status", "flags", "online_ips", "ip_limit", "ips_24h",
	"cycle_up", "cycle_down", "used", "quota", "count_mode", "reset_day", "reset_every", "next_reset", "starts_at", "expires_at",
	"device_mode", "speed_limit", "plan_id", "last_fetch_at", "last_online_at", "last_login_at", "note"}

// userLimitProps are the user tools' arguments for counting, periods and limits.
var userLimitProps = map[string]any{
	"count_mode":       pEnum("What counts toward the quota: both (upload and download, the default), down (download only), up (upload only), max (whichever is larger)", "both", "down", "up", "max"),
	"starts_on":        pStr("The first day of the user's period, YYYY-MM-DD (resets every N days count from it)"),
	"reset_every_days": pInt("Usage resets every this many days from starts_on; 0 = on reset_day each month"),
	"speed_limit_mbps": pInt("The most the user's devices get together on each server, in Mbps (1000 = 1 Gbps); 0 = no limit"),
	"device_mode":      pEnum("Devices over ip_limit: alert (only an alert, the default) or refuse (the extra devices are turned away until one goes offline)", "alert", "refuse"),
}

// withProps adds more arguments to a tool's.
func withProps(base map[string]any, more ...map[string]any) map[string]any {
	for _, m := range more {
		for k, v := range m {
			base[k] = v
		}
	}
	return base
}

var mcpTools = []mcpTool{
	// ------------------------------------------------------------ look
	{Name: "overview", Title: "Overview",
		Description: "Servers online, people connected now, today's traffic, current throughput and everything that needs attention (offline servers, failed changes, users over their limits).",
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			v, err := c.api("GET", "/api/overview", nil)
			return pick(v, "servers", "online_ips", "online_users", "users", "paused", "rx", "tx", "today", "alerts", "days"), err
		}},
	{Name: "list_servers", Title: "List servers",
		Description: "All servers with status, location, protocols (id, kind, port, enabled, people online), load and bandwidth used this cycle, and route_notes: traffic rules that do not work there as written.",
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			v, err := c.api("GET", "/api/servers", nil)
			if err != nil {
				return nil, err
			}
			list, _ := v.([]any)
			out := []any{}
			for _, x := range list {
				m, _ := x.(map[string]any)
				s := pick(m, "id", "name", "status", "address", "ipv4", "country", "city", "online_ips", "online_subs",
					"bw_used", "bw_limit", "pending_restart", "apply_errors", "agent_version", "last_seen_at", "public_ports",
					"ip_version", "addrs", "limits", "route_notes", "panel_relay", "relay_name", "panel_path", "relay_error", "panel_trouble", "relay_for", "relay_port",
					"panel_conn", "conn_error", "ddns", "dns").(map[string]any)
				s["protocols"] = pick(m["nodes"], "id", "kind", "label", "name", "port", "public_port", "bind_ip", "net", "enabled", "online", "pass_name", "pass_only")
				s["forwards"] = pick(m["forwards"], "id", "listen_port", "public_port", "target", "network", "engine", "enabled")
				if sys, ok := m["sys"].(map[string]any); ok {
					s["load"] = pick(sys, "cpu", "mem_used", "mem_total", "rx_rate", "tx_rate")
				}
				out = append(out, s)
			}
			return out, nil
		}},
	{Name: "get_server", Title: "Server details",
		Description: "One server in detail: system, cores, protocols with their settings (no private keys), forwards and current load. Configuration code (which may hold keys of its own) is not included - only whether there is some (has_code).",
		Props:       map[string]any{"server_id": pInt("Server id")}, Required: []string{"server_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "server_id")
			if err != nil {
				return nil, err
			}
			v, err := c.api("GET", fmt.Sprintf("/api/servers/%d", id), nil)
			if err != nil {
				return nil, err
			}
			m, _ := v.(map[string]any)
			srv, _ := m["server"].(map[string]any)
			delete(srv, "rates")
			delete(srv, "ports")
			// code may carry the operator's own secrets (a WARP key...): say only that it is there
			srv["has_code"] = srv["xray_code"] != nil && srv["xray_code"] != ""
			delete(srv, "xray_code")
			if nodes, ok := srv["nodes"].([]any); ok {
				for _, n := range nodes {
					if nm, ok := n.(map[string]any); ok {
						nm["has_code"] = nm["code"] != nil && nm["code"] != ""
						delete(nm, "code")
					}
				}
			}
			return srv, nil
		}},
	{Name: "list_users", Title: "List users",
		Description: "Users (each has a subscription link and maybe a sign-in for their own page) with status, soft-limit flags (over_quota, expired, over_ip_limit...), IPs online now, distinct IPs in 24 h and usage this cycle.",
		Props: map[string]any{
			"filter": pEnum("Only some of them", "all", "online", "flagged", "paused"),
			"query":  pStr("Text to look for in the name, username or note"),
		},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			v, err := c.api("GET", "/api/users", nil)
			if err != nil {
				return nil, err
			}
			filter, _ := argStr(a, "filter")
			q, _ := argStr(a, "query")
			q = strings.ToLower(q)
			all, _ := v.([]any)
			out := []any{}
			for _, x := range all {
				m, _ := x.(map[string]any)
				flags, _ := m["flags"].([]any)
				online, _ := m["online_ips"].(float64)
				switch filter {
				case "online":
					if online == 0 {
						continue
					}
				case "flagged":
					if len(flags) == 0 {
						continue
					}
				case "paused":
					if m["paused"] != true {
						continue
					}
				}
				if q != "" && !strings.Contains(strings.ToLower(fmt.Sprint(m["name"], " ", m["username"], " ", m["note"])), q) {
					continue
				}
				out = append(out, pick(m, subSummaryKeys...))
			}
			return out, nil
		}},
	{Name: "get_user", Title: "User details",
		Description: "One user: limits, usage (also per protocol: this cycle and all time), sign-in, who is connected right now (IP, place, network, server) and which protocols their link contains. Includes the subscription link.",
		Props:       map[string]any{"user_id": pInt("User id")}, Required: []string{"user_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "user_id")
			if err != nil {
				return nil, err
			}
			v, err := c.api("GET", fmt.Sprintf("/api/users/%d", id), nil)
			if err != nil {
				return nil, err
			}
			m, _ := v.(map[string]any)
			sub, _ := m["user"].(map[string]any)
			out := pick(sub, append(subSummaryKeys, "link", "online", "scope", "total_up", "total_down",
				"last_fetch_ip", "last_fetch_ua", "created_at")...).(map[string]any)
			out["protocols"] = pick(m["endpoints"], "name", "kind", "server", "host", "port")
			out["usage_by_protocol"] = m["usage"]
			return out, nil
		}},
	{Name: "online_now", Title: "Who is online",
		Description: "Every connection open right now: user, client IP, place, network (ASN/organisation), server and protocol, and whether the user is over their IP limit.",
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			return c.api("GET", "/api/live", nil)
		}},
	{Name: "ip_history", Title: "IP history",
		Description: "Which client IPs connected, when, how often, from where and through which users. Filter by user or server, or search an IP prefix, network name, city or country code.",
		Props: map[string]any{
			"days":      pInt("How many days back (default 7, max 400)"),
			"user_id":   pInt("Only this user"),
			"server_id": pInt("Only this server"),
			"query":     pStr("IP prefix, organisation, city or two-letter country code"),
		},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			days, _ := argInt(a, "days")
			if sub, ok := argInt(a, "user_id"); ok && sub > 0 {
				return c.api("GET", fmt.Sprintf("/api/users/%d/ips%s", sub, qs("days", days)), nil)
			}
			srv, _ := argInt(a, "server_id")
			q, _ := argStr(a, "query")
			return c.api("GET", "/api/ips"+qs("days", days, "server", srv, "q", q), nil)
		}},
	{Name: "destinations", Title: "Where traffic goes",
		Description: "Destinations (domains or IPs and ports) reached through the servers, with connection counts and bytes where measured (WireGuard counts bytes exactly; Xray and Hysteria2 count connections).",
		Props: map[string]any{
			"days":      pInt("How many days back (default 1)"),
			"user_id":   pInt("Only this user"),
			"server_id": pInt("Only this server"),
			"query":     pStr("Part of a domain or IP"),
		},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			days, _ := argInt(a, "days")
			sub, _ := argInt(a, "user_id")
			srv, _ := argInt(a, "server_id")
			q, _ := argStr(a, "query")
			v, err := c.api("GET", "/api/dests"+qs("days", days, "user", sub, "server", srv, "q", q), nil)
			if list, ok := v.([]any); ok && len(list) > 100 {
				v = list[:100]
			}
			return v, err
		}},
	{Name: "user_traffic", Title: "User traffic",
		Description: "Daily download/upload of one user and the totals per server and protocol.",
		Props:       map[string]any{"user_id": pInt("User id"), "days": pInt("How many days (default 30)")},
		Required:    []string{"user_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "user_id")
			if err != nil {
				return nil, err
			}
			days, _ := argInt(a, "days")
			return c.api("GET", fmt.Sprintf("/api/users/%d/traffic%s", id, qs("days", days)), nil)
		}},
	{Name: "events", Title: "Events",
		Description: "The activity timeline: sign-ins, changes, servers going offline, camouflage checks, pauses, blocks, newest first.",
		Props: map[string]any{
			"level":     pEnum("Only warnings", "all", "warn"),
			"server_id": pInt("Only this server"),
			"user_id":   pInt("Only this user"),
			"limit":     pInt("How many (default 50, max 500)"),
		},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			lv, _ := argStr(a, "level")
			if lv != "warn" {
				lv = ""
			}
			srv, _ := argInt(a, "server_id")
			sub, _ := argInt(a, "user_id")
			limit, ok := argInt(a, "limit")
			if !ok || limit <= 0 {
				limit = 50
			}
			v, err := c.api("GET", "/api/events"+qs("level", lv, "server", srv, "user", sub, "limit", limit), nil)
			return pick(v, "id", "ts", "level", "kind", "server_id", "user_id", "message"), err
		}},
	{Name: "list_risks", Title: "Health risks",
		Description: "What the servers' health checks found that looks like a break-in or abuse: crypto-miners, programs run from temporary folders or deleted, new ports, new accounts and SSH keys, changed administrator rights, scheduled tasks and services, kernel modules, SSH sign-ins, traffic Meridian does not account for, Meridian's own programs changed. Each has a severity (info, warning, high, critical), when it was first and last seen, how often, whether it still holds, and a status: open, acknowledged (seen; flagged again if it happens again) or expected (never flagged again). Nothing is ever stopped or blocked because of a risk.",
		Props: map[string]any{
			"status":    pEnum("Which (default open)", "open", "acknowledged", "expected", "all"),
			"severity":  pEnum("At least this serious", "info", "warning", "high", "critical"),
			"server_id": pInt("Only this server"),
		},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			st, _ := argStr(a, "status")
			sev, _ := argStr(a, "severity")
			srv, _ := argInt(a, "server_id")
			return c.api("GET", "/api/risks"+qs("status", st, "severity", sev, "server", srv), nil)
		}},
	{Name: "find_sharing", Title: "Find shared links",
		Description: "Ranks users whose links look shared or abused: over their IP limit now, many distinct IPs, several countries or networks in the period. Use ip_history on a suspect for details.",
		Props:       map[string]any{"days": pInt("Period to look at (default 1, max 30)")},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			days, _ := argInt(a, "days")
			if days <= 0 {
				days = 1
			}
			if days > 30 {
				days = 30
			}
			return c.findSharing(days)
		}},
	{Name: "list_blocked_ips", Title: "Blocked IPs",
		Description: "IP addresses and ranges currently blocked from the servers' protocols.",
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			return c.api("GET", "/api/blocks", nil)
		}},
	{Name: "list_external_nodes", Title: "External nodes",
		Description: "Proxies elsewhere (a provider's, a friend's) imported as exits: name, kind, address, on or off, and what uses each (protocols passing through it, traffic rules, load balancers). Credentials are never shown.",
		Run:         func(c *mcpCall, a map[string]any) (any, error) { return c.api("GET", "/api/external-nodes", nil) }},
	{Name: "get_routing", Title: "Traffic splitting",
		Description: "The traffic rules in order (what each matches, which servers or protocols it applies to, where it sends traffic: direct, block, node:<protocol id>, ext:<external node id>, lb:<load balancer id>), the load balancers, the exits rules can use, and problems: what does not work as written and on which servers (that traffic is blocked there - it never leaves directly instead), rules that reach nothing, and site lists or countries Xray refused.",
		Run:         func(c *mcpCall, a map[string]any) (any, error) { return c.api("GET", "/api/routing", nil) }},
	{Name: "list_plugins", Title: "Plugins",
		Description: "The operator's plugins: name, version, whether each is on and running, what it brings and may do (its warnings say it in plain words), the API routes, pages, MCP tools (named <plugin>__<tool>) and timers its program added, and its last problem. Only the supervisor installs, updates, removes and turns plugins on or off, in the panel (Settings › Plugins) - never through MCP.",
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			v, err := c.api("GET", "/api/settings/plugins", nil)
			if err != nil {
				return nil, err
			}
			m, _ := v.(map[string]any)
			return map[string]any{"plugins": pick(m["plugins"], "id", "name", "version", "description", "enabled", "state", "parts",
				"permissions", "warnings", "running", "last_error", "restarts"), "started_without_plugins": m["disabled"]}, nil
		}},
	{Name: "action_status", Title: "Action status",
		Description: "The result of a server action (restart, upgrade, camouflage check, external node check) started earlier.",
		Props:       map[string]any{"action_id": pInt("Action id returned when it was started")}, Required: []string{"action_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "action_id")
			if err != nil {
				return nil, err
			}
			return c.api("GET", fmt.Sprintf("/api/actions/%d", id), nil)
		}},

	// ------------------------------------------------------------ change
	{Name: "create_user", Title: "Create user", Write: true,
		Description: "Create a user: they get a subscription link and, unless sign_in is false, a username and password to see their own usage on the panel's site. A generated password is returned once - pass it on to the user. By default the link covers all servers, including future ones. A plan_id (list_plans) fills in whatever is left out. Quota and end date only raise alerts; a speed limit and device_mode refuse are enforced by the servers.",
		Props: withProps(map[string]any{
			"name":         pStr("Who it is for"),
			"username":     pStr("Sign-in name (3-26 of a-z 0-9 . _ -); omit to make one from the name"),
			"password":     pStr("Sign-in password (10-72 characters); omit to have one generated"),
			"sign_in":      pBool("Give the user a sign-in (default true)"),
			"quota_gb":     pNum("Monthly quota in GB; 0 or omitted = unlimited"),
			"reset_day":    pInt("Day of the month usage resets (1-31); 0 = never"),
			"expires_on":   pStr("Last valid day, YYYY-MM-DD; omit for no end"),
			"ip_limit":     pInt("Alert when more IPs than this are online at once; 0 = no limit"),
			"server_ids":   pInts("Only these whole servers (with protocols added to them later); omit both server_ids and protocol_ids (or pass everything=true) for everything"),
			"protocol_ids": pInts("Single protocols (ids from list_servers), besides whole servers"),
			"everything":   pBool("true: every server, including ones added later (the default when server_ids and protocol_ids are left out)"),
			"note":         pStr("Private note"),
			"count":        pInt("Create several at once (max 500), numbered name-01, name-02, ... (as many digits as the count needs), each with a generated password"),
			"plan_id":      pInt("A preset plan (list_plans) that fills in what is left out: quota, counting, reset, limits, access, and the end date from its duration"),
		}, userLimitProps),
		Required: []string{"name"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			b, err := c.subBody(a)
			if err != nil {
				return nil, err
			}
			if n, ok := argInt(a, "count"); ok {
				b["count"] = n
			}
			if v, ok := argBool(a, "sign_in"); ok {
				b["sign_in"] = v
			}
			_, day := b["reset_day"]
			_, every := b["reset_every"]
			if _, plan := b["plan_id"]; !day && !every && !plan {
				b["reset_day"] = 1
			}
			v, err := c.api("POST", "/api/users", b)
			return pick(v, "id", "name", "username", "password", "link", "quota", "count_mode", "expires_at", "ip_limit", "device_mode",
				"speed_limit", "plan_id"), err
		}},
	{Name: "update_user", Title: "Change user", Write: true,
		Description: "Change a user's name, sign-in, limits, counting, period, servers or note. Only the given fields change; a new reset schedule keeps the usage counted so far. To start a new period on a plan use apply_plan. A new username or password signs the user out of their page; an empty username removes the sign-in. Changing servers takes effect within seconds; devices pick up new servers when they refresh.",
		Props: withProps(map[string]any{
			"user_id":      pInt("User id"),
			"name":         pStr("New name"),
			"username":     pStr("New sign-in name; empty = no sign-in"),
			"password":     pStr("New sign-in password (10-72 characters)"),
			"quota_gb":     pNum("Monthly quota in GB; 0 = unlimited"),
			"reset_day":    pInt("Day of the month usage resets (1-31); 0 = never"),
			"expires_on":   pStr("Last valid day, YYYY-MM-DD, or 'never'"),
			"ip_limit":     pInt("IP limit; 0 = none"),
			"server_ids":   pInts("Whole servers the user can use (with protocols added to them later); [] for none. Omit to keep the current ones"),
			"protocol_ids": pInts("Single protocols the user can use, besides whole servers; [] for none. Omit to keep the current ones"),
			"everything":   pBool("true: every server, including ones added later (server_ids and protocol_ids are then ignored)"),
			"note":         pStr("Private note"),
		}, userLimitProps),
		Required: []string{"user_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "user_id")
			if err != nil {
				return nil, err
			}
			b, err := c.subBody(a)
			if err != nil {
				return nil, err
			}
			v, err := c.api("PATCH", fmt.Sprintf("/api/users/%d", id), b)
			return pick(v, subSummaryKeys...), err
		}},
	subAction("pause_user", "Pause user", "pause", true,
		"Pause a user now: every device using their link is disconnected and cannot connect until they are resumed."),
	subAction("resume_user", "Resume user", "resume", false,
		"Resume a paused user; their devices can connect again right away."),
	subAction("rotate_user_link", "Issue a new link", "rotate-link", true,
		"Replace the user's subscription link. The old link stops working; devices that already imported it stay connected but cannot refresh until they import the new link."),
	subAction("reset_user_credentials", "Reset credentials", "reset-keys", true,
		"Give the user new proxy credentials. Every device is disconnected and must refresh the subscription (the link stays the same). Use it to kick out copied configs."),
	subAction("reset_user_usage", "Reset usage", "reset-usage", false,
		"Set this cycle's usage of a user back to zero. History stays."),
	subAction("sign_out_user", "Sign the user out", "sign-out", false,
		"End the user's sessions on their own page (their proxy connections are not affected)."),
	{Name: "new_user_password", Title: "New sign-in password", Write: true,
		Description: "Give a user a new generated password for their own page (and a username when they have none). It is returned once - pass it on with the sign-in address. Their sessions on the page end; their proxy connections are not affected.",
		Props:       map[string]any{"user_id": pInt("User id")}, Required: []string{"user_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "user_id")
			if err != nil {
				return nil, err
			}
			v, err := c.api("POST", fmt.Sprintf("/api/users/%d/new-password", id), nil)
			if err != nil {
				return nil, err
			}
			out := pick(v, "id", "name", "username", "password").(map[string]any)
			out["sign_in_at"] = c.p.userURL(c.r)
			return out, nil
		}},
	{Name: "list_plans", Title: "Preset plans",
		Description: "The preset plans users can be created with or put on: quota (bytes), count_mode, duration and duration_unit, resets, ip_limit and device_mode, speed_limit (Mbps), access (scope), price, and how many users are on each.",
		Run:         func(c *mcpCall, a map[string]any) (any, error) { return c.api("GET", "/api/plans", nil) }},
	{Name: "set_plan", Title: "Add or change a preset plan", Write: true,
		Description: "Add a plan (no plan_id) or change one (only the given fields). Changing a plan changes nobody unless update_users is true: then its users take the new quota, counting, reset, limits and access (their own start and end dates stay).",
		Props: withProps(map[string]any{
			"plan_id":       pInt("The plan to change; omit to add one"),
			"name":          pStr("Name, e.g. Monthly 100 GB"),
			"note":          pStr("Private note"),
			"quota_gb":      pNum("Quota per cycle in GB; 0 = unlimited"),
			"duration":      pInt("How long a user's period lasts, in duration_unit; 0 = no end"),
			"duration_unit": pEnum("Unit of duration", "day", "month"),
			"reset_day":     pInt("Day of the month usage resets (1-31); 0 = none"),
			"ip_limit":      pInt("Devices (IPs) online at once; 0 = no limit"),
			"server_ids":    pInts("Whole servers users on the plan get"),
			"protocol_ids":  pInts("Single protocols users on the plan get"),
			"everything":    pBool("true: every server, including ones added later"),
			"price":         pNum("For your records"),
			"currency":      pStr("e.g. USD"),
			"update_users":  pBool("Users on this plan take its changes too"),
		}, map[string]any{"count_mode": userLimitProps["count_mode"], "reset_every_days": userLimitProps["reset_every_days"],
			"speed_limit_mbps": userLimitProps["speed_limit_mbps"], "device_mode": userLimitProps["device_mode"]}),
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			b, err := c.subBody(a)
			if err != nil {
				return nil, err
			}
			delete(b, "plan_id")
			for _, k := range []string{"duration", "duration_unit", "price", "currency", "update_users"} {
				if v, ok := a[k]; ok {
					b[k] = v
				}
			}
			if id, ok := argInt(a, "plan_id"); ok && id > 0 {
				return c.api("PATCH", fmt.Sprintf("/api/plans/%d", id), b)
			}
			return c.api("POST", "/api/plans", b)
		}},
	{Name: "remove_plan", Title: "Remove a preset plan", Write: true,
		Description: "Remove a plan. Its users keep what it gave them.",
		Props:       map[string]any{"plan_id": pInt("Plan id")}, Required: []string{"plan_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "plan_id")
			if err != nil {
				return nil, err
			}
			return c.api("DELETE", fmt.Sprintf("/api/plans/%d", id), nil)
		}},
	{Name: "apply_plan", Title: "Start a new period on a plan", Write: true, Destructive: true,
		Description: "Put a user on a plan for a new period: its quota, counting, reset, limits and access replace the user's, the period starts on starts_on (default today) and ends after the plan's duration, and usage starts at zero unless reset_usage is false. Changing access can disconnect devices on servers the plan leaves out.",
		Props: map[string]any{"user_id": pInt("User id"), "plan_id": pInt("Plan id"), "starts_on": pStr("First day, YYYY-MM-DD; default now"),
			"reset_usage": pBool("Start with usage at zero (default true)")},
		Required: []string{"user_id", "plan_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			uid, err := needInt(a, "user_id")
			if err != nil {
				return nil, err
			}
			pid, err := needInt(a, "plan_id")
			if err != nil {
				return nil, err
			}
			b := map[string]any{"plan_id": pid}
			if v, ok := argBool(a, "reset_usage"); ok {
				b["reset_usage"] = v
			}
			start, ok, err := c.dateArg(a, "starts_on")
			if err != nil {
				return nil, err
			}
			if ok && start > 0 {
				b["starts_at"] = start - 24*3600 + 1
			}
			v, err := c.api("POST", fmt.Sprintf("/api/users/%d/plan", uid), b)
			return pick(v, subSummaryKeys...), err
		}},
	{Name: "delete_user", Title: "Delete user", Write: true, Destructive: true,
		Description: "Delete a user for good. Every device using their link is disconnected and the link stops working.",
		Props:       map[string]any{"user_id": pInt("User id")}, Required: []string{"user_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "user_id")
			if err != nil {
				return nil, err
			}
			if _, err := c.api("DELETE", fmt.Sprintf("/api/users/%d", id), nil); err != nil {
				return nil, err
			}
			return "Deleted.", nil
		}},
	{Name: "add_server", Title: "Add server", Write: true,
		Description: "Register a new server and get the one-line install command to run on it as root. The listed protocols are set up as soon as the agent connects.",
		Props: map[string]any{
			"name":         pStr("Display name, e.g. Tokyo 1"),
			"address":      pStr("Domain or IP clients connect to; omit to use the IP the agent reports. An IP here also sets the server's location (from DB-IP)"),
			"protocols":    map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": kindNames()}, "description": "Protocols to set up with their default settings (default: vless with REALITY, and hysteria2). Use add_protocol for other settings."},
			"public_ports": pStr(publicPortsHelp),
			"dynamic_dns":  pBool(dynamicDNSHelp),
			"cloudflare":   pBool(cloudflareDNSHelp),
		},
		Required: []string{"name"},
		Disrupts: func(c *mcpCall, a map[string]any) string { return cloudflareTakeover(nil, a) },
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			b := map[string]any{}
			b["name"], _ = argStr(a, "name")
			if v, ok := argStr(a, "address"); ok {
				b["address"] = v
			}
			dynamicDNSArgs(a, b)
			if v, ok := argStr(a, "public_ports"); ok {
				b["public_ports"] = v
			}
			if raw, ok := a["protocols"].([]any); ok {
				b["protocols"] = raw
			} else {
				b["protocols"] = []string{"vless", "hysteria2"}
			}
			v, err := c.api("POST", "/api/servers", b)
			if err != nil {
				return nil, err
			}
			m, _ := v.(map[string]any)
			srv, _ := m["server"].(map[string]any)
			return map[string]any{"server_id": srv["id"], "name": srv["name"], "install_command": m["install"],
				"next": "Run install_command on the server as root. The command contains the server's secret token - share it only with the person installing."}, nil
		}},
	{Name: "set_server_ports", Title: "Ports from the provider", Write: true,
		Description: "For a server whose provider decides its ports (NAT servers, LXC and Incus containers): the ports the provider forwards to it. New protocols and forwards then get one of these ports, others are refused, and links carry the provider's numbers. Nothing on the server restarts; if a protocol's port is not in the list, get_server shows it under limits.",
		Props: map[string]any{
			"server_id":    pInt("Server id"),
			"public_ports": pStr(publicPortsHelp + " Send an empty string for an ordinary server."),
		},
		Required: []string{"server_id", "public_ports"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "server_id")
			if err != nil {
				return nil, err
			}
			ports, _ := argStr(a, "public_ports")
			v, err := c.api("PATCH", fmt.Sprintf("/api/servers/%d", id), map[string]any{"public_ports": ports})
			if err != nil {
				return nil, err
			}
			m, _ := v.(map[string]any)
			srv, _ := m["server"].(map[string]any)
			out := pick(srv, "id", "name", "public_ports", "limits").(map[string]any)
			out["protocols"] = pick(srv["nodes"], "id", "label", "port", "public_port")
			return out, nil
		}},
	{Name: "set_panel_relay", Title: "Reach the panel through another server", Write: true,
		Description: "For a server that keeps losing the panel (panel_trouble in get_server and list_servers, the panel_trouble alert): its agent then reaches the panel through another of your servers (a relay), which passes the connection on unread - still encrypted end to end. Nothing restarts and nothing changes for users: the agent switches within a minute, and goes directly by itself while the relay cannot be reached. relay_server_id 0 = directly again. The relay must reach the panel directly itself and run agent 1.0 or later (caps.relay in get_server); a firewall in front of it must let in the TCP port the answer names (relay_port).",
		Props: map[string]any{
			"server_id":       pInt("The server that keeps losing the panel"),
			"relay_server_id": pInt("The server to reach the panel through; 0 = directly"),
		},
		Required: []string{"server_id", "relay_server_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "server_id")
			if err != nil {
				return nil, err
			}
			relay, ok := argInt(a, "relay_server_id")
			if !ok || relay < 0 {
				return nil, errors.New("relay_server_id must be a server id, or 0 for directly")
			}
			v, err := c.api("PATCH", fmt.Sprintf("/api/servers/%d", id), map[string]any{"panel_relay": relay})
			if err != nil {
				return nil, err
			}
			m, _ := v.(map[string]any)
			srv, _ := m["server"].(map[string]any)
			out := pick(srv, "id", "name", "panel_relay", "relay_name", "panel_path", "relay_error", "panel_trouble").(map[string]any)
			if relay > 0 { // where the relay listens, for its firewall
				if rv, err := c.api("GET", fmt.Sprintf("/api/servers/%d", relay), nil); err == nil {
					rm, _ := rv.(map[string]any)
					rs, _ := rm["server"].(map[string]any)
					out["relay_port"] = rs["relay_port"]
				}
			}
			return out, nil
		}},
	{Name: "set_agent_connection", Title: "How agents reach the panel", Write: true,
		Description: "How every agent talks to the panel, and the automatic relay. transport ws (the default): each agent keeps one WebSocket open to the panel, and makes HTTP requests by itself while it cannot (get_server's conn_error says why - often a proxy in front of the panel that does not pass WebSockets); http: HTTP requests only. auto_relay_server_id: a server that keeps losing the panel is switched to reach it through this server, once, by itself, with a notification (0 = off: the panel only tells). Nothing restarts and nobody is disconnected; agents follow within a minute. Only the given fields change.",
		Props: map[string]any{
			"transport":            pEnum("How agents talk to the panel", "ws", "http"),
			"auto_relay_server_id": pInt("The server that takes servers which keep losing the panel; 0 = off"),
		},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			v, err := c.api("GET", "/api/settings", nil)
			if err != nil {
				return nil, err
			}
			s, _ := v.(map[string]any)
			if x, ok := argStr(a, "transport"); ok {
				if x != "ws" && x != "http" {
					return nil, errors.New("transport is ws or http")
				}
				s["agent_transport"] = x
			}
			if x, ok := argInt(a, "auto_relay_server_id"); ok {
				if x < 0 {
					return nil, errors.New("auto_relay_server_id must be a server id, or 0 for off")
				}
				s["auto_relay"] = x
			}
			out, err := c.api("PUT", "/api/settings", s)
			return pick(out, "agent_transport", "auto_relay"), err
		}},
	{Name: "update_server", Title: "Change a server", Write: true,
		Description: "Change a server's name, address, IP version or ports from its provider, whether its IP address changes (dynamic_dns) and whether the panel keeps its name in Cloudflare (cloudflare). Only the given fields change; Xray takes them live (an IP version change restarts Hysteria2 protocols once). An IP as address also sets its location (DB-IP). Links change with the address, IP version and ports: devices pick that up when they refresh.",
		Props: map[string]any{
			"server_id":    pInt("Server id"),
			"name":         pStr("Display name"),
			"address":      pStr("Domain or IP clients connect to; empty = the IP the agent reports"),
			"ip_version":   pEnum("both (IPv4 and IPv6), ipv4 or ipv6: how protocols reach sites, which address links use, whether WireGuard routes IPv6", "both", "ipv4", "ipv6"),
			"public_ports": pStr(publicPortsHelp + " Empty string = every port."),
			"note":         pStr("A note for yourself"),
			"xray_code":    pStr("Your own Xray configuration (JSON, comments allowed), merged on top of what the panel generates: outbounds (added, or replacing the one with the same tag), routing.rules (before the panel's), inbounds by a protocol's tag n<id> (merged into it) or new ones, other sections such as dns. api, stats, log and policy are Meridian's. Only the syntax is checked; Xray's refusals show in get_server (apply_errors). Empty string removes it"),
			"dynamic_dns":  pBool(dynamicDNSHelp),
			"cloudflare":   pBool(cloudflareDNSHelp),
		},
		Required: []string{"server_id"},
		Disrupts: func(c *mcpCall, a map[string]any) string {
			id, _ := argInt(a, "server_id")
			s, err := c.p.ownServer(c.r.Context(), c.account, id)
			if err != nil {
				return ""
			}
			what := cloudflareTakeover(s, a) // the name's records in Cloudflare (ddns.go)
			v, ok := argStr(a, "ip_version")
			if v == "both" {
				v = ""
			}
			if ok && s.IPVersion != v {
				if n := c.p.countKind(c.r.Context(), s.ID, "hysteria2"); n > 0 {
					what = strings.TrimSpace(what + " " + fmt.Sprintf("Changing the IP version restarts %d Hysteria2 protocol(s) on %s once: their devices drop and reconnect.", n, s.Name))
				}
			}
			return what
		},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "server_id")
			if err != nil {
				return nil, err
			}
			b := map[string]any{}
			for _, k := range []string{"name", "address", "public_ports", "note", "xray_code"} {
				if v, ok := argStr(a, k); ok {
					b[k] = v
				}
			}
			if v, ok := argStr(a, "ip_version"); ok {
				if v == "both" {
					v = ""
				}
				b["ip_version"] = v
			}
			dynamicDNSArgs(a, b)
			v, err := c.api("PATCH", fmt.Sprintf("/api/servers/%d", id), b)
			if err != nil {
				return nil, err
			}
			m, _ := v.(map[string]any)
			srv, _ := m["server"].(map[string]any)
			return pick(srv, "id", "name", "address", "ip_version", "addrs", "public_ports", "country", "city", "limits",
				"ddns", "ddns_cloudflare", "dns"), nil
		}},
	{Name: "set_protocol_code", Title: "A protocol's own settings", Write: true,
		Description: "Set (or with an empty string remove) a protocol's own advanced settings as code, merged on top of what the panel generates. " + protocolCodeHelp + " Afterwards read apply_errors in get_server: what the core refuses leaves the running configuration as it was.",
		Props:       map[string]any{"protocol_id": pInt("Protocol id (list_servers)"), "code": pStr("The settings: JSON for Xray protocols, YAML for Hysteria2")},
		Required:    []string{"protocol_id", "code"},
		Disrupts: func(c *mcpCall, a map[string]any) string {
			id, _ := argInt(a, "protocol_id")
			if n, s, err := c.p.ownNode(c.r.Context(), c.account, id); err == nil && n.Kind == "hysteria2" {
				return fmt.Sprintf("This Hysteria2 protocol on %s restarts once to take the settings: its devices drop and reconnect.", s.Name)
			}
			return ""
		},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "protocol_id")
			if err != nil {
				return nil, err
			}
			code, _ := argStr(a, "code")
			v, err := c.api("PATCH", fmt.Sprintf("/api/nodes/%d", id), map[string]any{"code": code})
			return pick(v, "id", "kind", "label", "port", "code", "notes"), err
		}},
	{Name: "list_certificates", Title: "Shared certificates",
		Description: "The shared certificates (kept once, used by TLS and Hysteria2 protocols on any server): names, domains, expiry, and for each protocol using one whether its server serves it yet (live), holds it (installed, Xray loads it within ten minutes), has not taken it (pending), is offline or needs agent 0.6. Private keys are never shown.",
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			v, err := c.api("GET", "/api/certs", nil)
			return pick(v, "id", "name", "domains", "not_after", "sha256", "uses", "live"), err
		}},
	{Name: "replace_certificate", Title: "Replace a shared certificate", Write: true,
		Description: "Replace a shared certificate once for every server that uses it - after a renewal. The new certificate must cover every domain its protocols use. Xray loads it within ten minutes without disconnecting anyone; Hysteria2 restarts briefly. Then list_certificates shows each server taking it.",
		Props: map[string]any{
			"cert_id":  pInt("Shared certificate id (list_certificates)"),
			"cert_pem": pStr("The new certificate chain, PEM (fullchain.pem)"),
			"key_pem":  pStr("Its private key, PEM (privkey.pem)"),
		},
		Required: []string{"cert_id", "cert_pem", "key_pem"},
		Disrupts: func(c *mcpCall, a map[string]any) string {
			id, _ := argInt(a, "cert_id")
			var n int
			_ = c.p.db.QueryRowContext(c.r.Context(), `SELECT COUNT(*) FROM nodes n JOIN servers s ON s.id = n.server_id
				WHERE s.account_id = ? AND s.deleted_at = 0 AND n.kind = 'hysteria2' AND json_extract(n.settings, '$.cert_mode') = 'shared'
				AND CAST(json_extract(n.settings, '$.cert_id') AS INTEGER) = ?`, c.account.ID, id).Scan(&n)
			if n > 0 {
				return fmt.Sprintf("%d Hysteria2 protocol(s) use this certificate and restart once to take it: their devices drop and reconnect.", n)
			}
			return ""
		},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "cert_id")
			if err != nil {
				return nil, err
			}
			certPEM, _ := argStr(a, "cert_pem")
			keyPEM, _ := argStr(a, "key_pem")
			v, err := c.api("PATCH", fmt.Sprintf("/api/certs/%d", id), map[string]any{"cert_pem": certPEM, "key_pem": keyPEM})
			return pick(v, "id", "name", "domains", "not_after", "sha256", "uses", "live"), err
		}},
	{Name: "get_install_command", Title: "Install command", Write: true,
		Description: "The one-line agent install command of a server (contains its secret token).",
		Props:       map[string]any{"server_id": pInt("Server id")}, Required: []string{"server_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "server_id")
			if err != nil {
				return nil, err
			}
			v, err := c.api("GET", fmt.Sprintf("/api/servers/%d", id), nil)
			if err != nil {
				return nil, err
			}
			m, _ := v.(map[string]any)
			if m["install"] == nil {
				return nil, errors.New("not available with this token")
			}
			return m["install"], nil
		}},
	{Name: "check_protocol", Title: "Check a protocol",
		Description: "Before adding or changing a protocol: says whether a combination of protocol, transport and security works (and if not, why and what to do instead), how apps will name it, its usual ports, which apps can use it and what the admin needs to do. With protocol_id the settings are checked as a change to that protocol (what is left out keeps its value, as saving does); with server_id, for that server. Nothing is changed.",
		Props: func() map[string]any {
			m := protocolProps(false)
			m["protocol_id"] = pInt("Check a change to this existing protocol (list_servers); kind may then be left out")
			m["server_id"] = pInt("Check a new protocol for this server (its IP version and shared certificates count)")
			return m
		}(),
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			kind, _ := argStr(a, "kind")
			body := map[string]any{"kind": kind, "settings": protocolSettingsArg(a)}
			if id, ok := argInt(a, "protocol_id"); ok {
				body["node_id"] = id
			} else if kind == "" {
				return nil, errors.New("kind is needed (or protocol_id to check a change to an existing protocol)")
			}
			if id, ok := argInt(a, "server_id"); ok {
				body["server_id"] = id
			}
			return c.api("POST", "/api/protocols/check", body)
		}},
	{Name: "add_protocol", Title: "Add protocol", Write: true,
		Description: "Add a protocol to a server. It is applied live - nothing restarts. Only combinations that work are accepted (see check_protocol). Good defaults: vless with REALITY for most people; hysteria2 for long or lossy routes; shadowsocks for every app; vmess or vless over ws behind a CDN to hide the server; wireguard for a full VPN; socks or http for apps that need a plain proxy.",
		Props:       protocolProps(true), Required: []string{"server_id", "kind"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "server_id")
			if err != nil {
				return nil, err
			}
			kind, _ := argStr(a, "kind")
			b := map[string]any{"kind": kind, "settings": protocolSettingsArg(a)}
			if v, ok := argInt(a, "port"); ok {
				b["port"] = v
			}
			if v, ok := argStr(a, "label"); ok {
				b["name"] = v
			}
			if v, ok := argInt(a, "exit_protocol_id"); ok {
				b["pass_node"] = v
			}
			if v, ok := argInt(a, "exit_external_node_id"); ok {
				b["pass_ext"] = v
			}
			if v, ok := a["pass_only"].(bool); ok {
				b["pass_only"] = v
			}
			if v, ok := argStr(a, "bind_ip"); ok {
				b["bind_ip"] = v
			}
			if v, ok := argStr(a, "code"); ok {
				b["code"] = v
			}
			v, err := c.api("POST", fmt.Sprintf("/api/servers/%d/nodes", id), b)
			return pick(v, "id", "kind", "label", "net", "port", "public_port", "enabled", "settings", "apps", "notes", "pass_name", "pass_only"), err
		}},
	{Name: "scan_server", Title: "Look for existing proxies", Write: true,
		Description: "Ask a server's agent to look for proxy software already running there (Xray, V2Ray, 3x-ui, x-ui, sing-box, Hysteria2). It only reads. Returns an action id; when action_status says done, call get_scan.",
		Props:       map[string]any{"server_id": pInt("Server id")}, Required: []string{"server_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "server_id")
			if err != nil {
				return nil, err
			}
			return c.api("POST", fmt.Sprintf("/api/servers/%d/scan", id), nil)
		}},
	{Name: "get_scan", Title: "What the scan found",
		Description: "The proxy software, protocols, ports and user names the last scan found on a server, and whether each protocol can be imported. Keys and passwords are never shown.",
		Props:       map[string]any{"server_id": pInt("Server id")}, Required: []string{"server_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "server_id")
			if err != nil {
				return nil, err
			}
			return c.api("GET", fmt.Sprintf("/api/servers/%d/scan", id), nil)
		}},
	{Name: "import_protocols", Title: "Import found protocols", Write: true, Destructive: true,
		Description: "Import protocols the scan found. Every user keeps their credentials (new users are created, or matched by name). With take_over the old service is stopped and Meridian serves the same ports, so devices keep working - its users are disconnected for a moment. Without take_over nothing is stopped and busy ports move.",
		Props: map[string]any{
			"server_id": pInt("Server id"),
			"items": map[string]any{"type": "array", "description": "Protocols to import, as get_scan lists them",
				"items": map[string]any{"type": "object", "properties": map[string]any{"config": pStr("Config path"), "tag": pStr("Tag"), "port": pInt("Port")}}},
			"take_over": pBool("Stop the old service and use the same ports"),
		},
		Required: []string{"server_id", "items"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "server_id")
			if err != nil {
				return nil, err
			}
			take, _ := argBool(a, "take_over")
			v, err := c.api("POST", fmt.Sprintf("/api/servers/%d/import", id), map[string]any{"items": a["items"], "take_over": take, "confirm": take})
			return pick(v, "nodes", "users", "matched", "stop_actions", "notes"), err
		}},
	{Name: "get_access", Title: "Country rules",
		Description: "The country rule for the servers (block or allow-only countries), what each server follows, devices connected now by country, packets refused today, and the rule for who may open this site.",
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			return c.api("GET", "/api/access", nil)
		}},
	{Name: "set_country_rule", Title: "Set the servers' country rule", Write: true, Destructive: true,
		Description: "Block the listed countries, or allow only them, on every protocol and forward of the servers that follow the global rule (SSH is never touched). Applies within seconds and also cuts connections that are already open. mode off removes the rule.",
		Props: map[string]any{
			"mode":       pEnum("What to do", "off", "block", "allow"),
			"countries":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Two-letter country codes, e.g. CN, RU"},
			"exceptions": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Addresses or networks that always get in"},
		},
		Required: []string{"mode"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			body := map[string]any{"mode": a["mode"], "countries": a["countries"], "exceptions": a["exceptions"]}
			if body["countries"] == nil {
				body["countries"] = []string{}
			}
			if body["exceptions"] == nil {
				body["exceptions"] = []string{}
			}
			v, err := c.api("PUT", "/api/access/servers", body)
			return pick(v, "servers", "server_rules", "online_now"), err
		}},
	{Name: "set_protocol_enabled", Title: "Turn a protocol on or off", Write: true, Destructive: true,
		Description: "Turn a protocol on or off. Turning it off disconnects everyone using it on that server.",
		Props:       map[string]any{"protocol_id": pInt("Protocol id"), "enabled": pBool("true = on, false = off")},
		Required:    []string{"protocol_id", "enabled"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "protocol_id")
			if err != nil {
				return nil, err
			}
			on, ok := argBool(a, "enabled")
			if !ok {
				return nil, errors.New("enabled must be true or false")
			}
			v, err := c.api("PATCH", fmt.Sprintf("/api/nodes/%d", id), map[string]any{"enabled": on})
			return pick(v, "id", "kind", "port", "enabled"), err
		}},
	{Name: "update_protocol", Title: "Change a protocol", Write: true,
		Description: "Change an existing protocol: only the given fields change, and the result must still work (check_protocol with protocol_id first). Xray and WireGuard take it live. A change apps connect with (transport, security, domain, certificate, port, address, cipher, flow, obfuscation, VLESS Encryption, port hopping) means its devices stop working until they refresh their subscription, and Hysteria2 restarts once for any change but port hopping: then the tool says so and needs confirm=true. Its own settings as code: set_protocol_code; on or off: set_protocol_enabled.",
		Props: func() map[string]any {
			m := protocolProps(true)
			delete(m, "kind")
			delete(m, "server_id")
			delete(m, "code")
			m["protocol_id"] = pInt("Protocol id (list_servers)")
			m["port"] = pInt("New port")
			m["exit_protocol_id"] = pInt("Proxy pass: send its traffic out through that protocol on another server (it may pass on once more: a chain has two passes at most, each to another server); 0 turns the pass off")
			m["exit_external_node_id"] = pInt("Or a proxy pass through an external node (list_external_nodes); 0 turns it off")
			m["address_override"] = pStr("A different domain or IP in links for this protocol only; empty string = its own address or the server's")
			return m
		}(),
		Required: []string{"protocol_id"},
		Disrupts: func(c *mcpCall, a map[string]any) string {
			id, _ := argInt(a, "protocol_id")
			body := protocolChange(a)
			check := map[string]any{"node_id": id, "settings": body["settings"]}
			for _, k := range []string{"port", "bind_ip", "host"} {
				if v, ok := body[k]; ok {
					check[k] = v
				}
			}
			v, err := c.api("POST", "/api/protocols/check", check)
			m, _ := v.(map[string]any)
			if err != nil || m == nil || m["valid"] != true {
				return "" // saving refuses it with the reason
			}
			var what []string
			if r, _ := m["refresh"].([]any); len(r) > 0 {
				var names []string
				for _, x := range r {
					names = append(names, fmt.Sprint(x))
				}
				what = append(what, "This changes how apps connect ("+strings.Join(names, ", ")+"): devices using the protocol stop working until they refresh their subscription.")
			}
			if m["restarts"] == true {
				what = append(what, "This Hysteria2 protocol restarts once to take it: its devices drop and reconnect.")
			}
			if po, ok := a["pass_only"].(bool); ok && po {
				what = append(what, "Serving only proxy passes, it leaves users' links: nobody can connect to it directly any more.")
			}
			return strings.Join(what, " ")
		},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "protocol_id")
			if err != nil {
				return nil, err
			}
			v, err := c.api("PATCH", fmt.Sprintf("/api/nodes/%d", id), protocolChange(a))
			return pick(v, "id", "kind", "label", "net", "port", "public_port", "enabled", "settings", "apps", "notes", "pass_name", "pass_only", "bind_ip", "host"), err
		}},
	{Name: "remove_protocol", Title: "Remove a protocol", Write: true, Destructive: true,
		Description: "Remove a protocol from its server: everyone using it is disconnected and it leaves every user's link. Protocols on other servers that pass through it are blocked until they get another exit. Users left with no access at all are named in the timeline.",
		Props:       map[string]any{"protocol_id": pInt("Protocol id (list_servers)")}, Required: []string{"protocol_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "protocol_id")
			if err != nil {
				return nil, err
			}
			if _, err := c.api("DELETE", fmt.Sprintf("/api/nodes/%d", id), nil); err != nil {
				return nil, err
			}
			return "Removed.", nil
		}},
	{Name: "add_forward", Title: "Add port forward", Write: true,
		Description: "Forward a port on a server to another host:port (TCP and/or UDP). The kernel engine (nftables) is fastest and needs an IP target; realm also takes domain names and can send the PROXY protocol.",
		Props: map[string]any{
			"server_id":   pInt("Server id"),
			"target":      pStr("host:port, e.g. 203.0.113.7:443"),
			"listen_port": pInt("Port on the server; omit to pick a free one"),
			"network":     pEnum("Transport", "tcp+udp", "tcp", "udp"),
			"engine":      pEnum("nft (kernel, default) or realm", "nft", "realm"),
			"name":        pStr("Optional name"),
		},
		Required: []string{"server_id", "target"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "server_id")
			if err != nil {
				return nil, err
			}
			b := map[string]any{}
			b["target"], _ = argStr(a, "target")
			for _, k := range []string{"network", "engine", "name"} {
				if v, ok := argStr(a, k); ok && v != "" {
					b[k] = v
				}
			}
			if v, ok := argInt(a, "listen_port"); ok {
				b["listen_port"] = v
			}
			return c.api("POST", fmt.Sprintf("/api/servers/%d/forwards", id), b)
		}},
	{Name: "remove_forward", Title: "Remove port forward", Write: true, Destructive: true,
		Description: "Remove a port forward; connections through it are closed.",
		Props:       map[string]any{"forward_id": pInt("Forward id")}, Required: []string{"forward_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "forward_id")
			if err != nil {
				return nil, err
			}
			if _, err := c.api("DELETE", fmt.Sprintf("/api/forwards/%d", id), nil); err != nil {
				return nil, err
			}
			return "Removed.", nil
		}},
	{Name: "block_ip", Title: "Block an IP", Write: true, Destructive: true,
		Description: "Block an IP address or range from every protocol on every server and drop its open connections. SSH and other services are not affected.",
		Props: map[string]any{
			"ip":     pStr("IP address or CIDR range"),
			"hours":  pInt("How long; 0 or omitted = until removed"),
			"reason": pStr("Why, for the record"),
		},
		Required: []string{"ip"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			b := map[string]any{}
			b["ip"], _ = argStr(a, "ip")
			b["reason"], _ = argStr(a, "reason")
			if v, ok := argInt(a, "hours"); ok {
				b["hours"] = v
			}
			if _, err := c.api("POST", "/api/blocks", b); err != nil {
				return nil, err
			}
			return fmt.Sprintf("%s is blocked.", b["ip"]), nil
		}},
	{Name: "import_external_nodes", Title: "Import external nodes", Write: true,
		Description: "Import proxies elsewhere as exits, from share links (vless, vmess, trojan, ss - Shadowsocks and 2022 -, hysteria2/hy2, wireguard, https), a subscription's base64 content or Clash YAML (text), or a provider's subscription address (url, https only) fetched once. Answers what was added and, for each entry left out, why (nodes that turn certificate checks off or send traffic unencrypted are refused, so are kinds Xray cannot connect to, like TUIC or AnyTLS). Nothing uses a new node until a protocol passes through it or a rule sends traffic there.",
		Props:       map[string]any{"text": pStr("Links (one per line), base64 content or Clash YAML"), "url": pStr("Or a subscription address, https://...")},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			b := map[string]any{}
			b["text"], _ = argStr(a, "text")
			b["url"], _ = argStr(a, "url")
			return c.api("POST", "/api/external-nodes", b)
		}},
	{Name: "update_external_node", Title: "Change an external node", Write: true,
		Description: "Rename it, turn it on or off, or give it a new link (its address or credentials changed). Turned off, the protocols passing through it and the rules sending traffic there block that traffic until it is on again (they never leave from their own server instead); a new link moves their traffic to the new address at once. Either needs confirm=true while something uses the node: the tool then says what would happen.",
		Props: map[string]any{"node_id": pInt("External node id (list_external_nodes)"), "name": pStr("New name (unique among your external nodes)"), "enabled": pBool("On or off"),
			"link": pStr("A new share link"), "note": pStr("A note for yourself")},
		Required: []string{"node_id"},
		Disrupts: func(c *mcpCall, a map[string]any) string {
			id, _ := argInt(a, "node_id")
			x, err := c.p.ownExt(c.r.Context(), c.account, id)
			if err != nil {
				return ""
			}
			users := blockedBy(c.p.exitUses(c.r.Context(), x.AccountID, "ext", x.ID))
			if len(users) == 0 {
				return ""
			}
			if on, ok := argBool(a, "enabled"); ok && !on && x.Enabled {
				return fmt.Sprintf("Turning %s off blocks the traffic of %s until it is on again - nobody leaves from their own server instead.", x.Name, strings.Join(users, ", "))
			}
			if link, ok := argStr(a, "link"); ok && link != "" {
				return fmt.Sprintf("A new link moves the traffic of %s to the new address at once.", strings.Join(users, ", "))
			}
			return ""
		},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "node_id")
			if err != nil {
				return nil, err
			}
			b := map[string]any{}
			for _, k := range []string{"name", "link", "note"} {
				if v, ok := argStr(a, k); ok {
					b[k] = v
				}
			}
			if v, ok := argBool(a, "enabled"); ok {
				b["enabled"] = v
			}
			return c.api("PATCH", fmt.Sprintf("/api/external-nodes/%d", id), b)
		}},
	{Name: "check_external_node", Title: "Check an external node from a server", Write: true,
		Description: "Ask one of the servers to reach an external node: it opens a connection to the node (and, behind TLS or REALITY, makes a TLS handshake checked against the node's server name) and says whether that worked and how long it took. Nothing passes through the node; its credentials stay on the panel. Hysteria2 and WireGuard nodes speak UDP and cannot be checked this way. Waits up to 15 seconds for the answer, otherwise returns an action id for action_status. Needs agent 1.0 or later on that server.",
		Props:       map[string]any{"node_id": pInt("External node id (list_external_nodes)"), "server_id": pInt("The server to check from (list_servers)")},
		Required:    []string{"node_id", "server_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			nid, err := needInt(a, "node_id")
			if err != nil {
				return nil, err
			}
			sid, err := needInt(a, "server_id")
			if err != nil {
				return nil, err
			}
			v, err := c.api("POST", fmt.Sprintf("/api/external-nodes/%d/check", nid), map[string]any{"server_id": sid})
			if err != nil {
				return nil, err
			}
			m, _ := v.(map[string]any)
			x, err := c.p.ownExt(c.r.Context(), c.account, nid)
			if err != nil {
				return nil, err
			}
			s, err := c.p.ownServer(c.r.Context(), c.account, sid)
			if err != nil {
				return nil, err
			}
			aid, _ := argInt(m, "id")
			return c.p.waitExitCheck(c.r.Context(), aid, x.Name, s.Name), nil
		}},
	{Name: "remove_external_node", Title: "Remove an external node", Write: true, Destructive: true,
		Description: "Remove an external node. Protocols that passed through it and rules that sent traffic there block it until they get another exit (they never leave from their own server instead); load balancers lose a member. The answer lists what is blocked now.",
		Props:       map[string]any{"node_id": pInt("External node id")}, Required: []string{"node_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "node_id")
			if err != nil {
				return nil, err
			}
			return c.api("DELETE", fmt.Sprintf("/api/external-nodes/%d", id), nil)
		}},
	{Name: "protocol_users", Title: "Who can use a protocol",
		Description: "Every user with how they have one protocol: all (everything, new servers included), server (the whole server, new protocols included), protocol (this one) or none.",
		Props:       map[string]any{"protocol_id": pInt("Protocol id (list_servers)")}, Required: []string{"protocol_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "protocol_id")
			if err != nil {
				return nil, err
			}
			return c.api("GET", fmt.Sprintf("/api/nodes/%d/users", id), nil)
		}},
	{Name: "set_protocol_users", Title: "Give a protocol to users, or take it from them", Write: true,
		Description: "Give one protocol to some users and take it from others, in one step, applied live. Users who lose it are disconnected from it. Taking it from users who have it through everything or the whole server needs split=true: their access is written out as what they have now, less this protocol, and they no longer get new servers or protocols by themselves. Taking it from anyone needs confirm=true.",
		Props: map[string]any{"protocol_id": pInt("Protocol id (list_servers)"), "give": pInts("User ids who get it"), "take": pInts("User ids who lose it"),
			"split": pBool("Also take it from users who have it through everything or the whole server")},
		Required: []string{"protocol_id"},
		Disrupts: func(c *mcpCall, a map[string]any) string {
			if take, ok := argInts(a, "take"); ok && len(take) > 0 {
				return fmt.Sprintf("%d users lose this protocol: their devices connected through it are disconnected.", len(take))
			}
			return ""
		},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "protocol_id")
			if err != nil {
				return nil, err
			}
			b := map[string]any{"give": []int64{}, "take": []int64{}}
			if v, ok := argInts(a, "give"); ok {
				b["give"] = v
			}
			if v, ok := argInts(a, "take"); ok {
				b["take"] = v
			}
			if v, ok := argBool(a, "split"); ok {
				b["split"] = v
			}
			return c.api("PUT", fmt.Sprintf("/api/nodes/%d/users", id), b)
		}},
	{Name: "add_subscription_link", Title: "Add a provider's subscription link", Write: true,
		Description: "Keep a provider's subscription in the panel: it is read at once and again every every_hours hours, and its nodes become external nodes that follow the provider (new ones added, changed ones updated in place, gone ones removed - or kept while something still names them). They can be used as exits, and a load balancer member src:<id> stands for all of them. With offer=true users get them in their own subscriptions too (Meridian cannot count or limit that traffic). Answers what the first read found.",
		Props: map[string]any{"name": pStr("A name for it"), "url": pStr("The subscription address, https://..."),
			"every_hours": pInt("Read it again every this many hours (default 12; 0 = only when asked)"),
			"prefix":      pStr("Put in front of each node's name"), "include": pStr("Only nodes whose name holds one of these words (comma separated)"),
			"exclude": pStr("Leave out nodes whose name holds one of these words"), "offer": pBool("Give the nodes to every user too")},
		Required: []string{"name", "url"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			b := map[string]any{}
			for _, k := range []string{"name", "url", "prefix", "include", "exclude"} {
				if v, ok := argStr(a, k); ok {
					b[k] = v
				}
			}
			if v, ok := argInt(a, "every_hours"); ok {
				b["every_hours"] = v
			}
			if v, ok := argBool(a, "offer"); ok {
				b["offer"] = v
			}
			return c.api("POST", "/api/external-sources", b)
		}},
	{Name: "list_subscription_links", Title: "Subscription links",
		Description: "Providers' subscriptions the panel reads on a schedule: their nodes, when they were last read (and why that failed, if it did), what the provider says about traffic and expiry, and whether users get the nodes.",
		Run:         func(c *mcpCall, a map[string]any) (any, error) { return c.api("GET", "/api/external-sources", nil) }},
	{Name: "refresh_subscription_link", Title: "Read a subscription link again", Write: true,
		Description: "Read a provider's subscription now and bring its nodes in line. A failed read changes nothing.",
		Props:       map[string]any{"link_id": pInt("Subscription link id (list_subscription_links)")}, Required: []string{"link_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "link_id")
			if err != nil {
				return nil, err
			}
			return c.api("POST", fmt.Sprintf("/api/external-sources/%d/refresh", id), nil)
		}},
	{Name: "remove_subscription_link", Title: "Remove a subscription link", Write: true, Destructive: true,
		Description: "Remove a provider's subscription link. With keep_nodes=true its nodes stay as nodes imported once; otherwise they go too, and protocols and rules that used them block their traffic until they get another exit (they never leave from their own server instead).",
		Props:       map[string]any{"link_id": pInt("Subscription link id"), "keep_nodes": pBool("Keep its nodes as imported nodes")}, Required: []string{"link_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "link_id")
			if err != nil {
				return nil, err
			}
			q := ""
			if v, _ := argBool(a, "keep_nodes"); v {
				q = "?keep_nodes=1"
			}
			return c.api("DELETE", fmt.Sprintf("/api/external-sources/%d%s", id, q), nil)
		}},
	{Name: "set_traffic_rule", Title: "Add or change a traffic rule", Write: true,
		Description: "Traffic splitting on the servers' Xray protocols (VLESS, VMess, Trojan, Shadowsocks, SOCKS5, HTTP - not Hysteria2 or WireGuard): the first rule that matches sends the traffic directly, through a proxy pass (node:<protocol id> or ext:<external node id>), to a load balancer (lb:<id>) or blocks it; what no rule takes leaves as before. On the exit protocol's own server the traffic leaves the way that protocol's own traffic does. Without rule_id a new rule is added (last, or before before_rule_id). Match by sites (Xray site lists: openai, netflix, google, cn, category-ads-all ...), domains (example.com covers its subdomains; full:, keyword:, regexp:), countries and addresses (they match connections made to an address - most apps send names, so pair a country with its site list, e.g. cn), ports, network, bittorrent, or everything; giving any of these replaces what the rule matches. Applied live, nothing restarts. Traffic passing through a server from other servers never follows its rules. Afterwards check problems in get_routing: what cannot be used somewhere is blocked there. A rule that blocks everything needs confirm=true.",
		Props: map[string]any{
			"rule_id": pInt("Change this rule; omit to add one"), "name": pStr("A name for it"), "enabled": pBool("On or off"),
			"servers":   pInts("Servers it applies to; empty (and no protocols) = every server"),
			"protocols": pInts("Or the Xray protocols it applies to (ids from list_servers) - servers or protocols, not both"),
			"sites":     pStrs("Site list names"), "domains": pStrs("Domains"), "countries": pStrs("Two-letter country codes"),
			"ips": pStrs("Addresses or ranges"), "ports": pStr("e.g. 443 or 80,443,8000-9000"), "network": pEnum("Only this network", "tcp", "udp", ""),
			"bittorrent": pBool("BitTorrent connections"), "everything": pBool("All traffic of the protocols it applies to"),
			"target":         pStr("direct | block | node:<protocol id> | ext:<external node id> | lb:<load balancer id>"),
			"before_rule_id": pInt("When adding: put it before this rule"),
		},
		Disrupts: func(c *mcpCall, a map[string]any) string {
			target, hasTarget := argStr(a, "target")
			everything, hasAll := argBool(a, "everything")
			enabled, hasEnabled := argBool(a, "enabled")
			matchGiven := false
			for _, k := range []string{"sites", "domains", "countries", "ips", "ports", "network", "bittorrent", "everything"} {
				matchGiven = matchGiven || a[k] != nil
			}
			if id, ok := argInt(a, "rule_id"); ok && id > 0 {
				rt, err := c.p.ownRoute(c.r.Context(), c.account, id)
				if err != nil {
					return ""
				}
				if !hasTarget {
					target = rt.Target
				}
				if !matchGiven {
					everything = rt.Match.All
				} else {
					everything = hasAll && everything
				}
				if !hasEnabled {
					enabled = rt.Enabled
				}
			} else if !hasEnabled {
				enabled = true
			}
			if enabled && everything && target == "block" {
				return "This rule blocks all traffic of the protocols it applies to: everyone connected through them can reach nothing until it is changed."
			}
			return ""
		},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			b := map[string]any{}
			if v, ok := argStr(a, "name"); ok {
				b["name"] = v
			}
			if v, ok := argBool(a, "enabled"); ok {
				b["enabled"] = v
			}
			if v, ok := argInts(a, "servers"); ok {
				b["servers"] = v
			}
			if v, ok := argInts(a, "protocols"); ok {
				b["nodes"] = v
			}
			if v, ok := argStr(a, "target"); ok {
				b["target"] = v
			}
			m := map[string]any{}
			for _, k := range []string{"sites", "domains", "countries", "ips"} {
				if v, ok := argStrs(a, k); ok {
					m[k] = v
				}
			}
			for _, k := range []string{"ports", "network"} {
				if v, ok := argStr(a, k); ok {
					m[k] = v
				}
			}
			if v, ok := argBool(a, "bittorrent"); ok {
				m["bittorrent"] = v
			}
			if v, ok := argBool(a, "everything"); ok {
				m["all"] = v
			}
			if len(m) > 0 {
				b["match"] = m
			}
			if id, ok := argInt(a, "rule_id"); ok && id > 0 {
				return c.api("PATCH", fmt.Sprintf("/api/routing/rules/%d", id), b)
			}
			if v, ok := argInt(a, "before_rule_id"); ok && v > 0 {
				b["before"] = v
			}
			return c.api("POST", "/api/routing/rules", b)
		}},
	{Name: "remove_traffic_rule", Title: "Remove a traffic rule", Write: true,
		Description: "Remove a traffic rule; its traffic then follows the next rule that matches, or leaves as before.",
		Props:       map[string]any{"rule_id": pInt("Rule id")}, Required: []string{"rule_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "rule_id")
			if err != nil {
				return nil, err
			}
			return c.api("DELETE", fmt.Sprintf("/api/routing/rules/%d", id), nil)
		}},
	{Name: "order_traffic_rules", Title: "Reorder traffic rules", Write: true,
		Description: "Put the traffic rules in a new order: list every rule id; the first that matches wins.",
		Props:       map[string]any{"rule_ids": pInts("Every rule id, in order")}, Required: []string{"rule_ids"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			ids, _ := argInts(a, "rule_ids")
			return c.api("PUT", "/api/routing/order", map[string]any{"ids": ids})
		}},
	{Name: "set_load_balancer", Title: "Add or change a load balancer", Write: true,
		Description: "A load balancer shares the traffic rules send to it (target lb:<id>) among its members: protocols on your servers (node:<id>), external nodes (ext:<id>) or direct (the server itself). On a member protocol's own server, that member leaves the way the protocol's own traffic does. strategy random, roundRobin (take turns) or leastPing (the fastest; Xray then checks every member each minute, which starts with one Xray restart on each server it is used on - shown as restart needed and done only on a click; until then it picks at random). fallback (fastest first only): where traffic goes when no member answers, block (default) or direct; any other load balancer blocks it while none of its members can be used. Applied live.",
		Props: map[string]any{"balancer_id": pInt("Change this one; omit to add one"), "name": pStr("Its name"),
			"strategy": pEnum("How it picks", "random", "roundRobin", "leastPing"), "members": pStrs("node:<id>, ext:<id> or direct"),
			"fallback": pEnum("Fastest first only: when no member answers", "block", "direct")},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			b := map[string]any{}
			for _, k := range []string{"name", "strategy", "fallback"} {
				if v, ok := argStr(a, k); ok {
					b[k] = v
				}
			}
			if v, ok := argStrs(a, "members"); ok {
				b["members"] = v
			}
			if id, ok := argInt(a, "balancer_id"); ok && id > 0 {
				return c.api("PATCH", fmt.Sprintf("/api/routing/balancers/%d", id), b)
			}
			return c.api("POST", "/api/routing/balancers", b)
		}},
	{Name: "remove_load_balancer", Title: "Remove a load balancer", Write: true,
		Description: "Remove a load balancer no rule sends traffic to (send those rules elsewhere first).",
		Props:       map[string]any{"balancer_id": pInt("Load balancer id")}, Required: []string{"balancer_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "balancer_id")
			if err != nil {
				return nil, err
			}
			return c.api("DELETE", fmt.Sprintf("/api/routing/balancers/%d", id), nil)
		}},
	{Name: "unblock_ip", Title: "Unblock an IP", Write: true,
		Description: "Remove a block (see list_blocked_ips for ids).",
		Props:       map[string]any{"block_id": pInt("Block id")}, Required: []string{"block_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "block_id")
			if err != nil {
				return nil, err
			}
			if _, err := c.api("DELETE", fmt.Sprintf("/api/blocks/%d", id), nil); err != nil {
				return nil, err
			}
			return "Unblocked.", nil
		}},
	{Name: "check_updates", Title: "Updates",
		Description: "This panel's Meridian version, the newest release (as of the last look on GitHub, every few hours) with its notes, whether the panel can install updates itself, whether automatic updates are on, and which servers run an older agent.",
		Run:         func(c *mcpCall, a map[string]any) (any, error) { return c.api("GET", "/api/update", nil) }},
	{Name: "update_panel", Title: "Install the newest release", Write: true, Destructive: true,
		Description: "Install the newest Meridian release: the panel downloads it, checks its signature and checksum, backs up the database, and the updater service installs it. Proxies keep running; the panel restarts once (this connection drops for a few seconds). With agents (the default), every server's agent is upgraded afterwards - nobody is disconnected. Ask the user first.",
		Props: map[string]any{
			"agents": pBool("Also upgrade every server's agent afterwards (default true)"),
		},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			body := map[string]any{}
			if v, ok := argBool(a, "agents"); ok {
				body["agents"] = v
			}
			return c.api("POST", "/api/update/install", body)
		}},
	{Name: "upgrade_all_agents", Title: "Upgrade every agent", Write: true, Destructive: true,
		Description: "Upgrade the agent on every server whose agent is not the panel's version (offline ones when they connect). Agents restart themselves; proxies keep running and nobody is disconnected. Ask the user first.",
		Run:         func(c *mcpCall, a map[string]any) (any, error) { return c.api("POST", "/api/agents/upgrade", nil) }},
	{Name: "get_notifications", Title: "Notifications",
		Description: "Where notifications go (a Telegram chat, a webhook - both shown masked) and which groups are sent: servers (offline, back online, rebooted, refused configurations, crashed cores), users (data used up, access ended or ending, too many devices), certificates (shared ones expiring), security (sign-ins, failed sign-ins, password and two-factor changes, new tokens), health (high and critical health risks). Also when the last one went out and why the last attempt failed, if it did, and the Telegram bot: commands on or off, the daily report and its hour, whether changes from Telegram are allowed and by whom.",
		Run:         func(c *mcpCall, a map[string]any) (any, error) { return c.api("GET", "/api/settings/notify", nil) }},
	{Name: "set_notifications", Title: "Set up notifications", Write: true,
		Description: "Send problems that need the operator to Telegram and/or an HTTPS webhook (Slack, Discord, Mattermost work as they are). Only the given fields change; an empty telegram_token or webhook_url removes it (removing Telegram also stops its bot). Turning notifications on never sends the past; they never pause anything. The Telegram bot can also answer commands in its chat and send a daily report; allowing changes from Telegram (deciding about health risks, pausing users) and choosing who may use the bot are done in the panel itself, never here. Afterwards call test_notifications.",
		Props: map[string]any{
			"telegram_token": pStr("The bot token from @BotFather"),
			"telegram_chat":  pStr("The chat: a numeric id (a group's starts with -) or @channel"),
			"webhook_url":    pStr("An https:// address that receives JSON {text, content, events}"),
			"groups":         map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"servers", "users", "certificates", "security", "health"}}, "description": "What to send"},
			"daily_report":   pBool("Telegram: send a daily report (traffic, availability, what ends soon, data running out, open health risks)"),
			"report_hour":    pInt("Telegram: the report's hour, 0-23 in the panel's time zone (default 9)"),
			"commands":       pBool("Telegram: the bot answers commands in the chat (/status, /servers, /traffic, /users, /risks, ...)"),
			"user_link":      pBool("Telegram: users may link up to two Telegram accounts each and check their usage with the bot (/usage, /devices) and its Mini App; they unlink one at most once a month"),
		},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			body := map[string]any{}
			for _, k := range []string{"telegram_token", "telegram_chat", "webhook_url"} {
				if v, ok := argStr(a, k); ok {
					body[k] = v
				}
			}
			if v, ok := argBool(a, "daily_report"); ok {
				body["telegram_report"] = v
			}
			if v, ok := argInt(a, "report_hour"); ok {
				body["telegram_report_hour"] = v
			}
			if v, ok := argBool(a, "commands"); ok {
				body["telegram_commands"] = v
			}
			if v, ok := argBool(a, "user_link"); ok {
				body["telegram_user_link"] = v
			}
			if g, ok := a["groups"]; ok {
				body["groups"] = g
			}
			return c.api("PUT", "/api/settings/notify", body)
		}},
	{Name: "list_telegram_links", Title: "Linked Telegram accounts",
		Description: "The Telegram accounts linked to users (up to two each; they check their usage with the bot and open their page in its Mini App) and to the supervisor (they open the panel from Telegram), with when each was linked and last used.",
		Run:         func(c *mcpCall, a map[string]any) (any, error) { return c.api("GET", "/api/telegram/links", nil) }},
	{Name: "unlink_telegram", Title: "Unlink a Telegram account", Write: true, Destructive: true,
		Description: "Remove a link (list_telegram_links): that Telegram account no longer gets answers from the bot or opens anything in its Mini App. It can be linked again; a user's own monthly unlink is not used up by this.",
		Props:       map[string]any{"link_id": pInt("The link's id (list_telegram_links)")}, Required: []string{"link_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "link_id")
			if err != nil {
				return nil, err
			}
			return c.api("DELETE", fmt.Sprintf("/api/telegram/links/%d", id), nil)
		}},
	{Name: "decide_risk", Title: "Decide about a health risk", Write: true,
		Description: "Override what a health check found. expected: it is the operator's own doing - never flag it again on its server (scope all: on every server, future ones included). acknowledged: seen - flag it again if it happens again (scope all: every server's risk with this key). open: flag it again. Never mark anything expected or acknowledged unless the user said it is theirs or known: a risk may be a real break-in. Scope all with expected needs confirm=true.",
		Props: map[string]any{
			"risk_id":  pInt("The risk's id (list_risks)"),
			"decision": pEnum("What the user decided", "expected", "acknowledged", "open"),
			"scope":    pEnum("server (default): this server's risk; all: every server", "server", "all"),
		},
		Required: []string{"risk_id", "decision"},
		Disrupts: func(c *mcpCall, a map[string]any) string {
			if d, _ := argStr(a, "decision"); d == "expected" {
				if s, _ := argStr(a, "scope"); s == "all" {
					return "Marking it expected on all servers means it is never flagged again on any server, including servers added later."
				}
			}
			return ""
		},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "risk_id")
			if err != nil {
				return nil, err
			}
			d, _ := argStr(a, "decision")
			s, _ := argStr(a, "scope")
			return c.api("POST", fmt.Sprintf("/api/risks/%d/decide", id), map[string]any{"decision": d, "scope": s})
		}},
	{Name: "test_notifications", Title: "Send a test notification", Write: true,
		Description: "Send one test message through every notification channel that is set up; says per channel whether it arrived or why not.",
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			return c.api("POST", "/api/settings/notify/test", nil)
		}},
	{Name: "get_status_page", Title: "Status page",
		Description: "How the status page is set up: off, the site's front page (home) or at /status (page), and its own domain. Visitors see a sign-in there; users see their own data left, devices and usage; the supervisor sees every server on a live globe. Also where users sign in.",
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			v, err := c.api("GET", "/api/settings", nil)
			if err != nil {
				return nil, err
			}
			out := pick(v, statusKeys...).(map[string]any)
			out["user_sign_in"] = c.p.userURL(c.r)
			return out, nil
		}},
	{Name: "set_status_page", Title: "Set up the status page", Write: true,
		Description: "Turn the status page on or off. mode home: it is the site's front page (the panel stays at /overview); page: at /status; off: not at the front (users still sign in at /me). With public on, visitors see every server (place, up or down, load, bandwidth, traffic, expiry date) and sign in from the top-right button; users see their own usage; the supervisor sees everything. The servers' IP addresses appear on the page only with show_ips on - then to everyone who opens it, otherwise to nobody (the panel always shows them). Prices are never shown. Only the given fields change.",
		Props: map[string]any{
			"mode":       pEnum("Where it is", "off", "home", "page"),
			"public":     pBool("Visitors see every server without signing in (default true); false: only a sign-in"),
			"show_ips":   pBool("The page shows the servers' public IP addresses, to everyone who opens it (default false)"),
			"overview":   pBool("Visitors and users get the overview (globe, totals, resources, throughput); false: they start at the server list (default true)"),
			"events":     pBool("Visitors and users see the outages of the last 30 days (default true)"),
			"charts":     pStrs("The charts of a server's details visitors and users get: cpu, memory, disk, diskio, network, load, connections, temperature, ping (the ping monitors marked public); [] = none. Default: all"),
			"domain":     pStr("Its own domain (shows only the status page and user sign-in), e.g. status.example.com; empty removes it"),
			"about":      pStr("A line on the sign-in page, e.g. who runs the service"),
			"panel_city": pStr("Where the panel runs, drawn on the globe with an arc from every server: a city such as 'Hong Kong' or 'Frankfurt am Main, DE'; 'none' removes it"),
		},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			v, err := c.api("GET", "/api/settings", nil)
			if err != nil {
				return nil, err
			}
			s, _ := v.(map[string]any)
			for arg, key := range map[string]string{"mode": "status_page", "domain": "status_domain", "about": "status_about"} {
				if x, ok := argStr(a, arg); ok {
					s[key] = x
				}
			}
			for arg, key := range map[string]string{"public": "status_public", "show_ips": "status_ips", "overview": "status_overview", "events": "status_events"} {
				if x, ok := argBool(a, arg); ok {
					s[key] = x
				}
			}
			if charts, ok := argStrs(a, "charts"); ok {
				for _, c := range charts {
					if !slices.Contains(chartKinds, c) {
						return nil, fmt.Errorf("there is no chart %q - the charts are %s", c, strings.Join(chartKinds, ", "))
					}
				}
				if charts == nil {
					charts = []string{}
				}
				s["status_charts"] = charts
			}
			if city, ok := argStr(a, "panel_city"); ok {
				if city == "" || strings.EqualFold(city, "none") {
					s["status_hub"] = nil
				} else {
					pl, err := findPlace(city)
					if err != nil {
						return nil, err
					}
					s["status_hub"] = map[string]any{"city": pl.Name, "cc": pl.Country, "lat": pl.Lat, "lon": pl.Lon}
				}
			}
			out, err := c.api("PUT", "/api/settings", s)
			return pick(out, statusKeys...), err
		}},
	{Name: "list_ping_monitors", Title: "Ping monitors",
		Description: "Addresses the servers measure the way to at fixed intervals (three ICMP echoes or TCP connections a round), with each server's latest round and its loss over the last hour.",
		Run:         func(c *mcpCall, a map[string]any) (any, error) { return c.api("GET", "/api/ping-monitors", nil) }},
	{Name: "set_ping_monitor", Title: "Add or change a ping monitor", Write: true,
		Description: "Without monitor_id a new one is added (at most 20); with it, only the given fields change. The servers start measuring within seconds; rounds measured while a server cannot reach the panel arrive later. The status page shows a server's ping charts in its details when the monitor is public and status_charts include ping.",
		Props: map[string]any{
			"monitor_id": pInt("Change this monitor; omit to add one"), "name": pStr("A name for it, e.g. 'Google DNS'"),
			"target": pStr("A host name or IP address on the internet"), "kind": pEnum("icmp (echo, default) or tcp (connection time)", "icmp", "tcp"),
			"port": pInt("With tcp: the port"), "every_secs": pInt("Seconds between rounds, 10-3600 (default 60)"),
			"servers": pInts("The servers that measure it; empty = all of them, new ones too"),
			"public":  pBool("Shown on the status page (default true)"), "enabled": pBool("Measuring or not"),
		},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			b := map[string]any{}
			for _, k := range []string{"name", "target", "kind"} {
				if v, ok := argStr(a, k); ok {
					b[k] = v
				}
			}
			for _, k := range []string{"port", "every_secs"} {
				if v, ok := argInt(a, k); ok {
					b[k] = v
				}
			}
			for _, k := range []string{"public", "enabled"} {
				if v, ok := argBool(a, k); ok {
					b[k] = v
				}
			}
			if v, ok := argInts(a, "servers"); ok {
				if v == nil {
					v = []int64{}
				}
				b["servers"] = v
			}
			if id, ok := argInt(a, "monitor_id"); ok {
				return c.api("PATCH", fmt.Sprintf("/api/ping-monitors/%d", id), b)
			}
			return c.api("POST", "/api/ping-monitors", b)
		}},
	{Name: "remove_ping_monitor", Title: "Remove a ping monitor", Write: true, Destructive: true,
		Description: "The servers stop measuring it and its measurements are deleted.",
		Props:       map[string]any{"monitor_id": pInt("Ping monitor id (list_ping_monitors)")}, Required: []string{"monitor_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "monitor_id")
			if err != nil {
				return nil, err
			}
			return c.api("DELETE", fmt.Sprintf("/api/ping-monitors/%d", id), nil)
		}},
	{Name: "server_charts", Title: "A server's charts",
		Description: "A server's processor, memory, disk, disk activity, network, load, connections and temperature over time, and its ping monitors' round trips and loss. Missing points mean it did not report.",
		Props:       map[string]any{"server_id": pInt("Server id"), "range": pEnum("How far back (default 24h)", "1h", "6h", "24h", "7d", "30d")},
		Required:    []string{"server_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "server_id")
			if err != nil {
				return nil, err
			}
			rng, _ := argStr(a, "range")
			return c.api("GET", fmt.Sprintf("/api/servers/%d/series?range=%s", id, url.QueryEscape(rng)), nil)
		}},
	{Name: "set_branding", Title: "Name, logo and animation", Write: true,
		Description: "What users see of the panel: its name, its logo and how the logo moves. Give the logo as SVG markup (logo_svg) or a base64 PNG, JPEG or WebP (logo_base64), up to 128 KB; SVGs may only draw - the answer says what to remove otherwise. reset_logo=true brings back the built-in umbrella. Only the given fields change.",
		Props: map[string]any{
			"name":        pStr("The panel's name, shown on every page and in users' apps (up to 64 characters)"),
			"animation":   pEnum("How the logo moves while pages load and when someone signs in (assemble needs the built-in umbrella; an uploaded logo rises instead)", logoAnimations...),
			"look":        pEnum("How the panel, the status page and users' pages look to people who have not picked a look themselves: umbrella (black, white and signal red, a laboratory's), romance (blush and rose, a serif voice), the quiet tones ice, celadon, ink, paper, mist - or automatic (Ice, Paper on light devices)", append([]string{"automatic"}, siteTones...)...),
			"logo_svg":    pStr("A new logo as SVG markup"),
			"logo_base64": pStr("A new logo as a base64 PNG, JPEG or WebP image"),
			"reset_logo":  pBool("Use the built-in umbrella again"),
		},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			svg, _ := argStr(a, "logo_svg")
			b64, _ := argStr(a, "logo_base64")
			reset, _ := argBool(a, "reset_logo")
			if n := btoi(svg != "") + btoi(b64 != "") + btoi(reset); n > 1 {
				return nil, errors.New("give only one of logo_svg, logo_base64 and reset_logo")
			}
			switch {
			case reset:
				if _, err := c.api("DELETE", "/api/settings/logo", nil); err != nil {
					return nil, err
				}
			case svg != "":
				if _, err := c.send("PUT", "/api/settings/logo", []byte(svg), "image/svg+xml"); err != nil {
					return nil, err
				}
			case b64 != "":
				raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
				if err != nil {
					return nil, errors.New("logo_base64 is not valid base64")
				}
				if _, err := c.send("PUT", "/api/settings/logo", raw, "application/octet-stream"); err != nil {
					return nil, err
				}
			}
			change := map[string]any{}
			if x, ok := argStr(a, "name"); ok && x != "" {
				change["site_title"] = x
			}
			if x, ok := argStr(a, "animation"); ok && x != "" {
				change["logo_animation"] = x
			}
			if x, ok := argStr(a, "look"); ok && x != "" {
				if x == "automatic" {
					x = ""
				}
				change["default_tone"] = x
			}
			if len(change) > 0 {
				if _, err := c.api("PUT", "/api/settings", change); err != nil {
					return nil, err
				}
			}
			return map[string]any{"name": c.p.settings().SiteTitle, "logo": c.p.logoInfo(), "look": nz(c.p.settings().DefaultTone, "automatic")}, nil
		}},
	{Name: "set_server_on_status_page", Title: "A server on the status page", Write: true,
		Description: "How a server appears on the status page: shown or hidden, the name shown there, and where it sits on the globe. IP databases often place data-centre addresses at the provider's office; set city to correct it.",
		Props: map[string]any{
			"server_id":   pInt("Server id"),
			"shown":       pBool("Show it on the status page"),
			"public_name": pStr("Name shown there; empty = the server's own name"),
			"city":        pStr("Where it is: a city such as 'Los Angeles' or 'Osaka, JP'; 'auto' = from the IP database"),
		},
		Required: []string{"server_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "server_id")
			if err != nil {
				return nil, err
			}
			b := map[string]any{}
			if x, ok := argBool(a, "shown"); ok {
				b["status_hidden"] = !x
			}
			if x, ok := argStr(a, "public_name"); ok {
				b["public_name"] = x
			}
			if city, ok := argStr(a, "city"); ok && city != "" {
				if strings.EqualFold(city, "auto") {
					b["auto_location"] = true
				} else {
					pl, err := findPlace(city)
					if err != nil {
						return nil, err
					}
					b["location"] = map[string]any{"city": pl.Name, "cc": pl.Country, "lat": pl.Lat, "lon": pl.Lon}
				}
			}
			v, err := c.api("PATCH", fmt.Sprintf("/api/servers/%d", id), b)
			if err != nil {
				return nil, err
			}
			m, _ := v.(map[string]any)
			return pick(m["server"], "id", "name", "public_name", "status_hidden", "city", "country", "lat", "lon", "loc_manual"), nil
		}},
	{Name: "server_action", Title: "Restart or upgrade", Write: true, Destructive: true,
		Description: "Run a maintenance action on a server: restart_pending (restarts exactly what waits for a restart - the server's pending_restart - and disconnects those users for a moment), restart_xray or upgrade_xray (disconnects Xray users for a moment), upgrade_hysteria or upgrade_realm (switches to the version in Settings; those users reconnect), upgrade_agent (nobody is disconnected). Returns an action id for action_status.",
		Props: map[string]any{
			"server_id": pInt("Server id"),
			"action":    pEnum("What to do", "restart_pending", "restart_xray", "upgrade_xray", "upgrade_hysteria", "upgrade_realm", "upgrade_agent"),
		},
		Required: []string{"server_id", "action"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "server_id")
			if err != nil {
				return nil, err
			}
			kind, _ := argStr(a, "action")
			return c.api("POST", fmt.Sprintf("/api/servers/%d/actions", id), map[string]any{"kind": kind})
		}},
}

var statusKeys = []string{"status_page", "status_domain", "status_about", "status_hub", "status_public", "status_ips", "status_overview", "status_events",
	"status_charts"}

// findPlace looks up a city of the panel's list: "Osaka" or "Osaka, JP".
func findPlace(q string) (place, error) {
	name, cc, _ := strings.Cut(q, ",")
	name, cc = strings.TrimSpace(name), strings.ToUpper(strings.TrimSpace(cc))
	for _, pl := range places {
		if strings.EqualFold(pl.Name, name) && (cc == "" || pl.Country == cc) {
			return pl, nil
		}
	}
	return place{}, fmt.Errorf("%q is not in the list of cities - pick one the panel knows (the server page's location editor lists them), or set exact coordinates there", q)
}

func subAction(name, title, action string, destructive bool, desc string) mcpTool {
	return mcpTool{Name: name, Title: title, Write: true, Destructive: destructive, Description: desc,
		Props: map[string]any{"user_id": pInt("User id")}, Required: []string{"user_id"},
		Run: func(c *mcpCall, a map[string]any) (any, error) {
			id, err := needInt(a, "user_id")
			if err != nil {
				return nil, err
			}
			v, err := c.api("POST", fmt.Sprintf("/api/users/%d/%s", id, action), nil)
			return pick(v, "id", "name", "status", "link"), err
		}}
}

func kindNames() []string {
	out := make([]string, 0, len(kindList))
	for _, k := range kindList {
		out = append(out, k.Kind)
	}
	return out
}

// protocolProps are the tool arguments that describe a protocol.
func protocolProps(withServer bool) map[string]any {
	m := map[string]any{
		"kind":         pEnum("Protocol", kindNames()...),
		"transport":    pEnum("Xray protocols: how it travels (default raw)", allTransports...),
		"security":     pEnum("Xray protocols: none, tls or reality (REALITY: VLESS and Trojan)", secNone, secTLS, secReality),
		"sni":          pStr("TLS: the certificate's domain. REALITY: the camouflage site (default www.apple.com). Hysteria2: the certificate name"),
		"target":       pStr("REALITY: where visitors who are not users go, host:port (default sni:443)"),
		"own_site":     pBool("REALITY: the camouflage is your own website on this server; target is then 127.0.0.1:port"),
		"cert_mode":    pEnum("TLS and Hysteria2 certificate: self (self-signed, pinned in apps that can), acme (Let's Encrypt, needs a domain pointing at the server and TCP port 80), custom (cert_pem and key_pem), shared (one of list_certificates, with cert_id)", certModes...),
		"cert_id":      pInt("cert_mode shared: the certificate's id from list_certificates (it must cover sni)"),
		"cert_pem":     pStr("cert_mode custom: the certificate chain (PEM)"),
		"key_pem":      pStr("cert_mode custom: the private key (PEM)"),
		"path":         pStr("ws, httpupgrade, xhttp: the path (default random)"),
		"host_header":  pStr("ws, httpupgrade, xhttp: the Host header"),
		"service_name": pStr("grpc: the service name (default random)"),
		"xhttp_mode":   pEnum("xhttp mode", xhttpModes...),
		"flow":         pEnum("VLESS over raw with TLS or REALITY, or VLESS with VLESS Encryption: xtls-rprx-vision (default) or none", flowVision, "none"),
		"encryption":   pEnum("VLESS: VLESS Encryption (post-quantum, Xray-based and Clash Meta apps and Stash only) - none (off), native (with TLS or REALITY), xorpub, or random (looks like random data: the choice without TLS). With it VLESS may run without TLS", append([]string{"none"}, encModes...)...),
		"enc_auth":     pEnum("VLESS Encryption: how the server proves itself - x25519 (default, short links) or mlkem768 (post-quantum too; links about 1.6 KB longer)", encAuths...),
		"fingerprint":  pEnum("Browser fingerprint apps present", fingerprints...),
		"cdn":          pBool("Behind a CDN that adds TLS (vless, vmess, trojan over ws, httpupgrade or xhttp with security none)"),
		"cdn_host":     pStr("CDN: the domain clients connect to"),
		"cdn_port":     pInt("CDN: the port clients connect to (default 443)"),
		"method":       pEnum("Shadowsocks cipher (2022 methods recommended)", ssMethods...),
		"udp":          pBool("SOCKS5: allow UDP"),
		"obfs":         pBool("Hysteria2: salamander obfuscation"),
		"up_mbps":      pInt("Hysteria2: server upload limit, 0 = none"),
		"down_mbps":    pInt("Hysteria2: server download limit, 0 = none"),
		"hop_ports":    pStr("Hysteria2: port hopping - a UDP port range apps hop between, e.g. 20000-30000 (from 1024; best below 32768); the agent redirects it to the protocol's port live (needs nftables and agent 1.0). Empty string turns it off"),
		"mtu":          pInt("WireGuard MTU"),
		"full_tunnel":  pBool("WireGuard: send all traffic through the VPN (default true)"),
		"dns_logging":  pBool("WireGuard: clients use the server's resolver, which logs lookups (default true)"),
		"ipv6":         pBool("WireGuard: route IPv6 through the tunnel too (default false: IPv4 only; needs IPv6 on the server and agent 0.6)"),
	}
	if withServer {
		m["server_id"] = pInt("Server id")
		m["port"] = pInt("Port; omit to pick a free common one")
		m["label"] = pStr("Optional label shown in apps")
		m["exit_protocol_id"] = pInt("Proxy pass: send this protocol's traffic out through that protocol on another server (an Xray entry; any exit but WireGuard). The exit may pass on once more (a relay): a chain has two passes at most, each to another server")
		m["exit_external_node_id"] = pInt("Or a proxy pass through an external node (list_external_nodes): users then appear at that node")
		m["pass_only"] = map[string]any{"type": "boolean", "description": "Serve only proxy passes from other servers: users cannot connect to it directly and it is left out of their links. Use it for an exit that people should reach only through a relay"}
		m["bind_ip"] = pStr("One of the server's addresses (addrs in get_server) for this protocol alone: it listens there, its traffic leaves from there, links use it. Protocols on different addresses may share a port. Omit for all addresses")
		m["code"] = pStr(protocolCodeHelp)
	}
	return m
}

// protocolSettingsArg maps tool arguments onto the protocol settings input.
func protocolSettingsArg(a map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"transport", "security", "sni", "target", "cert_mode", "cert_pem", "key_pem", "path",
		"host_header", "service_name", "xhttp_mode", "fingerprint", "cdn_host", "method", "encryption", "enc_auth", "hop_ports"} {
		if v, ok := argStr(a, k); ok {
			out[k] = v
		}
	}
	if v, ok := argStr(a, "flow"); ok {
		if v == "none" {
			v = ""
		}
		out["flow"] = v
	}
	for _, k := range []string{"own_site", "cdn", "udp", "obfs", "full_tunnel", "dns_logging", "ipv6"} {
		if v, ok := argBool(a, k); ok {
			out[k] = v
		}
	}
	for _, k := range []string{"cdn_port", "up_mbps", "down_mbps", "mtu", "cert_id"} {
		if v, ok := argInt(a, k); ok {
			out[k] = v
		}
	}
	return out
}

// protocolChange maps update_protocol's arguments onto PATCH /api/nodes/{id}: only what is given.
func protocolChange(a map[string]any) map[string]any {
	b := map[string]any{"settings": protocolSettingsArg(a)}
	if v, ok := argInt(a, "port"); ok {
		b["port"] = v
	}
	if v, ok := argStr(a, "label"); ok {
		b["name"] = v
	}
	if v, ok := argInt(a, "exit_protocol_id"); ok {
		b["pass_node"] = v
	}
	if v, ok := argInt(a, "exit_external_node_id"); ok {
		b["pass_ext"] = v
	}
	if v, ok := a["pass_only"].(bool); ok {
		b["pass_only"] = v
	}
	if v, ok := argStr(a, "bind_ip"); ok {
		b["bind_ip"] = v
	}
	if v, ok := argStr(a, "address_override"); ok {
		b["host"] = v
	}
	return b
}

// countKind counts a server's protocols of a kind.
func (p *Panel) countKind(ctx context.Context, serverID int64, kind string) int {
	var n int
	_ = p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE server_id = ? AND kind = ?`, serverID, kind).Scan(&n)
	return n
}

// findSharing scores users by how shared their links look.
func (c *mcpCall) findSharing(days int64) (any, error) {
	v, err := c.api("GET", "/api/users", nil)
	if err != nil {
		return nil, err
	}
	type suspect struct {
		ID        any      `json:"user_id"`
		Name      any      `json:"name"`
		Score     float64  `json:"score"`
		OnlineNow float64  `json:"online_ips_now"`
		IPLimit   float64  `json:"ip_limit"`
		IPs       int      `json:"distinct_ips"`
		Countries []string `json:"countries"`
		Networks  int      `json:"networks"`
		Reasons   []string `json:"reasons"`
	}
	// suspects are picked by their distinct IPs over the whole period (a link shared last week may
	// have had one device today), the ones over their limit now first; only the top ones are looked
	// at in detail
	seen := map[int64]int{}
	if rows, err := c.p.db.QueryContext(c.r.Context(), `SELECT sub_id, COUNT(DISTINCT ip) FROM ip_log WHERE last_seen >= ?
		GROUP BY sub_id`, now()-days*86400); err == nil {
		for rows.Next() {
			var id int64
			var n int
			if rows.Scan(&id, &n) == nil {
				seen[id] = n
			}
		}
		rows.Close()
	}
	type candidate struct {
		m      map[string]any
		ips    int
		over   bool
		online float64
		limit  float64
		id     int64
	}
	var cands []candidate
	all, _ := v.([]any)
	for _, x := range all {
		m, _ := x.(map[string]any)
		online, _ := m["online_ips"].(float64)
		limit, _ := m["ip_limit"].(float64)
		id, _ := m["id"].(float64)
		cd := candidate{m: m, ips: seen[int64(id)], over: limit > 0 && online > limit, online: online, limit: limit, id: int64(id)}
		if cd.ips >= 3 || cd.over {
			cands = append(cands, cd)
		}
	}
	slices.SortFunc(cands, func(a, b candidate) int {
		if a.over != b.over {
			if a.over {
				return -1
			}
			return 1
		}
		return b.ips - a.ips
	})
	if len(cands) > 60 { // keep the detailed look bounded
		cands = cands[:60]
	}
	var out []suspect
	for _, cd := range cands {
		m, online, limit, over, id := cd.m, cd.online, cd.limit, cd.over, cd.id
		rows, err := c.api("GET", fmt.Sprintf("/api/users/%d/ips?days=%d", id, days), nil)
		if err != nil {
			continue
		}
		list, _ := rows.([]any)
		countries := map[string]bool{}
		nets := map[float64]bool{}
		for _, r := range list {
			row, _ := r.(map[string]any)
			if cc, _ := row["country"].(string); cc != "" {
				countries[cc] = true
			}
			if asn, _ := row["asn"].(float64); asn > 0 {
				nets[asn] = true
			}
		}
		s := suspect{ID: m["id"], Name: m["name"], OnlineNow: online, IPLimit: limit, IPs: len(list), Networks: len(nets)}
		for cc := range countries {
			s.Countries = append(s.Countries, cc)
		}
		slices.Sort(s.Countries)
		s.Score = float64(len(list)) + 3*float64(max(len(countries)-1, 0)) + 1.5*float64(max(len(nets)-1, 0))
		if over {
			s.Score += 10
			s.Reasons = append(s.Reasons, fmt.Sprintf("%d IPs online now, limit %d", int(online), int(limit)))
		}
		if len(list) >= 5 {
			s.Reasons = append(s.Reasons, fmt.Sprintf("%d different IPs in %d day(s)", len(list), days))
		}
		if len(countries) > 1 {
			s.Reasons = append(s.Reasons, fmt.Sprintf("used from %d countries", len(countries)))
		}
		if len(nets) > 2 {
			s.Reasons = append(s.Reasons, fmt.Sprintf("used from %d different networks", len(nets)))
		}
		if len(s.Reasons) == 0 {
			continue
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b suspect) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		}
		return 0
	})
	if len(out) > 15 {
		out = out[:15]
	}
	if len(out) == 0 {
		return "No user's link looks shared in this period.", nil
	}
	return out, nil
}
