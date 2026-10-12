package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"meridian/internal/proto"
)

// The supervisor's setup script. After the cores are in place, the agent runs a script the
// supervisor wrote for this server - as root, once for each version (a new confirmed click is a new
// version) - to ready the environment the panel needs: install a package, open a port at the host's
// firewall, tune the kernel. What it printed and how it ended are reported (Live.Setup) and kept on
// disk, so the panel shows it and it survives a restart. It runs with a time limit; nothing re-runs
// a version that already ran.

const (
	setupTimeout = 10 * time.Minute
	setupMaxOut  = 16 << 10
)

type runner struct {
	mu      sync.Mutex
	result  *proto.SetupResult
	running bool
	ranVer  int64 // the version that ran (or is running)
}

func (r *runner) status() *proto.SetupResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.result == nil {
		return nil
	}
	c := *r.result
	return &c
}

// setupPath is where the last result is kept.
func (a *Agent) setupPath() string { return filepath.Join(DataDir, "setup.json") }

func (r *runner) load(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.result != nil || r.ranVer != 0 {
		return
	}
	if b, err := os.ReadFile(path); err == nil {
		var res proto.SetupResult
		if json.Unmarshal(b, &res) == nil && res.Version != 0 {
			r.result, r.ranVer = &res, res.Version
		}
	}
}

// maybeSetup runs the setup script when there is a new one and the cores are ready. A script of a
// version that already ran (or is running) is not run again; a cleared script (nil) stops reporting.
func (a *Agent) maybeSetup(st *proto.State) {
	if !runtimeOK() {
		return
	}
	a.setup.load(a.setupPath())
	s := st.Setup
	a.setup.mu.Lock()
	if s == nil {
		if a.setup.result != nil || a.setup.ranVer != 0 {
			a.setup.result, a.setup.ranVer = nil, 0
			os.Remove(a.setupPath())
		}
		a.setup.mu.Unlock()
		return
	}
	if a.setup.running || a.setup.ranVer == s.Version {
		a.setup.mu.Unlock()
		return
	}
	// the cores first: a setup script may rely on them (it runs once the server is prepared)
	if !a.prep.ready() {
		a.setup.mu.Unlock()
		return
	}
	a.setup.running, a.setup.ranVer = true, s.Version
	script := *s
	a.setup.mu.Unlock()
	go a.runSetup(script)
}

func (a *Agent) runSetup(s proto.SetupScript) {
	started := time.Now().Unix()
	a.setResult(&proto.SetupResult{Version: s.Version, Started: started})
	a.event("setup_script", "info", "Running the setup script written for this server")

	shell := "/bin/sh"
	if s.Shell == "/bin/bash" && isFile("/bin/bash") {
		shell = "/bin/bash"
	}
	dir := filepath.Join(DataDir, "setup")
	_ = os.MkdirAll(dir, 0o700)
	file := filepath.Join(dir, "setup.sh")
	res := &proto.SetupResult{Version: s.Version, Started: started}
	if err := os.WriteFile(file, []byte(s.Script), 0o700); err != nil {
		res.Finished, res.ExitCode, res.Error = time.Now().Unix(), -1, "could not write the script: "+err.Error()
		a.finishSetup(res)
		return
	}
	defer os.Remove(file)

	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, file)
	cmd.Dir = "/root"
	cmd.Env = append(os.Environ(), "MERIDIAN_SETUP=1", "HOME=/root")
	var out tailBuffer
	out.max = setupMaxOut
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	res.Finished, res.Output = time.Now().Unix(), out.String()
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		res.ExitCode, res.Error = -1, "the script was still running after 10 minutes and was stopped"
	case err == nil:
		res.ExitCode = 0
	default:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
		} else {
			res.ExitCode, res.Error = -1, err.Error()
		}
	}
	a.finishSetup(res)
}

func (a *Agent) finishSetup(res *proto.SetupResult) {
	a.setup.mu.Lock()
	a.setup.running = false
	a.setup.mu.Unlock()
	a.setResult(res)
	if b, err := json.Marshal(res); err == nil {
		_ = os.WriteFile(a.setupPath(), b, 0o600)
	}
	level, msg := "info", "The setup script finished"
	switch {
	case res.Error != "":
		level, msg = "warn", "The setup script did not finish: "+res.Error
	case res.ExitCode != 0:
		level, msg = "warn", "The setup script ended with a non-zero exit code"
	}
	a.event("setup_script", level, msg)
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

func (a *Agent) setResult(res *proto.SetupResult) {
	a.setup.mu.Lock()
	a.setup.result = res
	a.setup.mu.Unlock()
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	buf bytes.Buffer
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	t.buf.Write(p)
	if t.max > 0 && t.buf.Len() > t.max {
		b := t.buf.Bytes()
		t.buf = *bytes.NewBuffer(append([]byte(nil), b[t.buf.Len()-t.max:]...))
	}
	return n, nil
}

func (t *tailBuffer) String() string { return t.buf.String() }

// isFile says whether path is a regular file.
func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}
