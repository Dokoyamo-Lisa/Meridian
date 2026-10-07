package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"meridian/internal/proto"
)

func TestResolveShared(t *testing.T) {
	cfg := `{"port":443,"streamSettings":{"tlsSettings":{"certificates":[{"certificateFile":"` + proto.CertRef(7, "cert") +
		`","keyFile":"` + proto.CertRef(7, "key") + `"}]}}}`
	in := proto.XrayInbound{Tag: "n3", Config: json.RawMessage(cfg), Cert: 7}
	out, msg := resolveShared(in, map[int64]string{7: "abc"})
	cert, key := sharedPaths(7)
	if msg != "" || !strings.Contains(string(out.Config), cert) || !strings.Contains(string(out.Config), key) ||
		strings.Contains(string(out.Config), proto.CertPrefix) {
		t.Fatalf("resolved: %q %s", msg, out.Config)
	}
	if _, msg := resolveShared(in, map[int64]string{}); !strings.Contains(msg, "waiting for shared certificate 7") {
		t.Errorf("not installed: %q", msg)
	}
	// an inbound may only refer to the certificate it names
	sneaky := proto.XrayInbound{Tag: "n4", Config: json.RawMessage(strings.ReplaceAll(cfg, "/7/key", "/8/key")), Cert: 7}
	if _, msg := resolveShared(sneaky, map[int64]string{7: "abc", 8: "def"}); !strings.Contains(msg, "did not ask for") {
		t.Errorf("another certificate: %q", msg)
	}
	if _, ok := leafSHA([]byte("not a cert"), []byte("not a key")); ok {
		t.Error("garbage accepted as a certificate")
	}
}
