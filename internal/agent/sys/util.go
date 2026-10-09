package sys

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

type echoService struct {
	url   string
	parse func([]byte) string
}

// cloudflareTrace reads the address Cloudflare's trace page saw ("ip=...").
func cloudflareTrace(b []byte) string {
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "ip="); ok {
			return v
		}
	}
	return ""
}

// echoServices tell a host its public IPv4 address: Cloudflare's trace page first, then DB-IP (the
// geolocation provider the panel uses). The answer ends up in users' links, so they are asked over
// HTTPS only - Cloudflare at its own addresses, which its certificate names.
var echoServices = []echoService{
	{"https://1.1.1.1/cdn-cgi/trace", cloudflareTrace},
	{"https://api.db-ip.com/v2/free/self", func(b []byte) string {
		var v struct {
			IP string `json:"ipAddress"`
		}
		_ = json.Unmarshal(b, &v)
		return v.IP
	}},
}

// echoServices6 tell a host its public IPv6 address, for the rare host behind an IPv6 NAT.
var echoServices6 = []echoService{
	{"https://[2606:4700:4700::1111]/cdn-cgi/trace", cloudflareTrace},
}

// echoClient connects over one kind of address only (the question is that kind's address), ignores
// proxy settings and follows redirects only to HTTPS.
func echoClient(network string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
			},
			TLSHandshakeTimeout: 5 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" || len(via) >= 3 {
				return errors.New("redirect refused")
			}
			return nil
		},
	}
}

var echoClient4, echoClient6 = echoClient("tcp4"), echoClient("tcp6")

// echoIP asks one echo service; only a public address of the asked kind counts as an answer.
func echoIP(ctx context.Context, s echoService, v6 bool) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return ""
	}
	c := echoClient4
	if v6 {
		c = echoClient6
	}
	resp, err := c.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	ip := net.ParseIP(strings.TrimSpace(s.parse(b)))
	if ip == nil || !isPublic(ip) || (ip.To4() != nil) == v6 {
		return ""
	}
	return ip.String()
}

func lookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
