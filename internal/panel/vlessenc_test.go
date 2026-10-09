package panel

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"meridian/internal/subgen"
)

// Pairs printed by Xray 26.3.27's own `xray vlessenc`: the panel must derive exactly the client key
// Xray does from the server key, or devices could not connect.
const (
	xrayX25519Server = "qIhHk0zGAMM9-JWrt8OiRhWyn-KVmucLiIyWbeV4Mkk"
	xrayX25519Client = "yGFp__hvmtrM145uNYLVcd1GGHTv6n3pnNK1jzb7c3s"
	xrayMLKEMServer  = "Ibkw_xhbIi6MKn0dhtPqXqP3LX8leYljIAW77pN59Bv5sSl_Jb_Cfl6IPClRVT0I0dmido1gjNcpuq-Unmzwaw"
	xrayMLKEMClient  = "s3xT2fKSv4MteGa2LHmczcw0HZDCv_U3cwqfnfsh9VtNVnJGwzuGqvoYKUSQeXs2ccnIbUoPGks8lMlHr2MqikIKfmNNkiaMGTfBPvvIVakjObhEbUw1MeB5C8lkanAv8PK23nwNS8BbUMGyc2RCsFpqEdpwhGhpeeJTz4S4_Wl8MVUoglVE6WUTn2nJQ5e_2os9FXJhG2O-r9YxaFnDDDQQnlKsJGmMJ-UA75pzOHoHq7xHeiam0ICWJtEethQg3ds7hlor4-akKhyRexyp25qLj8cdxjvPvxFW8SmY-qRz-_Qb0XkszePMCBQ-AWt99ORyCEfD0HwmQJdEImmNjHNKe_PD0URmyLZkyTPIvvCWgnzMr5DHh6y6STxZCECp4SVFnRkdbgUKP4Cfyjo_PvbAStOBy5zJ0pOpYuRN1ttr9XbCZqG6blvOFLFJ83EqOephsZiZh8MRUpgBfCNCr6oYKVxb6PBy9jhOobu3PqSJEzSmA4wIRjKQmiJZJLtnZliGaaSoVrbEVvUXu0A6swERgOClNAh4_QC4MwTIzntuh8ane8SKCfFJa_e1_SpMrtROhDUhOeU7l9GIjJsNO0WWXzuzLVS-hSU8V0sv2OpkbpkWAzh8oGu7Q4a75GBafIZzqhWA9kGdUeSjNLWRPrIEdLnIkxMjAWQKcri6nAVsmuRKMrdCEpshFpuhpDA4yjED-3Gw7poTQZApLwCgFIIMLJyj9GdOoNQexhailHxugyCQ9aWhaVuKIekM-Rmr_TVQ9UGC8DkG7JaWNCWG05fHmbCjZno4zMV8oEeaSlq5vJsXUMNGPvyfExFpW-O4mVcDBLE31GQC9eBh-NwyiBKl7mu_2OUPZBW27vqrPiqwRoRRfWwVFcmcGoBncYWEAKS_rrM1YtkTJXy2OufMYDFoc0SUZFBpWqPK1qbNyMZAnAMY6qu6iOot_hwbS2C7SSwthZy9TQQBtJhOrywpvwsF4_S76ChZVHSpjstx_KIi-HVzIhU1J8dujGjIJPeujAsqzIQN3gqlJ9smdbOgr_QWqpBQIGaB0DFCcvu5a8SsvpFLx9y0kKhgn6RsVsGyGpOTcgBb_eGQYFwhCorLVymH8PMYioZS_oyYc3mNUiOws1m7mGC6HMMhHMJGUEuUQgMV_zgKredXwUOmduRhUVhMnouAAmu4IfAseAyzSpaKi7LMKpKGqsUI5zmauZCJ1lEgaSB0KkEBaYaSxIy54CEQfvmywmS2tkMfg_m2ekxraWWWIdyGeBde3qJ8krkoHgWurXKw0LkPA7gtZwA6upd2UmzHmTOssUZ1yDLHi4cpR_cE42cksijPCTlFx5Jr-GEpJjuaHbqP7QUuQzN10lSNdpFweolpO3d2NSZ_KAV4UmDL3YhJzkE-rLioz6CqjvF0z6dENDsmHDSzUmOJkjXHcnaUHXYsmayUcksN6kCY5qSGa7xO_FMSL_GumlS0BgtfHtl6c8TGaAXQYkmHXPu98Pt6QMMWWNqRHMkdeda84IpfoEmjjDrCNIRLudsdAZKDc4J-2CToJcm5BqOe-22fdrTIDoMjVyJadBNmzps"
)

