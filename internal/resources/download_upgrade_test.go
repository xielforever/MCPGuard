package resources

import (
	"crypto/sha256"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestPrivateDownloadV5UpgradeIntegration(t *testing.T) {
	f := newDownloadFixture(t)
	status, out := f.issue(t, "alice", 0)
	blob := f.blob(t, status.CredentialID)
	database := "ra_upgrade_v5_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
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
	for v := 1; v <= 5; v++ {
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
		if e = f.admin.QueryRow(f.ctx, "SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM "+table+" t WHERE org_id=$1", out.Request.OrgID).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		before[table] = sha256.Sum256(raw)
		if _, e = tx.Exec(f.ctx, "INSERT INTO "+table+" OVERRIDING SYSTEM VALUE SELECT * FROM jsonb_populate_recordset(NULL::"+table+",$1::jsonb)", raw); e != nil {
			t.Fatal(e)
		}
	}
	// The use counter is intentionally global by real key fingerprint. It is
	// copied as part of restore rather than recreated at zero for this issuer.
	var usage []byte
	if e = f.admin.QueryRow(f.ctx, "SELECT jsonb_agg(to_jsonb(u)) FROM mcpguard_ra.credential_key_usage u WHERE fingerprint=$1", f.config.Issuer.KeyFingerprint).Scan(&usage); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(f.ctx, "INSERT INTO mcpguard_ra.credential_key_usage SELECT * FROM jsonb_populate_recordset(NULL::mcpguard_ra.credential_key_usage,$1::jsonb)", usage); e != nil {
		t.Fatal(e)
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
		if e = target.QueryRow(f.ctx, "SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM "+table+" t WHERE org_id=$1", out.Request.OrgID).Scan(&raw); e != nil || sha256.Sum256(raw) != before[table] {
			t.Fatal("v5 state changed: ", table)
		}
	}
	var after []byte
	if e = target.QueryRow(f.ctx, "SELECT jsonb_agg(to_jsonb(u)) FROM mcpguard_ra.credential_key_usage u WHERE fingerprint=$1", f.config.Issuer.KeyFingerprint).Scan(&after); e != nil || sha256.Sum256(usage) != sha256.Sum256(after) {
		t.Fatal("key counter reset on upgrade")
	}
	var tickets int
	if target.QueryRow(f.ctx, "SELECT count(*) FROM mcpguard_ra.download_tickets").Scan(&tickets) != nil || tickets != 0 {
		t.Fatal("upgrade manufactured tickets")
	}
	if _, e = target.Exec(f.ctx, "SELECT setval('mcpguard.audit_events_seq_seq',1000000)"); e != nil {
		t.Fatal(e)
	}
	command := exec.Command(os.Getenv("MCPGUARD_TEST_CATALOG_BINARY"), "grant-delivery-runtime", "mcpguard_downloads")
	command.Env = append(os.Environ(), "MCPGUARD_MIGRATION_DSN="+dsn)
	if result, e := command.CombinedOutput(); e != nil {
		t.Fatal("upgrade grant failed, output bytes: ", len(result))
	}
	address, _ = url.Parse(os.Getenv("MCPGUARD_TEST_DELIVERY_DSN"))
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
	credentials, e := NewCredentialService(catalog, f.keys)
	if e != nil {
		t.Fatal(e)
	}
	if validated, e := credentials.Validate(f.ctx, f.people["alice"], out.Grant.ResourceID, status.CredentialID, blob); e != nil || validated != status {
		t.Fatal("v5 encrypted reference invalidated by migration")
	}
	downloads, e := NewDownloadService(credentials, 60, 5)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = downloads.Prepare(f.ctx, f.people["alice"], v02.PrepareDownloadInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()}); e != nil {
		t.Fatal("upgraded active grant cannot prepare: ", e)
	}
}
