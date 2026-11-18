package resources

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var errCommitOutcomeUnknown = errors.New("native commit outcome unknown")

// A revocation can commit on a different gateway replica. Poll only during a
// native attempt and cancel its context if its durable dispatch state changes
// or cannot be verified. The native pool sends PostgreSQL's CancelRequest for
// the borrowed connection. Sending that request does not establish an outcome.
func watchDispatch(ctx context.Context, pool *pgxpool.Pool, org, id string, write bool) (context.Context, func()) {
	query := `SELECT state='RUNNING' FROM mcpguard_ra.read_plans WHERE org_id=$1 AND plan_id=$2`
	if write {
		query = `SELECT state='EXECUTING' FROM mcpguard_ra.write_requests WHERE org_id=$1 AND request_id=$2`
	}
	call, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-call.Done():
				return
			case <-ticker.C:
			}
			check, finish := context.WithTimeout(call, 250*time.Millisecond)
			var current bool
			err := pool.QueryRow(check, query, org, id).Scan(&current)
			finish()
			if err != nil || !current {
				cancel()
				return
			}
		}
	}()
	return call, func() {
		cancel()
		<-done
	}
}
