// Package systemd manages the units the agent owns. Cores run as their own units so restarting or
// upgrading the agent never touches live traffic.
package systemd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const UnitDir = "/etc/systemd/system"

func run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, s)
	}
	return s, nil
}

// WriteUnit writes a unit file; it reports whether the content changed (and reloads systemd then).
func WriteUnit(name, content string) (bool, error) {
	path := filepath.Join(UnitDir, name)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, []byte(content)) {
		return false, nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return false, err
	}
	_, err := run("daemon-reload")
	return true, err
}

func RemoveUnit(name string) {
	_, _ = run("disable", "--now", name)
	os.Remove(filepath.Join(UnitDir, name))
	_, _ = run("daemon-reload")
}

func EnableNow(name string) error { _, err := run("enable", "--now", name); return err }
func Restart(name string) error   { _, err := run("restart", name); return err }
func Stop(name string) error      { _, err := run("stop", name); return err }

// DisableNow stops a unit and keeps it from starting at boot.
func DisableNow(name string) error { _, err := run("disable", "--now", name); return err }

func IsActive(name string) bool {
	out, _ := run("is-active", name)
	return out == "active"
}

// Status returns the main PID and when the unit became active.
func Status(name string) (pid int, since int64) {
	out, err := run("show", name, "-p", "MainPID", "-p", "ActiveEnterTimestamp")
	if err != nil {
		return 0, 0
	}
	for _, l := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(l, "=")
		switch k {
		case "MainPID":
			pid, _ = strconv.Atoi(v)
		case "ActiveEnterTimestamp":
			for _, layout := range []string{"Mon 2006-01-02 15:04:05 MST", "Mon 2006-01-02 15:04:05 -0700"} {
				if t, err := time.Parse(layout, v); err == nil {
					since = t.Unix()
					break
				}
			}
		}
	}
	return pid, since
}

// Available reports whether systemd runs this host.
func Available() bool {
	_, err := os.Stat("/run/systemd/system")
	return err == nil
}
