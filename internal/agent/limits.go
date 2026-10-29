package agent

import (
	"fmt"
	"log/slog"
	"slices"
	"time"

	"meridian/internal/proto"
)

// Users' limits on this server (State.Speed, State.Refuse). Speed limits are enforced by nftables
// (nft/speed.go): on a user's own ports (mieru, Snell, AnyTLS), on their WireGuard tunnel addresses,
// and on Xray's ports by the addresses their devices are connected from - known from the cores'
// online lists at every report, so a new device is limited within one report interval. Devices
// turned away over a device limit are refused at Hysteria2's sign-in (Xray has a routing rule for
// them in its configuration).

// speedAddrs are the addresses each limited user's Xray limit may hold: those their devices are
// connected from that no other user is online from now, on any protocol. An address shared with
// another user (one network behind a NAT) is left out - limiting it would slow the other user too,
// or make two limited users share one rate - and listed in shared (user -> addresses).
func speedAddrs(st *proto.State, online []proto.OnlineUser) (by, shared map[int64][]string) {
	limited := map[int64]bool{}
	for _, l := range st.Speed {
		limited[l.Sub] = true
	}
	users := map[string]map[int64]bool{} // address -> who is online from it
	for _, u := range online {
		for _, ip := range u.IPs {
			if users[ip.IP] == nil {
				users[ip.IP] = map[int64]bool{}
			}
			users[ip.IP][u.Sub] = true
		}
	}
	by, shared = map[int64][]string{}, map[int64][]string{}
	for _, u := range online {
		if !limited[u.Sub] {
			continue
		}
		for _, ip := range u.IPs {
			if len(users[ip.IP]) > 1 {
				if !slices.Contains(shared[u.Sub], ip.IP) {
					shared[u.Sub] = append(shared[u.Sub], ip.IP)
				}
				continue
			}
			by[u.Sub] = append(by[u.Sub], ip.IP)
		}
	}
	return by, shared
}

// limitDevices gives the speed limits the addresses each limited user is connected from now, and
// tells the panel (an event, once an hour per user and address) about a limit left off a shared one.
func (a *Agent) limitDevices(st *proto.State, online []proto.OnlineUser) {
	if st == nil || len(st.Speed) == 0 {
		return
	}
	by, shared := speedAddrs(st, online)
	if a.sharedTold == nil {
		a.sharedTold = map[string]time.Time{}
	}
	for sub, ips := range shared {
		for _, ip := range ips {
			k := fmt.Sprintf("%d %s", sub, ip)
			if time.Since(a.sharedTold[k]) < time.Hour {
				continue
			}
			a.sharedTold[k] = time.Now()
			a.event("speed_shared", "warn", fmt.Sprintf("The speed limit of user %d is not applied on Xray's protocols to %s: "+
				"another user is connected from that address too and would be slowed down with them - WireGuard, mieru, Snell "+
				"and AnyTLS are limited per user wherever they connect from", sub, ip))
		}
	}
	if err := a.nft.SetSpeedIPs(by); err != nil && time.Since(a.limitWarned) > 10*time.Minute {
		a.limitWarned = time.Now()
		slog.Warn("speed limits", "err", err)
	}
}
