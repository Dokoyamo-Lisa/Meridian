package panel

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

// An agent's report over HTTP is signed body and all, so the panel learns who sent it only after
// reading it - up to reportMax. Until then anyone could send such bodies, many at once, and fill the
// panel's memory. reportGate bounds that: the bodies being read take at most reportBudget together,
// and one address reads at most reportPerIP at a time. A report that has to wait too long is answered
// 503 and the agent sends it again (agents report over their WebSocket in any case, which is checked
// before anything is read: agentws.go).

const (
	reportMax    = 32 << 20  // the largest report
	reportBudget = 128 << 20 // all report bodies being read at once
	reportPerIP  = 2         // report bodies one address may have being read at once

)

// reportWait is how long a report waits for room (tests shorten it).
var reportWait = 15 * time.Second

type reportGate struct {
	mu    sync.Mutex
	used  int64
	perIP map[string]int
	freed chan struct{} // closed and replaced whenever room is given back
}

var errReportBusy = errStatus(http.StatusServiceUnavailable, "too many reports at once - send it again in a moment")

// take reserves n bytes for a body from ip, waiting up to reportWait for room.
func (g *reportGate) take(ip string, n int64) bool {
	deadline := time.Now().Add(reportWait)
	for {
		g.mu.Lock()
		if g.perIP == nil {
			g.perIP = map[string]int{}
		}
		if g.freed == nil {
			g.freed = make(chan struct{})
		}
		if g.perIP[ip] < reportPerIP && g.used+n <= reportBudget {
			g.perIP[ip]++
			g.used += n
			g.mu.Unlock()
			return true
		}
		wait := g.freed
		g.mu.Unlock()
		left := time.Until(deadline)
		if left <= 0 {
			return false
		}
		select {
		case <-wait:
		case <-time.After(left):
			return false
		}
	}
}

func (g *reportGate) give(ip string, n int64) {
	g.mu.Lock()
	g.used -= n
	if g.perIP[ip]--; g.perIP[ip] <= 0 {
		delete(g.perIP, ip)
	}
	close(g.freed)
	g.freed = make(chan struct{})
	g.mu.Unlock()
}

// read reads a report's body within the gate: as many bytes as it says it has (all of reportMax when
// it does not say), never more.
func (g *reportGate) read(w http.ResponseWriter, r *http.Request, ip string) ([]byte, error) {
	n := int64(reportMax)
	if r.ContentLength >= 0 {
		if r.ContentLength > reportMax {
			return nil, errStatus(http.StatusRequestEntityTooLarge, "body too large")
		}
		n = r.ContentLength
	}
	if !g.take(ip, n) {
		return nil, errReportBusy
	}
	defer g.give(ip, n)
	// sized once: a buffer that grows by doubling would hold up to twice the body
	buf := bytes.NewBuffer(make([]byte, 0, n+bytes.MinRead))
	if _, err := io.Copy(buf, http.MaxBytesReader(w, r.Body, n)); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return nil, errStatus(http.StatusRequestEntityTooLarge, "body too large")
		}
		return nil, errStatus(http.StatusBadRequest, "the body could not be read")
	}
	return buf.Bytes(), nil
}
