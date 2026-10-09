package agent

import (
	"log/slog"
	"time"

	"meridian/internal/proto"
)

// Users' limits on this server (State.Speed, State.Refuse). Speed limits are enforced by nftables on
// the addresses each limited user's devices are connected from - known from the cores' online lists
// at every report, so a new device is limited within one report interval. Devices turned away over a
// device limit are refused at Hysteria2's sign-in (Xray has a routing rule for them in its
// configuration).

// limitDevices gives the speed limits the addresses each limited user is connected from now.
func (a *Agent) limitDevices(st *proto.State, online []proto.OnlineUser) {
	if st == nil || len(st.Speed) == 0 {
		return
	}
	limited := map[int64]bool{}
	for _, l := range st.Speed {
		limited[l.Sub] = true
	}
	by := map[int64][]string{}
	for _, u := range online {
		if !limited[u.Sub] {
			continue
		}
		for _, ip := range u.IPs {
			by[u.Sub] = append(by[u.Sub], ip.IP)
		}
	}
	if err := a.nft.SetSpeedIPs(by); err != nil && time.Since(a.limitWarned) > 10*time.Minute {
		a.limitWarned = time.Now()
		slog.Warn("speed limits", "err", err)
	}
}
