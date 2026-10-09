package panel

// Servers with IPv6 only. Such a server reaches the panel - to install its agent and to report - only
// when the panel's public address has an IPv6 address (an AAAA record for its domain), unless its
// provider carries IPv6 to IPv4 for it (DNS64 and NAT64). Cores come from the panel's mirror first,
// so GitHub, which has no IPv6, is not needed. The add-server dialog and the page of a server without
// IPv4 say so when the panel has no IPv6 address.

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

// panelAddrCache keeps what the panel's public address resolves to, for a few minutes.
type panelAddrCache struct {
	mu      sync.Mutex
	host    string
	addrs   []netip.Addr
	known   bool // resolved (or an IP): addrs is what there is
	at      time.Time
	loading bool
}

type panelAddress struct {
	Host string   `json:"host" doc:"The host of the panel's public URL (Settings); empty when none is set"`
	IPv4 []string `json:"ipv4" doc:"Its IPv4 addresses"`
	IPv6 []string `json:"ipv6" doc:"Its IPv6 addresses (AAAA records)"`
	Note string   `json:"note,omitempty" doc:"What a server with IPv6 only needs, when the panel's address has no IPv6"`
}

// panelAddr resolves the host of the panel's public URL - at once when wait is true, otherwise from
// the last lookup (a new one starts in the background when that is old).
func (p *Panel) panelAddr(ctx context.Context, wait bool) panelAddress {
	host := urlHost(p.settings().PublicURL)
	out := panelAddress{Host: host, IPv4: []string{}, IPv6: []string{}}
	if host == "" {
		return out
	}
	var addrs []netip.Addr
	known := false
	if a, err := netip.ParseAddr(host); err == nil {
		addrs, known = []netip.Addr{a.Unmap()}, true
	} else {
		c := &p.dyn.panel
		c.mu.Lock()
		fresh := c.host == host && time.Since(c.at) < panelEvery
		addrs, known = c.addrs, c.known && c.host == host
		start := !fresh && !c.loading
		if start {
			c.loading = true
		}
		c.mu.Unlock()
		if start {
			load := func(ctx context.Context) ([]netip.Addr, bool) {
				got, _, temporary := resolveName(ctx, host)
				c.mu.Lock()
				defer c.mu.Unlock()
				c.loading = false
				if temporary && c.host == host {
					return c.addrs, c.known // no answer this time: keep what was known
				}
				c.host, c.addrs, c.known, c.at = host, got, !temporary, time.Now()
				return c.addrs, c.known
			}
			if wait {
				addrs, known = load(ctx)
			} else {
				go load(context.WithoutCancel(ctx))
			}
		}
	}
	for _, a := range addrs {
		if a.Is4() {
			out.IPv4 = append(out.IPv4, a.String())
		} else {
			out.IPv6 = append(out.IPv6, a.String())
		}
	}
	if known && len(out.IPv6) == 0 {
		out.Note = fmt.Sprintf("%s has no IPv6 address (no AAAA record), so a server with IPv6 only cannot reach the panel to install its agent or report - unless its provider carries IPv6 to IPv4 for it (NAT64). Give %s an AAAA record for the panel's IPv6 address, or let such a server reach the panel through another server that has both (a relay).", host, host)
	}
	return out
}

// panelIPv6Issue is the note for the page of a server without IPv4 that has not reached the panel
// (yet, or any more), when the panel's address has no IPv6: "" otherwise.
func (p *Panel) panelIPv6Issue(ctx context.Context, s *Server) string {
	noV4 := s.IPVersion == "ipv6" || (s.FirstSeenAt > 0 && s.IPv4 == "")
	if !noV4 || s.Online || s.caps().NAT64 {
		return ""
	}
	pa := p.panelAddr(ctx, false)
	if pa.Note == "" {
		return ""
	}
	return fmt.Sprintf("This server has no IPv4, and the panel's address %s has no IPv6 (no AAAA record): its agent cannot reach the panel, unless the provider carries IPv6 to IPv4 for it (NAT64). Give %s an AAAA record for the panel's IPv6 address, or let this server reach the panel through another server that has both (a relay).", pa.Host, pa.Host)
}

func (p *Panel) apiPanelAddress(w http.ResponseWriter, r *http.Request, a *Account) error {
	writeJSON(w, http.StatusOK, p.panelAddr(r.Context(), true))
	return nil
}
