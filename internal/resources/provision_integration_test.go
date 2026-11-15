package resources

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

type provisionFixture struct {
	*accessFixture
	worker  *Provisioner
	private *ProviderPrivate
	config  v02.ProvisioningConfiguration
	native  []*pgx.Conn
	admins  []string
}

func newProvisionFixture(t *testing.T) *provisionFixture {
	t.Helper()
	path := os.Getenv("MCPGUARD_RA_CONFIG")
	if path == "" {
		t.Skip("explicit RA-05 fixture required")
	}
	raw, e := os.ReadFile(path)
	var scene struct {
		Provision bool `json:"raProvisionPhase"`
		PG        []struct {
			DSN string `json:"dsn"`
		} `json:"raPgAdmins"`
	}
	if e != nil || json.Unmarshal(raw, &scene) != nil {
		t.Fatal("invalid private fixture")
	}
	if !scene.Provision {
		t.Skip("explicit RA-05 fixture required")
	}
	f := newAccessFixture(t, 3600)
	private, e := LoadProviderPrivate(os.Getenv("MCPGUARD_RA_PROVISION_CONFIG"))
	if e != nil {
		t.Fatal("provider config unavailable")
	}
	ws, e := metadata.Open(f.ctx, os.Getenv("MCPGUARD_TEST_PROVISIONER_DSN"), f.s.Catalog.Store.Vault)
	if e != nil {
		t.Fatal("worker metadata unavailable")
	}
	t.Cleanup(ws.Close)
	c, e := NewCatalog(f.ctx, ws)
	if e != nil {
		t.Fatal(e)
	}
	w, e := NewProvisioner(c, private, &PGProvisioningProvider{private})
	if e != nil {
		t.Fatal(e)
	}
	out := &provisionFixture{accessFixture: f, worker: w, private: private, config: v02.ProvisioningConfiguration{SchemaVersion: "0.2", Targets: []v02.ProvisioningTarget{}, ExistingAccounts: []v02.ExistingAccountRegistration{}}}
	for i, p := range f.bundle.Profiles {
		// Write profiles carry their own target and must not borrow a read scene slot.
		if p.Name != "isolated-read" || i >= len(scene.PG) {
			continue
		}
		r := f.bundle.Resources[i]
		target := v02.ProvisioningTarget{OrgID: r.OrgID, ResourceID: r.ResourceID, ResourceVersion: r.Version, ProfileID: p.ProfileID, ProfileVersion: p.Version, TargetID: uuid.NewString(), EndpointConfigRef: r.EndpointConfigRef, TargetFingerprint: r.TargetFingerprint, Template: "PG_OWNER_SELECT_V1", Version: "1", Enabled: true}
		out.config.Targets = append(out.config.Targets, target)
		if InstallPGProvider(f.ctx, scene.PG[i].DSN, "mcpguard_account_manager", []v02.ProvisioningTarget{target}) != nil {
			t.Fatal("native provider installation failed")
		}
		conn, e := pgx.Connect(f.ctx, scene.PG[i].DSN)
		if e != nil {
			t.Fatal("native admin unavailable")
		}
		t.Cleanup(func() { conn.Close(f.ctx) })
		out.native = append(out.native, conn)
		out.admins = append(out.admins, scene.PG[i].DSN)
		// Both targets have a human reviewer; the RA-04 base intentionally had only one.
		if i > 0 {
			f.config.Reviewers = append(f.config.Reviewers, v02.AccessReviewer{OrgID: r.OrgID, ResourceID: r.ResourceID, PrincipalID: f.people["reviewer"].ID, CanApprove: true})
		}
	}
	raw, _ = json.Marshal(f.config)
	if ImportAccess(f.ctx, f.dsn, raw) != nil {
		t.Fatal("reviewer configuration failed")
	}
	out.apply(t)
	return out
}
func (f *provisionFixture) apply(t *testing.T) {
	t.Helper()
	raw, _ := json.Marshal(f.config)
	if e := ImportProvisioning(f.ctx, f.dsn, raw); e != nil {
		t.Fatal(e)
	}
}
func (f *provisionFixture) approved(t *testing.T, name string, index int) v02.AccessStatusResult {
	t.Helper()
	input := f.input()
	input.ResourceID = f.bundle.Resources[index].ResourceID
	input.ProfileID = f.bundle.Profiles[index].ProfileID
	r, e := f.s.Request(f.ctx, f.people[name], input)
	if e != nil {
		t.Fatal(e)
	}
	out, e := f.s.Decide(f.ctx, f.people["reviewer"], r.RequestID, approveInput(r))
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func (f *provisionFixture) run(t *testing.T, out v02.AccessStatusResult, want string) v02.AccessStatusResult {
	t.Helper()
	job, e := f.worker.Run(f.ctx, out.Request.OrgID, out.Grant.GrantID)
	if e != nil {
		t.Fatal("worker run: ", e)
	}
	if job.State != want {
		t.Fatalf("job state %s, expected %s", job.State, want)
	}
	p := f.people["alice"]
	if out.Grant.SubjectID != p.ID {
		p = f.people["bob"]
	}
	status, e := f.s.Status(f.ctx, p, out.Request.RequestID)
	if e != nil {
		t.Fatal(e)
	}
	return status
}
func (f *provisionFixture) existing(t *testing.T, index int, name string, alter string) NativeSecret {
	t.Helper()
	id := uuid.NewString()
	identity := "existing_" + strings.ReplaceAll(id, "-", "")
	password := strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")
	role := pgx.Identifier{identity}.Sanitize()
	sql := "CREATE ROLE " + role + " LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '" + password + "';GRANT USAGE ON SCHEMA resource_demo TO " + role + ";GRANT SELECT ON resource_demo.items TO " + role + ";"
	if alter != "" {
		sql += strings.ReplaceAll(alter, "ROLE_NAME", role)
	}
	if _, e := f.native[index].Exec(f.ctx, sql); e != nil {
		t.Fatal("existing account setup failed")
	}
	target := f.config.Targets[index]
	ref := "existing." + strings.ReplaceAll(id, "-", "")
	f.config.ExistingAccounts = append(f.config.ExistingAccounts, v02.ExistingAccountRegistration{OrgID: target.OrgID, SubjectID: f.people[name].ID, ResourceID: target.ResourceID, ResourceVersion: target.ResourceVersion, ProfileID: target.ProfileID, ProfileVersion: target.ProfileVersion, AccountID: id, NativeIdentity: identity, SecretRef: ref, SecretVersion: "1", Version: "1", Enabled: true})
	secret := NativeSecret{Username: identity, Password: password, Version: "1"}
	f.private.ExistingSecrets[ref+"@1"] = secret
	f.apply(t)
	return secret
}

func TestNativeProvisioningIntegration(t *testing.T) {
	t.Run("verified_receipt_activation_transaction", func(t *testing.T) {
		f := newProvisionFixture(t)
		out := f.approved(t, "alice", 0)
		work, _, e := f.worker.prepare(f.ctx, out.Request.OrgID, out.Grant.GrantID)
		if e != nil {
			t.Fatal(e)
		}
		if e = f.worker.Provider.Create(f.ctx, work.Target, work.Plan, work.Hash, work.Secret); e != nil {
			t.Fatal(e)
		}
		observation, e := f.worker.Provider.Verify(f.ctx, work.Plan, work.Secret)
		if e != nil {
			t.Fatal(e)
		}
		if e = f.worker.finish(f.ctx, work, observation); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("two_targets_two_people_and_read_loop", func(t *testing.T) {
		f := newProvisionFixture(t)
		names := map[string]bool{}
		provider := &VaultSecretProvider{Catalog: f.s.Catalog, Endpoints: f.private.Endpoints}
		reader := NewReadManager(f.s.Catalog, provider)
		defer reader.Close()
		for index := 0; index < 2; index++ {
			for _, name := range []string{"alice", "bob"} {
				approved := f.approved(t, name, index)
				active := f.run(t, approved, "VERIFIED")
				if active.Grant.State != "ACTIVE" || active.Grant.GrantVersion != "2" || active.Grant.AccountID == nil || active.Grant.BindingID == nil || active.Provisioning == nil || active.Provisioning.State != "VERIFIED" {
					t.Fatal("verified native activation missing")
				}
				snap, e := f.s.Catalog.Snapshot(f.ctx, f.people[name], active.Grant.ResourceID)
				if e != nil {
					t.Fatal(e)
				}
				if names[snap.Binding.NativeIdentity] {
					t.Fatal("shared individual native identity")
				}
				names[snap.Binding.NativeIdentity] = true
				if _, e = f.native[index].Exec(f.ctx, "INSERT INTO resource_demo.items VALUES($1,'item_demo_individual',$2,1)", snap.Binding.NativeIdentity, name); e != nil {
					t.Fatal("native row setup failed")
				}
				result, e := reader.ReadFixture(f.ctx, f.people[name], v02.FixtureReadInput{ResourceID: snap.Resource.ResourceID, ItemID: "item_demo_individual"})
				if e != nil || !result.Found || len(result.Items) != 1 || result.Items[0].Note != name || result.NativeIdentity != snap.Binding.NativeIdentity {
					t.Fatal("server-only decrypted individual native read failed: ", e)
				}
				hidden, e := reader.ReadFixture(f.ctx, f.people[name], v02.FixtureReadInput{ResourceID: snap.Resource.ResourceID, ItemID: "item_demo_bob_only"})
				if e != nil || hidden.Found {
					t.Fatal("RLS crossed native identity")
				}
				ep, secret, e := provider.Resolve(f.ctx, snap.Resource.EndpointConfigRef, snap.Binding.SecretRef, "1")
				if e != nil {
					t.Fatal("server vault failed")
				}
				cfg, _ := nativeConfig(ep, secret)
				conn, e := pgx.ConnectConfig(f.ctx, cfg)
				if e != nil {
					t.Fatal("native login unavailable")
				}
				for _, sql := range []string{"SET default_transaction_read_only=off; CREATE ROLE must_not_exist", "GRANT SELECT ON resource_demo.items TO PUBLIC", "SET ROLE ra_bob", "UPDATE resource_demo.items SET note='changed'"} {
					if _, e = conn.Exec(f.ctx, sql); e == nil {
						t.Fatal("native query identity escaped template")
					}
				}
				conn.Close(f.ctx)
				var count int
				var oldNull bool
				var cipher []byte
				if f.admin.QueryRow(f.ctx, "SELECT count(*),bool_and(account_id IS NULL) FILTER(WHERE version=1) FROM mcpguard_ra.access_grant_versions WHERE org_id=$1 AND grant_id=$2", active.Request.OrgID, active.Grant.GrantID).Scan(&count, &oldNull) != nil || count != 2 || !oldNull {
					t.Fatal("approved grant history overwritten")
				}
				if f.admin.QueryRow(f.ctx, "SELECT ciphertext FROM mcpguard_ra.native_secrets WHERE org_id=$1 AND account_id=$2", active.Request.OrgID, *active.Grant.AccountID).Scan(&cipher) != nil || strings.Contains(string(cipher), secret.Password) {
					t.Fatal("plaintext native secret stored")
				}
				replay, e := f.worker.Run(f.ctx, active.Request.OrgID, active.Grant.GrantID)
				if e != nil || replay.JobID != active.Provisioning.JobID || replay.State != "VERIFIED" {
					t.Fatal("worker replay dispatched again")
				}
				grants, e := f.s.Grants(f.ctx, f.people[name], v02.ListGrantsInput{})
				if e != nil || len(grants.Grants) != index+1 {
					t.Fatal("grant versions duplicated in view")
				}
			}
		}
		if reader.PoolCount() != 4 {
			t.Fatal("native pools were shared across people or targets")
		}
		snap, e := f.s.Catalog.Snapshot(f.ctx, f.people["bob"], f.bundle.Resources[1].ResourceID)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.native[1].Exec(f.ctx, "GRANT UPDATE(note) ON resource_demo.items TO "+pgx.Identifier{snap.Binding.NativeIdentity}.Sanitize()); e != nil {
			t.Fatal("drift setup failed")
		}
		if _, e = reader.ReadFixture(f.ctx, f.people["bob"], v02.FixtureReadInput{ResourceID: snap.Resource.ResourceID, ItemID: "item_demo_individual"}); e == nil {
			t.Fatal("native privilege drift survived connection reuse")
		}
	})
	t.Run("management_credentials_are_separate_and_restricted", func(t *testing.T) {
		f := newProvisionFixture(t)
		if _, e := NewProvisioner(f.s.Catalog, f.private, &PGProvisioningProvider{f.private}); !errors.Is(e, ErrDenied) {
			t.Fatal("gateway constructed privileged worker")
		}
		if _, e := f.s.Catalog.Store.Pool.Exec(f.ctx, "INSERT INTO mcpguard_ra.provisioning_jobs DEFAULT VALUES"); e == nil {
			t.Fatal("gateway inserted provisioning job")
		}
		for _, dsn := range f.private.Managers {
			c, e := pgx.Connect(f.ctx, dsn)
			if e != nil {
				t.Fatal("manager unavailable")
			}
			for _, sql := range []string{"CREATE ROLE must_not_exist", "GRANT SELECT ON resource_demo.items TO PUBLIC", "SELECT * FROM resource_demo.items", "ALTER FUNCTION mcpguard_provision.disable_account(uuid,text) SECURITY INVOKER"} {
				if _, e = c.Exec(f.ctx, sql); e == nil {
					t.Fatal("manager obtained arbitrary native privileges")
				}
			}
			c.Close(f.ctx)
		}
		approved := f.approved(t, "alice", 0)
		active := f.run(t, approved, "VERIFIED")
		var worker string
		if f.admin.QueryRow(f.ctx, "SELECT worker_identity FROM mcpguard_ra.provision_audit WHERE org_id=$1 AND event_type='NATIVE_VERIFIED_GRANT_ACTIVE'", active.Request.OrgID).Scan(&worker) != nil || worker != "mcpguard_provisioner" {
			t.Fatal("audit impersonated requester")
		}
	})
	t.Run("registered_preexisting_account_is_verified_without_password_change", func(t *testing.T) {
		f := newProvisionFixture(t)
		secret := f.existing(t, 0, "alice", "")
		var before string
		_ = f.native[0].QueryRow(f.ctx, "SELECT rolpassword FROM pg_authid WHERE rolname=$1", secret.Username).Scan(&before)
		active := f.run(t, f.approved(t, "alice", 0), "VERIFIED")
		if active.Grant.State != "ACTIVE" {
			t.Fatal("existing account not activated")
		}
		var after string
		_ = f.native[0].QueryRow(f.ctx, "SELECT rolpassword FROM pg_authid WHERE rolname=$1", secret.Username).Scan(&after)
		if after != before {
			t.Fatal("existing credential altered")
		}
		var mode string
		_ = f.admin.QueryRow(f.ctx, "SELECT management_mode FROM mcpguard_ra.native_accounts WHERE org_id=$1 AND account_id=$2", active.Request.OrgID, *active.Grant.AccountID).Scan(&mode)
		if mode != "PREEXISTING" {
			t.Fatal("ownership lost")
		}
	})
	for _, tc := range []struct{ name, sql string }{
		{"bypassrls", "ALTER ROLE ROLE_NAME BYPASSRLS;"},
		{"role_membership", "GRANT ra_bob TO ROLE_NAME;"},
		{"column_write", "GRANT UPDATE(note) ON resource_demo.items TO ROLE_NAME;"},
		{"schema_create", "GRANT CREATE ON SCHEMA resource_demo TO ROLE_NAME;"},
		{"management_execute", "GRANT USAGE ON SCHEMA mcpguard_provision TO ROLE_NAME;GRANT EXECUTE ON FUNCTION mcpguard_provision.disable_account(uuid,text) TO ROLE_NAME;"},
		{"wrong_password", ""},
	} {
		t.Run("reject_existing_"+tc.name, func(t *testing.T) {
			f := newProvisionFixture(t)
			secret := f.existing(t, 0, "alice", tc.sql)
			if tc.name == "wrong_password" {
				for ref, s := range f.private.ExistingSecrets {
					s.Password = strings.Repeat("x", 43)
					f.private.ExistingSecrets[ref] = s
				}
			}
			status := f.run(t, f.approved(t, "alice", 0), "FAILED")
			if status.Grant.State != "PROVISIONING_FAILED" || status.Grant.AccountID != nil {
				t.Fatal("unsafe existing account activated")
			}
			var login bool
			_ = f.native[0].QueryRow(f.ctx, "SELECT rolcanlogin FROM pg_roles WHERE rolname=$1", secret.Username).Scan(&login)
			if !login {
				t.Fatal("preexisting account disabled on verification failure")
			}
		})
	}
	t.Run("physical_identity_cannot_be_shared", func(t *testing.T) {
		f := newProvisionFixture(t)
		f.existing(t, 0, "alice", "")
		f.run(t, f.approved(t, "alice", 0), "VERIFIED")
		reg := f.config.ExistingAccounts[0]
		reg.SubjectID = f.people["bob"].ID
		reg.AccountID = uuid.NewString()
		f.config.ExistingAccounts = append(f.config.ExistingAccounts, reg)
		f.apply(t)
		out := f.approved(t, "bob", 0)
		if _, e := f.worker.Run(f.ctx, out.Request.OrgID, out.Grant.GrantID); !errors.Is(e, ErrConflict) {
			t.Fatal("native identity shared across people")
		}
	})
	t.Run("concurrent_worker_dispatches_once", func(t *testing.T) {
		f := newProvisionFixture(t)
		out := f.approved(t, "alice", 0)
		wrapped := &observedProvider{NativeProvisioningProvider: f.worker.Provider}
		f.worker.Provider = wrapped
		var wg sync.WaitGroup
		fail := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, e := f.worker.Run(f.ctx, out.Request.OrgID, out.Grant.GrantID)
				if e != nil {
					fail <- e
				}
			}()
		}
		wg.Wait()
		close(fail)
		for e := range fail {
			t.Fatal(e)
		}
		if wrapped.creates.Load() != 1 {
			t.Fatal("duplicate native dispatch")
		}
		f.run(t, out, "VERIFIED")
	})
	t.Run("lost_native_receipt_is_unknown_and_never_recreated", func(t *testing.T) {
		f := newProvisionFixture(t)
		out := f.approved(t, "alice", 0)
		wrapped := &observedProvider{NativeProvisioningProvider: f.worker.Provider, loseReceipt: true}
		f.worker.Provider = wrapped
		status := f.run(t, out, "OUTCOME_UNKNOWN")
		if status.Grant.State != "PROVISIONING_UNKNOWN" || status.Grant.AccountID != nil {
			t.Fatal("unknown native outcome activated")
		}
		f.run(t, out, "OUTCOME_UNKNOWN")
		if wrapped.creates.Load() != 1 {
			t.Fatal("unknown outcome blindly retried")
		}
	})
	for _, mode := range []string{"access_removed", "target_version_changed", "expiry_passed", "critical_audit_failed"} {
		t.Run("late_"+mode, func(t *testing.T) {
			f := newProvisionFixture(t)
			out := f.approved(t, "alice", 0)
			if mode == "expiry_passed" { // New bounded request, not an illegal edit of an approved snapshot.
				input := f.input()
				input.RequestedExpiry = time.Now().UTC().Add(2 * time.Second).Format(time.RFC3339Nano)
				r, e := f.s.Request(f.ctx, f.people["alice"], input)
				if e != nil {
					t.Fatal(e)
				}
				out, e = f.s.Decide(f.ctx, f.people["reviewer"], r.RequestID, approveInput(r))
				if e != nil {
					t.Fatal(e)
				}
			}
			wrapped := &observedProvider{NativeProvisioningProvider: f.worker.Provider, afterVerify: func() {
				switch mode {
				case "access_removed":
					if _, e := f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.resource_access SET can_request=false WHERE org_id=$1 AND principal_id=$2 AND resource_id=$3", out.Request.OrgID, out.Grant.SubjectID, out.Request.ResourceID); e != nil {
						t.Fatal(e)
					}
				case "target_version_changed":
					f.config.Targets[0].Version = "2"
					f.apply(t)
				case "expiry_passed":
					time.Sleep(2100 * time.Millisecond)
				case "critical_audit_failed":
					// Per-organization fault: preserve all other workflows and the failure audit.
					sql := `CREATE FUNCTION mcpguard_ra.test_activation_audit_` + strings.ReplaceAll(out.Request.OrgID, "-", "") + `() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.org_id='` + out.Request.OrgID + `' AND NEW.event_type='NATIVE_VERIFIED_GRANT_ACTIVE' THEN RAISE EXCEPTION 'injected activation audit failure';END IF;RETURN NEW;END $$;`
					sql += `CREATE TRIGGER test_activation_audit_` + strings.ReplaceAll(out.Request.OrgID, "-", "") + ` BEFORE INSERT ON mcpguard_ra.provision_audit FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.test_activation_audit_` + strings.ReplaceAll(out.Request.OrgID, "-", "") + `();`
					if _, e := f.admin.Exec(f.ctx, sql); e != nil {
						t.Fatal("fault injection failed")
					}
				}
			}}
			f.worker.Provider = wrapped
			status := f.run(t, out, "CLEANUP_PENDING")
			if status.Grant.State == "ACTIVE" || status.Grant.AccountID != nil {
				t.Fatal("late result activated")
			}
			var login bool
			if f.native[0].QueryRow(f.ctx, "SELECT rolcanlogin FROM pg_roles WHERE rolname=$1", wrapped.plan.NativeIdentity).Scan(&login) != nil || login {
				t.Fatal("owned account compensation failed")
			}
			var n int
			_ = f.admin.QueryRow(f.ctx, "SELECT count(*) FROM mcpguard_ra.native_accounts WHERE org_id=$1", out.Request.OrgID).Scan(&n)
			if n != 0 {
				t.Fatal("partial activation committed")
			}
		})
	}
	t.Run("configuration_versions_preserve_history_and_stale_approval_denies", func(t *testing.T) {
		f := newProvisionFixture(t)
		out := f.approved(t, "alice", 0)
		f.config.Targets[0].Version = "2"
		f.apply(t)
		f.apply(t)
		var n int
		_ = f.admin.QueryRow(f.ctx, "SELECT count(*) FROM mcpguard_ra.provision_config_history WHERE org_id=$1 AND kind='provision_targets'", out.Request.OrgID).Scan(&n)
		if n != 3 {
			t.Fatal("configuration history missing or replay duplicated")
		}
		if _, e := f.worker.Run(f.ctx, out.Request.OrgID, out.Grant.GrantID); !errors.Is(e, ErrDenied) {
			t.Fatal("stale provisioning qualification accepted")
		}
		f.config.Targets[0].TargetFingerprint = strings.Repeat("0", 64)
		f.config.Targets[0].Version = "3"
		raw, _ := json.Marshal(f.config)
		if e := ImportProvisioning(f.ctx, f.dsn, raw); !errors.Is(e, ErrDenied) {
			t.Fatal("target substitution accepted")
		}
	})
	t.Run("active_grant_loses_server_secret_on_eligibility_change", func(t *testing.T) {
		f := newProvisionFixture(t)
		active := f.run(t, f.approved(t, "alice", 0), "VERIFIED")
		snap, e := f.s.Catalog.Snapshot(f.ctx, f.people["alice"], active.Grant.ResourceID)
		if e != nil {
			t.Fatal(e)
		}
		provider := &VaultSecretProvider{f.s.Catalog, f.private.Endpoints}
		if _, _, e = provider.Resolve(f.ctx, snap.Resource.EndpointConfigRef, snap.Binding.SecretRef, "1"); e != nil {
			t.Fatal(e)
		}
		if _, e = f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.resource_access SET can_request=false WHERE org_id=$1 AND principal_id=$2 AND resource_id=$3", active.Request.OrgID, active.Grant.SubjectID, active.Request.ResourceID); e != nil {
			t.Fatal(e)
		}
		if _, _, e = provider.Resolve(f.ctx, snap.Resource.EndpointConfigRef, snap.Binding.SecretRef, "1"); e == nil {
			t.Fatal("ineligible secret resolved")
		}
		status, e := f.s.Status(f.ctx, f.people["alice"], active.Request.RequestID)
		if e != nil || status.Grant.State != "REVOKED" {
			t.Fatal("active state did not converge")
		}
	})
}

