package panel

// A server plugin's program. The panel starts it with the plugin's folder as its working directory,
// a small environment without any of the panel's secrets and a process group of its own, and speaks
// JSON-RPC 2.0 with it: one JSON object per line on its standard input and output. What it writes to
// standard error goes to the panel's log (a limited amount) and to the plugin's log on the plugins
// page. Every call to it has a time limit and everything it is sent waits in bounded queues, so a slow
// or stuck program can never hold up the panel, the agents or subscriptions.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const pluginMsgMax = 16 << 20 // one message, either way

// pluginHooks are what a program can ask to be called for, with the permission each needs.
var pluginHooks = map[string]string{
	"event":               "events",
	"filter.compile":      "filter:compile",
	"filter.subscription": "filter:subscription",
	"filter.status":       "filter:status",
	"filter.notify":       "filter:notify",
}

type pluginProc struct {
	h       *pluginHost
	id      string
	name    string
	dir     string // its files, and its working directory
	dataDir string // its own data, kept across new versions
	man     *pluginManifest
	perms   map[string]bool
	log     *pluginLog

	out     chan []byte   // calls and answers, written in order
	evq     chan []byte   // events, written when there is room
	quit    chan struct{} // closed by stop
	done    chan struct{} // closed once the program has exited
	regd    chan struct{} // closed once it registered
	started time.Time

	stopOnce sync.Once
	stopping atomic.Bool
	nextID   atomic.Int64
	reg      atomic.Pointer[pluginReg]
	calls    chan struct{} // its own calls to the panel running at once
	dropped  atomic.Int64  // events it was too slow to take
	evFrom   atomic.Int64  // the last event handed to it (or recorded before it registered)

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   *os.File
	pending map[int64]chan pluginMsg
	skip    map[string]time.Time // hooks left out after a failure, until then
	ticking map[string]bool      // timers whose call has not come back yet
	due     map[string]time.Time // when each timer fires next
	why     string               // why the panel ended it, if it did
	last    string               // the last line it wrote to standard error
	noise   bool                 // it wrote something that is not JSON-RPC (reported once)
}

