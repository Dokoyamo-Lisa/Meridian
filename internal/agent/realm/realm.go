// Package realm runs realm forwards. Each forward is its own systemd instance, so editing one rule
// restarts only that rule.
package realm

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"meridian/internal/agent/cores"
	"meridian/internal/agent/service"
	"meridian/internal/proto"
)

const template = "meridian-realm@"

type Engine struct {
	Base    string
	ConfDir string

	mu sync.Mutex
}

func unitName(id int64) string { return service.Instance(template, id) }

func (e *Engine) confPath(id int64) string {
	return filepath.Join(e.ConfDir, strconv.FormatInt(id, 10)+".json")
}

func render(f proto.Forward) []byte {
	network := map[string]any{"no_tcp": !strings.Contains(f.Network, "tcp"), "use_udp": strings.Contains(f.Network, "udp")}
	if f.ProxyProtocol {
		network["send_proxy"] = true
		network["send_proxy_version"] = 2
	}
	cfg := map[string]any{
		"log":       map[string]any{"level": "warn", "output": "stderr"},
		"network":   network,
		"endpoints": []any{map[string]any{"listen": "0.0.0.0:" + strconv.Itoa(f.ListenPort), "remote": f.Target}},
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	return append(b, '\n')
}

// RestartAll restarts every realm forward (after an upgrade): open connections through them drop.
func (e *Engine) RestartAll() (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	entries, _ := os.ReadDir(e.ConfDir)
	n := 0
	var errs []string
	for _, ent := range entries {
		id, err := strconv.ParseInt(strings.TrimSuffix(ent.Name(), ".json"), 10, 64)
		if err != nil || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		if err := service.Restart(unitName(id)); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		n++
	}
	if len(errs) > 0 {
		return n, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return n, nil
}

// unsafeTarget says why a forward's target must not be used: its name resolves to loopback, an
// unspecified, link-local (cloud metadata) or multicast address. "" when it may be used (or does not
// resolve right now - realm then fails by itself).
func unsafeTarget(ctx context.Context, target string) string {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return "its target cannot be read"
	}
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else {
		lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		addrs, _ = net.DefaultResolver.LookupNetIP(lctx, "ip", host)
	}
	for _, a := range addrs {
		a = a.Unmap()
		if a.IsLoopback() || a.IsUnspecified() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsMulticast() {
			return fmt.Sprintf("its target %s leads to %s - this server itself or the cloud's metadata, where forwards may not lead", host, a)
		}
	}
	return ""
}

// Apply runs exactly the given realm forwards.
func (e *Engine) Apply(ctx context.Context, forwards []proto.Forward, version, mirror string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	want := map[int64]proto.Forward{}
	for _, f := range forwards {
		if f.Engine == "realm" {
			want[f.ID] = f
		}
	}
	var errs []string
	if len(want) > 0 {
		bin, err := cores.InUse(ctx, e.Base, "realm", version, mirror)
		if err != nil {
			return fmt.Errorf("install realm %s: %w", version, err)
		}
		unit := service.Spec{
			Name:         template,
			Description:  "Meridian forward %i (realm)",
			Exec:         bin,
			Args:         []string{"-c", e.ConfDir + "/%i.json"},
			Unprivileged: true,
			Caps:         []string{"CAP_NET_BIND_SERVICE"},
			NoFile:       1048576,
		}
		if _, err := service.Define(unit); err != nil {
			return err
		}
		// realm runs as an unprivileged dynamic user: its config (no secrets in it) must be readable
		if err := os.MkdirAll(e.ConfDir, 0o755); err != nil {
			return err
		}
		_ = os.Chmod(filepath.Dir(e.ConfDir), 0o755)
		_ = os.Chmod(e.ConfDir, 0o755)
	}
	for id, f := range want {
		// a name may lead anywhere: never to this server or the cloud's metadata (checked again on
		// every pass, so a name that starts pointing there stops being forwarded)
		if why := unsafeTarget(ctx, f.Target); why != "" {
			_ = service.DisableNow(unitName(id))
			errs = append(errs, fmt.Sprintf("forward %d is stopped: %s", id, why))
			continue
		}
		body := render(f)
		path := e.confPath(id)
		old, _ := os.ReadFile(path)
		changed := string(old) != string(body)
		if changed {
			if err := os.WriteFile(path, body, 0o644); err != nil {
				errs = append(errs, err.Error())
				continue
			}
		}
		switch {
		case !service.IsActive(unitName(id)):
			if err := service.EnableNow(unitName(id)); err != nil {
				errs = append(errs, err.Error())
			}
		case changed: // the admin edited this forward
			if err := service.Restart(unitName(id)); err != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	entries, _ := os.ReadDir(e.ConfDir)
	for _, ent := range entries {
		id, err := strconv.ParseInt(strings.TrimSuffix(ent.Name(), ".json"), 10, 64)
		if err != nil {
			continue
		}
		if _, ok := want[id]; !ok {
			service.Remove(unitName(id))
			os.Remove(e.confPath(id))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("realm: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Remove stops every realm forward (decommission).
func (e *Engine) Remove() {
	_ = e.Apply(context.Background(), nil, "", "")
	service.Undefine(template)
}

// Running reports which forwards have a running process.
func (e *Engine) Running(forwards []proto.Forward) map[int64]bool {
	out := map[int64]bool{}
	for _, f := range forwards {
		if f.Engine == "realm" {
			out[f.ID] = service.IsActive(unitName(f.ID))
		}
	}
	return out
}
