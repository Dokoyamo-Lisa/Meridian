package agent

import (
	"encoding/json"
	"testing"

	"meridian/internal/agent/hy"
	"meridian/internal/proto"
)

// TestHealthOwn: the health check knows Meridian's ports from the state - protocols, WireGuard,
// forwards, the agent's own and Let's Encrypt's check - and waits while there is no state.
func TestHealthOwn(t *testing.T) {
	a := &Agent{cfg: &Config{APIPort: 50000}, hy: &hy.Engine{AuthPort: 50001}}
	if _, ok := a.healthOwn(); ok {
		t.Fatal("no state, yet the ports are known")
	}
	a.state = &proto.State{
		Xray: &proto.Xray{Inbounds: []proto.XrayInbound{
			{Tag: "n1", Config: json.RawMessage(`{"port":443,"protocol":"vless"}`), ACME: "a.example.com"},
			{Tag: "n2", Config: json.RawMessage(`{"port":8388,"protocol":"shadowsocks"}`)},
		}},
		Hysteria:  []proto.HyNode{{NodeID: 3, Port: 8443}},
		WireGuard: []proto.WGInterface{{NodeID: 4, Name: "uwg4", ListenPort: 51820}},
		Forwards:  []proto.Forward{{ID: 1, ListenPort: 9000, Network: "tcp+udp"}, {ID: 2, ListenPort: 9001, Network: "udp"}},
		Agent:     proto.AgentSettings{ACMEPort: 20080},
		Cores:     proto.Cores{Digests: map[string]string{"hysteria/2.13.0/hysteria-linux-amd64": "ab"}},
	}
	own, ok := a.healthOwn()
	if !ok {
		t.Fatal("state, but no ports")
	}
	for _, k := range []string{"tcp:443", "tcp:8388", "udp:8388", "udp:8443", "udp:51820", "tcp:9000", "udp:9000", "udp:9001",
		"tcp:50000", "tcp:50001", "tcp:80", "tcp:20080"} {
		if !own.Ports[k] {
			t.Errorf("%s is not Meridian's", k)
		}
	}
	if own.Ports["tcp:9001"] || own.Ports["tcp:22"] {
		t.Errorf("too many: %v", own.Ports)
	}
	if own.Data != DataDir || own.Agent != BinPath || own.Digests["hysteria/2.13.0/hysteria-linux-amd64"] != "ab" {
		t.Errorf("own: %+v", own)
	}
	if n := carriedBytes([]proto.UserTraffic{{Up: 10, Down: 20}, {Up: -5}}, []proto.FwdTraffic{{Up: 1, Down: 2}}); n != 33 {
		t.Errorf("carried %d", n)
	}
}
