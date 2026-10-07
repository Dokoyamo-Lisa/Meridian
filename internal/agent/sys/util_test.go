package sys

import (
	"net"
	"os"
	"testing"
)

func TestEchoParsers(t *testing.T) {
	bodies := []string{
		`{"ipAddress":"203.0.113.7","continentCode":"NA","countryCode":"US","city":"Los Angeles"}`,
		"fl=1f1\nh=1.1.1.1\nip=203.0.113.7\nts=1791370000.1\nvisit_scheme=https\n",
	}
	for i, s := range echoServices {
		if s.url[:8] != "https://" {
			t.Errorf("%s is not HTTPS", s.url)
		}
		if got := s.parse([]byte(bodies[i])); got != "203.0.113.7" {
			t.Errorf("%s: %q", s.url, got)
		}
	}
	if !isPublic(net.ParseIP("203.0.113.7")) || isPublic(net.ParseIP("10.1.2.3")) || isPublic(net.ParseIP("100.64.1.1")) {
		t.Error("isPublic")
	}
}

// TestLookupPublicLive asks the real services; set MERIDIAN_NET_TEST=1 to run it.
func TestLookupPublicLive(t *testing.T) {
	if os.Getenv("MERIDIAN_NET_TEST") == "" {
		t.Skip("set MERIDIAN_NET_TEST=1 to ask the real echo services")
	}
	for _, s := range echoServices {
		if ip := echoIP(t.Context(), s.url, s.parse); ip == "" {
			t.Errorf("%s gave no address", s.url)
		} else {
			t.Logf("%s: %s", s.url, ip)
		}
	}
}
