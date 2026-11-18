package nativepg

import (
	"context"
	"errors"
)

// CheckReady verifies the existing SELECT-only role and fixed query's columns.
// LIMIT 0 performs no business row read and accepts no caller-selected target.
func (r *Reader) CheckReady(ctx context.Context) error {
	var ready bool
	err := r.Pool.QueryRow(ctx, `SELECT current_setting('transaction_read_only')='on'
		AND NOT (r.rolsuper OR r.rolcreatedb OR r.rolcreaterole)
		AND has_table_privilege(current_user,'demo.items','SELECT')
		AND NOT has_table_privilege(current_user,'demo.items','INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
		AND `+DemoDatabasePredicate+`
		FROM pg_roles r WHERE r.rolname=current_user`).Scan(&ready)
	if err != nil {
		return err
	}
	if !ready {
		return errors.New("business PostgreSQL read role is not ready")
	}
	_, err = r.Pool.Exec(ctx, `SELECT scope_id,item_id,note,version::text FROM demo.items LIMIT 0`)
	return err
}
