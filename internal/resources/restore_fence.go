package resources

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RestoreFence compares the recovery generation recorded in metadata with the
// generation kept by the native provider. The native copy is never restored
// with the metadata backup, so a stale restore shows up as native > expected.
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
	var generation int64
	e := f.Pool.QueryRow(ctx, `SELECT generation FROM mcpguard_ra.recovery_fence WHERE org_id=$1`, org).Scan(&generation)
	if errors.Is(e, pgx.ErrNoRows) {
		return 0, nil
	}
	if e != nil {
		return 0, ErrUnavailable
	}
	return generation, nil
}

// Check refuses when the native side is ahead of the metadata record.
func (f *RestoreFence) Check(ctx context.Context, org, endpointRef string) error {
	native, e := f.Native(ctx, org, endpointRef)
	if e != nil {
		return e
	}
	expected, e := f.Expected(ctx, org)
	if e != nil {
		return e
	}
	if native > expected {
		return ErrDenied
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

// Record stores an accepted recovery point and the operator who verified it.
func (f *RestoreFence) Record(ctx context.Context, org string, generation int64, verifier string) error {
	_, e := f.Pool.Exec(ctx, `INSERT INTO mcpguard_ra.recovery_fence(org_id,generation,verified_at,verified_by,updated_at) VALUES($1,$2,clock_timestamp(),$3,clock_timestamp()) ON CONFLICT(org_id) DO UPDATE SET generation=$2,verified_at=clock_timestamp(),verified_by=$3,updated_at=clock_timestamp()`, org, generation, verifier)
	if e != nil {
		return ErrUnavailable
	}
	return nil
}
