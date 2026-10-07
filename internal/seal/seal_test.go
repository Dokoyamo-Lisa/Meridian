package seal

import (
	"bytes"
	"testing"
)

func keys(t *testing.T) *Keys {
	t.Helper()
	k, err := Derive(NewSecret())
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealRoundTrip(t *testing.T) {
	k := keys(t)
	plain := []byte(`{"hello":"world"}`)
	box := k.Seal("/agent/v1/report", plain)
	got, err := k.Open("/agent/v1/report", box)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("round trip failed: %v %q", err, got)
	}
}

func TestSealIsBoundToContext(t *testing.T) {
	k := keys(t)
	box := k.Seal(ReplyContext("/agent/v1/state?rev=a&wait=50", "nonce-1"), []byte("state"))
	if _, err := k.Open(ReplyContext("/agent/v1/state?rev=a&wait=50", "nonce-2"), box); err == nil {
		t.Fatal("a reply sealed for one request must not open for another request to the same URL")
	}
	if _, err := k.Open("/agent/v1/state?rev=b&wait=50\nnonce-1", box); err == nil {
		t.Fatal("a reply must not open for a different path")
	}
}

func TestSealRejectsTampering(t *testing.T) {
	k := keys(t)
	box := k.Seal("/p", []byte("payload"))
	for i := range box {
		bad := append([]byte(nil), box...)
		bad[i] ^= 0x01
		if _, err := k.Open("/p", bad); err == nil {
			t.Fatalf("flipping byte %d was not detected", i)
		}
	}
	if _, err := k.Open("/p", box[:5]); err == nil {
		t.Fatal("a truncated box must not open")
	}
}

func TestOtherSecretCannotOpen(t *testing.T) {
	box := keys(t).Seal("/p", []byte("payload"))
	if _, err := keys(t).Open("/p", box); err == nil {
		t.Fatal("another server's key opened the box")
	}
}

func TestSignVerify(t *testing.T) {
	k := keys(t)
	sig := k.Sign("POST", "/agent/v1/report", 1700000000, "n1", []byte("body"))
	if !k.Verify("POST", "/agent/v1/report", 1700000000, "n1", []byte("body"), sig) {
		t.Fatal("valid signature rejected")
	}
	cases := []struct {
		method, path string
		ts           int64
		nonce, body  string
	}{
		{"GET", "/agent/v1/report", 1700000000, "n1", "body"},
		{"POST", "/agent/v1/state", 1700000000, "n1", "body"},
		{"POST", "/agent/v1/report", 1700000001, "n1", "body"},
		{"POST", "/agent/v1/report", 1700000000, "n2", "body"},
		{"POST", "/agent/v1/report", 1700000000, "n1", "bodY"},
	}
	for _, c := range cases {
		if k.Verify(c.method, c.path, c.ts, c.nonce, []byte(c.body), sig) {
			t.Fatalf("signature accepted for altered request %+v", c)
		}
	}
	if keys(t).Verify("POST", "/agent/v1/report", 1700000000, "n1", []byte("body"), sig) {
		t.Fatal("another key verified the signature")
	}
}

func TestParseToken(t *testing.T) {
	s := NewSecret()
	id, secret, err := ParseToken(Token(42, s))
	if err != nil || id != 42 || secret != s {
		t.Fatalf("got %d %q %v", id, secret, err)
	}
	for _, bad := range []string{"", "42", "42.short", "x." + s, "-1." + s, "0." + s} {
		if _, _, err := ParseToken(bad); err == nil {
			t.Errorf("accepted malformed token %q", bad)
		}
	}
}

func TestNonceAndSecretAreRandom(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		n := Nonce()
		if seen[n] {
			t.Fatal("nonce repeated")
		}
		seen[n] = true
	}
	if a, b := NewSecret(), NewSecret(); a == b {
		t.Fatal("secret repeated")
	}
}
