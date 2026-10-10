package panel

import (
	"strings"
	"testing"
)

// The note for servers with IPv6 only when the panel's address has no IPv6: at an IPv4 address it
// asks for a domain name, at a name for that name's AAAA record - and never for a relay, which a
// server learns of only from a panel it has reached.
func TestPanelIPv6Note(t *testing.T) {
	h := newHarness(t)
	c := h.browser()
	c.login("owner", "owner-password-1")
	set := c.must("GET", "/api/settings", nil, 200)
	set["public_url"] = "https://203.0.113.9"
	c.must("PUT", "/api/settings", set, 200)
	note, _ := c.must("GET", "/api/panel-address", nil, 200)["note"].(string)
	if !strings.Contains(note, "IPv4 address 203.0.113.9") || !strings.Contains(note, "domain name with an AAAA record") || strings.Contains(note, "relay") {
		t.Errorf("at an IPv4 address: %q", note)
	}
	set["public_url"] = "https://[2001:db8::9]"
	c.must("PUT", "/api/settings", set, 200)
	if note, _ := c.must("GET", "/api/panel-address", nil, 200)["note"].(string); note != "" {
		t.Errorf("at an IPv6 address: %q", note)
	}
	if n := noIPv6Note("panel.example.com", "this server cannot reach the panel"); !strings.Contains(n, "Give panel.example.com an AAAA record") || strings.Contains(n, "relay") {
		t.Errorf("at a name: %q", n)
	}
}
