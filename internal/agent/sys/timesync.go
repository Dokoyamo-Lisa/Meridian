package sys

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// timeDaemons are the programs that keep a host's clock in time.
var timeDaemons = map[string]string{"chronyd": "chrony", "ntpd": "ntpd", "openntpd": "OpenNTPD", "systemd-timesyn": "systemd-timesyncd"}

// TimeSync says whether the host keeps its clock in time, and how: systemd says its clock is
// synchronized, or one of the time daemons runs. Agents sign their reports with the time, so a
// clock that drifts far keeps the panel from accepting them.
func TimeSync() (bool, string) {
	if lookPath("timedatectl") {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		out, err := exec.CommandContext(ctx, "timedatectl", "show", "-p", "NTPSynchronized", "--value").Output()
		cancel()
		if err == nil && strings.TrimSpace(string(out)) == "yes" {
			return true, "kept in time"
		}
	}
	comms, _ := filepath.Glob("/proc/[0-9]*/comm")
	for _, c := range comms {
		b, err := os.ReadFile(c)
		if err != nil {
			continue
		}
		if name, ok := timeDaemons[strings.TrimSpace(string(b))]; ok {
			return true, "kept in time by " + name
		}
	}
	return false, ""
}
