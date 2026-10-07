package xray

import "testing"

func TestFailedLine(t *testing.T) {
	warn := "adding: n11\n2026/10/07 04:40:21 [Warning] common/errors: The feature VMess (with no Forward Secrecy, etc.) is deprecated\n{}"
	if f := failedLine(warn); f != "" {
		t.Errorf("a warning counted as a failure: %q", f)
	}
	bad := "adding: n11\nfailed to add inbound: rpc error: code = Unknown desc = app/proxyman/inbound: existing tag found: n11"
	if f := failedLine(bad); f == "" {
		t.Error("a failure was missed")
	}
}
