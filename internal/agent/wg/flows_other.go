//go:build !linux

package wg

import (
	"errors"
	"net/netip"

	"meridian/internal/proto"
)

const WGMark uint32 = 0x4d570000

type flowMonitor struct{}

func newFlowMonitor() (*flowMonitor, error) { return nil, errors.New("conntrack needs Linux") }

func (m *flowMonitor) stop() {}

func (m *flowMonitor) collect(map[netip.Addr]struct{ sub, node int64 }, func(client, dst netip.Addr) string) []proto.DestSeen {
	return nil
}

func dropFlows([]netip.Prefix) int { return 0 }
