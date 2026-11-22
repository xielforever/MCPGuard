package connectors

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
)

const maxMySQLColumns = 256

func validMySQLIdentifier(name string) bool {
	return validText(name, MaxMySQLIdentifierBytes) && utf8.RuneCountInString(name) <= 64 && !strings.HasSuffix(name, " ")
}

func validMySQLTableName(name string) bool {
	return validMySQLIdentifier(name) && !strings.ContainsAny(name, "./\\")
}

func (p *Providers) readMySQLMetadata(ctx context.Context, def Definition, table string) (any, error) {
	config := def.Config
	if !validMySQLTableName(config.Database) || config.Port < 1 || config.Port > 65535 {
		return nil, ErrInput
	}
	clientTLS, err := p.tlsConfig(config.Host, config)
	if err != nil {
		return nil, err
	}
	settings := mysql.NewConfig()
	settings.User = def.Credentials.Username
	settings.Passwd = def.Credentials.Secret
	settings.DBName = config.Database
	settings.Net = "tcp"
	settings.Addr = net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	settings.TLS = clientTLS
	settings.ReadTimeout = providerDeadline
	settings.WriteTimeout = providerDeadline
	settings.DialFunc = p.policy.DialContext
	settings.Logger = log.New(io.Discard, "", 0)
	connector, err := mysql.NewConnector(settings)
	if err != nil {
		return nil, ErrInput
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	if table == "" {
		return listMySQLTables(ctx, db, config.Database)
	}
	return listMySQLColumns(ctx, db, config.Database, table)
}

func listMySQLTables(ctx context.Context, db *sql.DB, database string) (MySQLTablesResult, error) {
	// The first predicate allows normal dictionary lookup. The byte-exact
	// predicate keeps case or collation aliases from crossing database scope.
	rows, err := db.QueryContext(ctx, `SELECT TABLE_NAME FROM INFORMATION_SCHEMA.TABLES
		WHERE TABLE_SCHEMA = ? AND CAST(TABLE_SCHEMA AS BINARY) = CAST(? AS BINARY)
		AND TABLE_TYPE = 'BASE TABLE'
		ORDER BY CAST(TABLE_NAME AS BINARY) LIMIT ?`, database, database, maxTableNames+1)
	if err != nil {
		return MySQLTablesResult{}, mysqlReadError(err)
	}
	defer rows.Close()
	tables := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil || !validMySQLTableName(name) {
			return MySQLTablesResult{}, ErrUnavailable
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return MySQLTablesResult{}, mysqlReadError(err)
	}
	truncated := len(tables) > maxTableNames
	if truncated {
		tables = tables[:maxTableNames]
	}
	return MySQLTablesResult{Database: database, Tables: tables, Truncated: truncated}, nil
}

func listMySQLColumns(ctx context.Context, db *sql.DB, database, table string) (MySQLColumnsResult, error) {
	rows, err := db.QueryContext(ctx, `SELECT c.COLUMN_NAME, c.DATA_TYPE, c.IS_NULLABLE, c.ORDINAL_POSITION
		FROM INFORMATION_SCHEMA.COLUMNS AS c
		JOIN INFORMATION_SCHEMA.TABLES AS t ON t.TABLE_SCHEMA = c.TABLE_SCHEMA AND t.TABLE_NAME = c.TABLE_NAME
		AND CAST(t.TABLE_SCHEMA AS BINARY) = CAST(c.TABLE_SCHEMA AS BINARY)
		AND CAST(t.TABLE_NAME AS BINARY) = CAST(c.TABLE_NAME AS BINARY)
		WHERE c.TABLE_SCHEMA = ? AND c.TABLE_NAME = ? AND t.TABLE_TYPE = 'BASE TABLE'
		AND CAST(c.TABLE_SCHEMA AS BINARY) = CAST(? AS BINARY) AND CAST(c.TABLE_NAME AS BINARY) = CAST(? AS BINARY)
		ORDER BY c.ORDINAL_POSITION LIMIT ?`, database, table, database, table, maxMySQLColumns+1)
	if err != nil {
		return MySQLColumnsResult{}, mysqlReadError(err)
	}
	defer rows.Close()
	columns := make([]MySQLColumn, 0)
	for rows.Next() {
		var column MySQLColumn
		var nullable string
		if err := rows.Scan(&column.Name, &column.DataType, &nullable, &column.OrdinalPosition); err != nil ||
			!validMySQLIdentifier(column.Name) || !validText(column.DataType, 64) ||
			column.OrdinalPosition < 1 || (nullable != "YES" && nullable != "NO") {
			return MySQLColumnsResult{}, ErrUnavailable
		}
		column.Nullable = nullable == "YES"
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return MySQLColumnsResult{}, mysqlReadError(err)
	}
	if len(columns) == 0 {
		return MySQLColumnsResult{}, ErrDenied
	}
	truncated := len(columns) > maxMySQLColumns
	if truncated {
		columns = columns[:maxMySQLColumns]
	}
	return MySQLColumnsResult{Database: database, Table: table, Columns: columns, Truncated: truncated}, nil
}

func mysqlReadError(err error) error {
	if errors.Is(err, ErrDenied) {
		return ErrDenied
	}
	var serverError *mysql.MySQLError
	if errors.As(err, &serverError) {
		switch serverError.Number {
		case 1044, 1045, 1049, 1142, 1143: // Account, database, table or column access denied.
			return ErrDenied
		}
	}
	return ErrUnavailable
}
