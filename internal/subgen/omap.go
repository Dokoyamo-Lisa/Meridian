package subgen

import (
	"bytes"
	"encoding/json"

	"gopkg.in/yaml.v3"
)

// omap is a map that keeps insertion order in YAML and JSON output, so generated configs read
// top-down the way people expect ("type" first, rules last).
type omap []kv

type kv struct {
	k string
	v any
}

func (m omap) set(k string, v any) omap { return append(m, kv{k, v}) }

func (m omap) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode}
	for _, e := range m {
		var kn, vn yaml.Node
		kn.SetString(e.k)
		if err := vn.Encode(e.v); err != nil {
			return nil, err
		}
		n.Content = append(n.Content, &kn, &vn)
	}
	return n, nil
}

func (m omap) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, e := range m {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(e.k)
		b.Write(k)
		b.WriteByte(':')
		v, err := marshalJSONNoEscape(e.v)
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func marshalJSONNoEscape(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// prettyJSON indents generated JSON without escaping & < >.
func prettyJSON(v any) ([]byte, error) {
	raw, err := marshalJSONNoEscape(v)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

func toYAML(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