// pluginMsg is a JSON-RPC message in either direction.
type pluginMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// pluginOut is a message the panel writes.
type pluginOut struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Method  string    `json:"method,omitempty"`
	Params  any       `json:"params,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

func newPluginProc(h *pluginHost, pl *plugin) *pluginProc {
	id := pl.row.ID
	pp := &pluginProc{h: h, id: id, name: pl.man.Name, dir: filepath.Join(h.dir, id), dataDir: filepath.Join(h.data, id), man: pl.man,
		perms: map[string]bool{}, log: pl.log, out: make(chan []byte, 256), evq: make(chan []byte, 512), quit: make(chan struct{}),
		done: make(chan struct{}), regd: make(chan struct{}), calls: make(chan struct{}, 8), pending: map[int64]chan pluginMsg{},
		skip: map[string]time.Time{}, ticking: map[string]bool{}, due: map[string]time.Time{}, started: time.Now()}
	for _, x := range pl.man.Permissions {
		pp.perms[x] = true
	}
	return pp
}

// env is all the program gets from its surroundings: no variable of the panel's own.
func (pp *pluginProc) env() []string {
	env := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=" + pp.dataDir,
		"LANG=C.UTF-8",
		"MERIDIAN_PLUGIN_ID=" + pp.id,
		"MERIDIAN_PLUGIN_DATA=" + pp.dataDir,
		"MERIDIAN_VERSION=" + Version,
	}
	if tz := os.Getenv("TZ"); tz != "" {
		env = append(env, "TZ="+tz)
	}
	return env
}

// start runs the program.
func (pp *pluginProc) start() error {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	if pp.stopping.Load() {
		close(pp.done)
		return errors.New("it was stopped")
	}
	// the program is a regular file in the plugin's own folder - never a link to somewhere else
	cmdPath := filepath.Join(pp.dir, filepath.FromSlash(pp.man.Server.Command))
	real, err := filepath.EvalSymlinks(cmdPath)
	base, berr := filepath.EvalSymlinks(pp.dir)
	st, serr := os.Lstat(cmdPath)
	if err != nil || berr != nil || serr != nil || !st.Mode().IsRegular() || !strings.HasPrefix(real, base+string(filepath.Separator)) {
		close(pp.done)
		return fmt.Errorf("%s is not a program in the plugin's folder", pp.man.Server.Command)
	}
	if err := os.MkdirAll(pp.dataDir, 0o700); err != nil {
		close(pp.done)
		return err
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		close(pp.done)
		return err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		close(pp.done)
		return err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		close(pp.done)
		return err
	}
	cmd := exec.Command(cmdPath, pp.man.Server.Args...)
	cmd.Dir = pp.dir
	cmd.Env = pp.env()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	cmd.SysProcAttr = pluginProcAttr()
	err = cmd.Start()
	inR.Close()
	outW.Close()
	errW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		errR.Close()
		close(pp.done)
		return err
	}
	pp.cmd, pp.stdin = cmd, inW
	errDone := make(chan struct{})
	go pp.readLoop(outR)
	go pp.stderrLoop(errR, errDone)
	go pp.writeLoop()
	go pp.wait(cmd, outR, errR, errDone)
	go pp.awaitRegister()
	return nil
}

// stop ends the program: its input closes, it gets SIGTERM, and SIGKILL if it is still there after
// a few seconds - with everything it started (its process group).
func (pp *pluginProc) stop() {
	pp.stopOnce.Do(func() {
		pp.stopping.Store(true)
		close(pp.quit)
		pp.mu.Lock()
		cmd, in := pp.cmd, pp.stdin
		pp.mu.Unlock()
		if cmd == nil {
			return // it never started
		}
		_ = in.Close()
		signalGroup(cmd.Process, syscall.SIGTERM)
		t := time.NewTimer(pp.h.stopWait)
		defer t.Stop()
		select {
		case <-pp.done:
			signalGroup(cmd.Process, syscall.SIGKILL) // anything it left behind
		case <-t.C:
			signalGroup(cmd.Process, syscall.SIGKILL)
			select {
			case <-pp.done:
			case <-time.After(2 * time.Second):
				slog.Warn("plugins: a program does not end", "plugin", pp.id)
			}
		}
	})
}

// ended says whether the program has exited.
func (pp *pluginProc) ended() bool {
	select {
	case <-pp.done:
		return true
	default:
		return false
	}
}

// signalGroup signals a program and everything it started.
func signalGroup(p *os.Process, sig syscall.Signal) {
	if p == nil {
		return
	}
	if err := syscall.Kill(-p.Pid, sig); err != nil {
		_ = p.Signal(sig)
	}
}

// wait notices the program's end.
func (pp *pluginProc) wait(cmd *exec.Cmd, outR, errR *os.File, errDone chan struct{}) {
	err := cmd.Wait()
	close(pp.done)
	// what it wrote last is still read; then the pipes close (something it left running could hold
	// them open forever)
	select {
	case <-errDone:
	case <-time.After(500 * time.Millisecond):
	}
	time.AfterFunc(time.Second, func() {
		outR.Close()
		errR.Close()
	})
	if !pp.stopping.Load() {
		pp.h.exited(pp.id, pp, pp.exitWhy(err))
	}
	// servers whose configuration it filtered get it without the filter now
	if r := pp.reg.Load(); r != nil && r.has("filter.compile") && pp.h.p != nil {
		pp.h.p.touchAll()
	}
}

func (pp *pluginProc) exitWhy(err error) string {
	pp.mu.Lock()
	why, last := pp.why, pp.last
	pp.mu.Unlock()
	if why != "" {
		return why
	}
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		if st, ok := ee.Sys().(syscall.WaitStatus); ok && st.Signaled() {
			why = "its program was killed by " + st.Signal().String()
		} else {
			why = fmt.Sprintf("its program exited with status %d", ee.ExitCode())
		}
	case err != nil:
		why = "its program failed: " + err.Error()
	default:
		why = "its program exited"
	}
	if last != "" {
		why += " (it said: " + truncate(last, 200) + ")"
	}
	return why
}

// awaitRegister ends a program that does not register in time: without registering it is useless.
func (pp *pluginProc) awaitRegister() {
	t := time.NewTimer(pp.h.registerWait)
	defer t.Stop()
	select {
	case <-pp.regd:
	case <-pp.done:
	case <-pp.quit:
	case <-t.C:
		pp.mu.Lock()
		pp.why = fmt.Sprintf("its program did not register within %s - it must call register as soon as it starts", plainDur(pp.h.registerWait))
		cmd := pp.cmd
		pp.mu.Unlock()
		signalGroup(cmd.Process, syscall.SIGKILL)
	}
}

// ---------------------------------------------------------------- reading and writing

// readLine reads one line; of a line longer than max only the first max bytes are kept (long).
func readLine(br *bufio.Reader, max int) (line []byte, long bool, err error) {
	for {
		chunk, e := br.ReadSlice('\n')
		if !long {
			if len(line)+len(chunk) > max {
				line, long = append(line, chunk[:max-len(line)]...), true
			} else {
				line = append(line, chunk...)
			}
		}
		if e != bufio.ErrBufferFull {
			return line, long, e
		}
	}
}

func (pp *pluginProc) readLoop(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, long, err := readLine(br, pluginMsgMax)
		switch {
		case long:
			pp.log.write(pp.id, "the panel: a message from its program was larger than 16 MB and was dropped")
		case len(bytes.TrimSpace(line)) > 0:
			pp.handle(line)
		}
		if err != nil {
			return
		}
	}
}

func (pp *pluginProc) stderrLoop(r io.Reader, done chan struct{}) {
	defer close(done)
	br := bufio.NewReaderSize(r, 4096)
	for {
		line, _, err := readLine(br, 2000)
		if s := strings.TrimRight(string(line), "\r\n"); strings.TrimSpace(s) != "" {
			s = cleanNote(s, 2000)
			pp.mu.Lock()
			pp.last = s
			pp.mu.Unlock()
			pp.log.write(pp.id, s)
		}
		if err != nil {
			return
		}
	}
}

func (pp *pluginProc) writeLoop() {
	for {
		var b []byte
		select { // calls and answers first; events when nothing else waits
		case b = <-pp.out:
		default:
			select {
			case b = <-pp.out:
			case b = <-pp.evq:
			case <-pp.quit:
				return
			case <-pp.done:
				return
			}
		}
		pp.mu.Lock()
		in := pp.stdin
		pp.mu.Unlock()
		if _, err := in.Write(b); err != nil {
			return
		}
	}
}

// send queues a message (ending in a newline) without waiting: false when the queue is full.
func (pp *pluginProc) send(b []byte) bool {
	select {
	case pp.out <- b:
		return true
	default:
		return false
	}
}

// call asks the program something and waits for its answer, at most wait.
func (pp *pluginProc) call(ctx context.Context, method string, params any, wait time.Duration) (json.RawMessage, error) {
	id := pp.nextID.Add(1)
	b, err := json.Marshal(pluginOut{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	if len(b) > pluginMsgMax {
		return nil, errors.New("what it would be sent is larger than 16 MB")
	}
	ch := make(chan pluginMsg, 1)
	pp.mu.Lock()
	pp.pending[id] = ch
	pp.mu.Unlock()
	defer func() {
		pp.mu.Lock()
		delete(pp.pending, id)
		pp.mu.Unlock()
	}()
	if !pp.send(append(b, '\n')) {
		return nil, errors.New("its program is not keeping up - too many messages wait for it")
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case m := <-ch:
		if m.Error != nil {
			return nil, errors.New("it answered with an error: " + cleanName(m.Error.Message, 300))
		}
		if len(m.Result) == 0 {
			return json.RawMessage("null"), nil
		}
		return m.Result, nil
	case <-t.C:
		return nil, fmt.Errorf("it did not answer within %s", plainDur(wait))
	case <-pp.done:
		return nil, errors.New("its program stopped")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// answer replies to one of the program's calls (a notification gets none).
func (pp *pluginProc) answer(id json.RawMessage, res any, e *rpcError) {
	if len(id) == 0 || string(id) == "null" {
		return
	}
	m := pluginOut{JSONRPC: "2.0", ID: id}
	if e != nil {
		m.Error = e
	} else if m.Result = res; res == nil {
		m.Result = struct{}{}
	}
	b, err := json.Marshal(m)
	if err != nil {
		b, _ = json.Marshal(pluginOut{JSONRPC: "2.0", ID: id, Error: &rpcError{-32603, "the answer could not be encoded"}})
	}
	select {
	case pp.out <- append(b, '\n'):
	case <-pp.done:
	case <-time.After(2 * time.Second): // it does not read: its own call times out
	}
}

// handle takes one message from the program.
func (pp *pluginProc) handle(line []byte) {
	var m pluginMsg
	if err := json.Unmarshal(line, &m); err != nil || m.JSONRPC != "2.0" {
		pp.mu.Lock()
		first := !pp.noise
		pp.noise = true
		pp.mu.Unlock()
		if first {
			pp.log.write(pp.id, "the panel: its program wrote something that is not a JSON-RPC 2.0 message (one JSON object per line) - write anything else to standard error: "+
				truncate(string(bytes.TrimSpace(line)), 120))
		}
		return
	}
	if m.Method == "" { // an answer to one of the panel's calls
		id, err := strconv.ParseInt(string(m.ID), 10, 64)
		if err != nil {
			return
		}
		pp.mu.Lock()
		ch := pp.pending[id]
		pp.mu.Unlock()
		if ch != nil {
			select {
			case ch <- m:
			default:
			}
		}
		return
	}
	switch m.Method {
	case "register":
		res, e := pp.register(m.Params)
		pp.answer(m.ID, res, e)
	case "log":
		var in struct {
			Level   string `json:"level"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(m.Params, &in); err != nil || strings.TrimSpace(in.Message) == "" {
			pp.answer(m.ID, nil, &rpcError{-32602, "log needs a message"})
			return
		}
		lv := ""
		if in.Level == "warn" || in.Level == "error" {
			lv = in.Level + ": "
		}
		pp.log.write(pp.id, lv+cleanNote(in.Message, 2000))
		pp.answer(m.ID, nil, nil)
	case "api", "notify":
		select {
		case pp.calls <- struct{}{}:
			go func() {
				defer func() { <-pp.calls }()
				var res any
				var e *rpcError
				if m.Method == "api" {
					res, e = pp.apiCall(m.Params)
				} else {
					res, e = pp.notifyCall(m.Params)
				}
				pp.answer(m.ID, res, e)
			}()
		default:
			pp.answer(m.ID, nil, &rpcError{-32000, "too many calls at once - wait for answers before sending more"})
		}
	default:
		pp.answer(m.ID, nil, &rpcError{-32601, "unknown method " + truncate(m.Method, 40) + " - the panel answers register, api, notify and log"})
	}
}

