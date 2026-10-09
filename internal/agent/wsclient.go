package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"meridian/internal/proto"
	"meridian/internal/seal"
	"meridian/internal/ws"
)

// The agent's WebSocket to the panel (see proto.WSRequest): one lasting connection - through the
// relay or directly, like any request - that carries the same signed requests and sealed answers as
// HTTP, the state's long poll and the reports side by side. While one cannot be opened (an older
// panel, a proxy in front of it that does not pass WebSockets), requests go as HTTP requests and it is
// tried again later; Settings can have the agent use HTTP only.

const (
	wsMaxMessage = 64<<20 + 64<<10  // an answer: a state, as large as over HTTP, and its head
	wsAnswerWait = 90 * time.Second // no answer for this long: the connection is taken for dead
	wsPing       = 25 * time.Second
	wsQuiet      = 75 * time.Second // nothing at all heard for this long, pongs included: dead
	wsOpenWait   = 30 * time.Second
	wsRetry      = time.Minute      // after opening failed: HTTP for this long...
	wsRetryMax   = 15 * time.Minute // ...doubling while it keeps failing, up to this
	wsNoSupport  = time.Hour        // the panel's address answered, but not as a WebSocket: HTTP this long

	connWebSocket = "websocket"
	connHTTP      = "http"
)

// errRetired ends a connection a newer one took over from.
var errRetired = errors.New("replaced by a newer connection")

// noWebSocket: the panel's address answered the upgrade with something else.
type noWebSocket struct{ status int }

func (e *noWebSocket) Error() string {
	return fmt.Sprintf("the panel's address answered %d instead of opening a WebSocket - a proxy in front of the panel must pass WebSockets on (see the docs)", e.status)
}

type wsLink struct {
	c *Client

	openMu    sync.Mutex // one attempt to open at a time
	mu        sync.Mutex
	conn      *wsConn
	off       bool      // Settings say HTTP
	retryAt   time.Time // opening failed: HTTP until then
	backoff   time.Duration
	why       string // why opening failed last
	last      string // what the last request that got through went over
	switching bool   // a connection through the relay is being opened to replace a direct one
}

// wanted says whether the next request goes over the WebSocket.
func (l *wsLink) wanted() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.off && !time.Now().Before(l.retryAt)
}

// setOff follows Settings: true is HTTP only.
func (l *wsLink) setOff(off bool) {
	l.mu.Lock()
	cur := l.conn
	if off {
		l.conn = nil
	}
	changed := l.off != off
	l.off = off
	if changed {
		l.retryAt, l.backoff, l.why = time.Time{}, 0, ""
	}
	l.mu.Unlock()
	if off && cur != nil {
		cur.retire()
	}
}

// failed notes that the WebSocket could not be had: HTTP for a while.
func (l *wsLink) failed(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var nw *noWebSocket
	if errors.As(err, &nw) {
		l.retryAt = time.Now().Add(wsNoSupport)
	} else {
		l.backoff = min(max(2*l.backoff, wsRetry), wsRetryMax)
		l.retryAt = time.Now().Add(l.backoff)
	}
	l.why = wsProblem(err)
}

// wsProblem words why the WebSocket could not be opened, for the panel's server page.
func wsProblem(err error) string {
	var nw *noWebSocket
	var ne net.Error
	switch {
	case errors.As(err, &nw):
		return nw.Error()
	case errors.Is(err, syscall.ECONNREFUSED):
		return "the panel's address refused the connection"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, syscall.ECONNRESET):
		return "the connection was closed on the way to the panel"
	case errors.As(err, &ne) && ne.Timeout():
		return "no answer from the panel's address in time"
	}
	msg := strings.Join(strings.Fields(err.Error()), " ")
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return msg
}

// used notes what a request that got through went over.
func (l *wsLink) used(conn string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.last = conn
	if conn == connWebSocket {
		l.backoff, l.why = 0, ""
	}
}

// status is what the agent talks to the panel over, and why not its WebSocket when it wants one.
func (l *wsLink) status() (conn, why string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.off || l.last == connWebSocket {
		return l.last, ""
	}
	return l.last, l.why
}

// do sends one request over the WebSocket, opening one when there is none. An *unreachable error
// means it could not be had: the request may go as an HTTP request instead.
func (l *wsLink) do(ctx context.Context, method, pathQuery string, plain []byte) (int, []byte, error) {
	c := l.c
	var body []byte
	if plain != nil {
		body = c.keys.Seal(pathQuery, plain)
	}
	for try := 0; ; try++ {
		w, err := l.get(ctx)
		if err != nil {
			return 0, nil, err
		}
		head := proto.WSRequest{Method: method, Path: pathQuery, Time: time.Now().Unix(), Nonce: seal.Nonce()}
		head.Sign = c.keys.Sign(method, pathQuery, head.Time, head.Nonce, body)
		if body != nil {
			head.Type = seal.ContentType
		}
		ans, raw, err := w.roundTrip(ctx, &head, body)
		var u *unreachable
		if errors.As(err, &u) && try == 0 && ctx.Err() == nil {
			continue // the connection broke or was replaced: once more, on a new one
		}
		if err != nil {
			return 0, nil, err
		}
		return c.answer(ans.Status, ans.Type, raw, pathQuery, head.Nonce)
	}
}

