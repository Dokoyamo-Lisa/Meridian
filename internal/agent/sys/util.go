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

// echoServices tell a host its public IPv4 address: DB-IP (the geolocation provider the panel
// uses) and Cloudflare. The answer ends up in users' links, so they are asked over HTTPS only.
var echoServices = []struct {
	url   string
	parse func([]byte) string
}{
	{"https://api.db-ip.com/v2/free/self", func(b []byte) string {
		var v struct {
			IP string `json:"ipAddress"`
		}
		_ = json.Unmarshal(b, &v)
		return v.IP
	}},
	{"https://1.1.1.1/cdn-cgi/trace", func(b []byte) string {
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "ip="); ok {
				return v
			}
		}
		return ""
	}},
}

// echoClient connects over IPv4 only (the question is the IPv4 address), ignores proxy settings
// and follows redirects only to HTTPS.
var echoClient = &http.Client{
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp4", addr)
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

// echoIP asks one echo service; only a public IPv4 address counts as an answer.
func echoIP(ctx context.Context, url string, parse func([]byte) string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	resp, err := echoClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	ip := net.ParseIP(strings.TrimSpace(parse(b)))
	if ip == nil || ip.To4() == nil || !isPublic(ip) {
		return ""
	}
	return ip.To4().String()
}

func lookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
