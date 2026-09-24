package store

import (
	"database/sql"

	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	db *sql.DB
}

func NewStore(driver, dsn string) (*Store, error) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) InitSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS rules (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT,
		pattern TEXT,
		tool_name TEXT,
		action TEXT,
		enabled BOOLEAN,
		priority INTEGER
	);

	CREATE TABLE IF NOT EXISTS audit_logs (
		id TEXT PRIMARY KEY,
		timestamp DATETIME,
		tool_name TEXT,
		params TEXT,
		action TEXT,
		rule_name TEXT,
		confirm_code TEXT,
		status TEXT,
		user_id TEXT
	);

	CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		username TEXT
	);

	CREATE TABLE IF NOT EXISTS confirmations (
		id TEXT PRIMARY KEY,
		code TEXT,
		status TEXT
	);
	`
	_, err := s.db.Exec(schema)
	return err
}

func (s *Store) Close() error {
	return s.db.Close()
}
