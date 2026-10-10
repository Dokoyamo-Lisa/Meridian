package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"meridian/internal/proto"
	"meridian/internal/subgen"
)

// mieru, Snell and AnyTLS: their servers take one user each, so every user of such a protocol has
// their own process on the server, on their own port - from the protocol's port on, as many ports as
// the protocol has room for (soloSettings.Users). A user keeps their port; it is stored the moment
// they first need one (solo_ports) and freed when they lose the protocol. Usage, cuts and limits
// are then exact per user: their traffic is what passes their port, cutting them is stopping their
// process (agent solo/).

func isSolo(kind string) bool {
	return kind == subgen.KindMieru || kind == subgen.KindSnell || kind == subgen.KindAnyTLS
}

// soloSettings are mieru's and Snell's - and the part of AnyTLS's (anytlsSettings) that is the same.
type soloSettings struct {
	Users     int    `json:"users"`               // ports in the range: users at most
	Transport string `json:"transport,omitempty"` // mieru: tcp | udp
}

const (
	soloDefaultUsers = 50
	soloMaxUsers     = 1000
)

func parseSolo(raw json.RawMessage) soloSettings {
	var s soloSettings
	_ = json.Unmarshal(raw, &s)
	if s.Users < 1 {
		s.Users = soloDefaultUsers
	}
	return s
}

func (s *soloSettings) apply(kind string, in *protoInput) error {
	if in.Users != nil {
		if *in.Users < 1 || *in.Users > soloMaxUsers {
			return fmt.Errorf("users is 1 to %d: how many people the protocol has room for (each gets their own port)", soloMaxUsers)
		}
		s.Users = *in.Users
	}
	if kind == subgen.KindMieru {
		if in.Transport != nil {
			s.Transport = strings.ToLower(strings.TrimSpace(*in.Transport))
		}
		if s.Transport == "" {
			s.Transport = "tcp"
		}
		if s.Transport != "tcp" && s.Transport != "udp" {
			return fmt.Errorf("mieru runs over tcp or udp")
		}
	} else {
		s.Transport = ""
	}
	return nil
}

// soloRange is the ports of a protocol's users: first..last.
func soloRange(n *Node) (first, last int) {
	return n.Port, n.Port + parseSolo(n.Settings).Users - 1
}

// soloNets says what a protocol listens on: mieru one transport, Snell both (QUIC mode on UDP),
// AnyTLS TCP.
func soloNets(kind string, raw json.RawMessage) (tcp, udp bool) {
	switch kind {
	case subgen.KindSnell:
		return true, true
	case subgen.KindAnyTLS:
		return true, false
	}
	return parseSolo(raw).Transport != "udp", parseSolo(raw).Transport == "udp"
}

// soloConflict says why port cannot be used because a mieru, Snell or AnyTLS protocol's users'
// ports cover it, or returns "".
func soloConflict(port int, tcp, udp bool, bind string, nodes []*Node, skipNode int64) string {
	for _, n := range nodes {
		if n.ID == skipNode || !isSolo(n.Kind) {
			continue
		}
		if other := n.BindIP; bind != "" && other != "" && bind != other {
			continue
		}
		first, last := soloRange(n)
		t, u := soloNets(n.Kind, n.Settings)
		if port >= first && port <= last && ((t && tcp) || (u && udp)) {
			return fmt.Sprintf("port %d is one of the ports %s gives its users (%d to %d)", port, protocolLabel(n.Kind, n.Settings), first, last)
		}
	}
	return ""
}

// checkSoloRange says why a mieru, Snell or AnyTLS protocol cannot have its users' ports from port
// on.
func checkSoloRange(kind string, raw json.RawMessage, port int, bind string, nodes []*Node, fwds []*Forward, skip int64, hostPorts []int) error {
	users := parseSolo(raw).Users
	if port < 1024 || port+users-1 > 65535 {
		return errStatus(http.StatusBadRequest, fmt.Sprintf("%s needs %d ports from its port on: pick a port from 1024 to %d", labelOf(kind), users, 65536-users))
	}
	tcp, udp := soloNets(kind, raw)
	for q := port; q < port+users; q++ {
		if msg := portConflictAt(q, tcp, udp, bind, nodes, fwds, skip, 0, hostPorts); msg != "" {
			return errStatus(http.StatusConflict, fmt.Sprintf("%s gives each user their own port, from %d to %d: %s", labelOf(kind), port, port+users-1, msg))
		}
	}
	return nil
}

// soloServes says whether a mieru, Snell or AnyTLS protocol can serve a user at all. mieru over UDP
// cannot keep to a speed limit - its transfers stall under one, tried in the lab - so users with a
// limit do not get it (their links leave it out; mieru over TCP keeps to limits).
func soloServes(n *Node, s *Sub) bool {
	return !(n.Kind == subgen.KindMieru && parseSolo(n.Settings).Transport == "udp" && s.SpeedLimit > 0)
}

// soloCreds are a user's name and secret on a mieru, Snell or AnyTLS protocol.
func soloCreds(n *Node, s *Sub) (name, secret string) {
	return fmt.Sprintf("u%d", s.ID), derive(s.Secret, fmt.Sprintf("%s-n%d", n.Kind, n.ID))
}

