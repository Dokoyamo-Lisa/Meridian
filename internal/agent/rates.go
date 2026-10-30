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
