package nativepg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// DemoDatabasePredicate checks the actual connected database. Both namespaces
// are reserved for protected resources and remain after a configuration rollback.
const DemoDatabasePredicate = `NOT EXISTS (
	SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname IN ('resource_demo', 'mcpguard_provision')
)`

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func CheckDemoDatabase(ctx context.Context, db rowQuerier) error {
	var allowed bool
	if err := db.QueryRow(ctx, "SELECT "+DemoDatabasePredicate).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return errors.New("legacy demo access requires an isolated database")
	}
	return nil
}
