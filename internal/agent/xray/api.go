package xray

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"meridian/internal/proto"
)

// API is a minimal gRPC client for Xray's management services. It speaks gRPC over cleartext
// HTTP/2 to 127.0.0.1 and hand-encodes the few small messages it needs, so the agent does not
// depend on (or have to match the version of) xray-core.
type API struct {
	addr   string
	client *http.Client
}

func NewAPI(addr string) *API {
	var protos http.Protocols
	protos.SetUnencryptedHTTP2(true) // gRPC over cleartext HTTP/2 with prior knowledge
	tr := &http.Transport{Protocols: &protos, MaxIdleConns: 4, IdleConnTimeout: 90 * time.Second}
	return &API{addr: addr, client: &http.Client{Transport: tr, Timeout: 10 * time.Second}}
}

// call performs one unary gRPC call and returns the response message bytes.
func (a *API) call(ctx context.Context, method string, req []byte) ([]byte, error) {
	var frame bytes.Buffer
	frame.WriteByte(0)
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(req)))
	frame.Write(l[:])
	frame.Write(req)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+a.addr+method, &frame)
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/grpc")
	hr.Header.Set("TE", "trailers")
	resp, err := a.client.Do(hr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	status := resp.Trailer.Get("Grpc-Status")
	if status == "" {
		status = resp.Header.Get("Grpc-Status")
	}
	if status != "" && status != "0" {
		msg, _ := url.PathUnescape(resp.Trailer.Get("Grpc-Message"))
		if msg == "" {
			msg, _ = url.PathUnescape(resp.Header.Get("Grpc-Message"))
		}
		return nil, &RPCError{Code: status, Msg: msg}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("xray api: HTTP %d", resp.StatusCode)
	}
	if len(body) < 5 {
		return nil, nil // empty message
	}
	n := binary.BigEndian.Uint32(body[1:5])
	if int(n) > len(body)-5 {
		return nil, errors.New("xray api: short response")
	}
	return body[5 : 5+n], nil
}

type RPCError struct {
	Code string
	Msg  string
}

func (e *RPCError) Error() string { return "xray api: " + e.Msg + " (code " + e.Code + ")" }

// IsExists reports an "already exists" failure; IsNotFound a "not found" one.
func IsExists(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "already exist")
}
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "not found") || strings.Contains(s, "not exist")
}

// ---------------------------------------------------------------- encoding helpers

func str(b []byte, field protowire.Number, s string) []byte {
	if s == "" {
		return b
	}
	b = protowire.AppendTag(b, field, protowire.BytesType)
	return protowire.AppendString(b, s)
}

func msg(b []byte, field protowire.Number, m []byte) []byte {
	b = protowire.AppendTag(b, field, protowire.BytesType)
	return protowire.AppendBytes(b, m)
}

func boolean(b []byte, field protowire.Number, v bool) []byte {
	if !v {
		return b
	}
	b = protowire.AppendTag(b, field, protowire.VarintType)
	return protowire.AppendVarint(b, 1)
}

func typed(name string, value []byte) []byte {
	var b []byte
	b = str(b, 1, name)
	b = msg(b, 2, value)
	return b
}

// ssCipher maps classic Shadowsocks methods to Xray's CipherType enum (proxy/shadowsocks/config.proto).
var ssCipher = map[string]uint64{"aes-128-gcm": 5, "aes-256-gcm": 6, "chacha20-ietf-poly1305": 7,
	"chacha20-poly1305": 7, "xchacha20-ietf-poly1305": 8, "xchacha20-poly1305": 8}

// account encodes a user's account as the TypedMessage its inbound expects.
func account(a proto.XrayAccount) ([]byte, error) {
	var v []byte
	switch a.Kind {
	case "vless":
		v = str(v, 1, a.ID)
		v = str(v, 2, a.Flow)
		return typed("xray.proxy.vless.Account", v), nil
	case "vmess":
		v = str(v, 1, a.ID)
		return typed("xray.proxy.vmess.Account", v), nil
	case "trojan":
		v = str(v, 1, a.Password)
		return typed("xray.proxy.trojan.Account", v), nil
	case "ss2022":
		v = str(v, 1, a.Password)
		return typed("xray.proxy.shadowsocks_2022.Account", v), nil
	case "shadowsocks":
		ct, ok := ssCipher[a.Method]
		if !ok {
			return nil, fmt.Errorf("unsupported Shadowsocks method %q", a.Method)
		}
		v = str(v, 1, a.Password)
		v = protowire.AppendTag(v, 2, protowire.VarintType)
		v = protowire.AppendVarint(v, ct)
		return typed("xray.proxy.shadowsocks.Account", v), nil
	case "hysteria":
		v = str(v, 1, a.Password)
		return typed("xray.proxy.hysteria.account.Account", v), nil
	}
	return nil, fmt.Errorf("unsupported account kind %q", a.Kind)
}

// fields walks a message and calls fn for each field.
func fields(b []byte, fn func(num protowire.Number, typ protowire.Type, v []byte, n uint64)) error {
	for len(b) > 0 {
		num, typ, l := protowire.ConsumeTag(b)
		if l < 0 {
			return protowire.ParseError(l)
		}
		b = b[l:]
		switch typ {
		case protowire.VarintType:
			x, l := protowire.ConsumeVarint(b)
			if l < 0 {
				return protowire.ParseError(l)
			}
			fn(num, typ, nil, x)
			b = b[l:]
		case protowire.BytesType:
			x, l := protowire.ConsumeBytes(b)
			if l < 0 {
				return protowire.ParseError(l)
			}
			fn(num, typ, x, 0)
			b = b[l:]
		default:
			l := protowire.ConsumeFieldValue(num, typ, b)
			if l < 0 {
				return protowire.ParseError(l)
			}
			b = b[l:]
		}
	}
	return nil
}

