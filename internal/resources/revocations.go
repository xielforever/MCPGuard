package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
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
	Fence *RestoreFence
}

// RevocationResult reports the blast radius actually changed by one revocation.
type RevocationResult struct {
	Kind           string `json:"kind"`
	TargetID       string `json:"targetId"`
	SubjectID      string `json:"subjectId,omitempty"`
	ResourceID     string `json:"resourceId,omitempty"`
	Grants         int    `json:"grants"`
	Credentials    int    `json:"credentials"`
	Tickets        int    `json:"tickets"`
	Contexts       int    `json:"contexts"`
	WriteUnknown   int    `json:"writeUnknown"`
	ReadUnknown    int    `json:"readUnknown"`
	CleanupPending int    `json:"cleanupPending"`
	Epoch          string `json:"epoch"`
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
	if s.Fence != nil {
		if e = s.Fence.advanceAfterRevocation(ctx, tx, actor.OrgID, actor.ID); e != nil {
			return out, e
		}
	}
	if e = s.markInflight(ctx, tx, actor.OrgID, kind, targetID, &out); e != nil {
		return out, ErrUnavailable
	}
	if e = s.enqueueCleanup(ctx, tx, actor.OrgID, kind, targetID, &out); e != nil {
		return out, ErrUnavailable
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

// markInflight records uncertainty for plans that were already dispatched. The
// gateway never replays them; the completed native fact is not rewritten.
func (s *RevocationService) markInflight(ctx context.Context, tx pgx.Tx, org, kind, target string, out *RevocationResult) error {
	var wp, rp string
	switch kind {
	case "CREDENTIAL":
		wp, rp = "credential_id=$2", "credential_id=$2"
	case "GRANT":
		wp = "credential_id IN (SELECT credential_id FROM mcpguard_ra.credential_leases WHERE org_id=$1 AND grant_id=$2)"
		rp = wp
	case "PRINCIPAL":
		wp, rp = "subject_id=$2", "principal_id=$2"
	case "RESOURCE":
		wp, rp = "resource_id=$2", "resource_id=$2"
	default:
		return ErrInput
	}
	tag, e := tx.Exec(ctx, `WITH affected AS (
 UPDATE mcpguard_ra.write_requests SET state='OUTCOME_UNKNOWN' WHERE org_id=$1 AND state='EXECUTING' AND `+wp+`
 RETURNING org_id,subject_id,request_id)
 INSERT INTO mcpguard_ra.write_audit(org_id,subject_id,request_id,event_type)
 SELECT org_id,subject_id,request_id,'OUTCOME_UNKNOWN' FROM affected`, org, target)
	if e != nil {
		return e
	}
	out.WriteUnknown = int(tag.RowsAffected())
	tag, e = tx.Exec(ctx, `WITH affected AS (
 UPDATE mcpguard_ra.read_plans SET state='OUTCOME_UNKNOWN' WHERE org_id=$1 AND state='RUNNING' AND `+rp+`
 RETURNING org_id,principal_id,plan_id)
 INSERT INTO mcpguard_ra.read_audit(org_id,principal_id,plan_id,event_type)
 SELECT org_id,principal_id,plan_id,'OUTCOME_UNKNOWN' FROM affected`, org, target)
	if e != nil {
		return e
	}
	out.ReadUnknown = int(tag.RowsAffected())
	return nil
}

// enqueueCleanup records every native account affected by the revocation as a
// pending cleanup; the revocation itself never claims the native side effect is
// gone.
func (s *RevocationService) enqueueCleanup(ctx context.Context, tx pgx.Tx, org, kind, target string, out *RevocationResult) error {
	var filter string
	switch kind {
	case "CREDENTIAL":
		// A package is not the grant's native account. Other valid packages
		// under the same grant retain their authority.
		return nil
	case "GRANT":
		filter = "EXISTS (SELECT FROM mcpguard_ra.provisioning_jobs j WHERE j.org_id=$1 AND j.account_id=a.account_id AND j.grant_id=$2)"
	case "PRINCIPAL":
		filter = "a.subject_id=$2"
	case "RESOURCE":
		filter = "a.resource_id=$2"
	default:
		return ErrInput
	}
	tag, e := tx.Exec(ctx, `INSERT INTO mcpguard_ra.revocation_cleanup(org_id,account_id,resource_id,state)
 SELECT $1,a.account_id,a.resource_id,'PENDING' FROM mcpguard_ra.account_claims a
 WHERE a.org_id=$1 AND a.management_mode='PROVISIONED' AND `+filter+`
 ON CONFLICT (org_id,account_id) DO UPDATE SET state=CASE WHEN mcpguard_ra.revocation_cleanup.state='CLEANED' THEN 'CLEANED' ELSE 'PENDING' END,updated_at=clock_timestamp()`, org, target)
	if e != nil {
		return e
	}
	out.CleanupPending = int(tag.RowsAffected())
	return nil
}

// ProviderCleaner removes one native account through the provisioning provider,
// using the immutable plan recorded for the account's provisioning job. A
// missing job or unreadable plan is an error, so the row stays retryable and is
// never reported as cleaned.
func (s *RevocationService) ProviderCleaner(provider NativeRecoveryProvider) AccountCleaner {
	return func(ctx context.Context, org, accountID string) (CleanupReceipt, error) {
		var proof CleanupReceipt
		var jobID, hash, state string
		var cipher []byte
		if e := s.Pool.QueryRow(ctx, `SELECT j.job_id::text,j.plan_hash,j.plan_ciphertext,h.state
 FROM mcpguard_ra.provisioning_jobs j JOIN mcpguard_ra.access_grant_heads h USING(org_id,grant_id)
 WHERE j.org_id=$1 AND j.account_id=$2 ORDER BY j.created_at DESC LIMIT 1`, org, accountID).Scan(&jobID, &hash, &cipher, &state); e != nil {
			return proof, fmt.Errorf("no provisioning plan for account")
		}
		// Older deployments could enqueue a native account for a file-only
		// revocation. A retry must still respect the current grant.
		switch state {
		case "REVOKED", "EXPIRED", "PROVISIONING_FAILED":
		default:
			return proof, fmt.Errorf("native account still has a live grant")
		}
		plain, e := s.Vault.Open(cipher, "provision-plan/"+org+"/"+jobID)
		if e != nil || s.Vault.Hash(plain) != hash {
			return proof, fmt.Errorf("provisioning plan is unreadable")
		}
		var plan v02.ProvisioningPlan
		if json.Unmarshal(plain, &plan) != nil || plan.OrgID != org || plan.AccountID != accountID {
			return proof, fmt.Errorf("provisioning plan does not match the account")
		}
		receipt, e := provider.Cleanup(ctx, plan, hash)
		if e != nil {
			return proof, e
		}
		if receipt.AccountID != accountID || receipt.TargetID != plan.TargetID || receipt.PlanHash != hash {
			return proof, ErrDenied
		}
		proof = CleanupReceipt{Status: receipt.Status, TargetID: receipt.TargetID, AccountID: receipt.AccountID, RoleOID: receipt.RoleOID, ActiveSessions: receipt.ActiveSessions}
		if (receipt.Status == "CLEANED" || receipt.Status == "ABSENT_SEALED") && receipt.ActiveSessions != nil && *receipt.ActiveSessions == 0 {
			return proof, nil
		}
		return proof, fmt.Errorf("native cleanup status %s", receipt.Status)
	}
}

type CleanupReceipt struct {
	Status         string  `json:"status"`
	TargetID       string  `json:"targetId"`
	AccountID      string  `json:"accountId"`
	RoleOID        *string `json:"roleOid,omitempty"`
	ActiveSessions *int    `json:"activeSessions,omitempty"`
}

// AccountCleaner removes one native account through the provisioning provider.
type AccountCleaner func(ctx context.Context, org, accountID string) (CleanupReceipt, error)

// Cleanup retries pending/failed native account cleanups. A failed attempt is
// recorded and stays retryable; nothing is reported as cleaned without proof.
func (s *RevocationService) Cleanup(ctx context.Context, org string, cleaner AccountCleaner, limit int) (int, int, error) {
	if cleaner == nil || limit <= 0 || limit > 100 {
		return 0, 0, ErrInput
	}
	rows, e := s.Pool.Query(ctx, `SELECT account_id::text FROM mcpguard_ra.revocation_cleanup WHERE org_id=$1 AND (state<>'CLEANED' OR native_receipt IS NULL) ORDER BY updated_at LIMIT $2`, org, limit)
	if e != nil {
		return 0, 0, ErrUnavailable
	}
	var accounts []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			return 0, 0, ErrUnavailable
		}
		accounts = append(accounts, id)
	}
	rows.Close()
	if rows.Err() != nil {
		return 0, 0, ErrUnavailable
	}
	cleaned, failed := 0, 0
	for _, accountID := range accounts {
		var attempt int
		e = s.Pool.QueryRow(ctx, `UPDATE mcpguard_ra.revocation_cleanup SET attempts=attempts+1,updated_at=clock_timestamp()
 WHERE org_id=$1 AND account_id=$2 AND (state<>'CLEANED' OR native_receipt IS NULL) RETURNING attempts`, org, accountID).Scan(&attempt)
		if errors.Is(e, pgx.ErrNoRows) {
			continue
		}
		if e != nil {
			return cleaned, failed, ErrUnavailable
		}
		proof, err := cleaner(ctx, org, accountID)
		var raw []byte
		if proof.Status != "" {
			raw, _ = json.Marshal(proof)
		}
		state, lastError := "CLEANED", any(nil)
		if err != nil {
			state, lastError = "FAILED", truncateError(err)
		}
		tag, e := s.Pool.Exec(ctx, `UPDATE mcpguard_ra.revocation_cleanup SET state=$4,last_error=$5,native_receipt=$6,updated_at=clock_timestamp()
 WHERE org_id=$1 AND account_id=$2 AND attempts=$3`, org, accountID, attempt, state, lastError, raw)
		if e != nil {
			return cleaned, failed, ErrUnavailable
		}
		if tag.RowsAffected() == 1 {
			if err == nil {
				cleaned++
			} else {
				failed++
			}
		}
	}
	return cleaned, failed, nil
}

func truncateError(err error) string {
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

func revocationAAD(org, kind, target string) string {
	return "revocation-reason/" + org + "/" + kind + "/" + target
}
