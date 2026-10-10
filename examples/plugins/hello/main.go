// Hello is an example Rosélune server plugin: a program the panel starts and talks to over its
// standard input and output - JSON-RPC 2.0, one JSON object per line. It uses every part of the
// protocol:
//
//   - register: as soon as it starts it tells the panel what it does
//   - event: it counts what the panel records in its timeline (the events permission)
//   - filter.subscription: it puts a star before every entry of users' subscriptions
//   - route: it answers GET /api/plugins/hello/stats (the routes permission)
//   - mcp: it offers the MCP tool hello__summary (the mcp permission)
//   - tick and api: every five minutes it counts the panel's servers through the panel's API
//     (the schedule and api:read permissions) and keeps its counts in its data folder
//   - log: it writes to its log on the Plugins page; what it writes to standard error lands there too
//
// It uses nothing but Go's standard library. Build it for the panel's machine and zip it with
// plugin.json, panel.js and status.js - README.md shows how. docs/plugins.md describes the protocol.
package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// message is a JSON-RPC 2.0 message in either direction.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ---------------------------------------------------------------- talking to the panel

// conn is the line to the panel: what we write to standard output reaches it, what it sends arrives
// on standard input.
type conn struct {
	out     sync.Mutex // one message at a time on standard output
	mu      sync.Mutex
	next    int
	pending map[string]chan message // our calls waiting for their answers, by id
}

func (c *conn) send(m any) {
	b, err := json.Marshal(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot encode a message:", err)
		return
	}
	c.out.Lock()
	defer c.out.Unlock()
	os.Stdout.Write(append(b, '\n'))
}

