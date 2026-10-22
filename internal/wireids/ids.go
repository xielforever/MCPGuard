// Package wireids preserves the type and precision of MCP request IDs around
// go-sdk v1.8.0, whose JSON-RPC decoder otherwise converts numeric IDs to float64.
// It rewrites only envelope IDs and cancellation targets. The SDK still owns
// framing, protocol negotiation, concurrency, routing and cancellation handling.
package wireids

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"strconv"
	"strings"
)

const prefix = "mgid1."
const maxIDBytes = 1024
const MaxFrameBytes = 64 << 10

func object(data []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	t, e := d.Token()
	if e != nil {
		return nil, e
	}
	if t != json.Delim('{') {
		return nil, errors.New("JSON-RPC object required")
	}
	m := map[string]json.RawMessage{}
	for d.More() {
		k, e := d.Token()
		if e != nil {
			return nil, e
		}
		key, ok := k.(string)
		if !ok {
			return nil, errors.New("invalid key")
		}
		if _, ok = m[key]; ok {
			return nil, errors.New("duplicate envelope field")
		}
		var v json.RawMessage
		if e = d.Decode(&v); e != nil {
			return nil, e
		}
		m[key] = v
	}
	if _, e = d.Token(); e != nil {
		return nil, e
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	return m, nil
}
func marshal(m map[string]json.RawMessage) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(m); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte{'\n'}), nil
}

func canonicalID(raw []byte) ([]byte, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > maxIDBytes {
		return nil, errors.New("request ID exceeds limit")
	}
	if raw[0] == '"' {
		var s string
		if e := json.Unmarshal(raw, &s); e != nil {
			return nil, e
		}
		b, e := json.Marshal(s)
		if e != nil || len(b) > maxIDBytes {
			return nil, errors.New("string request ID exceeds limit")
		}
		return b, nil
	}
	if raw[0] != '-' && (raw[0] < '0' || raw[0] > '9') {
		return nil, errors.New("request ID must be a string or integer")
	}
	// JSON validation has already checked the number grammar. Bound exponent
	// expansion before using arbitrary precision arithmetic.
	if i := bytes.IndexAny(raw, "eE"); i >= 0 {
		exp, e := strconv.Atoi(string(raw[i+1:]))
		if e != nil || exp > maxIDBytes || exp < -maxIDBytes {
			return nil, errors.New("numeric request ID exceeds limit")
		}
	}
	r, ok := new(big.Rat).SetString(string(raw))
	if !ok || !r.IsInt() {
		return nil, errors.New("request ID must be an integer")
	}
	b := []byte(r.Num().String())
	if len(b) > maxIDBytes {
		return nil, errors.New("numeric request ID exceeds limit")
	}
	return b, nil
}
func encode(raw []byte) (json.RawMessage, error) {
	b, e := canonicalID(raw)
	if e != nil {
		return nil, e
	}
	return json.Marshal(prefix + base64.RawURLEncoding.EncodeToString(b))
}

func Incoming(data []byte) ([]byte, error) {
	m, e := object(data)
	if e != nil {
		return nil, e
	}
	changed := false
	if id, ok := m["id"]; ok {
		m["id"], e = encode(id)
		if e != nil {
			return nil, e
		}
		changed = true
	}
	var method string
	_ = json.Unmarshal(m["method"], &method)
	if method == "notifications/cancelled" {
		p, e := object(m["params"])
		if e != nil {
			return nil, e
		}
		if id, ok := p["requestId"]; ok {
			p["requestId"], e = encode(id)
			if e != nil {
				return nil, e
			}
			m["params"], e = marshal(p)
			if e != nil {
				return nil, e
			}
			changed = true
		}
	}
	if !changed {
		return data, nil
	}
	return marshal(m)
}
func Outgoing(data []byte) ([]byte, error) {
	m, e := object(data)
	if e != nil {
		return nil, e
	}
	if _, ok := m["method"]; ok {
		return data, nil
	}
	var id string
	if e = json.Unmarshal(m["id"], &id); e != nil || !strings.HasPrefix(id, prefix) {
		return data, nil
	}
	raw, e := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, prefix))
	if e != nil {
		return nil, e
	}
	b, e := canonicalID(raw)
	if e != nil {
		return nil, e
	}
	m["id"] = b
	return marshal(m)
}
