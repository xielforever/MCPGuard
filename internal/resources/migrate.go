package resources

import (
	"context"
	"embed"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/xielforever/MCPGuard/internal/pgmigration"
)

//go:embed migrations/*.sql
var migrations embed.FS
var migrationFiles = []string{"001_catalog.sql", "002_access_workflow.sql", "003_native_provisioning.sql", "004_provision_recovery.sql", "005_credential_packages.sql", "006_private_downloads.sql", "007_credential_contexts.sql", "008_planned_reads.sql", "009_controlled_writes.sql", "010_revocation.sql", "011_revocation_cleanup.sql", "012_recovery_fence.sql", "013_cleanup_receipts.sql"}

var catalogSchema = func() pgmigration.Set {
	scripts := make([]string, 0, len(migrationFiles))
	for _, name := range migrationFiles {
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			panic(err)
		}
		scripts = append(scripts, string(body))
	}
	return pgmigration.Set{Schema: "mcpguard_ra", Scripts: scripts}
}()

func migrationBytes(version int) []byte { return []byte(catalogSchema.Scripts[version-1]) }
func migrationChecksum(version int) string {
	return pgmigration.Checksum(catalogSchema.Scripts[version-1])
}

func schemaVersion(ctx context.Context, q pgmigration.Querier) (int, error) {
	version, err := catalogSchema.Version(ctx, q)
	if err != nil {
		return 0, ErrUnavailable
	}
	return version, nil
}

func PlanMigration(ctx context.Context, dsn string) (pgmigration.Plan, error) {
	return catalogSchema.DryRun(ctx, dsn)
}

// Migrate is an explicit administrative operation; startup never upgrades.
func Migrate(ctx context.Context, adminDSN string) error {
	c, e := pgx.Connect(ctx, adminDSN)
	if e != nil {
		return errors.New("catalog migration connection unavailable")
	}
	defer c.Close(ctx)
	tx, e := c.Begin(ctx)
	if e != nil {
		return e
	}
	defer rollback(tx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(729403613)"); e != nil {
		return e
	}
	if e = catalogSchema.Apply(ctx, tx); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