// call asks the panel something and waits for its answer.
func (c *conn) call(method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.next++
	id := strconv.Itoa(c.next)
	ch := make(chan message, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	c.send(map[string]any{"jsonrpc": "2.0", "id": c.next, "method": method, "params": params})
	select {
	case m := <-ch:
		if m.Error != nil {
			return nil, errors.New(m.Error.Message)
		}
		return m.Result, nil
	case <-time.After(time.Minute):
		return nil, errors.New("the panel did not answer")
	}
}

// answered hands the panel's answer to the call waiting for it.
func (c *conn) answered(m message) {
	c.mu.Lock()
	ch := c.pending[string(m.ID)]
	c.mu.Unlock()
	if ch != nil {
		ch <- m
	}
}

func (c *conn) reply(id json.RawMessage, result any) {
	c.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (c *conn) fail(id json.RawMessage, text string) {
	c.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": rpcError{Code: -32000, Message: text}})
}

// log writes a line to the plugin's log on the Plugins page.
func (c *conn) log(level, text string) {
	c.send(map[string]any{"jsonrpc": "2.0", "method": "log", "params": map[string]string{"level": level, "message": text}})
}

// ---------------------------------------------------------------- what it keeps

// event is one entry of the panel's timeline, as the panel sends it.
type event struct {
	ID      int64  `json:"id"`
	TS      int64  `json:"ts"`
	Level   string `json:"level"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

type stats struct {
	mu        sync.Mutex
	Since     int64            `json:"since"`      // when it started counting (Unix seconds)
	Events    map[string]int64 `json:"events"`     // events by kind
	Latest    []event          `json:"latest"`     // the newest first, at most 20
	Servers   int              `json:"servers"`    // servers at the last count
	CountedAt int64            `json:"counted_at"` // when they were counted
	Starred   int64            `json:"starred"`    // subscriptions it put stars in
}

func (s *stats) file() string {
	return filepath.Join(os.Getenv("MERIDIAN_PLUGIN_DATA"), "stats.json") // the panel sets this folder for us
}

// load reads what was counted before the last restart.
func (s *stats) load() {
	b, err := os.ReadFile(s.file())
	if err == nil {
		_ = json.Unmarshal(b, s)
	}
	if s.Events == nil {
		s.Events = map[string]int64{}
	}
	if s.Since == 0 {
		s.Since = time.Now().Unix()
	}
}

// save keeps the counts in the plugin's data folder, which stays when a new version is installed.
func (s *stats) save() error {
	s.mu.Lock()
	b, err := json.MarshalIndent(s, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := s.file() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.file())
}

func (s *stats) heard(e event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Events[e.Kind]++
	s.Latest = append([]event{e}, s.Latest...)
	if len(s.Latest) > 20 {
		s.Latest = s.Latest[:20]
	}
}

// summary is the MCP tool's answer: the counts in plain words.
func (s *stats) summary(latest int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	kinds := make([]string, 0, len(s.Events))
	var total int64
	for k, n := range s.Events {
		kinds = append(kinds, k)
		total += n
	}
	sort.Slice(kinds, func(i, j int) bool { return s.Events[kinds[i]] > s.Events[kinds[j]] })
	var b strings.Builder
	fmt.Fprintf(&b, "Since %s the panel recorded %d events.\n", time.Unix(s.Since, 0).UTC().Format("2006-01-02 15:04 UTC"), total)
	for _, k := range kinds {
		fmt.Fprintf(&b, "- %s: %d\n", k, s.Events[k])
	}
	if s.CountedAt > 0 {
		servers := fmt.Sprintf("%d servers", s.Servers)
		if s.Servers == 1 {
			servers = "1 server"
		}
		fmt.Fprintf(&b, "At the last count (%s) the panel had %s.\n", time.Unix(s.CountedAt, 0).UTC().Format("15:04 UTC"), servers)
	}
	fmt.Fprintf(&b, "Subscriptions with stars: %d.\n", s.Starred)
	for i, e := range s.Latest {
		if i >= latest {
			break
		}
		if i == 0 {
			b.WriteString("The latest events:\n")
		}
		fmt.Fprintf(&b, "- %s %s\n", time.Unix(e.TS, 0).UTC().Format("2006-01-02 15:04"), e.Message)
	}
	return b.String()
}

// ---------------------------------------------------------------- the plugin

func main() {
	c := &conn{pending: map[string]chan message{}}
	s := &stats{}
	s.load()

	// register first: the panel ends a program that has not registered within ten seconds
	go func() {
		res, err := c.call("register", map[string]any{
			"hooks":  []string{"event", "filter.subscription"},
			"routes": []map[string]string{{"method": "GET", "path": "/stats"}},
			"tools": []map[string]any{{
				"name":        "summary",
				"title":       "Hello summary",
				"description": "What the Hello plugin counted: the panel's events by kind, the latest ones, and how many servers the panel had at the last count.",
				"input_schema": map[string]any{"type": "object", "properties": map[string]any{
					"latest": map[string]any{"type": "integer", "description": "How many of the latest events to list (default 5, at most 20)"},
				}},
			}},
			"schedules": []map[string]any{{"name": "count-servers", "every": 300}},
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "the panel refused to register us:", err)
			os.Exit(1)
		}
		var welcome struct {
			Version string `json:"version"`
		}
		_ = json.Unmarshal(res, &welcome)
		c.log("info", "Hello is running, with Rosélune "+welcome.Version)
		count(c, s) // the first count now, then every five minutes
	}()

	// messages can be large (a whole subscription), so the reader's buffer grows as needed
	in := bufio.NewReaderSize(os.Stdin, 64<<10)
	for {
		line, err := in.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var m message
			if jerr := json.Unmarshal(line, &m); jerr != nil {
				fmt.Fprintln(os.Stderr, "the panel sent something that is not JSON:", jerr)
			} else if m.Method == "" {
				c.answered(m) // an answer to one of our calls
			} else if m.Method == "event" {
				var e event
				if json.Unmarshal(m.Params, &e) == nil {
					s.heard(e)
				}
			} else {
				go handle(c, s, m) // each call on its own, so a slow one holds up nothing else
			}
		}
		if err != nil {
			_ = s.save()
			return // the panel closed our input: it wants us to stop
		}
	}
}

func handle(c *conn, s *stats, m message) {
	switch m.Method {
	case "filter.subscription":
		var p struct {
			Format string `json:"format"`
			UserID int64  `json:"user_id"`
			Body   string `json:"body"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			c.fail(m.ID, "unexpected parameters")
			return
		}
		out := star(p.Format, p.Body)
		if out != p.Body {
			s.mu.Lock()
			s.Starred++
			s.mu.Unlock()
		}
		c.reply(m.ID, out) // the answer is the new body, as a string
	case "route":
		var r struct {
			Method string `json:"method"`
			Path   string `json:"path"`
		}
		_ = json.Unmarshal(m.Params, &r)
		if r.Path != "/stats" {
			c.reply(m.ID, map[string]any{"status": 404, "body": map[string]string{"error": "not found"}})
			return
		}
		s.mu.Lock()
		b, _ := json.Marshal(s)
		s.mu.Unlock()
		c.reply(m.ID, map[string]any{"status": 200, "headers": map[string]string{"Cache-Control": "no-store"}, "body": json.RawMessage(b)})
	case "mcp":
		var p struct {
			Tool string `json:"tool"`
			Args struct {
				Latest *int `json:"latest"`
			} `json:"args"`
		}
		_ = json.Unmarshal(m.Params, &p)
		if p.Tool != "summary" {
			c.fail(m.ID, "there is no tool "+p.Tool)
			return
		}
		latest := 5
		if p.Args.Latest != nil {
			latest = min(max(*p.Args.Latest, 0), 20)
		}
		c.reply(m.ID, s.summary(latest)) // a string reaches the assistant as text
	case "tick":
		count(c, s)
		if err := s.save(); err != nil {
			c.log("warn", "cannot keep the counts: "+err.Error())
		}
		c.reply(m.ID, map[string]any{})
	default:
		c.fail(m.ID, "unknown method "+m.Method)
	}
}

