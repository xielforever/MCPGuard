package resources

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

type fakeCleanupProvider struct{ fail bool }

func (f *fakeCleanupProvider) Reconcile(context.Context, v02.ProvisioningPlan, string) (NativeReceipt, error) {
	return NativeReceipt{}, errors.New("not used")
}

func (f *fakeCleanupProvider) Cleanup(_ context.Context, plan v02.ProvisioningPlan, hash string) (NativeReceipt, error) {
	if f.fail {
		return NativeReceipt{}, errors.New("provider unavailable")
	}
	return NativeReceipt{Status: "CLEANED", TargetID: plan.TargetID, AccountID: plan.AccountID, PlanHash: hash}, nil
}

type revokeFixture struct {
	*contextFixture
	revocations *RevocationService
}

// newRevokeFixture needs the dedicated RA-13 scene (separate evidence run).
func newRevokeFixture(t *testing.T) *revokeFixture {
	t.Helper()
	raw, e := os.ReadFile(os.Getenv("MCPGUARD_RA_CONFIG"))
	var scene struct {
		Revoke bool `json:"raRevokePhase"`
	}
	if e != nil || json.Unmarshal(raw, &scene) != nil || !scene.Revoke {
		t.Skip("explicit RA-13 revocation fixture required")
	}
	f := newContextFixture(t)
	pool, e := pgxpool.New(f.ctx, os.Getenv("MCPGUARD_TEST_METADATA_ADMIN_DSN"))
	if e != nil {
		t.Fatal("revocation runtime unavailable")
	}
	t.Cleanup(pool.Close)
	return &revokeFixture{contextFixture: f, revocations: NewRevocationService(pool, f.s.Catalog.Store.Vault)}
}

// activatedCredential prepares, takes and activates Alice's credential on one
// resource, returning the ticket and its grant id.
func (f *revokeFixture) activatedCredential(t *testing.T, index int) (v02.PrepareDownloadResult, string) {
	t.Helper()
	ticket, out := f.prepareContext(t, "alice", index)
	blob := f.take(t, "alice", ticket)
	f.activate(t, "alice", ticket, blob)
	return ticket, out.Grant.GrantID
}

func (f *revokeFixture) credentialState(t *testing.T, credentialID string) string {
	t.Helper()
	status, e := f.service.Status(f.ctx, f.people["alice"], v02.CredentialIDInput{CredentialID: credentialID})
	if e != nil {
		t.Fatal("credential status: ", e)
	}
	return status.State
}

func TestRevocationCredentialStopsNewAcquisition(t *testing.T) {
	f := newRevokeFixture(t)
	ticket, grantID := f.activatedCredential(t, 0)
	res, e := f.revocations.Revoke(f.ctx, f.people["alice"], "CREDENTIAL", ticket.CredentialID, "lost device")
	if e != nil {
		t.Fatal("credential revocation: ", e)
	}
	if res.Credentials != 1 {
		t.Fatalf("credential revocation touched %d credentials, want 1", res.Credentials)
	}
	if state := f.credentialState(t, ticket.CredentialID); state != "REVOKED" {
		t.Fatalf("credential state is %q, want REVOKED", state)
	}
	if _, e := f.contexts.Prepare(f.ctx, f.people["alice"], f.proofs["alice"], v02.PrepareDownloadInput{GrantID: grantID, RequestKey: uuid.NewString()}); !errors.Is(e, ErrDenied) {
		t.Fatal("a revoked credential still acquired a new download: ", e)
	}
}

func TestRevocationGrantCascadesToCredentials(t *testing.T) {
	f := newRevokeFixture(t)
	ticket, grantID := f.activatedCredential(t, 0)
	res, e := f.revocations.Revoke(f.ctx, f.people["alice"], "GRANT", grantID, "access withdrawn")
	if e != nil {
		t.Fatal("grant revocation: ", e)
	}
	if res.Grants != 1 || res.Credentials != 1 {
		t.Fatalf("grant revocation touched grants=%d credentials=%d, want 1/1", res.Grants, res.Credentials)
	}
	if state := f.credentialState(t, ticket.CredentialID); state != "REVOKED" {
		t.Fatalf("credential under a revoked grant is %q, want REVOKED", state)
	}
	if _, e := f.contexts.Prepare(f.ctx, f.people["alice"], f.proofs["alice"], v02.PrepareDownloadInput{GrantID: grantID, RequestKey: uuid.NewString()}); !errors.Is(e, ErrDenied) {
		t.Fatal("a revoked grant still acquired a download: ", e)
	}
}

