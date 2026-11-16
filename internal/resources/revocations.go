package resources

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

// RevocationService performs operator-driven revocation at four scopes:
// credential (one file/package), grant, principal and resource. Every cascade
// runs in one transaction that first locks the authorization epoch, so a
// concurrent replica cannot issue or claim anything that the committed
// revocation would have refused.
// RevocationService is an administrative component: it runs on the privileged
// migration connection, never on a runtime role, and never exposes an MCP tool.
type RevocationService struct {
	Pool  *pgxpool.Pool
	Vault *metadata.Vault
}

// RevocationResult reports the blast radius actually changed by one revocation.
type RevocationResult struct {
	Kind        string `json:"kind"`
	TargetID    string `json:"targetId"`
	SubjectID   string `json:"subjectId,omitempty"`
	ResourceID  string `json:"resourceId,omitempty"`
	Grants      int    `json:"grants"`
	Credentials int    `json:"credentials"`
	Tickets     int    `json:"tickets"`
	Contexts    int    `json:"contexts"`
	Epoch       string `json:"epoch"`
}

func NewRevocationService(pool *pgxpool.Pool, vault *metadata.Vault) *RevocationService {
	return &RevocationService{Pool: pool, Vault: vault}
}

// Revoke terminalizes the target and its dependants. The actor is recorded in
// the audit row; it must be an existing principal of the same organization.
func (s *RevocationService) Revoke(ctx context.Context, actor metadata.Principal, kind, targetID, reason string) (RevocationResult, error) {
	out := RevocationResult{Kind: kind, TargetID: targetID}
	switch kind {
	case "CREDENTIAL", "GRANT", "PRINCIPAL", "RESOURCE":
	default:
		return out, ErrInput
	}
	if _, e := parseUUID(targetID); e != nil || len(reason) > 200 {
		return out, ErrInput
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return out, ErrUnavailable
	}
	defer rollback(tx)
	var epoch int64
	if e = tx.QueryRow(ctx, `SELECT mcpguard.lock_auth_epoch_exclusive($1)`, actor.OrgID).Scan(&epoch); e != nil {
		return out, ErrUnavailable
	}
	var actorOK bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM mcpguard.principals WHERE org_id=$1 AND id=$2)`, actor.OrgID, actor.ID).Scan(&actorOK); e != nil || !actorOK {
		return out, ErrDenied
	}
	if e = tx.QueryRow(ctx, `SELECT mcpguard_ra.advance_access_epoch($1)`, actor.OrgID).Scan(&epoch); e != nil {
		return out, ErrUnavailable
	}
	switch kind {
	case "CREDENTIAL":
		e = s.revokeCredential(ctx, tx, actor.OrgID, targetID, &out)
	case "GRANT":
		e = s.revokeGrant(ctx, tx, actor.OrgID, targetID, epoch, &out)
	case "PRINCIPAL":
		e = s.revokePrincipal(ctx, tx, actor.OrgID, targetID, epoch, &out)
	case "RESOURCE":
		e = s.revokeResource(ctx, tx, actor.OrgID, targetID, epoch, &out)
	}
	if e != nil {
		return out, e
	}
	out.Epoch = fmt.Sprintf("%d", epoch)
	var cipher []byte
	if reason != "" {
		cipher = s.Vault.Seal([]byte(reason), revocationAAD(actor.OrgID, kind, targetID))
	}
	var subject, resource any
	if out.SubjectID != "" {
		subject = out.SubjectID
	}
	if out.ResourceID != "" {
		resource = out.ResourceID
	}
	if _, e = tx.Exec(ctx, `INSERT INTO mcpguard_ra.revocation_audit(org_id,actor_id,target_kind,target_id,subject_id,resource_id,affected_grants,affected_credentials,affected_tickets,affected_contexts,reason_ciphertext) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		actor.OrgID, actor.ID, kind, targetID, subject, resource, out.Grants, out.Credentials, out.Tickets, out.Contexts, cipher); e != nil {
		return out, ErrUnavailable
	}
	if e = commit(ctx, tx); e != nil {
		return out, ErrUnavailable
	}
	return out, nil
}

