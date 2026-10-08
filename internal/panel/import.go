package panel

// Importing what already runs on a server. The agent scans (read only) for Xray, V2Ray, 3x-ui and
// x-ui, sing-box and Hysteria2; the admin picks protocols to bring over. Every user of a protocol
// becomes (or is matched to) a Meridian user and keeps exactly the credentials and keys they had,
// so devices that already use them keep working. With "take over", the old service is stopped
// and Meridian serves the same port; without it, the import uses a new port and nothing is stopped.

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"meridian/internal/agent/scan"
	"meridian/internal/db"
	"meridian/internal/proto"
	"meridian/internal/subgen"
)

// ---------------------------------------------------------------- scan results

// storeScan keeps the raw scan of a server (with credentials, for the import) and leaves only a
// summary in the action's output, which the API shows.
func storeScan(tx *sql.Tx, serverID int64, ar proto.ActionResult) string {
	var res scan.Result
	if err := json.Unmarshal([]byte(ar.Output), &res); err != nil {
		return "the scan result could not be read"
	}
	if _, err := tx.Exec(`INSERT INTO server_scans (server_id, at, data) VALUES (?, ?, ?)
		ON CONFLICT(server_id) DO UPDATE SET at = excluded.at, data = excluded.data`, serverID, now(), ar.Output); err != nil {
		slog.Error("store scan", "err", err)
	}
	n := 0
	for _, f := range res.Found {
		n += len(f.Inbounds)
	}
	if len(res.Found) == 0 {
		return "No other proxy software found"
	}
	return fmt.Sprintf("Found %d program(s) with %d protocol(s) - see the import page", len(res.Found), n)
}

func (p *Panel) loadScan(ctx context.Context, serverID int64) (*scan.Result, int64, error) {
	var raw string
	var at int64
	if err := p.db.QueryRowContext(ctx, `SELECT at, data FROM server_scans WHERE server_id = ?`, serverID).Scan(&at, &raw); err != nil {
		return nil, 0, err
	}
	var res scan.Result
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, 0, err
	}
	return &res, at, nil
}

type scanView struct {
	At       int64           `json:"at" doc:"When the scan ran; 0 = never"`
	Found    []scanFoundView `json:"found"`
	Pending  bool            `json:"pending" doc:"A scan is running"`
	ActionID int64           `json:"action_id,omitempty"`
}

type scanFoundView struct {
	Software string            `json:"software"`
	Config   string            `json:"config"`
	Unit     string            `json:"unit"`
	Running  bool              `json:"running"`
	Error    string            `json:"error,omitempty"`
	Inbounds []scanInboundView `json:"inbounds"`
}

type scanInboundView struct {
	Tag        string   `json:"tag"`
	Protocol   string   `json:"protocol"`
	Port       int      `json:"port"`
	Label      string   `json:"label" doc:"How it would be named, e.g. REALITY or VMess WS"`
	Users      []string `json:"users" doc:"User names (credentials are not shown)"`
	Importable bool     `json:"importable"`
	Why        string   `json:"why,omitempty" doc:"Why it cannot be imported, or what changes"`
	Imported   bool     `json:"imported" doc:"Already imported to this server"`
}

