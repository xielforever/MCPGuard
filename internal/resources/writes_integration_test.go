package resources

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

// newWriteFixture binds the write service to the context runtime, like RA-11.
func newWriteFixture(t *testing.T) (*contextFixture, *WriteService) {
	t.Helper()
	requireWriteScene(t)
	f := newContextFixture(t)
	store, e := metadata.Open(f.ctx, os.Getenv("MCPGUARD_TEST_CONTEXT_DSN"), f.s.Catalog.Store.Vault)
	if e != nil {
		t.Fatal("context runtime unavailable")
	}
	t.Cleanup(store.Close)
	catalog, e := NewCatalog(f.ctx, store)
	if e != nil {
		t.Fatal(e)
	}
	credentials, e := NewCredentialService(catalog, f.keys)
	if e != nil {
		t.Fatal(e)
	}
	manager := NewReadManager(catalog, &VaultSecretProvider{Catalog: catalog, Endpoints: f.private.Endpoints})
	t.Cleanup(manager.Close)
	write := NewWriteService(catalog, credentials, manager)
	pool, e := pgxpool.New(f.ctx, os.Getenv("MCPGUARD_TEST_METADATA_ADMIN_DSN"))
	if e == nil {
		t.Cleanup(pool.Close)
		write.Fence = NewRestoreFence(pool, &PGProvisioningProvider{Private: f.private})
	}
	return f, write
}

// requireWriteScene skips the write tests unless the RA-12 fixture prepared them.
func requireWriteScene(t *testing.T) {
	t.Helper()
	raw, e := os.ReadFile(os.Getenv("MCPGUARD_RA_CONFIG"))
	var scene struct {
		Write bool `json:"raWritePhase"`
	}
	if e != nil || json.Unmarshal(raw, &scene) != nil || !scene.Write {
		t.Skip("explicit RA-12 write fixture required")
	}
}

func writeProfileID(t *testing.T, f *contextFixture) string {
	t.Helper()
	for _, profile := range f.bundle.Profiles {
		if profile.Name == "isolated-write" {
			return profile.ProfileID
		}
	}
	t.Fatal("no write profile in the fixture bundle")
	return ""
}

// writeApprover registers a reviewer on the write runtime and returns it.
func writeApprover(t *testing.T, f *contextFixture, writes *WriteService, resourceID string) metadata.Principal {
	t.Helper()
	alice := f.people["alice"]
	var approver metadata.Principal
	for _, name := range []string{"visitor", "reviewer", "bob"} {
		if p, ok := f.people[name]; ok && p.ID != alice.ID {
			approver = p
			break
		}
	}
	if approver.ID == "" {
		t.Fatal("no approver principal in the fixture")
	}
	const seed = `INSERT INTO mcpguard_ra.access_reviewers(org_id,resource_id,principal_id,can_approve) VALUES($1,$2,$3,true) ON CONFLICT (org_id,resource_id,principal_id) DO UPDATE SET can_approve=true`
	if _, e := writes.Catalog.Store.Pool.Exec(f.ctx, seed, alice.OrgID, resourceID, approver.ID); e != nil {
		if _, e2 := f.admin.Exec(f.ctx, seed, alice.OrgID, resourceID, approver.ID); e2 != nil {
			t.Fatal("could not seed the write approver: ", e2)
		}
	}
	return approver
}

// activateWrite requests the write profile, approves it, provisions the write
// identity and issues the write credential.
func activateWrite(t *testing.T, f *contextFixture, writes *WriteService) v02.PrepareDownloadResult {
	t.Helper()
	input := f.input()
	input.ProfileID = writeProfileID(t, f)
	r, e := f.s.Request(f.ctx, f.people["alice"], input)
	if e != nil {
		t.Fatal("write access request: ", e)
	}
	out, e := f.s.Decide(f.ctx, writeApprover(t, f, writes, input.ResourceID), r.RequestID, approveInput(r))
	if e != nil {
		t.Fatal("write approval: ", e)
	}
	f.run(t, out, "VERIFIED")
	status, e := f.service.Issue(f.ctx, f.people["alice"], v02.IssueCredentialInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()})
	if e != nil {
		t.Fatal("write issue: ", e)
	}
	resource := input.ResourceID
	return v02.PrepareDownloadResult{CredentialID: status.CredentialID, ResourceID: &resource}
}

