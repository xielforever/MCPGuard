package resources

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"os"
)

// newReadFixture reuses the RA-09 private scene and, like the context runtime,
// binds the read service to the context store so the same credential rows and
// least-privilege role are exercised.
func newReadFixture(t *testing.T) (*contextFixture, *ReadService) {
	t.Helper()
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
	return f, NewReadService(catalog, credentials, manager)
}

// activated prepares, claims and activates the first credential of one person.
func (f *contextFixture) activated(t *testing.T, name string, index int) v02.PrepareDownloadResult {
	t.Helper()
	ticket, _ := f.prepareContext(t, name, index)
	f.activate(t, name, ticket, f.take(t, name, ticket))
	return ticket
}

// seedReadItem inserts one row owned by the account-backed native identity, the
// same way the context fixture proves the RA-03 read foundation.
func (f *contextFixture) seedReadItem(t *testing.T, reads *ReadService, name, resourceID string) {
	t.Helper()
	snap, e := reads.Catalog.Snapshot(f.ctx, f.people[name], resourceID)
	if e != nil {
		t.Fatal("snapshot unavailable: ", e)
	}
	for _, conn := range f.native {
		if _, e := conn.Exec(f.ctx, "INSERT INTO resource_demo.items VALUES($1,'item_demo_1',$2,1) ON CONFLICT DO NOTHING", snap.Binding.NativeIdentity, name); e != nil {
			t.Fatal("could not seed the read item: ", e)
		}
	}
}

func TestPlannedReadItem(t *testing.T) {
	f, reads := newReadFixture(t)
	ticket := f.activated(t, "alice", 0)
	if ticket.ResourceID == nil {
		t.Fatal("fixture returned no resource")
	}
	f.seedReadItem(t, reads, "alice", *ticket.ResourceID)
	in := v02.ReadItemInput{ResourceID: *ticket.ResourceID, CredentialID: ticket.CredentialID, ItemID: "item_demo_1"}

	out, e := reads.ReadItem(f.ctx, f.people["alice"], in)
	if e != nil {
		t.Fatal("planned read failed: ", e)
	}
	if !out.Found || len(out.Items) != 1 || out.Items[0].ItemID != "item_demo_1" {
		t.Fatal("planned read returned no bound item")
	}
	if out.PlanID == "" || out.NativeIdentity == "" || out.CredentialID != ticket.CredentialID {
		t.Fatal("planned read did not report its binding")
	}

	p := f.people["alice"]
	var plans, audit int
	if e := f.s.Catalog.Store.Pool.QueryRow(f.ctx, `SELECT count(*) FROM mcpguard_ra.read_plans WHERE org_id=$1 AND principal_id=$2 AND state='SUCCEEDED'`, p.OrgID, p.ID).Scan(&plans); e != nil || plans != 1 {
		t.Fatal("successful plan was not recorded: ", e)
	}
	if e := f.s.Catalog.Store.Pool.QueryRow(f.ctx, `SELECT count(*) FROM mcpguard_ra.read_audit WHERE org_id=$1 AND plan_id=$2`, p.OrgID, out.PlanID).Scan(&audit); e != nil || audit != 2 {
		t.Fatal("read audit is not correlated with the plan: ", e)
	}
	var stored, credential string
	if e := f.s.Catalog.Store.Pool.QueryRow(f.ctx, `SELECT item_id,credential_id::text FROM mcpguard_ra.read_plans WHERE org_id=$1 AND plan_id=$2`, p.OrgID, out.PlanID).Scan(&stored, &credential); e != nil || stored != "item_demo_1" || credential != ticket.CredentialID {
		t.Fatal("plan does not bind the requested item and credential")
	}
}

func TestPlannedReadRejectsOtherIdentity(t *testing.T) {
	f, reads := newReadFixture(t)
	ticket := f.activated(t, "alice", 0)
	f.seedReadItem(t, reads, "alice", *ticket.ResourceID)
	in := v02.ReadItemInput{ResourceID: *ticket.ResourceID, CredentialID: ticket.CredentialID, ItemID: "item_demo_1"}

	if _, e := reads.ReadItem(f.ctx, f.people["bob"], in); !errors.Is(e, ErrDenied) {
		t.Fatal("another identity could use the credential: ", e)
	}
	var plans int
	if e := f.s.Catalog.Store.Pool.QueryRow(f.ctx, `SELECT count(*) FROM mcpguard_ra.read_plans WHERE org_id=$1 AND principal_id=$2`, f.people["bob"].OrgID, f.people["bob"].ID).Scan(&plans); e != nil || plans != 0 {
		t.Fatal("a rejected read still created a plan")
	}
}

func TestPlannedReadRejectsRevokedCredential(t *testing.T) {
	f, reads := newReadFixture(t)
	ticket := f.activated(t, "alice", 0)
	f.seedReadItem(t, reads, "alice", *ticket.ResourceID)
	in := v02.ReadItemInput{ResourceID: *ticket.ResourceID, CredentialID: ticket.CredentialID, ItemID: "item_demo_1"}
	if _, e := reads.ReadItem(f.ctx, f.people["alice"], in); e != nil {
		t.Fatal("baseline read failed: ", e)
	}
	if _, e := f.service.Revoke(f.ctx, f.people["alice"], v02.RevokeCredentialInput{CredentialID: ticket.CredentialID, ExpectedRevision: "1", RequestKey: uuid.NewString(), Reason: "planned read revocation test"}); e != nil {
		t.Fatal("revocation failed: ", e)
	}
	if _, e := reads.ReadItem(f.ctx, f.people["alice"], in); !errors.Is(e, ErrDenied) {
		t.Fatal("revoked credential still dispatched a read: ", e)
	}
}

func TestPlannedReadNeedsExplicitCredential(t *testing.T) {
	f, reads := newReadFixture(t)
	ticket := f.activated(t, "alice", 0)
	in := v02.ReadItemInput{ResourceID: *ticket.ResourceID, CredentialID: uuid.NewString(), ItemID: "item_demo_1"}
	if _, e := reads.ReadItem(f.ctx, f.people["alice"], in); !errors.Is(e, ErrDenied) {
		t.Fatal("an unknown credential was accepted: ", e)
	}
}

func TestPlannedReadRejectsInvalidInput(t *testing.T) {
	f, reads := newReadFixture(t)
	ticket := f.activated(t, "alice", 0)
	if _, e := reads.ReadItem(f.ctx, f.people["alice"], v02.ReadItemInput{ResourceID: *ticket.ResourceID, CredentialID: ticket.CredentialID, ItemID: "not-a-fixture-item"}); !errors.Is(e, ErrInput) {
		t.Fatal("invalid item id was accepted: ", e)
	}
}
