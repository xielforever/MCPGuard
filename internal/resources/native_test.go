package resources

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"github.com/xielforever/MCPGuard/internal/runtimeconfig"
)

func TestPrivateProviderRejectsAmbiguousAndPublicFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "private.json")
	valid := `{"endpoints":{"pg":{"host":"127.0.0.1","port":5432,"database":"mcpguard_business","sslMode":"disable"}},"secrets":{"a@1":{"username":"ra_alice","password":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","version":"1"}}}`
	if os.WriteFile(path, []byte(valid), 0600) != nil {
		t.Fatal("cannot prepare isolated private file")
	}
	if _, e := LoadFileSecrets(path); e != nil {
		t.Fatal("valid private provider rejected")
	}
	if os.Chmod(path, 0644) != nil {
		t.Fatal("chmod failed")
	}
	if _, e := LoadFileSecrets(path); e == nil {
		t.Fatal("world-readable credential file accepted")
	}
	_ = os.Chmod(path, 0600)
	for _, raw := range []string{valid + " {}", strings.Replace(valid, `"version":"1"`, `"version":"1","version":"2"`, 1), strings.Replace(valid, "127.0.0.1", "192.0.2.10", 1), strings.Replace(valid, "mcpguard_business", "mcpguard_meta", 1)} {
		_ = os.WriteFile(path, []byte(raw), 0600)
		if _, e := LoadFileSecrets(path); e == nil {
			t.Fatal("unsafe private configuration accepted")
		}
	}
	_ = os.WriteFile(path, []byte(valid), 0600)
	link := filepath.Join(dir, "symlink.json")
	if e := os.Symlink(path, link); e != nil {
		t.Fatal(e)
	}
	if _, e := LoadFileSecrets(link); e == nil {
		t.Fatal("credential symlink accepted")
	}
}
func TestNativePoolHygieneIntegration(t *testing.T) {
	if os.Getenv("MCPGUARD_RA_CONFIG") == "" {
		t.Skip("fresh RA fixture required")
	}
	cfg, e := runtimeconfig.Load()
	if e != nil {
		t.Fatal(e)
	}
	vault, e := metadata.NewVault(cfg.EncryptionKey, cfg.PlanKey)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	store, e := metadata.Open(ctx, cfg.MetadataDSN, vault)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	c, e := NewCatalog(ctx, store)
	if e != nil {
		t.Fatal(e)
	}
	provider, e := LoadFileSecrets(os.Getenv("MCPGUARD_RA_PRIVATE_CONFIG"))
	if e != nil {
		t.Fatal(e)
	}
	p, e := store.VerifyToken(ctx, os.Getenv("MCPGUARD_TEST_ALICE_TOKEN"))
	if e != nil {
		t.Fatal(e)
	}
	m := NewReadManager(c, provider)
	defer m.Close()
	resourceID := "40000000-0000-4000-8000-000000000002"
	result, e := m.ReadFixture(ctx, p, v02.FixtureReadInput{ResourceID: resourceID, ItemID: "item_demo_1"})
	if e != nil || !result.Found {
		t.Fatal("native pool setup failed")
	}
	snapshot, e := c.Snapshot(ctx, p, resourceID)
	if e != nil {
		t.Fatal(e)
	}
	pool, e := m.pool(ctx, p, snapshot)
	if e != nil {
		t.Fatal(e)
	}
	acquire, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, e := pool.Acquire(acquire)
	if e != nil {
		t.Fatal(e)
	}
	_, e = conn.Exec(ctx, "SET default_transaction_read_only=off")
	if e == nil {
		_, e = conn.Exec(ctx, "SET search_path=pg_temp; CREATE TEMP TABLE ra_private_state(note text); INSERT INTO ra_private_state VALUES('must be discarded')")
	}
	conn.Release()
	if e != nil {
		t.Fatal("cannot inject session-state test")
	}
	conn, e = pool.Acquire(acquire)
	if e != nil {
		t.Fatal(e)
	}
	var clean bool
	e = conn.QueryRow(ctx, "SELECT to_regclass('pg_temp.ra_private_state') IS NULL").Scan(&clean)
	conn.Release()
	if e != nil || !clean {
		t.Fatal("temporary session state survived release")
	}
	result, e = m.ReadFixture(ctx, p, v02.FixtureReadInput{ResourceID: resourceID, ItemID: "item_demo_1"})
	if e != nil || result.NativeIdentity != "ra_alice" {
		t.Fatal("pool reset changed native identity or read behavior")
	}
	next := snapshot
	next.Binding.BindingVersion = "2"
	if _, e = m.pool(ctx, p, next); e != nil || m.PoolCount() != 1 {
		t.Fatal("superseded credential pool not retired")
	}
	// A private endpoint change without the corresponding immutable catalog
	// fingerprint must not silently point an existing binding at a different PG.
	original := provider.endpoints[snapshot.Resource.EndpointConfigRef]
	changed := original
	changed.Port++
	provider.endpoints[snapshot.Resource.EndpointConfigRef] = changed
	if _, e = m.pool(ctx, p, snapshot); e == nil {
		t.Fatal("endpoint fingerprint substitution accepted")
	}
	provider.endpoints[snapshot.Resource.EndpointConfigRef] = original
	private, _ := json.Marshal(snapshot)
	if strings.Contains(string(private), "password") {
		t.Fatal("snapshot contains a native secret")
	}
}
