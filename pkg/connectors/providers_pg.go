package connectors

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const maxTableNames = 100

func (p *Providers) listTables(ctx context.Context, def Definition) (any, error) {
	config := def.Config
	clientTLS, err := p.tlsConfig(config.Host, config)
	if err != nil {
		return nil, err
	}
	connection := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(def.Credentials.Username, def.Credentials.Secret),
		Host:     net.JoinHostPort(config.Host, strconv.Itoa(config.Port)),
		Path:     "/" + config.Database,
		RawQuery: "sslmode=disable&connect_timeout=3",
	}
	pgConfig, err := pgx.ParseConfig(connection.String())
	if err != nil {
		return nil, ErrInput
	}
	pgConfig.Database = config.Database
	pgConfig.TLSConfig = clientTLS
	pgConfig.Fallbacks = nil
	pgConfig.LookupFunc = func(ctx context.Context, host string) ([]string, error) {
		addresses, err := p.policy.Resolve(ctx, host)
		if err != nil {
			return nil, err
		}
		result := make([]string, 0, len(addresses))
		for _, address := range addresses {
			result = append(result, address.String())
		}
		return result, nil
	}
	pgConfig.DialFunc = p.policy.DialContext
	pgConfig.RuntimeParams = map[string]string{
		"application_name":              "mcpguard-connector",
		"default_transaction_read_only": "on",
		"statement_timeout":             "3000",
		"search_path":                   "pg_catalog",
	}
	conn, err := pgx.ConnectConfig(ctx, pgConfig)
	if err != nil {
		return nil, postgresReadError(err)
	}
	defer conn.Close(ctx)
	var allowed bool
	err = conn.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_catalog.pg_namespace
		WHERE nspname = $1 AND pg_catalog.has_schema_privilege(oid, 'USAGE')
	)`, config.Schema).Scan(&allowed)
	if err != nil {
		return nil, postgresReadError(err)
	}
	if !allowed {
		return nil, ErrDenied
	}
	rows, err := conn.Query(ctx, `SELECT relation.relname
		FROM pg_catalog.pg_class AS relation
		JOIN pg_catalog.pg_namespace AS schema ON schema.oid = relation.relnamespace
		WHERE schema.nspname = $1 AND relation.relkind IN ('r', 'p', 'v', 'm', 'f')
		AND pg_catalog.has_table_privilege(relation.oid, 'SELECT')
		ORDER BY relation.relname LIMIT $2`, config.Schema, maxTableNames+1)
	if err != nil {
		return nil, postgresReadError(err)
	}
	defer rows.Close()
	tables := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil || !validText(name, 255) {
			return nil, ErrUnavailable
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return nil, postgresReadError(err)
	}
	truncated := len(tables) > maxTableNames
	if truncated {
		tables = tables[:maxTableNames]
	}
	return ListTablesResult{Schema: config.Schema, Tables: tables, Truncated: truncated}, nil
}

func postgresReadError(err error) error {
	var pgErr *pgconn.PgError
	if errors.Is(err, ErrDenied) || (errors.As(err, &pgErr) &&
		(pgErr.Code == "42501" || pgErr.Code == "28P01" || pgErr.Code == "28000")) {
		return ErrDenied
	}
	return ErrUnavailable
}
