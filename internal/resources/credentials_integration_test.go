package resources

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

type credentialFixture struct {
	*provisionFixture
	service *CredentialService
	config  v02.CredentialConfiguration
	keys    PackageKeyProvider
}

func newCredentialFixture(t *testing.T) *credentialFixture {
	t.Helper()
	path := os.Getenv("MCPGUARD_RA_CONFIG")
	if path == "" {
		t.Skip("explicit RA-07 fixture required")
	}
	raw, e := os.ReadFile(path)
	var scene struct {
		Credential bool `json:"raCredentialPhase"`
	}
	if e != nil || json.Unmarshal(raw, &scene) != nil {
		t.Fatal("invalid private fixture")
	}
	if !scene.Credential {
		t.Skip("explicit RA-07 fixture required")
	}
	f := newProvisionFixture(t)
	keyBytes := make([]byte, 32)
	if _, e = rand.Read(keyBytes); e != nil {
		t.Fatal(e)
	}
	key, _ := packageKey(keyBytes)
	org := f.people["alice"].OrgID
	issuer := v02.CredentialIssuer{OrgID: org, Issuer: "mcpguard.test", IssuerEpoch: uuid.NewString(), KeyID: "reference.v1", KeyFingerprint: key.Fingerprint(), Version: "1", Enabled: true}
	conf := v02.CredentialConfiguration{SchemaVersion: "0.2", Issuer: issuer, Policies: []v02.CredentialLeasePolicy{}}
	for _, p := range f.bundle.Profiles {
		conf.Policies = append(conf.Policies, v02.CredentialLeasePolicy{OrgID: org, ResourceID: p.ResourceID, ResourceVersion: "1", ProfileID: p.ProfileID, ProfileVersion: p.Version, Version: "1", MaxTtlSeconds: 600, Enabled: true})
	}
	raw, _ = json.Marshal(conf)
	if e = ImportCredentials(f.ctx, f.dsn, raw); e != nil {
		t.Fatal(e)
	}
	store, e := metadata.Open(f.ctx, os.Getenv("MCPGUARD_TEST_CREDENTIAL_DSN"), f.s.Catalog.Store.Vault)
	if e != nil {
		t.Fatal("credential runtime unavailable")
	}
	t.Cleanup(store.Close)
	catalog, e := NewCatalog(f.ctx, store)
	if e != nil {
		t.Fatal(e)
	}
	keys := &filePackageKeys{map[string]PackageKey{org + "/" + issuer.Issuer + "/" + issuer.KeyID: key}}
	service, e := NewCredentialService(catalog, keys)
	if e != nil {
		t.Fatal(e)
	}
	return &credentialFixture{f, service, conf, keys}
}
func (f *credentialFixture) apply(t *testing.T) {
	t.Helper()
	raw, _ := json.Marshal(f.config)
	if e := ImportCredentials(f.ctx, f.dsn, raw); e != nil {
		t.Fatal(e)
	}
}
func (f *credentialFixture) issue(t *testing.T, name string, index int) (v02.CredentialStatus, v02.AccessStatusResult) {
	t.Helper()
	out := f.approved(t, name, index)
	f.run(t, out, "VERIFIED")
	status, e := f.service.Issue(f.ctx, f.people[name], v02.IssueCredentialInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()})
	if e != nil {
		t.Fatal("issue failed: ", e)
	}
	return status, out
}
func (f *credentialFixture) blob(t *testing.T, cid string) []byte {
	t.Helper()
	var raw []byte
	if e := f.admin.QueryRow(f.ctx, "SELECT envelope FROM mcpguard_ra.credential_blobs WHERE credential_id=$1", cid).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	return raw
}
func (f *credentialFixture) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if f.admin.QueryRow(f.ctx, "SELECT count(*) FROM mcpguard_ra."+table+" WHERE org_id=$1", f.people["alice"].OrgID).Scan(&n) != nil {
		t.Fatal("fixture count failed")
	}
	return n
}
func revokeInput(status v02.CredentialStatus) v02.RevokeCredentialInput {
	return v02.RevokeCredentialInput{CredentialID: status.CredentialID, ExpectedRevision: status.Revision, RequestKey: uuid.NewString(), Reason: "bounded test revocation"}
}

