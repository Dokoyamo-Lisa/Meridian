// A plugin's program for the panel's tests (plugins_test.go builds it): it speaks the plugin protocol
// and does what its arguments say, so the tests see every part of the protocol at work.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	mode   = flag.String("mode", "normal", "normal | crash | silent | noise")
	hooks  = flag.String("hooks", "", "hooks to register, comma-separated")
	routes = flag.Bool("routes", false, "register routes")
	pages  = flag.Bool("pages", false, "register a public page")
	tools  = flag.Bool("tools", false, "register MCP tools")
	every  = flag.Int("every", 0, "a timer every so many seconds")
)

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type msg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

var (
	outMu    sync.Mutex
	nextID   atomic.Int64
	pendMu   sync.Mutex
	pending  = map[int64]chan msg{}
	events   atomic.Int64
	ticks    atomic.Int64
	lastKind atomic.Value
)

func write(v any) {
	b, _ := json.Marshal(v)
	outMu.Lock()
	os.Stdout.Write(append(b, '\n'))
	outMu.Unlock()
}

func call(method string, params any) (json.RawMessage, error) {
	id := nextID.Add(1)
	ch := make(chan msg, 1)
	pendMu.Lock()
	pending[id] = ch
	pendMu.Unlock()
	write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	select {
	case m := <-ch:
		if m.Error != nil {
			return nil, errors.New(m.Error.Message)
		}
		return m.Result, nil
	case <-time.After(20 * time.Second):
		return nil, errors.New("no answer")
	}
}

func reply(id json.RawMessage, result any) {
	write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func fail(id json.RawMessage, text string) {
	write(map[string]any{"jsonrpc": "2.0", "id": id, "error": rpcErr{-32000, text}})
}

func main() {
	flag.Parse()
	switch *mode {
	case "crash":
		fmt.Fprintln(os.Stderr, "boom: the helper stops on purpose")
		os.Exit(3)
	case "silent":
		time.Sleep(time.Hour)
		return
	case "noise":
		fmt.Println("this is not JSON")
	}
	go register()
	in := bufio.NewReaderSize(os.Stdin, 1<<20)
	for {
		line, err := in.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var m msg
			if json.Unmarshal(line, &m) == nil {
				if m.Method == "" {
					id, _ := strconv.ParseInt(string(m.ID), 10, 64)
					pendMu.Lock()
					ch := pending[id]
					delete(pending, id)
					pendMu.Unlock()
					if ch != nil {
						ch <- m
					}
				} else {
					go handle(m)
				}
			}
		}
		if err != nil {
			return // the panel closed our input: stop
		}
	}
}

func register() {
	reg := map[string]any{}
	if *hooks != "" {
		reg["hooks"] = strings.Split(*hooks, ",")
	}
	if *routes {
		reg["routes"] = []map[string]string{{"method": "GET", "path": "/hello"}, {"method": "POST", "path": "/echo"},
			{"method": "GET", "path": "/slow"}, {"path": "/call"}, {"path": "/notify"}, {"method": "GET", "path": "/env"}}
	}
	if *pages {
		reg["pages"] = []map[string]string{{"method": "GET", "path": "/"}}
	}
	if *tools {
		reg["tools"] = []map[string]any{
			{"name": "count_events", "title": "Count events", "description": "How many events the helper heard",
				"input_schema": map[string]any{"type": "object", "properties": map[string]any{"verbose": map[string]any{"type": "boolean"}}}},
			{"name": "wipe", "description": "Pretends to wipe something", "destructive": true},
		}
	}
	if *every > 0 {
		reg["schedules"] = []map[string]any{{"name": "beat", "every": *every}}
	}
	res, err := call("register", reg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "register:", err)
		os.Exit(4)
	}
	fmt.Fprintln(os.Stderr, "registered:", string(res))
}

