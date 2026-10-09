package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"meridian/internal/proto"
	"meridian/internal/seal"
)

// Client talks to the panel. Every request is signed with the server token and every body is
// encrypted with a key derived from it, so the agent works the same over plain HTTP - and through a
// relay (see panelLink), and on its WebSocket (see wsLink).
type Client struct {
	panel    string
	serverID int64
	keys     *seal.Keys
	link     *panelLink
	ws       *wsLink
	ua       string
}

func NewClient(panel, token, version string) (*Client, error) {
	return newClient(panel, token, version, nil, nil)
}

// newClient is NewClient with the TLS settings and direct dialler of a test.
func newClient(panel, token, version string, tlsConf *tls.Config, dial dialFunc) (*Client, error) {
	id, secret, err := seal.ParseToken(token)
	if err != nil {
		return nil, err
	}
	keys, err := seal.Derive(secret)
	if err != nil {
		return nil, err
	}
	panel = strings.TrimRight(panel, "/")
	link, err := newPanelLink(panel, tlsConf, dial)
	if err != nil {
		return nil, err
	}
	c := &Client{panel: panel, serverID: id, keys: keys, link: link, ua: "meridian-agent/" + version}
	c.ws = &wsLink{c: c}
	return c, nil
}

// follow takes from a state how to reach the panel: through which relay, if any, and whether over the
// WebSocket or HTTP requests.
func (c *Client) follow(st *proto.State) {
	c.link.setVia(st.PanelVia)
	c.ws.setOff(st.Agent.Transport == "http")
}

// linkStatus says in a report how the agent reaches the panel.
func (c *Client) linkStatus(lv *proto.Live) {
	lv.PanelPath, lv.RelayError = c.link.status()
	lv.PanelConn, lv.ConnError = c.ws.status()
}

type statusError struct {
	code int
	msg  string
}

func (e *statusError) Error() string { return fmt.Sprintf("panel answered %d: %s", e.code, e.msg) }

// unreachable is an error on the way to the panel, as opposed to an answer from it: the request
// may go another way.
type unreachable struct{ err error }

func (e *unreachable) Error() string { return e.err.Error() }
func (e *unreachable) Unwrap() error { return e.err }

// do sends one request: on the WebSocket, while one is open or can be opened, else as an HTTP request.
func (c *Client) do(ctx context.Context, method, pathQuery string, plain []byte) (int, []byte, error) {
	if c.ws.wanted() {
		code, out, err := c.ws.do(ctx, method, pathQuery, plain)
		var u *unreachable
		if !errors.As(err, &u) {
			if err == nil {
				c.ws.used(connWebSocket)
			}
			return code, out, err
		}
		if ctx.Err() != nil {
			return code, out, err
		}
		c.ws.failed(u.err) // HTTP requests for a while
	}
	code, out, err := c.httpDo(ctx, method, pathQuery, plain)
	if err == nil {
		c.ws.used(connHTTP)
	}
	return code, out, err
}

// httpDo sends one HTTP request, through the relay or directly - the other way when the first cannot
// reach the panel. Each try is signed afresh (its own nonce), so the panel never takes one for a replay.
func (c *Client) httpDo(ctx context.Context, method, pathQuery string, plain []byte) (int, []byte, error) {
	routes := c.link.routes()
	var errs []error
	for _, route := range routes {
		code, out, err := c.once(ctx, c.link.transport(route), method, pathQuery, plain)
		var u *unreachable
		if !errors.As(err, &u) {
			c.link.worked(route)
			return code, out, err
		}
		c.link.failed(route, u.err)
		if len(routes) == 1 {
			return code, out, u.err
		}
		errs = append(errs, routeErr(route, u.err))
		if ctx.Err() != nil {
			break
		}
	}
	return 0, nil, errors.Join(errs...)
}

func (c *Client) once(ctx context.Context, rt http.RoundTripper, method, pathQuery string, plain []byte) (int, []byte, error) {
	var body []byte
	if plain != nil {
		body = c.keys.Seal(pathQuery, plain)
	}
	ts := time.Now().Unix()
	nonce := seal.Nonce()
	req, err := http.NewRequestWithContext(ctx, method, c.panel+pathQuery, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set(seal.HeaderServer, strconv.FormatInt(c.serverID, 10))
	req.Header.Set(seal.HeaderTime, strconv.FormatInt(ts, 10))
	req.Header.Set(seal.HeaderNonce, nonce)
	req.Header.Set(seal.HeaderSign, c.keys.Sign(method, pathQuery, ts, nonce, body))
	if body != nil {
		req.Header.Set("Content-Type", seal.ContentType)
	}
	resp, err := (&http.Client{Transport: rt, Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, &unreachable{err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return resp.StatusCode, nil, &unreachable{err}
	}
	return c.answer(resp.StatusCode, resp.Header.Get("Content-Type"), raw, pathQuery, nonce)
}

// answer reads the panel's answer to a request - the same over HTTP and on the WebSocket.
func (c *Client) answer(code int, ctype string, raw []byte, pathQuery, nonce string) (int, []byte, error) {
	switch {
	case code == http.StatusNoContent:
		return code, nil, nil
	case code != http.StatusOK:
		return code, nil, &statusError{code, strings.TrimSpace(string(raw))}
	}
	if ctype != seal.ContentType {
		return code, nil, &unreachable{fmt.Errorf("unexpected response from panel (is %s the panel address?)", c.panel)}
	}
	out, err := c.keys.Open(seal.ReplyContext(pathQuery, nonce), raw)
	if err != nil {
		return code, nil, fmt.Errorf("cannot decrypt the panel's answer: %w", err)
	}
	return code, out, nil
}

// State waits up to wait seconds for a state newer than rev. It returns nil when nothing changed.
func (c *Client) State(ctx context.Context, rev string, wait int) (*proto.State, error) {
	q := url.Values{}
	q.Set("rev", rev)
	q.Set("wait", strconv.Itoa(wait))
	code, body, err := c.do(ctx, http.MethodGet, "/agent/v1/state?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if code == http.StatusNoContent || body == nil {
		return nil, nil
	}
	var st proto.State
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// GeoList fetches the address list of a country rule. The document must hash to want.
func (c *Client) GeoList(ctx context.Context, want string) ([]byte, error) {
	if !hexRE.MatchString(want) {
		return nil, fmt.Errorf("bad list hash %q", want)
	}
	_, body, err := c.do(ctx, http.MethodGet, "/agent/v1/geo/"+want, nil)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != want {
		return nil, errors.New("the country list does not match its checksum")
	}
	return body, nil
}

var hexRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Report sends a report and returns the acknowledged batch sequence.
func (c *Client) Report(ctx context.Context, rep *proto.Report) (int64, error) {
	plain, err := json.Marshal(rep)
	if err != nil {
		return 0, err
	}
	_, body, err := c.do(ctx, http.MethodPost, "/agent/v1/report", plain)
	if err != nil {
		return 0, err
	}
	var ack proto.ReportAck
	if err := json.Unmarshal(body, &ack); err != nil {
		return 0, err
	}
	return ack.Ack, nil
}
