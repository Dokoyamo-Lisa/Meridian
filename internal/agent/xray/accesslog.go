package xray

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"meridian/internal/proto"
)

// AccessEntry is one parsed access log line.
type AccessEntry struct {
	TS      int64
	SrcIP   string
	Network string
	Host    string
	Port    int
	Inbound string
	Email   string
	OK      bool
}

// ParseAccess parses a line like
//
//	2026/10/06 15:04:05.123456 from 1.2.3.4:5678 accepted tcp:www.google.com:443 [n5 -> direct] email: s1.n5
func ParseAccess(line string) (AccessEntry, bool) {
	var e AccessEntry
	i := strings.Index(line, " from ")
	if i < 0 {
		return e, false
	}
	if t, err := time.ParseInLocation("2006/01/02 15:04:05.000000", strings.TrimSpace(line[:i]), time.Local); err == nil {
		e.TS = t.Unix()
	} else if t, err := time.ParseInLocation("2006/01/02 15:04:05", strings.TrimSpace(line[:i]), time.Local); err == nil {
		e.TS = t.Unix()
	} else {
		e.TS = time.Now().Unix()
	}
	rest := line[i+6:]
	src, rest, ok := strings.Cut(rest, " ")
	if !ok {
		return e, false
	}
	src = strings.TrimPrefix(strings.TrimPrefix(src, "tcp:"), "udp:")
	host, _, err := net.SplitHostPort(src)
	if err != nil {
		return e, false
	}
	if a, err := netip.ParseAddr(host); err == nil {
		e.SrcIP = a.Unmap().String() // one address, one spelling: "::ffff:1.2.3.4" is 1.2.3.4
	}
	status, rest, ok := strings.Cut(rest, " ")
	if !ok {
		return e, false
	}
	e.OK = status == "accepted"
	dst, rest, _ := strings.Cut(rest, " ")
	if nw, hp, ok := strings.Cut(dst, ":"); ok && (nw == "tcp" || nw == "udp") {
		e.Network = nw
		dst = hp
	} else {
		e.Network = "tcp"
	}
	if h, p, err := net.SplitHostPort(dst); err == nil {
		e.Host = strings.ToLower(h)
		e.Port, _ = strconv.Atoi(p)
	} else {
		e.Host = strings.ToLower(dst)
	}
	if j := strings.Index(rest, "["); j >= 0 {
		if k := strings.Index(rest[j:], "]"); k > 0 {
			detour := rest[j+1 : j+k]
			for _, sep := range []string{" -> ", " >> ", " ==> "} {
				if in, _, ok := strings.Cut(detour, sep); ok {
					e.Inbound = in
					break
				}
			}
		}
	}
	if j := strings.LastIndex(rest, "email: "); j >= 0 {
		e.Email = strings.TrimSpace(rest[j+7:])
	}
	return e, true
}

// accessTail follows the access log. Rotation is lossless: the file is renamed, Xray is told
// to reopen its logs, and the renamed file is read to its end before it is deleted.
type accessTail struct {
	path   string
	api    *API
	f      *os.File
	r      *bufio.Reader
	offset int64
}

const rotateAt = 4 << 20

func (t *accessTail) open() error {
	if t.f != nil {
		return nil
	}
	f, err := os.Open(t.path)
	if err != nil {
		return err
	}
	t.f, t.r, t.offset = f, bufio.NewReaderSize(f, 64<<10), 0
	return nil
}

// read returns complete new lines.
func (t *accessTail) read(fn func(string)) {
	if err := t.open(); err != nil {
		return
	}
	t.drain(fn)
	// the file was replaced underneath us (removed, or Xray recreated it): finish and reopen
	if cur, err := os.Stat(t.path); err == nil {
		if st, err := t.f.Stat(); err == nil && !os.SameFile(cur, st) {
			t.f.Close()
			t.f = nil
			if t.open() == nil {
				t.drain(fn)
			}
			return
		}
	}
	if st, err := t.f.Stat(); err == nil && st.Size() > rotateAt {
		t.rotate(fn)
	}
}

func (t *accessTail) drain(fn func(string)) {
	for {
		line, err := t.r.ReadString('\n')
		if err == io.EOF {
			if line != "" {
				// partial line: rewind so it is read again once complete
				_, _ = t.f.Seek(t.offset, io.SeekStart)
				t.r.Reset(t.f)
			}
			return
		}
		if err != nil {
			return
		}
		t.offset += int64(len(line))
		fn(strings.TrimRight(line, "\r\n"))
	}
}

func (t *accessTail) rotate(fn func(string)) {
	old := t.path + ".1"
	if err := os.Rename(t.path, old); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = t.api.RestartLogger(ctx)
	cancel()
	time.Sleep(200 * time.Millisecond)
	t.drain(fn) // the rest of the renamed file
	t.f.Close()
	t.f = nil
	os.Remove(old)
}

// ---------------------------------------------------------------- aggregation

type ipKey struct {
	sub, node int64
	ip        string
}

type destKey struct {
	sub, node int64
	host      string
	port      int
	net       string
}

// Aggregate collects access log entries between two reports.
type Aggregate struct {
	IPs   map[ipKey]*proto.IPSeen
	Dests map[destKey]*proto.DestSeen
}

func newAggregate() *Aggregate {
	return &Aggregate{IPs: map[ipKey]*proto.IPSeen{}, Dests: map[destKey]*proto.DestSeen{}}
}

// add counts an entry for the user and node it belongs to (see Engine.accessIdentity).
func (g *Aggregate) add(e AccessEntry, sub, node int64, connLog, destLog bool) {
	if connLog && e.SrcIP != "" {
		k := ipKey{sub, node, e.SrcIP}
		s := g.IPs[k]
		if s == nil {
			s = &proto.IPSeen{Sub: sub, Node: node, IP: e.SrcIP, First: e.TS, Last: e.TS}
			g.IPs[k] = s
		}
		s.Conns++
		s.First = min(s.First, e.TS)
		s.Last = max(s.Last, e.TS)
	}
	if destLog && e.Host != "" {
		k := destKey{sub, node, e.Host, e.Port, e.Network}
		d := g.Dests[k]
		if d == nil {
			d = &proto.DestSeen{Sub: sub, Node: node, Host: e.Host, Port: e.Port, Net: e.Network}
			g.Dests[k] = d
		}
		d.Conns++
		d.Last = max(d.Last, e.TS)
	}
}
