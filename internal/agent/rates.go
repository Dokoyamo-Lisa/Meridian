package agent

import (
	"sort"

	"meridian/internal/proto"
)

// Speed now, per user and per client address, for the panel's Monitor (proto.Live.Rates, IPRates).
// It is measured, never worked out from the bandwidth counters: those stay as they are.

// userRates is each user's speed over the last report's interval (secs), from what their protocols
// carried for them. Only users are in traffic: a proxy pass's exit counts its entry's traffic as the
// entry's own identity ("p<node>"), which is no user's - a user going through a pass is counted
// once, on the server they connect to, and never again on the exit.
func userRates(traffic []proto.UserTraffic, secs float64) []proto.UserRate {
	if secs < 1 {
		return nil
	}
	by := map[int64]*proto.UserRate{}
	for _, t := range traffic {
		if t.Sub <= 0 {
			continue
		}
		r := by[t.Sub]
		if r == nil {
			r = &proto.UserRate{Sub: t.Sub}
			by[t.Sub] = r
		}
		r.Up += t.Up
		r.Down += t.Down
	}
	out := make([]proto.UserRate, 0, len(by))
	for _, r := range by {
		r.Up, r.Down = int64(float64(r.Up)/secs), int64(float64(r.Down)/secs)
		if r.Up > 0 || r.Down > 0 {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sub < out[j].Sub })
	return out
}

// nodeRates is the same speed protocol by protocol.
func nodeRates(traffic []proto.UserTraffic, secs float64) []proto.NodeRate {
	if secs < 1 {
		return nil
	}
	by := map[[2]int64]*proto.NodeRate{}
	for _, t := range traffic {
		if t.Sub <= 0 {
			continue
		}
		k := [2]int64{t.Sub, t.Node}
		if by[k] == nil {
			by[k] = &proto.NodeRate{Sub: t.Sub, Node: t.Node}
		}
		by[k].Up += t.Up
		by[k].Down += t.Down
	}
	out := make([]proto.NodeRate, 0, len(by))
	for _, r := range by {
		r.Up, r.Down = int64(float64(r.Up)/secs), int64(float64(r.Down)/secs)
		if r.Up > 0 || r.Down > 0 {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sub != out[j].Sub {
			return out[i].Sub < out[j].Sub
		}
		return out[i].Node < out[j].Node
	})
	return out
}

// ipRates is the speed of each client address users are online from (online), from what the
// firewall counted on the users' protocols' ports (nft.TakeIPBytes). Any other sender is left out: a
// proxy pass's entry server talking to this exit, a scanner, a panel - none of them is a user's device.
func ipRates(bytes map[string][2]int64, online []proto.OnlineUser, secs float64) []proto.IPRate {
	if secs < 1 || len(bytes) == 0 {
		return nil
	}
	users := map[string]bool{}
	for _, u := range online {
		for _, ip := range u.IPs {
			users[ip.IP] = true
		}
	}
	var out []proto.IPRate
	for ip, b := range bytes {
		if !users[ip] {
			continue
		}
		r := proto.IPRate{IP: ip, Up: int64(float64(b[0]) / secs), Down: int64(float64(b[1]) / secs)}
		if r.Up > 0 || r.Down > 0 {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out
}

// minRateSpan is the shortest time a speed is measured over. Reports faster than that (the panel's live
// view) are taken together - the last ones covering at least this long - so the speed still changes
// with every report but never reads a pause as "idle": traffic comes in bursts (a download, a video
// fetching its next pieces), with pauses of a second or two even at a steady few hundred KB/s - measured
// in the kernel's own counts as much as in Xray's. Only a user quiet for this long reads as idle.
const minRateSpan = 5.0

// rateWindow holds the last reports' counts for one panel's speeds (rates.go): as many as cover
// minRateSpan, and no more. At reports of 5 seconds or longer it holds only the last one.
type rateWindow struct {
	parts []ratePart
}

type ratePart struct {
	secs    float64
	traffic []proto.UserTraffic
	ips     map[string][2]int64
}

// add puts in what a report counted over secs and gives back what the window holds: the traffic and
// client addresses' bytes of its reports, and the time they cover.
func (w *rateWindow) add(secs float64, traffic []proto.UserTraffic, ips map[string][2]int64) ([]proto.UserTraffic, map[string][2]int64, float64) {
	if secs <= 0 { // the first report: nothing to measure over yet
		w.parts = nil
		return nil, nil, 0
	}
	w.parts = append(w.parts, ratePart{secs: secs, traffic: traffic, ips: ips})
	total := 0.0
	for _, p := range w.parts {
		total += p.secs
	}
	for len(w.parts) > 1 && total-w.parts[0].secs >= minRateSpan {
		total -= w.parts[0].secs
		w.parts = w.parts[1:]
	}
	var all []proto.UserTraffic
	var sum map[string][2]int64
	for _, p := range w.parts {
		all = append(all, p.traffic...)
		for ip, b := range p.ips {
			if sum == nil {
				sum = map[string][2]int64{}
			}
			c := sum[ip]
			sum[ip] = [2]int64{c[0] + b[0], c[1] + b[1]}
		}
	}
	return all, sum, total
}
