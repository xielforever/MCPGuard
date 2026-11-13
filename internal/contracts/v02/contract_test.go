package v02

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCanonicalExamples(t *testing.T) {
	var document struct {
		Examples []struct {
			Schema string          `json:"schema"`
			Valid  bool            `json:"valid"`
			Value  json.RawMessage `json:"value"`
		} `json:"x-examples"`
	}
	if err := json.Unmarshal(source, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Examples) < 20 {
		t.Fatal("missing positive/negative acceptance vectors")
	}
	for i, example := range document.Examples {
		err := Decode(example.Schema, example.Value, nil)
		if (err == nil) != example.Valid {
			t.Errorf("example %d (%s) expected valid=%t, got %v", i, example.Schema, example.Valid, err)
		}
	}
}
func TestRejectAmbiguousOrOversizedInput(t *testing.T) {
	for _, raw := range []string{
		`{"limit":1,"limit":2}`, `{"limit":null}`, `{"limit":1} {}`, `{"ownerId":"self"}`,
		strings.Repeat(" ", MaxInputBytes+1), `{"limit":1.5}`,
	} {
		if Decode("ListResourcesInput", []byte(raw), nil) == nil {
			t.Fatal("unsafe input accepted")
		}
	}
	if Decode("Missing", []byte("{}"), nil) == nil {
		t.Fatal("unknown contract accepted")
	}
}
