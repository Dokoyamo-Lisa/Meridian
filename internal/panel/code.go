package panel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"meridian/internal/proto"
)

// Configuration as code: what the panel generates is the base, and the operator's own document is
// merged on top - JSON for a server's Xray, YAML for a Hysteria2 protocol. Only the syntax is
// checked here: the cores decide whether the result works, and the agent reports what they refuse
// (the running configuration stays).

const maxCode = 256 << 10

// Sections the agent owns: Meridian's statistics, logs and user management depend on them.
var (
	xrayOwned = []string{"api", "stats", "log", "policy"}
	hyOwned   = []string{"auth", "trafficStats"}
	// a hand-written inbound's tag must not look like one the panel makes ("n12") or its API
	reservedTag = regexp.MustCompile(`^(n\d+|api)$`)
)

// codeError says where a document stops making sense.
func codeError(src string, offset int64, what string) error {
	line, col := 1, 1
	for i, r := range src {
		if int64(i) >= offset {
			break
		}
		if r == '\n' {
			line, col = line+1, 1
		} else {
			col++
		}
	}
	return errStatus(http.StatusBadRequest, fmt.Sprintf("line %d, column %d: %s", line, col, what))
}

// parseJSONC reads a JSON object that may have comments ("//", "/* */") and trailing commas -
// what Xray itself accepts and what its examples look like. Comments become spaces, so positions in
// error messages are those of the original text.
func parseJSONC(src string) (map[string]any, error) {
	if strings.TrimSpace(src) == "" {
		return nil, nil
	}
	if len(src) > maxCode {
		return nil, errStatus(http.StatusBadRequest, "the configuration is too long (256 KB at most)")
	}
	b := []byte(src)
	inStr, esc := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case inStr:
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for ; i < len(b) && b[i] != '\n'; i++ {
				b[i] = ' '
			}
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			j := bytes.Index(b[i+2:], []byte("*/"))
			if j < 0 {
				return nil, codeError(src, int64(i), "a /* comment is not closed")
			}
			for k := i; k < i+2+j+2; k++ {
				if b[k] != '\n' {
					b[k] = ' '
				}
			}
			i += 2 + j + 1
		case c == ',': // a comma right before } or ] is dropped
			k := i + 1
			for k < len(b) && (b[k] == ' ' || b[k] == '\t' || b[k] == '\r' || b[k] == '\n') {
				k++
			}
			if k < len(b) && (b[k] == '}' || b[k] == ']') {
				b[i] = ' '
			}
		}
	}
	var out map[string]any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		var se *json.SyntaxError
		var te *json.UnmarshalTypeError
		switch {
		case errors.As(err, &se):
			return nil, codeError(src, se.Offset-1, se.Error())
		case errors.As(err, &te):
			return nil, errStatus(http.StatusBadRequest, "the configuration must be one JSON object: { ... }")
		}
		return nil, errStatus(http.StatusBadRequest, "the configuration is not valid JSON: "+err.Error())
	}
	if dec.More() {
		return nil, errStatus(http.StatusBadRequest, "the configuration must be one JSON object: { ... } - there is more after it")
	}
	return out, nil
}

// checkXrayCode checks a server's Xray document: its syntax and that it leaves the agent's sections
// alone. It returns the text to store ("" when empty).
func checkXrayCode(src string) (string, error) {
	doc, err := parseJSONC(src)
	if err != nil || doc == nil {
		return "", err
	}
	for _, k := range xrayOwned {
		if _, ok := doc[k]; ok {
			return "", errStatus(http.StatusBadRequest, fmt.Sprintf("%q is Meridian's own (its statistics and logs depend on it) - leave it out", k))
		}
	}
	if v, ok := doc["outbounds"]; ok {
		list, ok := v.([]any)
		if !ok {
			return "", errStatus(http.StatusBadRequest, `"outbounds" must be a list: [ { "tag": "...", ... } ]`)
		}
		for i, o := range list {
			if m, ok := o.(map[string]any); !ok || fmt.Sprint(m["tag"]) == "" || m["tag"] == nil {
				return "", errStatus(http.StatusBadRequest, fmt.Sprintf("outbound %d needs a \"tag\" (routing rules point at it by tag)", i+1))
			}
		}
	}
	if v, ok := doc["inbounds"]; ok {
		list, ok := v.([]any)
		if !ok {
			return "", errStatus(http.StatusBadRequest, `"inbounds" must be a list: [ { "tag": "...", ... } ]`)
		}
		for i, o := range list {
			if m, ok := o.(map[string]any); !ok || m["tag"] == nil || fmt.Sprint(m["tag"]) == "" {
				return "", errStatus(http.StatusBadRequest, fmt.Sprintf("inbound %d needs a \"tag\": a protocol's (n12 - shown on its card) to change it, or a new one", i+1))
			}
		}
	}
	if v, ok := doc["routing"]; ok {
		rt, ok := v.(map[string]any)
		if !ok {
			return "", errStatus(http.StatusBadRequest, `"routing" must be an object: { "rules": [ ... ] }`)
		}
		if r, ok := rt["rules"]; ok {
			if _, ok := r.([]any); !ok {
				return "", errStatus(http.StatusBadRequest, `"routing.rules" must be a list`)
			}
		}
	}
	return strings.TrimSpace(src), nil
}

