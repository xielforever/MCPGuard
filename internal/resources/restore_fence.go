package resources

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errRecoveryPointUnverified = fmt.Errorf("native recovery point requires reconciliation: %w", ErrDenied)

// RestoreFence compares the recovery generation recorded in metadata with the
// highest generation kept by the native providers. The native copies are
// never restored with the metadata backup.
type RestoreFence struct {
	Pool     *pgxpool.Pool
	Provider *PGProvisioningProvider
}

func NewRestoreFence(pool *pgxpool.Pool, provider *PGProvisioningProvider) *RestoreFence {
	return &RestoreFence{Pool: pool, Provider: provider}
}

// Native reads the generation of one endpoint's native fence.
func (f *RestoreFence) Native(ctx context.Context, org, endpointRef string) (int64, error) {
	conn, e := f.Provider.manager(ctx, endpointRef)
	if e != nil {
		return 0, ErrUnavailable
	}
	defer conn.Close(context.Background())
	var generation int64
	if e = conn.QueryRow(ctx, "SELECT mcpguard_provision.fence_generation($1)", org).Scan(&generation); e != nil {
		return 0, ErrUnavailable
	}
	return generation, nil
}

// NativeMax reports the highest native generation across configured targets.
func (f *RestoreFence) NativeMax(ctx context.Context, org string) (int64, error) {
	var max int64
	seen := false
	for ref := range f.Provider.Private.Managers {
		generation, e := f.Native(ctx, org, ref)
		if e != nil {
			return 0, e
		}
		seen = true
		if generation > max {
			max = generation
		}
	}
	if !seen {
		return 0, ErrUnavailable
	}
	return max, nil
}

// Expected reads the metadata recovery point for the organization.
func (f *RestoreFence) Expected(ctx context.Context, org string) (int64, error) {
	return expectedRecoveryGeneration(ctx, f.Pool, org)
}

func expectedRecoveryGeneration(ctx context.Context, db databaseClock, org string) (int64, error) {
	var generation int64
	e := db.QueryRow(ctx, `SELECT generation FROM mcpguard_ra.recovery_fence WHERE org_id=$1`, org).Scan(&generation)
	if errors.Is(e, pgx.ErrNoRows) {
		return 0, nil
	}
	if e != nil {
		return 0, ErrUnavailable
	}
	return generation, nil
}

// Check requires a verified organization-wide recovery point. Providers can
// have different counters after a partial failure or a configuration change.
func (f *RestoreFence) Check(ctx context.Context, org, endpointRef string) error {
	if _, ok := f.Provider.Private.Managers[endpointRef]; !ok {
		return ErrUnavailable
	}
	native, e := f.NativeMax(ctx, org)
	if e != nil {
		return e
	}
	expected, e := f.Expected(ctx, org)
	if e != nil {
		return e
	}
	if native != expected {
		return errRecoveryPointUnverified
	}
	return nil
}

// Advance bumps every configured native fence for the organization and returns
// the new maximum. It must run before a revocation commits its metadata change,
// so a crash leaves the native side ahead (fail closed) instead of behind.
func (f *RestoreFence) Advance(ctx context.Context, org string) (int64, error) {
	var max int64
	seen := false
	for ref := range f.Provider.Private.Managers {
		conn, e := f.Provider.manager(ctx, ref)
		if e != nil {
			return 0, ErrUnavailable
		}
		var generation int64
		e = conn.QueryRow(ctx, "SELECT mcpguard_provision.advance_fence($1)", org).Scan(&generation)
		conn.Close(context.Background())
		if e != nil {
			return 0, ErrUnavailable
		}
		seen = true
		if generation > max {
			max = generation
		}
	}
	if !seen {
		return 0, ErrUnavailable
	}
	return max, nil
}

// advanceAfterRevocation runs under the caller's authorization lock. If the
// recovered ledger is still suspect, allow the restrictive change to commit
// without accepting the recovery point on behalf of its reviewers.
func (f *RestoreFence) advanceAfterRevocation(ctx context.Context, tx pgx.Tx, org, actor string) error {
	expected, e := expectedRecoveryGeneration(ctx, tx, org)
	if e != nil {
		return e
	}
	native, e := f.NativeMax(ctx, org)
	if e != nil {
		return e
	}
	if native != expected {
		return nil
	}
	generation, e := f.Advance(ctx, org)
	if e != nil {
		return e
	}
	if generation != expected+1 {
		return ErrConflict
	}
	return recordRecoveryGeneration(ctx, tx, org, generation, actor)
}

// Record stores an accepted recovery point and the operator who verified it.
func (f *RestoreFence) Record(ctx context.Context, org string, generation int64, verifier string) error {
	tx, e := f.Pool.Begin(ctx)
	if e != nil {
		return ErrUnavailable
	}
	defer rollback(tx)
	var epoch int64
	if e = tx.QueryRow(ctx, `SELECT mcpguard.lock_auth_epoch_exclusive($1)`, org).Scan(&epoch); e != nil {
		return ErrUnavailable
	}
	expected, e := expectedRecoveryGeneration(ctx, tx, org)
	if e != nil {
		return e
	}
	native, e := f.NativeMax(ctx, org)
	if e != nil {
		return e
	}
	if native != generation || generation < expected {
		return ErrConflict
	}
	if generation > expected {
		// Approvals from the restored snapshot must be planned and reviewed
		// again against the accepted authorization state.
		if e = tx.QueryRow(ctx, `SELECT mcpguard_ra.advance_access_epoch($1)`, org).Scan(&epoch); e != nil {
			return ErrUnavailable
		}
	}
	if e = recordRecoveryGeneration(ctx, tx, org, generation, verifier); e != nil {
		return e
	}
	return commit(ctx, tx)
}

func recordRecoveryGeneration(ctx context.Context, tx pgx.Tx, org string, generation int64, verifier string) error {
	_, e := tx.Exec(ctx, `INSERT INTO mcpguard_ra.recovery_fence(org_id,generation,verified_at,verified_by,updated_at) VALUES($1,$2,clock_timestamp(),$3,clock_timestamp()) ON CONFLICT(org_id) DO UPDATE SET generation=$2,verified_at=clock_timestamp(),verified_by=$3,updated_at=clock_timestamp()`, org, generation, verifier)
	if e != nil {
		return ErrUnavailable
	}
	return nil
}
