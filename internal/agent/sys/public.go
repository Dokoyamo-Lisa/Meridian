package sys

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"
)

// The host's public addresses are what users' links and other servers' links to this server use.
// The agent checks them every two minutes and tells the panel as soon as they change. The check is
// local and cheap - the source address the routing table picks towards the internet; no packet is
// sent - and only when that address is not a public one (behind a provider's NAT) is an echo
// service asked, at most every five minutes: Cloudflare's trace first, DB-IP second.

// PublicAddrs is what a check found.
type PublicAddrs struct {
	IPv4, IPv6 string // "" = none found this time
	// IPv4Gone, IPv6Gone: the host had no route to the internet of that kind in two checks in a row -
	// it has no such address any more, unlike an empty IPv4 or IPv6 alone (a lookup that failed)
	IPv4Gone, IPv6Gone bool
}

// echoEvery is how often an echo service may be asked for one kind of address.
const echoEvery = 5 * time.Minute

type publicWatch struct {
	checking sync.Mutex // one check at a time; mu is never held while one asks the network
	mu       sync.Mutex
	checked  bool
	last     PublicAddrs
	miss     [2]int       // checks in a row without a route, IPv4 and IPv6
	echoAt   [2]time.Time // when an echo service was last asked
	echo     [2]string    // and what it said
}

var public = &publicWatch{}

// Hooks for tests.
var (
	// routeSource is the address the host would send from towards the internet (IPv4 or IPv6), or an
	// error when it has no route of that kind. Dialling UDP only asks the routing table.
	routeSource = func(v6 bool) (netip.Addr, error) {
		network, to := "udp4", "1.1.1.1:53"
		if v6 {
			network, to = "udp6", "[2606:4700:4700::1111]:53"
		}
		c, err := net.Dial(network, to)
		if err != nil {
			return netip.Addr{}, err
		}
		defer c.Close()
		a, ok := c.LocalAddr().(*net.UDPAddr)
		if !ok {
			return netip.Addr{}, errors.New("no local address")
		}
		ip, ok := netip.AddrFromSlice(a.IP)
		if !ok {
			return netip.Addr{}, errors.New("no local address")
		}
		return ip.Unmap(), nil
	}
	// askEcho asks the echo services for the host's public address of one kind, in order.
	askEcho = func(ctx context.Context, v6 bool) string {
		list := echoServices
		if v6 {
			list = echoServices6
		}
		for _, s := range list {
			c, cancel := context.WithTimeout(ctx, 6*time.Second)
			ip := echoIP(c, s, v6)
			cancel()
			if ip != "" {
				return ip
			}
		}
		return ""
	}
	clock = time.Now
)

// CheckPublic looks at the host's public addresses now.
func CheckPublic(ctx context.Context) PublicAddrs { return public.check(ctx) }

// LastPublic is what the last check found; it checks once when none has run yet.
func LastPublic(ctx context.Context) PublicAddrs {
	public.mu.Lock()
	done, last := public.checked, public.last
	public.mu.Unlock()
	if done {
		return last
	}
	return public.check(ctx)
}

func (w *publicWatch) check(ctx context.Context) PublicAddrs {
	w.checking.Lock()
	defer w.checking.Unlock()
	var out PublicAddrs
	out.IPv4, out.IPv4Gone = w.kind(ctx, 0, false)
	out.IPv6, out.IPv6Gone = w.kind(ctx, 1, true)
	w.mu.Lock()
	w.checked, w.last = true, out
	w.mu.Unlock()
	return out
}

// kind checks one kind of address (i: 0 for IPv4, 1 for IPv6). Only check runs it.
func (w *publicWatch) kind(ctx context.Context, i int, v6 bool) (string, bool) {
	src, err := routeSource(v6)
	if err != nil {
		w.miss[i]++
		w.echoAt[i] = time.Time{} // a route that comes back is asked about at once
		return "", w.miss[i] >= 2
	}
	w.miss[i] = 0
	if isPublic(net.IP(src.AsSlice())) {
		return src.String(), false
	}
	// behind NAT: the address the internet sees comes from an echo service
	if now := clock(); w.echoAt[i].IsZero() || now.Sub(w.echoAt[i]) >= echoEvery {
		w.echoAt[i] = now
		w.echo[i] = askEcho(ctx, v6)
	}
	return w.echo[i], false
}

// nat64 remembers whether the host's resolver gives names that have only IPv4 addresses an IPv6
// address of its own making (DNS64), which the provider's NAT64 carries on to IPv4: RFC 7050's
// ipv4only.arpa has IPv4 addresses only, so any IPv6 address for it was made that way. It is asked
// every 30 minutes; a lookup that fails keeps what was known.
var nat64 struct {
	mu sync.Mutex
	at time.Time
	on bool
}

var lookupNAT64 = func(ctx context.Context) (bool, error) {
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip6", "ipv4only.arpa")
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return len(ips) > 0, nil
}

// NAT64 says whether the host reaches IPv4-only names through its provider's DNS64 and NAT64.
func NAT64() bool {
	nat64.mu.Lock()
	defer nat64.mu.Unlock()
	if !nat64.at.IsZero() && clock().Sub(nat64.at) < 30*time.Minute {
		return nat64.on
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	on, err := lookupNAT64(ctx)
	nat64.at = clock()
	if err == nil {
		nat64.on = on
	}
	return nat64.on
}