// seedWriteItem inserts one row owned by the provisioned write identity.
func seedWriteItem(t *testing.T, f *contextFixture, resourceID string) {
	t.Helper()
	profile := writeProfileID(t, f)
	alice := f.people["alice"]
	var identity string
	e := f.s.Catalog.Store.Pool.QueryRow(f.ctx, `SELECT definition->>'nativeIdentity' FROM mcpguard_ra.identity_bindings WHERE org_id=$1 AND principal_id=$2 AND resource_id=$3 AND profile_id=$4 ORDER BY binding_version DESC LIMIT 1`, alice.OrgID, alice.ID, resourceID, profile).Scan(&identity)
	if e != nil {
		t.Fatal("write binding unavailable: ", e)
	}
	for _, conn := range f.native {
		if _, e := conn.Exec(f.ctx, "INSERT INTO resource_demo.items VALUES($1,'item_demo_1','write seed',1) ON CONFLICT DO NOTHING", identity); e != nil {
			t.Fatal("could not seed the write item: ", e)
		}
	}
}

func writeInput(ticket v02.PrepareDownloadResult, note, expected string) v02.UpdateNoteInput {
	return v02.UpdateNoteInput{ResourceID: *ticket.ResourceID, CredentialID: ticket.CredentialID, ItemID: "item_demo_1", Note: note, ExpectedVersion: v02.Version(expected)}
}

func TestControlledWriteNeedsAnotherApprover(t *testing.T) {
	f, writes := newWriteFixture(t)
	ticket := activateWrite(t, f, writes)
	seedWriteItem(t, f, *ticket.ResourceID)

	approver := writeApprover(t, f, writes, *ticket.ResourceID)
	planned, e := writes.Plan(f.ctx, f.people["alice"], writeInput(ticket, "approved note", "1"), uuid.NewString())
	if e != nil || planned.State != "DEFERRED" {
		t.Fatal("plan was not deferred: ", e, planned.State)
	}
	if _, e := writes.Approve(f.ctx, f.people["alice"], planned.RequestID, true, ""); !errors.Is(e, ErrDenied) {
		t.Fatal("self approval was accepted: ", e)
	}
	if _, e := writes.Resume(f.ctx, f.people["alice"], planned.RequestID); !errors.Is(e, ErrDenied) {
		t.Fatal("an unapproved plan was dispatched: ", e)
	}
	approved, e := writes.Approve(f.ctx, approver, planned.RequestID, true, "reviewed")
	if e != nil || approved.State != "APPROVED" {
		t.Fatal("reviewer could not approve: ", e, approved.State)
	}
	done, e := writes.Resume(f.ctx, f.people["alice"], planned.RequestID)
	if e != nil {
		t.Fatal("resume failed: ", e)
	}
	if done.State != "SUCCEEDED" || done.Result == nil || done.Result.Outcome != "UPDATED" || done.Result.NewVersion != "2" {
		t.Fatalf("write did not complete: state=%s result=%+v", done.State, done.Result)
	}
	if _, e := writes.Resume(f.ctx, f.people["alice"], planned.RequestID); !errors.Is(e, ErrDenied) {
		t.Fatal("the same plan was dispatched twice: ", e)
	}
	alice := f.people["alice"]
	var events int
	if e := f.s.Catalog.Store.Pool.QueryRow(f.ctx, `SELECT count(DISTINCT event_type) FROM mcpguard_ra.write_audit WHERE org_id=$1 AND request_id=$2`, alice.OrgID, planned.RequestID).Scan(&events); e != nil || events < 3 {
		t.Fatalf("write audit is not correlated to the plan: events=%d err=%v", events, e)
	}
	var state string
	if e := f.s.Catalog.Store.Pool.QueryRow(f.ctx, `SELECT state FROM mcpguard_ra.write_requests WHERE org_id=$1 AND request_id=$2`, alice.OrgID, planned.RequestID).Scan(&state); e != nil || state != "SUCCEEDED" {
		t.Fatalf("write result is not correlated to the plan: state=%q err=%v", state, e)
	}
}

