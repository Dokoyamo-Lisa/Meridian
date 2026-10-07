package wg

import (
	"bufio"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// resolver is the DNS server WireGuard clients use. It forwards to the host's resolvers and
// remembers which name each client looked up for which address, so traffic to an address can be
// shown by name.
type resolver struct {
	addr     string
	upstream []string
	udp, tcp *dns.Server

	mu      sync.Mutex
	answers map[netip.Addr]map[netip.Addr]nameEntry
}

type nameEntry struct {
	name string
	exp  time.Time
}

func upstreams(self string) []string {
	var out []string
	f, err := os.Open("/etc/resolv.conf")
	if err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) >= 2 && fields[0] == "nameserver" && fields[1] != self {
				if ip := net.ParseIP(fields[1]); ip != nil {
					out = append(out, net.JoinHostPort(fields[1], "53"))
				}
			}
		}
	}
	if len(out) == 0 {
		out = []string{"1.1.1.1:53", "8.8.8.8:53"}
	}
	return out
}

func startResolver(addr string) (*resolver, error) {
	r := &resolver{addr: addr, upstream: upstreams(addr), answers: map[netip.Addr]map[netip.Addr]nameEntry{}}
	listen := net.JoinHostPort(addr, "53")
	pc, err := net.ListenPacket("udp", listen)
	if err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp", listen)
	if err != nil {
		pc.Close()
		return nil, err
	}
	r.udp = &dns.Server{PacketConn: pc, Handler: r}
	r.tcp = &dns.Server{Listener: l, Handler: r}
	go r.udp.ActivateAndServe()
	go r.tcp.ActivateAndServe()
	return r, nil
}

func (r *resolver) stop() {
	if r.udp != nil {
		r.udp.Shutdown()
	}
	if r.tcp != nil {
		r.tcp.Shutdown()
	}
}

func (r *resolver) ServeDNS(w dns.ResponseWriter, q *dns.Msg) {
	var client netip.Addr
	if a, ok := w.RemoteAddr().(*net.UDPAddr); ok {
		client, _ = netip.AddrFromSlice(a.IP)
	} else if a, ok := w.RemoteAddr().(*net.TCPAddr); ok {
		client, _ = netip.AddrFromSlice(a.IP)
	}
	client = client.Unmap()
	var resp *dns.Msg
	for _, up := range r.upstream {
		c := &dns.Client{Net: "udp", Timeout: 3 * time.Second}
		m, _, err := c.Exchange(q, up)
		if err == nil && m != nil && m.Truncated {
			c.Net = "tcp"
			m, _, err = c.Exchange(q, up)
		}
		if err == nil && m != nil {
			resp = m
			break
		}
	}
	if resp == nil {
		resp = new(dns.Msg)
		resp.SetRcode(q, dns.RcodeServerFailure)
	} else if len(q.Question) > 0 && client.IsValid() {
		name := strings.TrimSuffix(strings.ToLower(q.Question[0].Name), ".")
		r.record(client, name, resp.Answer)
	}
	_ = w.WriteMsg(resp)
}

func (r *resolver) record(client netip.Addr, name string, rrs []dns.RR) {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.answers[client]
	if m == nil {
		m = map[netip.Addr]nameEntry{}
		r.answers[client] = m
	}
	for _, rr := range rrs {
		var ip net.IP
		ttl := rr.Header().Ttl
		switch v := rr.(type) {
		case *dns.A:
			ip = v.A
		case *dns.AAAA:
			ip = v.AAAA
		default:
			continue
		}
		a, ok := netip.AddrFromSlice(ip)
		if !ok {
			continue
		}
		// keep names well past the TTL: connections often outlive it
		keep := max(time.Duration(ttl)*time.Second, 30*time.Minute)
		m[a.Unmap()] = nameEntry{name: name, exp: now.Add(keep)}
	}
	if len(m) > 20000 {
		for k, v := range m {
			if v.exp.Before(now) {
				delete(m, k)
			}
		}
	}
}

func (r *resolver) nameFor(client, dst netip.Addr) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m := r.answers[client]; m != nil {
		if e, ok := m[dst]; ok {
			return e.name
		}
	}
	return ""
}