// ---------------------------------------------------------------- HandlerService

const handler = "/xray.app.proxyman.command.HandlerService/"

// AddUser adds a client to a running inbound.
func (a *API) AddUser(ctx context.Context, tag string, c proto.XrayClient) error {
	acc, err := account(c.Account)
	if err != nil {
		return err
	}
	var user []byte
	user = str(user, 2, c.Email)
	user = msg(user, 3, acc)
	var op []byte
	op = msg(op, 1, user)
	var req []byte
	req = str(req, 1, tag)
	req = msg(req, 2, typed("xray.app.proxyman.command.AddUserOperation", op))
	_, err = a.call(ctx, handler+"AlterInbound", req)
	return err
}

// RemoveUser removes a client by email from a running inbound.
func (a *API) RemoveUser(ctx context.Context, tag, email string) error {
	var op []byte
	op = str(op, 1, email)
	var req []byte
	req = str(req, 1, tag)
	req = msg(req, 2, typed("xray.app.proxyman.command.RemoveUserOperation", op))
	_, err := a.call(ctx, handler+"AlterInbound", req)
	return err
}

// RemoveInbound removes an inbound by tag.
func (a *API) RemoveInbound(ctx context.Context, tag string) error {
	_, err := a.call(ctx, handler+"RemoveInbound", str(nil, 1, tag))
	return err
}

// RemoveOutbound removes an outbound by tag.
func (a *API) RemoveOutbound(ctx context.Context, tag string) error {
	_, err := a.call(ctx, handler+"RemoveOutbound", str(nil, 1, tag))
	return err
}

// InboundTags lists the tags of running inbounds.
func (a *API) InboundTags(ctx context.Context) ([]string, error) {
	resp, err := a.call(ctx, handler+"ListInbounds", boolean(nil, 1, true))
	if err != nil {
		return nil, err
	}
	var tags []string
	err = fields(resp, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) {
		if num == 1 && typ == protowire.BytesType {
			_ = fields(v, func(n protowire.Number, t protowire.Type, s []byte, _ uint64) {
				if n == 1 && t == protowire.BytesType {
					tags = append(tags, string(s))
				}
			})
		}
	})
	return tags, err
}

// InboundUsers lists the emails of an inbound's users.
func (a *API) InboundUsers(ctx context.Context, tag string) ([]string, error) {
	resp, err := a.call(ctx, handler+"GetInboundUsers", str(nil, 1, tag))
	if err != nil {
		return nil, err
	}
	var emails []string
	err = fields(resp, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) {
		if num == 1 && typ == protowire.BytesType {
			_ = fields(v, func(n protowire.Number, t protowire.Type, s []byte, _ uint64) {
				if n == 2 && t == protowire.BytesType {
					emails = append(emails, string(s))
				}
			})
		}
	})
	return emails, err
}

// ---------------------------------------------------------------- StatsService

const stats = "/xray.app.stats.command.StatsService/"

type Stat struct {
	Name  string
	Value int64
}

// QueryStats returns counters matching pattern, resetting them when reset is set.
func (a *API) QueryStats(ctx context.Context, pattern string, reset bool) ([]Stat, error) {
	var req []byte
	req = str(req, 1, pattern)
	req = boolean(req, 2, reset)
	resp, err := a.call(ctx, stats+"QueryStats", req)
	if err != nil {
		return nil, err
	}
	var out []Stat
	err = fields(resp, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) {
		if num != 1 || typ != protowire.BytesType {
			return
		}
		var s Stat
		_ = fields(v, func(n protowire.Number, t protowire.Type, b []byte, x uint64) {
			switch {
			case n == 1 && t == protowire.BytesType:
				s.Name = string(b)
			case n == 2 && t == protowire.VarintType:
				s.Value = int64(x)
			}
		})
		out = append(out, s)
	})
	return out, err
}

// OnlineUsers returns the stat names of users with open connections.
func (a *API) OnlineUsers(ctx context.Context) ([]string, error) {
	resp, err := a.call(ctx, stats+"GetAllOnlineUsers", nil)
	if err != nil {
		return nil, err
	}
	var out []string
	err = fields(resp, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) {
		if num == 1 && typ == protowire.BytesType {
			out = append(out, string(v))
		}
	})
	return out, err
}

// OnlineIPs returns ip -> last seen (unix seconds) for a user.
func (a *API) OnlineIPs(ctx context.Context, email string) (map[string]int64, error) {
	name := email
	if !strings.HasPrefix(name, "user>>>") {
		name = "user>>>" + email + ">>>online"
	}
	resp, err := a.call(ctx, stats+"GetStatsOnlineIpList", str(nil, 1, name))
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	err = fields(resp, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) {
		if num != 2 || typ != protowire.BytesType {
			return
		}
		var k string
		var t int64
		_ = fields(v, func(n protowire.Number, tp protowire.Type, b []byte, x uint64) {
			switch {
			case n == 1 && tp == protowire.BytesType:
				k = string(b)
			case n == 2 && tp == protowire.VarintType:
				t = int64(x)
			}
		})
		if k != "" {
			out[k] = t
		}
	})
	return out, err
}

// ---------------------------------------------------------------- LoggerService

// RestartLogger makes Xray reopen its log files (after we rotate the access log).
func (a *API) RestartLogger(ctx context.Context) error {
	_, err := a.call(ctx, "/xray.app.log.command.LoggerService/RestartLogger", nil)
	return err
}

// Ping checks the API is reachable.
func (a *API) Ping(ctx context.Context) error {
	_, err := a.call(ctx, stats+"GetSysStats", nil)
	return err
}