func TestRevocationPrincipalDisablesOnlyThatSubject(t *testing.T) {
	f := newRevokeFixture(t)
	ticket, _ := f.activatedCredential(t, 0)
	res, e := f.revocations.Revoke(f.ctx, f.people["alice"], "PRINCIPAL", f.people["alice"].ID, "employee left")
	if e != nil {
		t.Fatal("principal revocation: ", e)
	}
	if res.Credentials < 1 || res.Contexts < 1 || res.Grants < 1 {
		t.Fatalf("principal revocation cascade is incomplete: %+v", res)
	}
	var leaseState string
	if e = f.admin.QueryRow(f.ctx, `SELECT state FROM mcpguard_ra.credential_leases WHERE org_id=$1 AND credential_id=$2`, f.people["alice"].OrgID, ticket.CredentialID).Scan(&leaseState); e != nil {
		t.Fatal(e)
	}
	if leaseState != "REVOKED" {
		t.Fatalf("credential lease of a revoked principal is %q, want REVOKED", leaseState)
	}
	var aliceEnabled, bobEnabled bool
	if e = f.admin.QueryRow(f.ctx, `SELECT enabled FROM mcpguard.principals WHERE org_id=$1 AND id=$2`, f.people["alice"].OrgID, f.people["alice"].ID).Scan(&aliceEnabled); e != nil {
		t.Fatal(e)
	}
	if e = f.admin.QueryRow(f.ctx, `SELECT enabled FROM mcpguard.principals WHERE org_id=$1 AND id=$2`, f.people["bob"].OrgID, f.people["bob"].ID).Scan(&bobEnabled); e != nil {
		t.Fatal(e)
	}
	if aliceEnabled || !bobEnabled {
		t.Fatalf("principal revocation scope is wrong: alice=%v bob=%v", aliceEnabled, bobEnabled)
	}
}

func TestRevocationResourceDisablesBindingsAndGrants(t *testing.T) {
	f := newRevokeFixture(t)
	ticket, grantID := f.activatedCredential(t, 0)
	res, e := f.revocations.Revoke(f.ctx, f.people["alice"], "RESOURCE", *ticket.ResourceID, "resource retired")
	if e != nil {
		t.Fatal("resource revocation: ", e)
	}
	if res.Grants < 1 || res.Credentials < 1 {
		t.Fatalf("resource revocation cascade is incomplete: %+v", res)
	}
	if state := f.credentialState(t, ticket.CredentialID); state != "REVOKED" {
		t.Fatalf("credential under a revoked resource is %q, want REVOKED", state)
	}
	var enabled bool
	var bindings int
	if e = f.admin.QueryRow(f.ctx, `SELECT enabled FROM mcpguard_ra.resource_heads WHERE org_id=$1 AND resource_id=$2`, f.people["alice"].OrgID, *ticket.ResourceID).Scan(&enabled); e != nil {
		t.Fatal(e)
	}
	if e = f.admin.QueryRow(f.ctx, `SELECT count(*) FROM mcpguard_ra.identity_bindings WHERE org_id=$1 AND resource_id=$2 AND enabled`, f.people["alice"].OrgID, *ticket.ResourceID).Scan(&bindings); e != nil {
		t.Fatal(e)
	}
	if enabled || bindings != 0 {
		t.Fatalf("resource revocation scope is wrong: enabled=%v liveBindings=%d", enabled, bindings)
	}
	if _, e := f.contexts.Prepare(f.ctx, f.people["alice"], f.proofs["alice"], v02.PrepareDownloadInput{GrantID: grantID, RequestKey: uuid.NewString()}); !errors.Is(e, ErrDenied) {
		t.Fatal("a revoked resource still acquired a download: ", e)
	}
}

