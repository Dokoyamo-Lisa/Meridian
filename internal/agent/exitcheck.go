package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"meridian/internal/proto"
)

// Checking an external node from this server (proto.ActionCheckExit): can the server open a
// connection to it, how long does that take, and - for nodes behind TLS or REALITY - does a TLS
// handshake succeed with a valid certificate for its server name. Nothing is sent through it.
//
// What the panel asks is checked again here: the node is a name or an address, never this host,
// a link-local address (cloud metadata) or a multicast one - also when a name resolves there,
// which is checked on the address the connection is actually made to.

var exitHostRE = regexp.MustCompile(`^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

const exitCheckTimeout = 10 * time.Second

// exitRoots are the certificate authorities a TLS check trusts (nil: the system's; tests set theirs).
var exitRoots *x509.CertPool

// exitAddrOK refuses what a check must never reach (tests allow loopback).
var exitAddrOK = func(a netip.Addr) error {
	a = a.Unmap()
	switch {
	case a.IsLoopback(), a.IsUnspecified():
		return errors.New("the node's address is this server itself")
	case a.IsLinkLocalUnicast(), a.IsLinkLocalMulticast(), a.IsMulticast():
		return errors.New("the node's address is a link-local or multicast address")
	}
	return nil
}

func validExitHost(h string) bool {
	if len(h) == 0 || len(h) > 253 {
		return false
	}
	if a, err := netip.ParseAddr(h); err == nil {
		return a.Zone() == ""
	}
	return exitHostRE.MatchString(h)
}

// checkExit runs one check.
func checkExit(ctx context.Context, req proto.ExitCheck) proto.ExitResult {
	var res proto.ExitResult
	switch {
	case !validExitHost(req.Host):
		res.Error = "the node's address is not a host name or IP address"
		return res
	case req.Port < 1 || req.Port > 65535:
		res.Error = "the node's port is not between 1 and 65535"
		return res
	case req.TLS && (req.SNI == "" || !validExitHost(req.SNI)):
		res.Error = "the node's server name (SNI) is not a host name"
		return res
	}
	ctx, cancel := context.WithTimeout(ctx, exitCheckTimeout)
	defer cancel()
	d := &net.Dialer{Control: func(network, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return err
		}
		return exitAddrOK(ap.Addr())
	}}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(req.Host, strconv.Itoa(req.Port)))
	if err != nil {
		res.Error = "no connection: " + plainDialErr(err)
		return res
	}
	defer conn.Close()
	res.Addr = conn.RemoteAddr().String()
	if req.TLS {
		_ = conn.SetDeadline(time.Now().Add(exitCheckTimeout))
		tc := tls.Client(conn, &tls.Config{ServerName: req.SNI, MinVersion: tls.VersionTLS12, RootCAs: exitRoots})
		if err := tc.HandshakeContext(ctx); err != nil {
			res.MS = time.Since(start).Milliseconds()
			res.Error = fmt.Sprintf("connected, but the TLS handshake for %s failed: %s", req.SNI, plainDialErr(err))
			return res
		}
		res.TLS = true
	}
	res.OK, res.MS = true, max(time.Since(start).Milliseconds(), 1)
	return res
}

// plainDialErr is a connection error without Go's operation prefixes.
func plainDialErr(err error) string {
	var dns *net.DNSError
	switch {
	case errors.As(err, &dns):
		return "the name cannot be found (" + dns.Name + ")"
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, syscall.ETIMEDOUT):
		return "no answer within 10 seconds"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused - nothing listens on that port"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return "the address cannot be reached from this server"
	}
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		msg = msg[i+2:]
	}
	return msg
}
