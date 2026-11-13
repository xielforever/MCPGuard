package resources

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
)

// ImportCredentials registers public policy and key identity only. Key bytes
// are never stored beside packages or accepted as resource/MCP input.
func ImportCredentials(ctx context.Context, dsn string, raw []byte) error {
	var cfg v02.CredentialConfiguration
	if v02.Decode("CredentialConfiguration", raw, &cfg) != nil {
		return ErrInput
	}
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
	org := cfg.Issuer.OrgID
	var epoch int64
	if e = tx.QueryRow(ctx, "SELECT mcpguard.lock_auth_epoch_exclusive($1)", org).Scan(&epoch); e != nil {
		return e
	}
	i := cfg.Issuer
	if _, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.credential_key_registry VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", org, i.Issuer, i.KeyID, i.KeyFingerprint); e != nil {
		return e
	}
	var same bool
	if tx.QueryRow(ctx, "SELECT fingerprint=$4 FROM mcpguard_ra.credential_key_registry WHERE org_id=$1 AND issuer=$2 AND key_id=$3", org, i.Issuer, i.KeyID, i.KeyFingerprint).Scan(&same) != nil || !same {
		return ErrConflict
	}
	if _, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.credential_key_usage(fingerprint) VALUES($1) ON CONFLICT DO NOTHING", i.KeyFingerprint); e != nil {
		return e
	}
	encoded, _ := json.Marshal(i)
	if e = putCredentialConfig(ctx, tx, "credential_issuers", []any{org}, i.Version, i.Enabled, 0, encoded, &i); e != nil {
		return e
	}
	for _, p := range cfg.Policies {
		if p.OrgID != org {
			return ErrDenied
		}
		var exists bool
		if tx.QueryRow(ctx, "SELECT EXISTS(SELECT FROM mcpguard_ra.resource_versions r JOIN mcpguard_ra.profile_versions p ON (p.org_id,p.resource_id)=(r.org_id,r.resource_id) WHERE r.org_id=$1 AND r.resource_id=$2 AND r.version=$3 AND p.profile_id=$4 AND p.version=$5)", org, p.ResourceID, p.ResourceVersion, p.ProfileID, p.ProfileVersion).Scan(&exists) != nil || !exists {
			return ErrDenied
		}
		encoded, _ = json.Marshal(p)
		if e = putCredentialConfig(ctx, tx, "credential_policies", []any{org, p.ResourceID, p.ResourceVersion, p.ProfileID, p.ProfileVersion}, p.Version, p.Enabled, p.MaxTtlSeconds, encoded, nil); e != nil {
			return e
		}
	}
	return commit(ctx, tx)
}
func putCredentialConfig(ctx context.Context, tx pgx.Tx, table string, key []any, version string, enabled bool, ttl int, raw []byte, issuer *v02.CredentialIssuer) error {
	where, columns := "org_id=$1", "org_id"
	if table == "credential_policies" {
		where = "org_id=$1 AND resource_id=$2 AND resource_version=$3 AND profile_id=$4 AND profile_version=$5"
		columns = "org_id,resource_id,resource_version,profile_id,profile_version"
	} else if table != "credential_issuers" {
		return ErrInput
	}
	var old int64
	var same bool
	index := len(key) + 1
	args := append(append([]any{}, key...), raw)
	e := tx.QueryRow(ctx, "SELECT version,definition=$"+strconv.Itoa(index)+"::jsonb FROM mcpguard_ra."+table+" WHERE "+where, args...).Scan(&old, &same)
	if e == nil && same {
		return nil
	}
	if e == nil && number(version) <= old {
		return ErrConflict
	}
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	values := append(append([]any{}, key...), version, enabled, raw)
	fields := []string{"version", "enabled", "definition"}
	if issuer != nil {
		fields = append(fields, "issuer", "issuer_epoch", "key_id", "key_fingerprint")
		values = append(values, issuer.Issuer, issuer.IssuerEpoch, issuer.KeyID, issuer.KeyFingerprint)
	} else {
		fields = append(fields, "max_ttl_seconds")
		values = append(values, ttl)
	}
	if e == nil {
		sets := ""
		for j, f := range fields {
			if j > 0 {
				sets += ","
			}
			sets += f + "=$" + strconv.Itoa(index+j)
		}
		_, e = tx.Exec(ctx, "UPDATE mcpguard_ra."+table+" SET "+sets+" WHERE "+where, values...)
		return e
	}
	for _, f := range fields {
		columns += "," + f
	}
	marks := ""
	for j := range values {
		if j > 0 {
			marks += ","
		}
		marks += "$" + strconv.Itoa(j+1)
	}
	_, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra."+table+"("+columns+") VALUES("+marks+")", values...)
	return e
}