func handle(m msg) {
	switch m.Method {
	case "event":
		var e struct {
			Kind string `json:"kind"`
		}
		_ = json.Unmarshal(m.Params, &e)
		events.Add(1)
		lastKind.Store(e.Kind)
	case "tick":
		ticks.Add(1)
		reply(m.ID, map[string]any{})
	case "filter.subscription":
		var p struct {
			Body string `json:"body"`
		}
		_ = json.Unmarshal(m.Params, &p)
		if strings.Contains(p.Body, "Slowpoke") {
			time.Sleep(3 * time.Second)
		}
		reply(m.ID, strings.ReplaceAll(p.Body, "Tokyo", "Tokyo (plugin)"))
	case "filter.compile":
		var p struct {
			State map[string]any `json:"state"`
		}
		_ = json.Unmarshal(m.Params, &p)
		st := p.State
		ips, _ := st["blocked_ips"].([]any)
		st["blocked_ips"] = append(ips, "198.51.100.77")
		// what the panel keeps for itself: these must not get through
		st["contract"] = 999
		st["cores"] = map[string]any{"xray": "evil"}
		st["actions"] = []any{map[string]any{"id": 7, "kind": "stop_service"}}
		reply(m.ID, st)
	case "filter.status":
		var p struct {
			Data map[string]any `json:"data"`
		}
		_ = json.Unmarshal(m.Params, &p)
		p.Data["title"] = fmt.Sprint(p.Data["title"]) + " (plugin)"
		reply(m.ID, p.Data)
	case "filter.notify":
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(m.Params, &p)
		if strings.Contains(p.Text, "hush") {
			reply(m.ID, "")
			return
		}
		reply(m.ID, p.Text+" [via plugin]")
	case "route":
		route(m)
	case "mcp":
		var p struct {
			Tool  string         `json:"tool"`
			Args  map[string]any `json:"args"`
			Scope string         `json:"scope"`
		}
		_ = json.Unmarshal(m.Params, &p)
		switch p.Tool {
		case "count_events":
			reply(m.ID, map[string]any{"events": events.Load(), "scope": p.Scope, "args": p.Args})
		case "wipe":
			reply(m.ID, "wiped")
		default:
			fail(m.ID, "no such tool")
		}
	default:
		if len(m.ID) > 0 {
			fail(m.ID, "unknown method")
		}
	}
}

func route(m msg) {
	var r struct {
		Method  string            `json:"method"`
		Path    string            `json:"path"`
		Query   string            `json:"query"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
		Public  bool              `json:"public"`
		Caller  map[string]string `json:"caller"`
	}
	_ = json.Unmarshal(m.Params, &r)
	q, _ := url.ParseQuery(r.Query)
	switch r.Path {
	case "/hello":
		k, _ := lastKind.Load().(string)
		reply(m.ID, map[string]any{"status": 200,
			"headers": map[string]string{"Set-Cookie": "mrd_s=stolen", "X-Evil": "1", "Cache-Control": "max-age=5"},
			"body":    map[string]any{"hello": "world", "events": events.Load(), "ticks": ticks.Load(), "last": k, "caller": r.Caller}})
	case "/echo":
		reply(m.ID, map[string]any{"status": 201, "body": map[string]any{"method": r.Method, "path": r.Path, "query": r.Query,
			"headers": r.Headers, "body": r.Body, "public": r.Public}})
	case "/slow":
		time.Sleep(3 * time.Second)
		reply(m.ID, map[string]any{"status": 200, "body": "late"})
	case "/call":
		var body any
		if b := q.Get("body"); b != "" {
			_ = json.Unmarshal([]byte(b), &body)
		}
		res, err := call("api", map[string]any{"method": q.Get("method"), "path": q.Get("path"), "body": body})
		if err != nil {
			reply(m.ID, map[string]any{"status": 200, "body": map[string]any{"error": err.Error()}})
			return
		}
		reply(m.ID, map[string]any{"status": 200, "body": json.RawMessage(res)})
	case "/notify":
		res, err := call("notify", map[string]any{"text": q.Get("text")})
		if err != nil {
			reply(m.ID, map[string]any{"status": 200, "body": map[string]any{"error": err.Error()}})
			return
		}
		reply(m.ID, map[string]any{"status": 200, "body": json.RawMessage(res)})
	case "/env":
		wd, _ := os.Getwd()
		reply(m.ID, map[string]any{"status": 200, "body": map[string]any{"env": os.Environ(), "wd": wd}})
	case "/":
		reply(m.ID, map[string]any{"status": 200, "headers": map[string]string{"Content-Type": "text/html; charset=utf-8"},
			"body": "<h1>public page</h1>"})
	default:
		reply(m.ID, map[string]any{"status": 404, "body": "no"})
	}
}