func TestVLESSEncryptionKeysMatchXray(t *testing.T) {
	for server, client := range map[string]string{xrayX25519Server: xrayX25519Client, xrayMLKEMServer: xrayMLKEMClient} {
		got, err := vlessEncClientKey(server)
		if err != nil || got != client {
			t.Errorf("client key of %s...: %v\n got %.40s...\nwant %.40s...", server[:10], err, got, client)
		}
	}
	if encAuthOf(xrayX25519Server) != encAuthX25519 || encAuthOf(xrayMLKEMServer) != encAuthMLKEM {
		t.Error("authentication not told apart by the key")
	}
	// new keys have the lengths `xray vlessenc` prints: 32-byte X25519 keys, a 64-byte seed and a
	// 1184-byte encapsulation key - all base64url without padding
	b64 := regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	for auth, lens := range map[string][2]int{encAuthX25519: {43, 43}, encAuthMLKEM: {86, 1579}} {
		srv, cli, err := vlessEncKeys(auth)
		if err != nil {
			t.Fatal(err)
		}
		if len(srv) != lens[0] || len(cli) != lens[1] || !b64.MatchString(srv) || !b64.MatchString(cli) {
			t.Errorf("%s keys: %d and %d characters", auth, len(srv), len(cli))
		}
		if derived, _ := vlessEncClientKey(srv); derived != cli {
			t.Errorf("%s: the client key does not belong to the server key", auth)
		}
	}
	if _, _, err := vlessEncKeys("rsa"); err == nil {
		t.Error("an unknown authentication made keys")
	}
}

