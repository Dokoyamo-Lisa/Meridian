package sys

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
)

// Several addresses: a server with more than one can give each protocol its own. A public address is
// reached as it is. A private one - a cloud's second address behind its own elastic or floating IP -
// is reached at the public address its traffic leaves with, which an echo service tells when it is
// asked from that address. Each private address is asked at most every echoEvery, at most
// maxAddrPub of them; an address that went away is forgotten.

const maxAddrPub = 16

var addrPub = struct {
	sync.Mutex
	at  map[string]time.Time // when each address was last asked
	ans map[string]string    // what was said ("" = no answer, or not a public address)
}{at: map[string]time.Time{}, ans: map[string]string{}}

// askFrom asks the echo services, in order, for the public address of traffic from local (a hook for
// tests). Only a public address of local's kind counts as an answer.
var askFrom = func(ctx context.Context, local netip.Addr) string {
	list := echoServices
	if local.Is6() {
		list = echoServices6
	}
	for _, s := range list {
		c, cancel := context.WithTimeout(ctx, 6*time.Second)
		ip := echoIPFrom(c, s, local)
		cancel()
		if ip != "" {
			return ip
		}
	}
	return ""
}

// AddrPublic gives, for each address in addrs that is not public itself, the public address its
// traffic leaves with, as far as known - asking where the last answer is older than echoEvery.
func AddrPublic(ctx context.Context, addrs []string) map[string]string {
	keep := map[string]bool{}
	n := 0
	for _, s := range addrs {
		a, err := netip.ParseAddr(s)
		if err != nil || isPublic(net.IP(a.Unmap().AsSlice())) || n == maxAddrPub {
			continue
		}
		n++
		keep[s] = true
		addrPub.Lock()
		at, asked := addrPub.at[s]
		addrPub.Unlock()
		if asked && clock().Sub(at) < echoEvery {
			continue
		}
		ans := askFrom(ctx, a.Unmap())
		addrPub.Lock()
		addrPub.at[s], addrPub.ans[s] = clock(), ans
		addrPub.Unlock()
	}
	addrPub.Lock()
	defer addrPub.Unlock()
	for s := range addrPub.at {
		if !keep[s] {
			delete(addrPub.at, s)
			delete(addrPub.ans, s)
		}
	}
	return lastAddrPublicLocked()
}

// LastAddrPublic is what the last AddrPublic found, without asking anyone.
func LastAddrPublic() map[string]string {
	addrPub.Lock()
	defer addrPub.Unlock()
	return lastAddrPublicLocked()
}

func lastAddrPublicLocked() map[string]string {
	out := map[string]string{}
	for s, ans := range addrPub.ans {
		if ans != "" {
			out[s] = ans
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SameAddrPublic says whether two answers of AddrPublic tell the same.
func SameAddrPublic(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if b[k] != a[k] {
			return false
		}
	}
	return true
}

// echoIPFrom asks one echo service over a connection made from local: what it says is the public
// address that traffic from local leaves with.
func echoIPFrom(ctx context.Context, s echoService, local netip.Addr) string {
	network := "tcp4"
	if local.Is6() {
		network = "tcp6"
	}
	d := &net.Dialer{Timeout: 5 * time.Second, LocalAddr: &net.TCPAddr{IP: net.IP(local.AsSlice())}}
	c := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				return d.DialContext(ctx, network, addr)
			},
			TLSHandshakeTimeout: 5 * time.Second,
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
			DisableKeepAlives:   true,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" || len(via) >= 3 {
				return errors.New("redirect refused")
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return ""
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
	if ip == nil || !isPublic(ip) || (ip.To4() != nil) == local.Is6() {
		return ""
	}
	return ip.String()
}
