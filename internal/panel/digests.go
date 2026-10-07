package panel

// Release checksums. The panel learns the SHA-256 of each core release file from GitHub over
// HTTPS and hands them to agents inside the signed, encrypted state. Agents then verify every
// download - from the panel's mirror or from GitHub - against a checksum that never travelled
// over the mirror itself. Agent upgrades are pinned the same way.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type coreAssets struct {
	repo   string
	tag    func(version string) string
	assets []string
}

var coreReleases = map[string]coreAssets{
	"xray": {repo: "XTLS/Xray-core", tag: func(v string) string { return "v" + v },
		assets: []string{"Xray-linux-64.zip", "Xray-linux-arm64-v8a.zip"}},
	"hysteria": {repo: "apernet/hysteria", tag: func(v string) string { return "app/v" + v },
		assets: []string{"hysteria-linux-amd64", "hysteria-linux-arm64"}},
	"realm": {repo: "zhboner/realm", tag: func(v string) string { return "v" + v },
		assets: []string{"realm-x86_64-unknown-linux-musl.tar.gz", "realm-aarch64-unknown-linux-musl.tar.gz"}},
}

type digestCache struct {
	mu      sync.Mutex
	m       map[string]string    // core/version/asset -> sha256 hex
	tried   map[string]time.Time // core/version -> last attempt
	refresh chan struct{}
}

func newDigestCache() *digestCache {
	return &digestCache{m: map[string]string{}, tried: map[string]time.Time{}, refresh: make(chan struct{}, 1)}
}

func (d *digestCache) get(key string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.m[key]
}

// forVersions returns the known checksums of the given core versions.
func (d *digestCache) forVersions(versions map[string]string) map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]string{}
	for core, v := range versions {
		for _, a := range coreReleases[core].assets {
			k := core + "/" + v + "/" + a
			if s, ok := d.m[k]; ok {
				out[k] = s
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (d *digestCache) complete(core, version string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, a := range coreReleases[core].assets {
		if d.m[core+"/"+version+"/"+a] == "" {
			return false
		}
	}
	return true
}

// kick asks the maintainer to look for missing checksums now (after a settings change).
func (d *digestCache) kick() {
	select {
	case d.refresh <- struct{}{}:
	default:
	}
}

// maintainDigests keeps checksums for the configured core versions. New ones reach the agents
// with the next state.
func (p *Panel) maintainDigests(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		changed := false
		set := p.settings()
		for core, v := range map[string]string{"xray": set.XrayVersion, "hysteria": set.HysteriaVersion, "realm": set.RealmVersion} {
			if v == "" || p.digests.complete(core, v) {
				continue
			}
			p.digests.mu.Lock()
			last := p.digests.tried[core+"/"+v]
			p.digests.mu.Unlock()
			if time.Since(last) < 5*time.Minute {
				continue
			}
			if n := p.fetchDigests(ctx, core, v); n > 0 {
				changed = true
			}
		}
		if changed {
			if err := p.compileAll(ctx); err != nil {
				slog.Warn("recompile after checksums", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-p.digests.refresh:
		}
	}
}

func githubGet(ctx context.Context, url string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "meridian-panel")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// fetchDigests learns the checksums of one release: GitHub's own asset digests first, the
// project's checksum files second. It returns how many it found.
func (p *Panel) fetchDigests(ctx context.Context, core, version string) int {
	rel := coreReleases[core]
	p.digests.mu.Lock()
	p.digests.tried[core+"/"+version] = time.Now()
	p.digests.mu.Unlock()
	found := map[string]string{}
	tag := rel.tag(version)
	if b, err := githubGet(ctx, fmt.Sprintf("https://api.github.com/repos/%s/releases/tags/%s", rel.repo, tag), 4<<20); err == nil {
		var r struct {
			Assets []struct {
				Name   string `json:"name"`
				Digest string `json:"digest"`
			} `json:"assets"`
		}
		if json.Unmarshal(b, &r) == nil {
			for _, a := range r.Assets {
				if d := strings.ToLower(strings.TrimPrefix(a.Digest, "sha256:")); len(d) == 64 {
					found[a.Name] = d
				}
			}
		}
	}
	dl := fmt.Sprintf("https://github.com/%s/releases/download/%s/", rel.repo, strings.ReplaceAll(tag, "/", "%2F"))
	for _, a := range rel.assets {
		if found[a] != "" {
			continue
		}
		switch core {
		case "xray":
			if b, err := githubGet(ctx, dl+a+".dgst", 1<<16); err == nil {
				for _, l := range strings.Split(string(b), "\n") {
					if v, ok := strings.CutPrefix(strings.TrimSpace(l), "SHA2-256="); ok && len(strings.TrimSpace(v)) == 64 {
						found[a] = strings.ToLower(strings.TrimSpace(v))
					}
				}
			}
		case "hysteria":
			if b, err := githubGet(ctx, dl+"hashes.txt", 1<<20); err == nil {
				for _, l := range strings.Split(string(b), "\n") {
					if f := strings.Fields(l); len(f) == 2 && filepath.Base(f[1]) == a && len(f[0]) == 64 {
						found[a] = strings.ToLower(f[0])
					}
				}
			}
		}
	}
	n := 0
	p.digests.mu.Lock()
	for _, a := range rel.assets {
		if d := found[a]; d != "" {
			p.digests.m[core+"/"+version+"/"+a] = d
			n++
		}
	}
	p.digests.mu.Unlock()
	if n < len(rel.assets) {
		slog.Warn("release checksums incomplete - agents will ask GitHub themselves", "core", core, "version", version, "found", n)
	}
	return n
}

// ---------------------------------------------------------------- agent binaries

type agentSums struct {
	mu   sync.Mutex
	at   map[string]time.Time // file modification time when hashed
	sums map[string]string
}

// agentSHA256 returns the checksums of the agent binaries the panel serves, by architecture.
func (p *Panel) agentSHA256() map[string]string {
	out := map[string]string{}
	if p.cfg.AgentDir == "" {
		return out
	}
	p.agentSums.mu.Lock()
	defer p.agentSums.mu.Unlock()
	for _, arch := range []string{"amd64", "arm64"} {
		path := filepath.Join(p.cfg.AgentDir, "meridian-agent-linux-"+arch)
		st, err := os.Stat(path)
		if err != nil {
			continue
		}
		if p.agentSums.at[arch].Equal(st.ModTime()) && p.agentSums.sums[arch] != "" {
			out[arch] = p.agentSums.sums[arch]
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			continue
		}
		p.agentSums.at[arch] = st.ModTime()
		p.agentSums.sums[arch] = hex.EncodeToString(h.Sum(nil))
		out[arch] = p.agentSums.sums[arch]
	}
	return out
}
