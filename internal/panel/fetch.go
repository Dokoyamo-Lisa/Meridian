package panel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Fetching from addresses the supervisor types in (a provider's subscription): HTTPS only, and
// only from public addresses - checked where the connection is made, so a name that resolves to
// the panel's own network (or changes its answer between two lookups) gets nowhere.

const fetchLimit = 4 << 20

var errNotPublic = errors.New("that address is not on the public internet")

// publicDial connects only to public addresses.
func publicDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("%s cannot be found", host)
	}
	d := &net.Dialer{Timeout: 10 * time.Second}
	var last error = errNotPublic
	for _, ip := range ips {
		if !publicAddr(ip) {
			continue
		}
		c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

func publicClient() *http.Client {
	tr := &http.Transport{DialContext: publicDial, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second,
		MaxResponseHeaderBytes: 64 << 10, ForceAttemptHTTP2: true}
	return &http.Client{Timeout: 30 * time.Second, Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return errors.New("it redirects to a plain HTTP address")
			}
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			return nil
		}}
}

// checkFetchURL accepts an https:// address with a host name or a public IP.
func checkFetchURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return nil, errStatus(http.StatusBadRequest, "enter the full address, starting with https://")
	}
	if u.Scheme != "https" {
		return nil, errStatus(http.StatusBadRequest, "only https:// addresses are fetched - a plain http:// one could be read or changed on the way")
	}
	if u.User != nil {
		return nil, errStatus(http.StatusBadRequest, "put no user name or password in the address")
	}
	if a, err := netip.ParseAddr(u.Hostname()); err == nil && !publicAddr(a) {
		return nil, errStatus(http.StatusBadRequest, errNotPublic.Error())
	}
	return u, nil
}

// fetchText gets a provider's subscription (at most 4 MB).
func fetchText(ctx context.Context, raw string) (string, error) {
	u, err := checkFetchURL(raw)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", errStatus(http.StatusBadRequest, "the address cannot be used")
	}
	req.Header.Set("User-Agent", "Meridian/"+Version)
	resp, err := publicClient().Do(req)
	if err != nil {
		return "", errStatus(http.StatusBadGateway, "could not fetch it: "+plainNetErr(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errStatus(http.StatusBadGateway, fmt.Sprintf("the address answered %d - check it in a browser", resp.StatusCode))
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, fetchLimit+1))
	if err != nil {
		return "", errStatus(http.StatusBadGateway, "could not read the answer: "+plainNetErr(err))
	}
	if len(b) > fetchLimit {
		return "", errStatus(http.StatusBadGateway, "the answer is larger than 4 MB")
	}
	return string(b), nil
}
