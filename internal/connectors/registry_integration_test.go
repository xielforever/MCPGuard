package connectors

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"github.com/xielforever/MCPGuard/internal/runtimeconfig"
	connector "github.com/xielforever/MCPGuard/pkg/connectors"
)

func TestRegistryLifecycle(t *testing.T) {
	dsn := os.Getenv("MCPGUARD_TEST_METADATA_ADMIN_DSN")
	if dsn == "" {
		t.Skip("isolated metadata fixture required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cfg, err := runtimeconfig.Load()
	if err != nil {
		t.Fatal(err)
	}
	vault, err := metadata.NewVault(cfg.EncryptionKey, cfg.PlanKey)
	if err != nil {
		t.Fatal(err)
	}
	store, err := metadata.Open(ctx, cfg.MetadataDSN, vault)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	registry := NewRegistry(store)
	if err := registry.Check(ctx); err != nil {
		t.Fatal("connector schema unavailable:", err)
	}

	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	org, otherOrg := uuid.NewString(), uuid.NewString()
	for _, id := range []string{org, otherOrg} {
		seed("INSERT INTO mcpguard.authorization_heads(org_id) VALUES($1)", id)
		seed("INSERT INTO mcpguard.control_heads(org_id) VALUES($1)", id)
	}
	newActor := func(org, displayName string, isAdmin, canRead bool) metadata.Principal {
		t.Helper()
		id := uuid.NewString()
		token := uuid.NewString() + uuid.NewString()
		hash := sha256.Sum256([]byte(token))
		seed(`INSERT INTO mcpguard.principals(org_id,id,display_name,token_hash,expires_at,can_read,scope_id)
			VALUES($1,$2,$3,$4,clock_timestamp()+interval '1 hour',$5,'connector_test')`, org, id, displayName, hash[:], canRead)
		if isAdmin {
			seed("INSERT INTO mcpguard.control_members(org_id,principal_id,is_admin) VALUES($1,$2,true)", org, id)
		}
		actor, err := store.VerifyToken(ctx, token)
		if err != nil {
			t.Fatal(err)
		}
		return actor
	}
	owner := newActor(org, "connector admin", true, false)
	member := newActor(org, "connector member", false, true)
	ungranted := newActor(org, "ungranted member", false, true)
	external := newActor(otherOrg, "other organization admin", true, true)
	if yes, err := registry.IsAdmin(ctx, member); yes || err != nil {
		t.Fatalf("grantee was mistaken for an administrator, allowed=%t err=%v", yes, err)
	}

	testSecret := uuid.NewString()
	input := CreateInput{Name: "deployment inventory", Type: connector.Kubernetes, Config: connector.Config{Namespace: "warehouse"},
		Credentials: connector.Credentials{Secret: testSecret}, PrincipalIDs: []string{external.ID}}
	if _, err := registry.Create(ctx, owner, input, uuid.NewString()); !errors.Is(err, ErrInput) {
		t.Fatalf("accepted a cross-organization grantee, err=%v", err)
	}
	input.PrincipalIDs = []string{member.ID}
	key := uuid.NewString()
	created, err := registry.Create(ctx, owner, input, key)
	if err != nil || created.Revision != 1 || created.Enabled || created.TestStatus != "NOT_TESTED" {
		t.Fatalf("unexpected connector creation state: %+v err=%v", created, err)
	}
	replayed, err := registry.Create(ctx, owner, input, key)
	if err != nil || replayed.ID != created.ID {
		t.Fatalf("create replay stored a second connector, err=%v", err)
	}
	changed := input
	changed.Credentials.Secret = uuid.NewString()
	if _, err := registry.Create(ctx, owner, changed, key); !errors.Is(err, ErrConflict) {
		t.Fatalf("create replay accepted a different definition, err=%v", err)
	}
	if _, err := registry.SetEnabled(ctx, owner, created.ID, created.Revision, uuid.NewString(), true); !errors.Is(err, ErrConflict) {
		t.Fatalf("enabled an untested definition, err=%v", err)
	}
	if rows, err := registry.ListUsable(ctx, member); err != nil || len(rows) != 0 {
		t.Fatalf("draft appeared in MCP discovery, got %d, err=%v", len(rows), err)
	}
	if _, err := registry.GetUsable(ctx, member, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("draft callable via explicit ID, err=%v", err)
	}

	entered := make(chan struct{})
	finish := make(chan struct{}, 1)
	defer close(finish)
	finished := make(chan error, 1)
	go func() {
		_, err := registry.Test(ctx, owner, created.ID, created.Revision, uuid.NewString(), func(context.Context, connector.Definition) error {
			close(entered)
			select {
			case <-finish:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		finished <- err
	}()
	select {
	case <-entered:
	case err := <-finished:
		t.Fatal("probe did not start:", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	failed, err := registry.Test(ctx, owner, created.ID, created.Revision, uuid.NewString(), func(_ context.Context, def connector.Definition) error {
		if def.Credentials.Secret != testSecret || def.Config.Namespace != input.Config.Namespace {
			t.Error("stored protected definition did not survive sealing")
		}
		return errors.New(testSecret)
	})
	if err != nil || failed.TestStatus != "FAILED" || failed.Enabled || failed.Revision != 2 {
		t.Fatalf("failed test was not recorded safely, status=%s err=%v", failed.TestStatus, err)
	}
	finish <- struct{}{}
	select {
	case err := <-finished:
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("stale probe overwrote a later test, err=%v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	key = uuid.NewString()
	tested, err := registry.Test(ctx, owner, failed.ID, failed.Revision, key, func(context.Context, connector.Definition) error { return nil })
	if err != nil || tested.TestStatus != "PASSED" || tested.Revision != 3 {
		t.Fatalf("successful test was not recorded, status=%s err=%v", tested.TestStatus, err)
	}
	if _, err := registry.Test(ctx, owner, failed.ID, failed.Revision, key, func(context.Context, connector.Definition) error {
		t.Error("replaying a completed test probed again")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	enabled, err := registry.SetEnabled(ctx, owner, tested.ID, tested.Revision, uuid.NewString(), true)
	if err != nil || !enabled.Enabled || enabled.Revision != 4 {
		t.Fatalf("tested connector could not be enabled, err=%v", err)
	}
	if def, err := registry.GetUsable(ctx, member, enabled.ID); err != nil || def.ID != enabled.ID {
		t.Fatalf("authorized actor could not use an enabled connector, err=%v", err)
	}
	for _, actor := range []metadata.Principal{owner, ungranted, external} {
		if _, err := registry.GetUsable(ctx, actor, enabled.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("actor could use a connector without permission, err=%v", err)
		}
	}
	disabled, err := registry.SetEnabled(ctx, owner, enabled.ID, enabled.Revision, uuid.NewString(), false)
	if err != nil || disabled.Enabled || disabled.Revision != 5 {
		t.Fatalf("could not disable the connector, err=%v", err)
	}
	if _, err := registry.GetUsable(ctx, member, enabled.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled connector remained callable, err=%v", err)
	}
	audit, err := registry.Events(ctx, owner, created.ID)
	if err != nil || len(audit) != 5 || audit[0].Type != "DISABLED" || audit[3].Type != "TEST_FAILED" || audit[4].Type != "CREATED" {
		t.Fatalf("unexpected registry audit: %+v err=%v", audit, err)
	}
	visible, err := registry.ListAdmin(ctx, owner)
	if err != nil || len(visible) != 1 {
		t.Fatalf("unexpected registry summaries, got %d err=%v", len(visible), err)
	}
	public, err := json.Marshal([]any{audit, visible})
	if err != nil || strings.Contains(string(public), testSecret) || strings.Contains(string(public), "warehouse") {
		t.Fatal("private definition escaped into control-plane views")
	}
}
