package cores

import (
	"strings"
	"testing"
)

func TestCheckVersion(t *testing.T) {
	for _, ok := range []string{"26.3.27", "2.13.0", "2.9.6", "1.0"} {
		if checkVersion("x", ok) != nil {
			t.Errorf("rejected %q", ok)
		}
	}
	for _, bad := range []string{"", "../../etc", "26.3.27/../../x", "v26.3.27", "26", "1.2.3.4.5", "26.3.27\n"} {
		if checkVersion("x", bad) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestParseChecksums(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	if got := parseDgst([]byte("MD5= 123\nSHA1= 456\nSHA2-256= " + strings.ToUpper(sum) + "\nSHA2-512= 789\n")); got != sum {
		t.Errorf("parseDgst = %q", got)
	}
	if got := parseDgst([]byte("SHA2-256= short")); got != "" {
		t.Errorf("parseDgst accepted a short digest: %q", got)
	}
	hashes := []byte(sum + "  build/hysteria-linux-amd64\n" + strings.Repeat("cd", 32) + "  build/hysteria-linux-arm64\n")
	if got := parseHashes(hashes, "hysteria-linux-amd64"); got != sum {
		t.Errorf("parseHashes = %q", got)
	}
	if got := parseHashes(hashes, "hysteria-linux-mips"); got != "" {
		t.Errorf("parseHashes found a missing asset: %q", got)
	}
}

func TestTrustedDigests(t *testing.T) {
	sum := strings.Repeat("ef", 32)
	SetDigests(map[string]string{"xray/26.3.27/Xray-linux-64.zip": "sha256:" + strings.ToUpper(sum), "bad/1.0/x": "nope"})
	if got := digestFor("xray", "26.3.27", "Xray-linux-64.zip"); got != sum {
		t.Errorf("digestFor = %q", got)
	}
	if got := digestFor("bad", "1.0", "x"); got != "" {
		t.Errorf("a malformed digest was trusted: %q", got)
	}
	if checkSum([]byte("data"), sum, "test") == nil {
		t.Error("checkSum accepted the wrong data")
	}
	if checkSum([]byte("data"), "", "test") == nil {
		t.Error("checkSum accepted a missing digest")
	}
	SetDigests(nil)
}
