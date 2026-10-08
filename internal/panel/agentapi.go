package panel

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"meridian/internal/proto"
	"meridian/internal/seal"
)

//go:embed install.sh
var installScript string

// ---------------------------------------------------------------- request authentication

type nonceCache struct {
	mu sync.Mutex
	m  map[string]int64
}

func newNonceCache() *nonceCache { return &nonceCache{m: map[string]int64{}} }

// add records a nonce; false means it was seen before (a replay).
func (c *nonceCache) add(server int64, nonce string, ts int64) bool {
	key := strconv.FormatInt(server, 10) + "/" + nonce
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, seen := c.m[key]; seen {
		return false
	}
	c.m[key] = ts + 2*seal.MaxSkew
	if len(c.m) > 50000 {
		t := now()
		for k, exp := range c.m {
			if exp < t {
				delete(c.m, k)
			}
		}
	}
	return true
}

// agentAuth verifies a signed agent request and returns the server and its keys.
func (p *Panel) agentAuth(r *http.Request, body []byte) (*Server, *seal.Keys, error) {
	id, err := strconv.ParseInt(r.Header.Get(seal.HeaderServer), 10, 64)
	if err != nil {
		return nil, nil, errors.New("missing server id")
	}
	ts, err := strconv.ParseInt(r.Header.Get(seal.HeaderTime), 10, 64)
	if err != nil {
		return nil, nil, errors.New("missing time")
	}
	if d := now() - ts; d > seal.MaxSkew || d < -seal.MaxSkew {
		return nil, nil, fmt.Errorf("clock skew of %ds - fix the server's time (NTP)", d)
	}
	nonce := r.Header.Get(seal.HeaderNonce)
	if len(nonce) < 8 {
		return nil, nil, errors.New("missing nonce")
	}
	srv, err := p.serverByID(r.Context(), id)
	if err != nil {
		return nil, nil, errors.New("unknown server")
	}
	keys, err := seal.Derive(srv.Secret)
	if err != nil {
		return nil, nil, err
	}
	if !keys.Verify(r.Method, r.URL.RequestURI(), ts, nonce, body, r.Header.Get(seal.HeaderSign)) {
		return nil, nil, errors.New("bad signature - the token may have been rotated")
	}
	if !p.nonces.add(id, nonce, ts) {
		return nil, nil, errors.New("replayed request")
	}
	if srv.PassSecret != srv.Secret {
		p.adoptPassSecret(r.Context(), srv)
	}
	return srv, keys, nil
}

// adoptPassSecret: the agent of a server whose token was rotated is back with the new one, so its
// proxy passes take credentials from the new secret now - on this server and on their exits at once.
// (Until now the old agent kept passing with the old credentials, which the exits still accepted.)
func (p *Panel) adoptPassSecret(ctx context.Context, srv *Server) {
	res, err := p.db.Exec1(`UPDATE servers SET pass_secret = secret WHERE id = ? AND pass_secret != secret`, srv.ID)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return
	}
	srv.PassSecret = srv.Secret
	p.touchServers(append(p.passExitsOf(ctx, srv.ID), srv.ID)...)
}

func agentDeny(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	io.WriteString(w, err.Error())
}

func writeSealed(w http.ResponseWriter, keys *seal.Keys, path string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "encode", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", seal.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Write(keys.Seal(path, b))
}

// ---------------------------------------------------------------- state (long poll)

func (p *Panel) handleAgentState(w http.ResponseWriter, r *http.Request) {
	srv, keys, err := p.agentAuth(r, nil)
	if err != nil {
		agentDeny(w, err)
		return
	}
	c := p.hub.get(srv.ID)
	if c == nil {
		if err := p.recompile(r.Context(), srv.ID); err != nil {
			http.Error(w, "compile failed", http.StatusInternalServerError)
			return
		}
		c = p.hub.get(srv.ID)
	}
	have := r.URL.Query().Get("rev")
	if c != nil && c.state.Rev == have {
		wait, _ := strconv.Atoi(r.URL.Query().Get("wait"))
		wait = clamp(wait, 0, 50)
		if wait > 0 {
			c = p.hub.wait(r.Context(), srv.ID, have, time.Duration(wait)*time.Second)
		} else {
			c = nil
		}
	}
	if c == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", seal.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Write(keys.Seal(seal.ReplyContext(r.URL.RequestURI(), r.Header.Get(seal.HeaderNonce)), c.body))
}

// ---------------------------------------------------------------- report

func (p *Panel) handleAgentReport(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<20))
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	srv, keys, err := p.agentAuth(r, body)
	if err != nil {
		agentDeny(w, err)
		return
	}
	plain, err := keys.Open(r.URL.RequestURI(), body)
	if err != nil {
		http.Error(w, "cannot open body", http.StatusBadRequest)
		return
	}
	var rep proto.Report
	if err := json.Unmarshal(plain, &rep); err != nil {
		http.Error(w, "bad report", http.StatusBadRequest)
		return
	}
	ack, err := p.ingest(r.Context(), srv, &rep)
	if err != nil {
		slog.Error("ingest", "server", srv.ID, "err", err)
		http.Error(w, "ingest failed", http.StatusInternalServerError)
		return
	}
	writeSealed(w, keys, seal.ReplyContext(r.URL.RequestURI(), r.Header.Get(seal.HeaderNonce)), proto.ReportAck{Ack: ack, ServerTime: now()})
}