// ---------------------------------------------------------------- registering

// pluginReg is what a program registers when it starts.
type pluginReg struct {
	Hooks     []string         `json:"hooks"`
	Routes    []pluginRoute    `json:"routes"`
	Pages     []pluginRoute    `json:"pages"`
	Tools     []pluginToolSpec `json:"tools"`
	Schedules []pluginSchedule `json:"schedules"`
}

// pluginRoute is a path the plugin answers: exactly that path, or - ending in "/*" - a folder and
// everything under it ("/reports/*" answers /reports, /reports/ and /reports/2026/05; "/*" answers
// every path).
type pluginRoute struct {
	Method string `json:"method"` // GET, POST, PUT, PATCH or DELETE; empty or * for any
	Path   string `json:"path"`
}

type pluginToolSpec struct {
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
	Write       bool           `json:"write"`
	Destructive bool           `json:"destructive"`
}

type pluginSchedule struct {
	Name  string `json:"name"`
	Every int    `json:"every"` // seconds
}

// pluginWelcome answers register.
type pluginWelcome struct {
	ID          string   `json:"id"`
	Version     string   `json:"version"` // the panel's
	DataDir     string   `json:"data_dir"`
	Permissions []string `json:"permissions"`
	PublicURL   string   `json:"public_url"`
}

