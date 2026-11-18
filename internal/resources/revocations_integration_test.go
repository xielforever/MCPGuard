package resources

import (
	"context"
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

type fakeCleanupProvider struct {
	fail  bool
	calls int
}

func (f *fakeCleanupProvider) Reconcile(context.Context, v02.ProvisioningPlan, string) (NativeReceipt, error) {
	return NativeReceipt{}, errors.New("not used")
}

func (f *fakeCleanupProvider) Cleanup(_ context.Context, plan v02.ProvisioningPlan, hash string) (NativeReceipt, error) {
	f.calls++
	if f.fail {
		return NativeReceipt{}, errors.New("provider unavailable")
	}
	zero := 0
	return NativeReceipt{Status: "CLEANED", TargetID: plan.TargetID, AccountID: plan.AccountID, PlanHash: hash, ActiveSessions: &zero}, nil
}

func TestRevocationHistoricalCleanupPreservesLiveGrant(t *testing.T) {
	f := newRevokeFixture(t)
	ticket, grantID := f.activatedCredential(t, 0)
	alice := f.people["alice"]
	if _, e := f.revocations.Revoke(f.ctx, alice, "CREDENTIAL", ticket.CredentialID, "one retired file"); e != nil {
		t.Fatal(e)
	}
	// A receipt-less v12 cleanup could have been queued for that file alone.
	var accountID string
	if e := f.admin.QueryRow(f.ctx, `INSERT INTO mcpguard_ra.revocation_cleanup(org_id,account_id,resource_id,state)
 SELECT j.org_id,j.account_id,a.resource_id,'CLEANED' FROM mcpguard_ra.provisioning_jobs j
 JOIN mcpguard_ra.account_claims a USING(org_id,account_id) WHERE j.org_id=$1 AND j.grant_id=$2
 RETURNING account_id::text`, alice.OrgID, grantID).Scan(&accountID); e != nil {
		t.Fatal(e)
	}
	provider := &fakeCleanupProvider{}
	cleaner := f.revocations.ProviderCleaner(provider)
	cleaned, failed, e := f.revocations.Cleanup(f.ctx, alice.OrgID, cleaner, 10)
	if e != nil || cleaned != 0 || failed != 1 || provider.calls != 0 {
		t.Fatalf("historical cleanup touched a live account: cleaned=%d failed=%d calls=%d err=%v", cleaned, failed, provider.calls, e)
	}
	if _, e = f.revocations.Revoke(f.ctx, alice, "GRANT", grantID, "retire the native account"); e != nil {
		t.Fatal(e)
	}
	cleaned, failed, e = f.revocations.Cleanup(f.ctx, alice.OrgID, cleaner, 10)
	if e != nil || cleaned != 1 || failed != 0 || provider.calls != 1 {
		t.Fatalf("authorized cleanup did not resume: cleaned=%d failed=%d calls=%d err=%v", cleaned, failed, provider.calls, e)
	}
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
	service := NewRevocationService(pool, f.s.Catalog.Store.Vault)
	service.Fence = NewRestoreFence(pool, &PGProvisioningProvider{Private: f.private})
	f.service.Fence = service.Fence
	return &revokeFixture{contextFixture: f, revocations: service}
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

// TestRevocationAdvancesRecoveryFence proves an operator revocation also moves
// the native recovery fence, so restoring an older metadata backup is visible.
func TestRevocationAdvancesRecoveryFence(t *testing.T) {
	f := newRevokeFixture(t)
	ticket, _ := f.activatedCredential(t, 0)
	alice := f.people["alice"]
	if _, e := f.revocations.Revoke(f.ctx, alice, "CREDENTIAL", ticket.CredentialID, "fence advance"); e != nil {
		t.Fatal(e)
	}
	var generation int64
	if e := f.admin.QueryRow(f.ctx, `SELECT generation FROM mcpguard_ra.recovery_fence WHERE org_id=$1`, alice.OrgID).Scan(&generation); e != nil {
		t.Fatal("metadata recovery point missing: ", e)
	}
	if generation < 1 {
		t.Fatalf("metadata recovery point=%d", generation)
	}
	native, e := f.revocations.Fence.NativeMax(f.ctx, alice.OrgID)
	if e != nil {
		t.Fatal(e)
	}
	if native != generation {
		t.Fatalf("native fence=%d metadata=%d", native, generation)
	}
}

// TestRevocationRecoveryFenceBlocksOldPackage proves a stale metadata restore
// cannot re-use an old package while the native recovery generation is ahead.
func TestRevocationRecoveryFenceBlocksOldPackage(t *testing.T) {
	f := newRevokeFixture(t)
	_, missingGrant := f.activatedCredential(t, 0)
	ticket, _ := f.activatedCredential(t, 1)
	alice := f.people["alice"]
	// Only a different native database advances. A local counter matching the
	// metadata must not hide that the organization-wide recovery point is stale.
	if _, e := f.native[0].Exec(f.ctx, `SELECT mcpguard_provision.advance_fence($1)`, alice.OrgID); e != nil {
		t.Fatal(e)
	}
	status, e := f.service.Status(f.ctx, alice, v02.CredentialIDInput{CredentialID: ticket.CredentialID})
	if !errors.Is(e, ErrDenied) || status.State == "ACTIVE" {
		t.Fatalf("a stale metadata fence exposed a usable package: state=%q err=%v", status.State, e)
	}
	var recorded string
	if e = f.admin.QueryRow(f.ctx, `SELECT state FROM mcpguard_ra.credential_leases WHERE org_id=$1 AND credential_id=$2`, alice.OrgID, ticket.CredentialID).Scan(&recorded); e != nil || recorded != "ACTIVE" {
		t.Fatalf("the recovery fence changed the recorded lease before reconciliation: state=%q err=%v", recorded, e)
	}
	res, e := f.revocations.Revoke(f.ctx, alice, "GRANT", missingGrant, "record known prior revocation")
	if e != nil || res.Grants != 1 {
		t.Fatalf("could not record a restriction behind the fence: grants=%d err=%v", res.Grants, e)
	}
	if _, e = f.service.Status(f.ctx, alice, v02.CredentialIDInput{CredentialID: ticket.CredentialID}); !errors.Is(e, errRecoveryPointUnverified) {
		t.Fatalf("a regular revocation cleared the recovery fence: %v", e)
	}
	native, e := f.revocations.Fence.NativeMax(f.ctx, alice.OrgID)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.revocations.Fence.Record(f.ctx, alice.OrgID, native, alice.ID); e != nil {
		t.Fatal(e)
	}
	if state := f.credentialState(t, ticket.CredentialID); state != "ACTIVE" {
		t.Fatalf("verified recovery did not restore the unrelated lease: %s", state)
	}
}

// TestRevocationConsolePageAndRevoke drives the revocation page endpoints.
func TestRevocationConsolePageAndRevoke(t *testing.T) {
	f := newRevokeFixture(t)
	ticket, _ := f.activatedCredential(t, 0)
	alice := f.people["alice"]
	if _, e := f.admin.Exec(f.ctx, `INSERT INTO mcpguard_ra.access_reviewers(org_id,resource_id,principal_id,can_approve) VALUES($1,$2,$3,true) ON CONFLICT (org_id,resource_id,principal_id) DO UPDATE SET can_approve=true`, alice.OrgID, *ticket.ResourceID, alice.ID); e != nil {
		t.Fatal(e)
	}
	console := &SelfServiceConsole{Catalog: f.s.Catalog, Access: f.s, Revoker: f.revocations}
	do := func(method, path, body string, p metadata.Principal) (int, string) {
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
	if code, _ := do("GET", "/console/api/v2/self-service/resources", "", f.people["bob"]); code != http.StatusForbidden {
		t.Fatalf("non-reviewer resource page returned %d, want 403", code)
	}
	code, body := do("GET", "/console/api/v2/self-service/resources", "", alice)
	if code != http.StatusOK || strings.Contains(strings.ToLower(body), "password") {
		t.Fatalf("resource page code=%d body=%s", code, body)
	}
	code, body = do("POST", "/console/api/v2/self-service/revocations", `{"kind":"CREDENTIAL","targetId":"`+ticket.CredentialID+`","reason":"console"}`, alice)
	if code != http.StatusOK || !strings.Contains(body, "CREDENTIAL") {
		t.Fatalf("revocation POST code=%d body=%s", code, body)
	}
	code, body = do("GET", "/console/api/v2/self-service/revocations", "", alice)
	if code != http.StatusOK || !strings.Contains(body, ticket.CredentialID) {
		t.Fatalf("revocation ledger code=%d body=%s", code, body)
	}
}
