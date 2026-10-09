package agent

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"meridian/internal/proto"
	"meridian/internal/seal"
	"meridian/internal/ws"
)

// fakePanel answers the agent like the panel - over HTTP and on its WebSocket: it checks every
// signature and seals each answer to its request. It records the name the agent asked for (Host and
// TLS server name) and how requests came.
type fakePanel struct {
	*httptest.Server
	keys  *seal.Keys
	noWS  atomic.Bool                   // answers the WebSocket with 404, like an older panel
	hold  atomic.Pointer[chan struct{}] // state requests that wait (a long poll) wait for it to close
	mu    sync.Mutex
	hosts []string
	snis  []string
	http  int // requests that came as HTTP requests
	onWS  int // requests that came on a WebSocket
	wsUp  int // WebSockets opened
	conns []*ws.Conn
}

func newFakePanel(t *testing.T, secret string) *fakePanel {
	t.Helper()
	keys, err := seal.Derive(secret)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakePanel{keys: keys}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakePanel) port() string {
	_, port, _ := net.SplitHostPort(f.Listener.Addr().String())
	return port
}

func (f *fakePanel) counts() (httpReqs, wsReqs, opened int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.http, f.onWS, f.wsUp
}

// dropWebSockets closes every WebSocket, as a panel restart does.
func (f *fakePanel) dropWebSockets() {
	f.mu.Lock()
	conns := f.conns
	f.conns = nil
	f.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

func (f *fakePanel) serve(w http.ResponseWriter, r *http.Request) {
	ts, _ := strconv.ParseInt(r.Header.Get(seal.HeaderTime), 10, 64)
	nonce, sign := r.Header.Get(seal.HeaderNonce), r.Header.Get(seal.HeaderSign)
	f.mu.Lock()
	f.hosts = append(f.hosts, r.Host)
	f.snis = append(f.snis, r.TLS.ServerName)
	f.mu.Unlock()
	if r.URL.Path == proto.WSPath {
		if f.noWS.Load() {
			http.NotFound(w, r)
			return
		}
		if !f.keys.Verify(r.Method, r.URL.RequestURI(), ts, nonce, nil, sign) {
			http.Error(w, "bad signature - the token may have been rotated", http.StatusUnauthorized)
			return
		}
		c, err := ws.Accept(w, r, 64<<20)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.wsUp++
		f.conns = append(f.conns, c)
		f.mu.Unlock()
		for {
			msg, err := c.Read()
			if err != nil {
				return
			}
			var head proto.WSRequest
			body, err := proto.WSUnpack(msg, &head)
			if err != nil {
				return
			}
			f.mu.Lock()
			f.onWS++
			f.mu.Unlock()
			go func() {
				code, ctype, out := f.answer(head.Method, head.Path, head.Time, head.Nonce, head.Sign, body)
				b, _ := proto.WSPack(proto.WSAnswer{ID: head.ID, Status: code, Type: ctype}, out)
				_ = c.Write(b)
			}()
		}
	}
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.http++
	f.mu.Unlock()
	code, ctype, out := f.answer(r.Method, r.URL.RequestURI(), ts, nonce, sign, body)
	w.Header().Set("Content-Type", ctype)
	w.WriteHeader(code)
	w.Write(out)
}

// answer is the panel's answer to one request, however it came.
func (f *fakePanel) answer(method, path string, ts int64, nonce, sign string, body []byte) (int, string, []byte) {
	if !f.keys.Verify(method, path, ts, nonce, body, sign) {
		return http.StatusUnauthorized, "text/plain", []byte("bad signature")
	}
	reply := func(plain string) (int, string, []byte) {
		return http.StatusOK, seal.ContentType, f.keys.Seal(seal.ReplyContext(path, nonce), []byte(plain))
	}
	switch {
	case strings.HasPrefix(path, "/agent/v1/state"):
		if h := f.hold.Load(); h != nil && !strings.Contains(path, "wait=0") {
			select {
			case <-*h:
			case <-time.After(10 * time.Second):
			}
		}
		return reply(`{"contract":4,"rev":"abc","server_id":7}`)
	case method == http.MethodPost && path == "/agent/v1/report":
		if _, err := f.keys.Open(path, body); err != nil {
			return http.StatusBadRequest, "text/plain", []byte("cannot open body")
		}
		return reply(`{"ack":1}`)
	}
	return http.StatusNotFound, "text/plain", []byte("not found")
}
