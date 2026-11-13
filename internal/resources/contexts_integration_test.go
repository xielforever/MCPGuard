package resources

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type contextFixture struct {
	*downloadFixture
	contexts     *ContextService
	clientTokens map[string]string
	proofs       map[string]ContextProof
	views        map[string]v02.CredentialContext
}

func newContextFixture(t *testing.T) *contextFixture {
	t.Helper()
	path := os.Getenv("MCPGUARD_RA_CONFIG")
	if path == "" {
		t.Skip("explicit RA-09 fixture required")
	}
	raw, e := os.ReadFile(path)
	var scene struct {
		Context bool `json:"raContextPhase"`
	}
	if e != nil || json.Unmarshal(raw, &scene) != nil {
		t.Fatal("invalid private fixture")
	}
	if !scene.Context {
		t.Skip("explicit RA-09 fixture required")
	}
	f := newDownloadFixture(t)
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
	downloads, e := NewDownloadService(credentials, 60, 20)
	if e != nil {
		t.Fatal(e)
	}
	service, e := NewContextService(downloads, 60, 600, 10, 2)
	if e != nil {
		t.Fatal(e)
	}
	enrollmentDownloads, e := NewDownloadService(f.service, 60, 10)
	if e != nil {
		t.Fatal(e)
	}
	enrollment, e := NewContextService(enrollmentDownloads, 1, 1, 1, 1)
	if e != nil {
		t.Fatal(e)
	} // enrollment uses the trusted server role below
	result := &contextFixture{f, service, map[string]string{}, map[string]ContextProof{}, map[string]v02.CredentialContext{}}
	for _, name := range []string{"alice", "bob"} {
		_, _, token, e := enrollment.EnrollClient(f.ctx, f.people[name], 600)
		if e != nil {
			t.Fatal("client enrollment: ", e)
		}
		result.clientTokens[name] = token
		created, e := service.Create(f.ctx, f.people[name], token, v02.ContextCreateInput{ClientLabel: "same untrusted label", RequestKey: uuid.NewString()})
		if e != nil {
			t.Fatal("create context: ", e)
		}
		proof, e := ParseContextProof(created.Context.ContextID + "." + created.ContextToken)
		if e != nil {
			t.Fatal(e)
		}
		result.proofs[name] = proof
		result.views[name] = created.Context
	}
	return result
}
func (f *contextFixture) prepareContext(t *testing.T, name string, index int) (v02.PrepareDownloadResult, v02.AccessStatusResult) {
	t.Helper()
	out := f.approved(t, name, index)
	f.run(t, out, "VERIFIED")
	ticket, e := f.contexts.Prepare(f.ctx, f.people[name], f.proofs[name], v02.PrepareDownloadInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()})
	if e != nil {
		t.Fatal(e)
	}
	return ticket, out
}
func (f *contextFixture) take(t *testing.T, name string, ticket v02.PrepareDownloadResult) []byte {
	t.Helper()
	var blob []byte
	e := f.contexts.Deliver(f.ctx, f.people[name], f.clientTokens[name], f.proofs[name], v02.DownloadIDInput{DownloadID: ticket.DownloadID}, func(_ v02.DownloadTicket, _ string, raw []byte) error { blob = append([]byte{}, raw...); return nil })
	if e != nil {
		t.Fatal(e)
	}
	return blob
}
func activationInput(t *testing.T, ticket v02.PrepareDownloadResult, blob []byte) v02.ActivationInput {
	t.Helper()
	var env v02.CredentialEnvelope
	if json.Unmarshal(blob, &env) != nil || ticket.ResourceID == nil {
		t.Fatal("invalid private file fixture")
	}
	id := ticket.DownloadID
	return v02.ActivationInput{CredentialID: ticket.CredentialID, ResourceID: *ticket.ResourceID, CredentialPackage: env, RequestKey: uuid.NewString(), DownloadID: &id}
}
func (f *contextFixture) activate(t *testing.T, name string, ticket v02.PrepareDownloadResult, blob []byte) v02.ContextActivation {
	t.Helper()
	out, e := f.contexts.Activate(f.ctx, f.people[name], f.proofs[name], activationInput(t, ticket, blob))
	if e != nil {
		t.Fatal("activation: ", e)
	}
	return out
}
func (f *contextFixture) contextStatus(t *testing.T, name string) v02.ContextStatusResult {
	t.Helper()
	out, e := f.contexts.Status(f.ctx, f.people[name], f.proofs[name])
	if e != nil {
		t.Fatal(e)
	}
	return out
}

