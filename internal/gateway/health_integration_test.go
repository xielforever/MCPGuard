package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func testHealthIntegration(t *testing.T, ctx context.Context, g *Gateway, admin, badmin *pgx.Conn, clients map[string]*mcp.ClientSession, serverURL, visitorID, visitorToken string) {
	t.Helper()
	call := func(client *mcp.ClientSession) healthResult {
		t.Helper()
		result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "gateway.health", Arguments: map[string]any{}})
		if err != nil || result.IsError {
			t.Fatal("health call failed", result, err)
		}
		b, _ := json.Marshal(result.StructuredContent)
		var out healthResult
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		if len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text == "" {
			t.Fatal("health has no text fallback")
		}
		return out
	}
	for _, name := range []string{"ALICE", "VISITOR"} {
		r := call(clients[name])
		if !r.Live || !r.Ready || !r.Dependencies["metadata"].Ready || !r.Dependencies["businessPostgres"].Ready {
			t.Fatal("healthy dependencies were not ready", name, r)
		}
	}
	assertLive := func() {
		t.Helper()
		resp, err := http.Get(serverURL + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatal("dependency failure incorrectly failed process liveness")
		}
	}
	func() {
		if _, err := badmin.Exec(ctx, `REVOKE SELECT ON demo.items FROM mcpguard_reader`); err != nil {
			t.Fatal(err)
		}
		defer badmin.Exec(ctx, `GRANT SELECT ON demo.items TO mcpguard_reader`)
		r := call(clients["ALICE"])
		if !r.Live || r.Ready || !r.Dependencies["metadata"].Ready || r.Dependencies["businessPostgres"].Status != "UNAVAILABLE" {
			t.Fatal("business dependency failure was hidden", r)
		}
		assertLive()
	}()
	func() {
		if _, err := admin.Exec(ctx, `REVOKE INSERT ON mcpguard.audit_events FROM mcpguard_app`); err != nil {
			t.Fatal(err)
		}
		defer admin.Exec(ctx, `GRANT INSERT ON mcpguard.audit_events TO mcpguard_app`)
		r := call(clients["ALICE"])
		if !r.Live || r.Ready || r.Dependencies["metadata"].Status != "UNAVAILABLE" || !r.Dependencies["businessPostgres"].Ready {
			t.Fatal("metadata dependency failure was hidden", r)
		}
		assertLive()
	}()
	func() {
		lock, err := badmin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Rollback(ctx)
		if _, err := lock.Exec(ctx, `LOCK TABLE demo.items IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		r := call(clients["ALICE"])
		if !r.Live || r.Ready || r.Dependencies["businessPostgres"].Ready || time.Since(start) > 4*time.Second {
			t.Fatal("blocked table did not fail bounded readiness", r)
		}
		assertLive()
	}()
	if r := call(clients["ALICE"]); !r.Ready {
		t.Fatal("readiness did not recover after dependencies recovered", r)
	}
	if err := g.Store.CheckReady(ctx); err != nil {
		t.Fatal("metadata did not recover", err)
	}
	// A 401 closes that SDK connection. Use a separate session so this test
	// does not invalidate the shared Visitor client used by later audit tests.
	client := mcp.NewClient(&mcp.Implementation{Name: "health-revocation-test", Version: "1"}, nil)
	revoked, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: serverURL + "/mcp", HTTPClient: &http.Client{Transport: tokenTransport{visitorToken}}, DisableStandaloneSSE: true, MaxRetries: -1}, &mcp.ClientSessionOptions{ProtocolVersion: Profile})
	if err != nil {
		t.Fatal(err)
	}
	defer revoked.Close()
	if _, err := admin.Exec(ctx, `UPDATE mcpguard.principals SET enabled=false WHERE id=$1`, visitorID); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, `UPDATE mcpguard.principals SET enabled=true WHERE id=$1`, visitorID)
	result, err := revoked.CallTool(ctx, &mcp.CallToolParams{Name: "gateway.health", Arguments: map[string]any{}})
	if err == nil && (!result.IsError || result.StructuredContent != nil) {
		t.Fatal("disabled identity retained health access")
	}
}
