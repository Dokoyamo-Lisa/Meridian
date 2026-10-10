// Package panel is the Meridian control plane: accounts, servers, protocols, subscriptions,
// monitoring, the agent API and the subscription endpoint.
package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"meridian/internal/db"
	"meridian/internal/geo"
	"meridian/internal/proto"
)

// Version is set at build time.
var Version = "dev"

type Config struct {
	Listen         string
	DataDir        string
	TrustedProxies []netip.Prefix // reverse proxies whose X-Forwarded-For / X-Real-IP we believe
	WebFS          fs.FS          // built UI (nil = API only)
	AgentDir       string         // directory with meridian-agent-linux-{amd64,arm64}
	NoPlugins      bool           // start without any plugin (--no-plugins): the way back when one breaks the panel
}

type Panel struct {
	cfg  Config
	db   *db.DB
	hub  *hub
	live *liveStore
	geo  *geo.DB

	setMu sync.RWMutex
	set   Settings

	limiter  *rateLimiter
	notify   *notifier  // notifications outside the panel (Telegram, webhook)
	upd      *updater   // installing a new release
	geoMu    sync.Mutex // the country rules last sent (rememberGeo)
	nonces   *nonceCache
	mirrorMu sync.Mutex
	mux      *http.ServeMux // the routes, for MCP tools that call the API in-process
	routes   []string       // registered patterns (tests check them against the API docs)

	digests   *digestCache
	agentSums agentSums
	acc       accessState
	status    statusCache // the public status page's data, for a few seconds
	brand     brandStore  // an uploaded logo, if any

	routeMu    sync.Mutex
	routeNotes map[int64][]string // per server: traffic rules that cannot be used there as they stand

	relays   relayCache        // servers that keep losing the panel (relay.go)
	bot      botState          // the Telegram bot (telegram.go)
	plugins  *pluginHost       // the operator's plugins (plugins.go)
	devices  deviceState       // devices turned away over a device limit (limits.go)
	signin   signinGuard       // addresses and usernames that keep failing to sign in (guard.go)
	passkeys passkeyCeremonies // passkey challenges handed out and not yet answered (passkeys.go)
	css      cssCache          // the operator's own style sheets (customcss.go)

	backupMu    sync.Mutex  // one backup at a time (backups.go)
	console     consoleHub  // the supervisor's consoles (console.go)
	sourceMu    sync.Mutex  // one subscription link refresh at a time (extsources.go)
	sourcesBusy atomic.Bool // the scheduled refreshes are running
	dyn         dynState    // dynamic DNS names, Cloudflare and the panel's own address (ddns.go)
	tg          tgState     // codes that link Telegram accounts (tglink.go)
}

func New(cfg Config, d *db.DB) (*Panel, error) {
	p := &Panel{cfg: cfg, db: d, hub: newHub(), live: newLiveStore(), limiter: newRateLimiter(), notify: newNotifier(), upd: &updater{},
		nonces: newNonceCache(), digests: newDigestCache(),
		agentSums: agentSums{at: map[string]time.Time{}, sums: map[string]string{}}}
	if err := p.loadSettings(); err != nil {
		return nil, err
	}
	p.fillNodeUsageCycle()
	if err := p.loadBrand(); err != nil {
		return nil, err
	}
	p.geo = geo.Open(cfg.DataDir)
	p.geo.OnLoad = p.touchAll // country rules held back for want of the database go out now
	p.plugins = newPluginHost(p, d, cfg.DataDir, cfg.NoPlugins)
	return p, nil
}

// Run starts background work and blocks until ctx ends.
func (p *Panel) Run(ctx context.Context) {
	p.updateResult(ctx) // a new version: say so, and upgrade the agents when that was asked for
	go p.plugins.run(ctx)
	p.plugins.ready(ctx) // plugins that filter what servers run come first (plugins_hooks.go)
	if err := p.compileAll(ctx); err != nil {
		slog.Error("initial compile", "err", err)
	}
	go p.compileLoop(ctx)
	go p.jobs(ctx)
	go p.maintainDigests(ctx)
	go p.geo.Maintain(ctx)
	go p.telegramBot(ctx)
	go p.dynLoop(ctx)
	go p.backupLoop(ctx)
	<-ctx.Done()
}

// ---------------------------------------------------------------- settings

func (p *Panel) settings() Settings {
	p.setMu.RLock()
	defer p.setMu.RUnlock()
	return p.set
}

func (p *Panel) loadSettings() error {
	var raw string
	err := p.db.QueryRow(`SELECT value FROM settings WHERE key = 'panel'`).Scan(&raw)
	s := defaultSettings()
	adopted := ""
	if err == nil {
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return err
		}
		adopted = adoptIdentity([]byte(raw), &s) // settings from before Rosélune (identity.go)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	s.normalize()
	p.setMu.Lock()
	p.set = s
	p.setMu.Unlock()
	if err := p.saveSettings(s); err != nil { // always store the full, explicit set
		return err
	}
	if adopted != "" {
		p.event(0, "info", "settings", 0, 0, 0, adopted, nil)
	}
	return nil
}