// mergePatch applies an RFC 7386 merge patch: objects merge key by key, null removes a key, anything
// else replaces.
func mergePatch(base, patch any) any {
	pm, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	bm, ok := base.(map[string]any)
	if !ok {
		bm = map[string]any{}
	}
	out := make(map[string]any, len(bm)+len(pm))
	for k, v := range bm {
		out[k] = v
	}
	for k, v := range pm {
		if v == nil {
			delete(out, k)
			continue
		}
		out[k] = mergePatch(out[k], v)
	}
	return out
}

// mergeXray puts a server's Xray document on top of the generated configuration: outbounds added or
// replacing the one with the same tag, routing rules before the panel's, inbounds merged into the
// protocol with that tag or added as the operator's own, other sections merged.
func mergeXray(base json.RawMessage, inbounds []proto.XrayInbound, src string) (json.RawMessage, []proto.XrayInbound, error) {
	doc, err := parseJSONC(src)
	if err != nil || doc == nil {
		return base, inbounds, err
	}
	var b map[string]any
	dec := json.NewDecoder(bytes.NewReader(base))
	dec.UseNumber()
	if err := dec.Decode(&b); err != nil {
		return base, inbounds, err
	}
	for k, v := range doc {
		switch k {
		case "api", "stats", "log", "policy":
			continue
		case "outbounds":
			obs, _ := b["outbounds"].([]any)
			for _, o := range v.([]any) {
				m, _ := o.(map[string]any)
				tag := fmt.Sprint(m["tag"])
				replaced := false
				for i, old := range obs {
					if om, ok := old.(map[string]any); ok && fmt.Sprint(om["tag"]) == tag {
						obs[i], replaced = m, true
					}
				}
				if !replaced {
					obs = append(obs, m)
				}
			}
			b["outbounds"] = obs
		case "routing":
			rt, _ := b["routing"].(map[string]any)
			if rt == nil {
				rt = map[string]any{}
			}
			mine, _ := v.(map[string]any)
			for k2, v2 := range mine {
				switch k2 {
				case "rules": // the operator's rules come first
					old, _ := rt["rules"].([]any)
					rt["rules"] = append(append([]any{}, v2.([]any)...), old...)
				case "balancers":
					old, _ := rt["balancers"].([]any)
					if add, ok := v2.([]any); ok {
						rt["balancers"] = append(old, add...)
					}
				default:
					rt[k2] = mergePatch(rt[k2], v2)
				}
			}
			b["routing"] = rt
		case "inbounds":
			for _, o := range v.([]any) {
				m, _ := o.(map[string]any)
				tag := fmt.Sprint(m["tag"])
				found := false
				for i := range inbounds {
					if inbounds[i].Tag != tag {
						continue
					}
					found = true
					var cur map[string]any
					d := json.NewDecoder(bytes.NewReader(inbounds[i].Config))
					d.UseNumber()
					if d.Decode(&cur) != nil {
						break
					}
					merged, _ := json.Marshal(mergePatch(cur, m))
					inbounds[i].Config = merged
				}
				if !found && !reservedTag.MatchString(tag) { // the operator's own inbound, with its own users
					raw, _ := json.Marshal(m)
					inbounds = append(inbounds, proto.XrayInbound{Tag: tag, Config: raw})
				}
			}
		default:
			b[k] = mergePatch(b[k], v)
		}
	}
	out, err := json.Marshal(b)
	return out, inbounds, err
}

