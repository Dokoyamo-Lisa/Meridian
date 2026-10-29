package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"meridian/internal/subgen"
)

// The Cloudflare check. A name a protocol depends on that resolves into Cloudflare's network is
// trouble the supervisor hears of in red before saving:
//
//   - a REALITY camouflage site behind Cloudflare: strangers who reach the server are passed on to
//     Cloudflare through it, so Cloudflare can take the server for an abusive proxy and flag it - and a
//     server outside Cloudflare's network answering for a Cloudflare site is easy to spot;
//   - the domain of a protocol with its own certificate, proxied by Cloudflare (the orange cloud):
//     apps reach Cloudflare instead of the server, and Cloudflare can take that traffic for abuse.
//
// The CDN kind goes through Cloudflare on purpose and is not warned about. Names are looked up by the
// panel - two seconds at most, the answer kept for half an hour; a name that does not resolve says
// nothing.

// cloudflareNets are Cloudflare's published ranges (https://www.cloudflare.com/ips/).
var cloudflareNets = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18",
		"108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15",
		"104.16.0.0/13", "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
		"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32"} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

func inCloudflare(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range cloudflareNets {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

const cfKeep = 30 * time.Minute

var cfCache = struct {
	sync.Mutex
	at   map[string]time.Time
	cf   map[string]bool
	busy map[string]bool
}{at: map[string]time.Time{}, cf: map[string]bool{}, busy: map[string]bool{}}

// lookupCF resolves a name (a hook for tests).
var lookupCF = func(ctx context.Context, name string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", name)
}

// behindCloudflare says whether name resolves into Cloudflare's network, looking it up when the last
// answer is older than cfKeep.
func behindCloudflare(ctx context.Context, name string) bool {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if name == "" || !strings.Contains(name, ".") {
		return false
	}
	if cf, ok := cfCached(name); ok {
		return cf
	}
	return cfLookup(ctx, name)
}

// cloudflareKnown says what is known of a name without waiting: a lookup starts in the background when
// nothing recent is (known false meanwhile).
func cloudflareKnown(name string) (cf, known bool) {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if name == "" || !strings.Contains(name, ".") {
		return false, true
	}
	if cf, ok := cfCached(name); ok {
		return cf, true
	}
	cfCache.Lock()
	start := !cfCache.busy[name]
	cfCache.busy[name] = true
	cfCache.Unlock()
	if start {
		go cfLookup(context.Background(), name)
	}
	return false, false
}

func cfCached(name string) (bool, bool) {
	cfCache.Lock()
	defer cfCache.Unlock()
	if at, ok := cfCache.at[name]; ok && time.Since(at) < cfKeep {
		return cfCache.cf[name], true
	}
	return false, false
}

func cfLookup(ctx context.Context, name string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	addrs, err := lookupCF(ctx, name)
	cf := false
	for _, a := range addrs {
		cf = cf || inCloudflare(a)
	}
	cfCache.Lock()
	defer cfCache.Unlock()
	delete(cfCache.busy, name)
	if err != nil && len(addrs) == 0 {
		return false // no answer: nothing to say, asked again next time
	}
	if len(cfCache.at) > 2000 {
		cfCache.at, cfCache.cf = map[string]time.Time{}, map[string]bool{}
	}
	cfCache.at[name], cfCache.cf[name] = time.Now(), cf
	return cf
}

// cfRealityWarning and cfProxiedWarning say what is wrong, and what to do.
func cfRealityWarning(site string) string {
	return fmt.Sprintf("%s is behind Cloudflare: REALITY passes strangers' visits on to it through this server, so Cloudflare can take the "+
		"server for an abusive proxy and flag it - and a server outside Cloudflare's network answering for a Cloudflare site is easy to spot. "+
		"Choose a camouflage site that is not behind Cloudflare.", site)
}

// cfCDNWarning: a CDN domain on Cloudflare works, but Cloudflare can take proxy traffic for abuse.
func cfCDNWarning(name string) string {
	return fmt.Sprintf("%s is on Cloudflare's CDN: Cloudflare can take proxy traffic passing through it for abuse of its service, "+
		"and flag the domain or suspend the account - then everybody using this protocol loses it at once. It works, at that risk: "+
		"keep a second protocol for your users that does not go through Cloudflare.", name)
}

// cfHostWarning: the address in a protocol's links points at Cloudflare's proxy, so apps never reach
// the server.
func cfHostWarning(name string) string {
	return fmt.Sprintf("%s, the address in this protocol's links, points at Cloudflare's proxy (the orange cloud): apps would reach "+
		"Cloudflare instead of this server, so the protocol would not work - and Cloudflare can take the traffic for abuse. In Cloudflare, "+
		"set the record to DNS only (the grey cloud), or give the protocol an address that is not proxied.", name)
}

func cfProxiedWarning(name string) string {
	return fmt.Sprintf("%s points at Cloudflare's proxy (the orange cloud): apps would reach Cloudflare instead of this server, and Cloudflare "+
		"can take this traffic for abuse. In Cloudflare, set the record to DNS only (the grey cloud) - or use VLESS · WebSocket · CDN to go "+
		"through Cloudflare on purpose.", name)
}

// cfNames are the names of a protocol to look at, and the warning each gets when it is behind
// Cloudflare. host is the address in its links (a domain is looked at; an IP address is not).
func cfNames(kind string, raw json.RawMessage, host string) map[string]string {
	out := map[string]string{}
	switch kind {
	case subgen.KindVLESS, subgen.KindVMess, subgen.KindTrojan:
		var s xraySettings
		if json.Unmarshal(raw, &s) != nil {
			return nil
		}
		if s.CDN { // through a CDN on purpose: its domain is the address in the links
			if s.CDNHost != "" {
				out[s.CDNHost] = cfCDNWarning(s.CDNHost)
			}
			return out
		}
		switch s.Security {
		case secReality:
			if s.OwnSite {
				return nil
			}
			if s.SNI != "" {
				out[s.SNI] = cfRealityWarning(s.SNI)
			}
			if host, _, err := net.SplitHostPort(s.Target); err == nil && host != "" && host != s.SNI {
				if _, err := netip.ParseAddr(host); err != nil {
					out[host] = cfRealityWarning(host)
				}
			}
		case secTLS:
			if s.CertMode != certSelf && s.SNI != "" {
				out[s.SNI] = cfProxiedWarning(s.SNI)
			}
		}
	case subgen.KindHysteria2:
		var s hy2Settings
		if json.Unmarshal(raw, &s) == nil && s.CertMode != certSelf && s.SNI != "" {
			out[s.SNI] = cfProxiedWarning(s.SNI)
		}
	case subgen.KindAnyTLS:
		if s := parseAnyTLS(raw); s.CertMode != certSelf && s.SNI != "" {
			out[s.SNI] = cfProxiedWarning(s.SNI)
		}
	}
	if host = strings.Trim(strings.TrimSpace(host), "[]"); host != "" && out[host] == "" {
		if _, err := netip.ParseAddr(host); err != nil {
			out[host] = cfHostWarning(host)
		}
	}
	return out
}

// domainWarnings are the red warnings for a protocol as set up: the names it depends on that are
// behind Cloudflare (looked up now, two seconds at most each).
func domainWarnings(ctx context.Context, kind string, raw json.RawMessage, host string) []string {
	var out []string
	for name, warn := range cfNames(kind, raw, host) {
		if behindCloudflare(ctx, name) {
			out = append(out, warn)
		}
	}
	return out
}

// knownDomainWarnings are the same from what is known now, without waiting (the server page's notes).
func knownDomainWarnings(kind string, raw json.RawMessage, host string) []string {
	var out []string
	for name, warn := range cfNames(kind, raw, host) {
		if cf, known := cloudflareKnown(name); known && cf {
			out = append(out, warn)
		}
	}
	return out
}