// ensureSoloPorts gives every user in subs their port on protocol n (stored, so it stays theirs),
// and frees the ports of users who lost the protocol. Users beyond the range get none.
func (p *Panel) ensureSoloPorts(ctx context.Context, n *Node, subs []*Sub, allUsers bool) (map[int64]int, error) {
	out := map[int64]int{}
	made := false
	first, last := soloRange(n)
	err := p.db.Write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT sub_id, port FROM solo_ports WHERE node_id = ?`, n.ID)
		if err != nil {
			return err
		}
		have := map[int64]int{}
		used := map[int]bool{}
		for rows.Next() {
			var sub int64
			var port int
			if rows.Scan(&sub, &port) == nil {
				have[sub], used[port] = port, true
			}
		}
		rows.Close()
		want := map[int64]bool{}
		for _, s := range subs {
			want[s.ID] = true
		}
		// a port outside the range (it was made smaller) goes; with the whole list of users, so do
		// the ports of those who lost the protocol
		for sub, port := range have {
			if port < first || port > last || (allUsers && !want[sub]) {
				if _, err := tx.Exec(`DELETE FROM solo_ports WHERE node_id = ? AND sub_id = ?`, n.ID, sub); err != nil {
					return err
				}
				delete(have, sub)
				delete(used, port)
			}
		}
		next := first
		for _, s := range subs {
			if port, ok := have[s.ID]; ok {
				out[s.ID] = port
				continue
			}
			for next <= last && used[next] {
				next++
			}
			if next > last {
				continue // no room: the protocol's range is full
			}
			if _, err := tx.Exec(`INSERT INTO solo_ports (node_id, sub_id, port) VALUES (?, ?, ?)`, n.ID, s.ID, next); err != nil {
				return err
			}
			out[s.ID], used[next] = next, true
			made = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if made && !allUsers { // a port given for a link must reach the server too
		p.touchServers(n.ServerID)
	}
	return out, nil
}

// soloNode is a mieru, Snell or AnyTLS protocol for a server's state: its users with their ports.
// The users it has no room for are left out (and told about: soloFull).
func (p *Panel) soloNode(ctx context.Context, n *Node, all, serving []*Sub) (proto.SoloNode, error) {
	ss := parseSolo(n.Settings)
	out := proto.SoloNode{NodeID: n.ID, Kind: n.Kind, Transport: ss.Transport, Bind: n.BindIP, Users: []proto.SoloUser{}}
	// everyone who may use it keeps a port (a paused user, or one out of data, too: back on the same
	// port); only those it serves now run
	ports, err := p.ensureSoloPorts(ctx, n, usersOf(all, n), true)
	if err != nil {
		return out, err
	}
	for _, s := range serving {
		port, ok := ports[s.ID]
		if !ok || !soloServes(n, s) {
			continue
		}
		name, secret := soloCreds(n, s)
		out.Users = append(out.Users, proto.SoloUser{Sub: s.ID, Port: port, Name: name, Secret: secret})
	}
	slices.SortFunc(out.Users, func(a, b proto.SoloUser) int { return a.Port - b.Port })
	return out, nil
}

// soloEndpoint is a user's entry for a mieru, Snell or AnyTLS protocol, on their own port.
func soloEndpoint(n *Node, srv *Server, sub *Sub, port int, name string) subgen.Endpoint {
	user, secret := soloCreds(n, sub)
	e := subgen.Endpoint{NodeID: n.ID, Name: name, Kind: n.Kind, Server: srv.ShownName(), Country: srv.Country,
		Host: linkHost(n, srv), Port: port, Password: secret}
	switch n.Kind {
	case subgen.KindMieru:
		e.Username, e.Transport = user, parseSolo(n.Settings).Transport
	case subgen.KindSnell:
		e.Version = subgen.SnellVersion
	case subgen.KindAnyTLS:
		s := parseAnyTLS(n.Settings)
		e.SNI, e.Fingerprint = s.SNI, "chrome"
		if s.CertMode == certSelf || s.CertMode == "" { // apps that can pin it check exactly this certificate
			e.PinSHA256, e.CertPEM = s.CertSHA256, s.CertPEM
		}
	}
	return e
}

// anytlsCert is the certificate an AnyTLS protocol's users' servers present: its own PEMs (self-signed
// or the operator's), a shared one's current version, or a Let's Encrypt certificate the agent keeps.
// ok is false while a shared certificate is missing.
func anytlsCert(n *Node, sn *proto.SoloNode, shared map[int64]*Cert) bool {
	s := parseAnyTLS(n.Settings)
	switch s.CertMode {
	case certACME:
		sn.ACME = s.SNI
	case certShared:
		c := shared[s.CertID]
		if c == nil {
			return false
		}
		sn.CertPEM, sn.KeyPEM = c.CertPEM, c.KeyPEM
	default:
		sn.CertPEM, sn.KeyPEM = s.CertPEM, s.KeyPEM
	}
	return true
}
