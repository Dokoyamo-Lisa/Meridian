package panel

// VLESS Encryption ("mlkem768x25519plus", Xray 25.9 and later): VLESS encrypts by itself with a
// post-quantum key exchange (ML-KEM-768 together with X25519) and an authenticated record layer, so
// it also works without TLS, and under TLS or REALITY it adds a layer that whatever ends the TLS (a
// CDN) cannot read. The server holds a private key - its "decryption" - and clients the matching
// public key - their "encryption"; the panel makes both exactly as `xray vlessenc` prints them
// (Xray-core main/commands/all/vlessenc.go, infra/conf/vless.go).

import (
	"crypto/ecdh"
	"crypto/mlkem"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"meridian/internal/subgen"
)

const (
	encNative = "native" // the record layer as it is (with TLS or REALITY)
	encXorPub = "xorpub" // the same, with the public key part disguised
	encRandom = "random" // everything looks random, like Shadowsocks (without TLS)

	encAuthX25519 = "x25519"   // short keys; the key exchange is post-quantum either way
	encAuthMLKEM  = "mlkem768" // the server proves itself post-quantum too; links grow by about 1.6 KB

	encHandshake = "mlkem768x25519plus"
	encTickets   = "600s" // the server allows 0-RTT resumption for up to ten minutes (as xray vlessenc)
	encResume    = "0rtt" // clients resume where the server allows it
)

var (
	encModes = []string{encNative, encXorPub, encRandom}
	encAuths = []string{encAuthX25519, encAuthMLKEM}
)

// vlessEncKeys makes a new key pair for an authentication: the server's key (an X25519 private key or
// an ML-KEM-768 seed) and the client's (the X25519 public key or the ML-KEM-768 encapsulation key),
// both base64url without padding.
func vlessEncKeys(auth string) (server, client string, err error) {
	switch auth {
	case encAuthX25519:
		server, client = x25519Pair(base64.RawURLEncoding)
		return server, client, nil
	case encAuthMLKEM:
		seed := make([]byte, mlkem.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return "", "", err
		}
		server = base64.RawURLEncoding.EncodeToString(seed)
		client, err = vlessEncClientKey(server)
		return server, client, err
	}
	return "", "", fmt.Errorf("unknown VLESS Encryption check %q", auth)
}

// vlessEncClientKey derives the client's key from the server's: it says whether two keys belong
// together.
func vlessEncClientKey(server string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(server)
	if err != nil {
		return "", err
	}
	switch len(b) {
	case 32:
		k, err := ecdh.X25519().NewPrivateKey(b)
		if err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()), nil
	case mlkem.SeedSize:
		k, err := mlkem.NewDecapsulationKey768(b)
		if err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(k.EncapsulationKey().Bytes()), nil
	}
	return "", errors.New("a VLESS Encryption key is 32 bytes (X25519) or a 64-byte ML-KEM-768 seed")
}

// encAuthOf names the authentication a server key belongs to.
func encAuthOf(server string) string {
	if b, err := base64.RawURLEncoding.DecodeString(server); err == nil && len(b) == mlkem.SeedSize {
		return encAuthMLKEM
	}
	return encAuthX25519
}

// decryption is the VLESS inbound's "decryption": the server's side, with its private key.
func (s *xraySettings) decryption() string {
	if s.Encryption == "" {
		return "none"
	}
	return strings.Join([]string{encHandshake, s.Encryption, encTickets, s.EncKey}, ".")
}

// encryption is what clients put in their VLESS "encryption": the public side, "" when off.
func (s *xraySettings) encryption() string {
	if s.Encryption == "" {
		return ""
	}
	return strings.Join([]string{encHandshake, s.Encryption, encResume, s.EncClient}, ".")
}

