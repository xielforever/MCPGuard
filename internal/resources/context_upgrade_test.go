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

func TestContextV6UpgradeIntegration(t *testing.T) {
	f := newContextFixture(t).downloadFixture
	first, out := f.prepare(t, "alice", 0)
	if f.deliver("alice", first.DownloadID, discardDownload) != nil {
		t.Fatal("old claim")
	}
	waiting, e := f.download.Prepare(f.ctx, f.people["alice"], v02.PrepareDownloadInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()})
	if e != nil {
		t.Fatal(e)
	}
	db := "ra_upgrade_v6_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	if _, e = f.admin.Exec(f.ctx, "CREATE DATABASE "+pgx.Identifier{db}.Sanitize()); e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(f.dsn)
	u.Path = "/" + db
	dsn := u.String()
	if e = metadata.Migrate(f.ctx, dsn); e != nil {
		t.Fatal(e)
	}
	target, e := pgx.Connect(f.ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer target.Close(f.ctx)
	for v := 1; v <= 6; v++ {
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
		var name string
		if rows.Scan(&name) != nil {
			t.Fatal("table scan")
		}
		tables = append(tables, name)
	}
	rows.Close()
	query := func(table string) string {
		projection := "to_jsonb(t)"
		if table == "mcpguard_ra.download_tickets" {
			projection += "-ARRAY['client_id','claimed_context_id','queue_seq']"
		}
		return "SELECT coalesce(jsonb_agg(" + projection + " ORDER BY (" + projection + ")::text),'[]'::jsonb) FROM " + table + " t WHERE org_id=$1"
	}
	tx, e := target.Begin(f.ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer rollback(tx)
	if _, e = tx.Exec(f.ctx, "SET LOCAL session_replication_role=replica"); e != nil {
		t.Fatal(e)
	}
	before := map[string][32]byte{}
	for _, table := range tables {
		var raw []byte
		if e = f.admin.QueryRow(f.ctx, query(table), out.Request.OrgID).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		before[table] = sha256.Sum256(raw)
		if _, e = tx.Exec(f.ctx, "INSERT INTO "+table+" OVERRIDING SYSTEM VALUE SELECT * FROM jsonb_populate_recordset(NULL::"+table+",$1::jsonb)", raw); e != nil {
			t.Fatal(e)
		}
	}
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
	for i := 0; i < 2; i++ {
		if e = Migrate(f.ctx, dsn); e != nil {
			t.Fatal(e)
		}
	}
	for _, table := range tables {
		var raw []byte
		if e = target.QueryRow(f.ctx, query(table), out.Request.OrgID).Scan(&raw); e != nil || before[table] != sha256.Sum256(raw) {
			t.Fatal("old v6 state changed: ", table)
		}
	}
	var n int
	if target.QueryRow(f.ctx, "SELECT count(*) FROM mcpguard_ra.credential_contexts").Scan(&n) != nil || n != 0 {
		t.Fatal("upgrade manufactured contexts")
	}
	if _, e = target.Exec(f.ctx, "SELECT setval('mcpguard.audit_events_seq_seq',1000000)"); e != nil {
		t.Fatal(e)
	}
	command := exec.Command(os.Getenv("MCPGUARD_TEST_CATALOG_BINARY"), "grant-delivery-runtime", "mcpguard_downloads")
	command.Env = append(os.Environ(), "MCPGUARD_MIGRATION_DSN="+dsn)
	if raw, e := command.CombinedOutput(); e != nil {
		t.Fatal("legacy role grant failed, bytes: ", len(raw))
	}
	u, _ = url.Parse(os.Getenv("MCPGUARD_TEST_DELIVERY_DSN"))
	u.Path = "/" + db
	store, e := metadata.Open(f.ctx, u.String(), f.s.Catalog.Store.Vault)
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
	downloads, e := NewDownloadService(credentials, 60, 10)
	if e != nil {
		t.Fatal(e)
	}
	if old, e := downloads.Status(f.ctx, f.people["alice"], f.tokens["alice"], v02.DownloadIDInput{DownloadID: first.DownloadID}); e != nil || old.State != "CLAIMED" || old.ClientInstanceID != nil {
		t.Fatal("old claimed manual task changed")
	}
	if e = downloads.Deliver(f.ctx, f.people["alice"], f.tokens["alice"], v02.DownloadIDInput{DownloadID: waiting.DownloadID}, discardDownload); e != nil {
		t.Fatal("old pending manual ticket cannot claim after upgrade: ", e)
	}
}