// ---------------------------------------------------------------- install script and binaries

// renderInstallScript fills in the panel address and the checksums of the agent binaries.
func (p *Panel) renderInstallScript(base string) (string, error) {
	if !safeBaseURL(base) {
		return "", errStatus(http.StatusBadRequest, "set the panel's public URL in Settings first")
	}
	sums := p.agentSHA256()
	return strings.NewReplacer("__PANEL_URL__", base, "__SHA_AMD64__", sums["amd64"], "__SHA_ARM64__", sums["arm64"]).
		Replace(installScript), nil
}

func (p *Panel) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	s, err := p.renderInstallScript(p.baseURL(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, s)
}

var agentFileRE = regexp.MustCompile(`^meridian-agent-linux-(amd64|arm64)(\.sha256)?$`)

func (p *Panel) handleAgentDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	m := agentFileRE.FindStringSubmatch(name)
	if m == nil || p.cfg.AgentDir == "" {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(p.cfg.AgentDir, strings.TrimSuffix(name, ".sha256"))
	if m[2] != "" {
		f, err := os.Open(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			http.Error(w, "read", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(h.Sum(nil)), strings.TrimSuffix(name, ".sha256"))
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, path)
}

// ---------------------------------------------------------------- core mirror

var (
	versionRE    = regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}$`)
	mirrorAssets = map[string]*regexp.Regexp{
		"xray":     regexp.MustCompile(`^Xray-linux-(64|arm64-v8a)\.zip$`),
		"realm":    regexp.MustCompile(`^realm-(x86_64|aarch64)-unknown-linux-musl\.tar\.gz$`),
		"hysteria": regexp.MustCompile(`^hysteria-linux-(amd64|arm64)$`),
	}
	mirrorRepos = map[string]string{"xray": "XTLS/Xray-core", "realm": "zhboner/realm", "hysteria": "apernet/hysteria"}
)

// handleMirror serves core release files, fetching each from GitHub once. Servers that cannot
// reach GitHub well download from the panel instead. Only files whose checksum the panel knows
// are mirrored, and they are verified before they are kept; agents verify them again against the
// checksum in their signed state.
func (p *Panel) handleMirror(w http.ResponseWriter, r *http.Request) {
	core, version, asset := r.PathValue("core"), r.PathValue("version"), r.PathValue("asset")
	re := mirrorAssets[core]
	if re == nil || !re.MatchString(asset) || !versionRE.MatchString(version) || !p.settings().Mirror {
		http.NotFound(w, r)
		return
	}
	dir := filepath.Join(p.cfg.DataDir, "mirror", core, version)
	path := filepath.Join(dir, asset)
	if _, err := os.Stat(path); err != nil {
		// only versions the panel uses are fetched: anyone may ask, and every file stays on its disk
		if !p.mirrorVersions(r.Context(), core)[version] {
			http.NotFound(w, r)
			return
		}
		if !p.limiter.allow("mirror:"+p.clientIP(r), 30, time.Hour) {
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		key := core + "/" + version + "/" + asset
		want := p.digests.get(key)
		if want == "" {
			p.fetchDigests(r.Context(), core, version)
			want = p.digests.get(key)
		}
		if want == "" {
			http.Error(w, "no checksum for this file - download it from GitHub instead", http.StatusBadGateway)
			return
		}
		p.mirrorMu.Lock()
		tag := "v" + version
		if core == "hysteria" {
			tag = "app%2Fv" + version
		}
		err := fetchOnce(path, fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", mirrorRepos[core], tag, asset), want)
		p.mirrorMu.Unlock()
		if err != nil {
			slog.Warn("mirror", "asset", asset, "err", err)
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
			return
		}
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, path)
}

// mirrorVersions are the versions of a core the mirror serves: the one in Settings and those the
// servers run.
func (p *Panel) mirrorVersions(ctx context.Context, core string) map[string]bool {
	set := p.settings()
	out := map[string]bool{}
	switch core {
	case "xray":
		out[set.XrayVersion] = true
		if servers, err := p.serversOf(ctx, 0); err == nil {
			for _, s := range servers {
				if s.XrayVersion != "" {
					out[strings.TrimPrefix(s.XrayVersion, "v")] = true
				}
			}
		}
	case "hysteria":
		out[set.HysteriaVersion] = true
		for _, ls := range p.live.all() {
			for name, cs := range ls.Live.Cores {
				if strings.HasPrefix(name, "hysteria") && cs.Version != "" {
					out[cs.Version] = true
				}
			}
		}
	case "realm":
		out[set.RealmVersion] = true
	}
	return out
}

// pruneMirror deletes the versions the mirror no longer serves.
func (p *Panel) pruneMirror(ctx context.Context) {
	for core := range mirrorRepos {
		keep := p.mirrorVersions(ctx, core)
		dir := filepath.Join(p.cfg.DataDir, "mirror", core)
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() && !keep[e.Name()] {
				_ = os.RemoveAll(filepath.Join(dir, e.Name()))
			}
		}
	}
}

// fetchOnce downloads url to path unless it is there, keeping it only if its SHA-256 is want.
func fetchOnce(path, url, want string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	tmp := path + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 200<<20)); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		os.Remove(tmp)
		return fmt.Errorf("%s does not match its published checksum", url)
	}
	return os.Rename(tmp, path)
}