// applyEncryption merges the VLESS Encryption input and makes keys where they are needed: new ones
// when it is turned on or its authentication changes. Off - or not VLESS - leaves nothing behind.
func (s *xraySettings) applyEncryption(kind string, in *protoInput) error {
	if in.Encryption != nil {
		m := trimLower(in.Encryption)
		if m == "none" || m == "off" {
			m = ""
		}
		s.Encryption = m
	}
	if in.EncAuth != nil {
		if a := trimLower(in.EncAuth); a != s.EncAuth {
			s.EncAuth, s.EncKey, s.EncClient = a, "", ""
		}
	}
	if kind != subgen.KindVLESS || s.Encryption == "" {
		s.Encryption, s.EncAuth, s.EncKey, s.EncClient = "", "", "", ""
		return nil
	}
	if s.EncAuth == "" {
		s.EncAuth = encAuthX25519
	}
	if s.EncKey == "" && slices.Contains(encAuths, s.EncAuth) {
		var err error
		if s.EncKey, s.EncClient, err = vlessEncKeys(s.EncAuth); err != nil {
			return err
		}
	}
	return nil
}

// checkEncryption refuses a VLESS Encryption that would not work.
func (s *xraySettings) checkEncryption(kind string) error {
	if s.Encryption == "" {
		if s.EncKey != "" || s.EncClient != "" {
			return errors.New("VLESS Encryption keys are left over - turn VLESS Encryption on or regenerate the keys")
		}
		return nil
	}
	if kind != subgen.KindVLESS {
		return errors.New("VLESS Encryption works with VLESS only")
	}
	if !slices.Contains(encModes, s.Encryption) {
		return errors.New("VLESS Encryption must be none, native, xorpub or random")
	}
	if !slices.Contains(encAuths, s.EncAuth) {
		return errors.New("VLESS Encryption's server check must be x25519 or mlkem768")
	}
	client, err := vlessEncClientKey(s.EncKey)
	if err != nil || client != s.EncClient || encAuthOf(s.EncKey) != s.EncAuth {
		return errors.New("the VLESS Encryption keys are damaged - regenerate the keys")
	}
	return nil
}

// importDecryption reads the "decryption" of an existing VLESS inbound into the settings: the look and
// the key stay, so devices keep working. Its own padding and session ticket times are not kept - the
// server picks those, and clients work with any. It returns a note for the admin, or why it cannot be
// imported.
func (s *xraySettings) importDecryption(dec string) (note string, err error) {
	if dec == "" || dec == "none" {
		return "", nil
	}
	parts := strings.Split(dec, ".")
	if len(parts) < 4 || parts[0] != encHandshake || !slices.Contains(encModes, parts[1]) {
		return "", errors.New("it uses a VLESS Encryption Meridian does not know - only mlkem768x25519plus with native, xorpub or random")
	}
	if _, err := strconv.Atoi(strings.SplitN(strings.TrimSuffix(parts[2], "s"), "-", 2)[0]); err != nil {
		return "", errors.New("its VLESS Encryption setting cannot be read")
	}
	var keys, padding []string
	for _, p := range parts[3:] {
		if len(p) < 20 { // what Xray reads as padding
			padding = append(padding, p)
		} else {
			keys = append(keys, p)
		}
	}
	if len(keys) != 1 {
		return "", errors.New("its VLESS Encryption has several keys (a relay chain) - Meridian supports one key per protocol")
	}
	client, err := vlessEncClientKey(keys[0])
	if err != nil {
		return "", errors.New("its VLESS Encryption key cannot be read")
	}
	s.Encryption, s.EncKey, s.EncClient, s.EncAuth = parts[1], keys[0], client, encAuthOf(keys[0])
	if len(padding) > 0 || parts[2] != encTickets {
		note = "its VLESS Encryption padding and session times become Meridian's (" + encTickets + " tickets) - devices keep working"
	}
	return note, nil
}

// encryptionNotes tells the admin what VLESS Encryption as configured means.
func encryptionNotes(s *xraySettings) []string {
	if s.Encryption == "" {
		return nil
	}
	notes := []string{"VLESS Encryption: only apps that support it get this protocol - the Xray-based apps (v2rayN, v2rayNG and others), Clash Verge Rev, FlClash, Mihomo Party and Stash; the others are listed below."}
	if s.Security == secNone && !s.CDN && s.Encryption != encRandom {
		notes = append(notes, "Without TLS or REALITY, choose the random look: native and xorpub show the structure of VLESS Encryption to anyone watching the traffic.")
	}
	if s.EncAuth == encAuthMLKEM {
		notes = append(notes, "ML-KEM-768 keys make every link and QR code about 1.6 KB longer - choose X25519 if users scan QR codes. The key exchange is post-quantum either way.")
	}
	return notes
}
