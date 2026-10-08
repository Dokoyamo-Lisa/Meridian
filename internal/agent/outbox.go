package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"sync"
	"time"

	"meridian/internal/agent/sys"
	"meridian/internal/proto"
)

// outbox makes accounting exact across network failures and restarts. Data accumulates in Accum;
// when nothing is in flight it becomes Pending with the next sequence number and is resent as-is
// until the panel acknowledges that number. The panel applies each number once. Both live in a
// file, so an agent restart neither loses nor repeats a batch. Losing the file starts a new
// instance id, which tells the panel to start counting afresh.
type outbox struct {
	mu       sync.Mutex
	path     string
	Instance string       `json:"instance"`
	Seq      int64        `json:"seq"`
	Pending  *proto.Batch `json:"pending,omitempty"`
	Accum    *proto.Batch `json:"accum,omitempty"`
	// Baselines are where the counters and logs stood when the data above was taken from them
	Baselines *Baselines `json:"baselines,omitempty"`
}

// Baselines are where the kernel's counters and the cores' logs stood when the data in the outbox was
// taken from them. They are saved together with that data, so a restarted agent goes on from there:
// it neither counts again what the kernel still holds (WireGuard peers and nftables rules outlive the
// agent) nor loses what came since its last report.
type Baselines struct {
	Boot string               `json:"boot"` // counters from another boot started over
	WG   map[string][2]int64  `json:"wg,omitempty"`
	NFT  map[string][2]uint64 `json:"nft,omitempty"`
	Xray sys.LogPos           `json:"xray"`
	Hy   map[int64]sys.LogPos `json:"hy,omitempty"`
}

func loadOutbox(path string) *outbox {
	o := &outbox{path: path}
	if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, o) == nil && o.Instance != "" {
		o.path = path
		return o
	}
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return &outbox{path: path, Instance: hex.EncodeToString(buf)}
}

func (o *outbox) save() {
	o.mu.Lock()
	b, err := json.Marshal(o)
	o.mu.Unlock()
	if err != nil {
		return
	}
	tmp := o.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, o.path)
	}
}

const (
	maxIPs   = 20000
	maxDests = 50000
)

// add merges new data into the accumulating batch.
func (o *outbox) add(traffic []proto.UserTraffic, fwds []proto.FwdTraffic, ips []proto.IPSeen, dests []proto.DestSeen,
	nic proto.NICDelta, events []proto.AgentEvent, geoDrops int64, base *Baselines) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.Baselines = base
	a := o.Accum
	if a == nil {
		a = &proto.Batch{From: time.Now().Unix()}
		o.Accum = a
	}
	a.To = time.Now().Unix()

	tIdx := map[[2]int64]int{}
	for i, t := range a.Traffic {
		tIdx[[2]int64{t.Sub, t.Node}] = i
	}
	for _, t := range traffic {
		if i, ok := tIdx[[2]int64{t.Sub, t.Node}]; ok {
			a.Traffic[i].Up += t.Up
			a.Traffic[i].Down += t.Down
		} else {
			tIdx[[2]int64{t.Sub, t.Node}] = len(a.Traffic)
			a.Traffic = append(a.Traffic, t)
		}
	}

	fIdx := map[int64]int{}
	for i, f := range a.Forwards {
		fIdx[f.ID] = i
	}
	for _, f := range fwds {
		if i, ok := fIdx[f.ID]; ok {
			a.Forwards[i].Up += f.Up
			a.Forwards[i].Down += f.Down
			a.Forwards[i].Conns += f.Conns
		} else {
			fIdx[f.ID] = len(a.Forwards)
			a.Forwards = append(a.Forwards, f)
		}
	}

	type ik struct {
		s, n int64
		ip   string
	}
	iIdx := map[ik]int{}
	for i, x := range a.IPs {
		iIdx[ik{x.Sub, x.Node, x.IP}] = i
	}
	for _, x := range ips {
		k := ik{x.Sub, x.Node, x.IP}
		if i, ok := iIdx[k]; ok {
			y := &a.IPs[i]
			y.First = min(y.First, x.First)
			y.Last = max(y.Last, x.Last)
			y.Conns += x.Conns
		} else if len(a.IPs) < maxIPs {
			iIdx[k] = len(a.IPs)
			a.IPs = append(a.IPs, x)
		}
	}

	type dk struct {
		s, n int64
		h    string
		p    int
		nw   string
	}
	dIdx := map[dk]int{}
	for i, x := range a.Dests {
		dIdx[dk{x.Sub, x.Node, x.Host, x.Port, x.Net}] = i
	}
	for _, x := range dests {
		k := dk{x.Sub, x.Node, x.Host, x.Port, x.Net}
		if i, ok := dIdx[k]; ok {
			y := &a.Dests[i]
			y.Conns += x.Conns
			y.Bytes += x.Bytes
			y.Last = max(y.Last, x.Last)
		} else if len(a.Dests) < maxDests {
			dIdx[k] = len(a.Dests)
			a.Dests = append(a.Dests, x)
		}
	}

	a.NIC.RX += nic.RX
	a.NIC.TX += nic.TX
	a.GeoDrops += geoDrops
	a.Events = append(a.Events, events...)
	if len(a.Events) > 500 {
		a.Events = a.Events[len(a.Events)-500:]
	}
}

// next returns the batch to send: the one in flight, or a new one made from what accumulated.
func (o *outbox) next() *proto.Batch {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.Pending == nil && o.Accum != nil {
		o.Seq++
		o.Accum.Seq = o.Seq
		o.Pending, o.Accum = o.Accum, nil
	}
	return o.Pending
}

// ack drops the in-flight batch once the panel has it.
func (o *outbox) ack(seq int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.Pending != nil && o.Pending.Seq == seq {
		o.Pending = nil
	}
}
