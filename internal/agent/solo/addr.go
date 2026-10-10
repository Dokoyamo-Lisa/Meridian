package solo

import (
	"fmt"
	"net/netip"
)

func parseAddr(s string) (netip.Addr, error) {
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" || a.IsUnspecified() {
		return netip.Addr{}, fmt.Errorf("bad address %q", s)
	}
	return a, nil
}