func (p *Panel) apiServerScan(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if _, err := p.ownServer(r.Context(), a, id); err != nil {
		return err
	}
	v := scanView{Found: []scanFoundView{}}
	var aid int64
	if p.db.QueryRowContext(r.Context(), `SELECT id FROM actions WHERE server_id = ? AND kind = ? AND status = 'pending'
		ORDER BY id DESC LIMIT 1`, id, proto.ActionScan).Scan(&aid) == nil {
		v.Pending, v.ActionID = true, aid
	}
	res, at, err := p.loadScan(r.Context(), id)
	if err == nil {
		v.At = at
		imported := p.importedKeys(r.Context(), id)
		for _, f := range res.Found {
			fv := scanFoundView{Software: f.Software, Config: f.Config, Unit: f.Unit, Running: f.Running, Error: f.Error,
				Inbounds: []scanInboundView{}}
			for _, in := range f.Inbounds {
				iv := scanInboundView{Tag: in.Tag, Protocol: in.Protocol, Port: in.Port, Users: []string{}}
				for _, u := range in.Users {
					iv.Users = append(iv.Users, u.Name)
				}
				kind, raw, why, err := importSettings(in)
				if err != nil {
					iv.Why = err.Error()
				} else {
					iv.Importable, iv.Label, iv.Why = true, protocolLabel(kind, raw), why
				}
				iv.Imported = imported[importKey(f.Config, in.Tag, in.Port)]
				fv.Inbounds = append(fv.Inbounds, iv)
			}
			v.Found = append(v.Found, fv)
		}
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

func (p *Panel) apiStartScan(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownServer(r.Context(), a, id)
	if err != nil {
		return err
	}
	if !s.Online {
		return errStatus(http.StatusConflict, "the server is offline - the agent must be connected to scan")
	}
	res, err := p.db.Exec1(`INSERT INTO actions (server_id, kind, args, created_at, created_by) VALUES (?, ?, '{}', ?, ?)`,
		id, proto.ActionScan, now(), a.ID)
	if err != nil {
		return err
	}
	aid, _ := res.LastInsertId()
	p.touchServers(id)
	writeJSON(w, http.StatusAccepted, actionRef{ID: aid})
	return nil
}

// ---------------------------------------------------------------- mapping

func importKey(config, tag string, port int) string {
	return fmt.Sprintf("%s#%s#%d", config, tag, port)
}

func (p *Panel) importedKeys(ctx context.Context, serverID int64) map[string]bool {
	out := map[string]bool{}
	rows, err := p.db.QueryContext(ctx, `SELECT imported FROM nodes WHERE server_id = ? AND imported != ''`, serverID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil {
			out[k] = true
		}
	}
	return out
}

// importSettings turns a detected inbound into Meridian settings, keeping its keys. why says what
// changes on the way, if anything.
func importSettings(in scan.Inbound) (kind string, raw json.RawMessage, why string, err error) {
	if in.Note != "" {
		return "", nil, "", errors.New(in.Note)
	}
	if in.Port < 1 || in.Port > 65535 {
		return "", nil, "", errors.New("no usable port")
	}
	// an inbound that serves only this machine (behind nginx, or local software) is no protocol for
	// the internet: importing it would open it to everyone
	if a, err := netip.ParseAddr(strings.Trim(in.Listen, "[]")); err == nil && (a.IsLoopback() || a.IsLinkLocalUnicast()) {
		return "", nil, "", fmt.Errorf("it listens only on %s, behind another server or for local software - importing would open it to the internet", a)
	}
	switch in.Protocol {
	case "hysteria2":
		s := hy2Settings{SNI: nz(in.SNI, defaultSelfSignedName)}
		s.CertMode = certCustom
		switch {
		case in.ACME:
			s.CertMode = certACME
		case in.CertPEM != "" && in.KeyPEM != "":
			s.CertPEM, s.KeyPEM = in.CertPEM, in.KeyPEM
			if names, _, _ := certNames(in.CertPEM); len(names) > 0 && in.SNI == "" {
				s.SNI = names[0]
			}
			if s.certSettings.keepSelfSigned(s.SNI) {
				why = selfSignedKept
			}
		default:
			s.CertMode = certSelf
			why = "its certificate could not be read: Meridian makes a new self-signed one, so devices must refresh their subscription"
			if err := s.certSettings.settle(nil, s.SNI, "", ""); err != nil {
				return "", nil, "", err
			}
		}
		if in.ObfsPassword != "" {
			s.Obfs, s.ObfsPassword = true, in.ObfsPassword
		}
		if err := s.certSettings.check(s.SNI); err != nil {
			if s.CertMode == certCustom { // a certificate for another name still works for pinned clients
				s.CertMode = certSelf
				_ = s.certSettings.settle(nil, s.SNI, "", "")
				why = "its certificate does not match the server name: Meridian makes a new self-signed one"
			} else {
				return "", nil, "", err
			}
		}
		if s.CertMode == certCustom && s.CertExpires > 0 {
			why = joinWhy(why, frozenCert(s.CertExpires))
		}
		raw, err = json.Marshal(s)
		return subgen.KindHysteria2, raw, why, err
	case "vless", "vmess", "trojan", "shadowsocks", "socks", "http":
	default:
		return "", nil, "", fmt.Errorf("this panel does not run %s", in.Protocol)
	}
	kind = in.Protocol
	s := &xraySettings{Transport: nz(in.Transport, tRaw), Security: nz(in.Security, secNone), Path: in.Path,
		HostHeader: in.Host, ServiceName: in.Service, XHTTPMode: in.XHTTPMode, SNI: in.SNI, Fingerprint: "chrome"}
	if s.Transport == tXHTTP && s.XHTTPMode == "" {
		s.XHTTPMode = "auto"
	}
	if s.Security == secNone {
		s.Fingerprint = ""
	}
	switch s.Security {
	case secReality:
		s.Target, s.PrivateKey, s.ShortIDs = in.Target, in.PrivateKey, in.ShortIDs
		if k, err := base64.RawURLEncoding.DecodeString(s.PrivateKey); err == nil && len(k) == 32 {
			s.PublicKey = x25519Public(k)
		}
		if strings.HasPrefix(s.Target, "127.") || strings.HasPrefix(s.Target, "localhost") || strings.HasPrefix(s.Target, "[::1]") {
			s.OwnSite = true
			s.Target = strings.Replace(s.Target, "localhost", "127.0.0.1", 1)
		}
		// Meridian's links use the first short id: a non-empty one goes first; an empty one (devices
		// that use none) stays only when the inbound had it
		var ids []string
		empty := false
		for _, id := range s.ShortIDs {
			if id != "" {
				ids = append(ids, id)
			} else {
				empty = true
			}
		}
		if len(ids) > 0 {
			if empty {
				ids = append(ids, "")
			}
			s.ShortIDs = ids
		}
		if s.Target == "" && s.SNI != "" {
			s.Target = s.SNI + ":443"
		}
	case secTLS:
		switch {
		case in.ACME:
			s.CertMode = certACME
		case in.CertPEM != "" && in.KeyPEM != "":
			s.CertMode, s.CertPEM, s.KeyPEM = certCustom, in.CertPEM, in.KeyPEM
			if s.SNI == "" {
				if names, _, _ := certNames(in.CertPEM); len(names) > 0 {
					s.SNI = names[0]
				}
			}
			if s.certSettings.keepSelfSigned(s.SNI) {
				why = selfSignedKept
			}
		default:
			s.CertMode = certSelf
			s.SNI = nz(s.SNI, defaultSelfSignedName)
			why = "its certificate could not be read: Meridian makes a new self-signed one, so devices must refresh their subscription"
			if err := s.certSettings.settle(nil, s.SNI, "", ""); err != nil {
				return "", nil, "", err
			}
		}
	}
	// VLESS: the flow is per protocol in Meridian; take the one most users have (the others connect
	// with their new link)
	if kind == subgen.KindVLESS {
		flows := map[string]int{}
		for _, u := range in.Users {
			flows[u.Flow]++
		}
		best, n := "", -1
		for f, c := range flows {
			if c > n || (c == n && f > best) {
				best, n = f, c
			}
		}
		s.Flow = best
		var others []string
		for _, u := range in.Users {
			if u.Flow != best {
				others = append(others, u.Name)
			}
		}
		if len(others) > 0 {
			why = joinWhy(why, fmt.Sprintf("its users do not all use the same flow: Meridian has one per protocol (%s), so %s must refresh their subscription",
				nz(best, "no flow"), strings.Join(others, ", ")))
		}
	}
	if kind == subgen.KindShadowsocks {
		s.Method, s.ServerKey = in.Method, in.ServerKey
		if strings.HasPrefix(s.Method, "2022-") && len(in.Users) == 1 && in.Users[0].Password == in.ServerKey {
			// one key for everyone: such devices send no user of their own, which a protocol of many users needs
			why = joinWhy(why, "it served one key to all devices (single-user Shadowsocks 2022): Meridian gives each user a key of their own, so its devices must refresh their subscription")
		}
		if !strings.HasPrefix(s.Method, "2022-") {
			// classic: users carry their own passwords; the server key only guards the placeholder user
			s.ServerKey = randB64(16)
			for _, u := range in.Users {
				if u.Method != "" && u.Method != s.Method {
					return "", nil, "", errors.New("its users use different ciphers - Meridian needs one cipher per protocol")
				}
			}
		}
	}
	if kind == subgen.KindSOCKS {
		s.UDP = in.UDP
	}
	if err := s.check(kind); err != nil {
		return "", nil, "", err
	}
	if s.Security == secTLS && s.CertMode == certCustom && s.CertExpires > 0 {
		why = joinWhy(why, frozenCert(s.CertExpires))
	}
	raw, err = json.Marshal(s)
	return kind, raw, why, err
}

// frozenCert is what an import says about a certificate it read from files: whatever renewed them on
// the server (certbot, acme.sh) no longer reaches it.
func frozenCert(expires int64) string {
	return fmt.Sprintf("its certificate is copied as it is now and expires on %s - Meridian does not renew it: switch the protocol to Let's Encrypt or a shared certificate before then",
		time.Unix(expires, 0).UTC().Format("2006-01-02"))
}

func joinWhy(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

// ---------------------------------------------------------------- import

type importInput struct {
	Items []struct {
		Config string `json:"config" doc:"The config the scan found it in"`
		Tag    string `json:"tag"`
		Port   int    `json:"port"`
	} `json:"items"`
	TakeOver bool `json:"take_over" doc:"Stop the old service and serve the same ports (devices keep working). Without it the import uses new ports and nothing is stopped"`
	Confirm  bool `json:"confirm" doc:"Required with take_over: the old service's users are disconnected for a moment"`
}

type importResult struct {
	Nodes   []nodeView `json:"nodes"`
	Users   []subView  `json:"users" doc:"Users created by the import; generated passwords are shown once"`
	Matched int        `json:"matched" doc:"Detected users that matched an existing user by name"`
	Stopped []int64    `json:"stop_actions,omitempty" doc:"Actions stopping the old services; poll GET /api/actions/{id}"`
	Notes   []string   `json:"notes"`
}

func (p *Panel) apiImport(w http.ResponseWriter, r *http.Request, a *Account) error {
	ctx := r.Context()
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	srv, err := p.ownServer(ctx, a, id)
	if err != nil {
		return err
	}
	var in importInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if len(in.Items) == 0 {
		return errStatus(http.StatusBadRequest, "choose what to import")
	}
	if in.TakeOver && !in.Confirm {
		return errStatus(http.StatusBadRequest, "taking over stops the old service: send confirm=true")
	}
	res, _, err := p.loadScan(ctx, id)
	if err != nil {
		return errStatus(http.StatusConflict, "scan the server first")
	}
	nodes, err := p.nodesOf(ctx, id)
	if err != nil {
		return err
	}
	fwds, _ := p.forwardsOf(ctx, id)
	hostPorts := p.hostPorts(id)
	imported := p.importedKeys(ctx, id)

	type pick struct {
		found scan.Found
		in    scan.Inbound
	}
	var picks []pick
	units := map[string]bool{}
	for _, it := range in.Items {
		ok := false
		for _, f := range res.Found {
			if f.Config != it.Config {
				continue
			}
			for _, x := range f.Inbounds {
				if x.Tag == it.Tag && x.Port == it.Port {
					picks = append(picks, pick{f, x})
					ok = true
					if f.Unit != "" && f.Running {
						units[f.Unit] = true
					}
				}
			}
		}
		if !ok {
			return errStatus(http.StatusBadRequest, fmt.Sprintf("%s in %s is not in the last scan - scan again", it.Tag, it.Config))
		}
		if imported[importKey(it.Config, it.Tag, it.Port)] {
			return errStatus(http.StatusConflict, fmt.Sprintf("%s was imported already", it.Tag))
		}
	}
	if in.TakeOver && len(units) == 0 {
		return errStatus(http.StatusBadRequest, "nothing to take over: the chosen protocols do not run as a service right now")
	}

	out := importResult{Nodes: []nodeView{}, Users: []subView{}, Notes: []string{}}
	t := now()
	var made []*Node
	var newSubs []int64
	passwords := map[int64]string{}
	err = p.db.Write(ctx, func(tx *sql.Tx) error {
		// sign-in names handed out in this import: the database does not show them until it is done
		taken := map[string]bool{}
		freeLogin := func(name string) string {
			base := loginFromName(name)
			for i := 1; i <= 50; i++ {
				cand := base
				if i > 1 {
					cand = fmt.Sprintf("%s-%d", base, i)
				}
				if taken[cand] {
					continue
				}
				if l, err := p.cleanLogin(ctx, cand, 0); err == nil && l != "" {
					taken[l] = true
					return l
				}
			}
			return "" // no free name: the user gets no sign-in (one can be given later)
		}
		byName := map[string]int64{}
		rows, err := tx.Query(`SELECT id, name FROM subs WHERE account_id = ?`, srv.AccountID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var sid int64
			var name string
			if rows.Scan(&sid, &name) == nil {
				byName[strings.ToLower(name)] = sid
			}
		}
		rows.Close()
		all := append([]*Node{}, nodes...)
		for _, pk := range picks {
			kind, raw, why, err := importSettings(pk.in)
			if err != nil {
				return errStatus(http.StatusBadRequest, fmt.Sprintf("%s: %v", pk.in.Tag, err))
			}
			if why != "" {
				out.Notes = append(out.Notes, pk.in.Tag+": "+why)
			}
			port := pk.in.Port
			tcp, udp := nodeNets(kind, raw)
			enabled := true
			if in.TakeOver {
				enabled = false // turned on when the old service has stopped
				if msg := portConflict(port, tcp, udp, all, fwds, 0, 0, nil); msg != "" {
					return errStatus(http.StatusConflict, pk.in.Tag+": "+msg)
				}
			} else if msg := portConflict(port, tcp, udp, all, fwds, 0, 0, hostPorts); msg != "" {
				if port = p.pickPort(srv, kind, raw, "", all, fwds, hostPorts); port == 0 {
					return errStatus(http.StatusConflict, fmt.Sprintf("%s: port %d is in use, and %s", pk.in.Tag, pk.in.Port, noFreePort(srv)))
				}
				out.Notes = append(out.Notes, fmt.Sprintf("%s: port %d is in use, imported on port %d - devices must refresh their subscription", pk.in.Tag, pk.in.Port, port))
			}
			name := cleanName(pk.in.Tag, 40)
			res, err := tx.Exec(`INSERT INTO nodes (id, server_id, kind, name, port, enabled, settings, sort, created_at, updated_at, imported)
				VALUES (`+db.NextID("nodes")+`, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, kind, name, port, enabled, string(raw), len(all), t, t,
				importKey(pk.found.Config, pk.in.Tag, pk.in.Port))
			if err != nil {
				return err
			}
			nid, _ := res.LastInsertId()
			n := &Node{ID: nid, ServerID: id, Kind: kind, Name: name, Port: port, Enabled: enabled, Settings: raw}
			all = append(all, n)
			made = append(made, n)
			for _, u := range pk.in.Users {
				key := strings.ToLower(cleanName(u.Name, 64))
				if key == "" {
					continue
				}
				sid, ok := byName[key]
				if ok {
					if !contains64(newSubs, sid) {
						out.Matched++
						// a matched user limited to other servers gets this protocol: their devices keep working
						var raw string
						var sc Scope
						if tx.QueryRow(`SELECT scope FROM subs WHERE id = ?`, sid).Scan(&raw) == nil && raw != "" &&
							json.Unmarshal([]byte(raw), &sc) == nil && !sc.HasNode(id, nid) {
							sc.Nodes, sc.None = append(sc.Nodes, nid), false
							if _, err := tx.Exec(`UPDATE subs SET scope = ? WHERE id = ?`, sc.String(), sid); err != nil {
								return err
							}
							out.Notes = append(out.Notes, fmt.Sprintf("%s: %s could use only other servers - this protocol was added to their access", pk.in.Tag, cleanName(u.Name, 64)))
						}
					}
				} else {
					login := freeLogin(u.Name)
					pw := genPassword()
					h, _ := hashPassword(pw)
					if login == "" {
						h = ""
					}
					scope := Scope{Servers: []int64{id}}
					r, err := tx.Exec(`INSERT INTO subs (id, account_id, name, note, token, uuid, secret, scope, cycle_start, created_at,
						updated_at, login, password_hash) VALUES (`+db.NextID("subs")+`, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, srv.AccountID,
						cleanName(u.Name, 64), "Imported from "+pk.found.Software+" on "+srv.Name, randB64URL(18), newUUID(),
						randB64URL(24), scope.String(), t, t, t, login, h)
					if err != nil {
						return err
					}
					sid, _ = r.LastInsertId()
					byName[key] = sid
					newSubs = append(newSubs, sid)
					if login != "" {
						passwords[sid] = pw
					}
				}
				c := creds{ID: u.ID, Password: u.Password, Username: u.Username}
				if kind == subgen.KindSOCKS || kind == subgen.KindHTTP {
					// SOCKS5 and HTTP users are identified by their name in the stats
					if c.Username == "" {
						c.Username = u.Name
					}
				}
				if _, err := tx.Exec(`INSERT OR REPLACE INTO node_creds (node_id, sub_id, id, password, username) VALUES (?, ?, ?, ?, ?)`,
					nid, sid, c.ID, c.Password, c.Username); err != nil {
					return err
				}
			}
		}
		if in.TakeOver {
			var ids []int64
			for _, n := range made {
				ids = append(ids, n.ID)
			}
			for unit := range units {
				args, _ := json.Marshal(map[string]any{"unit": unit, "enable_nodes": ids})
				r, err := tx.Exec(`INSERT INTO actions (server_id, kind, args, created_at, created_by) VALUES (?, ?, ?, ?, ?)`,
					id, proto.ActionStopService, string(args), t, a.ID)
				if err != nil {
					return err
				}
				aid, _ := r.LastInsertId()
				out.Stopped = append(out.Stopped, aid)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, n := range made {
		out.Nodes = append(out.Nodes, viewOfNode(n, srv))
	}
	for _, sid := range newSubs {
		if s, err := p.subByID(ctx, sid); err == nil {
			v := p.subView(r, s, nil, false)
			v.Password = passwords[sid]
			out.Users = append(out.Users, *v)
		}
	}
	msg := fmt.Sprintf("Imported %d protocol(s) and %d new user(s) on %s", len(made), len(newSubs), srv.Name)
	if in.TakeOver {
		msg += " - taking over from the old service"
	}
	p.event(srv.AccountID, "warn", "imported", id, 0, a.ID, msg, nil)
	p.touchAccount(srv.AccountID)
	writeJSON(w, http.StatusCreated, out)
	return nil
}

// finishTakeover turns on the imported protocols once the old service has stopped.
func finishTakeover(tx *sql.Tx, srv *Server, ar proto.ActionResult) {
	var kind, args string
	if tx.QueryRow(`SELECT kind, args FROM actions WHERE id = ?`, ar.ID).Scan(&kind, &args) != nil || kind != proto.ActionStopService {
		return
	}
	var x struct {
		Unit  string  `json:"unit"`
		Nodes []int64 `json:"enable_nodes"`
	}
	_ = json.Unmarshal([]byte(args), &x)
	if !ar.OK {
		eventTx(tx, srv.AccountID, "warn", "takeover_failed", srv.ID, 0, fmt.Sprintf(
			"%s: could not stop %s (%s) - the imported protocols stay off; stop it by hand, then turn them on", srv.Name, x.Unit, ar.Output))
		return
	}
	for _, id := range x.Nodes {
		_, _ = tx.Exec(`UPDATE nodes SET enabled = 1, updated_at = ? WHERE id = ? AND server_id = ?`, now(), id, srv.ID)
	}
	eventTx(tx, srv.AccountID, "info", "takeover_done", srv.ID, 0, fmt.Sprintf(
		"%s: %s stopped - Meridian now serves the imported protocols on the same ports", srv.Name, x.Unit))
}