// get is the connection to use, opened now when there is none (or the relay changed). One that went
// directly while the relay failed is replaced by one through the relay, in the background, once the
// relay is tried again.
func (l *wsLink) get(ctx context.Context) (*wsConn, error) {
	l.openMu.Lock()
	defer l.openMu.Unlock()
	gen, routes := l.c.link.generation(), l.c.link.routes()
	l.mu.Lock()
	cur := l.conn
	gone := cur != nil && (!cur.alive() || cur.gen != gen)
	if gone {
		l.conn = nil
	}
	l.mu.Unlock()
	if gone {
		cur.retire()
		cur = nil
	}
	if cur != nil {
		if cur.route == pathDirect && routes[0] == pathRelay {
			l.backToRelay(gen)
		}
		return cur, nil
	}
	var errs []error
	for _, route := range routes {
		w, err := l.open(ctx, route, gen)
		if err == nil {
			l.swap(w)
			return w, nil
		}
		var u *unreachable
		var nw *noWebSocket
		if !errors.As(err, &u) || errors.As(err, &nw) || ctx.Err() != nil {
			return nil, err // the panel answered (or the caller gave up): another way changes nothing
		}
		if len(routes) == 1 {
			return nil, err
		}
		errs = append(errs, routeErr(route, u.err))
	}
	return nil, &unreachable{errors.Join(errs...)}
}

// backToRelay opens a connection through the relay to replace the direct one, without holding up
// requests meanwhile.
func (l *wsLink) backToRelay(gen int) {
	l.mu.Lock()
	if l.switching {
		l.mu.Unlock()
		return
	}
	l.switching = true
	l.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), wsOpenWait)
		defer cancel()
		if w, err := l.open(ctx, pathRelay, gen); err == nil {
			l.swap(w)
		}
		l.mu.Lock()
		l.switching = false
		l.mu.Unlock()
	}()
}

// swap makes w the connection to use; the one before finishes what it carries, then closes.
func (l *wsLink) swap(w *wsConn) {
	l.mu.Lock()
	old := l.conn
	stale := l.off || w.gen != l.c.link.generation()
	if !stale {
		l.conn = w
	}
	l.mu.Unlock()
	if stale {
		w.fail(errRetired)
		return
	}
	if old != nil && old != w {
		old.retire()
	}
}

// open connects one way, does TLS when the panel's address is https, and asks for the WebSocket with
// a signed request.
func (l *wsLink) open(ctx context.Context, route string, gen int) (*wsConn, error) {
	c := l.c
	ctx, cancel := context.WithTimeout(ctx, wsOpenWait)
	defer cancel()
	nc, err := c.link.dial(ctx, route)
	if err != nil {
		if ctx.Err() == nil {
			c.link.failed(route, err)
		}
		return nil, &unreachable{err}
	}
	conn, err := l.handshake(ctx, nc)
	if err != nil {
		nc.Close()
		var u *unreachable
		var nw *noWebSocket
		if errors.As(err, &u) && !errors.As(err, &nw) {
			if ctx.Err() == nil {
				c.link.failed(route, u.err)
			}
		} else {
			c.link.worked(route) // the panel answered: this way works
		}
		return nil, err
	}
	c.link.worked(route)
	conn.SetIdle(wsQuiet)
	w := &wsConn{ws: conn, route: route, gen: gen, waiting: map[uint32]chan wsResult{}, done: make(chan struct{})}
	go w.read()
	go w.ping()
	return w, nil
}

