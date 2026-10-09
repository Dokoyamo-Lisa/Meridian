package panel

// The status page (once a stand-alone status site, now built in). Visitors see only a sign-in; a
// user who signs in sees their own page (/me: data left, devices, usage per server); the supervisor
// sees every server on a live globe - which are up, where they are, how available they have been,
// throughput, load, bandwidth and outages. The dashboard data never contains users, addresses,
// ports or credentials either.

import (
	"context"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------- availability

// sampleUptime records, every minute, which servers are up, in 10-minute buckets.
func (p *Panel) sampleUptime(ctx context.Context) {
	rows, err := p.db.QueryContext(ctx, `SELECT id, online FROM servers WHERE deleted_at = 0 AND first_seen_at > 0`)
	if err != nil {
		return
	}
	type st struct {
		id int64
		up bool
	}
	var list []st
	for rows.Next() {
		var x st
		if rows.Scan(&x.id, &x.up) == nil {
			list = append(list, x)
		}
	}
	rows.Close()
	bucket := now() / 600
	for _, x := range list {
		up := 0
		if x.up {
			up = 1
		}
		_, err := p.db.Exec1(`INSERT INTO server_uptime (server_id, bucket, samples, up) VALUES (?, ?, 1, ?)
			ON CONFLICT(server_id, bucket) DO UPDATE SET samples = samples + 1, up = up + excluded.up`, x.id, bucket, up)
		logErr("uptime", err)
	}
	_, err = p.db.Exec1(`DELETE FROM server_uptime WHERE bucket < ?`, bucket-31*144)
	logErr("uptime retention", err)
}

type availability struct {
	H24  *float64   `json:"h24" doc:"Percent of the last 24 hours the server was up; null = no data"`
	D30  *float64   `json:"d30" doc:"Percent of the last 30 days"`
	Days []*float64 `json:"days" doc:"Percent per day, oldest first, 30 days; null = no data"`
}

// availabilityOf computes 24 h, 30 d and per-day availability for servers.
func (p *Panel) availabilityOf(ctx context.Context, ids []int64, days []string) map[int64]*availability {
	out := map[int64]*availability{}
	if len(ids) == 0 {
		return out
	}
	loc := p.loc()
	since := now()/600 - 30*144
	rows, err := p.db.QueryContext(ctx, `SELECT server_id, bucket, samples, up FROM server_uptime WHERE bucket >= ?`, since)
	if err != nil {
		return out
	}
	defer rows.Close()
	type acc struct{ n, up int64 }
	h24 := map[int64]*acc{}
	d30 := map[int64]*acc{}
	perDay := map[int64]map[string]*acc{}
	cut24 := now()/600 - 144
	for rows.Next() {
		var id, b, n, up int64
		if rows.Scan(&id, &b, &n, &up) != nil {
			continue
		}
		add := func(m map[int64]*acc) {
			a := m[id]
			if a == nil {
				a = &acc{}
				m[id] = a
			}
			a.n += n
			a.up += up
		}
		add(d30)
		if b >= cut24 {
			add(h24)
		}
		day := time.Unix(b*600, 0).In(loc).Format("2006-01-02")
		if perDay[id] == nil {
			perDay[id] = map[string]*acc{}
		}
		a := perDay[id][day]
		if a == nil {
			a = &acc{}
			perDay[id][day] = a
		}
		a.n += n
		a.up += up
	}
	pct := func(a *acc) *float64 {
		if a == nil || a.n == 0 {
			return nil
		}
		v := float64(a.up) * 100 / float64(a.n)
		return &v
	}
	for _, id := range ids {
		av := &availability{H24: pct(h24[id]), D30: pct(d30[id]), Days: make([]*float64, len(days))}
		for i, d := range days {
			av.Days[i] = pct(perDay[id][d])
		}
		out[id] = av
	}
	return out
}

// ---------------------------------------------------------------- public payload

type statusServer struct {
	ID           int64         `json:"id"`
	Name         string        `json:"name"`
	Country      string        `json:"cc"`
	City         string        `json:"city"`
	Loc          []float64     `json:"loc,omitempty" doc:"[latitude, longitude]"`
	TZ           string        `json:"tz,omitempty" doc:"IANA time zone of the city, when known"`
	Approx       bool          `json:"approx,omitempty" doc:"The location comes from the IP database and may be off (set it by hand on the server page)"`
	Online       bool          `json:"online"`
	Since        int64         `json:"since" doc:"When it last went up or down"`
	Availability *availability `json:"availability"`
	Speed        *statusSpeed  `json:"speed,omitempty"`
	Bandwidth    *statusBW     `json:"bandwidth,omitempty"`
	Sys          *statusSys    `json:"sys,omitempty"`
	Addrs        []string      `json:"addrs,omitempty" doc:"Its public IP addresses - set by hand, its protocols' own, found by its agent - to visitors only where the status page shows them"`
	Host         *statusHost   `json:"host,omitempty"`
	Cycle        *statusCycle  `json:"cycle,omitempty" doc:"Its own traffic since the monthly reset"`
	Expires      string        `json:"expires,omitempty" doc:"The day its paid period ends (YYYY-MM-DD), when set"`
}

type statusHost struct {
	OS     string `json:"os,omitempty"`
	Kernel string `json:"kernel,omitempty" doc:"Linux kernel version"`
	Virt   string `json:"virt,omitempty" doc:"What it runs in: kvm, xen, vmware, hyper-v, openvz, lxc ...; none = no hypervisor shows"`
	Arch   string `json:"arch,omitempty"`
	CPU    string `json:"cpu,omitempty" doc:"Processor model"`
	Cores  int    `json:"cores,omitempty"`
	Mem    int64  `json:"mem,omitempty" doc:"Memory, bytes"`
	Disk   int64  `json:"disk,omitempty" doc:"Disk, bytes"`
}

type statusCycle struct {
	Up    int64 `json:"up" doc:"Bytes sent"`
	Down  int64 `json:"down" doc:"Bytes received"`
	Start int64 `json:"start" doc:"When the count began"`
}

type statusSpeed struct {
	Up   int64 `json:"up" doc:"Bytes per second sent"`
	Down int64 `json:"down" doc:"Bytes per second received"`
}

type statusBW struct {
	Used      int64  `json:"used"`
	Limit     int64  `json:"limit"`
	ResetDay  int    `json:"reset_day"`
	NextReset int64  `json:"next_reset"`
	Mode      string `json:"mode" doc:"What counts against the limit: both (sent and received) | up | down | max (the larger)"`
}

type statusSys struct {
	CPU       float64    `json:"cpu"`
	Mem       float64    `json:"mem" doc:"Percent used"`
	Disk      float64    `json:"disk" doc:"Percent used"`
	Uptime    int64      `json:"uptime" doc:"Seconds since the machine started, when it last reported"`
	Booted    int64      `json:"booted" doc:"When the machine started (Unix seconds)"`
	Cores     int        `json:"cores"`
	Load      [3]float64 `json:"load" doc:"Load averages over 1, 5 and 15 minutes"`
	MemUsed   uint64     `json:"mem_used"`
	MemTotal  uint64     `json:"mem_total"`
	SwapUsed  uint64     `json:"swap_used"`
	SwapTotal uint64     `json:"swap_total"`
	DiskUsed  uint64     `json:"disk_used"`
	DiskTotal uint64     `json:"disk_total"`
	TCP       int        `json:"tcp" doc:"Open TCP connections"`
	UDP       int        `json:"udp" doc:"Open UDP sockets"`
}

type statusEvent struct {
	T        int64  `json:"t"`
	ServerID int64  `json:"server_id"`
	Kind     string `json:"kind" doc:"offline | online"`
	Message  string `json:"message"`
}

type statusPayload struct {
	Title     string             `json:"title"`
	About     string             `json:"about"`
	Timezone  string             `json:"timezone" doc:"The panel's time zone: days and monthly resets follow it"`
	Generated int64              `json:"generated_at"`
	Days      []string           `json:"days"`
	Servers   []statusServer     `json:"servers"`
	Totals    statusTotals       `json:"totals"`
	History   map[string][]int64 `json:"history,omitempty" doc:"Bytes per day per server id, aligned with days"`
	Events    []statusEvent      `json:"events,omitempty"`
	Hub       *statusHub         `json:"hub,omitempty" doc:"Where the panel is drawn on the globe"`
	// Supervisor says the supervisor is signed in; everyone else sees what the status page shows them
	Supervisor bool `json:"supervisor,omitempty" doc:"The supervisor is asking (else a visitor: the status page shows the servers to everyone)"`
	// Maintenance is the notice shown while the panel is in maintenance mode
	Maintenance string `json:"maintenance,omitempty" doc:"Maintenance in progress: the notice the page shows (servers keep working)"`
	// Show says which parts visitors and users get (the supervisor always gets all of them)
	Show statusShow `json:"show"`
}

type statusShow struct {
	Overview bool `json:"overview" doc:"Visitors and users get the overview (else they start at the list of servers)"`
	Events   bool `json:"events" doc:"Visitors and users see the outages of the last 30 days"`
}

type statusHub struct {
	City    string    `json:"city"`
	Country string    `json:"cc"`
	Loc     []float64 `json:"loc" doc:"[latitude, longitude]"`
	TZ      string    `json:"tz,omitempty"`
}

type statusTotals struct {
	Servers int   `json:"servers"`
	Online  int   `json:"online"`
	Up      int64 `json:"up"`
	Down    int64 `json:"down"`
	Today   int64 `json:"today"`
}

type statusCache struct {
	mu   sync.Mutex
	at   time.Time
	body *statusPayload
}

func (c *statusCache) forget() {
	c.mu.Lock()
	c.body = nil
	c.mu.Unlock()
}

// statusData builds the dashboard's data, cached for a few seconds.
func (p *Panel) statusData(ctx context.Context) (*statusPayload, error) {
	p.status.mu.Lock()
	if p.status.body != nil && time.Since(p.status.at) < 5*time.Second {
		b := p.status.body
		p.status.mu.Unlock()
		return b, nil
	}
	p.status.mu.Unlock()
	set := p.settings()
	out := &statusPayload{Title: set.SiteTitle, About: set.StatusAbout, Timezone: p.loc().String(), Generated: now(),
		Servers: []statusServer{}}
	for i := 29; i >= 0; i-- {
		out.Days = append(out.Days, p.dayKey(time.Now().AddDate(0, 0, -i)))
	}
	if h := set.StatusHub; h != nil {
		out.Hub = &statusHub{City: h.City, Country: h.Country, Loc: []float64{round1(h.Lat), round1(h.Lon)}, TZ: tzOf(h.City, h.Country)}
	}
	servers, err := p.serversOf(ctx, 0)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, s := range servers {
		if s.FirstSeenAt == 0 || s.StatusHidden {
			continue
		}
		ids = append(ids, s.ID)
	}
	avail := p.availabilityOf(ctx, ids, out.Days)
	// the addresses protocols give users: an address of their own, or one set for their links - behind
	// NAT these can differ from the one the agent found
	nodeAddrs := map[int64][]string{}
	rows, err := p.db.QueryContext(ctx, `SELECT server_id, host, bind_ip FROM nodes WHERE host != '' OR bind_ip != '' ORDER BY sort, id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var sid int64
		var host, bind string
		if err := rows.Scan(&sid, &host, &bind); err != nil {
			rows.Close()
			return nil, err
		}
		nodeAddrs[sid] = append(nodeAddrs[sid], host, bind)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	keep := map[int64]bool{}
	for _, s := range servers {
		if !contains64(ids, s.ID) {
			continue
		}
		keep[s.ID] = true
		// no users, protocols, ports or prices here: the page may be public (addresses are left out
		// where it does not show them - see apiStatus)
		v := statusServer{ID: s.ID, Name: s.ShownName(), Country: s.Country, City: s.City, TZ: tzOf(s.City, s.Country),
			Online: s.Online, Since: s.StatusChangedAt, Availability: avail[s.ID], Addrs: publicAddrsOf(s, nodeAddrs[s.ID]),
			Cycle: &statusCycle{Up: s.CycleTX, Down: s.CycleRX, Start: s.CycleStart}, Expires: s.ExpiresOn}
		if s.OS != "" || s.CPUCores > 0 {
			v.Host = &statusHost{OS: s.OS, Kernel: s.Kernel, Virt: s.Virt, Arch: s.Arch, CPU: s.CPUModel, Cores: s.CPUCores, Mem: s.MemTotal, Disk: s.DiskTotal}
		}
		if s.Lat != nil && s.Lon != nil {
			v.Loc = []float64{round1(*s.Lat), round1(*s.Lon)}
			v.Approx = !s.LocManual
		}
		ls := p.live.get(s.ID)
		if s.Online && ls != nil {
			v.Speed = &statusSpeed{Up: ls.Live.Sys.TXRate, Down: ls.Live.Sys.RXRate}
			out.Totals.Up += ls.Live.Sys.TXRate
			out.Totals.Down += ls.Live.Sys.RXRate
			y := ls.Live.Sys
			v.Sys = &statusSys{CPU: y.CPU, Uptime: y.Uptime, Booted: ls.At - y.Uptime, Cores: s.CPUCores, Load: [3]float64{y.Load1, y.Load5, y.Load15},
				MemUsed: y.MemUsed, MemTotal: y.MemTotal, SwapUsed: y.SwapUsed, SwapTotal: y.SwapTotal, DiskUsed: y.DiskUsed,
				DiskTotal: y.DiskTotal, TCP: y.TCP, UDP: y.UDP}
			if y.MemTotal > 0 {
				v.Sys.Mem = float64(y.MemUsed) * 100 / float64(y.MemTotal)
			}
			if y.DiskTotal > 0 {
				v.Sys.Disk = float64(y.DiskUsed) * 100 / float64(y.DiskTotal)
			}
		}
		if s.BwLimit > 0 {
			bw := &statusBW{Used: s.BwUsed(), Limit: s.BwLimit, ResetDay: s.BwResetDay, Mode: nz(s.BwMode, "both")}
			if s.BwResetDay > 0 {
				bw.NextReset = nextReset(s.BwResetDay, time.Now().In(p.loc())).Unix()
			}
			v.Bandwidth = bw
		}
		out.Servers = append(out.Servers, v)
		out.Totals.Servers++
		if s.Online {
			out.Totals.Online++
		}
	}
	// traffic per day (the servers' network counters)
	p.statusHistory(ctx, out, keep)
	{
		if rows, err := p.db.QueryContext(ctx, `SELECT ts, server_id, kind FROM events WHERE kind IN ('server_offline', 'server_online')
			AND ts >= ? ORDER BY id DESC LIMIT 200`, now()-30*86400); err == nil {
			for rows.Next() {
				var e statusEvent
				var kind string
				if rows.Scan(&e.T, &e.ServerID, &kind) != nil || !keep[e.ServerID] {
					continue
				}
				e.Kind = strings.TrimPrefix(kind, "server_")
				for _, s := range out.Servers {
					if s.ID == e.ServerID {
						if e.Kind == "offline" {
							e.Message = s.Name + " went offline"
						} else {
							e.Message = s.Name + " is back online"
						}
					}
				}
				out.Events = append(out.Events, e)
			}
			rows.Close()
		}
	}
	p.status.mu.Lock()
	p.status.body, p.status.at = out, time.Now()
	p.status.mu.Unlock()
	return out, nil
}

// statusHistory fills in bytes per day for the servers in keep (the servers' own network counters).
func (p *Panel) statusHistory(ctx context.Context, out *statusPayload, keep map[int64]bool) {
	rows, err := p.db.QueryContext(ctx, `SELECT day, server_id, rx + tx FROM server_daily WHERE day >= ?`, out.Days[0])
	if err != nil {
		return
	}
	defer rows.Close()
	idx := map[string]int{}
	for i, d := range out.Days {
		idx[d] = i
	}
	out.History = map[string][]int64{}
	for rows.Next() {
		var day string
		var id, b int64
		if rows.Scan(&day, &id, &b) != nil || !keep[id] {
			continue
		}
		k := strconv.FormatInt(id, 10)
		if out.History[k] == nil {
			out.History[k] = make([]int64, len(out.Days))
		}
		if i, ok := idx[day]; ok {
			out.History[k][i] += b
			if i == len(out.Days)-1 {
				out.Totals.Today += b
			}
		}
	}
}

func contains64(list []int64, v int64) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func round1(v float64) float64 { return float64(int64(v*10+sign(v)*0.5)) / 10 }

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

// publicAddrsOf lists a server's public IP addresses: an address set by hand that is an IP, those its
// protocols give users (extra), the one its agent found on the internet side, and those on its
// interfaces.
func publicAddrsOf(s *Server, extra []string) []string {
	var out []string
	add := func(raw string) {
		a, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(raw), "[]"))
		if err != nil || !publicAddr(a) {
			return
		}
		if v := a.Unmap().String(); !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	add(s.Address)
	for _, x := range extra {
		add(x)
	}
	add(s.IPv4)
	add(s.IPv6)
	for _, x := range s.Addrs {
		add(x)
	}
	return out
}

// statusOpen says whether visitors see the servers: the status page is on (the front page, /status or
// its own domain) and set to show them to everyone.
func (p *Panel) statusOpen() bool {
	set := p.settings()
	return set.StatusPublic && (set.StatusPage != "off" || set.StatusDomain != "")
}

// statusViewer says who asks for the status page's data: the supervisor (a valid session or API token)
// or a visitor. An API token that does not work is an error, never a visitor.
func (p *Panel) statusViewer(w http.ResponseWriter, r *http.Request) (bool, error) {
	if _, ok := bearerToken(r); ok {
		a, _, err := p.resolve(w, r, authOpts{})
		if err != nil {
			return false, err
		}
		return a.IsOwner(), nil
	}
	if a, err := p.sessionAccount(r); err == nil && a.IsOwner() {
		return true, nil
	}
	if !p.statusOpen() {
		return false, errStatus(http.StatusUnauthorized, "sign in required")
	}
	return false, nil
}

// apiStatus serves the dashboard's data: to the supervisor, and to everyone while the status page shows
// the servers - IP addresses only while it shows those, and then to everyone alike.
func (p *Panel) apiStatus(w http.ResponseWriter, r *http.Request) {
	sup, err := p.statusViewer(w, r)
	if err != nil {
		writeErr(w, err)
		return
	}
	d, err := p.statusData(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := *d
	out.Supervisor = sup
	out.Maintenance = p.maintenanceText() // guard.go
	set := p.settings()
	out.Show = statusShow{Overview: set.StatusOverview, Events: set.StatusEvents}
	if !sup { // what the supervisor keeps to themselves is not sent at all
		if !set.StatusEvents {
			out.Events = nil
		}
		if !set.StatusOverview {
			out.Hub = nil // the panel's place on the overview's globe
		}
	}
	// the switch is for the page, not the person: off, nobody sees addresses there (the panel shows them)
	if !p.settings().StatusIPs {
		out.Servers = make([]statusServer, len(d.Servers))
		for i, s := range d.Servers {
			s.Addrs = nil
			out.Servers[i] = s
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, p.plugins.filterStatus(r.Context(), &out)) // plugins with filter:status
}

type statusLive struct {
	T       int64                    `json:"t"`
	Servers map[string][]statusPoint `json:"servers" doc:"Per server id: [time, up, down] since the given time"`
}

type statusPoint [3]int64

// apiStatusLive serves recent throughput per server (the last 30 minutes at most), to whoever may see
// the dashboard.
func (p *Panel) apiStatusLive(w http.ResponseWriter, r *http.Request) {
	if _, err := p.statusViewer(w, r); err != nil {
		writeErr(w, err)
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	out := statusLive{T: now(), Servers: map[string][]statusPoint{}}
	servers, err := p.serversOf(r.Context(), 0)
	if err != nil {
		writeErr(w, err)
		return
	}
	for _, s := range servers {
		if s.FirstSeenAt == 0 || s.StatusHidden {
			continue
		}
		ls := p.live.get(s.ID)
		if ls == nil {
			continue
		}
		var pts []statusPoint
		for _, rp := range ls.Rates {
			if rp.T > since {
				pts = append(pts, statusPoint{rp.T, rp.TX, rp.RX})
			}
		}
		if len(pts) > 0 {
			out.Servers[strconv.FormatInt(s.ID, 10)] = pts
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------- server locations

// place is a city servers usually run in.
type place struct {
	Name    string  `json:"name"`
	Country string  `json:"cc"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	TZ      string  `json:"tz"`
}

// places are the cities offered when setting a server's location by hand. IP databases often put
// data-centre addresses at the provider's registered office; this corrects the globe.
var places = []place{
	{"Los Angeles", "US", 34.05, -118.24, "America/Los_Angeles"}, {"San Jose", "US", 37.34, -121.89, "America/Los_Angeles"},
	{"Santa Clara", "US", 37.35, -121.96, "America/Los_Angeles"}, {"Fremont", "US", 37.55, -121.99, "America/Los_Angeles"},
	{"San Francisco", "US", 37.77, -122.42, "America/Los_Angeles"}, {"Seattle", "US", 47.61, -122.33, "America/Los_Angeles"},
	{"Portland", "US", 45.52, -122.68, "America/Los_Angeles"}, {"Hillsboro", "US", 45.52, -122.99, "America/Los_Angeles"},
	{"Las Vegas", "US", 36.17, -115.14, "America/Los_Angeles"}, {"Phoenix", "US", 33.45, -112.07, "America/Phoenix"},
	{"Salt Lake City", "US", 40.76, -111.89, "America/Denver"}, {"Denver", "US", 39.74, -104.99, "America/Denver"},
	{"Dallas", "US", 32.78, -96.80, "America/Chicago"}, {"Houston", "US", 29.76, -95.37, "America/Chicago"},
	{"Kansas City", "US", 39.10, -94.58, "America/Chicago"}, {"Chicago", "US", 41.88, -87.63, "America/Chicago"},
	{"Atlanta", "US", 33.75, -84.39, "America/New_York"}, {"Miami", "US", 25.76, -80.19, "America/New_York"},
	{"Ashburn", "US", 39.04, -77.49, "America/New_York"}, {"Washington", "US", 38.91, -77.04, "America/New_York"},
	{"New York", "US", 40.71, -74.01, "America/New_York"}, {"Secaucus", "US", 40.79, -74.06, "America/New_York"},
	{"Piscataway", "US", 40.55, -74.46, "America/New_York"}, {"Buffalo", "US", 42.89, -78.88, "America/New_York"},
	{"Boston", "US", 42.36, -71.06, "America/New_York"}, {"Columbus", "US", 39.96, -83.00, "America/New_York"},
	{"Honolulu", "US", 21.31, -157.86, "Pacific/Honolulu"}, {"Toronto", "CA", 43.65, -79.38, "America/Toronto"},
	{"Montreal", "CA", 45.50, -73.57, "America/Toronto"}, {"Vancouver", "CA", 49.28, -123.12, "America/Vancouver"},
	{"Mexico City", "MX", 19.43, -99.13, "America/Mexico_City"}, {"São Paulo", "BR", -23.55, -46.63, "America/Sao_Paulo"},
	{"Santiago", "CL", -33.45, -70.67, "America/Santiago"}, {"Buenos Aires", "AR", -34.60, -58.38, "America/Argentina/Buenos_Aires"},
	{"Bogotá", "CO", 4.71, -74.07, "America/Bogota"}, {"Lima", "PE", -12.05, -77.04, "America/Lima"},
	{"London", "GB", 51.51, -0.13, "Europe/London"}, {"Manchester", "GB", 53.48, -2.24, "Europe/London"},
	{"Dublin", "IE", 53.35, -6.26, "Europe/Dublin"}, {"Amsterdam", "NL", 52.37, 4.90, "Europe/Amsterdam"},
	{"Brussels", "BE", 50.85, 4.35, "Europe/Brussels"}, {"Luxembourg", "LU", 49.61, 6.13, "Europe/Luxembourg"},
	{"Frankfurt am Main", "DE", 50.11, 8.68, "Europe/Berlin"}, {"Düsseldorf", "DE", 51.23, 6.77, "Europe/Berlin"},
	{"Nuremberg", "DE", 49.45, 11.08, "Europe/Berlin"}, {"Falkenstein", "DE", 50.48, 12.37, "Europe/Berlin"},
	{"Berlin", "DE", 52.52, 13.40, "Europe/Berlin"}, {"Munich", "DE", 48.14, 11.58, "Europe/Berlin"},
	{"Paris", "FR", 48.86, 2.35, "Europe/Paris"}, {"Roubaix", "FR", 50.69, 3.18, "Europe/Paris"},
	{"Gravelines", "FR", 50.99, 2.13, "Europe/Paris"}, {"Strasbourg", "FR", 48.57, 7.75, "Europe/Paris"},
	{"Marseille", "FR", 43.30, 5.37, "Europe/Paris"}, {"Zurich", "CH", 47.38, 8.54, "Europe/Zurich"},
	{"Vienna", "AT", 48.21, 16.37, "Europe/Vienna"}, {"Milan", "IT", 45.46, 9.19, "Europe/Rome"},
	{"Madrid", "ES", 40.42, -3.70, "Europe/Madrid"}, {"Lisbon", "PT", 38.72, -9.14, "Europe/Lisbon"},
	{"Stockholm", "SE", 59.33, 18.07, "Europe/Stockholm"}, {"Oslo", "NO", 59.91, 10.75, "Europe/Oslo"},
	{"Copenhagen", "DK", 55.68, 12.57, "Europe/Copenhagen"}, {"Helsinki", "FI", 60.17, 24.94, "Europe/Helsinki"},
	{"Warsaw", "PL", 52.23, 21.01, "Europe/Warsaw"}, {"Prague", "CZ", 50.08, 14.44, "Europe/Prague"},
	{"Bucharest", "RO", 44.43, 26.10, "Europe/Bucharest"}, {"Sofia", "BG", 42.70, 23.32, "Europe/Sofia"},
	{"Athens", "GR", 37.98, 23.73, "Europe/Athens"}, {"Kyiv", "UA", 50.45, 30.52, "Europe/Kiev"},
	{"Moscow", "RU", 55.76, 37.62, "Europe/Moscow"}, {"Saint Petersburg", "RU", 59.93, 30.34, "Europe/Moscow"},
	{"Istanbul", "TR", 41.01, 28.98, "Europe/Istanbul"}, {"Dubai", "AE", 25.20, 55.27, "Asia/Dubai"},
	{"Tel Aviv", "IL", 32.09, 34.78, "Asia/Jerusalem"}, {"Cairo", "EG", 30.04, 31.24, "Africa/Cairo"},
	{"Lagos", "NG", 6.52, 3.38, "Africa/Lagos"}, {"Nairobi", "KE", -1.29, 36.82, "Africa/Nairobi"},
	{"Johannesburg", "ZA", -26.20, 28.05, "Africa/Johannesburg"}, {"Mumbai", "IN", 19.08, 72.88, "Asia/Kolkata"},
	{"New Delhi", "IN", 28.61, 77.21, "Asia/Kolkata"}, {"Chennai", "IN", 13.08, 80.27, "Asia/Kolkata"},
	{"Bangalore", "IN", 12.97, 77.59, "Asia/Kolkata"}, {"Karachi", "PK", 24.86, 67.01, "Asia/Karachi"},
	{"Almaty", "KZ", 43.24, 76.89, "Asia/Almaty"}, {"Singapore", "SG", 1.35, 103.82, "Asia/Singapore"},
	{"Kuala Lumpur", "MY", 3.14, 101.69, "Asia/Kuala_Lumpur"}, {"Johor Bahru", "MY", 1.49, 103.74, "Asia/Kuala_Lumpur"},
	{"Bangkok", "TH", 13.76, 100.50, "Asia/Bangkok"}, {"Jakarta", "ID", -6.21, 106.85, "Asia/Jakarta"},
	{"Manila", "PH", 14.60, 120.98, "Asia/Manila"}, {"Hanoi", "VN", 21.03, 105.85, "Asia/Ho_Chi_Minh"},
	{"Ho Chi Minh City", "VN", 10.82, 106.63, "Asia/Ho_Chi_Minh"}, {"Phnom Penh", "KH", 11.56, 104.93, "Asia/Phnom_Penh"},
	{"Hong Kong", "HK", 22.32, 114.17, "Asia/Hong_Kong"}, {"Macau", "MO", 22.20, 113.54, "Asia/Macau"},
	{"Taipei", "TW", 25.03, 121.57, "Asia/Taipei"}, {"Taichung", "TW", 24.15, 120.67, "Asia/Taipei"},
	{"Tokyo", "JP", 35.68, 139.69, "Asia/Tokyo"}, {"Osaka", "JP", 34.69, 135.50, "Asia/Tokyo"},
	{"Seoul", "KR", 37.57, 126.98, "Asia/Seoul"}, {"Chuncheon", "KR", 37.88, 127.73, "Asia/Seoul"},
	{"Shanghai", "CN", 31.23, 121.47, "Asia/Shanghai"}, {"Beijing", "CN", 39.90, 116.41, "Asia/Shanghai"},
	{"Tianjin", "CN", 39.34, 117.36, "Asia/Shanghai"}, {"Shenzhen", "CN", 22.54, 114.06, "Asia/Shanghai"},
	{"Guangzhou", "CN", 23.13, 113.26, "Asia/Shanghai"}, {"Hangzhou", "CN", 30.27, 120.16, "Asia/Shanghai"},
	{"Suzhou", "CN", 31.30, 120.59, "Asia/Shanghai"}, {"Nanjing", "CN", 32.06, 118.80, "Asia/Shanghai"},
	{"Qingdao", "CN", 36.07, 120.38, "Asia/Shanghai"}, {"Xiamen", "CN", 24.48, 118.09, "Asia/Shanghai"},
	{"Wuhan", "CN", 30.59, 114.31, "Asia/Shanghai"}, {"Chengdu", "CN", 30.57, 104.07, "Asia/Shanghai"},
	{"Chongqing", "CN", 29.56, 106.55, "Asia/Shanghai"}, {"Xi'an", "CN", 34.34, 108.94, "Asia/Shanghai"},
	{"Sydney", "AU", -33.87, 151.21, "Australia/Sydney"}, {"Melbourne", "AU", -37.81, 144.96, "Australia/Melbourne"},
	{"Perth", "AU", -31.95, 115.86, "Australia/Perth"}, {"Auckland", "NZ", -36.85, 174.76, "Pacific/Auckland"},
}

// tzOf is the time zone of a city from the list above, or of a country that has only one.
func tzOf(city, cc string) string {
	zone := ""
	for _, pl := range places {
		if pl.Country != cc {
			continue
		}
		if strings.EqualFold(pl.Name, city) {
			return pl.TZ
		}
		if zone == "" {
			zone = pl.TZ
		} else if zone != pl.TZ {
			zone = "-" // several zones: unknown without the city
		}
	}
	if zone == "-" {
		return ""
	}
	return zone
}

func (p *Panel) apiPlaces(w http.ResponseWriter, r *http.Request, a *Account) error {
	w.Header().Set("Cache-Control", "private, max-age=3600")
	writeJSON(w, http.StatusOK, places)
	return nil
}

// serverLocation is a location set by hand (PATCH /api/servers/{id} location).
type serverLocation struct {
	City    string  `json:"city"`
	Country string  `json:"cc" doc:"Two-letter country code"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
}

func (l *serverLocation) clean() error {
	l.City = cleanName(l.City, 60)
	l.Country = strings.ToUpper(strings.TrimSpace(l.Country))
	if l.City == "" || len(l.Country) != 2 || l.Country[0] < 'A' || l.Country[0] > 'Z' || l.Country[1] < 'A' ||
		l.Country[1] > 'Z' || !(l.Lat >= -90 && l.Lat <= 90) || !(l.Lon >= -180 && l.Lon <= 180) {
		return errStatus(http.StatusBadRequest, "enter a city, a two-letter country code and valid coordinates")
	}
	return nil
}

// statusPageFor says which page a request for path should get: the status page or the panel.
func (p *Panel) statusPageFor(r *http.Request, path string) bool {
	set := p.settings()
	switch {
	case path == "/me" || strings.HasPrefix(path, "/me/"), path == "/tg":
		return true // the users' own page, and the bot's Mini App (tglink.go)
	case set.StatusDomain != "" && strings.EqualFold(hostOnly(r.Host), set.StatusDomain):
		// the status page's own domain never serves the panel (with the page off it is the sign-in)
		return true
	case set.StatusPage == "off":
		return false
	case path == "/status":
		return true
	case path == "/" && set.StatusPage == "home":
		return true
	}
	return false
}

// hostOnly is the name in a Host header, without its port and without the trailing dot browsers keep
// for "status.example.com." (which would otherwise not count as the status page's domain).
func hostOnly(h string) string {
	if a, err := netip.ParseAddrPort(h); err == nil {
		return a.Addr().String()
	}
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return strings.TrimSuffix(strings.ToLower(h), ".")
}