// count asks the panel's API how many servers there are.
func count(c *conn, s *stats) {
	res, err := c.call("api", map[string]any{"method": "GET", "path": "/api/servers"})
	if err != nil {
		c.log("warn", "cannot count the servers: "+err.Error())
		return
	}
	var r struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	}
	_ = json.Unmarshal(res, &r)
	var list []json.RawMessage
	if r.Status != 200 || json.Unmarshal(r.Body, &list) != nil {
		c.log("warn", fmt.Sprintf("the panel's API answered %d", r.Status))
		return
	}
	s.mu.Lock()
	s.Servers, s.CountedAt = len(list), time.Now().Unix()
	s.mu.Unlock()
}

// ---------------------------------------------------------------- stars in subscriptions

const mark = "★ "

// star puts a star before the name of every entry in a subscription. The panel sends the body as
// the app will receive it, in the app's format: this handles share links (the uri, base64,
// shadowrocket and hiddify formats), Clash and Stash profiles and sing-box profiles, and sends the
// other formats back as they came.
func star(format, body string) string {
	switch format {
	case "uri":
		return starLinks(body)
	case "base64", "shadowrocket", "hiddify":
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body))
		if err != nil {
			return body
		}
		return base64.StdEncoding.EncodeToString([]byte(starLinks(string(raw))))
	case "clash", "stash":
		return starYAML(body)
	case "singbox":
		return starSingBox(body)
	}
	return body
}

// starLinks marks share links, one per line: a link's name is what follows its #. (vmess:// links
// carry their name inside and stay as they are.)
func starLinks(list string) string {
	lines := strings.Split(list, "\n")
	for i, l := range lines {
		link, name, ok := strings.Cut(l, "#")
		if !ok || !strings.Contains(link, "://") {
			continue // Shadowrocket's usage line, for one
		}
		n, err := url.PathUnescape(name)
		if err != nil || strings.HasPrefix(n, mark) {
			continue
		}
		lines[i] = link + "#" + url.PathEscape(mark+n)
	}
	return strings.Join(lines, "\n")
}

// starYAML marks a Clash or Stash profile: each proxy's name where it is defined and wherever a
// group lists it. The panel writes a name the same way each time, so the text can be matched.
func starYAML(doc string) string {
	lines := strings.Split(doc, "\n")
	names := map[string]string{} // a proxy's name as written -> marked
	section := ""
	for _, l := range lines {
		if l != "" && l[0] != ' ' && l[0] != '#' {
			section = l // a top-level key, like "proxies:"
		}
		if v, ok := strings.CutPrefix(l, "  - name: "); ok && section == "proxies:" {
			if strings.HasPrefix(v, `"`) || strings.HasPrefix(v, `'`) {
				names[v] = v[:1] + mark + v[1:] // inside the quotes
			} else {
				names[v] = mark + v
			}
		}
	}
	for i, l := range lines {
		item := strings.TrimLeft(l, " ")
		indent := l[:len(l)-len(item)]
		for _, prefix := range []string{"- name: ", "- "} {
			if v, ok := strings.CutPrefix(item, prefix); ok {
				if marked, ok := names[v]; ok {
					lines[i] = indent + prefix + marked
				}
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}

// starSingBox marks a sing-box profile: the tags of its proxies and the groups' lists of them.
func starSingBox(doc string) string {
	d := json.NewDecoder(strings.NewReader(doc))
	d.UseNumber() // numbers stay exactly as they were
	var cfg map[string]any
	if d.Decode(&cfg) != nil {
		return doc
	}
	renamed := map[string]string{}
	var all []map[string]any
	for _, key := range []string{"outbounds", "endpoints"} { // WireGuard is an endpoint
		list, _ := cfg[key].([]any)
		for _, x := range list {
			o, ok := x.(map[string]any)
			if !ok {
				continue
			}
			all = append(all, o)
			switch o["type"] {
			case "selector", "urltest", "direct", "block", "dns":
				continue // groups and built-in ways out keep their names
			}
			if tag, ok := o["tag"].(string); ok {
				renamed[tag] = mark + tag
				o["tag"] = mark + tag
			}
		}
	}
	for _, o := range all {
		if list, ok := o["outbounds"].([]any); ok {
			for i, x := range list {
				if s, ok := x.(string); ok && renamed[s] != "" {
					list[i] = renamed[s]
				}
			}
		}
	}
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	e.SetIndent("", "  ")
	if e.Encode(cfg) != nil {
		return doc
	}
	return b.String()
}
