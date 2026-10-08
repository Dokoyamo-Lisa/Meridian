package realm

import (
	"context"
	"strings"
	"testing"
)

// TestUnsafeTarget: a forward never leads to this server or the cloud's metadata, by address or by a
// name that resolves there.
func TestUnsafeTarget(t *testing.T) {
	ctx := context.Background()
	for target, bad := range map[string]bool{
		"203.0.113.7:443":         false,
		"10.0.0.5:80":             false, // a private backend is what forwards are for
		"127.0.0.1:50000":         true,
		"169.254.169.254:80":      true,
		"[::1]:22":                true,
		"localhost:50000":         true, // resolves to loopback
		"0.0.0.0:80":              true,
		"[fe80::1]:80":            true,
		"no-such-name.invalid:80": false, // does not resolve: realm fails by itself
	} {
		why := unsafeTarget(ctx, target)
		if (why != "") != bad {
			t.Errorf("%s: %q", target, why)
		}
		if bad && !strings.Contains(why, "may not lead") {
			t.Errorf("%s: %q", target, why)
		}
	}
}
