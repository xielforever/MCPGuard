package resources

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
)

// ImportProvisioning is administrative only. Management secrets are supplied to
// the separate provisioner process, never imported into public catalog views.
func ImportProvisioning(ctx context.Context, dsn string, raw []byte) error {
	var cfg v02.ProvisioningConfiguration
	if v02.Decode("ProvisioningConfiguration", raw, &cfg) != nil || len(cfg.Targets) == 0 {
		return ErrInput
	}
	org := cfg.Targets[0].OrgID
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
	for _, target := range cfg.Targets {
		if target.OrgID != org {
			return ErrDenied
		}
		var resourceRaw, profileRaw []byte
		e = tx.QueryRow(ctx, `SELECT r.definition,p.definition FROM mcpguard_ra.resource_versions r JOIN mcpguard_ra.profile_versions p ON (p.org_id,p.resource_id)=(r.org_id,r.resource_id) WHERE r.org_id=$1 AND r.resource_id=$2 AND r.version=$3 AND p.profile_id=$4 AND p.version=$5`, org, target.ResourceID, target.ResourceVersion, target.ProfileID, target.ProfileVersion).Scan(&resourceRaw, &profileRaw)
		var r v02.ResourceVersion
		var p v02.PermissionProfile
		if e != nil || v02.Decode("ResourceVersion", resourceRaw, &r) != nil || v02.Decode("PermissionProfile", profileRaw, &p) != nil {
			return ErrUnavailable
		}
		if r.Kind != "POSTGRES" || p.NativeEnforcement != "PG_OWNER_RLS_V1" || len(p.AllowedOperations) != 1 || p.AllowedOperations[0] != ReadOperation || r.EndpointConfigRef != target.EndpointConfigRef || r.TargetFingerprint != target.TargetFingerprint {
			return ErrDenied
		}
		value, _ := json.Marshal(target)
		if e = putProvisionConfig(ctx, tx, "provision_targets", []any{org, target.ResourceID, target.ResourceVersion, target.ProfileID, target.ProfileVersion}, target.Version, target.Enabled, value); e != nil {
			return e
		}
	}
	for _, registration := range cfg.ExistingAccounts {
		if registration.OrgID != org {
			return ErrDenied
		}
		var targetExists bool
		e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT FROM mcpguard_ra.provision_targets WHERE org_id=$1 AND resource_id=$2 AND resource_version=$3 AND profile_id=$4 AND profile_version=$5)", org, registration.ResourceID, registration.ResourceVersion, registration.ProfileID, registration.ProfileVersion).Scan(&targetExists)
		if e != nil || !targetExists {
			return ErrDenied
		}
		value, _ := json.Marshal(registration)
		if e = putProvisionConfig(ctx, tx, "existing_account_registrations", []any{org, registration.SubjectID, registration.ResourceID, registration.ResourceVersion, registration.ProfileID, registration.ProfileVersion}, registration.Version, registration.Enabled, value); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func putProvisionConfig(ctx context.Context, tx pgx.Tx, table string, key []any, version string, enabled bool, value []byte) error {
	where, columns := "org_id=$1 AND resource_id=$2 AND resource_version=$3 AND profile_id=$4 AND profile_version=$5", "org_id,resource_id,resource_version,profile_id,profile_version"
	if table == "existing_account_registrations" {
		where = "org_id=$1 AND subject_id=$2 AND resource_id=$3 AND resource_version=$4 AND profile_id=$5 AND profile_version=$6"
		columns = "org_id,subject_id,resource_id,resource_version,profile_id,profile_version"
	} else if table != "provision_targets" {
		return ErrInput
	}
	var oldVersion int64
	var same bool
	idx := len(key) + 1
	args := append(append([]any{}, key...), value)
	e := tx.QueryRow(ctx, "SELECT version,definition=$"+strconvI(idx)+"::jsonb FROM mcpguard_ra."+table+" WHERE "+where, args...).Scan(&oldVersion, &same)
	if e == nil {
		if same {
			return nil
		}
		if number(version) <= oldVersion {
			return ErrConflict
		}
		values := append(append([]any{}, key...), version, enabled, value)
		_, e = tx.Exec(ctx, "UPDATE mcpguard_ra."+table+" SET version=$"+strconvI(idx)+",enabled=$"+strconvI(idx+1)+",definition=$"+strconvI(idx+2)+" WHERE "+where, values...)
		return e
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	values := append(append([]any{}, key...), version, enabled, value)
	marks := ""
	for i := range values {
		if i > 0 {
			marks += ","
		}
		marks += "$" + strconvI(i+1)
	}
	_, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra."+table+"("+columns+",version,enabled,definition) VALUES("+marks+")", values...)
	return e
}