func TestCredentialPackagesIntegration(t *testing.T) {
	t.Run("two_resources_two_people_server_only_references", func(t *testing.T) {
		f := newCredentialFixture(t)
		for index := 0; index < 2; index++ {
			for _, name := range []string{"alice", "bob"} {
				status, out := f.issue(t, name, index)
				blob := f.blob(t, status.CredentialID)
				checked, e := f.service.Validate(f.ctx, f.people[name], out.Grant.ResourceID, status.CredentialID, blob)
				if e != nil || checked != status {
					t.Fatal("reference validation failed: ", e)
				}
				env, n, c, e := parsePackage(blob)
				if e != nil {
					t.Fatal(e)
				}
				key, _ := f.keys.Resolve(f.ctx, out.Request.OrgID, env.Issuer, env.KeyID)
				ref, e := openPackage(f.ctx, key, env, n, c)
				if e != nil {
					t.Fatal(e)
				}
				plain, _ := json.Marshal(ref)
				for _, s := range []string{"password", "secretRef", "nativeIdentity", "dsn", "refreshToken", "keyUrl"} {
					if strings.Contains(string(plain), s) {
						t.Fatal("reference contains secret or native selector")
					}
				}
				public, _ := json.Marshal(checked)
				if strings.Contains(string(public), env.Ciphertext) || strings.Contains(string(public), "issuerEpoch") {
					t.Fatal("public status leaked envelope/reference")
				}
				if _, e = f.service.Validate(f.ctx, f.people[map[string]string{"alice": "bob", "bob": "alice"}[name]], out.Grant.ResourceID, status.CredentialID, blob); !errors.Is(e, ErrCredential) {
					t.Fatal("copied package crossed caller")
				}
				if _, e = f.service.Validate(f.ctx, f.people[name], f.bundle.Resources[1-index].ResourceID, status.CredentialID, blob); !errors.Is(e, ErrCredential) {
					t.Fatal("package crossed resource")
				}
			}
		}
		if f.count(t, "credential_leases") != 4 || f.count(t, "credential_packages") != 4 || f.count(t, "credential_blobs") != 4 {
			t.Fatal("package/lease/blob commit lost")
		}
	})
	t.Run("nonactive_and_cross_owner_grants_do_not_issue", func(t *testing.T) {
		f := newCredentialFixture(t)
		out := f.approved(t, "alice", 0)
		if _, e := f.service.Issue(f.ctx, f.people["alice"], v02.IssueCredentialInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()}); !errors.Is(e, ErrDenied) {
			t.Fatal("PROVISIONING grant issued package")
		}
		f.run(t, out, "VERIFIED")
		if _, e := f.service.Issue(f.ctx, f.people["bob"], v02.IssueCredentialInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()}); !errors.Is(e, ErrDenied) {
			t.Fatal("grant owner substituted")
		}
		if f.count(t, "credential_nonce_reservations") != 0 {
			t.Fatal("unauthorized issue reserved crypto material")
		}
	})
	t.Run("concurrent_idempotency_commits_one_package", func(t *testing.T) {
		f := newCredentialFixture(t)
		out := f.approved(t, "alice", 0)
		f.run(t, out, "VERIFIED")
		in := v02.IssueCredentialInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()}
		var wg sync.WaitGroup
		results := make(chan v02.CredentialStatus, 8)
		fail := make(chan error, 8)
		for n := 0; n < 8; n++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r, e := f.service.Issue(f.ctx, f.people["alice"], in)
				if e != nil {
					fail <- e
				} else {
					results <- r
				}
			}()
		}
		wg.Wait()
		close(results)
		close(fail)
		for e := range fail {
			t.Fatal(e)
		}
		var first v02.CredentialStatus
		for r := range results {
			if first.CredentialID == "" {
				first = r
			} else if r != first {
				t.Fatal("idempotent issue changed identity")
			}
		}
		if f.count(t, "credential_packages") != 1 {
			t.Fatal("concurrent issue duplicated package")
		}
		second := f.approved(t, "alice", 1)
		f.run(t, second, "VERIFIED")
		in.GrantID = second.Grant.GrantID
		if _, e := f.service.Issue(f.ctx, f.people["alice"], in); !errors.Is(e, ErrIdempotency) {
			t.Fatal("same key accepted a different grant")
		}
	})
	t.Run("revoke_is_terminal_and_does_not_affect_other_package", func(t *testing.T) {
		f := newCredentialFixture(t)
		one, out := f.issue(t, "alice", 0)
		two, e := f.service.Issue(f.ctx, f.people["alice"], v02.IssueCredentialInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()})
		if e != nil {
			t.Fatal(e)
		}
		in := revokeInput(one)
		closed, e := f.service.Revoke(f.ctx, f.people["alice"], in)
		if e != nil || closed.State != "REVOKED" || closed.Revision != "2" {
			t.Fatal("revoke did not close lease")
		}
		replay, e := f.service.Revoke(f.ctx, f.people["alice"], in)
		if e != nil || replay != closed {
			t.Fatal("revoke replay changed result")
		}
		if _, e = f.service.Validate(f.ctx, f.people["alice"], out.Grant.ResourceID, one.CredentialID, f.blob(t, one.CredentialID)); !errors.Is(e, ErrCredential) {
			t.Fatal("revoked package used")
		}
		if _, e = f.service.Validate(f.ctx, f.people["alice"], out.Grant.ResourceID, two.CredentialID, f.blob(t, two.CredentialID)); e != nil {
			t.Fatal("unrelated package was revoked")
		}
		if _, e = f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.credential_leases SET state='ACTIVE',revision=revision+1 WHERE credential_id=$1", one.CredentialID); e == nil {
			t.Fatal("lease revived")
		}
	})
	t.Run("expiry_is_bounded_by_policy_and_grant", func(t *testing.T) {
		f := newCredentialFixture(t)
		f.config.Policies[0].Version = "2"
		f.config.Policies[0].MaxTtlSeconds = 1
		f.apply(t)
		status, out := f.issue(t, "alice", 0)
		expiry, _, _ := normalizedTime(status.ExpiresAt)
		grantExpiry, _, _ := normalizedTime(out.Grant.ExpiresAt)
		if expiry.After(grantExpiry) {
			t.Fatal("lease outlives grant")
		}
		f.awaitDeadline(t, status.ExpiresAt)
		closed, e := f.service.Status(f.ctx, f.people["alice"], v02.CredentialIDInput{CredentialID: status.CredentialID})
		if e != nil || closed.State != "EXPIRED" || closed.Revision != "2" {
			t.Fatal("expired lease did not converge")
		}
		if _, e = f.service.Validate(f.ctx, f.people["alice"], out.Grant.ResourceID, status.CredentialID, f.blob(t, status.CredentialID)); !errors.Is(e, ErrCredential) {
			t.Fatal("expired package accepted")
		}
	})
	t.Run("prepared_issue_time_waits_for_database_without_changing_reference", func(t *testing.T) {
		f := newCredentialFixture(t)
		p := f.people["alice"]
		out := f.approved(t, "alice", 0)
		f.run(t, out, "VERIFIED")
		in := v02.IssueCredentialInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()}
		prep, prior, err := f.service.prepareIssue(f.ctx, p, in)
		if err != nil || prep == nil || prior != nil {
			t.Fatal("prepare isolated credential: ", err)
		}
		// A future prepared timestamp models a database clock moving back
		// between reservation and persist without changing any system clocks.
		issued := f.now(t).UTC().Add(150 * time.Millisecond)
		prep.ref.IssuedAt = issued.Format(time.RFC3339Nano)
		blob, err := f.service.encryptPreparation(f.ctx, p, prep)
		if err != nil || f.now(t).Before(issued) {
			t.Fatal("prepared credential did not wait for its issue time: ", err)
		}
		status, err := f.service.finishIssue(f.ctx, p, in, prep, blob)
		if err != nil || status.CredentialID != prep.ref.CredentialID || status.ExpiresAt != prep.ref.ExpiresAt {
			t.Fatal("finish changed the prepared credential: ", err)
		}
		if _, err := f.service.Validate(f.ctx, p, out.Grant.ResourceID, status.CredentialID, blob); err != nil {
			t.Fatal("prepared reference could not be verified: ", err)
		}
		in.RequestKey = uuid.NewString()
		prep, prior, err = f.service.prepareIssue(f.ctx, p, in)
		if err != nil || prep == nil || prior != nil {
			t.Fatal("prepare second isolated credential: ", err)
		}
		prep.ref.IssuedAt = f.now(t).UTC().Add(10 * time.Second).Format(time.RFC3339Nano)
		ctx, cancel := context.WithTimeout(f.ctx, 100*time.Millisecond)
		defer cancel()
		if blob, err := f.service.encryptPreparation(ctx, p, prep); !errors.Is(err, ErrUnavailable) || blob != nil {
			t.Fatal("issue continued before the database reached the prepared time")
		}
		if f.count(t, "credential_leases") != 1 || f.count(t, "credential_packages") != 1 || f.count(t, "credential_nonce_reservations") != 2 {
			t.Fatal("timed-out issue persisted a new credential or released its nonce")
		}
	})
	for _, mode := range []string{"grant_revoked", "resource_disabled", "access_removed", "binding_disabled", "issuer_epoch_changed", "policy_disabled", "subject_disabled", "subject_expired", "token_replaced"} {
		t.Run(mode, func(t *testing.T) {
			f := newCredentialFixture(t)
			status, out := f.issue(t, "alice", 0)
			p := f.people["alice"]
			var e error
			switch mode {
			case "grant_revoked":
				var epoch int64
				_ = f.admin.QueryRow(f.ctx, "SELECT mcpguard_ra.advance_access_epoch($1)", p.OrgID).Scan(&epoch)
				_, e = f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.access_grant_heads SET state='REVOKED',revision=revision+1,auth_epoch=$3 WHERE org_id=$1 AND grant_id=$2", p.OrgID, out.Grant.GrantID, epoch)
			case "resource_disabled":
				_, e = f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.resource_heads SET enabled=false,revision=revision+1 WHERE org_id=$1 AND resource_id=$2", p.OrgID, out.Grant.ResourceID)
			case "access_removed":
				_, e = f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.resource_access SET can_request=false WHERE org_id=$1 AND principal_id=$2 AND resource_id=$3", p.OrgID, p.ID, out.Grant.ResourceID)
			case "binding_disabled":
				_, e = f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.identity_bindings SET enabled=false WHERE org_id=$1 AND binding_id=$2", p.OrgID, *recoveryStatus(t, f.provisionFixture, out).Grant.BindingID)
			case "issuer_epoch_changed":
				f.config.Issuer.Version = "2"
				f.config.Issuer.IssuerEpoch = uuid.NewString()
				f.apply(t)
			case "policy_disabled":
				f.config.Policies[0].Version = "2"
				f.config.Policies[0].Enabled = false
				f.apply(t)
			case "subject_disabled":
				_, e = f.admin.Exec(f.ctx, "UPDATE mcpguard.principals SET enabled=false WHERE org_id=$1 AND id=$2", p.OrgID, p.ID)
			case "subject_expired":
				_, e = f.admin.Exec(f.ctx, "UPDATE mcpguard.principals SET expires_at=clock_timestamp()-interval '1 second' WHERE org_id=$1 AND id=$2", p.OrgID, p.ID)
			case "token_replaced":
				_, e = f.admin.Exec(f.ctx, "UPDATE mcpguard.principals SET token_hash=decode(repeat('ab',32),'hex') WHERE org_id=$1 AND id=$2", p.OrgID, p.ID)
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.service.Validate(f.ctx, p, out.Grant.ResourceID, status.CredentialID, f.blob(t, status.CredentialID)); !errors.Is(e, ErrCredential) {
				t.Fatal("closed eligibility accepted: ", e)
			}
		})
	}
	t.Run("server_runtime_has_no_native_account_or_secret_privileges", func(t *testing.T) {
		f := newCredentialFixture(t)
		var safe bool
		e := f.service.Access.Catalog.Store.Pool.QueryRow(f.ctx, `SELECT NOT has_table_privilege(current_user,'mcpguard_ra.provisioning_jobs','INSERT') AND NOT has_table_privilege(current_user,'mcpguard_ra.native_secrets','SELECT') AND NOT has_table_privilege(current_user,'mcpguard_ra.credential_issuers','UPDATE') AND NOT has_table_privilege(current_user,'mcpguard_ra.credential_nonce_reservations','DELETE')`).Scan(&safe)
		if e != nil || !safe {
			t.Fatal("credential runtime gained native/configuration rights")
		}
	})
}