func TestVLESSEncryptionSettings(t *testing.T) {
	str := func(s string) *string { return &s }
	// without TLS VLESS needs it; with it the look is chosen and the keys are made
	if _, err := newSettings(subgen.KindVLESS, &protoInput{Security: str(secNone)}, nil); err == nil ||
		!strings.Contains(err.Error(), "VLESS Encryption") {
		t.Fatalf("VLESS without TLS or VLESS Encryption: %v", err)
	}
	raw, err := newSettings(subgen.KindVLESS, &protoInput{Transport: str(tXHTTP), Security: str(secNone), Encryption: str("random")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := parseXray(raw)
	if s.Encryption != encRandom || s.EncAuth != encAuthX25519 || s.EncKey == "" || s.Flow != flowVision {
		t.Fatalf("encryption settings: %+v", s)
	}
	dec := regexp.MustCompile(`^mlkem768x25519plus\.random\.600s\.[A-Za-z0-9_-]{43}$`)
	enc := regexp.MustCompile(`^mlkem768x25519plus\.random\.0rtt\.[A-Za-z0-9_-]{43}$`)
	if !dec.MatchString(s.decryption()) || !enc.MatchString(s.encryption()) {
		t.Fatalf("not the form xray vlessenc prints: %s / %s", s.decryption(), s.encryption())
	}
	// the server's key never reaches the API view or the devices
	pub, _ := json.Marshal(publicSettings(subgen.KindVLESS, raw))
	if strings.Contains(string(pub), s.EncKey) {
		t.Fatal("the server's VLESS Encryption key is in the API view")
	}
	n := &Node{ID: 5, Kind: subgen.KindVLESS, Port: 443, Settings: raw}
	sub := &Sub{ID: 1, UUID: "00000000-0000-4000-8000-000000000001", Secret: "x"}
	in, err := xrayInbound(n, []*Sub{sub}, nil, nil)
	if err != nil || !strings.Contains(string(in.Config), `"decryption":"`+s.decryption()+`"`) {
		t.Fatalf("inbound: %v %s", err, in.Config)
	}
	e, err := endpoint(n, &Server{Address: "203.0.113.1"}, sub, nil, "x", nil)
	if err != nil || e.Encryption != s.encryption() || strings.Contains(subgen.URI(e), s.EncKey) {
		t.Fatalf("endpoint: %v %+v", err, e)
	}
	if got := protocolLabel(subgen.KindVLESS, raw); got != "VLESS XHTTP ENC" {
		t.Errorf("label %q", got)
	}

	// another look keeps the keys; another check makes new ones; regenerating makes new ones too
	raw2, err := updateSettings(subgen.KindVLESS, raw, &protoInput{Encryption: str("native")})
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := parseXray(raw2)
	if s2.EncKey != s.EncKey || s2.Encryption != encNative {
		t.Fatalf("a new look changed the keys: %+v", s2)
	}
	raw3, err := updateSettings(subgen.KindVLESS, raw2, &protoInput{EncAuth: str("mlkem768")})
	if err != nil {
		t.Fatal(err)
	}
	s3, _ := parseXray(raw3)
	if s3.EncKey == s2.EncKey || s3.EncAuth != encAuthMLKEM || len(s3.EncClient) != 1579 {
		t.Fatalf("ML-KEM-768 keys: %+v", s3)
	}
	raw4, err := regenKeys(subgen.KindVLESS, raw3)
	if err != nil {
		t.Fatal(err)
	}
	if s4, _ := parseXray(raw4); s4.EncKey == s3.EncKey || s4.EncAuth != encAuthMLKEM {
		t.Fatalf("regenerated: %+v", s4)
	}
	// turned off: nothing is left behind, and VLESS needs TLS again
	if _, err := updateSettings(subgen.KindVLESS, raw3, &protoInput{Encryption: str("none")}); err == nil {
		t.Fatal("VLESS without TLS and without VLESS Encryption was accepted")
	}
	raw5, err := updateSettings(subgen.KindVLESS, raw3, &protoInput{Encryption: str("none"), Security: str(secReality), Transport: str(tRaw)})
	if err != nil {
		t.Fatal(err)
	}
	if s5, _ := parseXray(raw5); s5.EncKey != "" || s5.EncClient != "" || s5.EncAuth != "" || s5.decryption() != "none" {
		t.Fatalf("keys left after turning it off: %+v", s5)
	}

	// refusals
	for _, c := range []struct {
		kind string
		in   *protoInput
		want string
	}{
		{subgen.KindVLESS, &protoInput{Encryption: str("plain")}, "native, xorpub or random"},
		{subgen.KindVLESS, &protoInput{Encryption: str("native"), EncAuth: str("rsa")}, "x25519 or mlkem768"},
		{subgen.KindVMess, &protoInput{Encryption: str("native")}, "VLESS Encryption"},
		{subgen.KindTrojan, &protoInput{EncAuth: str("x25519")}, "VLESS Encryption"},
		{subgen.KindHysteria2, &protoInput{Encryption: str("native")}, "transport and TLS options"},
		{subgen.KindVLESS, &protoInput{Transport: str(tWS), Security: str(secTLS), Flow: str(flowVision)}, "Vision"},
	} {
		if _, err := newSettings(c.kind, c.in, nil); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %+v: %v, want an error about %q", c.kind, c.in, err, c.want)
		}
	}
	// Vision works over every transport with VLESS Encryption
	if _, err := newSettings(subgen.KindVLESS, &protoInput{Transport: str(tWS), Security: str(secTLS), Flow: str(flowVision),
		Encryption: str("native")}, nil); err != nil {
		t.Errorf("Vision with VLESS Encryption over WebSocket: %v", err)
	}
	// damaged keys are refused
	bad := *s
	bad.EncClient = xrayX25519Client
	if err := bad.check(subgen.KindVLESS); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Errorf("keys that do not belong together: %v", err)
	}
}

func TestImportDecryption(t *testing.T) {
	for _, c := range []struct {
		dec, look, auth, client string
		note                    bool
	}{
		{"mlkem768x25519plus.native.600s." + xrayX25519Server, encNative, encAuthX25519, xrayX25519Client, false},
		{"mlkem768x25519plus.random.300-600s.100-111-1111.75-0-111.50-0-3333." + xrayMLKEMServer, encRandom, encAuthMLKEM, xrayMLKEMClient, true},
		{"none", "", "", "", false},
	} {
		var s xraySettings
		note, err := s.importDecryption(c.dec)
		if err != nil || s.Encryption != c.look || s.EncAuth != c.auth || s.EncClient != c.client || (note != "") != c.note {
			t.Errorf("%.50s: %v %q %+v", c.dec, err, note, s)
		}
	}
	for _, dec := range []string{
		"mlkem768x25519plus.native.600s." + xrayX25519Server + "." + xrayX25519Server, // a relay chain
		"mlkem768x25519plus.plain.600s." + xrayX25519Server,
		"x25519.native.600s." + xrayX25519Server,
		"mlkem768x25519plus.native.600s.not-a-key-at-all-but-long-enough",
		"mlkem768x25519plus.native.soon." + xrayX25519Server,
	} {
		var s xraySettings
		if _, err := s.importDecryption(dec); err == nil {
			t.Errorf("imported %.50s", dec)
		}
	}
}