type observedProvider struct {
	NativeProvisioningProvider
	creates     atomic.Int32
	plan        v02.ProvisioningPlan
	loseReceipt bool
	afterVerify func()
}

func (p *observedProvider) Create(ctx context.Context, t v02.ProvisioningTarget, plan v02.ProvisioningPlan, hash string, s NativeSecret) error {
	p.creates.Add(1)
	p.plan = plan
	e := p.NativeProvisioningProvider.Create(ctx, t, plan, hash, s)
	if e == nil && p.loseReceipt {
		return ErrUnavailable
	}
	return e
}
func (p *observedProvider) Verify(ctx context.Context, plan v02.ProvisioningPlan, s NativeSecret) (v02.NativeObservation, error) {
	out, e := p.NativeProvisioningProvider.Verify(ctx, plan, s)
	if e == nil && p.afterVerify != nil {
		p.afterVerify()
	}
	return out, e
}

func TestProviderPrivateBoundary(t *testing.T) {
	raw := `{"endpoints":{"pg":{"host":"127.0.0.1","port":5432,"database":"mcpguard_business","sslMode":"disable"}},"managers":{"pg":"postgres://manager:private@127.0.0.1:5432/mcpguard_business?sslmode=disable"},"existingSecrets":{}}`
	path := filepath.Join(t.TempDir(), "provider.json")
	for _, value := range []string{raw + " {}", strings.Replace(raw, "127.0.0.1", "192.0.2.1", 1), strings.Replace(raw, "5432/mcpguard_business", "5433/mcpguard_business", 1), strings.Replace(raw, `"managers":`, `"sql":"CREATE ROLE admin", "managers":`, 1), strings.Replace(raw, "sslmode=disable", "sslmode=prefer", 1)} {
		_ = os.WriteFile(path, []byte(value), 0600)
		if _, e := LoadProviderPrivate(path); e == nil {
			t.Fatal("unsafe provider config accepted")
		}
	}
	_ = os.WriteFile(path, []byte(raw), 0600)
	if _, e := LoadProviderPrivate(path); e != nil {
		t.Fatal("valid private provider rejected")
	}
	_ = os.Chmod(path, 0644)
	if _, e := LoadProviderPrivate(path); e == nil {
		t.Fatal("publicly readable management credential accepted")
	}
}

