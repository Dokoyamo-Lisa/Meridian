package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The way to the panel. A server with a poor route to the panel can reach it through another server
// the operator chooses: the panel sends that relay's addresses (State.PanelVia), and every request
// to the panel - state, reports, the country list, downloads from the panel - is then dialled
// through it. The relay passes the bytes on unread: TLS still ends at the panel and is checked for
// the panel's own name, and the bodies are sealed besides. When the relay cannot be reached the
// agent goes directly, and the other way round; it remembers what worked and tells the panel
// (Live.PanelPath, Live.RelayError).

const (
	pathRelay  = "relay"
	pathDirect = "direct"
)

const (
	relayDialTimeout = 10 * time.Second // one attempt to reach a relay address: a dead relay costs seconds
	relayRetry       = time.Minute      // after the relay failed, directly first for this long...
	relayRetryMax    = 10 * time.Minute // ...doubling while it keeps failing, up to this
	maxVia           = 8
)

type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

type panelLink struct {
	host       string      // the panel's host:port, as its URL names it
	hostname   string      // the panel's name (or IP): what TLS checks
	secure     bool        // https
	tlsConf    *tls.Config // nil: the system's certificate authorities (tests trust their own)
	direct     *http.Transport
	dialDirect dialFunc

	mu       sync.Mutex
	via      []string
	gen      int             // counts changes of via: connections opened before one go
	relay    *http.Transport // dials the relay; nil without one
	path     string          // what reached the panel last: pathRelay or pathDirect
	relayErr string          // why the relay failed last; "" while it works
	retryAt  time.Time       // the relay failed: directly first until then
	backoff  time.Duration
}

// newPanelLink prepares the way to the panel at panelURL. dial replaces the direct dialler (tests).
func newPanelLink(panelURL string, tlsConf *tls.Config, dial dialFunc) (*panelLink, error) {
	u, err := url.Parse(panelURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("the panel address %q is not an http:// or https:// URL", panelURL)
	}
	l := &panelLink{host: hostPort(u), hostname: u.Hostname(), secure: u.Scheme == "https", tlsConf: tlsConf, dialDirect: dial}
	l.direct = http.DefaultTransport.(*http.Transport).Clone()
	if tlsConf != nil {
		l.direct.TLSClientConfig = tlsConf.Clone()
	}
	if dial != nil {
		l.direct.DialContext = dial
	} else {
		d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		l.dialDirect = d.DialContext
	}
	return l, nil
}

// hostPort is a URL's host with its port, the default one when it names none.
func hostPort(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// setVia takes the relay addresses from the state: a new list starts afresh, through the relay
// first; an empty one means directly.
func (l *panelLink) setVia(list []string) {
	via := cleanVia(list)
	l.mu.Lock()
	defer l.mu.Unlock()
	if slices.Equal(via, l.via) {
		return
	}
	old := l.relay
	l.via, l.relay = via, nil
	if len(via) > 0 {
		l.relay = l.relayTransport(via)
	}
	l.gen++
	l.path, l.relayErr, l.retryAt, l.backoff = "", "", time.Time{}, 0
	if old != nil {
		old.CloseIdleConnections()
	}
}

// generation changes whenever the relay does.
func (l *panelLink) generation() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.gen
}

// dial opens a connection to the panel's host and port one way: through the relay, or directly.
func (l *panelLink) dial(ctx context.Context, route string) (net.Conn, error) {
	l.mu.Lock()
	via := l.via
	l.mu.Unlock()
	if route == pathRelay {
		if len(via) == 0 {
			return nil, errors.New("no relay is set")
		}
		return dialVia(ctx, via)
	}
	return l.dialDirect(ctx, "tcp", l.host)
}

// tlsFor is the TLS a connection of its own to the panel uses: the panel's own name, checked as for
// any request, and HTTP/1.1 (a WebSocket starts as one).
func (l *panelLink) tlsFor() *tls.Config {
	c := &tls.Config{MinVersion: tls.VersionTLS12}
	if l.tlsConf != nil {
		c = l.tlsConf.Clone()
	}
	c.ServerName, c.NextProtos = l.hostname, []string{"http/1.1"}
	return c
}

// cleanVia keeps the well-formed relay addresses, each once: "ip:port", or "name:port" for a relay
// whose IP changes (dynamic DNS) - the name is looked up when it is dialled.
func cleanVia(list []string) []string {
	var out []string
	for _, s := range list {
		v := ""
		if ap, err := netip.ParseAddrPort(strings.TrimSpace(s)); err == nil {
			if ap.Port() == 0 || ap.Addr().IsUnspecified() || ap.Addr().IsMulticast() || ap.Addr().Zone() != "" {
				continue
			}
			v = netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()).String()
		} else if host, port, err := net.SplitHostPort(strings.TrimSpace(s)); err == nil && hostName(host) {
			if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
				continue
			}
			v = net.JoinHostPort(strings.ToLower(host), port)
		}
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
		if len(out) == maxVia {
			break
		}
	}
	return out
}

