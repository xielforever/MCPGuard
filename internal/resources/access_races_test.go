package resources

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestAccessCancelDecisionRaceIntegration(t *testing.T) {
	f := newAccessFixture(t, 3600)
	r := f.request(t, "alice")
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, e := f.s.Cancel(f.ctx, f.people["alice"], v02.CancelAccessInput{RequestID: r.RequestID, ExpectedRevision: r.Revision, RequestKey: uuid.NewString()})
		errs <- e
	}()
	go func() {
		defer wg.Done()
		<-start
		_, e := f.s.Decide(f.ctx, f.people["reviewer"], r.RequestID, approveInput(r))
		errs <- e
	}()
	close(start)
	wg.Wait()
	close(errs)
	wins := 0
	for e := range errs {
		if e == nil {
			wins++
		} else if !errors.Is(e, ErrConflict) {
			t.Fatal(e)
		}
	}
	if wins != 1 {
		t.Fatal("cancel and approval both committed")
	}
	status, e := f.s.Status(f.ctx, f.people["alice"], r.RequestID)
	if e != nil {
		t.Fatal(e)
	}
	switch status.Request.State {
	case "APPROVED":
		if status.Grant == nil || f.count(t, "access_decisions", r.RequestID) != 1 {
			t.Fatal("approved race lost grant")
		}
	case "CANCELLED":
		if status.Grant != nil || f.count(t, "access_decisions", r.RequestID) != 0 {
			t.Fatal("cancelled race retained decision")
		}
	default:
		t.Fatal("race did not terminalize request")
	}
}
func TestAccessOrganizationAndPolicyIsolationIntegration(t *testing.T) {
	a, b := newAccessFixture(t, 3600), newAccessFixture(t, 3600)
	r := a.request(t, "alice")
	if _, e := b.s.Status(b.ctx, b.people["reviewer"], r.RequestID); !errors.Is(e, ErrDenied) {
		t.Fatal("request crossed organization")
	}
	if _, e := b.s.Decide(b.ctx, b.people["reviewer"], r.RequestID, approveInput(r)); !errors.Is(e, ErrDenied) {
		t.Fatal("decision crossed organization")
	}
	raw, _ := json.Marshal(a.config)
	if e := ImportAccess(a.ctx, a.dsn, raw); e != nil {
		t.Fatal("identical policy import failed")
	}
	var before, after int64
	_ = a.admin.QueryRow(a.ctx, "SELECT auth_epoch FROM mcpguard.authorization_heads WHERE org_id=$1", r.OrgID).Scan(&before)
	if e := ImportAccess(a.ctx, a.dsn, raw); e != nil {
		t.Fatal(e)
	}
	_ = a.admin.QueryRow(a.ctx, "SELECT auth_epoch FROM mcpguard.authorization_heads WHERE org_id=$1", r.OrgID).Scan(&after)
	if before != after {
		t.Fatal("idempotent policy import changed epoch")
	}
	a.config.Policies[0].MaxGrantTtlSeconds = 600
	raw, _ = json.Marshal(a.config)
	if e := ImportAccess(a.ctx, a.dsn, raw); !errors.Is(e, ErrConflict) {
		t.Fatal("policy version changed in place")
	}
}
func TestAccessV1CatalogUpgradeIntegration(t *testing.T) {
	// Keep the v1 database and its upgraded contents in the owned fixture. No
	// DROP DATABASE or reset of the original catalog is needed for this proof.
	f := newAccessFixture(t, 3600)
	ctx := context.Background()
	db := "ra_upgrade_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	if _, e := f.admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{db}.Sanitize()); e != nil {
		t.Fatal(e)
	}
	cfg, e := pgx.ParseConfig(os.Getenv("MCPGUARD_TEST_METADATA_ADMIN_DSN"))
	if e != nil {
		t.Fatal("private metadata config invalid")
	}
	cfg.Database = db
	target, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		t.Fatal("isolated upgrade database unavailable")
	}
	defer target.Close(ctx)
	targetURL, e := url.Parse(os.Getenv("MCPGUARD_TEST_METADATA_ADMIN_DSN"))
	if e != nil {
		t.Fatal("invalid private upgrade target")
	}
	targetURL.Path = "/" + db
	dsn := targetURL.String()
	if e = metadata.Migrate(ctx, dsn); e != nil {
		t.Fatal(e)
	}
	if _, e = target.Exec(ctx, string(migrationBytes(1))); e != nil {
		t.Fatal(e)
	}
	if _, e = target.Exec(ctx, "INSERT INTO mcpguard_ra.schema_versions VALUES(1,$1)", migrationChecksum(1)); e != nil {
		t.Fatal(e)
	}
	org, principal := uuid.NewString(), uuid.NewString()
	if _, e = target.Exec(ctx, "INSERT INTO mcpguard.authorization_heads(org_id) VALUES($1)", org); e != nil {
		t.Fatal(e)
	}
	if _, e = target.Exec(ctx, `INSERT INTO mcpguard.principals(org_id,id,display_name,token_hash,expires_at,scope_id) VALUES($1,$2,'existing v1 identity',decode(repeat('ab',32),'hex'),clock_timestamp()+interval '1 day','upgrade_test')`, org, principal); e != nil {
		t.Fatal(e)
	}
	var preservedDefinition []byte
	// The catalog definitions and access flags are real v1 rows, imported before
	// v2 exists. Migration must seed revisions without changing their contents.
	bundle := f.bundle
	for i := range bundle.Resources {
		bundle.Resources[i].OrgID = org
	}
	for i := range bundle.Heads {
		bundle.Heads[i].OrgID = org
	}
	for i := range bundle.Profiles {
		bundle.Profiles[i].OrgID = org
	}
	for i := range bundle.Tools {
		bundle.Tools[i].OrgID = org
	}
	bundle.Access = []v02.ResourceAccess{{OrgID: org, PrincipalID: principal, ResourceID: bundle.Resources[0].ResourceID, CanDiscover: true, CanRequest: true}}
	raw, _ := json.Marshal(bundle)
	if e = Import(ctx, dsn, raw); e != nil {
		t.Fatal(e)
	}
	if e = target.QueryRow(ctx, "SELECT convert_to(definition::text,'UTF8') FROM mcpguard_ra.resource_versions WHERE org_id=$1 AND resource_id=$2", org, bundle.Resources[0].ResourceID).Scan(&preservedDefinition); e != nil {
		t.Fatal(e)
	}
	if version, e := schemaVersion(ctx, target); e != nil || version != 1 {
		t.Fatal("v1 setup invalid")
	}
	if e = Migrate(ctx, dsn); e != nil {
		t.Fatal(e)
	}
	if e = Migrate(ctx, dsn); e != nil {
		t.Fatal("upgrade was not idempotent")
	}
	if version, e := schemaVersion(ctx, target); e != nil || version != len(migrationFiles) {
		t.Fatal("upgrade version invalid")
	}
	var unchanged bool
	e = target.QueryRow(ctx, `SELECT convert_to(v.definition::text,'UTF8')=$3 AND a.can_discover AND a.can_request AND a.revision=1 AND s.revision=1 FROM mcpguard_ra.resource_versions v JOIN mcpguard_ra.resource_access a ON (a.org_id,a.resource_id)=(v.org_id,v.resource_id) JOIN mcpguard_ra.subject_revisions s ON (s.org_id,s.principal_id)=(a.org_id,a.principal_id) WHERE v.org_id=$1 AND v.resource_id=$2`, org, bundle.Resources[0].ResourceID, preservedDefinition).Scan(&unchanged)
	if e != nil || !unchanged {
		t.Fatal("v1 rows changed during upgrade")
	}
}