// checkHyCode checks a Hysteria2 protocol's YAML document and returns it as JSON for the agent.
func checkHyCode(src string) (string, json.RawMessage, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return "", nil, nil
	}
	if len(src) > maxCode {
		return "", nil, errStatus(http.StatusBadRequest, "the configuration is too long (256 KB at most)")
	}
	var doc any
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		return "", nil, errStatus(http.StatusBadRequest, "the YAML cannot be read: "+strings.TrimPrefix(err.Error(), "yaml: "))
	}
	m, ok := jsonable(doc).(map[string]any)
	if !ok {
		return "", nil, errStatus(http.StatusBadRequest, "the configuration must be a YAML mapping, e.g.\noutbounds:\n  - name: ...")
	}
	for _, k := range hyOwned {
		if _, ok := m[k]; ok {
			return "", nil, errStatus(http.StatusBadRequest, fmt.Sprintf("%q is Meridian's own (users and traffic counting depend on it) - leave it out", k))
		}
	}
	if acl, ok := m["acl"].(map[string]any); ok {
		if _, ok := acl["file"]; ok {
			return "", nil, errStatus(http.StatusBadRequest, `"acl.file" cannot be used - write the rules under acl.inline (the blocks of private addresses always come first)`)
		}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return "", nil, errStatus(http.StatusBadRequest, "the YAML has values JSON cannot hold: "+err.Error())
	}
	return src, raw, nil
}

// jsonable turns what YAML decodes (maps with any keys) into what JSON can hold.
func jsonable(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = jsonable(x)
		}
		return t
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[fmt.Sprint(k)] = jsonable(x)
		}
		return out
	case []any:
		for i, x := range t {
			t[i] = jsonable(x)
		}
	}
	return v
}

// nodeCode checks a protocol's own configuration: JSON for an Xray protocol (checkXrayNodeCode),
// YAML for Hysteria2. WireGuard runs in the kernel and has none.
func nodeCode(kind string, in *string) (string, error) {
	if in == nil || strings.TrimSpace(*in) == "" {
		return "", nil
	}
	switch kind {
	case "hysteria2":
		code, _, err := checkHyCode(*in)
		return code, err
	case "wireguard":
		return "", errStatus(http.StatusBadRequest, "WireGuard runs in the kernel and takes no configuration code")
	}
	return checkXrayNodeCode(*in)
}

// Outbound tags a protocol's own settings may not take: the panel's, everyone's.
var reservedOutbound = regexp.MustCompile(`^(direct|block|direct-ipver|api|pass-n\d+|bind-n\d+)$`)

// checkXrayNodeCode checks an Xray protocol's own settings: fields merged into its inbound
// (sniffing, streamSettings.sockopt, fallbacks, ...), plus "outbounds" (added, each with its own
// tag) and "rules" (routing for this protocol's traffic only). The tag, the port and the users stay
// the panel's.
func checkXrayNodeCode(src string) (string, error) {
	doc, err := parseJSONC(src)
	if err != nil || doc == nil {
		return "", err
	}
	if _, ok := doc["tag"]; ok {
		return "", errStatus(http.StatusBadRequest, `"tag" is the panel's (other settings refer to the protocol by it) - leave it out`)
	}
	if _, ok := doc["port"]; ok {
		return "", errStatus(http.StatusBadRequest, `"port" is set with the protocol's Port field - leave it out`)
	}
	if st, ok := doc["settings"].(map[string]any); ok {
		for _, k := range []string{"clients", "accounts"} {
			if _, ok := st[k]; ok {
				return "", errStatus(http.StatusBadRequest, fmt.Sprintf(`"settings.%s" are the protocol's users - the panel manages them: add users in Users`, k))
			}
		}
	}
	if v, ok := doc["outbounds"]; ok {
		list, ok := v.([]any)
		if !ok {
			return "", errStatus(http.StatusBadRequest, `"outbounds" must be a list: [ { "tag": "...", ... } ]`)
		}
		for i, o := range list {
			m, ok := o.(map[string]any)
			if !ok || m["tag"] == nil || fmt.Sprint(m["tag"]) == "" {
				return "", errStatus(http.StatusBadRequest, fmt.Sprintf("outbound %d needs a \"tag\" (its rules point at it by tag)", i+1))
			}
			if reservedOutbound.MatchString(fmt.Sprint(m["tag"])) {
				return "", errStatus(http.StatusBadRequest, fmt.Sprintf("the outbound tag %q is the panel's - choose another one (or change it in the server's configuration code)", m["tag"]))
			}
		}
	}
	if v, ok := doc["rules"]; ok {
		list, ok := v.([]any)
		if !ok {
			return "", errStatus(http.StatusBadRequest, `"rules" must be a list: [ { "domain": [...], "outboundTag": "..." } ]`)
		}
		for i, r := range list {
			if _, ok := r.(map[string]any); !ok {
				return "", errStatus(http.StatusBadRequest, fmt.Sprintf("rule %d must be an object", i+1))
			}
		}
	}
	return strings.TrimSpace(src), nil
}