func TestCredentialContextsIntegration(t *testing.T) {
	t.Run("two_people_two_resources_online_native_foundation", func(t *testing.T) {
		f := newContextFixture(t)
		for index := 0; index < 2; index++ {
			for _, name := range []string{"alice", "bob"} {
				ticket, _ := f.prepareContext(t, name, index)
				blob := f.take(t, name, ticket)
				active := f.activate(t, name, ticket, blob)
				if active.State != "ACTIVE" {
					t.Fatal("not active")
				}
				if _, e := f.contexts.CheckActivation(f.ctx, f.people[name], f.proofs[name], active.ResourceID, active.CredentialID); e != nil {
					t.Fatal(e)
				}
				snap, e := f.s.Catalog.Snapshot(f.ctx, f.people[name], active.ResourceID)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = f.native[index].Exec(f.ctx, "INSERT INTO resource_demo.items VALUES($1,'item_demo_context',$2,1)", snap.Binding.NativeIdentity, name); e != nil {
					t.Fatal(e)
				}
				reader := NewReadManager(f.s.Catalog, &VaultSecretProvider{Catalog: f.s.Catalog, Endpoints: f.private.Endpoints})
				value, e := reader.ReadFixture(f.ctx, f.people[name], v02.FixtureReadInput{ResourceID: active.ResourceID, ItemID: "item_demo_context"})
				reader.Close()
				if e != nil || !value.Found || value.Items[0].Note != name || value.NativeIdentity != snap.Binding.NativeIdentity {
					t.Fatal("context-qualified native foundation crossed identity")
				}
			}
		}
		for _, name := range []string{"alice", "bob"} {
			if len(f.contextStatus(t, name).Activations) != 2 {
				t.Fatal("multi-resource activation missing")
			}
		}
	})
	t.Run("copied_tokens_and_files_cannot_cross_people", func(t *testing.T) {
		f := newContextFixture(t)
		ticket, _ := f.prepareContext(t, "alice", 0)
		blob := f.take(t, "alice", ticket)
		in := activationInput(t, ticket, blob)
		if _, e := f.contexts.Activate(f.ctx, f.people["bob"], f.proofs["bob"], in); e == nil {
			t.Fatal("Bob activated Alice file")
		}
		if _, e := f.contexts.Status(f.ctx, f.people["bob"], f.proofs["alice"]); !errors.Is(e, ErrDenied) {
			t.Fatal("copied context authenticated another user")
		}
		if _, e := f.contexts.Create(f.ctx, f.people["bob"], f.clientTokens["alice"], v02.ContextCreateInput{ClientLabel: "same label", RequestKey: uuid.NewString()}); !errors.Is(e, ErrDenied) {
			t.Fatal("copied enrollment crossed owner")
		}
	})
	t.Run("enrollment_required_and_create_secret_not_replayed", func(t *testing.T) {
		f := newContextFixture(t)
		if _, e := f.contexts.Create(f.ctx, f.people["alice"], f.tokens["alice"], v02.ContextCreateInput{ClientLabel: "self reported", RequestKey: uuid.NewString()}); !errors.Is(e, ErrDenied) {
			t.Fatal("manual authorization created enrolled context")
		}
		in := v02.ContextCreateInput{ClientLabel: "safe label", RequestKey: uuid.NewString()}
		one, e := f.contexts.Create(f.ctx, f.people["alice"], f.clientTokens["alice"], in)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.contexts.Create(f.ctx, f.people["alice"], f.clientTokens["alice"], in); !errors.Is(e, ErrConflict) {
			t.Fatal("secret response replayed")
		}
		old := f.contextStatus(t, "alice")
		if old.Context.State != "REVOKED" || one.Context.ClientInstanceID != old.Context.ClientInstanceID {
			t.Fatal("replacement did not close old context")
		}
	})
	t.Run("context_renewal_preserves_identity_and_absolute_bound", func(t *testing.T) {
		f := newContextFixture(t)
		before := f.views["alice"]
		time.Sleep(10 * time.Millisecond)
		in := v02.ContextRenewInput{ExpectedRevision: before.Revision, RequestKey: uuid.NewString()}
		renewed, e := f.contexts.Renew(f.ctx, f.people["alice"], f.proofs["alice"], in, false)
		if e != nil || renewed.ContextID != before.ContextID || renewed.AbsoluteExpiresAt != before.AbsoluteExpiresAt || renewed.Revision != "2" {
			t.Fatal("renewal changed identity or absolute bound")
		}
		oldTime, _, _ := normalizedTime(before.ExpiresAt)
		newTime, _, _ := normalizedTime(renewed.ExpiresAt)
		if !newTime.After(oldTime) {
			t.Fatal("context not extended")
		}
		replay, e := f.contexts.Renew(f.ctx, f.people["alice"], f.proofs["alice"], in, false)
		if e != nil || replay != renewed {
			t.Fatal("context renewal not idempotent")
		}
		public, _ := json.Marshal(renewed)
		if strings.Contains(string(public), f.proofs["alice"].secret) {
			t.Fatal("renewal exposed context secret")
		}
	})
	t.Run("expired_context_requires_new_private_context_and_file_submission", func(t *testing.T) {
		f := newContextFixture(t)
		ticket, _ := f.prepareContext(t, "alice", 0)
		blob := f.take(t, "alice", ticket)
		f.activate(t, "alice", ticket, blob)
		f.contexts.TTL = 1
		f.contexts.Lifetime = 2
		short, e := f.contexts.Create(f.ctx, f.people["alice"], f.clientTokens["alice"], v02.ContextCreateInput{ClientLabel: "short", RequestKey: uuid.NewString()})
		if e != nil {
			t.Fatal(e)
		}
		proof, _ := ParseContextProof(short.Context.ContextID + "." + short.ContextToken)
		f.proofs["alice"] = proof
		if len(f.contextStatus(t, "alice").Activations) != 0 {
			t.Fatal("new context inherited activation")
		}
		time.Sleep(1100 * time.Millisecond)
		if e = f.contexts.VerifyDelivery(f.ctx, f.people["alice"], proof, f.clientTokens["alice"]); e != nil {
			t.Fatal("owner cannot authenticate safe terminal Context inspection")
		}
		if _, e = f.contexts.Renew(f.ctx, f.people["alice"], proof, v02.ContextRenewInput{ExpectedRevision: "1", RequestKey: uuid.NewString()}, false); !errors.Is(e, ErrDenied) {
			t.Fatal("expired context revived")
		}
		if _, e = f.contexts.Activate(f.ctx, f.people["alice"], proof, activationInput(t, ticket, blob)); !errors.Is(e, ErrDenied) {
			t.Fatal("expired context accepted file")
		}
		fresh, e := f.contexts.Create(f.ctx, f.people["alice"], f.clientTokens["alice"], v02.ContextCreateInput{ClientLabel: "new explicit context", RequestKey: uuid.NewString()})
		if e != nil {
			t.Fatal(e)
		}
		newProof, _ := ParseContextProof(fresh.Context.ContextID + "." + fresh.ContextToken)
		status, e := f.contexts.Status(f.ctx, f.people["alice"], newProof)
		if e != nil || len(status.Activations) != 0 {
			t.Fatal("recreate automatically reactivated grants")
		}
		input := activationInput(t, ticket, blob)
		input.DownloadID = nil
		if _, e = f.contexts.Activate(f.ctx, f.people["alice"], newProof, input); e != nil {
			t.Fatal("valid existing file could not be explicitly resubmitted")
		}
	})
	t.Run("bounded_client_queue_cursor_and_receipts", func(t *testing.T) {
		f := newContextFixture(t)
		f.contexts.PageLimit = 1
		one, out := f.prepareContext(t, "alice", 0)
		for i := 0; i < 2; i++ {
			if _, e := f.contexts.Prepare(f.ctx, f.people["alice"], f.proofs["alice"], v02.PrepareDownloadInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()}); e != nil {
				t.Fatal(e)
			}
		}
		page, e := f.contexts.Queue(f.ctx, f.people["alice"], f.proofs["alice"], v02.ListDownloadsInput{})
		if e != nil || len(page.Tickets) != 1 || page.NextAfter == "" {
			t.Fatal("queue not bounded")
		}
		if _, e = f.contexts.Queue(f.ctx, f.people["bob"], f.proofs["bob"], v02.ListDownloadsInput{After: &page.NextAfter}); !errors.Is(e, ErrDenied) {
			t.Fatal("cursor crossed owner/client")
		}
		next, e := f.contexts.Queue(f.ctx, f.people["alice"], f.proofs["alice"], v02.ListDownloadsInput{After: &page.NextAfter})
		if e != nil || next.Tickets[0].DownloadID == page.Tickets[0].DownloadID {
			t.Fatal("cursor did not advance")
		}
		blob := f.take(t, "alice", one)
		f.activate(t, "alice", one, blob)
		page, e = f.contexts.Queue(f.ctx, f.people["alice"], f.proofs["alice"], v02.ListDownloadsInput{})
		if e != nil || page.Tickets[0].DownloadID == one.DownloadID {
			t.Fatal("completed task remained pending")
		}
	})
	t.Run("manual_fallback_and_other_client_claim_denied", func(t *testing.T) {
		f := newContextFixture(t)
		ticket, _ := f.prepareContext(t, "alice", 0)
		if e := f.downloadFixture.download.Deliver(f.ctx, f.people["alice"], f.clientTokens["alice"], v02.DownloadIDInput{DownloadID: ticket.DownloadID}, discardDownload); !errors.Is(e, ErrDenied) {
			t.Fatal("bound task fell back to manual")
		}
		other, e := f.contexts.Create(f.ctx, f.people["alice"], f.clientTokens["alice"], v02.ContextCreateInput{ClientLabel: "rebuild", RequestKey: uuid.NewString()})
		if e != nil {
			t.Fatal(e)
		}
		proof, _ := ParseContextProof(other.Context.ContextID + "." + other.ContextToken)
		if e = f.contexts.Deliver(f.ctx, f.people["alice"], f.clientTokens["alice"], proof, v02.DownloadIDInput{DownloadID: ticket.DownloadID}, discardDownload); e != nil {
			t.Fatal("same enrolled client could not recover pending task")
		}
	})
	t.Run("file_revocation_not_reversed_by_context_or_package_renewal", func(t *testing.T) {
		f := newContextFixture(t)
		one, _ := f.prepareContext(t, "alice", 0)
		two, _ := f.prepareContext(t, "alice", 1)
		blob := f.take(t, "alice", one)
		f.activate(t, "alice", one, blob)
		f.activate(t, "alice", two, f.take(t, "alice", two))
		if _, e := f.service.Revoke(f.ctx, f.people["alice"], v02.RevokeCredentialInput{CredentialID: one.CredentialID, ExpectedRevision: "1", RequestKey: uuid.NewString(), Reason: "private file revoked"}); e != nil {
			t.Fatal(e)
		}
		if _, e := f.contexts.Renew(f.ctx, f.people["alice"], f.proofs["alice"], v02.ContextRenewInput{ExpectedRevision: "1", RequestKey: uuid.NewString()}, false); e != nil {
			t.Fatal(e)
		}
		if _, e := f.contexts.CheckActivation(f.ctx, f.people["alice"], f.proofs["alice"], *one.ResourceID, one.CredentialID); !errors.Is(e, ErrDenied) {
			t.Fatal("context renewal revived revoked file")
		}
		if _, e := f.contexts.RenewCredential(f.ctx, f.people["alice"], f.proofs["alice"], v02.RenewCredentialInput{CredentialID: one.CredentialID, RequestKey: uuid.NewString()}); !errors.Is(e, ErrDenied) {
			t.Fatal("revoked package renewed")
		}
		if _, e := f.contexts.Activate(f.ctx, f.people["alice"], f.proofs["alice"], activationInput(t, one, blob)); e == nil {
			t.Fatal("copied revoked file restored activation")
		}
		if _, e := f.contexts.CheckActivation(f.ctx, f.people["alice"], f.proofs["alice"], *two.ResourceID, two.CredentialID); e != nil {
			t.Fatal("unrelated resource lost activation")
		}
	})
	t.Run("expired_file_can_append_new_package_without_account_or_lease_extension", func(t *testing.T) {
		f := newContextFixture(t)
		f.config.Policies[0].Version = "2"
		f.config.Policies[0].MaxTtlSeconds = 1
		f.apply(t)
		ticket, _ := f.prepareContext(t, "alice", 0)
		f.activate(t, "alice", ticket, f.take(t, "alice", ticket))
		time.Sleep(1100 * time.Millisecond)
		if _, e := f.contexts.CheckActivation(f.ctx, f.people["alice"], f.proofs["alice"], *ticket.ResourceID, ticket.CredentialID); !errors.Is(e, ErrDenied) {
			t.Fatal("expired file remained active")
		}
		next, e := f.contexts.RenewCredential(f.ctx, f.people["alice"], f.proofs["alice"], v02.RenewCredentialInput{CredentialID: ticket.CredentialID, RequestKey: uuid.NewString()})
		if e != nil || next.CredentialID == ticket.CredentialID || next.LeaseID == ticket.LeaseID {
			t.Fatal("renewal did not append new package: ", e)
		}
		old, e := f.service.Status(f.ctx, f.people["alice"], v02.CredentialIDInput{CredentialID: ticket.CredentialID})
		if e != nil || old.State != "EXPIRED" || old.ExpiresAt != ticket.CredentialExpiresAt || f.count(t, "native_accounts") != 1 || f.count(t, "provisioning_jobs") != 1 || f.count(t, "credential_packages") != 2 {
			t.Fatal("renewal changed original lease or recreated account")
		}
	})
	for _, mode := range []string{"employee_replaced", "employee_disabled", "employee_expired", "client_revoked", "delivery_revoked"} {
		t.Run(mode, func(t *testing.T) {
			f := newContextFixture(t)
			ticket, _ := f.prepareContext(t, "alice", 0)
			f.activate(t, "alice", ticket, f.take(t, "alice", ticket))
			p := f.people["alice"]
			var e error
			switch mode {
			case "employee_replaced":
				data := make([]byte, 32)
				_, _ = rand.Read(data)
				_, e = f.admin.Exec(f.ctx, "UPDATE mcpguard.principals SET token_hash=$3 WHERE org_id=$1 AND id=$2", p.OrgID, p.ID, data)
			case "employee_disabled":
				_, e = f.admin.Exec(f.ctx, "UPDATE mcpguard.principals SET enabled=false WHERE org_id=$1 AND id=$2", p.OrgID, p.ID)
			case "employee_expired":
				_, e = f.admin.Exec(f.ctx, "UPDATE mcpguard.principals SET expires_at=clock_timestamp()-interval '1 second' WHERE org_id=$1 AND id=$2", p.OrgID, p.ID)
			case "client_revoked":
				_, e = f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.credential_clients SET state='REVOKED',revision=revision+1 WHERE org_id=$1 AND client_id=$2", p.OrgID, f.views["alice"].ClientInstanceID)
			case "delivery_revoked":
				_, e = f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.delivery_authorizations SET state='REVOKED',revision=revision+1 WHERE org_id=$1 AND subject_id=$2 AND state='ACTIVE'", p.OrgID, p.ID)
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.contexts.CheckActivation(f.ctx, p, f.proofs["alice"], *ticket.ResourceID, ticket.CredentialID); !errors.Is(e, ErrDenied) {
				t.Fatal("independent identity invalidation bypassed")
			}
			if _, e = f.contexts.Renew(f.ctx, p, f.proofs["alice"], v02.ContextRenewInput{ExpectedRevision: "1", RequestKey: uuid.NewString()}, false); e == nil {
				t.Fatal("invalid identity renewed context")
			}
		})
	}
	t.Run("concurrent_package_renewal_one_successor_no_account", func(t *testing.T) {
		f := newContextFixture(t)
		ticket, _ := f.prepareContext(t, "alice", 0)
		var wg sync.WaitGroup
		var failed atomic.Int32
		ids := make(chan string, 6)
		in := v02.RenewCredentialInput{CredentialID: ticket.CredentialID, RequestKey: uuid.NewString()}
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				out, e := f.contexts.RenewCredential(f.ctx, f.people["alice"], f.proofs["alice"], in)
				if e != nil {
					failed.Add(1)
				} else {
					ids <- out.CredentialID
				}
			}()
		}
		wg.Wait()
		close(ids)
		first := ""
		for id := range ids {
			if first == "" {
				first = id
			} else if id != first {
				t.Fatal("renewal produced multiple successors")
			}
		}
		if failed.Load() != 0 || f.count(t, "credential_renewals") != 1 || f.count(t, "credential_packages") != 2 || f.count(t, "native_accounts") != 1 {
			t.Fatal("concurrent renewal not atomic")
		}
	})
}

func TestContextLateValidationIntegration(t *testing.T) {
	f := newContextFixture(t)
	ticket, _ := f.prepareContext(t, "alice", 0)
	blob := f.take(t, "alice", ticket)
	f.contexts.Downloads.Credentials.Keys = &faultPackageKeys{PackageKeyProvider: f.keys, beforeResolve: func() {
		_, e := f.contexts.Renew(context.Background(), f.people["alice"], f.proofs["alice"], v02.ContextRenewInput{ExpectedRevision: "1", RequestKey: uuid.NewString()}, true)
		if e != nil {
			t.Fatal(e)
		}
	}}
	if _, e := f.contexts.Activate(f.ctx, f.people["alice"], f.proofs["alice"], activationInput(t, ticket, blob)); !errors.Is(e, ErrDenied) {
		t.Fatal("context closed during decryption gained activation")
	}
	if f.count(t, "context_activation_versions") != 0 {
		t.Fatal("late activation partially committed")
	}
}