var (
	// plain path segments separated by single slashes, optionally ending in a slash or in "/*"
	pluginRoutePathRE = regexp.MustCompile(`^/(?:[A-Za-z0-9._~!$&'()+,;=:@%-]+/)*(?:[A-Za-z0-9._~!$&'()+,;=:@%-]+|\*)?$`)
	pluginToolRE      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	pluginTimerRE     = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	pluginPropRE      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
)

func (pp *pluginProc) register(raw json.RawMessage) (any, *rpcError) {
	if pp.reg.Load() != nil {
		return nil, &rpcError{-32000, "it registered already - register once, when the program starts"}
	}
	var r pluginReg
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) > 0 {
		if err := d.Decode(&r); err != nil {
			return nil, &rpcError{-32602, "register: " + cleanName(err.Error(), 200)}
		}
	}
	if err := r.check(pp.man, pp.perms, pp.h.minEvery); err != nil {
		pp.log.write(pp.id, "the panel: register was refused: "+err.Error())
		return nil, &rpcError{-32602, "register: " + err.Error()}
	}
	pp.mu.Lock()
	for _, s := range r.Schedules {
		pp.due[s.Name] = time.Now().Add(time.Duration(s.Every) * time.Second)
	}
	pp.mu.Unlock()
	if r.has("event") { // it hears what happens from now on
		var last int64
		_ = pp.h.db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&last)
		pp.evFrom.Store(last)
	}
	pp.reg.Store(&r)
	close(pp.regd)
	if r.has("filter.compile") && pp.h.p != nil {
		pp.h.p.touchAll() // servers get their configuration through the filter
	}
	perms := slices.Sorted(maps.Keys(pp.perms))
	pub := ""
	if pp.h.p != nil {
		pub = pp.h.p.settings().PublicURL
	}
	return pluginWelcome{ID: pp.id, Version: Version, DataDir: pp.dataDir, Permissions: perms, PublicURL: pub}, nil
}

