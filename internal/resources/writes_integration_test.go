package resources

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

// newWriteFixture binds the write service to the context runtime, like RA-11.
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
	return f, NewWriteService(catalog, credentials, manager)
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

func activateWrite(t *testing.T, f *contextFixture) v02.PrepareDownloadResult {
	t.Helper()
	input := f.input()
	input.ProfileID = writeProfileID(t, f)
	r, e := f.s.Request(f.ctx, f.people["alice"], input)
	if e != nil {
		t.Fatal("write access request: ", e)
	}
	out, e := f.s.Decide(f.ctx, f.people["reviewer"], r.RequestID, approveInput(r))
	if e != nil {
		t.Fatal("write approval: ", e)
	}
	done := f.run(t, out, "VERIFIED")
	t.Logf("grant state=%s request=%s account=%v binding=%v profile=%s", done.Grant.State, done.Request.State, done.Grant.AccountID, done.Grant.BindingID, done.Grant.ProfileID)
	status, e := f.service.Issue(f.ctx, f.people["alice"], v02.IssueCredentialInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()})
	if e != nil {
		t.Fatal("write issue: ", e)
	}
	resource := input.ResourceID
	return v02.PrepareDownloadResult{CredentialID: status.CredentialID, ResourceID: &resource}
}

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
	ticket := activateWrite(t, f)
	seedWriteItem(t, f, *ticket.ResourceID)

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
	approved, e := writes.Approve(f.ctx, f.people["reviewer"], planned.RequestID, true, "reviewed")
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
}

func TestControlledWriteRejectsVersionConflict(t *testing.T) {
	f, writes := newWriteFixture(t)
	ticket := activateWrite(t, f)
	seedWriteItem(t, f, *ticket.ResourceID)

	first, e := writes.Plan(f.ctx, f.people["alice"], writeInput(ticket, "first", "1"), uuid.NewString())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = writes.Approve(f.ctx, f.people["reviewer"], first.RequestID, true, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = writes.Resume(f.ctx, f.people["alice"], first.RequestID); e != nil {
		t.Fatal(e)
	}
	stale, e := writes.Plan(f.ctx, f.people["alice"], writeInput(ticket, "stale", "1"), uuid.NewString())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = writes.Approve(f.ctx, f.people["reviewer"], stale.RequestID, true, ""); e != nil {
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
	ticket := activateWrite(t, f)
	seedWriteItem(t, f, *ticket.ResourceID)
	planned, e := writes.Plan(f.ctx, f.people["alice"], writeInput(ticket, "before revoke", "1"), uuid.NewString())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.Revoke(f.ctx, f.people["alice"], v02.RevokeCredentialInput{CredentialID: ticket.CredentialID, ExpectedRevision: "1", RequestKey: uuid.NewString(), Reason: "write approval revoked credential"}); e != nil {
		t.Fatal(e)
	}
	if _, e = writes.Approve(f.ctx, f.people["reviewer"], planned.RequestID, true, ""); e != nil {
		t.Fatal(e)
	}
	if _, e := writes.Resume(f.ctx, f.people["alice"], planned.RequestID); !errors.Is(e, ErrDenied) {
		t.Fatal("revoked credential still dispatched a write: ", e)
	}
}

func TestControlledWriteRejectsNonReviewer(t *testing.T) {
	f, writes := newWriteFixture(t)
	ticket := activateWrite(t, f)
	seedWriteItem(t, f, *ticket.ResourceID)
	planned, e := writes.Plan(f.ctx, f.people["alice"], writeInput(ticket, "x", "1"), uuid.NewString())
	if e != nil {
		t.Fatal(e)
	}
	if _, e := writes.Approve(f.ctx, f.people["bob"], planned.RequestID, true, ""); !errors.Is(e, ErrDenied) {
		t.Fatal("a non-reviewer approved a write: ", e)
	}
}
