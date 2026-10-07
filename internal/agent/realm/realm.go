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