// TestControlledWriteRejectsBindingChange proves a change after approval makes
// the frozen plan stale (RA-AC-15).
func TestControlledWriteRejectsBindingChange(t *testing.T) {
	f, writes := newWriteFixture(t)
	ticket := activateWrite(t, f, writes)
	seedWriteItem(t, f, *ticket.ResourceID)
	approver := writeApprover(t, f, writes, *ticket.ResourceID)
	planned, e := writes.Plan(f.ctx, f.people["alice"], writeInput(ticket, "binding change", "1"), uuid.NewString())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = writes.Approve(f.ctx, approver, planned.RequestID, true, ""); e != nil {
		t.Fatal(e)
	}
	alice := f.people["alice"]
	if _, e = f.admin.Exec(f.ctx, `UPDATE mcpguard_ra.identity_bindings SET enabled=false WHERE org_id=$1 AND principal_id=$2 AND resource_id=$3 AND profile_id=$4`, alice.OrgID, alice.ID, *ticket.ResourceID, writeProfileID(t, f)); e != nil {
		t.Fatal("could not disable the binding: ", e)
	}
	if _, e := writes.Resume(f.ctx, f.people["alice"], planned.RequestID); !errors.Is(e, ErrDenied) {
		t.Fatal("a changed binding still dispatched the write: ", e)
	}
}

