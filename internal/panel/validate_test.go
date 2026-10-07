package panel

import (
	"strings"
	"testing"
	"time"
)

func TestCleanName(t *testing.T) {
	cases := map[string]string{
		"  Tokyo 1 ":                 "Tokyo 1",
		"evil\nPostUp = rm -rf /":    "evil PostUp = rm -rf /",
		"tab\there\r\nand separator": "tab here and separator",
		"\x00\x07bell":               "bell",
	}
	for in, want := range cases {
		if got := cleanName(in, 64); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := cleanName(strings.Repeat("é", 100), 10); len([]rune(got)) != 10 {
		t.Errorf("length limit counts characters: got %q", got)
	}
}

func TestCleanNoteKeepsLines(t *testing.T) {
	if got := cleanNote("line 1\nline 2\x00\x1b[31m", 100); got != "line 1\nline 2[31m" {
		t.Errorf("got %q", got)
	}
}

func TestNormHost(t *testing.T) {
	ok := map[string]string{
		"Example.COM":                 "example.com",
		"https://panel.example.com/x": "panel.example.com",
		"203.0.113.7":                 "203.0.113.7",
		"203.0.113.7:443":             "203.0.113.7",
		"2001:db8::1":                 "2001:db8::1",
		"[2001:db8::1]:8443":          "2001:db8::1",
		"::ffff:203.0.113.7":          "203.0.113.7",
		"":                            "",
	}
	for in, want := range ok {
		got, err := normHost(in)
		if err != nil || got != want {
			t.Errorf("normHost(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"a b.com", "evil.com\nx: y", "x,y.com", "-bad.com", "host.com;rm", "1.2.3", "fe80::1%eth0", "exa\"mple.com"} {
		if got, err := normHost(bad); err == nil {
			t.Errorf("normHost(%q) accepted as %q", bad, got)
		}
	}
}

func TestRealityTargetMustBePublic(t *testing.T) {
	for _, ok := range []string{"www.apple.com", "www.apple.com:443", "1.1.1.1:443"} {
		if _, err := realityTarget(ok); err != nil {
			t.Errorf("realityTarget(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"127.0.0.1:22", "10.0.0.5:443", "192.168.1.1:443", "169.254.169.254:80", "localhost:443",
		"[::1]:443", "router.lan:443", "100.64.0.1:443", "www.apple.com:0", "www.apple.com:99999"} {
		if _, err := realityTarget(bad); err == nil {
			t.Errorf("realityTarget(%q) accepted", bad)
		}
	}
}

func TestForwardTarget(t *testing.T) {
	if got, err := forwardTarget("203.0.113.7:443", "nft"); err != nil || got != "203.0.113.7:443" {
		t.Errorf("public IP: %q %v", got, err)
	}
	if _, err := forwardTarget("10.0.0.5:3389", "nft"); err != nil {
		t.Errorf("a company LAN target should be allowed: %v", err)
	}
	if _, err := forwardTarget("db.example.com:5432", "realm"); err != nil {
		t.Errorf("realm takes domain names: %v", err)
	}
	for _, c := range []struct{ target, engine string }{
		{"127.0.0.1:22", "realm"}, {"[::1]:22", "nft"}, {"169.254.169.254:80", "realm"}, {"0.0.0.0:80", "nft"},
		{"db.example.com:5432", "nft"}, {"localhost:80", "realm"}, {"203.0.113.7", "nft"}, {"203.0.113.7:70000", "nft"},
	} {
		if _, err := forwardTarget(c.target, c.engine); err == nil {
			t.Errorf("forwardTarget(%q, %s) accepted", c.target, c.engine)
		}
	}
}

func TestNormalizeBlock(t *testing.T) {
	for in, want := range map[string]string{"203.0.113.7": "203.0.113.7", "203.0.113.0/24": "203.0.113.0/24",
		"203.0.113.9/24": "203.0.113.0/24", "2001:db8::/48": "2001:db8::/48"} {
		if got, err := normalizeBlock(in); err != nil || got != want {
			t.Errorf("normalizeBlock(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"0.0.0.0/0", "::/0", "1.0.0.0/7", "127.0.0.1", "::1", "0.0.0.0", "x", "1.2.3.4/33"} {
		if _, err := normalizeBlock(bad); err == nil {
			t.Errorf("normalizeBlock(%q) accepted", bad)
		}
	}
}

func TestSafeBaseURL(t *testing.T) {
	for _, ok := range []string{"https://panel.example.com", "http://203.0.113.7:8080", "https://[2001:db8::1]:8443"} {
		if !safeBaseURL(ok) {
			t.Errorf("rejected %q", ok)
		}
	}
	for _, bad := range []string{"https://panel.example.com/path", "javascript:alert(1)", "https://a.com\"; rm -rf /",
		"https://a.com$(id)", "ftp://a.com", "https://a.com:8080/`id`", "https://a.com\nX: y"} {
		if safeBaseURL(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

// RFC 6238 appendix B, SHA-1, 6 digits (the reference uses 8: we compare the last 6).
func TestTOTP(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" // base32 of "12345678901234567890"
	vectors := map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"}
	for ts, want := range vectors {
		got, err := totpCode(secret, uint64(ts/30))
		if err != nil || got != want {
			t.Errorf("t=%d: got %s, want %s (%v)", ts, got, want, err)
		}
		step, ok := totpMatch(secret, want, time.Unix(ts, 0))
		if !ok || step != ts/30 {
			t.Errorf("t=%d: totpMatch = %d, %v", ts, step, ok)
		}
	}
	if _, ok := totpMatch(secret, "000000", time.Unix(59, 0)); ok {
		t.Error("wrong code accepted")
	}
	if _, ok := totpMatch(secret, "287082", time.Unix(59+120, 0)); ok {
		t.Error("a code four minutes old was accepted")
	}
}

func TestValidPassword(t *testing.T) {
	if validPassword("short") == nil {
		t.Error("short password accepted")
	}
	if validPassword(strings.Repeat("a", 73)) == nil {
		t.Error("password longer than bcrypt's 72 bytes accepted")
	}
	if err := validPassword("correct horse battery"); err != nil {
		t.Error(err)
	}
}

func TestRateLimiter(t *testing.T) {
	l := newRateLimiter()
	for i := 0; i < 3; i++ {
		if !l.allow("k", 3, time.Minute) {
			t.Fatalf("hit %d refused", i)
		}
	}
	if l.allow("k", 3, time.Minute) || !l.exceeded("k", 3, time.Minute) {
		t.Fatal("fourth hit allowed")
	}
	l.reset("k")
	if l.exceeded("k", 3, time.Minute) {
		t.Fatal("reset did not clear")
	}
}
