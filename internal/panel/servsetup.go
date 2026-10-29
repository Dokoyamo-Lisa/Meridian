package panel

import (
	"fmt"
	"net/http"
	"strings"
)

// A server's setup script: what the supervisor wrote for the agent to run on the server after the
// cores are in place - as root, once for each version - to ready the environment the panel needs
// (install a package, open a port at the host's firewall, tune the kernel). It runs only from a
// signed-in browser, after a confirmation, never for a guest server or from an API token. Saving a
// new script gives it a new version, which the agent runs once; clearing it stops it.

const (
	maxSetupScript = 64 << 10
	needSetup      = "the setup script needs agent 1.4 or later on this server - upgrade its agent first (nobody is disconnected by that)"
)

type setupInput struct {
	Script  string `json:"script" doc:"The script (empty clears it). At most 64 KB"`
	Shell   string `json:"shell" doc:"The interpreter: /bin/sh (default) or /bin/bash"`
	Confirm bool   `json:"confirm" doc:"Must be true: a setup script runs as root on the server"`
}

type setupOutput struct {
	Version int64 `json:"version" doc:"The script's version now (0 = none)"`
}

// apiPutSetup saves a server's setup script (browser sessions only; routes.go).
func (p *Panel) apiPutSetup(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownServer(r.Context(), a, id)
	if err != nil {
		return err
	}
	var in setupInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if s.Guest {
		return ownerOnly(s, "the setup script")
	}
	script := strings.ReplaceAll(in.Script, "\r\n", "\n")
	if len(script) > maxSetupScript {
		return errStatus(http.StatusBadRequest, "the setup script is too large (64 KB at most)")
	}
	if strings.TrimSpace(script) == "" {
		script = ""
	}
	if script != "" && !in.Confirm {
		return errStatus(http.StatusBadRequest, "confirm the setup script first (confirm: true): it runs as root on the server")
	}
	if script != "" && !s.caps().Setup {
		return errStatus(http.StatusBadRequest, needSetup)
	}
	shell := "/bin/sh"
	if in.Shell == "/bin/bash" {
		shell = "/bin/bash"
	}
	version := s.SetupVersion
	if script != s.SetupScript || (script != "" && shell != s.SetupShell) {
		version = now() // a new version: the agent runs it once
	}
	if script == "" {
		version = 0
	}
	if _, err := p.db.Exec1(`UPDATE servers SET setup_script = ?, setup_shell = ?, setup_version = ? WHERE id = ?`,
		script, shell, version, s.ID); err != nil {
		return err
	}
	msg := fmt.Sprintf("%s removed the setup script from %s", a.Username, s.Name)
	if script != "" {
		msg = fmt.Sprintf("%s saved a setup script for %s - the agent runs it after the cores", a.Username, s.Name)
	}
	p.event(s.AccountID, "warn", "setup_script", s.ID, 0, a.ID, msg, nil)
	p.touchServers(s.ID)
	writeJSON(w, http.StatusOK, setupOutput{Version: version})
	return nil
}