func (p *Panel) saveSettings(s Settings) error {
	s.normalize()
	b, _ := json.Marshal(s)
	if _, err := p.db.Exec1(`INSERT INTO settings (key, value) VALUES ('panel', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, string(b)); err != nil {
		return err
	}
	p.setMu.Lock()
	p.set = s
	p.setMu.Unlock()
	p.status.forget() // what the status page shows may have changed
	return nil
}

// StatusDomain is the status page's own domain, if one is set (for the HTTPS certificate).
func (p *Panel) StatusDomain() string { return p.settings().StatusDomain }

// DefaultPublicURL sets the public URL when none is configured yet (used with --domain).
func (p *Panel) DefaultPublicURL(u string) error {
	s := p.settings()
	if s.PublicURL != "" {
		return nil
	}
	s.PublicURL = u
	return p.saveSettings(s)
}

// baseURL is the panel's public URL, falling back to the request's own host.
func (p *Panel) baseURL(r *http.Request) string {
	if u := p.settings().PublicURL; u != "" {
		return u
	}
	scheme := "http"
	if p.isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// isHTTPS says whether the browser reached us over TLS, directly or through a trusted proxy.
func (p *Panel) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return p.peerTrusted(r) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (p *Panel) subBase(r *http.Request) string {
	if u := p.settings().SubURL; u != "" {
		return u
	}
	return p.baseURL(r)
}

// ---------------------------------------------------------------- live state

// liveStore keeps each server's latest snapshot and a short rate history in memory.
type liveStore struct {
	mu sync.RWMutex
	m  map[int64]*liveServer
}

type liveServer struct {
	At    int64
	Live  proto.Live
	Rates []ratePoint
}

type ratePoint struct {
	T  int64 `json:"t"`
	RX int64 `json:"rx"`
	TX int64 `json:"tx"`
}

func newLiveStore() *liveStore { return &liveStore{m: map[int64]*liveServer{}} }

func (l *liveStore) put(id int64, lv proto.Live) {
	l.mu.Lock()
	defer l.mu.Unlock()
	ls := l.m[id]
	if ls == nil {
		ls = &liveServer{}
		l.m[id] = ls
	}
	ls.At = time.Now().Unix()
	ls.Live = lv
	ls.Rates = append(ls.Rates, ratePoint{T: ls.At, RX: lv.Sys.RXRate, TX: lv.Sys.TXRate})
	cut := ls.At - 1800
	i := 0
	for i < len(ls.Rates) && ls.Rates[i].T < cut {
		i++
	}
	ls.Rates = ls.Rates[i:]
}

// all is a copy of every server's latest snapshot.
func (l *liveStore) all() []liveServer {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]liveServer, 0, len(l.m))
	for _, ls := range l.m {
		out = append(out, *ls)
	}
	return out
}

func (l *liveStore) get(id int64) *liveServer {
	l.mu.RLock()
	defer l.mu.RUnlock()
	ls := l.m[id]
	if ls == nil {
		return nil
	}
	cp := *ls
	cp.Rates = append([]ratePoint(nil), ls.Rates...)
	return &cp
}

func (l *liveStore) drop(id int64) {
	l.mu.Lock()
	delete(l.m, id)
	l.mu.Unlock()
}

// ---------------------------------------------------------------- HTTP helpers

type apiError struct {
	Status int
	Msg    string
}

func (e *apiError) Error() string { return e.Msg }

func errStatus(status int, msg string) error { return &apiError{Status: status, Msg: msg} }

var (
	errNotFound  = errStatus(http.StatusNotFound, "not found")
	errForbidden = errStatus(http.StatusForbidden, "not allowed")
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var ae *apiError
	switch {
	case errors.As(err, &ae):
		writeJSON(w, ae.Status, map[string]string{"error": ae.Msg})
	case errors.Is(err, sql.ErrNoRows):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	default:
		slog.Error("api", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
	}
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return errStatus(http.StatusBadRequest, "invalid JSON: "+err.Error())
	}
	return nil
}

func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, errNotFound
	}
	return id, nil
}

func peerAddr(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

func (p *Panel) trustedAddr(addr netip.Addr) bool {
	if addr.IsLoopback() {
		return true
	}
	for _, pf := range p.cfg.TrustedProxies {
		if pf.Contains(addr) {
			return true
		}
	}
	return false
}

// peerTrusted says whether the direct peer is a reverse proxy whose forwarding headers we believe.
func (p *Panel) peerTrusted(r *http.Request) bool {
	addr, ok := peerAddr(r)
	return ok && p.trustedAddr(addr)
}

// clientIP is the requester's address. Behind trusted proxies it is the right-most address in
// X-Forwarded-For that is not itself a trusted proxy - entries further left were supplied by the
// client and could be anything.
func (p *Panel) clientIP(r *http.Request) string {
	addr, ok := peerAddr(r)
	if !ok {
		return "unknown"
	}
	if !p.trustedAddr(addr) {
		return addr.String()
	}
	if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
		hops := strings.Split(strings.Join(xff, ","), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
			if err != nil {
				break
			}
			a = a.Unmap()
			if !p.trustedAddr(a) || i == 0 {
				return a.String()
			}
		}
	}
	if a, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); err == nil {
		return a.Unmap().String()
	}
	return addr.String()
}