// hostName says whether h is a domain name: letters, digits, hyphens and underscores in dotted
// labels, not all digits at the end (that would be a malformed IP).
func hostName(h string) bool {
	if len(h) == 0 || len(h) > 253 {
		return false
	}
	labels := strings.Split(strings.ToLower(h), ".")
	for _, l := range labels {
		if len(l) == 0 || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := 0; i < len(l); i++ {
			if c := l[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	return strings.Trim(labels[len(labels)-1], "0123456789") != ""
}

// relayTransport carries the panel's connections - only those - to the relay, and TLS through it to
// the panel under the panel's own name (the transport takes it from the request's URL).
func (l *panelLink) relayTransport(via []string) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil // the relay is dialled itself
	if l.tlsConf != nil {
		t.TLSClientConfig = l.tlsConf.Clone()
	}
	host := l.host
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != host {
			return nil, fmt.Errorf("the relay carries the panel's connections only, not %s", addr)
		}
		return dialVia(ctx, via)
	}
	return t
}

// dialVia connects to the first relay address that answers.
func dialVia(ctx context.Context, via []string) (net.Conn, error) {
	var errs []error
	for _, v := range via {
		d := net.Dialer{Timeout: relayDialTimeout}
		c, err := d.DialContext(ctx, "tcp", v)
		if err == nil {
			return c, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.Join(errs...)
}

// routes are the ways to the panel in the order to try them: the relay first when there is one,
// unless it failed a moment ago.
func (l *panelLink) routes() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.relay == nil:
		return []string{pathDirect}
	case time.Now().Before(l.retryAt):
		return []string{pathDirect, pathRelay}
	}
	return []string{pathRelay, pathDirect}
}

func (l *panelLink) transport(route string) http.RoundTripper {
	l.mu.Lock()
	defer l.mu.Unlock()
	if route == pathRelay && l.relay != nil {
		return l.relay
	}
	return l.direct
}

// worked notes that a route reached the panel.
func (l *panelLink) worked(route string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if route == pathRelay && l.relay == nil {
		return // the relay was dropped meanwhile
	}
	l.path = route
	if route == pathRelay {
		l.relayErr, l.retryAt, l.backoff = "", time.Time{}, 0
	}
}

// failed notes that a route did not reach the panel. A failed relay is tried again later, first
// after relayRetry, then after longer and longer pauses while it keeps failing.
func (l *panelLink) failed(route string, err error) {
	if route != pathRelay {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.relay == nil {
		return
	}
	l.relayErr = relayProblem(err)
	l.backoff = min(max(2*l.backoff, relayRetry), relayRetryMax)
	l.retryAt = time.Now().Add(l.backoff)
}

// status is how the agent reaches the panel, for the reports: the way that worked last, and why the
// relay failed when it went directly instead.
func (l *panelLink) status() (path, relayErr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.relay == nil {
		return l.path, ""
	}
	return l.path, l.relayErr
}

// RoundTrip carries downloads from the panel (an agent binary, cores from its mirror) the way the
// panel's requests go, falling over to the other way; anything else goes out directly.
func (l *panelLink) RoundTrip(req *http.Request) (*http.Response, error) {
	if hostPort(req.URL) != l.host {
		return l.direct.RoundTrip(req)
	}
	var errs []error
	for i, route := range l.routes() {
		r := req
		if i > 0 { // another try needs its own copy of the request and its body
			if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
				break
			}
			r = req.Clone(req.Context())
			if req.GetBody != nil {
				body, err := req.GetBody()
				if err != nil {
					break
				}
				r.Body = body
			}
		}
		resp, err := l.transport(route).RoundTrip(r)
		if err == nil {
			l.worked(route)
			return resp, nil
		}
		l.failed(route, err)
		errs = append(errs, routeErr(route, err))
		if req.Context().Err() != nil {
			break
		}
	}
	return nil, errors.Join(errs...)
}

// routeErr says which way an error happened on, when there are two.
func routeErr(route string, err error) error {
	if route == pathRelay {
		return fmt.Errorf("through the relay: %w", err)
	}
	return fmt.Errorf("directly: %w", err)
}

// relayProblem words why the relay failed, for the panel's server page.
func relayProblem(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "the relay server refused the connection - is its agent running and the port open in its firewall?"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, syscall.ECONNRESET):
		return "the relay closed the connection - it does not know this server's address yet, or cannot reach the panel itself"
	case errors.As(err, &ne) && ne.Timeout():
		return "no answer from the relay server"
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err // without the request's address
	}
	msg := strings.Join(strings.Fields(err.Error()), " ")
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return msg
}
