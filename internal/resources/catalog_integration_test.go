package resources

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"github.com/xielforever/MCPGuard/internal/runtimeconfig"
)

func TestCatalogVersioningIntegration(t *testing.T) {
	fixture := os.Getenv("MCPGUARD_RA_CONFIG")
	if fixture == "" {
		t.Skip("fresh RA fixture required")
	}
	ctx := context.Background()
	adminDSN := os.Getenv("MCPGUARD_TEST_METADATA_ADMIN_DSN")
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatal("isolated metadata unavailable")
	}
	defer admin.Close(ctx)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(fixture), "catalog.json"))
	var source v02.CatalogBundle
	if err != nil || v02.Decode("CatalogBundle", raw, &source) != nil {
		t.Fatal("fixture catalog invalid")
	}
	// A separate organization deliberately reuses the same resource and profile
	// IDs. Organization ownership must survive import, lookup and version changes.
	org, owner, token := uuid.NewString(), uuid.NewString(), uuid.NewString()+uuid.NewString()
	hash := sha256.Sum256([]byte(token))
	if _, err = admin.Exec(ctx, "INSERT INTO mcpguard.authorization_heads(org_id) VALUES($1)", org); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO mcpguard.principals(org_id,id,display_name,token_hash,expires_at,scope_id) VALUES($1,$2,'catalog-test',$3,clock_timestamp()+interval '1 hour','catalog_test')`, org, owner, hash[:]); err != nil {
		t.Fatal(err)
	}
	r, head, profile, tool := source.Resources[0], source.Heads[0], source.Profiles[0], source.Tools[0]
	r.OrgID, head.OrgID, profile.OrgID, tool.OrgID = org, org, org, org
	bundle := v02.CatalogBundle{SchemaVersion: "0.2", Resources: []v02.ResourceVersion{r}, Heads: []v02.ResourceHead{head}, Profiles: []v02.PermissionProfile{profile}, Tools: []v02.ToolResourceBinding{tool}, Access: []v02.ResourceAccess{{OrgID: org, PrincipalID: owner, ResourceID: r.ResourceID, CanDiscover: true, CanRequest: true}}, Bindings: []v02.CredentialBinding{}}
	apply := func(value v02.CatalogBundle) error {
		data, _ := json.Marshal(value)
		return Import(ctx, adminDSN, data)
	}
	if err = apply(bundle); err != nil {
		t.Fatal(err)
	}
	var before, after int64
	_ = admin.QueryRow(ctx, "SELECT auth_epoch FROM mcpguard.authorization_heads WHERE org_id=$1", org).Scan(&before)
	if err = apply(bundle); err != nil {
		t.Fatal("idempotent import failed")
	}
	_ = admin.QueryRow(ctx, "SELECT auth_epoch FROM mcpguard.authorization_heads WHERE org_id=$1", org).Scan(&after)
	if before != after {
		t.Fatal("identical import changed authority")
	}
	cfg, err := runtimeconfig.Load()
	if err != nil {
		t.Fatal(err)
	}
	vault, _ := metadata.NewVault(cfg.EncryptionKey, cfg.PlanKey)
	store, err := metadata.Open(ctx, cfg.MetadataDSN, vault)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	catalog, err := NewCatalog(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.VerifyToken(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if p.CanRead {
		t.Fatal("test accidentally inherited the legacy business grant")
	}
	get := func() v02.GetResourceResult {
		t.Helper()
		out, err := catalog.Get(ctx, p, v02.GetResourceInput{ResourceID: r.ResourceID})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if get().Resource.Version != "1" {
		t.Fatal("catalog incorrectly requires a legacy CanRead grant")
	}
	bundle.Resources[0].Alias = "changed-in-place"
	if !errors.Is(apply(bundle), ErrConflict) || get().Resource.Alias != r.Alias {
		t.Fatal("immutable resource version was rewritten")
	}
	bundle.Resources[0].Version = "2"
	bundle.Heads[0].Version, bundle.Heads[0].Revision = "2", "2"
	bundle.Tools[0].ResourceVersion = "2"
	if err = apply(bundle); err != nil {
		t.Fatal(err)
	}
	current := get()
	if current.Resource.ResourceID != r.ResourceID || current.Resource.Alias != "changed-in-place" || current.Resource.Version != "2" || current.Resource.Revision != "2" || len(current.Profiles) != 1 {
		t.Fatal("alias change lost stable identity or versioned profile")
	}
	var oldAlias string
	if admin.QueryRow(ctx, "SELECT definition->>'alias' FROM mcpguard_ra.resource_versions WHERE org_id=$1 AND resource_id=$2 AND version=1", org, r.ResourceID).Scan(&oldAlias) != nil || oldAlias != r.Alias {
		t.Fatal("historical resource version was lost")
	}
	bundle.Heads[0].Version, bundle.Heads[0].Revision = "1", "3"
	if !errors.Is(apply(bundle), ErrConflict) || get().Resource.Version != "2" {
		t.Fatal("catalog head rolled back to an older version")
	}
	// A new organization may discover its own copy without gaining a native
	// account or the original organization's resource binding.
	if _, err = catalog.Snapshot(ctx, p, r.ResourceID); !errors.Is(err, ErrDenied) {
		t.Fatal("native binding crossed organizations")
	}
}
