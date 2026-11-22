package metadata

import (
	"context"
	_ "embed"

	"github.com/jackc/pgx/v5"
	"github.com/xielforever/MCPGuard/internal/pgmigration"
)

//go:embed migrations/001_read_slice.sql
var readSliceDDL string

//go:embed migrations/002_authorization_lock.sql
var authorizationDDL string

//go:embed migrations/003_controlled_tools.sql
var controlledDDL string

//go:embed migrations/004_group_authorization.sql
var groupAuthorizationDDL string

//go:embed migrations/005_jenkins_resource.sql
var jenkinsResourceDDL string

//go:embed migrations/006_connectors.sql
var connectorsDDL string

//go:embed migrations/007_mysql_connectors.sql
var mysqlConnectorsDDL string

var schema = pgmigration.Set{
	Schema:  "mcpguard",
	Scripts: []string{readSliceDDL, authorizationDDL, controlledDDL, groupAuthorizationDDL, jenkinsResourceDDL, connectorsDDL, mysqlConnectorsDDL},
}

func PlanMigration(ctx context.Context, dsn string) (pgmigration.Plan, error) {
	return schema.DryRun(ctx, dsn)
}

func Migrate(ctx context.Context, dsn string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(729403612)"); err != nil {
		return err
	}
	if err = schema.Apply(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
