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
	"meridian/internal/agent/service"
	"meridian/internal/agent/wg"
	"meridian/internal/agent/xray"
	"meridian/internal/seal"
)

// agentService is the agent's own service: it restarts after a crash and holds no secrets itself.
var agentService = service.Spec{
	Name:        Unit,
	Description: "Meridian agent",
	Exec:        BinPath,
	Args:        []string{"run"},
	NoFile:      65536,
	RestartSec:  3,
}

// freePort finds two free loopback ports in a row from start: the Xray API's and, one up, the
// Hysteria auth hook's.
func freePort(start int) int {
	free := func(p int) bool {
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err != nil {
			return false
		}
		l.Close()
		return true
	}
	for p := start; p+1 <= 65535 && p < start+200; p += 2 {
		if free(p) && free(p+1) {
			return p
		}
	}
	return start
}

// Install configures and starts the agent on this host. apiPort is where its loopback ports start
// (0 = DefaultAPIPort); an agent that is already installed keeps its own, as moving them would
// restart Xray and Hysteria2.
func Install(panel, token string, apiPort int) error {
	if apiPort != 0 && (apiPort < 1024 || apiPort > 65534) {
		return errors.New("--api-port must be between 1024 and 65534 (the agent uses that port and the next)")
	}
	if runtime.GOOS != "linux" {
		return errors.New("the agent runs on Linux")
	}
	if os.Geteuid() != 0 {
		return errors.New("run as root")
	}
	if !service.Available() {
		return errors.New("this server runs neither systemd nor OpenRC - the agent needs one of them to run its services")
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
		start := DefaultAPIPort
		if apiPort != 0 {
			start = apiPort
		}
		cfg = &Config{APIPort: freePort(start)}
	} else if apiPort != 0 && apiPort != cfg.APIPort {
		fmt.Printf("Keeping this agent's local ports %d and %d (moving them would restart Xray and Hysteria2).\n", cfg.APIPort, cfg.APIPort+1)
	}
	cfg.Panel, cfg.Token = panel, token

	fmt.Println("Checking the connection to", panel)
	c, err := NewClient(panel, token, Version)
	if err != nil {
		return err
	}
	if st := (&Agent{}).loadState(); st != nil { // a reinstall: through the relay this server used, if any
		c.follow(st)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	_, err = c.State(ctx, "", 0)
	cancel()
	if err != nil {
		var se *statusError
		switch {
		case errors.As(err, &se) && se.code == http.StatusUnauthorized && strings.Contains(se.msg, "clock skew"):
			sync := "timedatectl set-ntp true"
			if service.Init() == "openrc" {
				sync = "apk add chrony && rc-update add chronyd && rc-service chronyd start"
			}
			return fmt.Errorf("this server's clock is off - enable time sync (%s) and retry: %s", sync, se.msg)
		case errors.As(err, &se) && se.code == http.StatusUnauthorized:
			return fmt.Errorf("the panel did not accept the token - copy a fresh command from the panel: %s", se.msg)
		default:
			return fmt.Errorf("cannot reach the panel at %s: %v", panel, err)
		}
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	if _, err := service.Define(agentService); err != nil {
		return err
	}
	if service.IsActive(Unit) {
		if err := service.Restart(Unit); err != nil {
			return err
		}
	} else if err := service.EnableNow(Unit); err != nil {
		return err
	}
	fmt.Println("\nRosélune agent installed and running.")
	fmt.Println("The server shows up as online in the panel within a few seconds.")
	fmt.Println("Logs: " + service.Logs(Unit))
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
	os.Remove(BinPath)
	say("Rosélune was removed from this server.")
	// last: this stops the agent itself when it runs as the service
	if service.Init() == "openrc" {
		// rc-service stops and starts services itself, from the calling process: hand the stop to a
		// detached shell, so it finishes after this process (the service) is gone
		_, _ = exec.Command("rc-update", "del", Unit, "default").CombinedOutput()
		_ = exec.Command("setsid", "sh", "-c", "sleep 1; rc-service "+Unit+" stop; rm -f /etc/init.d/"+Unit).Start()
		return
	}
	service.Undefine(Unit)
	_ = exec.Command("systemctl", "disable", "--now", Unit+".service").Run()
}
