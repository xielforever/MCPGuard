package gateway

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

func TestAuditQueryBoundariesAndIdentitySelectors(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		after int64
		limit int
	}{
		{"", 0, 100}, {`{}`, 0, 100}, {`{"limit":1}`, 0, 1},
		{`{"after":"42","limit":100}`, 42, 100},
		{`{"after":"9007199254740993"}`, 9007199254740993, 100},
		{`{"after":"9223372036854775807"}`, 9223372036854775807, 100},
	} {
		q, e := parseAuditQuery([]byte(tc.raw))
		if e != nil || q.After != tc.after || q.Limit != tc.limit {
			t.Fatalf("%q: query=%+v, error=%v", tc.raw, q, e)
		}
	}
	for _, raw := range []string{
		`null`, `[]`, `{"after":null}`, `{"limit":null}`, `{"limit":true}`,
		`{"after":-1}`, `{"after":0.5}`, `{"after":0}`, `{"after":"9223372036854775808"}`,
		`{"after":"-1"}`, `{"after":"00"}`, `{"after":"+1"}`, `{"after":"1.0"}`, `{"after":"1e2"}`,
		`{"limit":0}`, `{"limit":-1}`, `{"limit":101}`, `{"limit":1.5}`, `{"limit":"1"}`,
		`{"limit":1,"limit":2}`, `{"after":0,"after":1}`, `{"after":[]}`,
		`{"principalId":"other"}`, `{"orgId":"other"}`, `{"scopeId":"scope_b"}`,
		`{"after":0} {}`, `{"after":0`, `{"after":}`, "{" + strings.Repeat(" ", 256) + "}",
	} {
		if _, e := parseAuditQuery([]byte(raw)); e == nil {
			t.Errorf("accepted invalid audit arguments %s", raw)
		}
	}
}

func TestAuditPageContract(t *testing.T) {
	var schema jsonschema.Schema
	if e := json.Unmarshal(auditPageSchema, &schema); e != nil {
		t.Fatal(e)
	}
	resolved, e := schema.Resolve(nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, page := range []auditPage{
		{Events: []metadata.AuditEvent{}, NextAfter: 9223372036854775807},
		{Events: []metadata.AuditEvent{{Seq: 9007199254740993, ID: "event", EventType: "INPUT_REJECTED", CreatedAt: time.Now().UTC()}}, NextAfter: 9007199254740993},
	} {
		b, e := json.Marshal(page.mcp())
		if e != nil {
			t.Fatal(e)
		}
		var value any
		if e = json.Unmarshal(b, &value); e != nil {
			t.Fatal(e)
		}
		if e = resolved.Validate(value); e != nil {
			t.Fatalf("invalid audit page %s: %v", b, e)
		}
	}
}
