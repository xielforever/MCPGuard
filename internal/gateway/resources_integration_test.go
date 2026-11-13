package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"github.com/xielforever/MCPGuard/internal/nativepg"
	"github.com/xielforever/MCPGuard/internal/resources"
	"github.com/xielforever/MCPGuard/internal/runtimeconfig"
)

func TestResourceReadIntegration(t *testing.T) {
	path := os.Getenv("MCPGUARD_RA_CONFIG")
	if path == "" {
		t.Skip("explicit fresh RA fixture required")
	}
	var fixture struct {
		RaPgAdmins []struct {
			DSN string `json:"dsn"`
		} `json:"raPgAdmins"`
	}
	raw, e := os.ReadFile(path)
	if e != nil || json.Unmarshal(raw, &fixture) != nil || len(fixture.RaPgAdmins) != 2 {
		t.Fatal("invalid private fixture metadata")
	}
	ctx := context.Background()
	cfg, e := runtimeconfig.Load()
	if e != nil {
		t.Fatal(e)
	}
	vault, e := metadata.NewVault(cfg.EncryptionKey, cfg.PlanKey)
	if e != nil {
		t.Fatal(e)
	}
	store, e := metadata.Open(ctx, cfg.MetadataDSN, vault)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	catalog, e := resources.NewCatalog(ctx, store)
	if e != nil {
		t.Fatal(e)
	}
	secrets, e := resources.LoadFileSecrets(os.Getenv("MCPGUARD_RA_PRIVATE_CONFIG"))
	if e != nil {
		t.Fatal(e)
	}
	manager := resources.NewReadManager(catalog, secrets)
	defer manager.Close()
	legacy, e := nativepg.Open(ctx, cfg.BusinessDSN)
	if e != nil {
		t.Fatal(e)
	}
	defer legacy.Close()
	admin, e := pgx.Connect(ctx, os.Getenv("MCPGUARD_TEST_METADATA_ADMIN_DSN"))
	if e != nil {
		t.Fatal("metadata admin unavailable")
	}
	defer admin.Close(ctx)
	root, cancel := context.WithCancel(ctx)
	defer cancel()
	gateway := New(root, store, legacy)
	defer gateway.Close()
	gateway.AttachResources(catalog)
	// This probe exists only in this integration test binary. Production startup
	// deliberately registers no resource read before RA-11 package enforcement.
	gateway.MCP.AddTool(&mcp.Tool{Name: resources.FixtureToolName, InputSchema: v02.Schema("FixtureReadInput"), OutputSchema: v02.Schema("FixtureReadResult")}, func(call context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p, ok := principal(req.Extra)
		if !ok {
			return nil, safeProtocolError("authentication required")
		}
		var input v02.FixtureReadInput
		if v02.Decode("FixtureReadInput", req.Params.Arguments, &input) != nil {
			return resourceToolError(resources.ErrInput), nil
		}
		result, err := manager.ReadFixture(call, p, input)
		if err != nil {
			return resourceToolError(err), nil
		}
		return managedResult(result, false), nil
	})
	server := httptest.NewServer(gateway.Handler())
	defer server.Close()
	clients := map[string]*mcp.ClientSession{}
	principals := map[string]metadata.Principal{}
	tokens := map[string]string{}
	for _, name := range []string{"ALICE", "BOB", "VISITOR"} {
		token := os.Getenv("MCPGUARD_TEST_" + name + "_TOKEN")
		tokens[name] = token
		p, err := store.VerifyToken(ctx, token)
		if err != nil {
			t.Fatal("test identity unavailable")
		}
		principals[name] = p
		client := mcp.NewClient(&mcp.Implementation{Name: "ra03-isolated-fixture", Version: "1"}, nil)
		session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: tokenTransport{token}, Timeout: 8 * time.Second}, DisableStandaloneSSE: true, MaxRetries: -1}, &mcp.ClientSessionOptions{ProtocolVersion: Profile})
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		clients[name] = session
	}
	ids := []string{"40000000-0000-4000-8000-000000000001", "40000000-0000-4000-8000-000000000002"}
	requestOnly := "40000000-0000-4000-8000-000000000003"
	list := func(name string, args any) v02.ListResourcesResult {
		t.Helper()
		result, err := clients[name].CallTool(ctx, &mcp.CallToolParams{Name: "resource.list", Arguments: args})
		if err != nil || result.IsError {
			t.Fatal("resource list failed")
		}
		data, _ := json.Marshal(result.StructuredContent)
		var out v02.ListResourcesResult
		if v02.Decode("ListResourcesResult", data, &out) != nil {
			t.Fatal("resource list contract drift")
		}
		return out
	}
	read := func(name, rid, item string) (v02.FixtureReadResult, bool) {
		t.Helper()
		result, err := clients[name].CallTool(ctx, &mcp.CallToolParams{Name: resources.FixtureToolName, Arguments: v02.FixtureReadInput{ResourceID: rid, ItemID: item}})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			return v02.FixtureReadResult{}, false
		}
		data, _ := json.Marshal(result.StructuredContent)
		var out v02.FixtureReadResult
		if v02.Decode("FixtureReadResult", data, &out) != nil {
			t.Fatal("read result contract drift")
		}
		return out, true
	}
	t.Run("discovery_requestability_and_safe_views", func(t *testing.T) {
		a, b, v := list("ALICE", map[string]any{}), list("BOB", map[string]any{}), list("VISITOR", map[string]any{})
		if len(a.Resources) != 3 || len(b.Resources) != 2 || len(v.Resources) != 0 {
			t.Fatal("resource visibility drift")
		}
		for _, item := range a.Resources {
			if !item.CanRequest || item.AccessState != "NOT_REQUESTED" {
				t.Fatal("discovery created an access grant")
			}
		}
		for _, item := range b.Resources {
			if item.CanRequest {
				t.Fatal("requestability leak")
			}
		}
		raw, _ := json.Marshal(a)
		for _, secret := range []string{"127.0.0.1", "secretRef", "endpointConfigRef", "ra_alice", "password"} {
			if bytes.Contains(raw, []byte(secret)) {
				t.Fatal("private catalog information exposed")
			}
		}
		r, err := clients["ALICE"].CallTool(ctx, &mcp.CallToolParams{Name: "resource.get", Arguments: v02.GetResourceInput{ResourceID: ids[0]}})
		if err != nil || r.IsError {
			t.Fatal("resource details unavailable")
		}
		data, _ := json.Marshal(r.StructuredContent)
		var detail v02.GetResourceResult
		if v02.Decode("GetResourceResult", data, &detail) != nil || len(detail.Profiles) != 1 {
			t.Fatal("versioned profile not bound")
		}
	})
	t.Run("two_clusters_two_users_distinct_native_roles", func(t *testing.T) {
		for i, rid := range ids {
			for name, user := range map[string]string{"ALICE": "alice", "BOB": "bob"} {
				result, ok := read(name, rid, "item_demo_1")
				if !ok || !result.Found || len(result.Items) != 1 {
					t.Fatal("native read failed")
				}
				want := []string{"orders", "customers"}[i] + " / " + user
				if result.Items[0].Note != want || result.NativeIdentity != "ra_"+user || result.ResourceID != rid {
					t.Fatal("native identity or resource data crossed")
				}
				if name == "BOB" && result.Items[0].Version != "9007199254740993" {
					t.Fatal("native bigint precision lost")
				}
				var cipher []byte
				var native string
				if admin.QueryRow(ctx, "SELECT result_ciphertext,native_identity FROM mcpguard_ra.fixture_reads WHERE org_id=$1 AND id=$2 AND principal_id=$3", principals[name].OrgID, result.ExecutionID, principals[name].ID).Scan(&cipher, &native) != nil {
					t.Fatal("read journal unavailable")
				}
				if native != result.NativeIdentity || bytes.Contains(cipher, []byte(want)) {
					t.Fatal("read journal identity or encryption failed")
				}
				plain, err := vault.Open(cipher, "ra-read/"+principals[name].OrgID+"/"+principals[name].ID+"/"+result.ExecutionID)
				if err != nil || !bytes.Contains(plain, []byte(want)) {
					t.Fatal("stored read result binding invalid")
				}
				var count int
				if admin.QueryRow(ctx, "SELECT count(*) FROM mcpguard_ra.fixture_audit WHERE org_id=$1 AND execution_id=$2", principals[name].OrgID, result.ExecutionID).Scan(&count) != nil || count != 2 {
					t.Fatal("read intent/result audit incomplete")
				}
			}
		}
		if manager.PoolCount() != 4 {
			t.Fatal("expected four isolated pools")
		}
		a, ok := read("ALICE", ids[0], "item_demo_bob_only")
		if !ok || a.Found {
			t.Fatal("database RLS did not hide Bob's row")
		}
		b, ok := read("BOB", ids[0], "item_demo_bob_only")
		if !ok || !b.Found {
			t.Fatal("database native owner identity not effective")
		}
	})
	t.Run("cursor_authentication_filter_and_epoch", func(t *testing.T) {
		one := 1
		first := list("ALICE", v02.ListResourcesInput{Limit: &one})
		if len(first.Resources) != 1 || first.NextAfter == "" {
			t.Fatal("missing bounded cursor")
		}
		next := list("ALICE", v02.ListResourcesInput{Limit: &one, After: &first.NextAfter})
		if next.Resources[0].ResourceID == first.Resources[0].ResourceID {
			t.Fatal("cursor did not advance")
		}
		for _, name := range []string{"BOB", "VISITOR"} {
			r, e := clients[name].CallTool(ctx, &mcp.CallToolParams{Name: "resource.list", Arguments: map[string]any{"after": first.NextAfter}})
			if e != nil || !r.IsError {
				t.Fatal("cursor crossed principal")
			}
		}
		forged := first.NextAfter + "x"
		r, e := clients["ALICE"].CallTool(ctx, &mcp.CallToolParams{Name: "resource.list", Arguments: map[string]any{"after": forged}})
		if e != nil || !r.IsError {
			t.Fatal("cursor tampering accepted")
		}
		if _, e = admin.Exec(ctx, "UPDATE mcpguard_ra.resource_access SET can_request=false WHERE org_id=$1 AND principal_id=$2 AND resource_id=$3", principals["ALICE"].OrgID, principals["ALICE"].ID, requestOnly); e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() {
			_, _ = admin.Exec(ctx, "UPDATE mcpguard_ra.resource_access SET can_request=true WHERE org_id=$1 AND principal_id=$2 AND resource_id=$3", principals["ALICE"].OrgID, principals["ALICE"].ID, requestOnly)
		})
		r, e = clients["ALICE"].CallTool(ctx, &mcp.CallToolParams{Name: "resource.list", Arguments: map[string]any{"after": first.NextAfter}})
		if e != nil || !r.IsError {
			t.Fatal("stale authority cursor accepted")
		}
	})
	t.Run("hidden_request_only_and_unsupported_routes", func(t *testing.T) {
		for _, pair := range [][2]string{{"VISITOR", ids[0]}, {"BOB", requestOnly}, {"ALICE", requestOnly}, {"ALICE", "40000000-0000-4000-8000-999999999999"}} {
			if _, ok := read(pair[0], pair[1], "item_demo_1"); ok {
				t.Fatal("discovery or guessed ID granted execution")
			}
		}
		r, e := clients["BOB"].CallTool(ctx, &mcp.CallToolParams{Name: "resource.get", Arguments: v02.GetResourceInput{ResourceID: requestOnly}})
		if e != nil || !r.IsError {
			t.Fatal("hidden resource metadata leaked")
		}
		tools, e := clients["ALICE"].ListTools(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		for _, tool := range tools.Tools {
			if tool.Name == "pg.get_item" || strings.HasPrefix(tool.Name, "credential.") || strings.HasPrefix(tool.Name, "access.") {
				t.Fatal("future capabilities published before package enforcement")
			}
		}
	})
	t.Run("rest_mcp_parity_and_input_rejection", func(t *testing.T) {
		req, _ := http.NewRequest("GET", server.URL+"/api/v2/resources?limit=1", nil)
		req.Header.Set("Authorization", "Bearer "+tokens["ALICE"])
		response, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		var out v02.ListResourcesResult
		if response.StatusCode != 200 || json.NewDecoder(response.Body).Decode(&out) != nil || len(out.Resources) != 1 {
			t.Fatal("REST catalog mismatch")
		}
		req, _ = http.NewRequest("GET", server.URL+"/api/v2/resources?principalId="+principals["BOB"].ID, nil)
		req.Header.Set("Authorization", "Bearer "+tokens["ALICE"])
		response, e = http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		_ = response.Body.Close()
		if response.StatusCode != 400 {
			t.Fatal("REST identity selector accepted")
		}
		for _, args := range []any{map[string]any{"limit": 101}, map[string]any{"limit": nil}, map[string]any{"ownerId": principals["BOB"].ID}} {
			r, e := clients["ALICE"].CallTool(ctx, &mcp.CallToolParams{Name: "resource.list", Arguments: args})
			if e != nil || !r.IsError {
				t.Fatal("MCP invalid selector accepted")
			}
		}
		req, _ = http.NewRequest("GET", server.URL+"/api/v2/resources", nil)
		response, e = http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		_ = response.Body.Close()
		if response.StatusCode != 401 {
			t.Fatal("anonymous directory accepted")
		}
	})
	t.Run("disabled_resource_and_no_binding_fallback", func(t *testing.T) {
		_, e := admin.Exec(ctx, "UPDATE mcpguard_ra.resource_heads SET enabled=false,revision=revision+1 WHERE org_id=$1 AND resource_id=$2", principals["ALICE"].OrgID, ids[0])
		if e != nil {
			t.Fatal(e)
		}
		if _, ok := read("ALICE", ids[0], "item_demo_1"); ok {
			t.Fatal("disabled resource used an existing pool")
		}
		_, e = admin.Exec(ctx, "UPDATE mcpguard_ra.resource_heads SET enabled=true,revision=revision+1 WHERE org_id=$1 AND resource_id=$2", principals["ALICE"].OrgID, ids[0])
		if e != nil {
			t.Fatal(e)
		}
		var raw []byte
		if admin.QueryRow(ctx, "SELECT definition FROM mcpguard_ra.identity_bindings WHERE org_id=$1 AND principal_id=$2 AND resource_id=$3 ORDER BY binding_version DESC LIMIT 1", principals["ALICE"].OrgID, principals["ALICE"].ID, ids[0]).Scan(&raw) != nil {
			t.Fatal("binding missing")
		}
		var binding v02.CredentialBinding
		_ = json.Unmarshal(raw, &binding)
		binding.BindingVersion = "2"
		binding.Enabled = false
		raw, _ = json.Marshal(binding)
		_, e = admin.Exec(ctx, `INSERT INTO mcpguard_ra.identity_bindings SELECT org_id,principal_id,resource_id,binding_id,2,resource_version,profile_id,profile_version,false,$4 FROM mcpguard_ra.identity_bindings WHERE org_id=$1 AND principal_id=$2 AND resource_id=$3 AND binding_version=1`, principals["ALICE"].OrgID, principals["ALICE"].ID, ids[0], raw)
		if e != nil {
			t.Fatal(e)
		}
		if _, ok := read("ALICE", ids[0], "item_demo_1"); ok {
			t.Fatal("disabled latest binding fell back to v1")
		}
		if _, ok := read("BOB", ids[0], "item_demo_1"); !ok {
			t.Fatal("Alice binding change affected Bob")
		}
	})
	t.Run("postgres_role_drift_and_cancellation", func(t *testing.T) {
		native, e := pgx.Connect(ctx, fixture.RaPgAdmins[1].DSN)
		if e != nil {
			t.Fatal("native admin unavailable")
		}
		defer native.Close(ctx)
		_, e = native.Exec(ctx, "ALTER ROLE ra_alice BYPASSRLS")
		if e != nil {
			t.Fatal(e)
		}
		if _, ok := read("ALICE", ids[1], "item_demo_1"); ok {
			t.Fatal("BYPASSRLS drift accepted")
		}
		if _, e = native.Exec(ctx, "ALTER ROLE ra_alice NOBYPASSRLS"); e != nil {
			t.Fatal(e)
		}
		if _, ok := read("ALICE", ids[1], "item_demo_1"); !ok {
			t.Fatal("safe native role did not recover")
		}
		lock, e := native.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer lock.Rollback(ctx)
		if _, e = lock.Exec(ctx, "LOCK resource_demo.items IN ACCESS EXCLUSIVE MODE"); e != nil {
			t.Fatal(e)
		}
		limited, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, e = manager.ReadFixture(limited, principals["ALICE"], v02.FixtureReadInput{ResourceID: ids[1], ItemID: "item_demo_1"})
		if e == nil || time.Since(start) > 4*time.Second {
			t.Fatal("cancelled resource read continued")
		}
		if e = lock.Rollback(ctx); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("runtime_role_cannot_mutate_catalog_history", func(t *testing.T) {
		_, err := store.Pool.Exec(ctx, "UPDATE mcpguard_ra.resource_heads SET enabled=false")
		if err == nil {
			t.Fatal("runtime role can manage catalog")
		}
		var leftovers int
		if admin.QueryRow(ctx, "SELECT count(*) FROM mcpguard_ra.fixture_reads WHERE state='RUNNING'").Scan(&leftovers) != nil || leftovers != 0 {
			t.Fatal("read completion journal unfinished")
		}
		_, err = admin.Exec(ctx, "DELETE FROM mcpguard_ra.fixture_reads")
		if err == nil {
			t.Fatal("read execution history deletable")
		}
	})
	t.Run("catalog_opt_in_keeps_legacy_default", func(t *testing.T) {
		plain := New(ctx, store, legacy)
		defer plain.Close()
		local := httptest.NewServer(plain.Handler())
		defer local.Close()
		req, _ := http.NewRequest("GET", local.URL+"/api/v2/resources", nil)
		req.Header.Set("Authorization", "Bearer "+tokens["ALICE"])
		response, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		if response.StatusCode != 404 {
			t.Fatal("new catalog implicitly enabled")
		}
		if _, err := catalog.Get(ctx, principals["VISITOR"], v02.GetResourceInput{ResourceID: ids[0]}); !errors.Is(err, resources.ErrDenied) {
			t.Fatal("hidden resource not denied")
		}
	})
}
