package agent

import (
	"os"
	"path/filepath"
	"testing"

	"meridian/internal/proto"
)

// TestPreparePolicy: the cores a server lacks are installed by themselves on a new server only (or
// when the supervisor asks); a server that ran before shows them as missing - except a core it had,
// whose program went missing, and one a protocol needs. Whether installing is allowed survives a
// restart, until every core is in place.
func TestPreparePolicy(t *testing.T) {
	dir := t.TempDir()
	old := DataDir
	DataDir = dir
	defer func() { DataDir = old }()
	st := &proto.State{Cores: proto.Cores{Xray: "26.3.27", Hysteria: "2.6.5"}}

	// a server that ran before, with nothing installed and no protocol: nothing by itself
	var p preparer
	p.load(false)
	for _, c := range prepCores(st) {
		if p.may(c) {
			t.Errorf("%s is installed without asking on a server that ran before", c.key)
		}
	}
	// a protocol that needs a core: that one is installed
	st.Xray = &proto.Xray{Inbounds: []proto.XrayInbound{{Tag: "n1"}}}
	if cs := prepCores(st); !p.may(cs[0]) || p.may(cs[1]) {
		t.Errorf("a protocol needs Xray: may %v, Hysteria may %v", p.may(cs[0]), p.may(cs[1]))
	}
	st.Xray = nil
	// a core the server had (its link is there, its program is not): installed again
	if err := os.MkdirAll(filepath.Join(dir, "cores", "hysteria"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "cores", "hysteria", "2.6.1"), filepath.Join(dir, "cores", "hysteria", "current")); err != nil {
		t.Fatal(err)
	}
	cs := prepCores(st)
	if !cs[1].had || cs[1].version != "2.6.1" || !p.may(cs[1]) {
		t.Errorf("a core the server had: %+v, may %v", cs[1], p.may(cs[1]))
	}

	// a new server: everything; that holds after a restart, until the cores are in place
	var fresh preparer
	fresh.load(true)
	if !fresh.may(prepCores(st)[0]) {
		t.Error("a new server does not get its cores by itself")
	}
	var again preparer
	again.load(false)
	if !again.install {
		t.Error("a restart forgot that the cores may be installed")
	}
	again.mu.Lock()
	again.install = false
	again.persist()
	again.mu.Unlock()
	if _, err := os.Stat(prepPath()); !os.IsNotExist(err) {
		t.Errorf("the file stays once the cores are in place: %v", err)
	}
}

// TestPreparedFirst: what runs after the cores (the setup script) waits until the preparation ran to
// its end in this agent's run, without a failed step.
func TestPreparedFirst(t *testing.T) {
	var p preparer
	if p.ready() {
		t.Error("ready before the preparation ran")
	}
	p.st = &proto.Prepare{Started: 1, Steps: []proto.PrepStep{{Key: "xray", State: proto.PrepRunning}}}
	p.running = true
	if p.ready() {
		t.Error("ready while it runs")
	}
	p.running = false
	p.st.Finished = 2
	p.st.Steps[0].State = proto.PrepFailed
	if p.ready() {
		t.Error("ready with a failed step")
	}
	p.st.Steps[0].State = proto.PrepDone
	p.st.Steps = append(p.st.Steps, proto.PrepStep{Key: "wireguard", State: proto.PrepMissing})
	if !p.ready() {
		t.Error("not ready although the cores are in place (what the host lacks does not hold it up)")
	}
}
