package gateway

import (
	"bytes"
	"encoding/json"
	"github.com/google/jsonschema-go/jsonschema"
	"os"
	"testing"
)

func TestStrictArgumentsAndContract(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{}`, `{"itemId":null}`, `{"itemId":1}`, `{"itemId":"../scope_b"}`, `{"itemId":"item_demo_1","scopeId":"scope_b"}`, `{"itemId":"item_demo_1","sql":"SELECT 1"}`, `{"itemId":"item_demo_1","itemId":"item_demo_2"}`, `{"itemId":"item_demo_1"} {}`, `{"itemId":"item_demo_1"`, `{"itemId":"item_demo_\u212a"}`} {
		if _, e := parseItem([]byte(raw)); e == nil {
			t.Errorf("accepted invalid input %s", raw)
		}
	}
	if v, e := parseItem([]byte(`{"itemId":"item_demo_1"}`)); e != nil || v != "item_demo_1" {
		t.Fatal(v, e)
	}
	doc, e := os.ReadFile("../../docs/specs/tool-result.schema.json")
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(doc, resultSchema) {
		t.Fatal("embedded output schema differs from reviewed contract")
	}
	validateResult(t, failure("ACCESS_DENIED", "AUTHORIZATION", "Denied"))
}
func validateResult(t *testing.T, r Result) {
	t.Helper()
	var s jsonschema.Schema
	if e := json.Unmarshal(resultSchema, &s); e != nil {
		t.Fatal(e)
	}
	resolved, e := s.Resolve(nil)
	if e != nil {
		t.Fatal(e)
	}
	b, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	var v any
	_ = json.Unmarshal(b, &v)
	if e = resolved.Validate(v); e != nil {
		t.Fatalf("invalid structured result: %s: %v", b, e)
	}
}
