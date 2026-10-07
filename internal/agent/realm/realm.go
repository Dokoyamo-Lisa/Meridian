// Package realm runs realm forwards. Each forward is its own systemd instance, so editing one rule
// restarts only that rule.
package realm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"meridian/internal/agent/cores"
	"meridian/internal/agent/systemd"
	"meridian/internal/proto"
)

const template = "meridian-realm@.service"

type Engine struct {
	Base    string
	ConfDir string

	mu sync.Mutex
}

func unitName(id int64) string { return fmt.Sprintf("meridian-realm@%d.service", id) }

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
		bin, err := cores.EnsureRealm(ctx, e.Base, version, mirror)
		if err != nil {
			return fmt.Errorf("install realm %s: %w", version, err)
		}
		unit := fmt.Sprintf(`[Unit]
Description=Meridian forward %%i (realm)
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
ExecStart=%s -c %s/%%i.json
Restart=always
RestartSec=2
LimitNOFILE=1048576
DynamicUser=yes
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
`, bin, e.ConfDir)
		if _, err := systemd.WriteUnit(template, unit); err != nil {
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
		case !systemd.IsActive(unitName(id)):
			if err := systemd.EnableNow(unitName(id)); err != nil {
				errs = append(errs, err.Error())
			}
		case changed: // the admin edited this forward
			if err := systemd.Restart(unitName(id)); err != nil {
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
			systemd.RemoveUnit(unitName(id))
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
	os.Remove(filepath.Join(systemd.UnitDir, template))
}

// Running reports which forwards have a running process.
func (e *Engine) Running(forwards []proto.Forward) map[int64]bool {
	out := map[int64]bool{}
	for _, f := range forwards {
		if f.Engine == "realm" {
			out[f.ID] = systemd.IsActive(unitName(f.ID))
		}
	}
	return out
}
