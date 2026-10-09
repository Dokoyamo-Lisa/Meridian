package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"meridian/internal/proto"
	"meridian/internal/subgen"
)

// metricsSaved remembers when each server last wrote a metrics point (one per minute).
var (
	metricsMu    sync.Mutex
	metricsSaved = map[int64]int64{}
)

// ingest applies an agent report. Accumulating data (traffic, IPs, destinations) is applied at
// most once per batch sequence number, so a retried report never double counts.
func (p *Panel) ingest(ctx context.Context, srv *Server, rep *proto.Report) (int64, error) {
	t := now()
	set := p.settings()
	if rep.Live != nil {
		sanitizeLive(rep.Live)
	}
	var ack int64
	var events []func(tx *sql.Tx)
	var counted []proto.UserTraffic // the traffic this report added
	recompile, ipMoved, sites := false, false, false

	err := p.db.Write(ctx, func(tx *sql.Tx) error {
		// a new installation of the agent starts its own sequence
		lastSeq := srv.LastSeq
		if rep.Instance != "" && rep.Instance != srv.InstanceID {
			lastSeq = 0
			if _, err := tx.Exec(`UPDATE servers SET instance_id = ?, last_seq = 0 WHERE id = ?`, rep.Instance, srv.ID); err != nil {
				return err
			}
			if srv.InstanceID != "" {
				events = append(events, func(tx *sql.Tx) {
					eventTx(tx, srv.AccountID, "info", "agent_reinstalled", srv.ID, 0,
						fmt.Sprintf("Agent on %s was reinstalled - same server, same configuration", srv.Name))
				})
			}
		}

		if h := rep.Hello; h != nil {
			sanitizeHello(h)
			// a lookup of the public address that failed this time is no new address: keep the known one
			// (unless the agent says the host has no address of that kind any more)
			if h.IPv4 == "" && !h.IPv4Gone {
				h.IPv4 = srv.IPv4
			}
			if h.IPv6 == "" && !h.IPv6Gone {
				h.IPv6 = srv.IPv6
			}
			caps, _ := json.Marshal(h.Caps)
			country, city, lat, lon := p.locateServer(srv, h)
			first := srv.FirstSeenAt
			if first == 0 {
				first = t
				// the first contact may unlock a usable address for subscriptions, for proxy passes that
				// lead here, and for other servers' country-rule exceptions
				recompile, ipMoved = true, true
				events = append(events, func(tx *sql.Tx) {
					eventTx(tx, srv.AccountID, "info", "server_connected", srv.ID, 0,
						fmt.Sprintf("%s connected for the first time (%s, %s)", srv.Name, h.OS, h.IPv4))
				})
			} else if moved := addrMoves(srv, h); len(moved) > 0 {
				// a new, new kind of or lost public IPv4 or IPv6 (see addresses.go)
				recompile, ipMoved = true, true
				events = append(events, moved...)
			}
			if _, err := tx.Exec(`UPDATE servers SET agent_version = ?, hostname = ?, os = ?, kernel = ?, arch = ?,
				cpu_model = ?, cpu_cores = ?, mem_total = ?, disk_total = ?, ipv4 = ?, ipv6 = ?, caps = ?, boot_time = ?,
				agent_started_at = ?, country = ?, city = ?, lat = ?, lon = ?, first_seen_at = ? WHERE id = ?`,
				h.AgentVersion, h.Hostname, h.OS, h.Kernel, h.Arch, h.CPUModel, h.CPUCores, int64(h.MemTotal),
				int64(h.DiskTotal), h.IPv4, h.IPv6, string(caps), h.BootTime, h.StartedAt, country, city, lat, lon,
				first, srv.ID); err != nil {
				return err
			}
			if h.Virt != "" { // agents before 1.0 do not say
				if _, err := tx.Exec(`UPDATE servers SET virt = ? WHERE id = ?`, h.Virt, srv.ID); err != nil {
					return err
				}
			}
			// agents before 0.6 do not list their addresses: keep what is known
			if h.Addrs != nil {
				addrs, _ := json.Marshal(h.Addrs)
				if _, err := tx.Exec(`UPDATE servers SET addrs = ? WHERE id = ?`, string(addrs), srv.ID); err != nil {
					return err
				}
			}
			if settleAgentUpgrades(tx, srv, h) { // an upgrade whose report was lost (agentupgrades.go)
				recompile = true
			}
			switch {
			case srv.BootTime != 0 && h.BootTime-srv.BootTime > 120: // (the boot time drifts a little with clock corrections)
				events = append(events, func(tx *sql.Tx) {
					eventTx(tx, srv.AccountID, "warn", "server_rebooted", srv.ID, 0,
						fmt.Sprintf("%s restarted: the machine booted again, and its protocols were down until it was back", srv.Name))
				})
			case srv.AgentStartedAt != 0 && h.StartedAt != srv.AgentStartedAt:
				events = append(events, func(tx *sql.Tx) {
					eventTx(tx, srv.AccountID, "info", "agent_restarted", srv.ID, 0,
						fmt.Sprintf("Agent on %s restarted (traffic was not affected)", srv.Name))
				})
			}
		}

		// presence
		if !srv.Online {
			events = append(events, func(tx *sql.Tx) {
				if srv.StatusChangedAt > 0 {
					eventTx(tx, srv.AccountID, "info", "server_online", srv.ID, 0,
						fmt.Sprintf("%s is back online after %s", srv.Name, humanDuration(t-srv.StatusChangedAt)))
				}
			})
			if _, err := tx.Exec(`UPDATE servers SET online = 1, status_changed_at = ? WHERE id = ?`, t, srv.ID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`UPDATE servers SET last_seen_at = ? WHERE id = ?`, t, srv.ID); err != nil {
			return err
		}

		if b := rep.Batch; b != nil {
			ack = b.Seq
			if b.Seq > lastSeq {
				if err := p.applyBatch(tx, srv, b, set); err != nil {
					return err
				}
				counted = b.Traffic
				if _, err := tx.Exec(`UPDATE servers SET last_seq = ? WHERE id = ?`, b.Seq, srv.ID); err != nil {
					return err
				}
			}
		}

		if a := rep.Applied; a != nil {
			errs := nameProtocols(tx, srv.ID, strings.Join(a.Errors, "\n"))
			pending := nameProtocols(tx, srv.ID, strings.Join(a.Pending, "; "))
			if errs != srv.ApplyErrors && errs != "" {
				events = append(events, func(tx *sql.Tx) {
					eventTx(tx, srv.AccountID, "warn", "apply_failed", srv.ID, 0, fmt.Sprintf("%s: %s", srv.Name, firstLine(errs)))
				})
			}
			if _, err := tx.Exec(`UPDATE servers SET applied_rev = ?, apply_errors = ?, pending_restart = ? WHERE id = ?`,
				a.Rev, errs, pending, srv.ID); err != nil {
				return err
			}
		}

		for _, ar := range rep.ActionResults {
			status := "done"
			if !ar.OK {
				status = "failed"
			}
			var kind string
			_ = tx.QueryRow(`SELECT kind FROM actions WHERE id = ? AND server_id = ? AND status = 'pending'`, ar.ID, srv.ID).Scan(&kind)
			output := ar.Output
			if kind == proto.ActionScan && ar.OK {
				// the scan carries credentials: keep it apart, show only a summary
				output = storeScan(tx, srv.ID, ar)
				ar.Output = output
			}
			res, err := tx.Exec(`UPDATE actions SET status = ?, output = ?, done_at = ? WHERE id = ? AND server_id = ? AND status = 'pending'`,
				status, truncate(output, 4000), t, ar.ID, srv.ID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				recompile = true
				sites = sites || kind == proto.ActionCheckTarget // a switched site: passes through it follow
				if err := p.applyTargetResult(tx, srv, ar); err != nil {
					slog.Warn("target result", "err", err)
				}
				if kind == proto.ActionStopService {
					finishTakeover(tx, srv, ar)
				}
				ar := ar
				msg, ok := firstLine(ar.Output), true
				if kind == proto.ActionCheckTarget && ar.OK {
					msg, ok = targetSummary(ar.Output) // automatic checks have their own event
				}
				if ok {
					events = append(events, func(tx *sql.Tx) {
						lvl := "info"
						if !ar.OK {
							lvl = "warn"
						}
						eventTx(tx, srv.AccountID, lvl, "action_"+status, srv.ID, 0, fmt.Sprintf("%s: %s", srv.Name, msg))
					})
				}
			}
		}

		if lv := rep.Live; lv != nil {
			if c, ok := lv.Cores["xray"]; ok && c.Version != "" && c.Version != srv.XrayVersion {
				if _, err := tx.Exec(`UPDATE servers SET xray_version = ? WHERE id = ?`, c.Version, srv.ID); err != nil {
					return err
				}
			}
			metricsMu.Lock()
			due := t/60 != metricsSaved[srv.ID]/60 // the first report of each minute: one row a minute, none skipped
			if due {
				metricsSaved[srv.ID] = t
			}
			metricsMu.Unlock()
			if due {
				online := liveIPs(lv.Online)
				sy := lv.Sys
				if _, err := tx.Exec(`INSERT INTO server_metrics (server_id, ts, cpu, mem_used, swap_used, disk_used, load1,
					load5, load15, rx_rate, tx_rate, disk_read, disk_write, tcp, udp, online, temp)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
					ON CONFLICT(server_id, ts) DO UPDATE SET cpu = excluded.cpu, mem_used = excluded.mem_used, swap_used = excluded.swap_used,
					disk_used = excluded.disk_used, load1 = excluded.load1, load5 = excluded.load5, load15 = excluded.load15,
					rx_rate = excluded.rx_rate, tx_rate = excluded.tx_rate, disk_read = excluded.disk_read, disk_write = excluded.disk_write,
					tcp = excluded.tcp, udp = excluded.udp, online = excluded.online, temp = excluded.temp`, srv.ID, t/60*60, sy.CPU, int64(sy.MemUsed), int64(sy.SwapUsed),
					int64(sy.DiskUsed), sy.Load1, sy.Load5, sy.Load15, sy.RXRate, sy.TXRate, max(sy.DiskRead, 0), max(sy.DiskWrite, 0), sy.TCP, sy.UDP,
					online, hottest(sy.Temps)); err != nil {
					return err
				}
			}
		}
		for _, ev := range events {
			ev(tx)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	p.ingestHealth(ctx, srv, rep.Health) // health checks (health.go)
	if rep.Live != nil {
		p.live.put(srv.ID, *rep.Live)
		p.checkIPLimits(ctx, srv.AccountID)
	}
	p.relayAddrsChanged(ctx, srv, rep.Hello) // its relay and the servers it relays follow its addresses (relay.go)
	if len(counted) > 0 {                    // a user past a protocol's limit that stops it: this server stops serving them (nodequota.go)
		if stop := p.stopModeSubs(ctx, srv.AccountID); len(stop) > 0 {
			for _, u := range counted {
				if stop[u.Sub] && p.nodeQuotaCrossed(ctx, u.Sub, u.Node, u.Up, u.Down) {
					recompile = true
				}
			}
		}
	}
	if recompile {
		p.touchServers(srv.ID)
	}
	if ipMoved { // passes, rules and forwards on other servers connect to this one's IP; country rules let it in
		p.addressMoved(srv)
	} else if sites { // an exit's camouflage may have changed: the protocols passing through it follow
		p.touchServers(p.passEntriesOf(ctx, srv.ID)...)
		p.touchRoutes(ctx, srv.AccountID)
	}
	return ack, nil
}

// locateServer says where a server is, unless its place was set by hand. An IP set by hand as the
// server's address is where devices connect, so DB-IP is asked there (a local lookup, on every
// hello: a place found elsewhere before is corrected). Otherwise the agent's public address counts
// (IPv4, or IPv6 on an IPv6-only host): the IP database is asked again when it changed or the
// place is still incomplete (the city database arrives after the first contact on a new panel); a
// new address never keeps the old address's coordinates.
func (p *Panel) locateServer(srv *Server, h *proto.Hello) (country, city string, lat, lon *float64) {
	country, city, lat, lon = srv.Country, srv.City, srv.Lat, srv.Lon
	if srv.LocManual {
		return
	}
	if a := srv.addrIP(); a != "" {
		if c, ci, la, lo := p.lookupPlace(a); c != "" || la != nil {
			return c, ci, la, lo
		}
		return
	}
	ip, old := h.IPv4, srv.IPv4
	if ip == "" {
		ip, old = h.IPv6, srv.IPv6
	}
	if ip == "" {
		return
	}
	moved := ip != old
	if !moved && country != "" && lat != nil {
		return
	}
	c, ci, la, lo := p.lookupPlace(ip)
	if c == "" && la == nil {
		if moved { // unknown for now: better no place than the old one; the next hello asks again
			return "", "", nil, nil
		}
		return
	}
	return c, ci, la, lo
}

// sanitizeLive keeps what an agent says is connected in one spelling: addresses parsed, unmapped
// ("::ffff:1.2.3.4" is 1.2.3.4) and without zones, anything else dropped, each address once per
// user and protocol.
func sanitizeLive(lv *proto.Live) {
	if len(lv.Online) > batchLimit {
		lv.Online = lv.Online[:batchLimit]
	}
	sanitizeRelayLive(lv)
	// what the server holds and serves of the shared certificates: a few entries, short texts
	if len(lv.Certs) > 256 {
		lv.Certs = lv.Certs[:256]
	}
	for i := range lv.Certs {
		c := &lv.Certs[i]
		c.Installed = truncate(c.Installed, 64)
		if len(c.Served) > 256 {
			c.Served = c.Served[:256]
		}
		for j := range c.Served {
			c.Served[j].SHA256 = truncate(c.Served[j].SHA256, 64)
			c.Served[j].Error = cleanNote(c.Served[j].Error, 200)
		}
	}
	for i := range lv.Online {
		u := &lv.Online[i]
		at := map[string]int{}
		ips := u.IPs[:0]
		for _, ip := range u.IPs {
			a, err := netip.ParseAddr(ip.IP)
			if err != nil {
				continue
			}
			ip.IP = a.Unmap().WithZone("").String()
			if j, ok := at[ip.IP]; ok {
				if ip.Since > 0 && (ips[j].Since == 0 || ip.Since < ips[j].Since) {
					ips[j].Since = ip.Since
				}
				ips[j].Last = max(ips[j].Last, ip.Last)
				continue
			}
			at[ip.IP] = len(ips)
			ips = append(ips, ip)
		}
		u.IPs = ips
	}
}

// applyBatch writes one batch of accumulated data.
// virtRe is what a host's kind of machine may be called (proto.Hello.Virt).
var virtRe = regexp.MustCompile(`^[a-z0-9_-]{1,24}$`)

// sanitizeHello bounds what an agent says about its host before it is stored and shown.
func sanitizeHello(h *proto.Hello) {
	for _, s := range []*string{&h.AgentVersion, &h.Hostname, &h.OS, &h.Kernel, &h.Arch, &h.CPUModel} {
		*s = cleanName(*s, 128)
	}
	if !virtRe.MatchString(h.Virt) {
		h.Virt = ""
	}
	for _, s := range []*string{&h.IPv4, &h.IPv6} {
		if a, err := netip.ParseAddr(*s); err == nil {
			*s = a.Unmap().String()
		} else {
			*s = ""
		}
	}
	var addrs []string
	for _, s := range h.Addrs {
		if a, err := netip.ParseAddr(s); err == nil && a.Zone() == "" && a.IsGlobalUnicast() && !slices.Contains(addrs, a.Unmap().String()) {
			addrs = append(addrs, a.Unmap().String())
		}
		if len(addrs) == 64 {
			break
		}
	}
	h.Addrs = addrs
}

// batchLimit caps how many entries of each kind one report may carry.
const batchLimit = 50000

func (p *Panel) applyBatch(tx *sql.Tx, srv *Server, b *proto.Batch, set Settings) error {
	ts := b.To
	if ts <= 0 || ts > now()+3600 {
		ts = now()
	}
	if len(b.Traffic) > batchLimit || len(b.IPs) > batchLimit || len(b.Dests) > batchLimit || len(b.Forwards) > batchLimit ||
		len(b.Pings) > batchLimit {
		return fmt.Errorf("report from %s is too large", srv.Name)
	}
	if err := storePings(tx, srv, b.Pings); err != nil { // pingmon.go
		return err
	}
	day := p.dayKey(time.Unix(ts, 0))

	// which subscriptions and nodes this server may report on
	subOK := map[int64]bool{}
	rows, err := tx.Query(`SELECT id FROM subs WHERE account_id = ?`, srv.AccountID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			subOK[id] = true
		}
	}
	rows.Close()
	nodeOK := map[int64]bool{}
	rows, err = tx.Query(`SELECT id FROM nodes WHERE server_id = ?`, srv.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			nodeOK[id] = true
		}
	}
	rows.Close()

	for _, u := range b.Traffic {
		if !subOK[u.Sub] || u.Up < 0 || u.Down < 0 || (u.Up == 0 && u.Down == 0) {
			continue
		}
		if _, err := tx.Exec(`UPDATE subs SET cycle_up = cycle_up + ?, cycle_down = cycle_down + ?, total_up = total_up + ?,
			total_down = total_down + ?, last_online_at = ? WHERE id = ?`, u.Up, u.Down, u.Up, u.Down, ts, u.Sub); err != nil {
			return err
		}
		node := u.Node
		if !nodeOK[node] {
			node = -srv.ID // protocol removed meanwhile: keep the traffic on the user and the server
		}
		if _, err := tx.Exec(`INSERT INTO traffic_daily (day, sub_id, node_id, server_id, up, down) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(day, sub_id, node_id) DO UPDATE SET up = up + excluded.up, down = down + excluded.down,
			server_id = excluded.server_id`, day, u.Sub, node, srv.ID, u.Up, u.Down); err != nil {
			return err
		}
		if err := addNodeUsage(tx, u.Sub, node, srv.ID, u.Up, u.Down, ts); err != nil {
			return err
		}
	}

	for _, f := range b.Forwards {
		if f.Up < 0 || f.Down < 0 || f.Conns < 0 {
			continue
		}
		res, err := tx.Exec(`UPDATE forwards SET up_total = up_total + ?, down_total = down_total + ? WHERE id = ? AND server_id = ?`,
			f.Up, f.Down, f.ID, srv.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO forward_daily (day, forward_id, up, down, conns) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(day, forward_id) DO UPDATE SET up = up + excluded.up, down = down + excluded.down,
			conns = conns + excluded.conns`, day, f.ID, f.Up, f.Down, f.Conns); err != nil {
			return err
		}
	}

	if set.ConnLog {
		for _, ip := range b.IPs {
			addr, err := netip.ParseAddr(ip.IP)
			if !subOK[ip.Sub] || err != nil || ip.Conns < 0 {
				continue
			}
			ip.IP = addr.Unmap().String()
			ipDay := p.dayKey(time.Unix(max(ip.Last, ip.First, 1), 0))
			var country, city, org string
			var asn uint
			if g := p.geo.Lookup(ip.IP); g != nil {
				country, city, asn, org = g.Country, g.City, g.ASN, g.Org
			}
			if _, err := tx.Exec(`INSERT INTO ip_log (day, sub_id, ip, server_id, node_id, first_seen, last_seen, conns, country,
				city, asn, org) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(day, sub_id, ip, server_id) DO UPDATE SET first_seen = MIN(first_seen, excluded.first_seen),
				last_seen = MAX(last_seen, excluded.last_seen), conns = conns + excluded.conns, node_id = excluded.node_id`,
				ipDay, ip.Sub, ip.IP, srv.ID, ip.Node, ip.First, ip.Last, ip.Conns, country, city, asn, org); err != nil {
				return err
			}
		}
	}

	if set.DestLog {
		for _, d := range b.Dests {
			if !subOK[d.Sub] || d.Host == "" || d.Port < 0 || d.Port > 65535 || d.Conns < 0 || d.Bytes < 0 {
				continue
			}
			nw := d.Net
			if nw != "udp" {
				nw = "tcp"
			}
			d.Host = cleanName(d.Host, 253)
			if _, err := tx.Exec(`INSERT INTO dest_log (day, sub_id, server_id, host, port, network, conns, bytes, last_seen)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(day, sub_id, server_id, host, port, network) DO UPDATE SET
				conns = conns + excluded.conns, bytes = bytes + excluded.bytes, last_seen = MAX(last_seen, excluded.last_seen)`,
				day, d.Sub, srv.ID, truncate(strings.ToLower(d.Host), 253), d.Port, nw, d.Conns, d.Bytes, d.Last); err != nil {
				return err
			}
		}
	}

	b.NIC.RX, b.NIC.TX = max(b.NIC.RX, 0), max(b.NIC.TX, 0)
	if b.NIC.RX > 0 || b.NIC.TX > 0 {
		if _, err := tx.Exec(`UPDATE servers SET cycle_rx = cycle_rx + ?, cycle_tx = cycle_tx + ? WHERE id = ?`,
			b.NIC.RX, b.NIC.TX, srv.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO server_daily (day, server_id, rx, tx) VALUES (?, ?, ?, ?)
			ON CONFLICT(day, server_id) DO UPDATE SET rx = rx + excluded.rx, tx = tx + excluded.tx`,
			day, srv.ID, b.NIC.RX, b.NIC.TX); err != nil {
			return err
		}
	}

	if b.GeoDrops > 0 {
		if _, err := tx.Exec(`INSERT INTO server_daily (day, server_id, geo_drops) VALUES (?, ?, ?)
			ON CONFLICT(day, server_id) DO UPDATE SET geo_drops = geo_drops + excluded.geo_drops`,
			day, srv.ID, b.GeoDrops); err != nil {
			return err
		}
	}

	for _, e := range b.Events {
		lvl := e.Level
		if lvl != "warn" && lvl != "crit" {
			lvl = "info"
		}
		eventTx(tx, srv.AccountID, lvl, e.Kind, srv.ID, 0, srv.Name+": "+truncate(e.Message, 500))
	}
	return nil
}

// ---------------------------------------------------------------- IP limit alerts

var (
	ipFlagMu sync.Mutex
	ipFlags  = map[int64]bool{}
)

// checkIPLimits records an event when a subscription goes over its IP limit. It never pauses it.
func (p *Panel) checkIPLimits(ctx context.Context, accountID int64) {
	subs, err := p.subsOf(ctx, accountID)
	if err != nil {
		return
	}
	var limited []*Sub
	for _, s := range subs {
		if s.IPLimit > 0 && !s.Paused {
			limited = append(limited, s)
		}
	}
	// devices turned away over an enforced limit (limits.go) - and given back when a limit goes
	if slices.ContainsFunc(subs, (*Sub).enforced) || p.devices.any() {
		p.countDevices(accountID, subs, p.onlineBySub(ctx, accountID))
	}
	if len(limited) == 0 {
		return
	}
	online := p.onlineBySub(ctx, accountID)
	for _, s := range limited {
		n := distinctIPs(online[s.ID])
		over := n > s.IPLimit
		ipFlagMu.Lock()
		was := ipFlags[s.ID]
		ipFlags[s.ID] = over
		ipFlagMu.Unlock()
		if over && !was {
			ips := []string{}
			for _, o := range online[s.ID] {
				ips = append(ips, o.IP)
			}
			p.event(s.AccountID, "warn", "over_ip_limit", 0, s.ID, 0,
				fmt.Sprintf("%s is connected from %d IPs (limit %d): %s", s.Name, n, s.IPLimit, strings.Join(uniq(ips), ", ")), nil)
		}
	}
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func humanDuration(sec int64) string {
	switch {
	case sec < 90:
		return fmt.Sprintf("%ds", sec)
	case sec < 5400:
		return fmt.Sprintf("%dm", sec/60)
	case sec < 172800:
		return fmt.Sprintf("%.1fh", float64(sec)/3600)
	default:
		return fmt.Sprintf("%dd", sec/86400)
	}
}

func logErr(what string, err error) {
	if err != nil {
		slog.Error(what, "err", err)
	}
}

// targetSummary words a camouflage check someone asked for ("Test from server"); automatic checks
// are reported by applyTargetResult instead (ok = false).
func targetSummary(output string) (msg string, ok bool) {
	var out struct {
		Auto    bool                 `json:"auto"`
		Results []proto.TargetResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(output), &out); err != nil || len(out.Results) == 0 {
		return firstLine(output), true
	}
	if out.Auto {
		return "", false
	}
	r := out.Results[0]
	if r.OK {
		return fmt.Sprintf("REALITY camouflage %s works from this server (%d ms)", r.Target, r.MS), true
	}
	msg = fmt.Sprintf("REALITY camouflage %s does not work from this server", r.Target)
	if r.Error != "" {
		msg += ": " + firstLine(r.Error)
	}
	return msg, true
}

// applyTargetResult acts on an automatic camouflage check: when a new REALITY node's site does not
// work from its server, the node switches to the first site that does. Existing, used nodes are
// never switched on their own - their check only reports.
func (p *Panel) applyTargetResult(tx *sql.Tx, srv *Server, ar proto.ActionResult) error {
	var kind string
	if err := tx.QueryRow(`SELECT kind FROM actions WHERE id = ?`, ar.ID).Scan(&kind); err != nil || kind != proto.ActionCheckTarget || !ar.OK {
		return nil
	}
	var out struct {
		NodeID  int64                `json:"node_id"`
		Auto    bool                 `json:"auto"`
		Results []proto.TargetResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(ar.Output), &out); err != nil || !out.Auto || len(out.Results) == 0 {
		return nil
	}
	var settings string
	var created int64
	if err := tx.QueryRow(`SELECT settings, created_at FROM nodes WHERE id = ? AND server_id = ?`, out.NodeID, srv.ID).
		Scan(&settings, &created); err != nil {
		return nil
	}
	rs, err := parseXray(json.RawMessage(settings))
	if err != nil || rs.Security != secReality || rs.OwnSite {
		return nil
	}
	cur := out.Results[0]
	best := cur
	for _, r := range out.Results {
		if r.OK && (!best.OK || r.MS < best.MS) {
			best = r
		}
	}
	// keep the current site while it works and is not much slower than the best one
	if cur.OK && cur.Target == rs.SNI && (best.Target == cur.Target || cur.MS <= 2*best.MS+500) {
		eventTx(tx, srv.AccountID, "info", "target_ok", srv.ID, 0,
			fmt.Sprintf("%s: REALITY camouflage %s works from this server (%d ms)", srv.Name, rs.SNI, cur.MS))
		return nil
	}
	if !best.OK {
		eventTx(tx, srv.AccountID, "warn", "target_failed", srv.ID, 0,
			fmt.Sprintf("%s: none of the usual REALITY camouflage sites work from this server - set one by hand", srv.Name))
		return nil
	}
	if now()-created > 86400 {
		eventTx(tx, srv.AccountID, "warn", "target_slow", srv.ID, 0, fmt.Sprintf(
			"%s: REALITY camouflage %s is slow or failing here (%d ms); %s answers in %d ms - change it in the protocol settings",
			srv.Name, rs.SNI, cur.MS, best.Target, best.MS))
		return nil
	}
	if !publicName(best.Target) { // only ever switch to a public camouflage site
		return nil
	}
	old := rs.SNI
	rs.SNI, rs.Target = best.Target, best.Target+":443"
	if rs.check(subgen.KindVLESS) != nil {
		return nil
	}
	b, _ := json.Marshal(rs)
	if _, err := tx.Exec(`UPDATE nodes SET settings = ?, updated_at = ? WHERE id = ?`, string(b), now(), out.NodeID); err != nil {
		return err
	}
	why := "does not work"
	if cur.OK {
		why = fmt.Sprintf("is slow (%d ms)", cur.MS)
	}
	eventTx(tx, srv.AccountID, "info", "target_switched", srv.ID, 0, fmt.Sprintf(
		"%s: REALITY camouflage %s %s from this server - switched to %s (%d ms)", srv.Name, old, why, best.Target, best.MS))
	return nil
}

var protocolTag = regexp.MustCompile(`\bprotocol n(\d+)\b`)

// nameProtocols turns the agent's "protocol n12" into the protocol's name as the panel shows it
// ("protocol REALITY (n12)"), so an error says which protocol it means.
func nameProtocols(tx *sql.Tx, serverID int64, msg string) string {
	return protocolTag.ReplaceAllStringFunc(msg, func(m string) string {
		id, err := strconv.ParseInt(protocolTag.FindStringSubmatch(m)[1], 10, 64)
		if err != nil {
			return m
		}
		var kind, settings, name string
		if tx.QueryRow(`SELECT kind, settings, name FROM nodes WHERE id = ? AND server_id = ?`, id, serverID).Scan(&kind, &settings, &name) != nil {
			return m
		}
		if name == "" {
			name = protocolLabel(kind, json.RawMessage(settings))
		}
		return fmt.Sprintf("protocol %s (n%d)", name, id)
	})
}
