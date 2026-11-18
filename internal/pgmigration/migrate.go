// Package pgmigration validates and applies append-only PostgreSQL migrations.
package pgmigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

type Plan struct {
	Schema  string `json:"schema"`
	Current int    `json:"currentVersion"`
	Pending []int  `json:"pendingVersions"`
}

type Set struct {
	Schema  string
	Scripts []string // One script per version, starting at version 1.
}

type Querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

var ErrHistory = errors.New("migration history is missing or invalid")

// Checksum writes one stable checksum regardless of the checkout's line endings.
func Checksum(script string) string {
	return digest(strings.ReplaceAll(script, "\r\n", "\n"))
}

func digest(script string) string {
	sum := sha256.Sum256([]byte(script))
	return hex.EncodeToString(sum[:])
}

func (s Set) table() string {
	return pgx.Identifier{s.Schema, "schema_versions"}.Sanitize()
}

// Version verifies every recorded version in an already initialized schema.
func (s Set) Version(ctx context.Context, db Querier) (int, error) {
	rows, err := db.Query(ctx, "SELECT version, checksum FROM "+s.table()+" ORDER BY version")
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	current := 0
	for rows.Next() {
		var version int
		var saved string
		if err := rows.Scan(&version, &saved); err != nil {
			return 0, err
		}
		if version != current+1 || version > len(s.Scripts) {
			return 0, fmt.Errorf("%w at version %d", ErrHistory, version)
		}
		// Older binaries hashed the embedded bytes. Recognize both Git
		// checkout formats without rewriting an installed checksum.
		lf := strings.ReplaceAll(s.Scripts[version-1], "\r\n", "\n")
		if saved != digest(lf) && saved != digest(strings.ReplaceAll(lf, "\n", "\r\n")) {
			return 0, fmt.Errorf("%w at version %d", ErrHistory, version)
		}
		current = version
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if current == 0 {
		return 0, ErrHistory
	}
	return current, nil
}

func (s Set) inspect(ctx context.Context, db Querier) (Plan, error) {
	plan := Plan{Schema: s.Schema, Pending: []int{}}
	var exists bool
	if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname=$1)`, s.Schema).Scan(&exists); err != nil {
		return plan, err
	}
	if exists {
		version, err := s.Version(ctx, db)
		if err != nil {
			return plan, err
		}
		plan.Current = version
	}
	for version := plan.Current + 1; version <= len(s.Scripts); version++ {
		plan.Pending = append(plan.Pending, version)
	}
	return plan, nil
}

// DryRun reads a consistent snapshot and never runs DDL, takes the apply lock,
// or changes the migration history.
func (s Set) DryRun(ctx context.Context, dsn string) (Plan, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return Plan{}, err
	}
	defer conn.Close(ctx)
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Plan{}, err
	}
	defer tx.Rollback(ctx)
	return s.inspect(ctx, tx)
}

// Apply runs inside the caller's transaction after it acquires its migration lock.
func (s Set) Apply(ctx context.Context, tx pgx.Tx) error {
	plan, err := s.inspect(ctx, tx)
	if err != nil {
		return err
	}
	for _, version := range plan.Pending {
		script := s.Scripts[version-1]
		if _, err = tx.Exec(ctx, script); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO "+s.table()+" VALUES ($1,$2)", version, Checksum(script)); err != nil {
			return err
		}
	}
	return nil
}
