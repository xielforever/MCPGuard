package metadata

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrations embed.FS

func Migrate(ctx context.Context, dsn string) error {
	c, e := pgx.Connect(ctx, dsn)
	if e != nil {
		return e
	}
	defer c.Close(ctx)
	tx, e := c.Begin(ctx)
	if e != nil {
		return e
	}
	defer rollback(tx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(729403612)"); e != nil {
		return e
	}
	var exists bool
	if e = tx.QueryRow(ctx, "SELECT to_regclass('mcpguard.schema_versions') IS NOT NULL").Scan(&exists); e != nil {
		return e
	}
	files := []string{"001_read_slice.sql", "002_authorization_lock.sql"}
	if exists {
		var max int
		if e = tx.QueryRow(ctx, "SELECT coalesce(max(version),0) FROM mcpguard.schema_versions").Scan(&max); e != nil {
			return e
		}
		if max > len(files) {
			return errors.New("database schema is newer than this binary")
		}
	}
	for i, file := range files {
		version := i + 1
		ddl, e := migrations.ReadFile("migrations/" + file)
		if e != nil {
			return e
		}
		h := sha256.Sum256(ddl)
		checksum := hex.EncodeToString(h[:])
		saved := ""
		if exists {
			e = tx.QueryRow(ctx, "SELECT checksum FROM mcpguard.schema_versions WHERE version=$1", version).Scan(&saved)
			if e != nil && !errors.Is(e, pgx.ErrNoRows) {
				return e
			}
		}
		if saved != "" {
			if saved != checksum {
				return fmt.Errorf("migration %d checksum mismatch", version)
			}
			continue
		}
		if _, e = tx.Exec(ctx, string(ddl)); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO mcpguard.schema_versions VALUES($1,$2)", version, checksum); e != nil {
			return e
		}
		exists = true
	}
	return tx.Commit(ctx)
}