type faultPackageKeys struct {
	PackageKeyProvider
	beforeResolve func()
	fail          bool
	calls         atomic.Int32
}

func (f *faultPackageKeys) Resolve(ctx context.Context, org, issuer, keyID string) (PackageKey, error) {
	f.calls.Add(1)
	if f.beforeResolve != nil {
		f.beforeResolve()
	}
	if f.fail {
		return nil, ErrUnavailable
	}
	return f.PackageKeyProvider.Resolve(ctx, org, issuer, keyID)
}
func TestCredentialFailureAndRaceIntegration(t *testing.T) {
	t.Run("unavailable_keys_burn_nonce_without_package", func(t *testing.T) {
		f := newCredentialFixture(t)
		out := f.approved(t, "alice", 0)
		f.run(t, out, "VERIFIED")
		keys := &faultPackageKeys{PackageKeyProvider: f.keys, fail: true}
		f.service.Keys = keys
		in := v02.IssueCredentialInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()}
		if _, e := f.service.Issue(f.ctx, f.people["alice"], in); !errors.Is(e, ErrUnavailable) {
			t.Fatal("key outage failed open")
		}
		if f.count(t, "credential_packages") != 0 || f.count(t, "credential_nonce_reservations") != 1 {
			t.Fatal("failed crypto created a usable package or recycled nonce")
		}
		keys.fail = false
		status, e := f.service.Issue(f.ctx, f.people["alice"], in)
		if e != nil || status.State != "ACTIVE" || f.count(t, "credential_nonce_reservations") != 2 {
			t.Fatal("retry reused failed preparation")
		}
		keys.fail = true
		if _, e = f.service.Validate(f.ctx, f.people["alice"], out.Grant.ResourceID, status.CredentialID, f.blob(t, status.CredentialID)); !errors.Is(e, ErrUnavailable) {
			t.Fatal("validation used cached key on outage")
		}
		if current, e := f.service.Status(f.ctx, f.people["alice"], v02.CredentialIDInput{CredentialID: status.CredentialID}); e != nil || current != status {
			t.Fatal("key outage prevented non-secret status")
		}
		if current, e := f.service.Revoke(f.ctx, f.people["alice"], revokeInput(status)); e != nil || current.State != "REVOKED" {
			t.Fatal("key outage prevented lease revocation")
		}
	})
	t.Run("revocation_during_encryption_prevents_commit", func(t *testing.T) {
		f := newCredentialFixture(t)
		out := f.approved(t, "alice", 0)
		f.run(t, out, "VERIFIED")
		f.service.Keys = &faultPackageKeys{PackageKeyProvider: f.keys, beforeResolve: func() {
			if _, e := f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.resource_access SET can_request=false WHERE org_id=$1 AND principal_id=$2 AND resource_id=$3", out.Request.OrgID, out.Grant.SubjectID, out.Grant.ResourceID); e != nil {
				t.Fatal(e)
			}
		}}
		if _, e := f.service.Issue(f.ctx, f.people["alice"], v02.IssueCredentialInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()}); !errors.Is(e, ErrDenied) {
			t.Fatal("late encryption committed package")
		}
		if f.count(t, "credential_packages") != 0 || f.count(t, "credential_nonce_reservations") != 1 {
			t.Fatal("failed final qualification left a usable package")
		}
	})
	t.Run("revocation_during_decryption_prevents_validation", func(t *testing.T) {
		f := newCredentialFixture(t)
		status, out := f.issue(t, "alice", 0)
		f.service.Keys = &faultPackageKeys{PackageKeyProvider: f.keys, beforeResolve: func() {
			if _, e := f.service.Revoke(f.ctx, f.people["alice"], revokeInput(status)); e != nil {
				t.Fatal(e)
			}
		}}
		if _, e := f.service.Validate(f.ctx, f.people["alice"], out.Grant.ResourceID, status.CredentialID, f.blob(t, status.CredentialID)); !errors.Is(e, ErrCredential) {
			t.Fatal("late decryption bypassed revocation")
		}
	})
	t.Run("nonce_collision_does_not_encrypt_or_commit", func(t *testing.T) {
		f := newCredentialFixture(t)
		out := f.approved(t, "alice", 0)
		f.run(t, out, "VERIFIED")
		fixed := []byte("abcdefghijkl")
		f.service.nonce = func() ([]byte, error) { return fixed, nil }
		if _, e := f.service.Issue(f.ctx, f.people["alice"], v02.IssueCredentialInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()}); e != nil {
			t.Fatal(e)
		}
		keyCalls := &faultPackageKeys{PackageKeyProvider: f.keys}
		f.service.Keys = keyCalls
		if _, e := f.service.Issue(f.ctx, f.people["alice"], v02.IssueCredentialInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()}); !errors.Is(e, ErrUnavailable) {
			t.Fatal("reserved nonce reused")
		}
		if keyCalls.calls.Load() != 0 || f.count(t, "credential_packages") != 1 {
			t.Fatal("nonce collision reached encryption")
		}
	})
	t.Run("critical_issuance_audit_failure_is_atomic", func(t *testing.T) {
		f := newCredentialFixture(t)
		out := f.approved(t, "alice", 0)
		f.run(t, out, "VERIFIED")
		suffix := strings.ReplaceAll(out.Request.OrgID, "-", "")
		sql := `CREATE FUNCTION mcpguard_ra.fail_package_` + suffix + `() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.org_id='` + out.Request.OrgID + `' AND NEW.event_type='PACKAGE_ISSUED' THEN RAISE EXCEPTION 'injected issue audit failure';END IF;RETURN NEW;END $$;CREATE TRIGGER fail_package_` + suffix + ` BEFORE INSERT ON mcpguard_ra.credential_audit FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.fail_package_` + suffix + `();`
		if _, e := f.admin.Exec(f.ctx, sql); e != nil {
			t.Fatal("audit fault setup failed")
		}
		if _, e := f.service.Issue(f.ctx, f.people["alice"], v02.IssueCredentialInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()}); e == nil {
			t.Fatal("unaudited issuance committed")
		}
		if f.count(t, "credential_packages") != 0 || f.count(t, "credential_leases") != 0 || f.count(t, "credential_blobs") != 0 || f.count(t, "credential_commands") != 0 || f.count(t, "credential_nonce_reservations") != 1 {
			t.Fatal("issuance rollback lost atomicity")
		}
	})
}
