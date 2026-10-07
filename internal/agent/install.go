package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"meridian/internal/agent/hy"
	"meridian/internal/agent/nft"
	"meridian/internal/agent/realm"
	"meridian/internal/agent/systemd"
	"meridian/internal/agent/wg"
	"meridian/internal/agent/xray"
	"meridian/internal/seal"
)

const agentUnit = `[Unit]
Description=Meridian agent
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
ExecStart=/usr/local/bin/meridian-agent run
Restart=always
RestartSec=3
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`

func freePort(start int) int {
	for p := start; p < start+200; p++ {
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err == nil {
			l.Close()
			return p
		}
	}
	return start
}

// Install configures and starts the agent on this host.
func Install(panel, token string) error {
	if runtime.GOOS != "linux" {
		return errors.New("the agent runs on Linux")
	}
	if os.Geteuid() != 0 {
		return errors.New("run as root")
	}
	if !systemd.Available() {
		return errors.New("systemd is required")
	}
	panel = strings.TrimRight(strings.TrimSpace(panel), "/")
	if !strings.HasPrefix(panel, "http://") && !strings.HasPrefix(panel, "https://") {
		return errors.New("--panel must start with http:// or https://")
	}
	if _, _, err := seal.ParseToken(token); err != nil {
		return fmt.Errorf("the token looks wrong - copy the whole command from the panel (%v)", err)
	}
	cfg, err := LoadConfig()
	if err != nil {
		cfg = &Config{APIPort: freePort(62789)}
	}
	cfg.Panel, cfg.Token = panel, token

	fmt.Println("Checking the connection to", panel)
	c, err := NewClient(panel, token, Version)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	_, err = c.State(ctx, "", 0)
	cancel()
	if err != nil {
		var se *statusError
		switch {
		case errors.As(err, &se) && se.code == http.StatusUnauthorized && strings.Contains(se.msg, "clock skew"):
			return fmt.Errorf("this server's clock is off - enable time sync (timedatectl set-ntp true) and retry: %s", se.msg)
		case errors.As(err, &se) && se.code == http.StatusUnauthorized:
			return fmt.Errorf("the panel did not accept the token - copy a fresh command from the panel: %s", se.msg)
		default:
			return fmt.Errorf("cannot reach the panel at %s: %v", panel, err)
		}
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	if _, err := systemd.WriteUnit(Unit, agentUnit); err != nil {
		return err
	}
	if systemd.IsActive(Unit) {
		if err := systemd.Restart(Unit); err != nil {
			return err
		}
	} else if err := systemd.EnableNow(Unit); err != nil {
		return err
	}
	fmt.Println("\nMeridian agent installed and running.")
	fmt.Println("The server shows up as online in the panel within a few seconds.")
	fmt.Println("Logs: journalctl -u meridian-agent -f")
	return nil
}

// Uninstall removes everything the agent set up. Traffic through this server stops.
func Uninstall(verbose bool) {
	say := func(s string) {
		if verbose {
			fmt.Println(s)
		}
	}
	say("Stopping Xray")
	(&xray.Engine{Base: DataDir, ConfDir: filepath.Join(ConfDir, "xray"), RunDir: RunDir}).Remove()
	say("Stopping Hysteria2")
	(&hy.Engine{Base: DataDir, ConfDir: filepath.Join(ConfDir, "hy2")}).Remove()
	say("Removing realm forwards")
	(&realm.Engine{Base: DataDir, ConfDir: filepath.Join(ConfDir, "realm")}).Remove()
	say("Removing WireGuard interfaces")
	wg.New().Remove()
	say("Removing firewall and forwarding rules")
	nft.New().Remove()
	os.Remove("/etc/sysctl.d/99-meridian.conf")
	os.RemoveAll(DataDir)
	os.RemoveAll(RunDir)
	os.RemoveAll(ConfDir)
	say("Removing the agent")
	os.Remove(filepath.Join(systemd.UnitDir, Unit))
	_ = exec.Command("systemctl", "daemon-reload").Run()
	os.Remove(BinPath)
	say("Meridian was removed from this server.")
	// last: this stops the agent itself when it runs as the service
	_ = exec.Command("systemctl", "disable", "--now", Unit).Run()
}
