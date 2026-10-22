package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"github.com/xielforever/MCPGuard/internal/nativepg"
	"github.com/xielforever/MCPGuard/internal/runtimeconfig"
)

type tokenTransport struct{ token string }

func (tt tokenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c := r.Clone(r.Context())
	c.Header = r.Header.Clone()
	c.Header.Set("Authorization", "Bearer "+tt.token)
	return http.DefaultTransport.RoundTrip(c)
}

// Run via `make dev-test`. Without explicit isolated fixture credentials this
// test skips, rather than creating databases or touching arbitrary services.
func TestReadSliceIntegration(t *testing.T) {
	adminDSN := os.Getenv("MCPGUARD_TEST_METADATA_ADMIN_DSN")
	if adminDSN == "" {
		t.Skip("real PostgreSQL fixture required: make dev-up && make dev-test")
	}
	ctx := context.Background()
	cfg, e := runtimeconfig.Load()
	if e != nil {
		t.Fatal(e)
	}
	v, e := metadata.NewVault(cfg.EncryptionKey, cfg.PlanKey)
	if e != nil {
		t.Fatal(e)
	}
	if e = metadata.Migrate(ctx, adminDSN); e != nil {
		t.Fatal("migration idempotence:", e)
	}
	admin, e := pgx.Connect(ctx, adminDSN)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close(ctx)
	badmin, e := pgx.Connect(ctx, os.Getenv("MCPGUARD_TEST_BUSINESS_ADMIN_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	defer badmin.Close(ctx)
	s, e := metadata.Open(ctx, cfg.MetadataDSN, v)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	r, e := nativepg.Open(ctx, cfg.BusinessDSN)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	root, cancel := context.WithCancel(ctx)
	defer cancel()
	g := New(root, s, r)
	defer g.Close()
	httpServer := httptest.NewServer(g.Handler())
	defer httpServer.Close()
	tokens := map[string]string{}
	principals := map[string]metadata.Principal{}
	clients := map[string]*mcp.ClientSession{}
	for _, name := range []string{"ALICE", "BOB", "VISITOR"} {
		token := os.Getenv("MCPGUARD_TEST_" + name + "_TOKEN")
		tokens[name] = token
		p, e := s.VerifyToken(ctx, token)
		if e != nil {
			t.Fatal("fixture identity", name, e)
		}
		principals[name] = p
		c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
		cs, e := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp", HTTPClient: &http.Client{Transport: tokenTransport{token}, Timeout: 8 * time.Second}, DisableStandaloneSSE: true, MaxRetries: -1}, &mcp.ClientSessionOptions{ProtocolVersion: Profile})
		if e != nil {
			t.Fatal(e)
		}
		defer cs.Close()
		clients[name] = cs
	}
	call := func(t *testing.T, name string, args any) Result {
		t.Helper()
		rr, e := clients[name].CallTool(ctx, &mcp.CallToolParams{Name: "pg.get_demo_item", Arguments: args})
		if e != nil {
			t.Fatal(e)
		}
		var result Result
		b, _ := json.Marshal(rr.StructuredContent)
		if e = json.Unmarshal(b, &result); e != nil {
			t.Fatal(e)
		}
		validateResult(t, result)
		if rr.IsError != (result.RequestStatus != "SUCCEEDED") {
			t.Fatal("incorrect MCP isError")
		}
		if len(rr.Content) != 1 {
			t.Fatal("missing text fallback")
		}
		return result
	}
	read := func(t *testing.T, name, id string) Result { return call(t, name, map[string]any{"itemId": id}) }

	t.Run("two_employee_scopes_and_exact_versions", func(t *testing.T) {
		for name, want := range map[string]string{"ALICE": "Alice scope A", "BOB": "Bob scope B"} {
			list, e := clients[name].ListTools(ctx, nil)
			if e != nil || len(list.Tools) != 1 {
				t.Fatal(list, e)
			}
			rr := read(t, name, "item_demo_1")
			if rr.RequestStatus != "SUCCEEDED" {
				t.Fatal(rr)
			}
			data := rr.Data.(map[string]any)
			item := data["item"].(map[string]any)
			if item["note"] != want {
				t.Fatal("scope leak", data)
			}
			if name == "BOB" && item["version"] != "9007199254740993" {
				t.Fatal("version lost precision")
			}
			var cipher []byte
			if e = admin.QueryRow(ctx, `SELECT result_ciphertext FROM mcpguard.executions WHERE id=$1`, *rr.ExecutionID).Scan(&cipher); e != nil {
				t.Fatal(e)
			}
			if bytes.Contains(cipher, []byte(want)) {
				t.Fatal("plaintext result persisted")
			}
			if _, e = v.Open(cipher, principals[name].OrgID+"/"+*rr.ActionID+"/RESULT/local-v1"); e != nil {
				t.Fatal("encrypted result binding", e)
			}
		}
		for _, id := range []string{"item_demo_missing", "item_demo_b_only"} {
			rr := read(t, "ALICE", id)
			data := rr.Data.(map[string]any)
			if data["found"] != false || data["item"] != nil {
				t.Fatal("out-of-scope row exposed", data)
			}
		}
	})
	t.Run("hidden_tools_direct_denial_and_tampering", func(t *testing.T) {
		list, e := clients["VISITOR"].ListTools(ctx, nil)
		if e != nil || len(list.Tools) != 0 {
			t.Fatal("unauthorized directory", list, e)
		}
		rr := read(t, "VISITOR", "item_demo_1")
		if rr.Error.Code != "ACCESS_DENIED" || rr.ActionID != nil || rr.Data != nil {
			t.Fatal(rr)
		}
		for _, raw := range []json.RawMessage{[]byte(`{"itemId":"item_demo_1","scopeId":"scope_b"}`), []byte(`{"itemId":"item_demo_1","sql":"DELETE FROM demo.items"}`), []byte(`{"itemId":"item_demo_1","itemId":"item_demo_2"}`)} {
			rr = call(t, "ALICE", raw)
			if rr.Error.Code != "INVALID_ARGUMENTS" || rr.ExecutionID != nil {
				t.Fatal(rr)
			}
		}
	})
	t.Run("permissions_rechecked_in_existing_session", func(t *testing.T) {
		p := principals["BOB"]
		defer admin.Exec(ctx, `UPDATE mcpguard.principals SET can_read=true,scope_id='scope_b' WHERE id=$1`, p.ID)
		if _, e = admin.Exec(ctx, `UPDATE mcpguard.principals SET can_read=false WHERE id=$1`, p.ID); e != nil {
			t.Fatal(e)
		}
		list, e := clients["BOB"].ListTools(ctx, nil)
		if e != nil || len(list.Tools) != 0 {
			t.Fatal(list, e)
		}
		if rr := read(t, "BOB", "item_demo_1"); rr.Error.Code != "ACCESS_DENIED" {
			t.Fatal(rr)
		}
		if _, e = admin.Exec(ctx, `UPDATE mcpguard.principals SET can_read=true,scope_id='scope_a' WHERE id=$1`, p.ID); e != nil {
			t.Fatal(e)
		}
		rr := read(t, "BOB", "item_demo_1")
		if rr.Data.(map[string]any)["item"].(map[string]any)["note"] != "Alice scope A" {
			t.Fatal("stale scope reused")
		}
	})
	t.Run("native_role_is_select_only_even_without_readonly_setting", func(t *testing.T) {
		c, e := pgx.Connect(ctx, cfg.BusinessDSN)
		if e != nil {
			t.Fatal(e)
		}
		defer c.Close(ctx)
		if _, e = c.Exec(ctx, `SET default_transaction_read_only=off`); e != nil {
			t.Fatal(e)
		}
		_, e = c.Exec(ctx, `UPDATE demo.items SET note='forbidden' WHERE scope_id='scope_a'`)
		var pe *pgconn.PgError
		if !errors.As(e, &pe) || pe.Code != "42501" {
			t.Fatal("write permission not denied", e)
		}
		if unsafe, e := nativepg.Open(ctx, os.Getenv("MCPGUARD_TEST_BUSINESS_ADMIN_DSN")); e == nil {
			unsafe.Close()
			t.Fatal("native superuser accepted")
		}
		if unsafe, e := metadata.Open(ctx, adminDSN, v); e == nil {
			unsafe.Close()
			t.Fatal("metadata superuser accepted")
		}
	})
	t.Run("atomic_qualification_immutable_plan_and_consistent_states", func(t *testing.T) {
		ticket, e := s.Claim(ctx, principals["ALICE"], "item_demo_1")
		if e != nil {
			t.Fatal(e)
		}
		scope, item, e := s.ReadPlan(ticket)
		if e != nil || scope != "scope_a" || item != "item_demo_1" {
			t.Fatal(scope, item, e)
		}
		// Competing claims for this same Action cannot create another Execution.
		var wg sync.WaitGroup
		errs := make(chan error, 6)
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, e := s.Pool.Exec(ctx, `INSERT INTO mcpguard.executions(org_id,id,action_id,attempt_id,owner_instance,hard_deadline,dispatch_state,request_status) VALUES($1,$2,$3,$4,$5,$6,'INTENT_RECORDED','RUNNING')`, ticket.OrgID, uuid.NewString(), ticket.ActionID, ticket.AttemptID, s.Instance, ticket.Deadline)
				errs <- e
			}()
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			var pe *pgconn.PgError
			if !errors.As(e, &pe) || pe.Code != "23505" {
				t.Fatal("duplicate qualification not rejected", e)
			}
		}
		for _, sql := range []string{`UPDATE mcpguard.actions SET plan_hash=repeat('x',43) WHERE id=$1`, `DELETE FROM mcpguard.executions WHERE action_id=$1`, `UPDATE mcpguard.actions SET state='SUCCEEDED' WHERE id=$1`} {
			if _, e = admin.Exec(ctx, sql, ticket.ActionID); e == nil {
				t.Fatal("database invariant not enforced", sql)
			}
		}
		rr := failure("READ_CANCELLED", "CANCELLED", "Cancelled before native dispatch.")
		rr.ActionID = &ticket.ActionID
		rr.AttemptID = &ticket.AttemptID
		rr.ExecutionID = &ticket.ExecutionID
		rr.RequestStatus = "CANCELLED"
		b, _ := json.Marshal(rr)
		if e = s.Finish(ctx, ticket, "CANCELLED", "NOT_DISPATCHED", b); e != nil {
			t.Fatal(e)
		}
		if e = s.Finish(ctx, ticket, "CANCELLED", "NOT_DISPATCHED", b); e == nil {
			t.Fatal("terminal qualification was reused")
		}
		if _, e = admin.Exec(ctx, `UPDATE mcpguard.audit_events SET event_type='FAILED' WHERE action_id=$1`, ticket.ActionID); e == nil {
			t.Fatal("audit was rewritten")
		}
	})
	t.Run("audit_failure_rolls_back_qualification_before_native_access", func(t *testing.T) {
		var before, after int
		_ = admin.QueryRow(ctx, `SELECT count(*) FROM mcpguard.actions`).Scan(&before)
		acquires := r.Pool.Stat().AcquireCount()
		if _, e = admin.Exec(ctx, `REVOKE INSERT ON mcpguard.audit_events FROM mcpguard_app`); e != nil {
			t.Fatal(e)
		}
		defer admin.Exec(ctx, `GRANT INSERT ON mcpguard.audit_events TO mcpguard_app`)
		rr := read(t, "ALICE", "item_demo_1")
		if rr.RequestStatus != "FAILED" || rr.Data != nil || rr.Error.Code != "METADATA_UNAVAILABLE" {
			t.Fatal(rr)
		}
		_ = admin.QueryRow(ctx, `SELECT count(*) FROM mcpguard.actions`).Scan(&after)
		if before != after || r.Pool.Stat().AcquireCount() != acquires {
			t.Fatal("dispatch occurred without committed qualification and audit")
		}
	})
	t.Run("explicit_mcp_cancel_reaches_real_blocked_query", func(t *testing.T) {
		locker, e := pgx.Connect(ctx, os.Getenv("MCPGUARD_TEST_BUSINESS_ADMIN_DSN"))
		if e != nil {
			t.Fatal(e)
		}
		defer locker.Close(ctx)
		lock, e := locker.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer lock.Rollback(ctx)
		if _, e = lock.Exec(ctx, `LOCK TABLE demo.items IN ACCESS EXCLUSIVE MODE`); e != nil {
			t.Fatal(e)
		}
		c, ccancel := context.WithCancel(ctx)
		defer ccancel()
		done := make(chan error, 1)
		go func() {
			_, e := clients["ALICE"].CallTool(c, &mcp.CallToolParams{Name: "pg.get_demo_item", Arguments: map[string]any{"itemId": "item_demo_1"}})
			done <- e
		}()
		waitFor(t, 2*time.Second, func() bool {
			var n int
			_ = badmin.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE application_name='mcpguard-native-reader' AND wait_event_type='Lock' AND query LIKE 'SELECT item_id%'`).Scan(&n)
			return n == 1
		})
		var execID string
		if e = admin.QueryRow(ctx, `SELECT e.id::text FROM mcpguard.executions e JOIN mcpguard.actions a ON (a.org_id,a.id)=(e.org_id,e.action_id) WHERE e.request_status='RUNNING' AND a.principal_id=$1`, principals["ALICE"].ID).Scan(&execID); e != nil {
			t.Fatal(e)
		}
		quick, qcancel := context.WithTimeout(ctx, 500*time.Millisecond)
		_, e = clients["ALICE"].ListTools(quick, nil)
		qcancel()
		if e != nil {
			t.Fatal("blocked call blocked independent request", e)
		}
		ccancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("MCP cancellation did not finish")
		}
		var status, dispatch string
		var ciphertext []byte
		var action string
		waitFor(t, 4*time.Second, func() bool {
			e = admin.QueryRow(ctx, `SELECT request_status,dispatch_state,result_ciphertext,action_id::text FROM mcpguard.executions WHERE id=$1`, execID).Scan(&status, &dispatch, &ciphertext, &action)
			return e == nil && status != "RUNNING"
		})
		if status != "CANCELLED" || dispatch != "DISPATCHED" {
			t.Fatalf("cancel not confirmed by PG: %s/%s", status, dispatch)
		}
		plain, e := v.Open(ciphertext, principals["ALICE"].OrgID+"/"+action+"/RESULT/local-v1")
		if e != nil {
			t.Fatal(e)
		}
		var rr Result
		_ = json.Unmarshal(plain, &rr)
		validateResult(t, rr)
		if rr.Data != nil {
			t.Fatal("cancelled result leaked data")
		}
		waitFor(t, 2*time.Second, func() bool {
			var n int
			_ = badmin.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE application_name='mcpguard-native-reader' AND (wait_event_type='Lock' OR state LIKE 'idle in transaction%')`).Scan(&n)
			return n == 0 && r.Pool.Stat().AcquiredConns() == 0
		})
		if e = lock.Rollback(ctx); e != nil {
			t.Fatal(e)
		}
		if rr = read(t, "ALICE", "item_demo_1"); rr.RequestStatus != "SUCCEEDED" {
			t.Fatal("pool not reusable", rr)
		}
	})
	t.Run("bounded_native_timeout", func(t *testing.T) {
		lock, e := badmin.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer lock.Rollback(ctx)
		if _, e = lock.Exec(ctx, `LOCK TABLE demo.items IN ACCESS EXCLUSIVE MODE`); e != nil {
			t.Fatal(e)
		}
		start := time.Now()
		rr := read(t, "BOB", "item_demo_1")
		if rr.RequestStatus != "CANCELLED" || time.Since(start) > 5*time.Second || rr.Data != nil {
			t.Fatal("unbounded or incorrect timeout", rr)
		}
	})
	t.Run("audit_owner_filter_pagination_and_safe_fields", func(t *testing.T) {
		sets := map[string]map[string]bool{}
		for _, name := range []string{"ALICE", "BOB"} {
			p := principals[name]
			ev, e := s.Audit(ctx, p, 0, 100)
			if e != nil || len(ev) == 0 {
				t.Fatal(ev, e)
			}
			sets[name] = map[string]bool{}
			for _, a := range ev {
				if a.ActionID != nil {
					sets[name][*a.ActionID] = true
				}
			}
			hc := &http.Client{Transport: tokenTransport{tokens[name]}}
			resp, e := hc.Get(httpServer.URL + "/api/v1/audit?limit=1")
			if e != nil {
				t.Fatal(e)
			}
			var body map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			if resp.StatusCode != 200 || len(body["events"].([]any)) != 1 || body["nextAfter"].(float64) <= 0 {
				t.Fatal("bad audit pagination", body)
			}
			resp, e = hc.Get(httpServer.URL + "/api/v1/audit?principalId=" + principals["ALICE"].ID)
			if e != nil {
				t.Fatal(e)
			}
			resp.Body.Close()
			if resp.StatusCode != 400 {
				t.Fatal("audit accepts caller identity selector")
			}
			b, _ := json.Marshal(ev)
			if strings.Contains(string(b), tokens[name]) || strings.Contains(string(b), "scope A") {
				t.Fatal("audit disclosed secrets or raw data")
			}
		}
		for id := range sets["ALICE"] {
			if sets["BOB"][id] {
				t.Fatal("cross-employee audit leak")
			}
		}
	})
	t.Run("crash_left_claim_becomes_unknown_without_native_replay", func(t *testing.T) {
		// Insert a valid historical RUNNING fixture in one transaction. No old
		// worker exists; its original dispatch cannot be proven after the crash.
		p := principals["ALICE"]
		aid, atid, eid := uuid.NewString(), uuid.NewString(), uuid.NewString()
		plan := []byte(`{"fixture":"historical-read"}`)
		hash := v.Hash(plan)
		cipher := v.Seal(plan, p.OrgID+"/"+aid+"/PLAN/local-v1")
		tx, e := admin.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback(ctx)
		_, e = tx.Exec(ctx, `INSERT INTO mcpguard.actions(org_id,id,principal_id,tool_name,tool_version,scope_id,auth_epoch,plan_hash,plan_ciphertext,key_version,authorization_source,state) VALUES($1,$2,$3,'pg.get_demo_item','1',$4,1,$5,$6,'local-v1','POLICY','EXECUTING')`, p.OrgID, aid, p.ID, p.ScopeID, hash, cipher)
		if e != nil {
			t.Fatal(e)
		}
		_, e = tx.Exec(ctx, `INSERT INTO mcpguard.attempts VALUES($1,$2,$3,'2025-11-25','RUNNING')`, p.OrgID, atid, aid)
		if e != nil {
			t.Fatal(e)
		}
		_, e = tx.Exec(ctx, `INSERT INTO mcpguard.executions(org_id,id,action_id,attempt_id,owner_instance,hard_deadline,dispatch_state,request_status) VALUES($1,$2,$3,$4,$5,clock_timestamp()-interval '1 minute','INTENT_RECORDED','RUNNING')`, p.OrgID, eid, aid, atid, uuid.NewString())
		if e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
		acquires := r.Pool.Stat().AcquireCount()
		c, ccancel := context.WithCancel(ctx)
		defer ccancel()
		done := make(chan struct{})
		go func() { g.RunRecovery(c); close(done) }()
		waitFor(t, 2*time.Second, func() bool {
			var status string
			_ = admin.QueryRow(ctx, `SELECT request_status FROM mcpguard.executions WHERE id=$1`, eid).Scan(&status)
			return status == "OUTCOME_UNKNOWN"
		})
		ccancel()
		<-done
		if r.Pool.Stat().AcquireCount() != acquires {
			t.Fatal("recovery replayed a native operation")
		}
		var n int
		_ = admin.QueryRow(ctx, `SELECT count(*) FROM mcpguard.executions WHERE action_id=$1`, aid).Scan(&n)
		if n != 1 {
			t.Fatal("execution qualification changed")
		}
	})
}

func waitFor(t *testing.T, d time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal(fmt.Sprintf("condition not reached within %s", d))
}
