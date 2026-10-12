package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"meridian/internal/agent/cores"
	"meridian/internal/agent/service"
	"meridian/internal/proto"
)

// Installing a server's proxies again, on the supervisor's request (ActionReinstall with Rebuild or
// Clean).
//
// Reinstall (Rebuild): every core the server runs is downloaded again from its official release and
// checked - when one cannot be, nothing else is changed. Then what the agent generated for the
// proxies (their services and configurations, WireGuard interfaces, firewall rules) is cleared, and
// the agent restarts and builds it all again from its state: the same protocols, ports and keys, so
// users' links keep working. Everyone connected reconnects once.
//
// Clean reinstall (Clean): the panel has removed the server's protocols and forwards already. The
// agent clears the cores as well, and starts over as on a new server: fresh cores from their official
// releases, then the setup script once more. The agent itself, its settings and its certificates stay.

// rebuild installs the server's proxies again; clean starts over.
func (a *Agent) rebuild(ctx context.Context, st *proto.State, clean bool) (string, error) {
	if !runtimeOK() {
		return "", errors.New("this host is not Linux with systemd or OpenRC")
	}
	var done []string
	if !clean {
		var errs []string
		for _, j := range a.coreJobs(ctx, st, "all") {
			if err := cores.Reinstall(ctx, DataDir, j.name, j.version, st.Cores.Mirror); err != nil {
				errs = append(errs, fmt.Sprintf("%s %s: %v", j.label, j.version, err))
				continue
			}
			a.health.Replaced(cores.Path(DataDir, j.name, j.version))
			done = append(done, j.label+" "+j.version)
		}
		if len(errs) > 0 {
			return "", fmt.Errorf("could not download %s - nothing else was changed (the old files are kept); try again later",
				strings.Join(errs, "; "))
		}
	}

	a.applyMu.Lock() // nothing is applied again in this process: the agent restarts and builds it all anew
	a.teardown()
	if clean {
		os.RemoveAll(filepath.Join(DataDir, "cores"))
		os.Remove(a.setupPath()) // the setup script runs once more, after the new cores
		a.prep.mu.Lock()
		a.prep.install = true // the new cores are installed by themselves, as on a new server
		a.prep.persist()
		a.prep.mu.Unlock()
	}

	var msg string
	if clean {
		msg = "cleared the cores and everything set up for proxies; the agent restarts and starts over with fresh cores from their official releases"
		a.event("reinstalled", "warn", "Clean reinstall on request: the cores and everything set up for proxies were cleared; starting over")
	} else {
		what := "no cores yet"
		if len(done) > 0 {
			what = strings.Join(done, ", ")
		}
		msg = "installed again from the official releases: " + what + "; the agent restarts and builds every proxy again from the panel"
		a.event("reinstalled", "warn", "Reinstalled on request: "+what+" from the official releases; every proxy is built again")
	}
	go func() {
		time.Sleep(3 * time.Second) // let the result reach the panel first
		restartSelf()
	}()
	return msg, nil
}

// teardown clears what the agent set up for the proxies - their services and configurations,
// WireGuard interfaces and firewall rules - and leaves the agent, its settings and its data (cores,
// certificates, the last state) in place. The agent restarts right after it.
func (a *Agent) teardown() {
	a.xray.Remove()
	a.hy.Remove()
	a.solo.Remove()
	a.realm.Remove()
	a.wg.Remove()
	a.nft.Remove()
}

// restartSelf restarts the agent's service; that restart touches no traffic it did not touch already.
func restartSelf() {
	if service.Init() == "openrc" {
		os.Exit(0) // supervise-daemon starts it again (see upgradeSelf)
	}
	if err := service.Restart(Unit); err != nil {
		os.Exit(1) // Restart=always brings it back
	}
}
