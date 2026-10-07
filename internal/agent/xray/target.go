package xray

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/proxy"

	"meridian/internal/agent/acme"
	"meridian/internal/proto"
)

// checkSpec re-checks a camouflage from the panel: a public site, or the admin's own site on
// loopback. Nothing else may be handed to REALITY as a place to send strangers.
func checkSpec(t proto.TargetSpec) error {
	if !acme.ValidDomain(strings.ToLower(t.SNI)) {
		return fmt.Errorf("%q is not a domain name", t.SNI)
	}
	host, port, err := net.SplitHostPort(t.Addr)
	if err != nil {
		return fmt.Errorf("%q is not host:port", t.Addr)
	}
	if pn, err := strconv.Atoi(port); err != nil || pn < 1 || pn > 65535 {
		return fmt.Errorf("bad port in %q", t.Addr)
	}
	ip, err := netip.ParseAddr(host)
	if t.Own {
		if err != nil || !ip.IsLoopback() {
			return errors.New("your own site must listen on loopback, e.g. 127.0.0.1:8443")
		}
		return nil
	}
	if err == nil {
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() {
			return errors.New("the camouflage must be a public address")
		}
		return nil
	}
	if !acme.ValidDomain(strings.ToLower(host)) {
		return fmt.Errorf("%q is not a public domain", host)
	}
	return nil
}

func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// TestTarget checks a REALITY camouflage the only reliable way: it runs a throwaway REALITY server and
// client on localhost with this site as camouflage and fetches a page through them. A site that
// merely speaks TLS 1.3 and HTTP/2 can still fail here.
func (e *Engine) TestTarget(ctx context.Context, spec proto.TargetSpec) proto.TargetResult {
	target := spec.SNI
	res := proto.TargetResult{Target: spec.SNI, Addr: spec.Addr}
	if err := checkSpec(spec); err != nil {
		res.Error = err.Error()
		return res
	}
	if !e.Installed() {
		res.Error = "Xray is not installed yet"
		return res
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	b[0] &= 248
	b[31] &= 127
	b[31] |= 64
	k, err := ecdh.X25519().NewPrivateKey(b)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	priv := base64.RawURLEncoding.EncodeToString(b)
	pub := base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())
	rp, sp := freePort(), freePort()
	cfg := map[string]any{
		"log": map[string]any{"loglevel": "none"},
		"inbounds": []any{
			map[string]any{"tag": "srv", "listen": "127.0.0.1", "port": rp, "protocol": "vless",
				"settings": map[string]any{"decryption": "none", "clients": []any{map[string]any{
					"id": "00000000-0000-4000-8000-000000000001", "flow": "xtls-rprx-vision"}}},
				"streamSettings": map[string]any{"network": "raw", "security": "reality", "realitySettings": map[string]any{
					"target": spec.Addr, "serverNames": []string{target}, "privateKey": priv, "shortIds": []string{""}}}},
			map[string]any{"tag": "socks", "listen": "127.0.0.1", "port": sp, "protocol": "socks"},
		},
		"outbounds": []any{
			map[string]any{"tag": "direct", "protocol": "freedom"},
			map[string]any{"tag": "cli", "protocol": "vless", "settings": map[string]any{"address": "127.0.0.1", "port": rp,
				"id": "00000000-0000-4000-8000-000000000001", "flow": "xtls-rprx-vision", "encryption": "none"},
				"streamSettings": map[string]any{"network": "raw", "security": "reality", "realitySettings": map[string]any{
					"serverName": target, "fingerprint": "chrome", "publicKey": pub, "shortId": ""}}},
		},
		"routing": map[string]any{"rules": []any{
			map[string]any{"inboundTag": []string{"socks"}, "outboundTag": "cli"},
			map[string]any{"inboundTag": []string{"srv"}, "outboundTag": "direct"},
		}},
	}
	f, err := os.CreateTemp(e.RunDir, "target-*.json")
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer os.Remove(f.Name())
	f.Write(marshal(cfg))
	f.Close()
	cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, e.bin(), "run", "-c", f.Name())
	cmd.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+e.currentDir())
	if err := cmd.Start(); err != nil {
		res.Error = err.Error()
		return res
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	for i := 0; i < 50; i++ {
		if c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(sp)); err == nil {
			c.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	dialer, err := proxy.SOCKS5("tcp", "127.0.0.1:"+strconv.Itoa(sp), nil, proxy.Direct)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.(proxy.ContextDialer).DialContext(ctx, network, addr)
		}}}
	start := time.Now()
	resp, err := client.Get("https://www.gstatic.com/generate_204")
	if err != nil {
		if spec.Own {
			res.Error = fmt.Sprintf("your site at %s did not complete a TLS 1.3 handshake for %s - check that it runs there with a valid certificate for %s", spec.Addr, target, target)
		} else {
			res.Error = fmt.Sprintf("%s does not work as camouflage from this server", target)
		}
		return res
	}
	resp.Body.Close()
	res.OK = resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK
	res.MS = time.Since(start).Milliseconds()
	if !res.OK {
		res.Error = resp.Status
	}
	return res
}
