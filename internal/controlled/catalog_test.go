package controlled

import (
	"bytes"
	"encoding/json"
	"gopkg.in/yaml.v3"
	"os"
	"testing"
)

func TestCatalogContracts(t *testing.T) {
	ts, e := Catalog()
	if e != nil {
		t.Fatal(e)
	}
	if len(ts) != 6 {
		t.Fatal("six bounded operations required")
	}
	original, e := os.ReadFile("../../docs/specs/demo-tools.yaml")
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(bytes.ReplaceAll(original, []byte("\r\n"), []byte("\n")), bytes.ReplaceAll(catalogYAML, []byte("\r\n"), []byte("\n"))) {
		t.Fatal("compiled catalog drift")
	}
	var bundle struct {
		Tools []struct {
			Examples struct {
				Valid      []map[string]any `yaml:"validInputs"`
				Invalid    []map[string]any `yaml:"invalidInputs"`
				Success    []any            `yaml:"successDataSamples"`
				BadSuccess []any            `yaml:"invalidSuccessDataSamples"`
			} `yaml:"examples"`
		} `yaml:"tools"`
	}
	if e = yaml.Unmarshal(catalogYAML, &bundle); e != nil {
		t.Fatal(e)
	}
	for i, tool := range ts {
		t.Run(tool.Name, func(t *testing.T) {
			for _, v := range bundle.Tools[i].Examples.Valid {
				b, _ := json.Marshal(v)
				if _, e := tool.Arguments(b); e != nil {
					t.Fatalf("valid input: %s: %v", b, e)
				}
			}
			for _, v := range bundle.Tools[i].Examples.Invalid {
				b, _ := json.Marshal(v)
				if _, e := tool.Arguments(b); e == nil {
					t.Fatalf("accepted invalid input %s", b)
				}
			}
			for _, v := range bundle.Tools[i].Examples.Success {
				if e := tool.ValidateData(v); e != nil {
					t.Fatal(e)
				}
			}
			for _, v := range bundle.Tools[i].Examples.BadSuccess {
				if e := tool.ValidateData(v); e == nil {
					t.Fatal("accepted invalid output")
				}
			}
		})
	}
}
func TestStrictObjects(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{"x":1,"x":2}`, `{"x":{"a":1,"a":2}}`, `{"x":1} {}`, `{"x":[1]}`} {
		if _, e := Object([]byte(raw)); e == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
