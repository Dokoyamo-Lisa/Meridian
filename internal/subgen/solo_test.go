package subgen

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func yamlOf(t *testing.T, m omap) string {
	t.Helper()
	b, err := yaml.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSoloKinds: mieru goes to mihomo and Stash, Snell (version 4) to mihomo, Stash, Surge and
// sing-box; every other app says why not.
func TestSoloKinds(t *testing.T) {
	mieru := Endpoint{Name: "Tokyo · mieru", Kind: KindMieru, Host: "203.0.113.5", Port: 30001, Username: "u7", Password: "p-very-secret-1234", Transport: "tcp"}
	snell := Endpoint{Name: "Tokyo · Snell", Kind: KindSnell, Host: "203.0.113.5", Port: 31001, Password: "psk-very-secret-12345"}
	want := map[string]map[string]bool{
		KindMieru: {"mihomo": true, "stash": true},
		KindSnell: {"mihomo": true, "stash": true, "surge": true, "singbox": true},
	}
	for _, e := range []Endpoint{mieru, snell} {
		for _, s := range Support(e) {
			if s.OK != want[e.Kind][s.App] {
				t.Errorf("%s in %s: ok %v (%s)", e.Kind, s.App, s.OK, s.Why)
			}
			if !s.OK && s.Why == "" {
				t.Errorf("%s in %s: no reason", e.Kind, s.App)
			}
		}
	}
	m, _ := clashProxy(mieru, false)
	if y := yamlOf(t, m); !strings.Contains(y, "type: mieru") || !strings.Contains(y, "transport: TCP") || !strings.Contains(y, "username: u7") {
		t.Errorf("mihomo mieru: %s", y)
	}
	m, _ = clashProxy(mieru, true)
	if y := yamlOf(t, m); !strings.Contains(y, "transport: tcp") || strings.Contains(y, "multiplexing") {
		t.Errorf("Stash mieru: %s", y)
	}
	m, _ = clashProxy(snell, false)
	if y := yamlOf(t, m); !strings.Contains(y, "type: snell") || !strings.Contains(y, "version: 4") || !strings.Contains(y, "psk: psk-very-secret-12345") {
		t.Errorf("mihomo snell: %s", y)
	}
	if l, _, why := surgeLine(snell, 0); why != "" || !strings.Contains(l, "= snell, 203.0.113.5, 31001, psk=psk-very-secret-12345, version=4") {
		t.Errorf("Surge snell: %q %s", l, why)
	}
	if o, _, why := singboxOutbound(snell); why != "" || o == nil {
		t.Errorf("sing-box snell: %s", why)
	}
}