// revokeCredential terminalizes one lease and its unclaimed download tickets.
func (s *RevocationService) revokeCredential(ctx context.Context, tx pgx.Tx, org, credentialID string, out *RevocationResult) error {
	var subject, resource string
	if e := tx.QueryRow(ctx, `SELECT subject_id::text,resource_id::text FROM mcpguard_ra.credential_leases WHERE org_id=$1 AND credential_id=$2`, org, credentialID).Scan(&subject, &resource); errors.Is(e, pgx.ErrNoRows) {
		return ErrDenied
	} else if e != nil {
		return ErrUnavailable
	}
	out.SubjectID, out.ResourceID = subject, resource
	if err := s.revokeLeases(ctx, tx, org, "credential", credentialID, out); err != nil {
		return err
	}
	return nil
}

// revokeGrant terminalizes every live lease issued under one grant.
func (s *RevocationService) revokeGrant(ctx context.Context, tx pgx.Tx, org, grantID string, epoch int64, out *RevocationResult) error {
	var subject, resource string
	if e := tx.QueryRow(ctx, `SELECT subject_id::text,resource_id::text FROM mcpguard_ra.access_grant_versions WHERE org_id=$1 AND grant_id=$2 ORDER BY version DESC LIMIT 1`, org, grantID).Scan(&subject, &resource); errors.Is(e, pgx.ErrNoRows) {
		return ErrDenied
	} else if e != nil {
		return ErrUnavailable
	}
	out.SubjectID, out.ResourceID = subject, resource
	if err := s.revokeLeases(ctx, tx, org, "grant", grantID, out); err != nil {
		return err
	}
	tag, e := tx.Exec(ctx, `UPDATE mcpguard_ra.access_grant_heads SET state='REVOKED',revision=revision+1,auth_epoch=$3 WHERE org_id=$1 AND grant_id=$2 AND state IN ('PROVISIONING','ACTIVE','PROVISIONING_UNKNOWN')`, org, grantID, epoch)
	if e != nil {
		return ErrUnavailable
	}
	out.Grants = int(tag.RowsAffected())
	return nil
}

// revokePrincipal disables the principal, its contexts, grants and leases.
func (s *RevocationService) revokePrincipal(ctx context.Context, tx pgx.Tx, org, principalID string, epoch int64, out *RevocationResult) error {
	var enabled bool
	if e := tx.QueryRow(ctx, `SELECT enabled FROM mcpguard.principals WHERE org_id=$1 AND id=$2`, org, principalID).Scan(&enabled); errors.Is(e, pgx.ErrNoRows) {
		return ErrDenied
	} else if e != nil {
		return ErrUnavailable
	}
	out.SubjectID = principalID
	tag, e := tx.Exec(ctx, `UPDATE mcpguard_ra.credential_contexts SET state='REVOKED',revision=revision+1 WHERE org_id=$1 AND subject_id=$2 AND state='ACTIVE'`, org, principalID)
	if e != nil {
		return ErrUnavailable
	}
	out.Contexts = int(tag.RowsAffected())
	if err := s.revokeLeases(ctx, tx, org, "subject", principalID, out); err != nil {
		return err
	}
	tag, e = tx.Exec(ctx, `UPDATE mcpguard_ra.access_grant_heads SET state='REVOKED',revision=revision+1,auth_epoch=$3 WHERE org_id=$1 AND grant_id IN (SELECT DISTINCT grant_id FROM mcpguard_ra.access_grant_versions WHERE org_id=$1 AND subject_id=$2) AND state IN ('PROVISIONING','ACTIVE','PROVISIONING_UNKNOWN')`, org, principalID, epoch)
	if e != nil {
		return ErrUnavailable
	}
	out.Grants = int(tag.RowsAffected())
	if _, e = tx.Exec(ctx, `UPDATE mcpguard.principals SET enabled=false WHERE org_id=$1 AND id=$2 AND enabled`, org, principalID); e != nil {
		return ErrUnavailable
	}
	return nil
}

