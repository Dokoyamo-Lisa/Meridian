package panel

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

// vAuth is a passkey on a made-up device: a P-256 key that signs what the WebAuthn specification
// says an authenticator signs, with "none" attestation - as a phone's or a password manager's would.
type vAuth struct {
	key    *ecdsa.PrivateKey
	id     []byte
	handle []byte // the account's handle, from the registration options
	origin string
	rpID   string
	count  uint32
}

const (
	flagUP = 0x01
	flagUV = 0x04
	flagBE = 0x08
	flagBS = 0x10
	flagAT = 0x40
)

func newVAuth(t *testing.T, origin, rpID string) *vAuth {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &vAuth{key: k, id: id, origin: origin, rpID: rpID}
}

func (v *vAuth) authData(flags byte, attested bool) []byte {
	h := sha256.Sum256([]byte(v.rpID))
	b := append(append([]byte{}, h[:]...), flags)
	b = binary.BigEndian.AppendUint32(b, v.count)
	if attested {
		b = append(b, make([]byte, 16)...) // AAGUID
		b = binary.BigEndian.AppendUint16(b, uint16(len(v.id)))
		b = append(b, v.id...)
		pt, _ := v.key.PublicKey.Bytes() // 0x04, X, Y
		cose, _ := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: pt[1:33], -3: pt[33:65]})
		b = append(b, cose...)
	}
	return b
}

func challengeOf(t *testing.T, begun map[string]any) string {
	t.Helper()
	pk, _ := begun["options"].(map[string]any)["publicKey"].(map[string]any)
	ch, _ := pk["challenge"].(string)
	if ch == "" {
		t.Fatalf("no challenge in %v", begun)
	}
	return ch
}

