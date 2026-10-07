package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
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
// encrypted with a key derived from it, so the agent works the same over plain HTTP.
type Client struct {
	panel    string
	serverID int64
	keys     *seal.Keys
	http     *http.Client
	ua       string
}

func NewClient(panel, token, version string) (*Client, error) {
	id, secret, err := seal.ParseToken(token)
	if err != nil {
		return nil, err
	}
	keys, err := seal.Derive(secret)
	if err != nil {
		return nil, err
	}
	return &Client{panel: strings.TrimRight(panel, "/"), serverID: id, keys: keys,
		http: &http.Client{Timeout: 90 * time.Second}, ua: "meridian-agent/" + version}, nil
}

type statusError struct {
	code int
	msg  string
}

func (e *statusError) Error() string { return fmt.Sprintf("panel answered %d: %s", e.code, e.msg) }

func (c *Client) do(ctx context.Context, method, pathQuery string, plain []byte) (int, []byte, error) {
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
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	switch {
	case resp.StatusCode == http.StatusNoContent:
		return resp.StatusCode, nil, nil
	case resp.StatusCode != http.StatusOK:
		return resp.StatusCode, nil, &statusError{resp.StatusCode, strings.TrimSpace(string(raw))}
	}
	if resp.Header.Get("Content-Type") != seal.ContentType {
		return resp.StatusCode, nil, fmt.Errorf("unexpected response from panel (is %s the panel address?)", c.panel)
	}
	out, err := c.keys.Open(seal.ReplyContext(pathQuery, nonce), raw)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("cannot decrypt the panel's answer: %w", err)
	}
	return resp.StatusCode, out, nil
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