// revokeResource disables the resource, its bindings, grants and leases.
func (s *RevocationService) revokeResource(ctx context.Context, tx pgx.Tx, org, resourceID string, epoch int64, out *RevocationResult) error {
	var enabled bool
	if e := tx.QueryRow(ctx, `SELECT enabled FROM mcpguard_ra.resource_heads WHERE org_id=$1 AND resource_id=$2`, org, resourceID).Scan(&enabled); errors.Is(e, pgx.ErrNoRows) {
		return ErrDenied
	} else if e != nil {
		return ErrUnavailable
	}
	out.ResourceID = resourceID
	if err := s.revokeLeases(ctx, tx, org, "resource", resourceID, out); err != nil {
		return err
	}
	tag, e := tx.Exec(ctx, `UPDATE mcpguard_ra.access_grant_heads SET state='REVOKED',revision=revision+1,auth_epoch=$3 WHERE org_id=$1 AND grant_id IN (SELECT DISTINCT grant_id FROM mcpguard_ra.access_grant_versions WHERE org_id=$1 AND resource_id=$2) AND state IN ('PROVISIONING','ACTIVE','PROVISIONING_UNKNOWN')`, org, resourceID, epoch)
	if e != nil {
		return ErrUnavailable
	}
	out.Grants = int(tag.RowsAffected())
	if _, e = tx.Exec(ctx, `UPDATE mcpguard_ra.identity_bindings SET enabled=false WHERE org_id=$1 AND resource_id=$2 AND enabled`, org, resourceID); e != nil {
		return ErrUnavailable
	}
	if _, e = tx.Exec(ctx, `UPDATE mcpguard_ra.resource_heads SET enabled=false,revision=revision+1 WHERE org_id=$1 AND resource_id=$2`, org, resourceID); e != nil {
		return ErrUnavailable
	}
	return nil
}

// revokeLeases revokes matching leases and cancels their unclaimed tickets.
func (s *RevocationService) revokeLeases(ctx context.Context, tx pgx.Tx, org, scope, value string, out *RevocationResult) error {
	lease := map[string]string{
		"credential": "credential_id=$2",
		"grant":      "grant_id=$2",
		"subject":    "subject_id=$2",
		"resource":   "resource_id=$2",
	}[scope]
	ticket := map[string]string{
		"credential": "credential_id=$2",
		"grant":      "credential_id IN (SELECT credential_id FROM mcpguard_ra.credential_leases WHERE org_id=$1 AND grant_id=$2)",
		"subject":    "subject_id=$2",
		"resource":   "credential_id IN (SELECT credential_id FROM mcpguard_ra.credential_leases WHERE org_id=$1 AND resource_id=$2)",
	}[scope]
	if lease == "" || ticket == "" {
		return ErrInput
	}
	tag, e := tx.Exec(ctx, `UPDATE mcpguard_ra.credential_leases SET state='REVOKED',revision=revision+1 WHERE org_id=$1 AND state='ACTIVE' AND `+lease, org, value)
	if e != nil {
		return ErrUnavailable
	}
	out.Credentials = int(tag.RowsAffected())
	out.Tickets += s.cancelTickets(ctx, tx, org, ticket, value)
	return nil
}

func (s *RevocationService) cancelTickets(ctx context.Context, tx pgx.Tx, org, predicate, value string) int {
	tag, e := tx.Exec(ctx, `UPDATE mcpguard_ra.download_tickets SET state='CANCELLED',revision=revision+1 WHERE org_id=$1 AND state='READY' AND `+predicate, org, value)
	if e != nil {
		return 0
	}
	return int(tag.RowsAffected())
}

func revocationAAD(org, kind, target string) string {
	return "revocation-reason/" + org + "/" + kind + "/" + target
}
