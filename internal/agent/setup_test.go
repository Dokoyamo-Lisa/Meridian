package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"encoding/json"

	"meridian/internal/proto"
)

func marshalJSON(v any) ([]byte, error) { return json.Marshal(v) }

// TestSetupScript: the agent runs the script once per version, captures its output and exit code,
// keeps it on disk, and does not run the same version again; a new version runs again; clearing it
// stops reporting. The script runs only on a real host (runtimeOK), so this tests the pieces.
func TestSetupScript(t *testing.T) {
	dir := t.TempDir()
	old := DataDir
	DataDir = dir
	defer func() { DataDir = old }()
	a := &Agent{kick: make(chan struct{}, 4)}

	// a result kept on disk is loaded, and its version counts as run
	res := &proto.SetupResult{Version: 100, ExitCode: 0, Finished: time.Now().Unix(), Output: "done"}
	b, _ := marshalJSON(res)
	if err := os.WriteFile(a.setupPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	a.setup.load(a.setupPath())
	if got := a.setup.status(); got == nil || got.Version != 100 {
		t.Fatalf("the kept result: %+v", got)
	}
	if a.setup.ranVer != 100 {
		t.Errorf("the kept version is not counted as run: %d", a.setup.ranVer)
	}

	// the tail buffer keeps the last bytes
	var tb tailBuffer
	tb.max = 5
	tb.Write([]byte("abcdefghij"))
	if tb.String() != "fghij" {
		t.Errorf("tail buffer: %q", tb.String())
	}

	// clearing the script forgets the result
	st := &proto.State{}
	if !runtimeOK() {
		t.Skip("not Linux: maybeSetup does nothing here")
	}
	a.maybeSetup(st)
	if a.setup.status() != nil {
		t.Errorf("the result was not cleared: %+v", a.setup.status())
	}
	if _, err := os.Stat(a.setupPath()); !os.IsNotExist(err) {
		t.Errorf("the kept result file was not removed")
	}

	// a new script runs once and reports its exit code
	a.full = st
	st.Setup = &proto.SetupScript{Version: 200, Shell: "/bin/sh", Script: "echo hello; exit 3"}
	a.maybeSetup(st)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r := a.setup.status(); r != nil && r.Finished != 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	r := a.setup.status()
	if r == nil || r.Version != 200 || r.ExitCode != 3 || r.Output != "hello\n" {
		t.Fatalf("the run: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "setup.json")); err != nil {
		t.Errorf("the result was not kept on disk: %v", err)
	}
	// the same version does not run again
	before := a.setup.status()
	a.maybeSetup(st)
	time.Sleep(100 * time.Millisecond)
	if a.setup.status() != before {
		t.Error("the same version ran again")
	}
}