// nodeXray splits an Xray protocol's own settings into what is merged into its inbound, its
// outbounds, and its routing rules - each limited to the protocol's own traffic.
func nodeXray(n *Node) (patch map[string]any, outbounds, rules []map[string]any, err error) {
	doc, err := parseJSONC(n.Code)
	if err != nil || doc == nil {
		return nil, nil, nil, err
	}
	patch = map[string]any{}
	for k, v := range doc {
		list, _ := v.([]any) // checked when saved; anything else is skipped here
		switch k {
		case "outbounds":
			for _, o := range list {
				if m, ok := o.(map[string]any); ok {
					if tag, _ := m["tag"].(string); tag != "" && !reservedOutbound.MatchString(tag) {
						outbounds = append(outbounds, m)
					}
				}
			}
		case "rules":
			for i, r := range list {
				if m, ok := r.(map[string]any); ok {
					rules = append(rules, mergePatch(m, map[string]any{"inboundTag": []any{proto.InboundTag(n.ID)},
						"ruleTag": fmt.Sprintf("n%d-code-%d", n.ID, i+1)}).(map[string]any))
				}
			}
		case "tag", "port":
		default:
			patch[k] = v
		}
	}
	return patch, outbounds, rules, nil
}

// withNodeCode merges an Xray protocol's own settings into its rendered inbound.
func withNodeCode(in proto.XrayInbound, patch map[string]any) (proto.XrayInbound, error) {
	if len(patch) == 0 {
		return in, nil
	}
	var cur map[string]any
	d := json.NewDecoder(bytes.NewReader(in.Config))
	d.UseNumber()
	if err := d.Decode(&cur); err != nil {
		return in, err
	}
	merged := mergePatch(cur, patch).(map[string]any)
	merged["tag"], merged["port"] = cur["tag"], cur["port"] // the panel's, whatever the patch says
	b, err := json.Marshal(merged)
	if err != nil {
		return in, err
	}
	in.Config = b
	return in, nil
}

// apiServerConfig shows a server's Xray configuration as the agent gets it: generated, with the
// operator's code merged in, users left out (they are managed live).
func (p *Panel) apiServerConfig(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if _, err := p.ownServer(r.Context(), a, id); err != nil {
		return err
	}
	if !canWrite(r) { // it holds keys: passes, and whatever the operator's code carries
		return errStatus(http.StatusForbidden, "this needs a full-access token: the configuration holds keys")
	}
	st, err := p.compileServer(r.Context(), id)
	if err != nil {
		return err
	}
	out := configView{Xray: map[string]any{}}
	if st.Xray != nil {
		d := json.NewDecoder(bytes.NewReader(st.Xray.Base))
		d.UseNumber()
		_ = d.Decode(&out.Xray)
		ins := []any{}
		for _, in := range st.Xray.Inbounds {
			var obj map[string]any
			d := json.NewDecoder(bytes.NewReader(in.Config))
			d.UseNumber()
			if d.Decode(&obj) != nil {
				continue
			}
			if len(in.Clients) > 0 {
				obj["settings"] = mergePatch(obj["settings"], map[string]any{"clients": fmt.Sprintf("%d users, managed by Meridian", len(in.Clients))})
			}
			ins = append(ins, obj)
		}
		out.Xray["inbounds"] = ins
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type configView struct {
	Xray map[string]any `json:"xray" doc:"The Xray configuration this server gets, with your code merged in (users are managed live and left out; the agent adds its api, stats, log and policy)"`
}

// redactCode hides the operator's configuration code from read-only tokens: it may hold keys.
func redactCode(r *http.Request, v *serverView) {
	if canWrite(r) || v == nil {
		return
	}
	cp := *v.Server
	cp.XrayCode = ""
	v.Server = &cp
	for i := range v.Nodes {
		n := *v.Nodes[i].Node
		n.Code = ""
		v.Nodes[i].Node = &n
	}
}
