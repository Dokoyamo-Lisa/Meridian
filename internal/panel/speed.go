package panel

import (
	"net/http"
	"net/netip"
	"sort"
)

// Speed now, per user and per client address (Monitor › Speed): what the agents measured over their
// last report (proto.Live.Rates, IPRates). A user going through a proxy pass is counted once - where
// they connect: the exit counts that traffic under the entry protocol's identity, which is no user's
// - and a server's own traffic to an exit is no client address's. The servers' network rates and
// the bandwidth counters are not used here, and not changed.

type speedView struct {
	Up         int64       `json:"up" doc:"All users' upload now, bytes per second (each user counted once)"`
	Down       int64       `json:"down" doc:"All users' download now, bytes per second"`
	Users      []userSpeed `json:"users" doc:"Users with any speed now, fastest first"`
	Unmeasured []string    `json:"unmeasured" doc:"Online servers whose agent does not measure speed yet (upgrade it)"`
	Interval   int         `json:"interval" doc:"Seconds each measurement covers (the agents' report interval)"`
}

type userSpeed struct {
	UserID int64     `json:"user_id"`
	User   string    `json:"user"`
	Up     int64     `json:"up" doc:"Bytes per second, all servers and protocols"`
	Down   int64     `json:"down"`
	IPs    []ipSpeed `json:"ips" doc:"The addresses the user is online from, with their speed on each server"`
}

type ipSpeed struct {
	IP       string `json:"ip"`
	ServerID int64  `json:"server_id"`
	Server   string `json:"server"`
	Up       int64  `json:"up" doc:"Bytes per second from this address on this server, every protocol"`
	Down     int64  `json:"down"`
	Shared   int    `json:"shared" doc:"Other users online from the same address on this server (its speed is theirs too)"`
	Country  string `json:"country"`
	City     string `json:"city"`
	Org      string `json:"org"`
	ASN      uint   `json:"asn"`
}

type ipKey struct {
	server int64
	ip     string
}

func (p *Panel) apiLiveSpeed(w http.ResponseWriter, r *http.Request, a *Account) error {
	ctx := r.Context()
	acct := scopeAccount(r, a)
	servers, err := p.serversOf(ctx, acct)
	if err != nil {
		return err
	}
	subs, err := p.subsOf(ctx, acct)
	if err != nil {
		return err
	}
	names := map[int64]string{}
	for _, s := range subs {
		names[s.ID] = s.Name
	}
	every := p.settings().ReportInterval
	fresh := now() - int64(3*every) - 5 // a server that missed two reports has no speed now
	out := speedView{Users: []userSpeed{}, Unmeasured: []string{}, Interval: every}
	userRate := map[int64][2]int64{}
	ipRate := map[ipKey][2]int64{}
	for _, s := range servers {
		if !s.Online {
			continue
		}
		if !s.caps().Rates {
			out.Unmeasured = append(out.Unmeasured, s.Name)
			continue
		}
		ls := p.live.get(s.ID)
		if ls == nil || ls.At < fresh {
			continue
		}
		for _, x := range ls.Live.Rates {
			if _, ours := names[x.Sub]; !ours || x.Up < 0 || x.Down < 0 {
				continue
			}
			v := userRate[x.Sub]
			userRate[x.Sub] = [2]int64{v[0] + x.Up, v[1] + x.Down}
		}
		for _, x := range ls.Live.IPRates {
			ip, err := netip.ParseAddr(x.IP)
			if err != nil || x.Up < 0 || x.Down < 0 {
				continue
			}
			ipRate[ipKey{s.ID, ip.Unmap().String()}] = [2]int64{x.Up, x.Down}
		}
	}
	online := p.onlineBySub(ctx, acct)
	who := map[ipKey]map[int64]bool{} // the users online from each address on each server
	for sub, list := range online {
		for _, o := range list {
			k := ipKey{o.ServerID, o.IP}
			if who[k] == nil {
				who[k] = map[int64]bool{}
			}
			who[k][sub] = true
		}
	}
	for sub, name := range names {
		u := userSpeed{UserID: sub, User: name, IPs: []ipSpeed{}}
		v := userRate[sub]
		u.Up, u.Down = v[0], v[1]
		seen := map[ipKey]bool{}
		for _, o := range online[sub] {
			k := ipKey{o.ServerID, o.IP}
			rt, ok := ipRate[k]
			if !ok || seen[k] || rt == [2]int64{} {
				continue
			}
			seen[k] = true
			u.IPs = append(u.IPs, ipSpeed{IP: o.IP, ServerID: o.ServerID, Server: o.Server, Up: rt[0], Down: rt[1],
				Shared: len(who[k]) - 1, Country: o.Country, City: o.City, Org: o.Org, ASN: o.ASN})
		}
		if u.Up == 0 && u.Down == 0 && len(u.IPs) == 0 {
			continue
		}
		sort.Slice(u.IPs, func(i, j int) bool { return u.IPs[i].Up+u.IPs[i].Down > u.IPs[j].Up+u.IPs[j].Down })
		out.Up += u.Up
		out.Down += u.Down
		out.Users = append(out.Users, u)
	}
	sort.Slice(out.Users, func(i, j int) bool {
		a, b := out.Users[i], out.Users[j]
		if a.Up+a.Down != b.Up+b.Down {
			return a.Up+a.Down > b.Up+b.Down
		}
		return a.User < b.User
	})
	sort.Strings(out.Unmeasured)
	writeJSON(w, http.StatusOK, out)
	return nil
}
