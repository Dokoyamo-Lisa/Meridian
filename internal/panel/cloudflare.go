package panel

// Keeping a dynamic DNS name up to date in Cloudflare, for operators without an updater of their
// own. With a server's ddns_cloudflare on, the panel points that exact name's A and AAAA records at
// the addresses the agent reports - when they change, when the server is saved, and as a check every
// ten minutes. Records are DNS only (Cloudflare's proxy would break the protocols) with a short TTL.
// No other name is ever touched; a kind of address the server no longer has loses only that name's
// record of that kind. Every update and every new failure is an event (notification group servers).
// The token (permission Zone - DNS - Edit) is a secret: stored apart from the panel settings, set only
// from the browser, never shown, logged or written into an event.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

var (
	cloudflareAPI  = "https://api.cloudflare.com/client/v4" // tests point it at a stand-in
	cloudflareHTTP = &http.Client{Timeout: 15 * time.Second,
		// a redirect could carry the token somewhere else
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	cfTokenRE = regexp.MustCompile(`^[A-Za-z0-9_-]{30,200}$`)
	cfIDRE    = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// cfTTL is the records' time to live in seconds: the shortest Cloudflare takes, so devices follow a
// new address within a minute.
const cfTTL = 60

func init() {
	// updates and failures reach the operator with the other news about servers
	notifyGroups["servers"] = append(notifyGroups["servers"], "ddns_updated", "ddns_failed")
}

// ---------------------------------------------------------------- the token

// cfToken is the saved Cloudflare token ("" = none).
func (p *Panel) cfToken() string {
	p.dyn.mu.Lock()
	if t := p.dyn.token; t != nil {
		p.dyn.mu.Unlock()
		return *t
	}
	p.dyn.mu.Unlock()
	var c struct {
		Token string `json:"token"`
	}
	var raw string
	if p.db.QueryRow(`SELECT value FROM settings WHERE key = 'cloudflare'`).Scan(&raw) == nil {
		_ = json.Unmarshal([]byte(raw), &c)
	}
	p.dyn.mu.Lock()
	p.dyn.token = &c.Token
	p.dyn.mu.Unlock()
	return c.Token
}

func (p *Panel) saveCFToken(token string) error {
	var err error
	if token == "" {
		_, err = p.db.Exec1(`DELETE FROM settings WHERE key = 'cloudflare'`)
	} else {
		b, _ := json.Marshal(map[string]string{"token": token})
		_, err = p.db.Exec1(`INSERT INTO settings (key, value) VALUES ('cloudflare', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, string(b))
	}
	if err != nil {
		return err
	}
	p.dyn.mu.Lock()
	p.dyn.token, p.dyn.zones = &token, nil
	p.dyn.mu.Unlock()
	return nil
}

// ---------------------------------------------------------------- talking to Cloudflare

type cfClient struct{ token string }

type cfMessage struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cfZone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	at   time.Time
}

type cfRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
}

// cfError is Cloudflare refusing a request, in words an operator can act on.
type cfError struct {
	auth bool // the token was refused (or may not do this)
	msg  string
}

func (e *cfError) Error() string { return e.msg }

const cfTokenRefused = "Cloudflare refused the token - check it in Settings › Dynamic DNS: it needs the permission Zone - DNS - Edit for the name's zone"

// call runs one request against Cloudflare's API and decodes its result into out. Errors never
// carry the token.
func (c cfClient) call(ctx context.Context, method, path string, q url.Values, body, out any) error {
	u := cloudflareAPI + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return errors.New("the request to Cloudflare could not be made")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cloudflareHTTP.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("cannot reach Cloudflare: %s", c.hide(plainNetErr(err)))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var r struct {
		Success bool            `json:"success"`
		Errors  []cfMessage     `json:"errors"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("unexpected answer from Cloudflare: %s", resp.Status)
	}
	if !r.Success {
		e := cfRefusal(resp.StatusCode, r.Errors)
		e.msg = c.hide(e.msg)
		return e
	}
	if out != nil && json.Unmarshal(r.Result, out) != nil {
		return errors.New("unexpected answer from Cloudflare")
	}
	return nil
}

// hide keeps the token out of a message, whatever a server sent back.
func (c cfClient) hide(s string) string {
	if c.token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token, "…")
}

// cfRefusal words Cloudflare's errors.
func cfRefusal(status int, errs []cfMessage) *cfError {
	auth := status == http.StatusUnauthorized || status == http.StatusForbidden
	for _, e := range errs {
		switch e.Code {
		case 1000, 6003, 6111, 9106, 9109, 10000: // token missing, invalid or without permission
			auth = true
		}
	}
	if auth {
		return &cfError{auth: true, msg: cfTokenRefused}
	}
	msg := "Cloudflare refused the change"
	if len(errs) > 0 && errs[0].Message != "" {
		msg = "refused by Cloudflare: " + cleanName(errs[0].Message, 200)
	}
	return &cfError{msg: msg}
}

// zoneFor finds the Cloudflare zone a name is in: the name itself or its closest parent that is a
// zone. A token for one zone may look up only that zone, so a refusal for one candidate moves on to
// the next.
func (p *Panel) zoneFor(ctx context.Context, c cfClient, name string) (cfZone, error) {
	p.dyn.mu.Lock()
	z, ok := p.dyn.zones[name]
	p.dyn.mu.Unlock()
	if ok && time.Since(z.at) < time.Hour {
		return z, nil
	}
	labels := strings.Split(name, ".")
	allRefused := true
	for i := 0; i+1 < len(labels); i++ {
		cand := strings.Join(labels[i:], ".")
		var zs []cfZone
		if err := c.call(ctx, http.MethodGet, "/zones", url.Values{"name": {cand}}, nil, &zs); err != nil {
			var ce *cfError
			if !errors.As(err, &ce) {
				return cfZone{}, err // no answer: asking on would not help
			}
			allRefused = allRefused && ce.auth
			continue
		}
		allRefused = false
		for _, z := range zs {
			if strings.EqualFold(z.Name, cand) && cfIDRE.MatchString(z.ID) {
				z.at = time.Now()
				p.dyn.mu.Lock()
				if p.dyn.zones == nil {
					p.dyn.zones = map[string]cfZone{}
				}
				p.dyn.zones[name] = z
				p.dyn.mu.Unlock()
				return z, nil
			}
		}
	}
	if allRefused {
		return cfZone{}, &cfError{auth: true, msg: cfTokenRefused}
	}
	return cfZone{}, &cfError{msg: fmt.Sprintf("Cloudflare has no zone for %s that the token may change - add the domain to Cloudflare, or give the token Zone - DNS - Edit for its zone", name)}
}

// records lists the records of exactly name in a zone (whatever else Cloudflare's filter matched is
// left alone).
func (c cfClient) records(ctx context.Context, zone cfZone, name string) ([]cfRecord, error) {
	var all []cfRecord
	if err := c.call(ctx, http.MethodGet, "/zones/"+zone.ID+"/dns_records", url.Values{"name": {name}, "per_page": {"100"}}, nil, &all); err != nil {
		return nil, err
	}
	var out []cfRecord
	for _, r := range all {
		if strings.EqualFold(strings.TrimSuffix(r.Name, "."), name) && cfIDRE.MatchString(r.ID) {
			out = append(out, r)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- keeping a name up to date

// cfWanted are the addresses a server's name should have: its IPv4 and IPv6 as the agent reports
// them, each only where the server uses that kind ("" = no record of that kind).
func cfWanted(s *Server) (v4, v6 string) {
	if a, err := netip.ParseAddr(s.IPv4); err == nil && a.Is4() && publicAddr(a) && s.v4() {
		v4 = a.String()
	}
	if a, err := netip.ParseAddr(s.IPv6); err == nil && a.Is6() && publicAddr(a) && s.v6() {
		v6 = a.String()
	}
	return v4, v6
}

// cfWait is a reason not to touch the records yet - no failure.
type cfWait string

func (w cfWait) Error() string { return string(w) }

// cloudflareSync points server s's name at its addresses in Cloudflare and keeps how that went: an
// event for each change, and one for each new failure (the same failure again records nothing).
func (p *Panel) cloudflareSync(ctx context.Context, s *Server) {
	p.dyn.mu.Lock()
	st := p.dyn.cfFor(s.ID)
	st.pending, st.tried = false, time.Now()
	failedBefore := st.failed
	p.dyn.mu.Unlock()

	changes, err := p.cfApply(ctx, s)
	var wait cfWait
	if errors.As(err, &wait) {
		p.dyn.mu.Lock()
		st.name, st.state, st.msg, st.at = s.Address, "waiting", string(wait), now()
		p.dyn.mu.Unlock()
		return
	}
	if err != nil {
		msg := err.Error()
		p.dyn.mu.Lock()
		st.name, st.state, st.msg, st.at = s.Address, "failed", fmt.Sprintf("Cloudflare could not update %s: %s", s.Address, msg), now()
		st.failed = msg
		p.dyn.mu.Unlock()
		if msg != failedBefore {
			p.event(s.AccountID, "warn", "ddns_failed", s.ID, 0, 0, fmt.Sprintf("Cloudflare could not update %s for %s: %s", s.Address, s.Name, msg), nil)
		}
		return
	}
	v4, v6 := cfWanted(s)
	p.dyn.mu.Lock()
	st.name, st.state, st.at, st.failed = s.Address, "ok", now(), ""
	st.msg = fmt.Sprintf("%s points at %s", s.Address, strings.Join(nonEmpty(v4, v6), " and "))
	p.dyn.mu.Unlock()
	switch {
	case len(changes) > 0:
		p.event(s.AccountID, "info", "ddns_updated", s.ID, 0, 0, fmt.Sprintf("Cloudflare: %s for %s - %s", s.Address, s.Name, strings.Join(changes, "; ")), nil)
	case failedBefore != "":
		p.event(s.AccountID, "info", "ddns_updated", s.ID, 0, 0, fmt.Sprintf("Cloudflare: updating %s for %s works again - it points at the server", s.Address, s.Name), nil)
	}
}

func nonEmpty(list ...string) []string {
	var out []string
	for _, s := range list {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// cfApply makes the records of server s's name what they should be and says what it changed.
func (p *Panel) cfApply(ctx context.Context, s *Server) ([]string, error) {
	name := s.Address
	switch {
	case !s.DDNS || !s.DDNSCloudflare || !publicName(name):
		return nil, cfWait("not kept in Cloudflare")
	case s.FirstSeenAt == 0:
		return nil, cfWait("waiting for the server's agent to report its addresses")
	}
	token := p.cfToken()
	if token == "" {
		return nil, errors.New("no Cloudflare token is saved - add one in Settings › Dynamic DNS, or turn Cloudflare off for this server")
	}
	v4, v6 := cfWanted(s)
	if v4 == "" && v6 == "" { // never take every record away: the server has said nothing usable yet
		return nil, cfWait("waiting for the server's agent to report a public address")
	}
	c := cfClient{token: token}
	zone, err := p.zoneFor(ctx, c, name)
	if err != nil {
		return nil, err
	}
	recs, err := c.records(ctx, zone, name)
	if err != nil {
		p.dyn.mu.Lock()
		delete(p.dyn.zones, name) // the zone may have moved: look it up again next time
		p.dyn.mu.Unlock()
		return nil, err
	}
	if slices.ContainsFunc(recs, func(r cfRecord) bool { return r.Type == "CNAME" }) {
		return nil, fmt.Errorf("%s is an alias (CNAME record) in Cloudflare - the panel sets A and AAAA records: remove the alias, or give the server another name", name)
	}
	var changes []string
	for _, k := range []struct{ typ, want, label string }{{"A", v4, "IPv4"}, {"AAAA", v6, "IPv6"}} {
		var have []cfRecord
		for _, r := range recs {
			if r.Type == k.typ {
				have = append(have, r)
			}
		}
		done, err := c.setKind(ctx, zone, name, k.typ, k.want, k.label, have)
		changes = append(changes, done...)
		if err != nil {
			return changes, err
		}
	}
	return changes, nil
}

// setKind makes name have exactly one record of one kind, at want, DNS only - or none when want is
// empty. It says what it changed.
func (c cfClient) setKind(ctx context.Context, zone cfZone, name, typ, want, label string, have []cfRecord) ([]string, error) {
	base := "/zones/" + zone.ID + "/dns_records"
	var out []string
	if want == "" {
		for _, r := range have {
			if err := c.call(ctx, http.MethodDelete, base+"/"+r.ID, nil, nil, nil); err != nil {
				return out, err
			}
			out = append(out, fmt.Sprintf("its %s record (%s) removed: the server has no %s address", typ, r.Content, label))
		}
		return out, nil
	}
	keep := slices.IndexFunc(have, func(r cfRecord) bool {
		a, err := netip.ParseAddr(r.Content)
		return err == nil && a.Unmap().String() == want
	})
	switch {
	case keep >= 0 && have[keep].Proxied:
		if err := c.call(ctx, http.MethodPatch, base+"/"+have[keep].ID, nil, map[string]any{"proxied": false, "ttl": cfTTL}, nil); err != nil {
			return out, err
		}
		out = append(out, fmt.Sprintf("its %s record is DNS only now (Cloudflare's proxy breaks the protocols)", typ))
	case keep < 0 && len(have) > 0:
		keep = 0
		if err := c.call(ctx, http.MethodPatch, base+"/"+have[0].ID, nil, map[string]any{"content": want, "proxied": false, "ttl": cfTTL}, nil); err != nil {
			return out, err
		}
		out = append(out, fmt.Sprintf("%s record %s (was %s)", typ, want, have[0].Content))
	case keep < 0:
		if err := c.call(ctx, http.MethodPost, base, nil, map[string]any{"type": typ, "name": name, "content": want, "ttl": cfTTL,
			"proxied": false, "comment": "Kept up to date by Meridian"}, nil); err != nil {
			return out, err
		}
		out = append(out, fmt.Sprintf("%s record %s added", typ, want))
	}
	for i, r := range have {
		if i == keep {
			continue
		}
		if err := c.call(ctx, http.MethodDelete, base+"/"+r.ID, nil, nil, nil); err != nil {
			return out, err
		}
		out = append(out, fmt.Sprintf("a second %s record (%s) removed", typ, r.Content))
	}
	return out, nil
}

// ---------------------------------------------------------------- API

type cloudflareView struct {
	TokenSet bool     `json:"token_set" doc:"A Cloudflare API token is saved. It is never shown again"`
	Servers  []string `json:"servers" doc:"The servers whose name the panel keeps up to date in Cloudflare, as 'server (name)'"`
}

type cloudflareInput struct {
	Token *string `json:"token" doc:"An API token from Cloudflare with the permission Zone - DNS - Edit for the zones of your servers' names (My Profile › API Tokens › Create Token › the template Edit zone DNS); empty removes it"`
}

type cloudflareTestInput struct {
	Token string `json:"token" doc:"A token to try before saving it; empty = the saved one"`
	Name  string `json:"name" doc:"A name to check the token can change, e.g. home.example.com; empty = the names of the servers that use Cloudflare"`
}

type cloudflareTest struct {
	OK      bool         `json:"ok" doc:"The token works, for every name checked"`
	Message string       `json:"message" doc:"What the test found, in plain words"`
	Names   []cfNameTest `json:"names" doc:"Each name checked. Nothing is changed by a test"`
}

type cfNameTest struct {
	Name    string   `json:"name"`
	Zone    string   `json:"zone,omitempty" doc:"The Cloudflare zone it is in"`
	Records []string `json:"records" doc:"Its A and AAAA records now, e.g. A 203.0.113.9"`
	Error   string   `json:"error,omitempty" doc:"Why the token cannot change it, and what to do"`
}

func (p *Panel) cloudflareViewOf(ctx context.Context, acct int64) cloudflareView {
	v := cloudflareView{TokenSet: p.cfToken() != "", Servers: []string{}}
	if servers, err := p.serversOf(ctx, acct); err == nil {
		for _, s := range servers {
			if s.DDNS && s.DDNSCloudflare {
				v.Servers = append(v.Servers, fmt.Sprintf("%s (%s)", s.Name, s.Address))
			}
		}
	}
	return v
}

func (p *Panel) apiCloudflare(w http.ResponseWriter, r *http.Request, a *Account) error {
	writeJSON(w, http.StatusOK, p.cloudflareViewOf(r.Context(), scopeAccount(r, a)))
	return nil
}

func (p *Panel) apiPutCloudflare(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in cloudflareInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Token == nil {
		return errStatus(http.StatusBadRequest, "send the token (an empty one removes the saved token)")
	}
	t := strings.TrimSpace(*in.Token)
	if t != "" && !cfTokenRE.MatchString(t) {
		return errStatus(http.StatusBadRequest, "that does not look like a Cloudflare API token - copy the token Cloudflare shows once when it is created (not the Global API Key)")
	}
	if err := p.saveCFToken(t); err != nil {
		return err
	}
	if t == "" {
		p.event(a.ID, "info", "settings", 0, 0, a.ID, "The Cloudflare API token was removed: names kept there are no longer updated", nil)
	} else {
		p.event(a.ID, "info", "settings", 0, 0, a.ID, "A Cloudflare API token was saved for dynamic DNS", nil)
		if servers, err := p.serversOf(r.Context(), 0); err == nil {
			for _, s := range servers {
				if s.DDNS && s.DDNSCloudflare {
					p.dyn.syncSoon(s.ID)
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, p.cloudflareViewOf(r.Context(), scopeAccount(r, a)))
	return nil
}

// apiTestCloudflare checks a token without changing anything: that Cloudflare takes it and, for each
// name, which zone it is in and what records it has now.
func (p *Panel) apiTestCloudflare(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in cloudflareTestInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	token := strings.TrimSpace(in.Token)
	if token == "" {
		token = p.cfToken()
	}
	if token == "" {
		return errStatus(http.StatusBadRequest, "paste a Cloudflare API token first")
	}
	if !cfTokenRE.MatchString(token) {
		return errStatus(http.StatusBadRequest, "that does not look like a Cloudflare API token - copy the token Cloudflare shows once when it is created (not the Global API Key)")
	}
	var names []string
	if n := strings.TrimSpace(in.Name); n != "" {
		h, err := normHost(n)
		if err != nil || !publicName(h) {
			return errStatus(http.StatusBadRequest, "give a domain name to check, e.g. home.example.com")
		}
		names = append(names, h)
	} else if servers, err := p.serversOf(r.Context(), scopeAccount(r, a)); err == nil {
		for _, s := range servers {
			if s.DDNS && s.DDNSCloudflare && !slices.Contains(names, s.Address) {
				names = append(names, s.Address)
			}
		}
	}
	c := cfClient{token: token}
	out := cloudflareTest{OK: true, Names: []cfNameTest{}}
	if len(names) == 0 {
		if err := c.call(r.Context(), http.MethodGet, "/user/tokens/verify", nil, nil, nil); err != nil {
			out.OK, out.Message = false, err.Error()
		} else {
			out.Message = "Cloudflare takes the token. Give a name to check it can change that name - or turn on Cloudflare for a server whose IP address changes."
		}
		writeJSON(w, http.StatusOK, out)
		return nil
	}
	for _, name := range names {
		t := cfNameTest{Name: name, Records: []string{}}
		zone, err := p.zoneFor(r.Context(), c, name)
		if err == nil {
			t.Zone = zone.Name
			var recs []cfRecord
			if recs, err = c.records(r.Context(), zone, name); err == nil {
				for _, rec := range recs {
					if rec.Type == "A" || rec.Type == "AAAA" || rec.Type == "CNAME" {
						t.Records = append(t.Records, rec.Type+" "+rec.Content)
					}
				}
			}
		}
		if err != nil {
			t.Error, out.OK = err.Error(), false
		}
		out.Names = append(out.Names, t)
	}
	if out.OK {
		out.Message = "The token can change " + strings.Join(names, ", ") + "."
	} else {
		out.Message = "The token cannot change every name - see each one."
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
