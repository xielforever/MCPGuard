// Package v02 owns the canonical, versioned resource-access contract.
package v02

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
)

//go:embed source.json
var source []byte
var once sync.Once
var schemas map[string]*jsonschema.Resolved
var definitions map[string]any
var loadErr error

// Schema returns a self-contained schema, preserving the same definitions used
// to generate documentation and DTOs. No remote references are fetched.
func Schema(name string) json.RawMessage {
	var root map[string]any
	if json.Unmarshal(source, &root) != nil {
		return nil
	}
	definitions := root["$defs"].(map[string]any)
	definition, ok := definitions[name].(map[string]any)
	if !ok {
		return nil
	}
	// MCP checks the root type before resolving references. Expose the named
	// definition at the root while retaining local references to the same source.
	// Publish only transitively referenced definitions. A catalog tool must not
	// advertise the whole future credential lifecycle or multiply its schema size.
	used := map[string]any{}
	var collect func(any)
	collect = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			for key, child := range node {
				if key == "$ref" {
					ref, _ := child.(string)
					name := strings.TrimPrefix(ref, "#/$defs/")
					if referenced, ok := definitions[name]; ok && used[name] == nil {
						used[name] = referenced
						collect(referenced)
					}
				} else {
					collect(child)
				}
			}
		case []any:
			for _, child := range node {
				collect(child)
			}
		}
	}
	collect(definition)
	result := map[string]any{"$schema": root["$schema"], "$defs": used}
	for key, value := range definition {
		result[key] = value
	}
	b, _ := json.Marshal(result)
	return b
}

func load() {
	var root struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(source, &root); err != nil {
		loadErr = err
		return
	}
	definitions = make(map[string]any, len(root.Defs))
	for name, raw := range root.Defs {
		var definition map[string]any
		if err := json.Unmarshal(raw, &definition); err != nil {
			loadErr = err
			return
		}
		definitions[name] = definition
	}
	schemas = map[string]*jsonschema.Resolved{}
	for name := range root.Defs {
		var schema jsonschema.Schema
		if err := json.Unmarshal(Schema(name), &schema); err != nil {
			loadErr = err
			return
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			loadErr = err
			return
		}
		schemas[name] = resolved
	}
}

// jsonschema-go records format as an annotation. Apply our canonical UTC
// timestamp format as well, so runtime and offline acceptance vectors agree.
func validFormats(spec map[string]any, value any) bool {
	if ref, ok := spec["$ref"].(string); ok {
		definition, ok := definitions[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
		return ok && validFormats(definition, value)
	}
	if spec["format"] == "date-time" {
		text, ok := value.(string)
		if !ok {
			return false
		}
		if _, err := time.Parse(time.RFC3339Nano, text); err != nil {
			return false
		}
	}
	if object, ok := value.(map[string]any); ok {
		properties, _ := spec["properties"].(map[string]any)
		for key, child := range object {
			property, _ := properties[key].(map[string]any)
			if property != nil && !validFormats(property, child) {
				return false
			}
		}
	}
	if array, ok := value.([]any); ok {
		item, _ := spec["items"].(map[string]any)
		for _, child := range array {
			if item != nil && !validFormats(item, child) {
				return false
			}
		}
	}
	return true
}

// CheckJSON rejects duplicate keys at every depth and bounds untrusted JSON.
func CheckJSON(raw []byte) error {
	if len(raw) == 0 || len(raw) > MaxInputBytes {
		return errors.New("invalid contract input")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var visit func(int) error
	visit = func(depth int) error {
		if depth > MaxDepth {
			return errors.New("invalid contract depth")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				k, ok := key.(string)
				if !ok || seen[k] {
					return errors.New("invalid duplicate field")
				}
				seen[k] = true
				if err = visit(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return errors.New("invalid object")
			}
		case '[':
			for decoder.More() {
				if err := visit(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return errors.New("invalid array")
			}
		default:
			return errors.New("invalid delimiter")
		}
		return nil
	}
	if err := visit(0); err != nil {
		return errors.New("invalid contract input")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("invalid trailing input")
	}
	return nil
}

// Initialize validates and resolves the local contract before serving requests.
// Startup work must not consume a caller's bounded database execution time.
func Initialize() error {
	once.Do(load)
	if loadErr != nil {
		return errors.New("contract unavailable")
	}
	return nil
}

func Decode(name string, raw []byte, destination any) error {
	if CheckJSON(raw) != nil {
		return errors.New("invalid contract input")
	}
	if err := Initialize(); err != nil {
		return err
	}
	schema, ok := schemas[name]
	if !ok {
		return errors.New("unknown contract")
	}
	var value any
	if json.Unmarshal(raw, &value) != nil || schema.Validate(value) != nil || !validFormats(definitions[name].(map[string]any), value) {
		return errors.New("invalid contract input")
	}
	if destination != nil {
		if err := json.Unmarshal(raw, destination); err != nil {
			return errors.New("invalid contract input")
		}
	}
	return nil
}
