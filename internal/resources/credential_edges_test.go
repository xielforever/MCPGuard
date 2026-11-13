package resources

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

func TestCredentialAdditionalBoundariesIntegration(t *testing.T) {
	t.Run("grant_expiry_caps_lease", func(t *testing.T) {
		f := newCredentialFixture(t)
		f.config.Policies[0].Version = "2"
		f.config.Policies[0].MaxTtlSeconds = 86400
		f.apply(t)
		status, out := f.issue(t, "alice", 0)
		expiry, _, _ := normalizedTime(status.ExpiresAt)
		grantExpiry, _, _ := normalizedTime(out.Grant.ExpiresAt)
		if !expiry.Equal(grantExpiry) {
			t.Fatal("policy limit overrode shorter grant expiry")
		}
	})
	t.Run("cross_org_and_malformed_packages", func(t *testing.T) {
		f := newCredentialFixture(t)
		status, out := f.issue(t, "alice", 0)
		blob := f.blob(t, status.CredentialID)
		other := newCredentialFixture(t)
		if _, e := other.service.Validate(other.ctx, other.people["alice"], out.Grant.ResourceID, status.CredentialID, blob); !errors.Is(e, ErrCredential) {
			t.Fatal("package crossed organization")
		}
		for _, bad := range [][]byte{nil, bytes.Repeat([]byte{'a'}, v02.MaxPackageBytes+1), append(append([]byte{}, blob...), byte('\n')), bytes.Replace(blob, []byte(`"formatVersion":"1"`), []byte(`"formatVersion":"9"`), 1)} {
			if _, e := f.service.Validate(f.ctx, f.people["alice"], out.Grant.ResourceID, status.CredentialID, bad); !errors.Is(e, ErrCredential) {
				t.Fatal("malformed package accepted")
			}
		}
		current, e := f.service.Status(f.ctx, f.people["alice"], v02.CredentialIDInput{CredentialID: status.CredentialID})
		if e != nil || current != status {
			t.Fatal("untrusted packet changed authoritative state")
		}
	})
	t.Run("reference_is_not_a_native_password", func(t *testing.T) {
		f := newCredentialFixture(t)
		status, out := f.issue(t, "alice", 0)
		if _, e := f.service.Validate(f.ctx, f.people["alice"], out.Grant.ResourceID, status.CredentialID, f.blob(t, status.CredentialID)); e != nil {
			t.Fatal(e)
		}
		// Test-only composition proves that actual native credentials are resolved
		// separately on the server. The production execution gate is still RA-11.
		snap, e := f.s.Catalog.Snapshot(f.ctx, f.people["alice"], out.Grant.ResourceID)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.native[0].Exec(f.ctx, "INSERT INTO resource_demo.items VALUES($1,'item_demo_ra07','server native result',1)", snap.Binding.NativeIdentity); e != nil {
			t.Fatal(e)
		}
		reader := NewReadManager(f.s.Catalog, &VaultSecretProvider{Catalog: f.s.Catalog, Endpoints: f.private.Endpoints})
		defer reader.Close()
		result, e := reader.ReadFixture(f.ctx, f.people["alice"], v02.FixtureReadInput{ResourceID: out.Grant.ResourceID, ItemID: "item_demo_ra07"})
		if e != nil || !result.Found || result.Items[0].Note != "server native result" || result.NativeIdentity != snap.Binding.NativeIdentity {
			t.Fatal("server native identity read failed")
		}
	})
	t.Run("key_usage_limit_fails_before_encryption", func(t *testing.T) {
		f := newCredentialFixture(t)
		out := f.approved(t, "alice", 0)
		f.run(t, out, "VERIFIED")
		tx, e := f.admin.Begin(f.ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer rollback(tx)
		if _, e = tx.Exec(f.ctx, "ALTER TABLE mcpguard_ra.credential_key_usage DISABLE TRIGGER credential_usage_guard"); e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(f.ctx, "UPDATE mcpguard_ra.credential_key_usage SET use_count=4294967296 WHERE fingerprint=$1", f.config.Issuer.KeyFingerprint); e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(f.ctx, "ALTER TABLE mcpguard_ra.credential_key_usage ENABLE TRIGGER credential_usage_guard"); e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(f.ctx); e != nil {
			t.Fatal(e)
		}
		keys := &faultPackageKeys{PackageKeyProvider: f.keys}
		f.service.Keys = keys
		if _, e := f.service.Issue(f.ctx, f.people["alice"], v02.IssueCredentialInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()}); !errors.Is(e, ErrUnavailable) || keys.calls.Load() != 0 || f.count(t, "credential_nonce_reservations") != 0 {
			t.Fatal("exhausted key reached encryption")
		}
	})
	t.Run("immutable_key_mapping_and_config_versions", func(t *testing.T) {
		f := newCredentialFixture(t)
		f.config.Issuer.KeyFingerprint = strings.Repeat("ab", 32)
		f.config.Issuer.Version = "2"
		raw, _ := json.Marshal(f.config)
		if e := ImportCredentials(f.ctx, f.dsn, raw); !errors.Is(e, ErrConflict) {
			t.Fatal("key id silently changed key material")
		}
		f.config.Issuer.KeyFingerprint = f.keys.(*filePackageKeys).keys[f.people["alice"].OrgID+"/mcpguard.test/reference.v1"].Fingerprint()
		f.config.Issuer.Version = "1"
		f.config.Issuer.Enabled = false
		raw, _ = json.Marshal(f.config)
		if e := ImportCredentials(f.ctx, f.dsn, raw); !errors.Is(e, ErrConflict) {
			t.Fatal("same issuer version changed policy")
		}
	})
	for _, event := range []string{"LEASE_REVOKED", "PACKAGE_VALIDATED"} {
		t.Run("critical_audit_"+event, func(t *testing.T) {
			f := newCredentialFixture(t)
			status, out := f.issue(t, "alice", 0)
			suffix := strings.ReplaceAll(out.Request.OrgID, "-", "")
			sql := `CREATE FUNCTION mcpguard_ra.credential_fault_` + suffix + `() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.org_id='` + out.Request.OrgID + `' AND NEW.event_type='` + event + `' THEN RAISE EXCEPTION 'injected critical audit failure';END IF;RETURN NEW;END $$;CREATE TRIGGER credential_fault_` + suffix + ` BEFORE INSERT ON mcpguard_ra.credential_audit FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.credential_fault_` + suffix + `();`
			if _, e := f.admin.Exec(f.ctx, sql); e != nil {
				t.Fatal(e)
			}
			var e error
			if event == "LEASE_REVOKED" {
				_, e = f.service.Revoke(f.ctx, f.people["alice"], revokeInput(status))
			} else {
				_, e = f.service.Validate(f.ctx, f.people["alice"], out.Grant.ResourceID, status.CredentialID, f.blob(t, status.CredentialID))
			}
			if !errors.Is(e, ErrUnavailable) {
				t.Fatal("critical audit failure reported success")
			}
			current, e := f.service.Status(f.ctx, f.people["alice"], v02.CredentialIDInput{CredentialID: status.CredentialID})
			if e != nil || current != status || f.count(t, "credential_commands") != 1 {
				t.Fatal("failed critical audit partially committed")
			}
		})
	}
}

