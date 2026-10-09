package agent

import (
	"context"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"

	"meridian/internal/proto"
)

func TestCheckExit(t *testing.T) {
	// what a check must never reach, even when a name resolves there
	for _, h := range []string{"127.0.0.1", "::1", "169.254.169.254", "fe80::1", "0.0.0.0", "localhost"} {
		res := checkExit(context.Background(), proto.ExitCheck{Host: h, Port: 443})
		if res.OK || res.Error == "" {
			t.Errorf("%s: %+v", h, res)
		}
	}
	for _, req := range []proto.ExitCheck{
		{Host: "bad host", Port: 443},
		{Host: "example.com", Port: 0},
		{Host: "example.com", Port: 443, TLS: true},
		{Host: "example.com", Port: 443, TLS: true, SNI: "a b"},
		{Host: strings.Repeat("a.", 130) + "com", Port: 443},
	} {
		if res := checkExit(context.Background(), req); res.OK || res.Error == "" {
			t.Errorf("%+v: %+v", req, res)
		}
	}

	// against a local server, with loopback allowed for the test
	old := exitAddrOK
	exitAddrOK = func(netip.Addr) error { return nil }
	defer func() { exitAddrOK = old }()
	ts := httptest.NewTLSServer(http.NotFoundHandler()) // its certificate is for example.com
	defer ts.Close()
	host, port, _ := net.SplitHostPort(ts.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	if res := checkExit(context.Background(), proto.ExitCheck{Host: host, Port: p}); !res.OK || res.MS < 1 || res.TLS || res.Addr == "" {
		t.Errorf("TCP: %+v", res)
	}
	// the test server's certificate is not trusted here: the handshake reports it (and is never skipped)
	if res := checkExit(context.Background(), proto.ExitCheck{Host: host, Port: p, TLS: true, SNI: "example.com"}); res.OK || res.TLS ||
		!strings.Contains(res.Error, "TLS handshake for example.com failed") {
		t.Errorf("untrusted certificate: %+v", res)
	}
	// trusted, for its name and no other
	pool := x509.NewCertPool()
	pool.AddCert(ts.Certificate())
	exitRoots = pool
	defer func() { exitRoots = nil }()
	if res := checkExit(context.Background(), proto.ExitCheck{Host: host, Port: p, TLS: true, SNI: "example.com"}); !res.OK || !res.TLS {
		t.Errorf("trusted certificate: %+v", res)
	}
	if res := checkExit(context.Background(), proto.ExitCheck{Host: host, Port: p, TLS: true, SNI: "www.example.org"}); res.OK ||
		!strings.Contains(res.Error, "www.example.org") {
		t.Errorf("another name: %+v", res)
	}
	// nothing listening
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := l.Addr().(*net.TCPAddr).Port
	l.Close()
	if res := checkExit(context.Background(), proto.ExitCheck{Host: "127.0.0.1", Port: closed}); res.OK || !strings.Contains(res.Error, "refused") {
		t.Errorf("closed port: %+v", res)
	}
}