// TestRevocationAnotherReplicaSeesTheEpoch proves a second connection pool
// (the local stand-in for another gateway replica) observes the committed
// revocation and the advanced authorization epoch.
func TestRevocationAnotherReplicaSeesTheEpoch(t *testing.T) {
	f := newRevokeFixture(t)
	ticket, _ := f.activatedCredential(t, 0)
	var before int64
	if e := f.admin.QueryRow(f.ctx, `SELECT auth_epoch FROM mcpguard.authorization_heads WHERE org_id=$1`, f.people["alice"].OrgID).Scan(&before); e != nil {
		t.Fatal(e)
	}
	if _, e := f.revocations.Revoke(f.ctx, f.people["alice"], "CREDENTIAL", ticket.CredentialID, "replica race"); e != nil {
		t.Fatal(e)
	}
	var after int64
	if e := f.admin.QueryRow(f.ctx, `SELECT auth_epoch FROM mcpguard.authorization_heads WHERE org_id=$1`, f.people["alice"].OrgID).Scan(&after); e != nil {
		t.Fatal(e)
	}
	if after <= before {
		t.Fatalf("authorization epoch did not advance: %d -> %d", before, after)
	}
	store, e := metadata.Open(f.ctx, os.Getenv("MCPGUARD_TEST_CONTEXT_DSN"), f.s.Catalog.Store.Vault)
	if e != nil {
		t.Fatal("second replica unavailable")
	}
	t.Cleanup(store.Close)
	replicaCatalog, e := NewCatalog(f.ctx, store)
	if e != nil {
		t.Fatal(e)
	}
	replicaCredentials, e := NewCredentialService(replicaCatalog, f.keys)
	if e != nil {
		t.Fatal(e)
	}
	status, e := replicaCredentials.Status(f.ctx, f.people["alice"], v02.CredentialIDInput{CredentialID: ticket.CredentialID})
	if e != nil || status.State != "REVOKED" {
		t.Fatalf("second replica still sees state=%q err=%v", status.State, e)
	}
}

// TestRevocationCleanupStaysPendingUntilProven proves a revoked native account
// is not reported cleaned until a retry actually removed it.
func TestRevocationCleanupStaysPendingUntilProven(t *testing.T) {
	f := newRevokeFixture(t)
	ticket, _ := f.activatedCredential(t, 0)
	res, e := f.revocations.Revoke(f.ctx, f.people["alice"], "RESOURCE", *ticket.ResourceID, "resource retired")
	if e != nil {
		t.Fatal(e)
	}
	if res.CleanupPending < 1 {
		t.Fatalf("revocation did not enqueue a native cleanup: %+v", res)
	}
	alice := f.people["alice"]
	var pending int
	if e = f.admin.QueryRow(f.ctx, `SELECT count(*) FROM mcpguard_ra.revocation_cleanup WHERE org_id=$1 AND state='PENDING'`, alice.OrgID).Scan(&pending); e != nil {
		t.Fatal(e)
	}
	if pending != res.CleanupPending {
		t.Fatalf("cleanup rows=%d report=%d", pending, res.CleanupPending)
	}
	provider := &fakeCleanupProvider{fail: true}
	cleaned, failed, e := f.revocations.Cleanup(f.ctx, alice.OrgID, f.revocations.ProviderCleaner(provider), 10)
	if e != nil || failed < 1 || cleaned != 0 {
		t.Fatalf("failed cleanup run cleaned=%d failed=%d err=%v", cleaned, failed, e)
	}
	var state string
	var attempts int
	if e = f.admin.QueryRow(f.ctx, `SELECT state,attempts FROM mcpguard_ra.revocation_cleanup WHERE org_id=$1 ORDER BY updated_at DESC LIMIT 1`, alice.OrgID).Scan(&state, &attempts); e != nil {
		t.Fatal(e)
	}
	if state != "FAILED" || attempts < 1 {
		t.Fatalf("failed cleanup is state=%q attempts=%d, want FAILED/retryable", state, attempts)
	}
	provider.fail = false
	cleaned, failed, e = f.revocations.Cleanup(f.ctx, alice.OrgID, f.revocations.ProviderCleaner(provider), 10)
	if e != nil || failed != 0 || cleaned < 1 {
		t.Fatalf("retry result cleaned=%d failed=%d err=%v", cleaned, failed, e)
	}
	if e = f.admin.QueryRow(f.ctx, `SELECT state FROM mcpguard_ra.revocation_cleanup WHERE org_id=$1 ORDER BY updated_at DESC LIMIT 1`, alice.OrgID).Scan(&state); e != nil {
		t.Fatal(e)
	}
	if state != "CLEANED" {
		t.Fatalf("proven cleanup state=%q, want CLEANED", state)
	}
	// A repeated revocation must not resurrect an already proven cleanup.
	if _, e = f.revocations.Revoke(f.ctx, alice, "RESOURCE", *ticket.ResourceID, "retired again"); e != nil {
		t.Fatal(e)
	}
	if e = f.admin.QueryRow(f.ctx, `SELECT state FROM mcpguard_ra.revocation_cleanup WHERE org_id=$1 ORDER BY updated_at DESC LIMIT 1`, alice.OrgID).Scan(&state); e != nil {
		t.Fatal(e)
	}
	if state != "CLEANED" {
		t.Fatalf("repeated revocation reset proven cleanup to %q", state)
	}
}
