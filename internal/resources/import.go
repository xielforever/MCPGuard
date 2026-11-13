package resources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
)

// Import is administrative only: the gateway exposes no route to it.
func Import(ctx context.Context, adminDSN string, raw []byte) error {
	var b v02.CatalogBundle
	if v02.Decode("CatalogBundle", raw, &b) != nil {
		return ErrInput
	}
	org := b.Resources[0].OrgID
	for _, r := range b.Resources {
		if r.OrgID != org {
			return ErrDenied
		}
	}
	c, e := pgx.Connect(ctx, adminDSN)
	if e != nil {
		return ErrUnavailable
	}
	defer c.Close(ctx)
	tx, e := c.Begin(ctx)
	if e != nil {
		return ErrUnavailable
	}
	defer rollback(tx)
	var epoch int64
	if e = tx.QueryRow(ctx, "SELECT mcpguard.lock_auth_epoch_exclusive($1)", org).Scan(&epoch); e != nil {
		return ErrUnavailable
	}
	immutable := func(table, where string, args []any, definition any) error {
		value, _ := json.Marshal(definition)
		all := append(append([]any{}, args...), value)
		values := make([]string, len(all))
		for i := range values {
			values[i] = fmt.Sprintf("$%d", i+1)
		}
		columns := map[string]string{"resource_versions": "org_id,resource_id,version,definition", "profile_versions": "org_id,resource_id,profile_id,version,definition", "tool_bindings": "org_id,resource_id,resource_version,profile_id,profile_version,tool_name,tool_version,operation_id,definition"}[table]
		if columns == "" {
			return ErrInput
		}
		if _, e := tx.Exec(ctx, "INSERT INTO mcpguard_ra."+table+"("+columns+") VALUES("+join(values, ",")+") ON CONFLICT DO NOTHING", all...); e != nil {
			return e
		}
		var saved []byte
		if e := tx.QueryRow(ctx, "SELECT definition FROM mcpguard_ra."+table+" WHERE "+where, args...).Scan(&saved); e != nil {
			return e
		}
		var left, right any
		_ = json.Unmarshal(saved, &left)
		_ = json.Unmarshal(value, &right)
		l, _ := json.Marshal(left)
		r, _ := json.Marshal(right)
		if !bytes.Equal(l, r) {
			return ErrConflict
		}
		return nil
	}
	for _, r := range b.Resources {
		if e = immutable("resource_versions", "org_id=$1 AND resource_id=$2 AND version=$3", []any{r.OrgID, r.ResourceID, r.Version}, r); e != nil {
			return e
		}
	}
	for _, h := range b.Heads {
		if h.OrgID != org {
			return ErrDenied
		}
		var version, revision int64
		var enabled bool
		e = tx.QueryRow(ctx, "SELECT version,revision,enabled FROM mcpguard_ra.resource_heads WHERE org_id=$1 AND resource_id=$2", org, h.ResourceID).Scan(&version, &revision, &enabled)
		if errors.Is(e, pgx.ErrNoRows) {
			if h.Revision != "1" {
				return ErrConflict
			}
			_, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.resource_heads VALUES($1,$2,$3,$4,$5)", org, h.ResourceID, h.Version, h.Revision, h.Enabled)
		} else if e == nil {
			if version == number(h.Version) && enabled == h.Enabled && revision == number(h.Revision) {
				continue
			}
			if number(h.Version) < version || number(h.Revision) != revision+1 {
				return ErrConflict
			}
			_, e = tx.Exec(ctx, "UPDATE mcpguard_ra.resource_heads SET version=$3,revision=$4,enabled=$5 WHERE org_id=$1 AND resource_id=$2", org, h.ResourceID, h.Version, h.Revision, h.Enabled)
		}
		if e != nil {
			return e
		}
	}
	for _, p := range b.Profiles {
		if p.OrgID != org {
			return ErrDenied
		}
		if e = immutable("profile_versions", "org_id=$1 AND resource_id=$2 AND profile_id=$3 AND version=$4", []any{org, p.ResourceID, p.ProfileID, p.Version}, p); e != nil {
			return e
		}
	}
	for _, t := range b.Tools {
		if t.OrgID != org {
			return ErrDenied
		}
		if e = immutable("tool_bindings", "org_id=$1 AND resource_id=$2 AND resource_version=$3 AND profile_id=$4 AND profile_version=$5 AND tool_name=$6 AND tool_version=$7 AND operation_id=$8", []any{org, t.ResourceID, t.ResourceVersion, t.ProfileID, t.ProfileVersion, t.ToolName, t.ToolVersion, t.OperationID}, t); e != nil {
			return e
		}
	}
	for _, a := range b.Access {
		if a.OrgID != org {
			return ErrDenied
		}
		_, e = tx.Exec(ctx, `INSERT INTO mcpguard_ra.resource_access(org_id,principal_id,resource_id,can_discover,can_request) VALUES($1,$2,$3,$4,$5) ON CONFLICT(org_id,principal_id,resource_id) DO UPDATE SET can_discover=EXCLUDED.can_discover,can_request=EXCLUDED.can_request WHERE (resource_access.can_discover,resource_access.can_request) IS DISTINCT FROM (EXCLUDED.can_discover,EXCLUDED.can_request)`, org, a.PrincipalID, a.ResourceID, a.CanDiscover, a.CanRequest)
		if e != nil {
			return e
		}
	}
	for _, binding := range b.Bindings {
		if binding.OrgID != org {
			return ErrDenied
		}
		value, _ := json.Marshal(binding)
		_, e = tx.Exec(ctx, `INSERT INTO mcpguard_ra.identity_bindings VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`, org, binding.PrincipalID, binding.ResourceID, binding.BindingID, binding.BindingVersion, binding.ResourceVersion, binding.ProfileID, binding.ProfileVersion, binding.Enabled, value)
		if e != nil {
			return e
		}
		var saved []byte
		var enabled bool
		e = tx.QueryRow(ctx, "SELECT definition,enabled FROM mcpguard_ra.identity_bindings WHERE org_id=$1 AND principal_id=$2 AND resource_id=$3 AND binding_version=$4", org, binding.PrincipalID, binding.ResourceID, binding.BindingVersion).Scan(&saved, &enabled)
		if e != nil {
			return e
		}
		var previous v02.CredentialBinding
		if json.Unmarshal(saved, &previous) != nil {
			return ErrConflict
		}
		previous.Enabled = binding.Enabled
		equal, _ := json.Marshal(previous)
		if !bytes.Equal(equal, value) {
			return ErrConflict
		}
		if !enabled && binding.Enabled {
			return ErrConflict
		}
		if enabled && !binding.Enabled {
			if _, e = tx.Exec(ctx, "UPDATE mcpguard_ra.identity_bindings SET enabled=false WHERE org_id=$1 AND principal_id=$2 AND resource_id=$3 AND binding_version=$4", org, binding.PrincipalID, binding.ResourceID, binding.BindingVersion); e != nil {
				return e
			}
		}
	}
	return tx.Commit(ctx)
}
func join(values []string, separator string) string {
	out := ""
	for i, v := range values {
		if i > 0 {
			out += separator
		}
		out += v
	}
	return out
}
