package resources

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
)

// ImportAccess is an administrative, versioned import. No browser/MCP caller can
// choose its reviewers or manufacture an automatic policy decision.
func ImportAccess(ctx context.Context, dsn string, raw []byte) error {
	var cfg v02.AccessConfiguration
	if v02.Decode("AccessConfiguration", raw, &cfg) != nil || len(cfg.Policies) == 0 {
		return ErrInput
	}
	org := cfg.Policies[0].OrgID
	c, e := pgx.Connect(ctx, dsn)
	if e != nil {
		return ErrUnavailable
	}
	defer c.Close(ctx)
	tx, e := c.Begin(ctx)
	if e != nil {
		return e
	}
	defer rollback(tx)
	var epoch int64
	if e = tx.QueryRow(ctx, "SELECT mcpguard.lock_auth_epoch_exclusive($1)", org).Scan(&epoch); e != nil {
		return e
	}
	for _, p := range cfg.Policies {
		if p.OrgID != org {
			return ErrDenied
		}
		var policyRef string
		if e = tx.QueryRow(ctx, "SELECT definition->>'approvalPolicyRef' FROM mcpguard_ra.profile_versions WHERE org_id=$1 AND resource_id=$2 AND profile_id=$3 AND version=$4", org, p.ResourceID, p.ProfileID, p.ProfileVersion).Scan(&policyRef); e != nil {
			return e
		}
		if p.ApprovalPolicyRef != policyRef {
			return ErrConflict
		}
		data, _ := json.Marshal(p)
		args := []any{org, p.ResourceID, p.ResourceVersion, p.ProfileID, p.ProfileVersion, p.Version, data}
		if _, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.access_policy_versions VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING", args...); e != nil {
			return e
		}
		var equal bool
		if e = tx.QueryRow(ctx, "SELECT definition=$7::jsonb FROM mcpguard_ra.access_policy_versions WHERE org_id=$1 AND resource_id=$2 AND resource_version=$3 AND profile_id=$4 AND profile_version=$5 AND version=$6", args...).Scan(&equal); e != nil {
			return e
		}
		if !equal {
			return ErrConflict
		}
	}
	for _, h := range cfg.Heads {
		if h.OrgID != org {
			return ErrDenied
		}
		var version, revision int64
		var enabled bool
		args := []any{org, h.ResourceID, h.ResourceVersion, h.ProfileID, h.ProfileVersion}
		e = tx.QueryRow(ctx, "SELECT version,revision,enabled FROM mcpguard_ra.access_policy_heads WHERE org_id=$1 AND resource_id=$2 AND resource_version=$3 AND profile_id=$4 AND profile_version=$5", args...).Scan(&version, &revision, &enabled)
		values := append(args, h.Version, h.Revision, h.Enabled)
		if errors.Is(e, pgx.ErrNoRows) {
			if h.Revision != "1" {
				return ErrConflict
			}
			_, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.access_policy_heads VALUES($1,$2,$3,$4,$5,$6,$7,$8)", values...)
		} else if e == nil {
			if version == number(h.Version) && revision == number(h.Revision) && enabled == h.Enabled {
				continue
			}
			if number(h.Version) < version || number(h.Revision) != revision+1 {
				return ErrConflict
			}
			_, e = tx.Exec(ctx, "UPDATE mcpguard_ra.access_policy_heads SET version=$6,revision=$7,enabled=$8 WHERE org_id=$1 AND resource_id=$2 AND resource_version=$3 AND profile_id=$4 AND profile_version=$5", values...)
		}
		if e != nil {
			return e
		}
	}
	for _, r := range cfg.Reviewers {
		if r.OrgID != org {
			return ErrDenied
		}
		if _, e = tx.Exec(ctx, `INSERT INTO mcpguard_ra.access_reviewers VALUES($1,$2,$3,$4) ON CONFLICT(org_id,resource_id,principal_id) DO UPDATE SET can_approve=EXCLUDED.can_approve WHERE access_reviewers.can_approve IS DISTINCT FROM EXCLUDED.can_approve`, org, r.ResourceID, r.PrincipalID, r.CanApprove); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
