//go:build linux

package solo

import (
	"net/netip"

	"github.com/ti-mo/conntrack"
)

// udpPeers are the addresses with UDP flows to these local ports, as conntrack tracks them.
func udpPeers(ports map[int]bool) map[int][]netip.Addr {
	if len(ports) == 0 {
		return nil
	}
	c, err := conntrack.Dial(nil)
	if err != nil {
		return nil
	}
	defer c.Close()
	flows, err := c.Dump(nil)
	if err != nil {
		return nil
	}
	out := map[int][]netip.Addr{}
	for _, f := range flows {
		t := f.TupleOrig
		if t.Proto.Protocol != 17 || !ports[int(t.Proto.DestinationPort)] {
			continue
		}
		out[int(t.Proto.DestinationPort)] = append(out[int(t.Proto.DestinationPort)], t.IP.SourceAddress.Unmap())
	}
	return out
}
