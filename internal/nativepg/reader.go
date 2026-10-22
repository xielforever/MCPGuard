// Package nativepg contains one fixed native operation. No SQL is accepted from MCP.
package nativepg

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jackc/pgx/v5/pgxpool"
)

const ReadSQL = `SELECT item_id,note,version::text FROM demo.items WHERE scope_id=$1 AND item_id=$2 LIMIT 1`

type Reader struct{ Pool *pgxpool.Pool }
type Item struct {
	ItemID  string `json:"itemId"`
	Note    string `json:"note"`
	Version string `json:"version"`
}
type Data struct {
	Found bool  `json:"found"`
	Item  *Item `json:"item"`
}
type Outcome struct {
	Data     *Data
	Err      error
	Dispatch string
}

func Open(ctx context.Context, dsn string) (*Reader, error) {
	c, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		return nil, e
	}
	c.MaxConns = 4
	c.MinConns = 0
	c.ConnConfig.ConnectTimeout = 3 * time.Second
	c.ConnConfig.RuntimeParams["application_name"] = "mcpguard-native-reader"
	c.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	c.ConnConfig.RuntimeParams["statement_timeout"] = "3000"
	c.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "5000"
	c.ConnConfig.BuildContextWatcherHandler = func(conn *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: conn, CancelRequestDelay: 0, DeadlineDelay: time.Second}
	}
	p, e := pgxpool.NewWithConfig(ctx, c)
	if e != nil {
		return nil, e
	}
	var unsafe bool
	e = p.QueryRow(ctx, `SELECT r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR has_table_privilege(current_user,'demo.items','INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER') FROM pg_roles r WHERE rolname=current_user`).Scan(&unsafe)
	if e == nil && unsafe {
		e = errors.New("native reader role has excessive privileges")
	}
	if e != nil {
		p.Close()
		return nil, e
	}
	return &Reader{p}, nil
}
func (r *Reader) Close() { r.Pool.Close() }

func (r *Reader) Read(ctx context.Context, scope, itemID string) Outcome {
	out := Outcome{Dispatch: "NOT_DISPATCHED"}
	if e := ctx.Err(); e != nil {
		out.Err = e
		return out
	}
	tx, e := r.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if e != nil {
		out.Err = e
		return out
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = tx.Rollback(c)
	}()
	if e = ctx.Err(); e != nil {
		out.Err = e
		return out
	}
	// Only a server reply proves this SELECT was dispatched. A connection error
	// after entering the driver leaves dispatch uncertain, even for a pure read.
	out.Dispatch = "DISPATCH_UNKNOWN"
	i := &Item{}
	e = tx.QueryRow(ctx, ReadSQL, scope, itemID).Scan(&i.ItemID, &i.Note, &i.Version)
	var pgErr *pgconn.PgError
	if e == nil || errors.Is(e, pgx.ErrNoRows) || errors.As(e, &pgErr) {
		out.Dispatch = "DISPATCHED"
	}
	if errors.Is(e, pgx.ErrNoRows) {
		out.Data = &Data{Found: false, Item: nil}
		return out
	}
	if e != nil {
		out.Err = e
		return out
	}
	if !utf8.ValidString(i.Note) || utf8.RuneCountInString(i.Note) > 500 || len(i.Note) > 2000 {
		out.Err = errors.New("result limit exceeded")
		return out
	}
	out.Data = &Data{Found: true, Item: i}
	return out
}