func (l *wsLink) handshake(ctx context.Context, nc net.Conn) (*ws.Conn, error) {
	c := l.c
	if dl, ok := ctx.Deadline(); ok {
		_ = nc.SetDeadline(dl)
		defer nc.SetDeadline(time.Time{})
	}
	if c.link.secure {
		tc := tls.Client(nc, c.link.tlsFor())
		if err := tc.HandshakeContext(ctx); err != nil {
			return nil, &unreachable{err}
		}
		nc = tc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.panel+proto.WSPath, nil)
	if err != nil {
		return nil, err
	}
	ts, nonce := time.Now().Unix(), seal.Nonce()
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set(seal.HeaderServer, strconv.FormatInt(c.serverID, 10))
	req.Header.Set(seal.HeaderTime, strconv.FormatInt(ts, 10))
	req.Header.Set(seal.HeaderNonce, nonce)
	req.Header.Set(seal.HeaderSign, c.keys.Sign(http.MethodGet, proto.WSPath, ts, nonce, nil))
	conn, resp, err := ws.Client(nc, req, wsMaxMessage)
	switch {
	case err == nil:
		return conn, nil
	case resp == nil:
		return nil, &unreachable{err}
	case resp.StatusCode == http.StatusUnauthorized: // the panel refused this agent: HTTP would be refused too
		b, _ := io.ReadAll(resp.Body)
		return nil, &statusError{resp.StatusCode, strings.TrimSpace(string(b))}
	}
	return nil, &unreachable{&noWebSocket{resp.StatusCode}}
}

// wsConn is one open WebSocket and the requests waiting for their answers on it.
type wsConn struct {
	ws    *ws.Conn
	route string
	gen   int

	mu      sync.Mutex
	next    uint32
	waiting map[uint32]chan wsResult
	err     error // why it ended; nil while open
	retired bool  // a newer connection took over: it closes once its requests are answered
	done    chan struct{}
}

type wsResult struct {
	ans  proto.WSAnswer
	body []byte
	err  error
}

func (w *wsConn) alive() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err == nil && !w.retired
}

// roundTrip sends one request and waits for its answer.
func (w *wsConn) roundTrip(ctx context.Context, head *proto.WSRequest, body []byte) (proto.WSAnswer, []byte, error) {
	ch := make(chan wsResult, 1)
	w.mu.Lock()
	if w.err != nil {
		err := w.err
		w.mu.Unlock()
		return proto.WSAnswer{}, nil, &unreachable{err}
	}
	w.next++
	head.ID = w.next
	w.waiting[head.ID] = ch
	w.mu.Unlock()
	defer w.forget(head.ID)
	msg, err := proto.WSPack(head, body)
	if err != nil {
		return proto.WSAnswer{}, nil, err
	}
	if err := w.ws.Write(msg); err != nil {
		w.fail(err)
		return proto.WSAnswer{}, nil, &unreachable{err}
	}
	t := time.NewTimer(wsAnswerWait)
	defer t.Stop()
	select {
	case r := <-ch:
		if r.err != nil {
			return proto.WSAnswer{}, nil, &unreachable{r.err}
		}
		return r.ans, r.body, nil
	case <-ctx.Done():
		return proto.WSAnswer{}, nil, ctx.Err()
	case <-t.C:
		err := errors.New("the panel did not answer on the WebSocket")
		w.fail(err)
		return proto.WSAnswer{}, nil, &unreachable{err}
	}
}

// forget drops a request that is answered or given up; a retired connection closes after its last.
func (w *wsConn) forget(id uint32) {
	w.mu.Lock()
	delete(w.waiting, id)
	last := w.retired && len(w.waiting) == 0
	w.mu.Unlock()
	if last {
		w.fail(errRetired)
	}
}

// read hands each answer to the request waiting for it.
func (w *wsConn) read() {
	for {
		msg, err := w.ws.Read()
		if err != nil {
			w.fail(err)
			return
		}
		var ans proto.WSAnswer
		body, err := proto.WSUnpack(msg, &ans)
		if err != nil {
			w.fail(err)
			return
		}
		w.mu.Lock()
		ch := w.waiting[ans.ID]
		delete(w.waiting, ans.ID)
		w.mu.Unlock()
		if ch != nil {
			ch <- wsResult{ans: ans, body: body}
		}
	}
}

// ping keeps the connection and what lies between (NAT, the relay) awake, and finds a dead one.
func (w *wsConn) ping() {
	t := time.NewTicker(wsPing)
	defer t.Stop()
	for {
		select {
		case <-w.done:
			return
		case <-t.C:
			if err := w.ws.Ping(); err != nil {
				w.fail(err)
				return
			}
		}
	}
}

// retire has the connection finish what it carries and then close; nothing new goes on it.
func (w *wsConn) retire() {
	w.mu.Lock()
	w.retired = true
	idle := len(w.waiting) == 0
	w.mu.Unlock()
	if idle {
		w.fail(errRetired)
		return
	}
	time.AfterFunc(wsAnswerWait, func() { w.fail(errRetired) })
}

// fail ends the connection: every request still waiting gets err.
func (w *wsConn) fail(err error) {
	w.mu.Lock()
	if w.err != nil {
		w.mu.Unlock()
		return
	}
	w.err = err
	waiting := w.waiting
	w.waiting = map[uint32]chan wsResult{}
	w.mu.Unlock()
	close(w.done)
	w.ws.Close()
	for _, ch := range waiting {
		ch <- wsResult{err: err}
	}
}
