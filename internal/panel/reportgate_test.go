package panel

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"meridian/internal/seal"
)

// countingBody counts what is read of a request's body.
type countingBody struct {
	r io.Reader
	n int
}

func (c *countingBody) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n += n
	return n, err
}
func (c *countingBody) Close() error { return nil }

// A report from nobody the panel knows is refused before its body is read: anyone could otherwise
// make the panel hold 32 MB per request.
func TestReportRefusedBeforeBody(t *testing.T) {
	h := newHarness(t)
	for name, hdr := range map[string]map[string]string{
		"no headers":     {},
		"unknown server": {seal.HeaderServer: "999", seal.HeaderTime: strconv.FormatInt(now(), 10), seal.HeaderNonce: "abcdefgh12"},
		"stale clock":    {seal.HeaderServer: "1", seal.HeaderTime: "1000", seal.HeaderNonce: "abcdefgh12"},
	} {
		body := &countingBody{r: bytes.NewReader(make([]byte, 4<<20))}
		req := httptest.NewRequest(http.MethodPost, "/agent/v1/report", nil)
		req.Body, req.ContentLength = body, 4<<20
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.p.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized || body.n > 0 {
			t.Errorf("%s: %d, %d bytes read", name, w.Code, body.n)
		}
	}
}

// The gate lets an address read two bodies at once and all bodies together no more than the budget;
// room given back lets the next one in.
func TestReportGate(t *testing.T) {
	old := reportWait
	reportWait = 100 * time.Millisecond
	t.Cleanup(func() { reportWait = old })
	var g reportGate
	for i := range 2 {
		if !g.take("a", 10) {
			t.Fatalf("body %d from one address", i+1)
		}
	}
	if g.take("a", 10) {
		t.Error("a third body from the same address at once")
	}
	if !g.take("b", reportBudget-20) {
		t.Fatal("another address, within the budget")
	}
	if g.take("c", 1) {
		t.Error("past the budget")
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		g.give("a", 10)
	}()
	if !g.take("c", 10) {
		t.Error("room given back was not taken")
	}
	g.give("a", 10)
	g.give("b", reportBudget-20)
	g.give("c", 10)
	if g.used != 0 || len(g.perIP) != 0 {
		t.Errorf("left over: %d bytes, %v", g.used, g.perIP)
	}
}
