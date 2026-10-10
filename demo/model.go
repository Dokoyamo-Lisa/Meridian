package main

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
	"net/netip"
	"strings"
	"time"
)

// The model: how much each person uses each server at any moment. It is a function of time alone -
// the same numbers for the month written at setup and for the live reports afterwards - built from
// the hour of the day where the people are (quiet at night, busy in the evening), the day of the
// week, and slow noise of its own for every person and server.

const gb = 1 << 30

// hash01 is a number in [0, 1) that depends only on the keys.
func hash01(keys ...int64) float64 {
	h := fnv.New64a()
	var b [8]byte
	for _, k := range keys {
		binary.LittleEndian.PutUint64(b[:], uint64(k))
		h.Write(b[:])
	}
	return float64(h.Sum64()>>11) / (1 << 53)
}

// noise wanders smoothly in [0, 1) over periods of the given length, the same for the same keys.
func noise(t int64, period float64, keys ...int64) float64 {
	x := float64(t) / period
	i := math.Floor(x)
	f := x - i
	f = f * f * (3 - 2*f)
	ka := append(append([]int64{}, keys...), int64(i))
	kb := append(append([]int64{}, keys...), int64(i)+1)
	a, b := hash01(ka...), hash01(kb...)
	return a + (b-a)*f
}

func sq(x float64) float64 { return x * x }

// circ is the distance between two hours of the day.
func circ(a, b float64) float64 {
	d := math.Abs(a - b)
	return math.Min(d, 24-d)
}

func rhythmRaw(h float64) float64 {
	return 0.25 + 1.6*math.Exp(-sq(circ(h, 21)/3.2)) + 0.55*math.Exp(-sq(circ(h, 13)/2.6)) + 0.2*math.Exp(-sq(circ(h, 8)/1.5))
}

var rhythmMean = func() float64 {
	s := 0.0
	for i := 0; i < 1440; i++ {
		s += rhythmRaw(float64(i) / 60)
	}
	return s / 1440
}()

// rhythm is how busy people are at time t where they live: 1 on an average day, more at weekends.
func rhythm(t int64, utc float64) float64 {
	local := float64(t)/3600 + utc
	h := math.Mod(local, 24)
	if h < 0 {
		h += 24
	}
	v := rhythmRaw(h) / rhythmMean
	if wd := time.Unix(t+int64(utc*3600), 0).UTC().Weekday(); wd == time.Saturday || wd == time.Sunday {
		v *= 1.15
	}
	return v
}

// world is the demo's people and servers as the panel knows them.
type world struct {
	servers []*server
	users   []*user
}

type server struct {
	m      machine
	idx    int
	id     int64
	token  string
	nodes  []node
	cycle  int64 // where its bandwidth cycle starts
	people []*user
}

type node struct {
	ID   int64
	Kind string
	Port int
}

type user struct {
	p       person
	idx     int
	id      int64
	cycle   int64 // where their usage cycle starts
	devices []string
	aff     map[int64]float64 // server id -> share of their use
	nodes   map[int64][]int64 // server id -> the protocols they may use there
	paused  bool
}

// affinities share each person's use between the servers they may use: most on their home server.
func (w *world) affinities() {
	for _, u := range w.users {
		u.aff = map[int64]float64{}
		total := 0.0
		for _, s := range w.servers {
			if len(u.nodes[s.id]) == 0 {
				continue
			}
			v := 0.06 * s.m.Busy
			if s.m.Name == u.p.Home {
				v = 1.6
			}
			u.aff[s.id] = v
			total += v
		}
		for id := range u.aff {
			u.aff[id] /= total
		}
	}
	for _, s := range w.servers {
		s.people = nil
		for _, u := range w.users {
			if u.aff[s.id] > 0 {
				s.people = append(s.people, u)
			}
		}
	}
}

// rate is what a person moves through a server at time t, bytes per second (down and up together),
// on average - whether or not they happen to be online then.
func (w *world) rate(u *user, s *server, t int64) float64 {
	a := u.aff[s.id]
	if a == 0 || u.paused || t < u.started() || outageAt(s, t) {
		return 0
	}
	n := 0.55 + 0.9*noise(t, 2400, 11, int64(u.idx), int64(s.idx))
	return u.p.DailyGB * gb * a * rhythm(t, s.m.UTC) * n / 86400
}

func (u *user) started() int64 { return int64(float64(setupTime) - u.p.Started*86400) }

// presence is the chance a person is online on a server in the 20 minutes around t.
func (w *world) presence(u *user, s *server, t int64) float64 {
	a := u.aff[s.id]
	if a == 0 || u.paused || t < u.started() || outageAt(s, t) {
		return 0
	}
	heavy := math.Min(1.3, 0.55+u.p.DailyGB/9)
	return math.Max(0.02, math.Min(0.96, a*rhythm(t, s.m.UTC)*heavy*1.1))
}

// online says whether a person is online on a server at t, and with how many devices.
func (w *world) online(u *user, s *server, t int64) (bool, int) {
	p := w.presence(u, s, t)
	slot := t / 1200
	if p == 0 || hash01(12, int64(u.idx), int64(s.idx), slot) >= p {
		return false, 0
	}
	n := 1 + int(hash01(13, int64(u.idx), int64(s.idx), slot)*float64(u.p.Devices))
	return true, min(n, u.p.Devices, len(u.devices))
}