func (r *pluginReg) check(m *pluginManifest, perms map[string]bool, minEvery int) error {
	seen := map[string]bool{}
	for _, hk := range r.Hooks {
		perm, ok := pluginHooks[hk]
		if !ok {
			return fmt.Errorf("unknown hook %q - the hooks are event, filter.compile, filter.subscription, filter.status and filter.notify", truncate(hk, 40))
		}
		if !perms[perm] {
			return fmt.Errorf("the hook %s needs the %s permission in plugin.json", hk, perm)
		}
		if seen[hk] {
			return fmt.Errorf("the hook %s is listed twice", hk)
		}
		seen[hk] = true
	}
	if (len(r.Routes) > 0 || len(r.Pages) > 0) && !perms["routes"] {
		return errors.New("routes and pages need the routes permission in plugin.json")
	}
	if len(r.Pages) > 0 && !m.Server.PublicPages {
		return errors.New(`public pages need "public_pages": true in plugin.json's server part`)
	}
	if len(r.Routes) > 50 || len(r.Pages) > 50 {
		return errors.New("at most 50 routes and 50 pages")
	}
	for _, rt := range append(append([]pluginRoute(nil), r.Routes...), r.Pages...) {
		if err := rt.check(); err != nil {
			return err
		}
	}
	if len(r.Tools) > 0 && !perms["mcp"] {
		return errors.New("tools need the mcp permission in plugin.json")
	}
	if len(r.Tools) > 20 {
		return errors.New("at most 20 tools")
	}
	names := map[string]bool{}
	for i := range r.Tools {
		t := &r.Tools[i]
		if !pluginToolRE.MatchString(t.Name) {
			return fmt.Errorf("the tool name %q must be lowercase letters, digits and underscores, starting with a letter (at most 32)", truncate(t.Name, 40))
		}
		if names[t.Name] {
			return fmt.Errorf("the tool %s is listed twice", t.Name)
		}
		names[t.Name] = true
		if t.Title = cleanName(t.Title, 80); t.Title == "" {
			t.Title = t.Name
		}
		if t.Description = cleanNote(t.Description, 1000); t.Description == "" {
			return fmt.Errorf("the tool %s needs a description: assistants choose tools by it", t.Name)
		}
		if t.Destructive {
			t.Write = true
		}
		if err := checkToolSchema(t.InputSchema); err != nil {
			return fmt.Errorf("the tool %s: %v", t.Name, err)
		}
	}
	if len(r.Schedules) > 0 && !perms["schedule"] {
		return errors.New("schedules need the schedule permission in plugin.json")
	}
	if len(r.Schedules) > 10 {
		return errors.New("at most 10 schedules")
	}
	timers := map[string]bool{}
	for _, s := range r.Schedules {
		if !pluginTimerRE.MatchString(s.Name) {
			return fmt.Errorf("the schedule name %q must be lowercase letters, digits, dashes and underscores (at most 32)", truncate(s.Name, 40))
		}
		if timers[s.Name] {
			return fmt.Errorf("the schedule %s is listed twice", s.Name)
		}
		timers[s.Name] = true
		if s.Every < minEvery || s.Every > 7*86400 {
			return fmt.Errorf("the schedule %s: every must be between %d seconds and 7 days (604800 seconds)", s.Name, minEvery)
		}
	}
	return nil
}

func (rt *pluginRoute) check() error {
	rt.Method = strings.ToUpper(rt.Method)
	switch rt.Method {
	case "", "*", "GET", "POST", "PUT", "PATCH", "DELETE":
	default:
		return fmt.Errorf("the route method %q must be GET, POST, PUT, PATCH, DELETE or * (any)", truncate(rt.Method, 20))
	}
	parts := strings.Split(rt.Path, "/")
	if len(rt.Path) > 200 || !pluginRoutePathRE.MatchString(rt.Path) || slices.Contains(parts, "..") || slices.Contains(parts, ".") {
		return fmt.Errorf("the route path %q must start with / and hold only plain path characters (end it in /* for a folder and everything under it)",
			truncate(rt.Path, 60))
	}
	return nil
}