func TestProvisionV2PendingGrantUpgradeIntegration(t *testing.T) {
	f := newProvisionFixture(t)
	database := "ra_upgrade_v2_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	if _, e := f.admin.Exec(f.ctx, "CREATE DATABASE "+pgx.Identifier{database}.Sanitize()); e != nil {
		t.Fatal(e)
	}
	address, e := url.Parse(f.dsn)
	if e != nil {
		t.Fatal("upgrade configuration invalid")
	}
	address.Path = "/" + database
	dsn := address.String()
	if e = metadata.Migrate(f.ctx, dsn); e != nil {
		t.Fatal(e)
	}
	conn, e := pgx.Connect(f.ctx, dsn)
	if e != nil {
		t.Fatal("upgrade database unavailable")
	}
	defer conn.Close(f.ctx)
	for version := 1; version <= 2; version++ {
		if _, e = conn.Exec(f.ctx, string(migrationBytes(version))); e != nil {
			t.Fatal(e)
		}
		if _, e = conn.Exec(f.ctx, "INSERT INTO mcpguard_ra.schema_versions VALUES($1,$2)", version, migrationChecksum(version)); e != nil {
			t.Fatal(e)
		}
	}
	org := f.people["alice"].OrgID
	if _, e = conn.Exec(f.ctx, "INSERT INTO mcpguard.authorization_heads(org_id) VALUES($1)", org); e != nil {
		t.Fatal(e)
	}
	for name, p := range f.people {
		if _, e = conn.Exec(f.ctx, `INSERT INTO mcpguard.principals(org_id,id,display_name,token_hash,expires_at,scope_id) VALUES($1,$2,$3,$4,clock_timestamp()+interval '1 day','upgrade_test')`, org, p.ID, name, p.TokenHash); e != nil {
			t.Fatal(e)
		}
	}
	raw, _ := json.Marshal(f.bundle)
	if e = Import(f.ctx, dsn, raw); e != nil {
		t.Fatal(e)
	}
	raw, _ = json.Marshal(f.accessFixture.config)
	if e = ImportAccess(f.ctx, dsn, raw); e != nil {
		t.Fatal(e)
	}
	if _, e = conn.Exec(f.ctx, `GRANT USAGE ON SCHEMA mcpguard,mcpguard_ra TO mcpguard_app;GRANT SELECT ON ALL TABLES IN SCHEMA mcpguard,mcpguard_ra TO mcpguard_app;GRANT EXECUTE ON FUNCTION mcpguard.lock_auth_epoch(uuid),mcpguard.lock_auth_epoch_exclusive(uuid),mcpguard_ra.advance_access_epoch(uuid) TO mcpguard_app;GRANT INSERT ON mcpguard_ra.access_requests,mcpguard_ra.access_decisions,mcpguard_ra.access_grant_versions,mcpguard_ra.access_grant_heads,mcpguard_ra.access_commands,mcpguard_ra.access_audit TO mcpguard_app;GRANT UPDATE(state,revision) ON mcpguard_ra.access_requests TO mcpguard_app;GRANT UPDATE(state,revision,auth_epoch) ON mcpguard_ra.access_grant_heads TO mcpguard_app;GRANT USAGE ON SEQUENCE mcpguard.audit_events_seq_seq TO mcpguard_app;`); e != nil {
		t.Fatal(e)
	}
	appAddress, e := url.Parse(os.Getenv("MCPGUARD_METADATA_DSN"))
	if e != nil {
		t.Fatal("upgrade runtime configuration invalid")
	}
	appAddress.Path = "/" + database
	store, e := metadata.Open(f.ctx, appAddress.String(), f.s.Catalog.Store.Vault)
	if e != nil {
		t.Fatal("upgrade store unavailable")
	}
	defer store.Close()
	c, e := NewCatalog(f.ctx, store)
	if e != nil {
		t.Fatal(e)
	}
	service, e := NewAccessService(c)
	if e != nil {
		t.Fatal(e)
	}
	r, e := service.Request(f.ctx, f.people["alice"], f.input())
	if e != nil {
		t.Fatal(e)
	}
	out, e := service.Decide(f.ctx, f.people["reviewer"], r.RequestID, approveInput(r))
	if e != nil {
		t.Fatal(e)
	}
	var before string
	if e = conn.QueryRow(f.ctx, "SELECT to_jsonb(v)::text FROM mcpguard_ra.access_grant_versions v WHERE org_id=$1 AND grant_id=$2", org, out.Grant.GrantID).Scan(&before); e != nil {
		t.Fatal(e)
	}
	if e = Migrate(f.ctx, dsn); e != nil {
		t.Fatal(e)
	}
	if e = Migrate(f.ctx, dsn); e != nil {
		t.Fatal(e)
	}
	if _, e = conn.Exec(f.ctx, "GRANT SELECT ON ALL TABLES IN SCHEMA mcpguard_ra TO mcpguard_app"); e != nil {
		t.Fatal(e)
	}
	var after string
	var identities int
	if e = conn.QueryRow(f.ctx, "SELECT (to_jsonb(v)-ARRAY['account_id','binding_id','binding_version','observation_id','activation_job_id'])::text FROM mcpguard_ra.access_grant_versions v WHERE org_id=$1 AND grant_id=$2", org, out.Grant.GrantID).Scan(&after); e != nil || before != after {
		t.Fatal("old approved version rewritten during upgrade")
	}
	if e = conn.QueryRow(f.ctx, "SELECT count(*) FROM mcpguard_ra.access_grant_ids WHERE org_id=$1 AND grant_id=$2 AND request_id=$3", org, out.Grant.GrantID, r.RequestID).Scan(&identities); e != nil || identities != 1 {
		t.Fatal("stable grant identity not backfilled")
	}
	c, e = NewCatalog(f.ctx, store)
	if e != nil {
		t.Fatal(e)
	}
	service, e = NewAccessService(c)
	if e != nil {
		t.Fatal(e)
	}
	status, e := service.Status(f.ctx, f.people["alice"], r.RequestID)
	if e != nil || status.Grant.State != "PROVISIONING" || status.Grant.AccountID != nil {
		t.Fatal("upgrade changed prior pending grant semantics")
	}
}
