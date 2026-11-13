package controlled_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xielforever/MCPGuard/internal/controlled"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"github.com/xielforever/MCPGuard/internal/runtimeconfig"
)

type fixture struct {
	ctx             context.Context
	s               *controlled.Service
	store           *metadata.Store
	native          *controlled.Adapters
	admin, business *pgx.Conn
	p, q, reviewer  metadata.Principal
	tokens          map[string]string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	if os.Getenv("MCPGUARD_NATIVE_CONFIG") == "" || os.Getenv("MCPGUARD_TEST_METADATA_ADMIN_DSN") == "" {
		t.Skip("isolated controlled fixture required")
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
	t.Cleanup(store.Close)
	admin, e := pgx.Connect(ctx, os.Getenv("MCPGUARD_TEST_METADATA_ADMIN_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { admin.Close(ctx) })
	business, e := pgx.Connect(ctx, os.Getenv("MCPGUARD_TEST_BUSINESS_ADMIN_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { business.Close(ctx) })
	org := uuid.NewString()
	_, e = admin.Exec(ctx, `INSERT INTO mcpguard.authorization_heads(org_id) VALUES($1);`, org)
	if e != nil {
		t.Fatal(e)
	}
	_, e = admin.Exec(ctx, `INSERT INTO mcpguard.control_heads(org_id) VALUES($1)`, org)
	if e != nil {
		t.Fatal(e)
	}
	tokens := map[string]string{}
	var ps []metadata.Principal
	for i, name := range []string{"alice", "bob", "reviewer"} {
		id := uuid.NewString()
		token := uuid.NewString() + uuid.NewString()
		tokens[name] = token
		hash := sha256.Sum256([]byte(token))
		scope := "t_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
		can := i != 2
		_, e = admin.Exec(ctx, `INSERT INTO mcpguard.principals(org_id,id,display_name,token_hash,expires_at,can_read,scope_id) VALUES($1,$2,$3,$4,clock_timestamp()+interval '1 day',$5,$6)`, org, id, name, hash[:], can, scope)
		if e != nil {
			t.Fatal(e)
		}
		p, e := store.VerifyToken(ctx, token)
		if e != nil {
			t.Fatal(e)
		}
		ps = append(ps, p)
		if i != 2 {
			_, e = business.Exec(ctx, `INSERT INTO demo.items(scope_id,item_id,note,version) VALUES($1,'item_demo_1',$2,1)`, scope, name+" original")
			if e != nil {
				t.Fatal(e)
			}
			_, e = business.Exec(ctx, `INSERT INTO demo.offices VALUES($1,'office-demo',$2);`, scope, name+" office")
			if e != nil {
				t.Fatal(e)
			}
			_, e = business.Exec(ctx, `INSERT INTO demo.stock VALUES($1,'item_demo_1',100)`, scope)
			if e != nil {
				t.Fatal(e)
			}
		}
	}
	for _, resource := range []string{"demo.pg", "demo.redis", "demo.directory", "demo.inventory"} {
		_, e = admin.Exec(ctx, `INSERT INTO mcpguard.control_resources VALUES($1,$2,$3,true)`, org, resource, uuid.NewString())
		if e != nil {
			t.Fatal(e)
		}
		for _, review := range []metadata.Principal{ps[0], ps[2]} {
			_, e = admin.Exec(ctx, `INSERT INTO mcpguard.reviewer_grants VALUES($1,$2,$3,true)`, org, review.ID, resource)
			if e != nil {
				t.Fatal(e)
			}
		}
	}
	for _, p := range ps[:2] {
		for _, tool := range []string{"pg.update_demo_note", "redis.get_demo_note", "redis.set_demo_note", "directory.get_demo_office", "inventory.reserve_demo"} {
			_, e = admin.Exec(ctx, `INSERT INTO mcpguard.tool_grants VALUES($1,$2,$3,true)`, org, p.ID, tool)
			if e != nil {
				t.Fatal(e)
			}
		}
	}
	for _, p := range []metadata.Principal{ps[0], ps[2]} {
		_, e = admin.Exec(ctx, `INSERT INTO mcpguard.control_members VALUES($1,$2,true)`, org, p.ID)
		if e != nil {
			t.Fatal(e)
		}
	}
	native, e := controlled.OpenAdapters(ctx, cfg.NativeConfig)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(native.Close)
	s, e := controlled.New(store, native, "http://127.0.0.1:18080")
	if e != nil {
		t.Fatal(e)
	}
	return &fixture{ctx, s, store, native, admin, business, ps[0], ps[1], ps[2], tokens}
}
func create(t *testing.T, f *fixture, tool string, args any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(args)
	r, e := f.s.Create(f.ctx, f.p, tool, b)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func approve(t *testing.T, f *fixture, id string) {
	t.Helper()
	v, e := f.s.Review(f.ctx, f.reviewer, id)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.s.Decide(f.ctx, f.reviewer, id, "APPROVE", v["planHash"].(string), fmt.Sprintf(`"%v"`, v["revision"]), uuid.NewString())
	if e != nil {
		t.Fatal(e)
	}
}
func result(t *testing.T, f *fixture, id string) map[string]any {
	t.Helper()
	r, e := f.s.Control(f.ctx, f.p, id, "RESUME")
	if e != nil {
		t.Fatal(e)
	}
	v, ok := r["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", r)
	}
	return v
}
func TestControlledIntegration(t *testing.T) {
	f := setup(t)
	t.Run("approval-authentication-and-single-execution", func(t *testing.T) {
		r := create(t, f, "pg.update_demo_note", map[string]any{"itemId": "item_demo_1", "note": "approved value", "expectedVersion": "1"})
		id := r["actionId"].(string)
		if r["kind"] != "DEFERRED" {
			t.Fatal(r)
		}
		if _, e := f.s.Control(f.ctx, f.q, id, "STATUS"); !errors.Is(e, controlled.ErrDenied) {
			t.Fatal("cross owner status", e)
		}
		if _, e := f.s.Result(f.ctx, f.q, id); !errors.Is(e, controlled.ErrDenied) {
			t.Fatal("cross owner result", e)
		}
		v, e := f.s.Review(f.ctx, f.p, id)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.s.Decide(f.ctx, f.p, id, "APPROVE", v["planHash"].(string), `"1"`, uuid.NewString()); !errors.Is(e, controlled.ErrDenied) {
			t.Fatal("admin self approval", e)
		}
		before, e := f.s.Control(f.ctx, f.p, id, "RESUME")
		if e != nil || before["outcome"] != "APPROVAL_PENDING" {
			t.Fatal(before, e)
		}
		v, e = f.s.Review(f.ctx, f.reviewer, id)
		if e != nil {
			t.Fatal(e)
		}
		key := uuid.NewString()
		first, e := f.s.Decide(f.ctx, f.reviewer, id, "APPROVE", v["planHash"].(string), `"1"`, key)
		if e != nil {
			t.Fatal(e)
		}
		again, e := f.s.Decide(f.ctx, f.reviewer, id, "APPROVE", v["planHash"].(string), `"1"`, key)
		if e != nil || fmt.Sprint(first) != fmt.Sprint(again) {
			t.Fatal("idempotent decision", e)
		}
		if _, e = f.s.Decide(f.ctx, f.reviewer, id, "REJECT", v["planHash"].(string), `"1"`, key); !errors.Is(e, controlled.ErrConflict) {
			t.Fatal("key collision", e)
		}
		var count int
		if e = f.admin.QueryRow(f.ctx, `SELECT count(*) FROM mcpguard.managed_executions WHERE org_id=$1 AND action_id=$2`, f.p.OrgID, id).Scan(&count); e != nil || count != 0 {
			t.Fatal("approval dispatched", count, e)
		}
		var wg sync.WaitGroup
		errs := make(chan error, 12)
		for range 12 {
			wg.Add(1)
			go func() { defer wg.Done(); _, e := f.s.Control(f.ctx, f.p, id, "RESUME"); errs <- e }()
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			if e != nil {
				t.Error(e)
			}
		}
		var note, version string
		if e = f.business.QueryRow(f.ctx, `SELECT note,version::text FROM demo.items WHERE scope_id=$1 AND item_id='item_demo_1'`, f.p.ScopeID).Scan(&note, &version); e != nil || note != "approved value" || version != "2" {
			t.Fatal(note, version, e)
		}
		if e = f.admin.QueryRow(f.ctx, `SELECT count(*) FROM mcpguard.managed_executions WHERE org_id=$1 AND action_id=$2`, f.p.OrgID, id).Scan(&count); e != nil || count != 1 {
			t.Fatal(count, e)
		}
		for range 3 {
			r := result(t, f, id)
			if r["requestStatus"] != "SUCCEEDED" {
				t.Fatal(r)
			}
		}
	})
	t.Run("optimistic-version", func(t *testing.T) {
		r := create(t, f, "pg.update_demo_note", map[string]any{"itemId": "item_demo_1", "note": "stale", "expectedVersion": "1"})
		id := r["actionId"].(string)
		approve(t, f, id)
		v := result(t, f, id)
		if v["requestStatus"] != "FAILED" || v["sideEffectStatus"] != "NONE" {
			t.Fatal(v)
		}
	})
	t.Run("redis-fixed-namespace-and-ttl", func(t *testing.T) {
		r := create(t, f, "redis.set_demo_note", map[string]any{"noteId": "welcome", "value": "hello scoped Redis", "ttlSeconds": 60})
		id := r["actionId"].(string)
		approve(t, f, id)
		v := result(t, f, id)
		if v["requestStatus"] != "SUCCEEDED" {
			t.Fatal(v)
		}
		read := create(t, f, "redis.get_demo_note", map[string]any{"noteId": "welcome"})
		if read["requestStatus"] != "SUCCEEDED" || read["data"].(map[string]any)["value"] != "hello scoped Redis" {
			t.Fatal(read)
		}
		raw := []byte(`{"noteId":"welcome"}`)
		bob, e := f.s.Create(f.ctx, f.q, "redis.get_demo_note", raw)
		if e != nil || bob["data"].(map[string]any)["found"] != false {
			t.Fatal("namespace leak", bob, e)
		}
	})
	t.Run("http-fixed-operations", func(t *testing.T) {
		read := create(t, f, "directory.get_demo_office", map[string]any{"officeId": "office-demo"})
		if read["requestStatus"] != "SUCCEEDED" || read["data"].(map[string]any)["displayName"] != "alice office" {
			t.Fatal(read)
		}
		r := create(t, f, "inventory.reserve_demo", map[string]any{"itemId": "item_demo_1", "quantity": 2})
		id := r["actionId"].(string)
		approve(t, f, id)
		v := result(t, f, id)
		if v["requestStatus"] != "SUCCEEDED" {
			t.Fatal(v)
		}
		var available int
		if e := f.business.QueryRow(f.ctx, `SELECT available FROM demo.stock WHERE scope_id=$1 AND item_id='item_demo_1'`, f.p.ScopeID).Scan(&available); e != nil || available != 98 {
			t.Fatal(available, e)
		}
	})
	t.Run("reject-cancel-expire", func(t *testing.T) {
		for _, operation := range []string{"reject", "cancel", "expire"} {
			r := create(t, f, "redis.set_demo_note", map[string]any{"noteId": "help", "value": operation, "ttlSeconds": 60})
			id := r["actionId"].(string)
			switch operation {
			case "reject":
				v, e := f.s.Review(f.ctx, f.reviewer, id)
				if e != nil {
					t.Fatal(e)
				}
				_, e = f.s.Decide(f.ctx, f.reviewer, id, "REJECT", v["planHash"].(string), `"1"`, uuid.NewString())
				if e != nil {
					t.Fatal(e)
				}
			case "cancel":
				_, e := f.s.Control(f.ctx, f.p, id, "CANCEL")
				if e != nil {
					t.Fatal(e)
				}
			case "expire": // Use a transaction-local database trigger pause only in the isolated admin fixture.
				_, e := f.admin.Exec(f.ctx, `ALTER TABLE mcpguard.managed_actions DISABLE TRIGGER managed_action_guard`)
				if e != nil {
					t.Fatal(e)
				}
				_, e = f.admin.Exec(f.ctx, `UPDATE mcpguard.managed_actions SET expires_at=clock_timestamp()-interval '1 second' WHERE org_id=$1 AND id=$2`, f.p.OrgID, id)
				_, enable := f.admin.Exec(f.ctx, `ALTER TABLE mcpguard.managed_actions ENABLE TRIGGER managed_action_guard`)
				if e != nil || enable != nil {
					t.Fatal(e, enable)
				}
			}
			v, e := f.s.Control(f.ctx, f.p, id, "RESUME")
			if e != nil || v["outcome"] != "NOT_RESUMABLE" {
				t.Fatal(v, e)
			}
		}
	})
	t.Run("config-and-grant-revocation", func(t *testing.T) {
		r := create(t, f, "redis.set_demo_note", map[string]any{"noteId": "help", "value": "never dispatch", "ttlSeconds": 60})
		id := r["actionId"].(string)
		approve(t, f, id)
		_, e := f.admin.Exec(f.ctx, `UPDATE mcpguard.tool_grants SET allowed=false WHERE org_id=$1 AND principal_id=$2 AND tool_name='redis.set_demo_note'`, f.p.OrgID, f.p.ID)
		if e != nil {
			t.Fatal(e)
		}
		v, e := f.s.Control(f.ctx, f.p, id, "RESUME")
		if e != nil || v["outcome"] != "NOT_RESUMABLE" {
			t.Fatal(v, e)
		}
		_, e = f.admin.Exec(f.ctx, `UPDATE mcpguard.tool_grants SET allowed=true WHERE org_id=$1 AND principal_id=$2 AND tool_name='redis.set_demo_note'`, f.p.OrgID, f.p.ID)
		if e != nil {
			t.Fatal(e)
		}
	})
	t.Run("deny-wins-and-allow-never-skips-write-approval", func(t *testing.T) {
		id := uuid.NewString()
		_, e := f.admin.Exec(f.ctx, `INSERT INTO mcpguard.control_rules VALUES($1,$2,'redis.set_demo_note','ALLOW','',true)`, f.p.OrgID, id)
		if e != nil {
			t.Fatal(e)
		}
		r := create(t, f, "redis.set_demo_note", map[string]any{"noteId": "help", "value": "allow still pending", "ttlSeconds": 60})
		if r["kind"] != "DEFERRED" {
			t.Fatal(r)
		}
		deny := uuid.NewString()
		_, e = f.admin.Exec(f.ctx, `INSERT INTO mcpguard.control_rules VALUES($1,$2,'redis.set_demo_note','DENY','',true)`, f.p.OrgID, deny)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.s.Create(f.ctx, f.p, "redis.set_demo_note", []byte(`{"noteId":"help","value":"blocked","ttlSeconds":60}`)); !errors.Is(e, controlled.ErrDenied) {
			t.Fatal(e)
		}
		_, e = f.admin.Exec(f.ctx, `DELETE FROM mcpguard.control_rules WHERE org_id=$1`, f.p.OrgID)
		if e != nil {
			t.Fatal(e)
		}
	})
	t.Run("configuration-idempotency-before-revision", func(t *testing.T) {
		snap, e := f.s.Snapshot(f.ctx, f.reviewer)
		if e != nil {
			t.Fatal(e)
		}
		payload := map[string]any{"kind": "resource", "resource": "demo.directory", "enabled": false}
		match := `"` + snap["revision"].(string) + `"`
		key := uuid.NewString()
		first, e := f.s.Configure(f.ctx, f.reviewer, payload, match, key)
		if e != nil {
			t.Fatal(e)
		}
		again, e := f.s.Configure(f.ctx, f.reviewer, payload, match, key)
		if e != nil || fmt.Sprint(again) != fmt.Sprint(first) {
			t.Fatal("replay rejected after revision advance", e)
		}
		if _, e = f.s.Configure(f.ctx, f.reviewer, payload, match, uuid.NewString()); !errors.Is(e, controlled.ErrConflict) {
			t.Fatal(e)
		}
		snap, e = f.s.Snapshot(f.ctx, f.reviewer)
		if e != nil {
			t.Fatal(e)
		}
		payload["enabled"] = true
		if _, e = f.s.Configure(f.ctx, f.reviewer, payload, `"`+snap["revision"].(string)+`"`, uuid.NewString()); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("owner-audit-and-history", func(t *testing.T) {
		a, e := f.store.Audit(f.ctx, f.p, 0, 100)
		if e != nil || len(a) == 0 {
			t.Fatal(e)
		}
		b, e := f.store.Audit(f.ctx, f.q, 0, 100)
		if e != nil {
			t.Fatal(e)
		}
		seen := map[int64]bool{}
		for _, event := range a {
			seen[event.Seq] = true
		}
		for _, event := range b {
			if seen[event.Seq] {
				t.Fatal("cross principal audit")
			}
		}
		first, e := f.s.History(f.ctx, f.p, "", 1)
		if e != nil || len(first["actions"].([]any)) != 1 {
			t.Fatal(first, e)
		}
		if _, e = f.s.History(f.ctx, f.p, first["nextAfter"].(string), 100); e != nil {
			t.Fatal(e)
		}
		if _, e = f.s.History(f.ctx, f.p, "", 101); !errors.Is(e, controlled.ErrInput) {
			t.Fatal(e)
		}
	})
	t.Run("database-guards", func(t *testing.T) {
		r := create(t, f, "redis.set_demo_note", map[string]any{"noteId": "help", "value": "guard", "ttlSeconds": 60})
		id := r["actionId"].(string)
		if _, e := f.admin.Exec(f.ctx, `UPDATE mcpguard.managed_actions SET scope_id='scope_b' WHERE org_id=$1 AND id=$2`, f.p.OrgID, id); e == nil {
			t.Fatal("plan mutable")
		}
		if _, e := f.admin.Exec(f.ctx, `UPDATE mcpguard.managed_actions SET state='READY',revision=revision+1 WHERE org_id=$1 AND id=$2`, f.p.OrgID, id); e == nil {
			t.Fatal("missing approval accepted")
		}
		if _, e := f.store.Pool.Exec(f.ctx, `DELETE FROM mcpguard.managed_audit WHERE org_id=$1`, f.p.OrgID); e == nil {
			t.Fatal("audit deletable")
		}
	})
}

type uncertain struct{ calls atomic.Int32 }

func (*uncertain) Available(string) bool { return true }
func (*uncertain) Binding() string       { return "fault-test" }
func (n *uncertain) Execute(context.Context, controlled.Tool, string, map[string]any) controlled.Outcome {
	n.calls.Add(1)
	return controlled.Outcome{Dispatch: "DISPATCH_UNKNOWN", SideEffect: "UNKNOWN", Code: "OUTCOME_UNKNOWN"}
}
func TestUnknownNeverReplays(t *testing.T) {
	f := setup(t)
	fake := &uncertain{}
	s, e := controlled.New(f.store, fake, f.s.Origin)
	if e != nil {
		t.Fatal(e)
	}
	f.s = s
	r := create(t, f, "redis.set_demo_note", map[string]any{"noteId": "welcome", "value": "unknown", "ttlSeconds": 60})
	id := r["actionId"].(string)
	approve(t, f, id)
	v := result(t, f, id)
	if v["requestStatus"] != "OUTCOME_UNKNOWN" {
		t.Fatal(v)
	}
	for range 3 {
		result(t, f, id)
		if e = s.Recover(f.ctx); e != nil {
			t.Fatal(e)
		}
	}
	if fake.calls.Load() != 1 {
		t.Fatal("unknown replayed")
	}
}
func TestConsoleAuthentication(t *testing.T) {
	f := setup(t)
	cfg := t.TempDir() + "/console.json"
	hash := sha256.Sum256([]byte(f.tokens["reviewer"]))
	b, _ := json.Marshal([]controlled.Login{{Username: "reviewer", OrgID: f.reviewer.OrgID, PrincipalID: f.reviewer.ID, PasswordHash: hex.EncodeToString(hash[:])}})
	if e := os.WriteFile(cfg, b, 0600); e != nil {
		t.Fatal(e)
	}
	console, e := controlled.OpenConsole(f.s, cfg)
	if e != nil {
		t.Fatal(e)
	}
	mux := http.NewServeMux()
	console.Register(mux)
	request := func(method, path, body string, cookie *http.Cookie, csrf, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:18080"+path, strings.NewReader(body))
		if cookie != nil {
			r.AddCookie(cookie)
		}
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/console/api/state", "", nil, "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	login, _ := json.Marshal(map[string]string{"username": "reviewer", "password": f.tokens["reviewer"]})
	if w := request("POST", "/console/login", string(login), nil, "", "http://evil.invalid"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	w := request("POST", "/console/login", string(login), nil, "", f.s.Origin)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe cookie")
	}
	var session map[string]any
	if e = json.Unmarshal(w.Body.Bytes(), &session); e != nil {
		t.Fatal(e)
	}
	cookie := cookies[0]
	csrf := session["csrf"].(string)
	if w = request("GET", "/console/api/state", "", cookie, "", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w = request("POST", "/console/api/logout", "{}", cookie, "", f.s.Origin); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w = request("POST", "/console/api/logout", "{}", cookie, csrf, f.s.Origin); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w = request("GET", "/console/api/state", "", cookie, "", ""); w.Code != 401 {
		t.Fatal("logout failed", w.Code)
	}
}
func TestTelegramOutbox(t *testing.T) {
	f := setup(t)
	var calls atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Error(e)
		}
		text, _ := body["text"].(string)
		if strings.Contains(text, "never leak payload") {
			t.Error("notification leaked arguments")
		}
		if calls.Add(1) == 1 {
			http.Error(w, "temporary", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer receiver.Close()
	path := t.TempDir() + "/telegram.json"
	raw, _ := json.Marshal(controlled.NotificationConfig{OrgID: f.p.OrgID, BotToken: "test-only", ChatID: "local-reviewer", TestOrigin: receiver.URL})
	if e := os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	n, e := controlled.OpenNotifier(f.s, path)
	if e != nil {
		t.Fatal(e)
	}
	r := create(t, f, "redis.set_demo_note", map[string]any{"noteId": "help", "value": "never leak payload", "ttlSeconds": 60})
	id := r["actionId"].(string)
	if e = n.Deliver(f.ctx); e != nil {
		t.Fatal(e)
	}
	var state string
	var attempts int
	if e = f.admin.QueryRow(f.ctx, `SELECT state,attempts FROM mcpguard.notification_outbox WHERE org_id=$1 AND action_id=$2`, f.p.OrgID, id).Scan(&state, &attempts); e != nil || state != "PENDING" || attempts != 1 {
		t.Fatal(state, attempts, e)
	}
	_, e = f.admin.Exec(f.ctx, `UPDATE mcpguard.notification_outbox SET next_at=clock_timestamp() WHERE org_id=$1 AND action_id=$2`, f.p.OrgID, id)
	if e != nil {
		t.Fatal(e)
	}
	if e = n.Deliver(f.ctx); e != nil {
		t.Fatal(e)
	}
	if e = f.admin.QueryRow(f.ctx, `SELECT state FROM mcpguard.notification_outbox WHERE org_id=$1 AND action_id=$2`, f.p.OrgID, id).Scan(&state); e != nil || state != "SENT" || calls.Load() != 2 {
		t.Fatal(state, calls.Load(), e)
	}
}
