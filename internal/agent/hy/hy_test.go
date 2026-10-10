package hy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"meridian/internal/proto"
)

func TestPruneDevices(t *testing.T) {
	now := int64(10_000_000)
	e := &Engine{devices: map[string]map[string]int64{"1/s1.n1": {}, "1/s2.n1": {"192.0.2.1": now - 8*86400}}}
	for i := 0; i < 40; i++ {
		e.devices["1/s1.n1"][fmt.Sprintf("192.0.2.%d", i)] = now - int64(i)*60
	}
	e.pruneDevices(now)
	if len(e.devices["1/s1.n1"]) != 32 {
		t.Fatalf("kept %d", len(e.devices["1/s1.n1"]))
	}
	if _, ok := e.devices["1/s1.n1"]["192.0.2.0"]; !ok {
		t.Error("the newest address was dropped")
	}
	if _, ok := e.devices["1/s1.n1"]["192.0.2.39"]; ok {
		t.Error("the oldest address was kept")
	}
	if _, ok := e.devices["1/s2.n1"]; ok {
		t.Error("a user unseen for a week is still remembered")
	}
}

// TestRenderCustom: the operator's configuration is merged in; auth and trafficStats stay the agent's.
func TestRenderCustom(t *testing.T) {
	e := &Engine{ConfDir: t.TempDir(), AuthPort: 50001}
	n := proto.HyNode{NodeID: 7, Port: 443, Custom: json.RawMessage(`{"quic":{"maxIdleTimeout":"60s"},
		"auth":{"type":"password","password":"x"},"sniff":{"timeout":"5s"},"obfs":null}`)}
	out, err := e.render(n, portInfo{Port: 1234, Secret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	y := string(out)
	for _, want := range []string{"maxIdleTimeout: 60s", "type: http", "timeout: 5s", "enable: true"} {
		if !strings.Contains(y, want) {
			t.Errorf("missing %q in:\n%s", want, y)
		}
	}
	if strings.Contains(y, "password") {
		t.Errorf("the operator's auth replaced the agent's:\n%s", y)
	}
}

// TestRenderACL: the private-address rejects stay first whatever the protocol's own acl says; a rules
// file is refused.
func TestRenderACL(t *testing.T) {
	e := &Engine{ConfDir: t.TempDir(), AuthPort: 50001}
	for name, custom := range map[string]string{
		"none":   ``,
		"inline": `{"outbounds":[{"name":"warp","type":"socks5","socks5":{"addr":"127.0.0.1:40000"}}],"acl":{"inline":["warp(geosite:openai)","direct(all)"]}}`,
		"null":   `{"acl":null}`,
		"empty":  `{"acl":{"inline":[]}}`,
	} {
		n := proto.HyNode{NodeID: 7, Port: 443}
		if custom != "" {
			n.Custom = json.RawMessage(custom)
		}
		out, err := e.render(n, portInfo{Port: 1234, Secret: "s"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var cfg struct {
			ACL struct {
				Inline []string `yaml:"inline"`
			} `yaml:"acl"`
		}
		if err := yaml.Unmarshal(out, &cfg); err != nil {
			t.Fatal(err)
		}
		got := cfg.ACL.Inline
		if len(got) < len(privateRejects)+1 || !slices.Equal(got[:len(privateRejects)], privateRejects) {
			t.Errorf("%s: the rejects are not first: %v", name, got)
		}
		if name == "inline" && !slices.Contains(got, "warp(geosite:openai)") {
			t.Errorf("%s: the protocol's own rules are gone: %v", name, got)
		}
		if name != "inline" && got[len(got)-1] != "direct(all)" {
			t.Errorf("%s: no default: %v", name, got)
		}
	}
	if _, err := e.render(proto.HyNode{NodeID: 7, Port: 443, Custom: json.RawMessage(`{"acl":{"file":"/etc/acl.txt"}}`)},
		portInfo{Port: 1234}); err == nil || !strings.Contains(err.Error(), "acl.inline") {
		t.Errorf("acl.file: %v", err)
	}
}

// TestLogsAcrossRestarts: a restarted agent goes on reading a node's request log where the last one
// stopped; with no record of that, what is in the log was read already.
func TestLogsAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	line := func(host string) string {
		return `{"msg":"TCP request","id":"s1.n7","reqAddr":"` + host + `:443"}` + "\n"
	}
	path := dir + "/hy2-7.log"
	if err := os.WriteFile(path, []byte(line("old.example")+line("old.example")), 0o600); err != nil {
		t.Fatal(err)
	}
	e := &Engine{RunDir: dir}
	e.RestoreLogs(nil) // an agent before this one read it
	if d := e.readLog(7, true); len(d) != 0 {
		t.Fatalf("read again: %v", d)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(line("new.example"))
	f.Close()
	d := e.readLog(7, true)
	if len(d) != 1 || d[0].Host != "new.example" || d[0].Conns != 1 {
		t.Fatalf("new lines: %v", d)
	}
	// the next agent continues from the saved position
	pos := e.LogPos()
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(line("later.example"))
	f.Close()
	next := &Engine{RunDir: dir}
	next.RestoreLogs(pos)
	if d := next.readLog(7, true); len(d) != 1 || d[0].Host != "later.example" {
		t.Fatalf("after a restart: %v", d)
	}
}

// TestFailure: the reason a node stopped is the error its log ends with; a log that goes on after an
// error (the node started again and runs) gives none.
func TestFailure(t *testing.T) {
	up := `{"level":"info","time":1791435233987.745,"msg":"server mode"}
{"level":"info","time":1791435233990.2134,"msg":"server up and running","listen":":443"}
`
	refused := `{"level":"info","time":1791435310234.6348,"msg":"server mode"}
{"level":"fatal","time":1791435310234.7761,"msg":"failed to load server config","error":"invalid config: tls.cert: stat /nonexist.crt: no such file or directory"}
`
	for _, c := range []struct{ log, want string }{
		{up, ""},
		{"", ""},
		{refused, "failed to load server config: invalid config: tls.cert: stat /nonexist.crt: no such file or directory"},
		{refused + refused, "failed to load server config: invalid config: tls.cert: stat /nonexist.crt: no such file or directory"},
		{refused + up, ""},
		{up + "panic: runtime error: invalid memory address\n\ngoroutine 1 [running]:\nmain.main()\n", "it crashed: runtime error: invalid memory address"},
		{"panic: runtime error: invalid memory address\n\ngoroutine 1 [running]:\nmain.main()\n" + up, ""}, // started again since
		{`{"level":"fatal","msg":"failed to serve","error":"` + strings.Repeat("x", 400) + `"}`, "failed to serve: " + strings.Repeat("x", 300-len("failed to serve: ")) + "…"},
	} {
		if got := failureIn([]byte(c.log)); got != c.want {
			t.Errorf("%q: got %q, want %q", c.log, got, c.want)
		}
	}

	dir := t.TempDir()
	e := &Engine{RunDir: dir}
	if got := e.failure(7); got != "" {
		t.Errorf("no log: %q", got)
	}
	big := strings.Repeat(`{"level":"debug","msg":"TCP request","id":"1-2","reqAddr":"example.com:443"}`+"\n", 1000) + refused
	if err := os.WriteFile(filepath.Join(dir, "hy2-7.log"), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := e.failure(7); !strings.HasPrefix(got, "failed to load server config") {
		t.Errorf("long log: %q", got)
	}
}

// TestSpeedSignIn: a user with a speed limit gets in only with an app that declares a rate within it
// (with a little room); without a limit any rate - or none - is fine. A lower limit marks the user's
// sessions to sign in again; a higher one does not.
func TestSpeedSignIn(t *testing.T) {
	e := &Engine{}
	e.init()
	e.users[2] = map[string]string{"pw-a": proto.Email(5, 2), "pw-b": proto.Email(6, 2)}
	e.SetSpeed([]proto.SpeedLimit{{Sub: 5, Mbps: 300}})
	for _, c := range []struct {
		id   string
		tx   uint64
		over bool
	}{
		{proto.Email(5, 2), 0, true},                          // no declared rate: stalls under the limit
		{proto.Email(5, 2), 285 * 125_000, false},             // what the link declares
		{proto.Email(5, 2), 300 * 125_000 * 104 / 100, false}, // apps round
		{proto.Email(5, 2), 400 * 125_000, true},              // more than the limit
		{proto.Email(6, 2), 0, false},                         // no limit: BBR is fine
		{proto.Email(6, 2), 900 * 125_000, false},
		{"p7", 0, false}, // a proxy pass
	} {
		e.mu.Lock()
		over := e.overLimit(c.id, c.tx)
		e.mu.Unlock()
		if over != c.over {
			t.Errorf("%s at %d B/s: over %v, want %v", c.id, c.tx, over, c.over)
		}
	}
	if maxTx(300) != 39_375_000 {
		t.Errorf("maxTx(300) = %d", maxTx(300))
	}
}