func TestControlledWriteRejectsVersionConflict(t *testing.T) {
	f, writes := newWriteFixture(t)
	ticket := activateWrite(t, f, writes)
	seedWriteItem(t, f, *ticket.ResourceID)
	approver := writeApprover(t, f, writes, *ticket.ResourceID)

	first, e := writes.Plan(f.ctx, f.people["alice"], writeInput(ticket, "first", "1"), uuid.NewString())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = writes.Approve(f.ctx, approver, first.RequestID, true, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = writes.Resume(f.ctx, f.people["alice"], first.RequestID); e != nil {
		t.Fatal(e)
	}
	stale, e := writes.Plan(f.ctx, f.people["alice"], writeInput(ticket, "stale", "1"), uuid.NewString())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = writes.Approve(f.ctx, approver, stale.RequestID, true, ""); e != nil {
		t.Fatal(e)
	}
	out, e := writes.Resume(f.ctx, f.people["alice"], stale.RequestID)
	if e != nil {
		t.Fatal(e)
	}
	if out.State != "FAILED" || out.Result == nil || out.Result.Outcome != "VERSION_CONFLICT" {
		t.Fatalf("stale version was not rejected: state=%s result=%+v", out.State, out.Result)
	}
}

func TestControlledWriteRejectsRevokedCredential(t *testing.T) {
	f, writes := newWriteFixture(t)
	ticket := activateWrite(t, f, writes)
	seedWriteItem(t, f, *ticket.ResourceID)
	approver := writeApprover(t, f, writes, *ticket.ResourceID)
	planned, e := writes.Plan(f.ctx, f.people["alice"], writeInput(ticket, "before revoke", "1"), uuid.NewString())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.Revoke(f.ctx, f.people["alice"], v02.RevokeCredentialInput{CredentialID: ticket.CredentialID, ExpectedRevision: "1", RequestKey: uuid.NewString(), Reason: "write approval revoked credential"}); e != nil {
		t.Fatal(e)
	}
	if _, e = writes.Approve(f.ctx, approver, planned.RequestID, true, ""); e != nil {
		t.Fatal(e)
	}
	if _, e := writes.Resume(f.ctx, f.people["alice"], planned.RequestID); !errors.Is(e, ErrDenied) {
		t.Fatal("revoked credential still dispatched a write: ", e)
	}
}

func TestControlledWriteRejectsNonReviewer(t *testing.T) {
	f, writes := newWriteFixture(t)
	ticket := activateWrite(t, f, writes)
	seedWriteItem(t, f, *ticket.ResourceID)
	planned, e := writes.Plan(f.ctx, f.people["alice"], writeInput(ticket, "x", "1"), uuid.NewString())
	if e != nil {
		t.Fatal(e)
	}
	if _, e := writes.Approve(f.ctx, f.people["bob"], planned.RequestID, true, ""); !errors.Is(e, ErrDenied) {
		t.Fatal("a non-reviewer approved a write: ", e)
	}
}

// TestControlledWriteInFlightRevocation proves an already dispatched write is
// recorded as uncertain instead of being replayed, and its history is refused.
func TestControlledWriteInFlightRevocation(t *testing.T) {
	f, writes := newWriteFixture(t)
	ticket := activateWrite(t, f, writes)
	seedWriteItem(t, f, *ticket.ResourceID)
	approver := writeApprover(t, f, writes, *ticket.ResourceID)
	alice := f.people["alice"]
	var grantID string
	if e := f.admin.QueryRow(f.ctx, `SELECT grant_id::text FROM mcpguard_ra.credential_leases WHERE org_id=$1 AND credential_id=$2`, alice.OrgID, ticket.CredentialID).Scan(&grantID); e != nil {
		t.Fatal(e)
	}
	planned, e := writes.Plan(f.ctx, alice, writeInput(ticket, "in flight", "1"), uuid.NewString())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = writes.Approve(f.ctx, approver, planned.RequestID, true, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = f.admin.Exec(f.ctx, `UPDATE mcpguard_ra.write_requests SET state='EXECUTING' WHERE org_id=$1 AND request_id=$2`, alice.OrgID, planned.RequestID); e != nil {
		t.Fatal(e)
	}
	pool, e := pgxpool.New(f.ctx, os.Getenv("MCPGUARD_TEST_METADATA_ADMIN_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	revocations := NewRevocationService(pool, f.s.Catalog.Store.Vault)
	res, e := revocations.Revoke(f.ctx, alice, "GRANT", grantID, "revoked during dispatch")
	if e != nil {
		t.Fatal("revocation: ", e)
	}
	if res.WriteUnknown != 1 || res.CleanupPending < 1 {
		t.Fatalf("in-flight revocation report is incomplete: %+v", res)
	}
	var state string
	if e = f.admin.QueryRow(f.ctx, `SELECT state FROM mcpguard_ra.write_requests WHERE org_id=$1 AND request_id=$2`, alice.OrgID, planned.RequestID).Scan(&state); e != nil {
		t.Fatal(e)
	}
	if state != "OUTCOME_UNKNOWN" {
		t.Fatalf("in-flight write state=%q, want OUTCOME_UNKNOWN", state)
	}
	if _, e := writes.Resume(f.ctx, alice, planned.RequestID); !errors.Is(e, ErrDenied) {
		t.Fatal("an uncertain write was replayed: ", e)
	}
	history, e := writes.Status(f.ctx, alice, planned.RequestID)
	if e != nil || history.State != "OUTCOME_UNKNOWN" || history.Result != nil {
		t.Fatalf("historical write result was not redacted: state=%q result=%v err=%v", history.State, history.Result, e)
	}
}

// TestControlledWritePendingApprovalStaleAfterRevocation proves an approved but
// not yet dispatched plan cannot execute once the grant is revoked.
func TestControlledWritePendingApprovalStaleAfterRevocation(t *testing.T) {
	f, writes := newWriteFixture(t)
	ticket := activateWrite(t, f, writes)
	seedWriteItem(t, f, *ticket.ResourceID)
	approver := writeApprover(t, f, writes, *ticket.ResourceID)
	alice := f.people["alice"]
	var grantID string
	if e := f.admin.QueryRow(f.ctx, `SELECT grant_id::text FROM mcpguard_ra.credential_leases WHERE org_id=$1 AND credential_id=$2`, alice.OrgID, ticket.CredentialID).Scan(&grantID); e != nil {
		t.Fatal(e)
	}
	planned, e := writes.Plan(f.ctx, alice, writeInput(ticket, "pending approval", "1"), uuid.NewString())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = writes.Approve(f.ctx, approver, planned.RequestID, true, ""); e != nil {
		t.Fatal(e)
	}
	pool, e := pgxpool.New(f.ctx, os.Getenv("MCPGUARD_TEST_METADATA_ADMIN_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	if _, e = NewRevocationService(pool, f.s.Catalog.Store.Vault).Revoke(f.ctx, alice, "GRANT", grantID, "revoked before dispatch"); e != nil {
		t.Fatal(e)
	}
	if _, e := writes.Resume(f.ctx, alice, planned.RequestID); !errors.Is(e, ErrDenied) {
		t.Fatal("an approved plan executed after revocation: ", e)
	}
	var version int
	if e = f.native[0].QueryRow(f.ctx, "SELECT version FROM resource_demo.items WHERE item_id='item_demo_1'").Scan(&version); e != nil {
		t.Fatal(e)
	}
	if version != 1 {
		t.Fatalf("native row changed after a rejected dispatch: version=%d", version)
	}
	var state string
	if e = f.admin.QueryRow(f.ctx, `SELECT state FROM mcpguard_ra.write_requests WHERE org_id=$1 AND request_id=$2`, alice.OrgID, planned.RequestID).Scan(&state); e != nil {
		t.Fatal(e)
	}
	if state == "SUCCEEDED" || state == "EXECUTING" {
		t.Fatalf("stale plan state=%q", state)
	}
}

// TestControlledWriteRejectedAfterIssuerRotation proves a rotated package-key
// epoch invalidates an approval that was recorded under the old identity.
func TestControlledWriteRejectedAfterIssuerRotation(t *testing.T) {
	f, writes := newWriteFixture(t)
	ticket := activateWrite(t, f, writes)
	seedWriteItem(t, f, *ticket.ResourceID)
	approver := writeApprover(t, f, writes, *ticket.ResourceID)
	alice := f.people["alice"]
	planned, e := writes.Plan(f.ctx, alice, writeInput(ticket, "old epoch", "1"), uuid.NewString())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = writes.Approve(f.ctx, approver, planned.RequestID, true, ""); e != nil {
		t.Fatal(e)
	}
	rotated := f.config
	rotated.Issuer.Version = "2"
	rotated.Issuer.IssuerEpoch = uuid.NewString()
	raw, e := json.Marshal(rotated)
	if e != nil {
		t.Fatal(e)
	}
	if e = ImportCredentials(f.ctx, f.dsn, raw); e != nil {
		t.Fatal("issuer rotation: ", e)
	}
	if _, e := writes.Resume(f.ctx, alice, planned.RequestID); !errors.Is(e, ErrDenied) {
		t.Fatal("an approval from the old issuer epoch executed: ", e)
	}
	var version int
	if e = f.native[0].QueryRow(f.ctx, "SELECT version FROM resource_demo.items WHERE item_id='item_demo_1'").Scan(&version); e != nil {
		t.Fatal(e)
	}
	if version != 1 {
		t.Fatalf("native row changed after a stale approval: version=%d", version)
	}
}

// TestRecoveryFenceBlocksStaleMetadata proves a native fence ahead of the
// metadata recovery point blocks an otherwise valid approval until an operator
// accepts (re-verifies) the restored point.
func TestControlledWriteRecoveryFenceBlocksStaleMetadata(t *testing.T) {
	f, writes := newWriteFixture(t)
	ticket := activateWrite(t, f, writes)
	seedWriteItem(t, f, *ticket.ResourceID)
	approver := writeApprover(t, f, writes, *ticket.ResourceID)
	alice := f.people["alice"]
	planned, e := writes.Plan(f.ctx, alice, writeInput(ticket, "stale metadata", "1"), uuid.NewString())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = writes.Approve(f.ctx, approver, planned.RequestID, true, ""); e != nil {
		t.Fatal(e)
	}
	unclaimed, e := writes.Plan(f.ctx, alice, writeInput(ticket, "unclaimed before restore", "1"), uuid.NewString())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = writes.Approve(f.ctx, approver, unclaimed.RequestID, true, ""); e != nil {
		t.Fatal(e)
	}
	// The native side moves ahead without the metadata record: exactly what a
	// metadata restore to an older backup looks like.
	generation, e := writes.Fence.Advance(f.ctx, alice.OrgID)
	if e != nil || generation < 1 {
		t.Fatal("native fence advance failed: ", e)
	}
	if _, e := writes.Resume(f.ctx, alice, planned.RequestID); !errors.Is(e, ErrDenied) {
		t.Fatal("stale metadata passed the recovery fence: ", e)
	}
	var version int
	if e = f.native[0].QueryRow(f.ctx, "SELECT version FROM resource_demo.items WHERE item_id='item_demo_1'").Scan(&version); e != nil {
		t.Fatal(e)
	}
	if version != 1 {
		t.Fatalf("native row changed behind the fence: version=%d", version)
	}
	// The operator re-verifies and accepts the recovery point; new work resumes.
	if e = writes.Fence.Record(f.ctx, alice.OrgID, generation, alice.ID); e != nil {
		t.Fatal("accepting the recovery point failed: ", e)
	}
	if _, e = writes.Resume(f.ctx, alice, unclaimed.RequestID); !errors.Is(e, ErrDenied) {
		t.Fatal("an unclaimed approval from the restored snapshot was accepted: ", e)
	}
	second, e := writes.Plan(f.ctx, alice, writeInput(ticket, "after restore", "1"), uuid.NewString())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = writes.Approve(f.ctx, approver, second.RequestID, true, ""); e != nil {
		t.Fatal(e)
	}
	done, e := writes.Resume(f.ctx, alice, second.RequestID)
	if e != nil || done.State != "SUCCEEDED" {
		t.Fatal("verified recovery point did not resume work: ", e)
	}
}

// TestControlledWriteConsoleApprovals drives the real console HTTP endpoint:
// the reviewer sees the queue, the subject cannot approve, and a stale decision
// returns an explicit conflict code.
func TestControlledWriteConsoleApprovals(t *testing.T) {
	f, writes := newWriteFixture(t)
	ticket := activateWrite(t, f, writes)
	seedWriteItem(t, f, *ticket.ResourceID)
	approver := writeApprover(t, f, writes, *ticket.ResourceID)
	alice := f.people["alice"]
	planned, e := writes.Plan(f.ctx, alice, writeInput(ticket, "console approval", "1"), uuid.NewString())
	if e != nil {
		t.Fatal(e)
	}
	console := &SelfServiceConsole{Catalog: f.s.Catalog, Access: f.s, Writes: writes}
	do := func(method, path string, body string, p metadata.Principal) (int, string) {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, reader)
		rec := httptest.NewRecorder()
		if !console.ServeHTTP(rec, req, p) {
			t.Fatalf("console route %s %s was not served", method, path)
		}
		return rec.Code, rec.Body.String()
	}
	code, body := do("GET", "/console/api/v2/self-service/write-requests", "", approver)
	if code != http.StatusOK || !strings.Contains(body, planned.RequestID) {
		t.Fatalf("reviewer queue code=%d body=%s", code, body)
	}
	if code, _ = do("POST", "/console/api/v2/self-service/write-requests/"+planned.RequestID+"/decisions", `{"approve":true}`, alice); code != http.StatusForbidden {
		t.Fatalf("self approval over HTTP returned %d, want 403", code)
	}
	code, body = do("POST", "/console/api/v2/self-service/write-requests/"+planned.RequestID+"/decisions", `{"approve":true}`, approver)
	if code != http.StatusOK || !strings.Contains(body, "APPROVED") {
		t.Fatalf("reviewer decision code=%d body=%s", code, body)
	}
	code, body = do("POST", "/console/api/v2/self-service/write-requests/"+planned.RequestID+"/decisions", `{"approve":true}`, approver)
	if code != http.StatusConflict || !strings.Contains(body, "STATE_CONFLICT") {
		t.Fatalf("stale decision code=%d body=%s", code, body)
	}
}