// matches says whether the route answers a request (path is clean and starts with /).
func (rt pluginRoute) matches(method, path string) bool {
	if rt.Method != "" && rt.Method != "*" && rt.Method != method && !(rt.Method == "GET" && method == "HEAD") {
		return false
	}
	if dir, ok := strings.CutSuffix(rt.Path, "*"); ok { // "/reports/": the folder and everything under it
		return strings.HasPrefix(path, dir) || path == strings.TrimSuffix(dir, "/")
	}
	return path == rt.Path
}

// checkToolSchema accepts the JSON schema of a tool's arguments: an object with named properties.
func checkToolSchema(s map[string]any) error {
	if s == nil {
		return nil
	}
	if b, _ := json.Marshal(s); len(b) > 16<<10 {
		return errors.New("its input_schema is larger than 16 KB")
	}
	if t, ok := s["type"]; ok && t != "object" {
		return errors.New(`its input_schema must be of type "object"`)
	}
	props, _ := s["properties"].(map[string]any)
	if s["properties"] != nil && props == nil {
		return errors.New("its input_schema's properties must be an object")
	}
	for name, v := range props {
		if !pluginPropRE.MatchString(name) {
			return fmt.Errorf("the argument name %q must be letters, digits and underscores", truncate(name, 40))
		}
		if name == "confirm" {
			return errors.New("the argument confirm is the panel's own (it asks for it on destructive tools)")
		}
		if _, ok := v.(map[string]any); !ok {
			return fmt.Errorf("the argument %s must be described by an object", name)
		}
	}
	if req, ok := s["required"]; ok {
		list, ok := req.([]any)
		if !ok {
			return errors.New("its input_schema's required must be a list of argument names")
		}
		for _, x := range list {
			n, ok := x.(string)
			if !ok || props[n] == nil {
				return fmt.Errorf("required names %v, which is not one of its properties", x)
			}
		}
	}
	return nil
}

// view is what the program added, as the plugins page shows it.
func (r *pluginReg) view(id string, pp *pluginProc) *pluginRunView {
	v := &pluginRunView{Since: pp.started.Unix(), Hooks: append([]string{}, r.Hooks...), Routes: []string{}, Pages: []string{},
		Tools: []string{}, Schedules: []string{}, Dropped: pp.dropped.Load()}
	for _, rt := range r.Routes {
		v.Routes = append(v.Routes, strings.TrimSpace(nz(strings.Trim(rt.Method, "*"), "any")+" /api/plugins/"+id+rt.Path))
	}
	for _, rt := range r.Pages {
		v.Pages = append(v.Pages, strings.TrimSpace(nz(strings.Trim(rt.Method, "*"), "any")+" /p/"+id+rt.Path))
	}
	for _, t := range r.Tools {
		v.Tools = append(v.Tools, pluginToolName(id, t.Name))
	}
	for _, s := range r.Schedules {
		v.Schedules = append(v.Schedules, fmt.Sprintf("%s every %s", s.Name, plainDur(time.Duration(s.Every)*time.Second)))
	}
	return v
}

func (r *pluginReg) has(hook string) bool { return slices.Contains(r.Hooks, hook) }

// ---------------------------------------------------------------- the plugin's log

type pluginLogLine struct {
	T    int64  `json:"t"`
	Text string `json:"text"`
}

// pluginLog keeps the last lines a plugin's program wrote, for the plugins page, and passes them on
// to the panel's log - at most 60 a minute, so a chatty program cannot flood it.
type pluginLog struct {
	mu     sync.Mutex
	lines  []pluginLogLine
	minute int64
	sent   int
	held   int
}

func (l *pluginLog) write(id, text string) {
	l.mu.Lock()
	l.lines = append(l.lines, pluginLogLine{T: now(), Text: text})
	if len(l.lines) > 300 {
		l.lines = append([]pluginLogLine(nil), l.lines[len(l.lines)-300:]...)
	}
	if m := time.Now().Unix() / 60; m != l.minute {
		if l.held > 0 {
			slog.Warn("plugin", "plugin", id, "lines held back", l.held)
		}
		l.minute, l.sent, l.held = m, 0, 0
	}
	pass := l.sent < 60
	if pass {
		l.sent++
	} else {
		l.held++
	}
	l.mu.Unlock()
	if pass {
		slog.Info("plugin", "plugin", id, "says", text)
	}
}

func (l *pluginLog) all() []pluginLogLine {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]pluginLogLine{}, l.lines...)
}