func TestCredentialV4UpgradeIntegration(t *testing.T) {
	f := newCredentialFixture(t)
	active := f.approved(t, "alice", 0)
	f.run(t, active, "VERIFIED")
	pending := f.approved(t, "bob", 1)
	// Use a real v4 database with actual account and recovery history. Preserve
	// every organization row, not just its latest grant or plan.
	database := "ra_upgrade_v4_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	if _, e := f.admin.Exec(f.ctx, "CREATE DATABASE "+pgx.Identifier{database}.Sanitize()); e != nil {
		t.Fatal(e)
	}
	address, _ := url.Parse(f.dsn)
	address.Path = "/" + database
	dsn := address.String()
	if e := metadata.Migrate(f.ctx, dsn); e != nil {
		t.Fatal(e)
	}
	target, e := pgx.Connect(f.ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer target.Close(f.ctx)
	for v := 1; v <= 4; v++ {
		if _, e = target.Exec(f.ctx, string(migrationBytes(v))); e != nil {
			t.Fatal(e)
		}
		if _, e = target.Exec(f.ctx, "INSERT INTO mcpguard_ra.schema_versions VALUES($1,$2)", v, migrationChecksum(v)); e != nil {
			t.Fatal(e)
		}
	}
	rows, e := target.Query(f.ctx, `SELECT table_schema||'.'||table_name FROM information_schema.columns WHERE column_name='org_id' AND table_schema IN ('mcpguard','mcpguard_ra') GROUP BY table_schema,table_name ORDER BY table_schema,table_name`)
	if e != nil {
		t.Fatal(e)
	}
	var tables []string
	for rows.Next() {
		var table string
		_ = rows.Scan(&table)
		tables = append(tables, table)
	}
	rows.Close()
	before := map[string][32]byte{}
	tx, e := target.Begin(f.ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer rollback(tx)
	if _, e = tx.Exec(f.ctx, "SET LOCAL session_replication_role=replica"); e != nil {
		t.Fatal(e)
	}
	for _, table := range tables {
		var raw []byte
		q := "SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM " + table + " t WHERE org_id=$1"
		if e = f.admin.QueryRow(f.ctx, q, active.Request.OrgID).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		before[table] = sha256.Sum256(raw)
		if _, e = tx.Exec(f.ctx, "INSERT INTO "+table+" OVERRIDING SYSTEM VALUE SELECT * FROM jsonb_populate_recordset(NULL::"+table+",$1::jsonb)", raw); e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(f.ctx); e != nil {
		t.Fatal(e)
	}
	for n := 0; n < 2; n++ {
		if e = Migrate(f.ctx, dsn); e != nil {
			t.Fatal(e)
		}
	}
	for _, table := range tables {
		var raw []byte
		if e = target.QueryRow(f.ctx, "SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM "+table+" t WHERE org_id=$1", active.Request.OrgID).Scan(&raw); e != nil || sha256.Sum256(raw) != before[table] {
			t.Fatal("v4 history changed: ", table)
		}
	}
	var packages int
	if target.QueryRow(f.ctx, "SELECT count(*) FROM mcpguard_ra.credential_packages").Scan(&packages) != nil || packages != 0 {
		t.Fatal("migration manufactured packages")
	}
	binary := os.Getenv("MCPGUARD_TEST_CATALOG_BINARY")
	cmd := exec.Command(binary, "grant-credential-runtime", "mcpguard_credentials")
	cmd.Env = append(os.Environ(), "MCPGUARD_MIGRATION_DSN="+dsn)
	if result, e := cmd.CombinedOutput(); e != nil {
		t.Fatal("upgrade runtime grants failed, bytes: ", len(result))
	}
	address, _ = url.Parse(os.Getenv("MCPGUARD_TEST_CREDENTIAL_DSN"))
	address.Path = "/" + database
	store, e := metadata.Open(f.ctx, address.String(), f.s.Catalog.Store.Vault)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	catalog, e := NewCatalog(f.ctx, store)
	if e != nil {
		t.Fatal(e)
	}
	service, e := NewCredentialService(catalog, f.keys)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(f.config)
	if e = ImportCredentials(f.ctx, dsn, raw); e != nil {
		t.Fatal(e)
	}
	if _, e = service.Issue(f.ctx, f.people["bob"], v02.IssueCredentialInput{GrantID: pending.Grant.GrantID, RequestKey: uuid.NewString()}); !errors.Is(e, ErrDenied) {
		t.Fatal("upgrade promoted pending grant")
	}
	if _, e = service.Issue(f.ctx, f.people["alice"], v02.IssueCredentialInput{GrantID: active.Grant.GrantID, RequestKey: uuid.NewString()}); e != nil {
		t.Fatal("upgraded active grant cannot issue: ", e)
	}
}