// split shares a person's bytes on a server between the protocols they use there.
func split(nodes []int64, kinds map[int64]string, bytes float64) map[int64]float64 {
	weight := func(id int64) float64 {
		switch kinds[id] {
		case "vless":
			return 6
		case "hysteria2":
			return 3
		case "wireguard":
			return 2
		}
		return 1
	}
	total := 0.0
	for _, id := range nodes {
		total += weight(id)
	}
	out := map[int64]float64{}
	for _, id := range nodes {
		out[id] = bytes * weight(id) / total
	}
	return out
}

// downShare is how much of a person's traffic is downloads.
func downShare(u *user) float64 { return 0.84 + 0.1*hash01(14, int64(u.idx)) }

// ---------------------------------------------------------------- the host

// sample is what a server's agent would measure at one moment.
type sample struct {
	cpu, load1, load5, load15   float64
	mem, disk                   float64 // bytes
	rx, tx, diskRead, diskWrite float64 // bytes per second
	tcp, udp, online            int
	temp                        float64
}

// measure makes a server's measurements at time t from what moves through it then (bytes a second)
// and how many devices are online.
func measure(s *server, t int64, through float64, devices int) sample {
	m := s.m
	k := int64(s.idx)
	work := through / (float64(m.Cores) * 9e6) * 100 // per cent of the processors
	cpu := m.CPUBase + work + 2.2*noise(t, 600, 21, k) + 1.2*noise(t, 90, 22, k)
	if hash01(23, k, t/3600) < 0.07 && t%3600 < 240 { // a job now and then (updates, a backup)
		cpu += 12 + 10*hash01(24, k, t/3600)
	}
	cpu = math.Min(100, cpu)
	load := func(period float64) float64 {
		return math.Max(0.01, float64(m.Cores)*(cpu/100)*1.15+0.08*noise(t, period, 25, k))
	}
	total := m.MemGB * gb
	mem := total * (m.MemBase + 0.05*noise(t, 7200, 26, k) + 0.06*math.Min(1, through/4e6))
	disk := m.DiskGB * 1e9 * (m.DiskBase + 0.002*float64(t-setupTime+30*86400)/86400/30)
	udp := 4 + int(3*noise(t, 300, 27, k))
	for _, n := range s.nodes {
		if n.Kind == "hysteria2" || n.Kind == "wireguard" {
			udp += devices * 3
			break
		}
	}
	out := sample{
		cpu: cpu, load1: load(60), load5: load(400), load15: load(1200),
		mem: mem, disk: disk,
		rx:        through*1.03 + 900 + 600*noise(t, 120, 28, k),
		tx:        through + 1400 + 800*noise(t, 120, 29, k),
		diskRead:  2000 + 30000*sq(noise(t, 180, 30, k)),
		diskWrite: 12000 + through*0.004 + 60000*sq(noise(t, 240, 31, k)),
		tcp:       9 + devices*14 + int(8*noise(t, 300, 32, k)),
		udp:       udp,
		online:    devices,
	}
	if m.Temps {
		out.temp = 38 + cpu*0.45 + 3*noise(t, 1800, 33, k)
	}
	return out
}

// ---------------------------------------------------------------- pings

// rtt is the round trip from a server to a monitor's target, in milliseconds.
func rtt(m machine, mon monitor, t int64, k, j int64) float64 {
	base := 1.2 + 2.5*hash01(40, k, j)
	if !mon.Anycast {
		km := distance(m.Lat, m.Lon, mon.Lat, mon.Lon)
		base = 2 + km*2*1.45/200 // light in fibre, and the roads it takes
	}
	jitter := base * (0.02 + 0.06*noise(t, 300, 41, k, j))
	if hash01(42, k, j, t/1800) < 0.04 { // a slow half hour on the way
		jitter += base * 0.4
	}
	return base + jitter
}

func distance(lat1, lon1, lat2, lon2 float64) float64 {
	r := math.Pi / 180
	a := sq(math.Sin((lat2-lat1)*r/2)) + math.Cos(lat1*r)*math.Cos(lat2*r)*sq(math.Sin((lon2-lon1)*r/2))
	return 6371 * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// ---------------------------------------------------------------- addresses

// devicesFor gives each person's devices addresses from the documentation ranges, one or two of
// them IPv6.
func devicesFor(i int, n int) []string {
	out := make([]string, 0, n)
	for d := 0; d < n; d++ {
		if d == 2 {
			out = append(out, netip.MustParseAddr(fmt.Sprintf("2001:db8:%x::%x", 0xa0+i, d+1)).String())
			continue
		}
		base := "198.51.100"
		if d == 1 {
			base = "192.0.2"
		}
		out = append(out, fmt.Sprintf("%s.%d", base, 10+i*6+d))
	}
	return out
}

// instance is a stable id for a demo agent installation.
func instance(token string) string {
	h := fnv.New64a()
	h.Write([]byte(strings.TrimSpace(token)))
	return fmt.Sprintf("demo%016x", h.Sum64())
}
