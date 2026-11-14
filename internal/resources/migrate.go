package resources

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrations embed.FS
var migrationFiles = []string{"001_catalog.sql", "002_access_workflow.sql", "003_native_provisioning.sql", "004_provision_recovery.sql", "005_credential_packages.sql", "006_private_downloads.sql", "007_credential_contexts.sql", "008_planned_reads.sql", "009_controlled_writes.sql"}

func migrationBytes(version int) []byte {
	b, _ := migrations.ReadFile("migrations/" + migrationFiles[version-1])
	return b
}
func migrationChecksum(version int) string {
	sum := sha256.Sum256(migrationBytes(version))
	return hex.EncodeToString(sum[:])
}
func catalogChecksum() string { return migrationChecksum(1) }

type rowQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func schemaVersion(ctx context.Context, q rowQuerier) (int, error) {
	rows, e := q.Query(ctx, "SELECT version,checksum FROM mcpguard_ra.schema_versions ORDER BY version")
	if e != nil {
		return 0, ErrUnavailable
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var v int
		var hash string
		if rows.Scan(&v, &hash) != nil || v != count+1 || v > len(migrationFiles) || hash != migrationChecksum(v) {
			return 0, ErrUnavailable
		}
		count++
	}
	if rows.Err() != nil || count == 0 {
		return 0, ErrUnavailable
	}
	return count, nil
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
	var present bool
	if e = tx.QueryRow(ctx, "SELECT to_regclass('mcpguard_ra.schema_versions') IS NOT NULL").Scan(&present); e != nil {
		return e
	}
	version := 0
	if present {
		version, e = schemaVersion(ctx, tx)
		if e != nil {
			return errors.New("catalog migration version or checksum mismatch")
		}
	}
	for next := version + 1; next <= len(migrationFiles); next++ {
		if _, e = tx.Exec(ctx, string(migrationBytes(next))); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.schema_versions VALUES($1,$2)", next, migrationChecksum(next)); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
