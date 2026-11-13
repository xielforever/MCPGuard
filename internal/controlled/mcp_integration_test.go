package controlled_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xielforever/MCPGuard/internal/controlled"
	"github.com/xielforever/MCPGuard/internal/gateway"
	"github.com/xielforever/MCPGuard/internal/nativepg"
)

type transport struct{ token string }

func (t transport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header = r.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(copy)
}
func TestControlledMCPIntegration(t *testing.T) {
	f := setup(t)
	reader, e := nativepg.Open(f.ctx, os.Getenv("MCPGUARD_BUSINESS_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	defer reader.Close()
	g := gateway.New(f.ctx, f.store, reader)
	defer g.Close()
	console, e := controlled.OpenConsole(f.s, "")
	if e != nil {
		t.Fatal(e)
	}
	g.AttachControlled(f.s, console, nil)
	server := httptest.NewServer(g.Handler())
	defer server.Close()
	connect := func(token string) *mcp.ClientSession {
		client := mcp.NewClient(&mcp.Implementation{Name: "controlled-integration", Version: "1"}, nil)
		s, e := client.Connect(f.ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: transport{token}}, DisableStandaloneSSE: true, MaxRetries: -1}, &mcp.ClientSessionOptions{ProtocolVersion: gateway.Profile})
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}
	alice := connect(f.tokens["alice"])
	visitor := connect(f.tokens["reviewer"])
	listed, e := alice.ListTools(f.ctx, nil)
	if e != nil || len(listed.Tools) != 13 {
		t.Fatal("controlled directory", len(listed.Tools), e)
	}
	schemas := map[string]*jsonschema.Resolved{}
	for _, tool := range listed.Tools {
		if tool.Name == "action.approve" {
			t.Fatal("approval exposed to model")
		}
		if tool.OutputSchema != nil {
			raw, _ := json.Marshal(tool.OutputSchema)
			var schema jsonschema.Schema
			if e = json.Unmarshal(raw, &schema); e != nil {
				t.Fatal(e)
			}
			resolved, e := schema.Resolve(nil)
			if e != nil {
				t.Fatal(e)
			}
			schemas[tool.Name] = resolved
		}
	}
	limited, e := visitor.ListTools(f.ctx, nil)
	if e != nil || len(limited.Tools) != 7 {
		t.Fatal("visitor directory", len(limited.Tools), e)
	}
	call := func(name string, args any) map[string]any {
		t.Helper()
		res, e := alice.CallTool(f.ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if e != nil || res.IsError {
			t.Fatalf("%s failed: %v %v", name, res, e)
		}
		raw, _ := json.Marshal(res.StructuredContent)
		var v map[string]any
		if e = json.Unmarshal(raw, &v); e != nil {
			t.Fatal(e)
		}
		if schema := schemas[name]; schema != nil {
			if e = schema.Validate(v); e != nil {
				t.Fatalf("%s invalid output %s: %v", name, raw, e)
			}
		}
		return v
	}
	for _, name := range []string{"pg.get_demo_item", "redis.get_demo_note", "directory.get_demo_office", "gateway.health", "audit.list", "history.list"} {
		args := map[string]any{}
		switch name {
		case "pg.get_demo_item":
			args["itemId"] = "item_demo_1"
		case "redis.get_demo_note":
			args["noteId"] = "welcome"
		case "directory.get_demo_office":
			args["officeId"] = "office-demo"
		}
		read := call(name, args)
		if name == "pg.get_demo_item" {
			call("action.status", map[string]any{"actionId": read["actionId"]})
			call("history.get_result", map[string]any{"actionId": read["actionId"]})
		}
	}
	r := call("pg.update_demo_note", map[string]any{"itemId": "item_demo_1", "note": "MCP protocol write", "expectedVersion": "1"})
	id := r["actionId"].(string)
	call("action.status", map[string]any{"actionId": id})
	call("action.resume", map[string]any{"actionId": id})
	approve(t, f, id)
	call("action.resume", map[string]any{"actionId": id})
	call("action.status", map[string]any{"actionId": id})
	call("history.get_result", map[string]any{"actionId": id})
	r = call("redis.set_demo_note", map[string]any{"noteId": "help", "value": "cancel MCP", "ttlSeconds": 60})
	id = r["actionId"].(string)
	call("action.cancel", map[string]any{"actionId": id})
	call("action.resume", map[string]any{"actionId": id})
	call("action.status", map[string]any{"actionId": id})
}
