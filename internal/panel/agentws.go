package panel

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"meridian/internal/proto"
	"meridian/internal/seal"
	"meridian/internal/ws"
)

// Agents keep one WebSocket open to the panel (proto.WSPath) instead of making HTTP requests: the
// upgrade is signed like any request, and each message on it is one more signed request - the
// state's long poll, a report, a country list - which goes to the same handler as over HTTP, so the
// checks (signature, time, nonce, sealing) are exactly the same. Agents go back to HTTP by themselves
// while a WebSocket cannot be opened, and Settings can have them use HTTP only.

const (
	wsMaxMessage = 33 << 20        // a report (32 MB at most) and its head
	wsInFlight   = 4               // one agent's requests at a time: its long poll, a report, a country list
	wsIdle       = 3 * time.Minute // a connection without a sign of life this long is closed (agents ping every 25 s)
)

// handleAgentWS serves one agent's WebSocket until it closes.
func (p *Panel) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	srv, _, err := p.agentAuth(r, nil)
	if err != nil {
		agentDeny(w, err)
		return
	}
	conn, err := ws.Accept(w, r, wsMaxMessage)
	if err != nil {
		return // Accept answered
	}
	conn.SetIdle(wsIdle)
	ctx, cancel := context.WithCancel(r.Context())
	slots := make(chan struct{}, wsInFlight)
	var wg sync.WaitGroup
	for {
		msg, err := conn.Read()
		if err != nil {
			break
		}
		var head proto.WSRequest
		body, err := proto.WSUnpack(msg, &head)
		if err != nil {
			break // not an agent of ours: the connection ends
		}
		slots <- struct{}{} // a fifth request waits for one of the four to finish
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			ans, out := p.agentWSRequest(ctx, r, srv.ID, &head, body)
			b, err := proto.WSPack(ans, out)
			if err == nil {
				err = conn.Write(b)
			}
			if err != nil {
				conn.Close() // the read loop ends too
			}
		}()
	}
	cancel() // long polls in flight return at once
	conn.Close()
	wg.Wait()
}

// agentWSRequest answers one request that came over an agent's WebSocket the way the same request
// over HTTP is answered. The server is the connection's, whatever the message says; the request must
// still carry that server's signature.
func (p *Panel) agentWSRequest(ctx context.Context, up *http.Request, serverID int64, in *proto.WSRequest, body []byte) (proto.WSAnswer, []byte) {
	ans := proto.WSAnswer{ID: in.ID}
	path, _, _ := strings.Cut(in.Path, "?")
	var h http.HandlerFunc
	switch {
	case in.Method == http.MethodGet && path == "/agent/v1/state":
		h = p.handleAgentState
	case in.Method == http.MethodPost && path == "/agent/v1/report":
		h = p.handleAgentReport
	case in.Method == http.MethodGet && strings.HasPrefix(path, "/agent/v1/geo/"):
		h = p.handleAgentGeo
	default:
		ans.Status = http.StatusNotFound
		return ans, []byte("not over the WebSocket - send it as an HTTP request")
	}
	req, err := http.NewRequestWithContext(ctx, in.Method, "http://panel"+in.Path, bytes.NewReader(body))
	if err != nil || req.URL.RequestURI() != in.Path { // what is answered is exactly what was signed
		ans.Status = http.StatusBadRequest
		return ans, []byte("bad request path")
	}
	req.SetPathValue("hash", strings.TrimPrefix(path, "/agent/v1/geo/"))
	req.Host, req.RemoteAddr = up.Host, up.RemoteAddr
	req.Header.Set("User-Agent", up.UserAgent())
	req.Header.Set(seal.HeaderServer, strconv.FormatInt(serverID, 10))
	req.Header.Set(seal.HeaderTime, strconv.FormatInt(in.Time, 10))
	req.Header.Set(seal.HeaderNonce, in.Nonce)
	req.Header.Set(seal.HeaderSign, in.Sign)
	if in.Type != "" {
		req.Header.Set("Content-Type", in.Type)
	}
	rec := &wsAnswerWriter{header: http.Header{}}
	h(rec, req)
	ans.Status = rec.status
	if ans.Status == 0 {
		ans.Status = http.StatusOK
	}
	ans.Type = rec.header.Get("Content-Type")
	return ans, rec.body.Bytes()
}

// wsAnswerWriter keeps what a handler answers, to send it on the WebSocket.
type wsAnswerWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *wsAnswerWriter) Header() http.Header { return w.header }

func (w *wsAnswerWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func (w *wsAnswerWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.body.Write(b)
}
