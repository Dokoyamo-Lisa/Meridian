// Package seal authenticates and encrypts panel <-> agent traffic with the server token, so a panel
// reached over plain HTTP still never exposes credentials, keys or user data on the wire.
//
// A token looks like "<server id>.<secret>". The secret never travels: requests carry an HMAC over
// method, path, time, nonce and body hash, and bodies are AES-256-GCM sealed with a key derived from
// the secret.
package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

const (
	HeaderServer = "X-Meridian-Server"
	HeaderTime   = "X-Meridian-Time"
	HeaderNonce  = "X-Meridian-Nonce"
	HeaderSign   = "X-Meridian-Sign"
	ContentType  = "application/x-meridian-sealed"
	MaxSkew      = 300 // seconds
)

// NewSecret returns 32 random bytes, base64url.
func NewSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Token joins a server id and its secret.
func Token(id int64, secret string) string { return strconv.FormatInt(id, 10) + "." + secret }

// ParseToken splits a token into server id and secret.
func ParseToken(token string) (int64, string, error) {
	a, b, ok := strings.Cut(strings.TrimSpace(token), ".")
	if !ok || len(b) < 32 {
		return 0, "", errors.New("malformed token")
	}
	id, err := strconv.ParseInt(a, 10, 64)
	if err != nil || id <= 0 {
		return 0, "", errors.New("malformed token")
	}
	return id, b, nil
}

type Keys struct {
	sign []byte
	aead cipher.AEAD
}

// Derive turns a server secret into a signing key and an encryption key.
func Derive(secret string) (*Keys, error) {
	if secret == "" {
		return nil, errors.New("empty secret")
	}
	sign, err := hkdf.Key(sha256.New, []byte(secret), []byte("meridian"), "agent-sign-v1", 32)
	if err != nil {
		return nil, err
	}
	enc, err := hkdf.Key(sha256.New, []byte(secret), []byte("meridian"), "agent-enc-v1", 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(enc)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Keys{sign: sign, aead: aead}, nil
}

// Seal encrypts plain, binding it to path. Output: nonce || ciphertext.
func (k *Keys) Seal(path string, plain []byte) []byte {
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return k.aead.Seal(nonce, nonce, plain, []byte(path))
}

// Open reverses Seal.
func (k *Keys) Open(path string, data []byte) ([]byte, error) {
	n := k.aead.NonceSize()
	if len(data) < n+k.aead.Overhead() {
		return nil, errors.New("sealed body too short")
	}
	return k.aead.Open(nil, data[:n], data[n:], []byte(path))
}

// Sign returns the request signature.
func (k *Keys) Sign(method, path string, ts int64, nonce string, body []byte) string {
	sum := sha256.Sum256(body)
	m := hmac.New(sha256.New, k.sign)
	m.Write([]byte(method))
	m.Write([]byte{'\n'})
	m.Write([]byte(path))
	m.Write([]byte{'\n'})
	m.Write([]byte(strconv.FormatInt(ts, 10)))
	m.Write([]byte{'\n'})
	m.Write([]byte(nonce))
	m.Write([]byte{'\n'})
	m.Write(sum[:])
	return hex.EncodeToString(m.Sum(nil))
}

// Verify checks a signature in constant time.
func (k *Keys) Verify(method, path string, ts int64, nonce string, body []byte, sig string) bool {
	want := k.Sign(method, path, ts, nonce, body)
	return hmac.Equal([]byte(want), []byte(sig))
}

// ReplyContext binds a sealed answer to the request it answers: the same path and that request's
// own nonce. A recorded answer can therefore never be replayed to a later request, even one for
// the same URL.
func ReplyContext(path, nonce string) string { return path + "\n" + nonce }

// Nonce returns a random request nonce.
func Nonce() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
