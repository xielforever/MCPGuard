// Package controlled implements the local controlled-tool profile. It never
// accepts native commands, endpoints, credentials or authorization from callers.
package controlled

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"gopkg.in/yaml.v3"
)

//go:embed catalog.yaml
var catalogYAML []byte

type Tool struct {
	Name        string         `yaml:"name" json:"name"`
	Description string         `yaml:"description" json:"description"`
	Resource    string         `yaml:"resourceBinding" json:"resource"`
	Effect      string         `yaml:"effectClass" json:"effect"`
	Input       map[string]any `yaml:"inputSchema" json:"inputSchema"`
	Data        map[string]any `yaml:"resultDataSchema" json:"resultDataSchema"`
	Limits      struct {
		Timeout int `yaml:"timeoutMs"`
	} `yaml:"limits" json:"-"`
	input, output *jsonschema.Resolved
}

func Catalog() ([]Tool, error) {
	var bundle struct {
		Tools []Tool `yaml:"tools"`
	}
	if err := yaml.Unmarshal(catalogYAML, &bundle); err != nil {
		return nil, err
	}
	for i := range bundle.Tools {
		t := &bundle.Tools[i]
		var err error
		t.input, err = compile(t.Input)
		if err != nil {
			return nil, err
		}
		t.output, err = compile(t.Data)
		if err != nil {
			return nil, err
		}
	}
	return bundle.Tools, nil
}
func compile(m map[string]any) (*jsonschema.Resolved, error) {
	b, e := json.Marshal(m)
	if e != nil {
		return nil, e
	}
	var s jsonschema.Schema
	if e = json.Unmarshal(b, &s); e != nil {
		return nil, e
	}
	return s.Resolve(nil)
}

// Object rejects duplicate keys at every depth before schema validation. The
// small bounded inputs contain no numeric identifiers (versions are strings).
func Object(raw []byte) (map[string]any, error) {
	if len(raw) > 8192 || !utf8.Valid(raw) {
		return nil, errors.New("invalid JSON size or encoding")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	var visit func(int) (any, error)
	visit = func(depth int) (any, error) {
		if depth > 8 {
			return nil, errors.New("JSON too deep")
		}
		tok, e := d.Token()
		if e != nil {
			return nil, e
		}
		if delim, ok := tok.(json.Delim); ok {
			if delim != '{' {
				return nil, errors.New("object fields only")
			}
			m := map[string]any{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return nil, e
				}
				key, ok := k.(string)
				if !ok {
					return nil, errors.New("invalid key")
				}
				if _, yes := m[key]; yes {
					return nil, errors.New("duplicate field")
				}
				v, e := visit(depth + 1)
				if e != nil {
					return nil, e
				}
				m[key] = v
			}
			_, e = d.Token()
			return m, e
		}
		return tok, nil
	}
	v, e := visit(0)
	if e != nil {
		return nil, e
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("object required")
	}
	return m, nil
}
func (t Tool) Arguments(raw []byte) (map[string]any, error) {
	m, e := Object(raw)
	if e != nil {
		return nil, e
	}
	if e = t.input.Validate(m); e != nil {
		return nil, errors.New("arguments do not match tool schema")
	}
	return m, nil
}
func (t Tool) ValidateData(data any) error {
	b, e := json.Marshal(data)
	if e != nil {
		return e
	}
	var v any
	if e = json.Unmarshal(b, &v); e != nil {
		return e
	}
	return t.output.Validate(v)
}
