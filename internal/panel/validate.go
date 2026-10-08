package panel

// Input hygiene. Names and addresses typed into the panel end up in client configurations
// (YAML, INI-like Surge and Quantumult X lines, WireGuard files, share links), in nftables rules
// and in Xray configs, so they are checked where they enter.

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// cleanName trims a display name, drops control characters (newlines included) and limits its
// length in characters.
func cleanName(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r == utf8.RuneError || unicode.IsControl(r) || r == ' ' || r == ' ' {
			r = ' '
		}
		if n >= max {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// cleanNote keeps line breaks but no other control characters.
func cleanNote(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\t' || (!unicode.IsControl(r) && r != utf8.RuneError) {
			b.WriteRune(r)
		}
	}
	return truncate(strings.TrimSpace(b.String()), max)
}

// normHost accepts a domain name or an IP address (optionally written as a URL or with a port,
// which are dropped) and returns it in canonical form. "" stays "".
func normHost(s string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(s))
	if h == "" {
		return "", nil
	}
	h = strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://")
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	if a, err := netip.ParseAddr(h); err == nil {
		if a.Zone() != "" {
			return "", errStatus(http.StatusBadRequest, "addresses with a zone are not allowed")
		}
		return a.Unmap().String(), nil
	}
	h = strings.TrimSuffix(h, ".")
	if !validDNSName(h) {
		return "", errStatus(http.StatusBadRequest, "“"+truncate(s, 64)+"” is not a valid domain name or IP address")
	}
	return h, nil
}

// validDNSName checks letters-digits-hyphen labels (punycode for international names).
func validDNSName(h string) bool {
	if len(h) == 0 || len(h) > 253 {
		return false
	}
	labels := strings.Split(h, ".")
	for _, l := range labels {
		if len(l) == 0 || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	// a name made of digits only would be a malformed IP
	last := labels[len(labels)-1]
	for i := 0; i < len(last); i++ {
		if last[i] < '0' || last[i] > '9' {
			return true
		}
	}
	return false
}

// publicName is a domain that can only be meant for the internet (camouflage sites, certificate
// names): a dotted name that is not obviously local.
func publicName(h string) bool {
	if !validDNSName(h) || !strings.Contains(h, ".") {
		return false
	}
	for _, suffix := range []string{".local", ".localhost", ".internal", ".lan", ".home", ".corp", ".intranet", ".arpa"} {
		if strings.HasSuffix(h, suffix) {
			return false
		}
	}
	return h != "localhost"
}

// publicAddr reports whether an IP is routable on the internet.
func publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsValid() && a.IsGlobalUnicast() && !a.IsPrivate() && !a.IsLoopback() && !a.IsLinkLocalUnicast() &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(a) && !netip.MustParsePrefix("198.18.0.0/15").Contains(a)
}

// realityTarget validates the site a REALITY inbound forwards unauthenticated visitors to. It
// must be a public site: a private target would hand anyone on the internet a way into the
// server's own network.
func realityTarget(t string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(t))
	if err != nil {
		host, port = strings.TrimSpace(t), "443"
	}
	pn, err := strconv.Atoi(port)
	if err != nil || pn < 1 || pn > 65535 {
		return "", errStatus(http.StatusBadRequest, "the camouflage target port must be between 1 and 65535")
	}
	h, err := normHost(host)
	if err != nil || h == "" {
		return "", errStatus(http.StatusBadRequest, "the camouflage target must be a public site such as www.apple.com:443")
	}
	if a, err := netip.ParseAddr(h); err == nil {
		if !publicAddr(a) {
			return "", errStatus(http.StatusBadRequest, "the camouflage target must be a public address, not a private or local one")
		}
	} else if !publicName(h) {
		return "", errStatus(http.StatusBadRequest, "the camouflage target must be a public site such as www.apple.com")
	}
	return net.JoinHostPort(h, strconv.Itoa(pn)), nil
}

// forwardTarget validates where a port forward sends traffic. Private networks are allowed (a
// company LAN is a normal target); the server's own loopback, link-local (cloud metadata) and
// unspecified addresses are not. The kernel engine needs an IP address.
func forwardTarget(t, engine string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(t))
	if err != nil || host == "" {
		return "", errStatus(http.StatusBadRequest, "target must be host:port, e.g. 203.0.113.7:443 or [2001:db8::1]:443")
	}
	pn, err := strconv.Atoi(port)
	if err != nil || pn < 1 || pn > 65535 {
		return "", errStatus(http.StatusBadRequest, "target port must be between 1 and 65535")
	}
	h, err := normHost(host)
	if err != nil || h == "" {
		return "", errStatus(http.StatusBadRequest, "target must be host:port, e.g. 203.0.113.7:443")
	}
	if a, err := netip.ParseAddr(h); err == nil {
		if a.IsLoopback() || a.IsUnspecified() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsMulticast() {
			return "", errStatus(http.StatusBadRequest, "forwarding to the server's own loopback, link-local or multicast addresses is not allowed")
		}
	} else {
		if engine == "nft" {
			return "", errStatus(http.StatusBadRequest, "the kernel engine forwards to IP addresses - use an IP, or the realm engine for a domain")
		}
		if h == "localhost" || strings.HasSuffix(h, ".localhost") {
			return "", errStatus(http.StatusBadRequest, "forwarding to the server itself is not allowed")
		}
		// names that only mean something inside the provider's network (cloud metadata, local services)
		for _, suffix := range []string{".internal", ".local", ".localdomain", ".home.arpa", ".lan", ".intranet"} {
			if strings.HasSuffix(h, suffix) || h == strings.TrimPrefix(suffix, ".") {
				return "", errStatus(http.StatusBadRequest, "forward to a public name or an address - "+h+" is a name inside the provider's or a local network")
			}
		}
	}
	return net.JoinHostPort(h, strconv.Itoa(pn)), nil
}
