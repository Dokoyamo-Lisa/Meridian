package solo

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestConfigs: each user's process gets exactly their port and key, in the format its program reads;
// anything that could break a file, a unit name or a command line is refused before it gets there.
func TestConfigs(t *testing.T) {
	e := &Engine{ConfDir: "/etc/meridian-agent/solo"}
	sn := Inst{Kind: "snell", Node: 11, Sub: 2, Port: 31001, Secret: "AbCdEfGhIjKlMnOpQrStUv"}
	path, body := e.config(sn)
	if path != "/etc/meridian-agent/solo/11-2.conf" || body != "[snell-server]\nlisten = ::0:31001\npsk = AbCdEfGhIjKlMnOpQrStUv\nipv6 = false\n" {
		t.Errorf("snell: %s\n%s", path, body)
	}
	if !sn.TCP() || !sn.UDP() {
		t.Error("Snell listens on TCP and UDP")
	}
	mi := Inst{Kind: "mieru", Transport: "udp", Node: 12, Sub: 1, Port: 30000, Name: "u1", Secret: "AbCdEfGhIjKlMnOpQrStUv", Bind: "203.0.113.7"}
	path, body = e.config(mi)
	var cfg struct {
		PortBindings []struct {
			Port     int    `json:"port"`
			Protocol string `json:"protocol"`
		} `json:"portBindings"`
		Users []struct {
			Name, Password string
		} `json:"users"`
		Listen string `json:"listenIPAddress"`
	}
	if err := json.Unmarshal([]byte(body), &cfg); err != nil || path != "/etc/meridian-agent/solo/12-1.json" {
		t.Fatalf("mieru: %v %s", err, path)
	}
	if len(cfg.PortBindings) != 1 || cfg.PortBindings[0].Port != 30000 || cfg.PortBindings[0].Protocol != "UDP" ||
		len(cfg.Users) != 1 || cfg.Users[0].Name != "u1" || cfg.Users[0].Password != mi.Secret || cfg.Listen != "203.0.113.7" {
		t.Errorf("mieru config: %s", body)
	}
	if mi.TCP() || !mi.UDP() {
		t.Error("mieru over UDP listens on UDP only")
	}
	for _, bad := range []Inst{
		{Kind: "ss", Node: 1, Sub: 1, Port: 30000, Secret: strings.Repeat("a", 22)},
		{Kind: "snell", Node: 1, Sub: 1, Port: 80, Secret: strings.Repeat("a", 22)},
		{Kind: "snell", Node: 1, Sub: 1, Port: 30000, Secret: "short"},
		{Kind: "snell", Node: 1, Sub: 1, Port: 30000, Secret: strings.Repeat("a", 20) + "\npsk = x"},
		{Kind: "mieru", Transport: "quic", Node: 1, Sub: 1, Port: 30000, Name: "u1", Secret: strings.Repeat("a", 22)},
		{Kind: "mieru", Transport: "tcp", Node: 1, Sub: 1, Port: 30000, Name: "u\"1", Secret: strings.Repeat("a", 22)},
		{Kind: "snell", Node: 0, Sub: 1, Port: 30000, Secret: strings.Repeat("a", 22)},
		{Kind: "snell", Node: 1, Sub: 1, Port: 30000, Secret: strings.Repeat("a", 22), Bind: "0.0.0.0"},
	} {
		if clean(bad) == nil {
			t.Errorf("accepted: %+v", bad)
		}
	}
	if err := clean(sn); err != nil {
		t.Errorf("refused: %v", err)
	}
	if w := Wanted(nil); len(w) != 0 {
		t.Error("wanted from nothing")
	}
}