func (v *vAuth) register(t *testing.T, begun map[string]any) map[string]any {
	t.Helper()
	pk := begun["options"].(map[string]any)["publicKey"].(map[string]any)
	h, err := b64url.DecodeString(pk["user"].(map[string]any)["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	v.handle = h
	cd, _ := json.Marshal(map[string]any{"type": "webauthn.create", "challenge": challengeOf(t, begun), "origin": v.origin, "crossOrigin": false})
	att, _ := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": v.authData(flagUP|flagUV|flagBE|flagBS|flagAT, true)})
	return map[string]any{"id": b64url.EncodeToString(v.id), "rawId": b64url.EncodeToString(v.id), "type": "public-key",
		"authenticatorAttachment": "platform", "clientExtensionResults": map[string]any{},
		"response": map[string]any{"clientDataJSON": b64url.EncodeToString(cd), "attestationObject": b64url.EncodeToString(att), "transports": []string{"internal"}}}
}

func (v *vAuth) assert(t *testing.T, challenge string, uv bool) map[string]any {
	t.Helper()
	v.count++
	flags := byte(flagUP | flagBE | flagBS)
	if uv {
		flags |= flagUV
	}
	cd, _ := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": challenge, "origin": v.origin, "crossOrigin": false})
	ad := v.authData(flags, false)
	sum := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), sum[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, v.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"id": b64url.EncodeToString(v.id), "rawId": b64url.EncodeToString(v.id), "type": "public-key",
		"clientExtensionResults": map[string]any{}, "response": map[string]any{"clientDataJSON": b64url.EncodeToString(cd),
			"authenticatorData": b64url.EncodeToString(ad), "signature": b64url.EncodeToString(sig), "userHandle": b64url.EncodeToString(v.handle)}}
}

// passkeyLogin signs in with a passkey from an address (the harness trusts loopback's X-Forwarded-For).
func passkeyLogin(t *testing.T, h *harness, c *client, v *vAuth, ip string, uv bool) (int, string) {
	t.Helper()
	post := func(path string, body any) (int, map[string]any, string) {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", h.srv.URL+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Meridian", "1")
		if ip != "" {
			req.Header.Set("X-Forwarded-For", ip)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		return resp.StatusCode, m, string(raw)
	}
	code, begun, raw := post("/api/login/passkey/begin", nil)
	if code != 200 {
		return code, raw
	}
	code, _, raw = post("/api/login/passkey/finish", map[string]any{"ceremony": begun["ceremony"], "credential": v.assert(t, challengeOf(t, begun), uv)})
	return code, raw
}

// TestPasskeys: the supervisor adds a passkey from a signed-in browser and signs in with it - without
// a two-factor code - and only with a real answer to a fresh challenge from this panel's origin, with
// the person verified; a copied passkey (its counter going back), a turned-off account, a removed
// passkey, API tokens and IP addresses are refused; a banned network never holds a passkey back.
func TestPasskeys(t *testing.T) {
	h := newHarness(t)
	h.srv.URL = strings.Replace(h.srv.URL, "127.0.0.1", "localhost", 1) // passkeys need a name, not an IP
	b := h.browser()
	b.login("owner", "owner-password-1")
	v := newVAuth(t, h.srv.URL, "localhost")

	begun := b.must("POST", "/api/me/passkeys/begin", nil, 200)
	added := b.must("POST", "/api/me/passkeys/finish", map[string]any{"ceremony": begun["ceremony"], "name": "Laptop", "credential": v.register(t, begun)}, 201)
	if added["name"] != "Laptop" || added["synced"] != true {
		t.Fatalf("added: %v", added)
	}
	if code, _, _ := b.do("POST", "/api/me/passkeys/finish", map[string]any{"ceremony": begun["ceremony"], "name": "Again", "credential": v.register(t, begun)}); code != 400 {
		t.Errorf("a used registration ceremony: %d", code)
	}
	var list []map[string]any
	_, _, raw := b.do("GET", "/api/me/passkeys", nil)
	_ = json.Unmarshal(raw, &list)
	if len(list) != 1 || list[0]["name"] != "Laptop" {
		t.Fatalf("list: %s", raw)
	}

	// API tokens cannot see or add passkeys
	tok := b.must("POST", "/api/tokens", map[string]any{"name": "t", "scope": "full"}, 201)["token"].(string)
	if code, _, _ := h.bearer(tok).do("GET", "/api/me/passkeys", nil); code != http.StatusForbidden {
		t.Errorf("a token listed passkeys: %d", code)
	}

	// two-factor sign-in on: the password needs a code, the passkey does not
	if _, err := h.p.db.Exec1(`UPDATE accounts SET totp_secret = 'JBSWY3DPEHPK3PXP' WHERE username = 'owner'`); err != nil {
		t.Fatal(err)
	}
	if _, m, _ := h.browser().do("POST", "/api/login", map[string]string{"username": "owner", "password": "owner-password-1"}); m["totp_required"] != true {
		t.Errorf("a password sign-in without a code: %v", m)
	}
	c := h.browser()
	if code, raw := passkeyLogin(t, h, c, v, "", true); code != 200 || !strings.Contains(raw, `"kind":"admin"`) {
		t.Fatalf("passkey sign-in: %d %s", code, raw)
	}
	c.must("GET", "/api/me", nil, 200)
	var via string
	h.p.db.QueryRow(`SELECT via FROM sessions ORDER BY created_at DESC, rowid DESC LIMIT 1`).Scan(&via)
	if via != "passkey" {
		t.Errorf("the session was made %q", via)
	}

	// an answer must be fresh, from this origin, with the person verified
	other := h.browser()
	begun = other.must("POST", "/api/login/passkey/begin", nil, 200)
	stale := v.assert(t, challengeOf(t, other.must("POST", "/api/login/passkey/begin", nil, 200)), true)
	if code, _, _ := other.do("POST", "/api/login/passkey/finish", map[string]any{"ceremony": begun["ceremony"], "credential": stale}); code != http.StatusUnauthorized {
		t.Errorf("an answer to another challenge: %d", code)
	}
	if code, _, _ := other.do("POST", "/api/login/passkey/finish", map[string]any{"ceremony": begun["ceremony"], "credential": stale}); code != http.StatusBadRequest {
		t.Errorf("a used sign-in ceremony: %d", code)
	}
	elsewhere := *v
	elsewhere.origin = "https://phish.example.com"
	if code, _ := passkeyLogin(t, h, h.browser(), &elsewhere, "", true); code != http.StatusUnauthorized {
		t.Errorf("another origin: %d", code)
	}
	v.count = elsewhere.count
	if code, _ := passkeyLogin(t, h, h.browser(), v, "", false); code != http.StatusUnauthorized {
		t.Errorf("without user verification: %d", code)
	}

	// a banned network: the password is held back, the passkey is not
	for i := 0; i < guardNetFails; i++ {
		h.p.signin.failed(fmt.Sprintf("203.0.113.%d", 10+i), fmt.Sprint("x", i), false, now())
	}
	if code, _, _ := loginFrom(t, h, "203.0.113.200", "owner", "owner-password-1", ""); code != http.StatusTooManyRequests {
		t.Errorf("a password from the banned network: %d", code)
	}
	if code, raw := passkeyLogin(t, h, h.browser(), v, "203.0.113.201", true); code != 200 {
		t.Errorf("a passkey from the banned network: %d %s", code, raw)
	}

	// a counter that goes back: the passkey may have been copied
	v.count = 0
	if code, _ := passkeyLogin(t, h, h.browser(), v, "", true); code != http.StatusUnauthorized {
		t.Errorf("a counter going back: %d", code)
	}
	var n int
	h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind = 'login_failed' AND message LIKE '%counted backwards%'`).Scan(&n)
	if n != 1 {
		t.Errorf("copy events: %d", n)
	}
	v.count = 100

	// a turned-off account
	h.p.db.Exec1(`UPDATE accounts SET enabled = 0 WHERE username = 'owner'`)
	if code, _ := passkeyLogin(t, h, h.browser(), v, "", true); code != http.StatusUnauthorized {
		t.Errorf("a turned-off account: %d", code)
	}
	h.p.db.Exec1(`UPDATE accounts SET enabled = 1 WHERE username = 'owner'`)

	// at an IP address, or another name than the panel's: no passkeys
	ipURL := strings.Replace(h.srv.URL, "localhost", "127.0.0.1", 1)
	req, _ := http.NewRequest("POST", ipURL+"/api/login/passkey/begin", nil)
	req.Header.Set("X-Meridian", "1")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 400 {
		t.Errorf("at an IP address: %v %v", err, resp)
	} else {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(raw), "IP address") {
			t.Errorf("at an IP address: %s", raw)
		}
	}
	set := c.must("GET", "/api/settings", nil, 200)
	set["public_url"] = "https://panel.example.com"
	c.must("PUT", "/api/settings", set, 200)
	if code, _, raw := c.do("POST", "/api/me/passkeys/begin", nil); code != 400 || !strings.Contains(string(raw), "https://panel.example.com") {
		t.Errorf("at another name than the panel's: %d %s", code, raw)
	}
	set["public_url"] = ""
	c.must("PUT", "/api/settings", set, 200)

	// removed: it signs in no more
	c.must("DELETE", fmt.Sprintf("/api/me/passkeys/%d", id(added["id"])), nil, 200)
	if code, _ := passkeyLogin(t, h, h.browser(), v, "", true); code != http.StatusUnauthorized {
		t.Errorf("a removed passkey: %d", code)
	}
	if testing.Verbose() {
		rows, _ := h.p.db.Query(`SELECT message FROM events WHERE kind = 'login_failed' ORDER BY id`)
		for rows.Next() {
			var m string
			rows.Scan(&m)
			t.Log("REASON:", m)
		}
		rows.Close()
	}
	h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind IN ('passkey_added', 'passkey_removed')`).Scan(&n)
	if n != 2 {
		t.Errorf("passkey events: %d", n)
	}
}
