//go:build !linux

package solo

import "net/netip"

func udpPeers(map[int]bool) map[int][]netip.Addr { return nil }
