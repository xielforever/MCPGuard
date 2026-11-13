package resources

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
)

func publicRecovery(ctx context.Context, tx pgx.Tx, org string, job *v02.ProvisioningJob) error {
	var present bool
	if tx.QueryRow(ctx, "SELECT to_regclass('mcpguard_ra.provision_recovery') IS NOT NULL").Scan(&present) != nil {
		return ErrUnavailable
	}
	if !present {
		return nil
	}
	var generation int64
	var owner, outcome *string
	var until, next time.Time
	r := &v02.ProvisioningRecovery{}
	e := tx.QueryRow(ctx, `SELECT generation,attempts,cleanup_attempts,operation,cleanup_state,last_outcome,owner_id::text,lease_until,next_attempt_at FROM mcpguard_ra.provision_recovery WHERE org_id=$1 AND job_id=$2`, org, job.JobID).Scan(&generation, &r.Attempts, &r.CleanupAttempts, &r.Operation, &r.CleanupState, &outcome, &owner, &until, &next)
	if errors.Is(e, pgx.ErrNoRows) || (e == nil && generation == 0) {
		return nil
	}
	if e != nil {
		return ErrUnavailable
	}
	r.Generation = strconv.FormatInt(generation, 10)
	r.LastOutcome = outcome
	if owner != nil {
		stamp := until.UTC().Format(time.RFC3339Nano)
		r.LeaseExpiresAt = &stamp
	} else if job.State == "OUTCOME_UNKNOWN" || job.State == "CLEANUP_PENDING" || job.State == "EXECUTING" {
		stamp := next.UTC().Format(time.RFC3339Nano)
		r.NextAttemptAt = &stamp
	}
	job.Recovery = r
	return nil
}
func (w *Provisioner) initialLease(ctx context.Context, tx pgx.Tx, work *provisionWork) error {
	work.LeaseOwner = uuid.NewString()
	work.Generation = 1
	_, e := tx.Exec(ctx, `INSERT INTO mcpguard_ra.provision_recovery(org_id,job_id,generation,owner_id,lease_until,operation) VALUES($1,$2,1,$3,clock_timestamp()+make_interval(secs=>$4),'DISPATCH')`, work.Plan.OrgID, work.Plan.JobID, work.LeaseOwner, v02.ProvisioningLeaseSeconds)
	if e != nil {
		return e
	}
	if e = w.leaseContext(ctx, tx, work); e != nil {
		return e
	}
	return w.attemptEvent(ctx, tx, work, "DISPATCH_CLAIMED")
}
func (w *Provisioner) leaseContext(ctx context.Context, tx pgx.Tx, work *provisionWork) error {
	if w.Access.Catalog.SchemaVersion < 4 {
		return nil
	}
	var current bool
	e := tx.QueryRow(ctx, `SELECT owner_id=$3::uuid AND generation=$4 AND lease_until>clock_timestamp() FROM mcpguard_ra.provision_recovery WHERE org_id=$1 AND job_id=$2 FOR UPDATE`, work.Plan.OrgID, work.Plan.JobID, work.LeaseOwner, work.Generation).Scan(&current)
	if e != nil || !current {
		return ErrConflict
	}
	_, e = tx.Exec(ctx, "SELECT set_config('mcpguard.provision_owner',$1,true),set_config('mcpguard.provision_generation',$2,true)", work.LeaseOwner, strconv.FormatInt(work.Generation, 10))
	return e
}
func (w *Provisioner) attemptEvent(ctx context.Context, tx pgx.Tx, work *provisionWork, event string) error {
	if w.Access.Catalog.SchemaVersion < 4 {
		return nil
	}
	_, e := tx.Exec(ctx, `INSERT INTO mcpguard_ra.provision_attempt_events(org_id,job_id,generation,event_type,operation) SELECT org_id,job_id,generation,$3,operation FROM mcpguard_ra.provision_recovery WHERE org_id=$1 AND job_id=$2`, work.Plan.OrgID, work.Plan.JobID, event)
	return e
}
func (w *Provisioner) saveReceipt(ctx context.Context, tx pgx.Tx, work *provisionWork) error {
	if work.Receipt == nil {
		return nil
	}
	raw, _ := json.Marshal(work.Receipt)
	_, e := tx.Exec(ctx, "UPDATE mcpguard_ra.provision_recovery SET native_receipt=$3 WHERE org_id=$1 AND job_id=$2", work.Plan.OrgID, work.Plan.JobID, raw)
	return e
}
func (w *Provisioner) releaseLease(ctx context.Context, tx pgx.Tx, work *provisionWork, code, cleanup string, delay time.Duration) error {
	if w.Access.Catalog.SchemaVersion < 4 {
		return nil
	}
	if e := w.attemptEvent(ctx, tx, work, code); e != nil {
		return e
	}
	_, e := tx.Exec(ctx, `UPDATE mcpguard_ra.provision_recovery SET owner_id=NULL,lease_until=clock_timestamp(),next_attempt_at=clock_timestamp()+make_interval(secs=>$3),last_outcome=$4,cleanup_state=$5 WHERE org_id=$1 AND job_id=$2`, work.Plan.OrgID, work.Plan.JobID, delay.Seconds(), code, cleanup)
	return e
}
func (w *Provisioner) loadFrozenWork(ctx context.Context, tx pgx.Tx, work *provisionWork, cipher []byte) error {
	original := work.Plan
	vault := w.Access.Catalog.Store.Vault
	raw, e := vault.Open(cipher, "provision-plan/"+original.OrgID+"/"+original.JobID)
	if e != nil || !hmac.Equal([]byte(vault.Hash(raw)), []byte(work.Hash)) {
		return ErrDenied
	}
	var plan v02.ProvisioningPlan
	if v02.Decode("ProvisioningPlan", raw, &plan) != nil || plan.OrgID != original.OrgID || plan.JobID != original.JobID || plan.GrantID != original.GrantID || plan.AccountID != original.AccountID || plan.GrantVersion != "1" {
		return ErrDenied
	}
	var targetRaw []byte
	e = tx.QueryRow(ctx, `SELECT definition FROM mcpguard_ra.provision_config_history WHERE org_id=$1 AND kind='provision_targets' AND version=$2 AND definition->>'targetId'=$3 AND definition->>'resourceId'=$4 AND definition->>'resourceVersion'=$5 AND definition->>'profileId'=$6 AND definition->>'profileVersion'=$7`, plan.OrgID, plan.TargetVersion, plan.TargetID, plan.ResourceID, plan.ResourceVersion, plan.ProfileID, plan.ProfileVersion).Scan(&targetRaw)
	var target v02.ProvisioningTarget
	if e != nil || v02.Decode("ProvisioningTarget", targetRaw, &target) != nil || target.TargetFingerprint != plan.TargetFingerprint || target.EndpointConfigRef != plan.EndpointConfigRef {
		return ErrDenied
	}
	ep, ok := w.Private.Endpoints[plan.EndpointConfigRef]
	if !ok || ep.Fingerprint() != plan.TargetFingerprint {
		return ErrDenied
	}
	var secretCipher []byte
	e = tx.QueryRow(ctx, "SELECT ciphertext FROM mcpguard_ra.native_secrets WHERE org_id=$1 AND account_id=$2 AND secret_ref=$3 AND version=$4", plan.OrgID, plan.AccountID, plan.SecretRef, plan.SecretVersion).Scan(&secretCipher)
	if e != nil {
		return ErrDenied
	}
	raw, e = vault.Open(secretCipher, "native-secret/"+plan.OrgID+"/"+plan.AccountID+"/"+plan.SecretVersion)
	var secret NativeSecret
	if e != nil || json.Unmarshal(raw, &secret) != nil || secret.Username != plan.NativeIdentity || secret.Version != plan.SecretVersion || len(secret.Password) < 32 {
		return ErrDenied
	}
	g, e := w.Access.readGrant(ctx, tx, plan.OrgID, plan.GrantID)
	if e != nil {
		return e
	}
	expiry, _, e := normalizedTime(plan.ExpiresAt)
	expected, _, _ := normalizedTime(g.ExpiresAt)
	if e != nil || !expiry.Equal(expected) || g.SubjectID != plan.SubjectID || g.ResourceID != plan.ResourceID || g.ResourceVersion != plan.ResourceVersion || g.ProfileID != plan.ProfileID || g.ProfileVersion != plan.ProfileVersion {
		return ErrDenied
	}
	work.Plan = plan
	work.Target = target
	work.Secret = secret
	return nil
}

type recoveryClaim struct {
	work      *provisionWork
	operation string
	attempts  int
	invalid   bool
}

func (w *Provisioner) claimRecovery(ctx context.Context, org, grant string) (*recoveryClaim, error) {
	tx, e := w.begin(ctx, org)
	if e != nil {
		return nil, e
	}
	defer rollback(tx)
	var job, account, hash, state string
	var cipher []byte
	var generation int64
	var until, next, now time.Time
	var attempts, cleanups int
	e = tx.QueryRow(ctx, `SELECT j.job_id::text,j.account_id::text,j.plan_hash,j.plan_ciphertext,j.state,l.generation,l.lease_until,l.next_attempt_at,l.attempts,l.cleanup_attempts,clock_timestamp() FROM mcpguard_ra.provisioning_jobs j JOIN mcpguard_ra.provision_recovery l USING(org_id,job_id) WHERE j.org_id=$1 AND j.grant_id=$2 FOR UPDATE OF l`, org, grant).Scan(&job, &account, &hash, &cipher, &state, &generation, &until, &next, &attempts, &cleanups, &now)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ErrDenied
	}
	if e != nil {
		return nil, ErrUnavailable
	}
	if state != "EXECUTING" && state != "OUTCOME_UNKNOWN" && state != "CLEANUP_PENDING" {
		return nil, nil
	}
	if until.After(now) || next.After(now) {
		return nil, nil
	}
	operation := "RECONCILE"
	count := attempts + 1
	if state == "CLEANUP_PENDING" {
		operation = "CLEANUP"
		count = cleanups + 1
	}
	limit := v02.MaxRecoveryAttempts
	if operation == "CLEANUP" {
		limit = v02.MaxCleanupAttempts
	}
	if count > limit {
		operation = "FINALIZE"
	}
	work := &provisionWork{Plan: v02.ProvisioningPlan{OrgID: org, GrantID: grant, JobID: job, AccountID: account}, Hash: hash, LeaseOwner: uuid.NewString(), Generation: generation + 1, Recovered: true}
	invalid := w.loadFrozenWork(ctx, tx, work, cipher) != nil
	_, e = tx.Exec(ctx, `UPDATE mcpguard_ra.provision_recovery SET generation=generation+1,owner_id=$3,lease_until=clock_timestamp()+make_interval(secs=>$4),operation=$5,attempts=attempts+CASE WHEN $5='RECONCILE' THEN 1 ELSE 0 END,cleanup_attempts=cleanup_attempts+CASE WHEN $5='CLEANUP' THEN 1 ELSE 0 END,native_receipt=NULL WHERE org_id=$1 AND job_id=$2`, org, job, work.LeaseOwner, v02.ProvisioningLeaseSeconds, operation)
	if e != nil {
		return nil, e
	}
	if e = w.leaseContext(ctx, tx, work); e != nil {
		return nil, e
	}
	if e = w.attemptEvent(ctx, tx, work, operation+"_CLAIMED"); e != nil {
		return nil, e
	}
	if e = provisionAudit(ctx, tx, org, job, operation+"_CLAIMED"); e != nil {
		return nil, e
	}
	if e = commit(ctx, tx); e != nil {
		return nil, e
	}
	return &recoveryClaim{work, operation, count, invalid}, nil
}
func (w *Provisioner) eligible(ctx context.Context, work *provisionWork) (bool, error) {
	tx, e := w.begin(ctx, work.Plan.OrgID)
	if e != nil {
		return false, e
	}
	defer rollback(tx)
	if e = w.leaseContext(ctx, tx, work); e != nil {
		return false, e
	}
	g, e := w.Access.readGrant(ctx, tx, work.Plan.OrgID, work.Plan.GrantID)
	if e != nil {
		return false, e
	}
	if g.State == "ACTIVE" {
		return false, ErrConflict
	}
	if g.State != "PROVISIONING" && g.State != "PROVISIONING_UNKNOWN" {
		return false, nil
	}
	r, e := w.Access.request(ctx, tx, work.Plan.OrgID, g.RequestID)
	if e != nil {
		return false, e
	}
	yes, e := w.Access.qualified(ctx, tx, r)
	if e != nil {
		return false, e
	}
	now, e := databaseTime(ctx, tx)
	if e != nil {
		return false, e
	}
	expiry, _, _ := normalizedTime(g.ExpiresAt)
	return yes && expiry.After(now), commit(ctx, tx)
}
func (w *Provisioner) recoveryGrantState(ctx context.Context, tx pgx.Tx, work *provisionWork, wanted string) error {
	g, e := w.Access.readGrant(ctx, tx, work.Plan.OrgID, work.Plan.GrantID)
	if e != nil {
		return e
	}
	if g.State != "PROVISIONING" && g.State != "PROVISIONING_UNKNOWN" {
		return nil
	}
	now, e := databaseTime(ctx, tx)
	if e != nil {
		return e
	}
	expiry, _, _ := normalizedTime(g.ExpiresAt)
	if !expiry.After(now) {
		wanted = "EXPIRED"
	} else {
		r, e := w.Access.request(ctx, tx, work.Plan.OrgID, g.RequestID)
		if e != nil {
			return e
		}
		yes, e := w.Access.qualified(ctx, tx, r)
		if e != nil {
			return e
		}
		if !yes {
			wanted = "REVOKED"
		}
	}
	if g.State == wanted {
		return nil
	}
	var epoch int64
	if e = tx.QueryRow(ctx, "SELECT mcpguard_ra.advance_access_epoch($1)", work.Plan.OrgID).Scan(&epoch); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "UPDATE mcpguard_ra.access_grant_heads SET state=$3,revision=revision+1,auth_epoch=$4 WHERE org_id=$1 AND grant_id=$2", work.Plan.OrgID, work.Plan.GrantID, wanted, epoch)
	return e
}
func (w *Provisioner) recoveryResult(ctx context.Context, work *provisionWork, state, grant, code, cleanup string, delay time.Duration) error {
	tx, e := w.begin(ctx, work.Plan.OrgID)
	if e != nil {
		return e
	}
	defer rollback(tx)
	if e = w.leaseContext(ctx, tx, work); e != nil {
		return e
	}
	if e = w.saveReceipt(ctx, tx, work); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "UPDATE mcpguard_ra.provisioning_jobs SET state=$3,failure_code=$4 WHERE org_id=$1 AND job_id=$2", work.Plan.OrgID, work.Plan.JobID, state, code); e != nil {
		return e
	}
	if e = w.recoveryGrantState(ctx, tx, work, grant); e != nil {
		return e
	}
	if e = provisionAudit(ctx, tx, work.Plan.OrgID, work.Plan.JobID, code); e != nil {
		return e
	}
	if e = w.releaseLease(ctx, tx, work, code, cleanup, delay); e != nil {
		return e
	}
	return commit(ctx, tx)
}
func recoveryDelay(attempt int) time.Duration {
	seconds := v02.RecoveryBackoffSeconds
	for i := 1; i < attempt && seconds < v02.MaxRecoveryBackoffSeconds; i++ {
		seconds *= 2
	}
	if seconds > v02.MaxRecoveryBackoffSeconds {
		seconds = v02.MaxRecoveryBackoffSeconds
	}
	return time.Duration(seconds) * time.Second
}
func (w *Provisioner) recoveryRetry(ctx context.Context, claim *recoveryClaim) error {
	state, grant, cleanup, code := "OUTCOME_UNKNOWN", "PROVISIONING_UNKNOWN", "NOT_REQUIRED", "NATIVE_RECONCILIATION_UNAVAILABLE"
	if claim.operation == "CLEANUP" {
		state, grant, cleanup, code = "CLEANUP_PENDING", "PROVISIONING_FAILED", "PENDING", "NATIVE_CLEANUP_UNAVAILABLE"
	}
	if claim.attempts >= v02.MaxRecoveryAttempts {
		state, grant, cleanup, code = "MANUAL_REQUIRED", "PROVISIONING_FAILED", "MANUAL_REQUIRED", "RECOVERY_ATTEMPTS_EXHAUSTED"
	}
	return w.recoveryResult(ctx, claim.work, state, grant, code, cleanup, recoveryDelay(claim.attempts))
}
func (w *Provisioner) currentJob(ctx context.Context, org, grant string) (v02.ProvisioningJob, error) {
	tx, e := w.begin(ctx, org)
	if e != nil {
		return v02.ProvisioningJob{}, e
	}
	defer rollback(tx)
	job, e := safeJob(ctx, tx, org, grant)
	if e != nil || job == nil {
		return v02.ProvisioningJob{}, ErrDenied
	}
	return *job, commit(ctx, tx)
}

// Recover never invokes Create. A new lease authorizes inspection or owned
// cleanup only; metadata completion is fenced by its nonce and generation.
func (w *Provisioner) Recover(ctx context.Context, org, grant string) (v02.ProvisioningJob, error) {
	if w.Access.Catalog.SchemaVersion < 4 {
		return v02.ProvisioningJob{}, ErrUnavailable
	}
	if _, e := uuid.Parse(org); e != nil || !valid("GrantIDInput", v02.GrantIDInput{GrantID: grant}) {
		return v02.ProvisioningJob{}, ErrInput
	}
	claim, e := w.claimRecovery(ctx, org, grant)
	if e != nil {
		return v02.ProvisioningJob{}, e
	}
	if claim == nil {
		return w.currentJob(ctx, org, grant)
	}
	work := claim.work
	call, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	finish, finishCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer finishCancel()
	manual := func(code string) error {
		return w.recoveryResult(finish, work, "MANUAL_REQUIRED", "PROVISIONING_FAILED", code, "MANUAL_REQUIRED", 0)
	}
	if claim.operation == "FINALIZE" {
		e = manual("RECOVERY_ATTEMPTS_EXHAUSTED")
	} else if claim.invalid {
		e = manual("FROZEN_PLAN_OR_TARGET_INVALID")
	} else if work.Plan.ManagementMode == "PREEXISTING" {
		var yes bool
		yes, e = w.eligible(call, work)
		if e == nil && !yes {
			e = w.recoveryResult(finish, work, "FAILED", "PROVISIONING_FAILED", "PREEXISTING_AUTHORIZATION_ENDED", "NOT_REQUIRED", 0)
		} else if e == nil {
			var observation v02.NativeObservation
			observation, e = w.Provider.Verify(call, work.Plan, work.Secret)
			if e == nil {
				oid := observation.RoleOid
				work.Receipt = &NativeReceipt{Status: "OWNED", TargetID: work.Plan.TargetID, AccountID: work.Plan.AccountID, PlanHash: work.Hash, RoleOID: &oid}
				e = w.finish(call, work, observation)
			}
			if errors.Is(e, ErrDenied) {
				e = w.recoveryResult(finish, work, "FAILED", "PROVISIONING_FAILED", "PREEXISTING_VERIFICATION_FAILED", "NOT_REQUIRED", 0)
			} else if e != nil {
				e = w.recoveryRetry(finish, claim)
			}
		} else {
			e = w.recoveryRetry(finish, claim)
		}
	} else {
		provider, ok := w.Provider.(NativeRecoveryProvider)
		if !ok {
			e = manual("RECOVERY_PROVIDER_UNAVAILABLE")
		} else {
			var receipt NativeReceipt
			if claim.operation == "CLEANUP" {
				receipt, e = provider.Cleanup(call, work.Plan, work.Hash)
			} else {
				receipt, e = provider.Reconcile(call, work.Plan, work.Hash)
			}
			if errors.Is(e, ErrDenied) {
				e = manual("NATIVE_TARGET_OR_IDENTITY_CONFLICT")
			} else if e != nil {
				e = w.recoveryRetry(finish, claim)
			} else {
				if receipt.TargetID != work.Plan.TargetID || receipt.AccountID != work.Plan.AccountID || receipt.PlanHash != work.Hash {
					if e = manual("NATIVE_RECEIPT_BINDING_INVALID"); e != nil {
						return v02.ProvisioningJob{}, e
					}
					return w.currentJob(finish, org, grant)
				}
				work.Receipt = &receipt
				switch {
				case receipt.Status == "CONFLICT":
					e = manual("NATIVE_OWNERSHIP_CONFLICT")
				case claim.operation == "CLEANUP" && (receipt.Status == "CLEANED" || receipt.Status == "ABSENT_SEALED"):
					e = w.recoveryResult(finish, work, "CLEANED", "PROVISIONING_FAILED", "OWNED_TEMPLATE_PRIVILEGES_REMOVED", "SUCCEEDED", 0)
				case claim.operation == "CLEANUP":
					e = manual("CLEANUP_RECEIPT_INVALID")
				case receipt.Status == "ABSENT_SEALED":
					e = w.recoveryResult(finish, work, "FAILED", "PROVISIONING_FAILED", "NATIVE_ABSENT_SEALED", "NOT_REQUIRED", 0)
				case receipt.Status == "DISABLED":
					e = w.recoveryResult(finish, work, "CLEANUP_PENDING", "PROVISIONING_FAILED", "OWNED_NATIVE_ACCOUNT_DISABLED", "PENDING", 0)
				case receipt.Status == "OWNED":
					var yes bool
					yes, e = w.eligible(call, work)
					if e == nil && !yes {
						e = w.recoveryResult(finish, work, "CLEANUP_PENDING", "PROVISIONING_FAILED", "LATE_NATIVE_RESULT_REQUIRES_CLEANUP", "PENDING", 0)
					} else if e == nil {
						var observation v02.NativeObservation
						observation, e = w.Provider.Verify(call, work.Plan, work.Secret)
						if e == nil {
							e = w.finish(call, work, observation)
						}
						if errors.Is(e, ErrDenied) || errors.Is(e, ErrConflict) {
							e = w.recoveryResult(finish, work, "CLEANUP_PENDING", "PROVISIONING_FAILED", "NATIVE_VERIFICATION_OR_QUALIFICATION_CHANGED", "PENDING", 0)
						} else if e != nil {
							e = w.recoveryRetry(finish, claim)
						}
					} else {
						e = w.recoveryRetry(finish, claim)
					}
				default:
					e = manual("NATIVE_RECEIPT_INVALID")
				}
			}
		}
	}
	if e != nil {
		return v02.ProvisioningJob{}, e
	}
	return w.currentJob(finish, org, grant)
}

// RecoverOnce scans due work without holding a queue row while acquiring the
// organization lock. ClaimRecovery then serializes and fences each candidate.
func (w *Provisioner) RecoverOnce(ctx context.Context) ([]v02.ProvisioningJob, error) {
	if w.Access.Catalog.SchemaVersion < 4 {
		return nil, ErrUnavailable
	}
	rows, e := w.Access.Catalog.Store.Pool.Query(ctx, `SELECT j.org_id::text,j.grant_id::text FROM mcpguard_ra.provisioning_jobs j JOIN mcpguard_ra.provision_recovery l USING(org_id,job_id) WHERE j.state IN ('EXECUTING','OUTCOME_UNKNOWN','CLEANUP_PENDING') AND l.lease_until<=clock_timestamp() AND l.next_attempt_at<=clock_timestamp() ORDER BY l.next_attempt_at,j.org_id,j.job_id LIMIT $1`, v02.MaxRecoveryBatch)
	if e != nil {
		return nil, ErrUnavailable
	}
	type candidate struct{ org, grant string }
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if rows.Scan(&c.org, &c.grant) != nil {
			rows.Close()
			return nil, ErrUnavailable
		}
		candidates = append(candidates, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out := []v02.ProvisioningJob{}
	for _, c := range candidates {
		if ctx.Err() != nil {
			return out, ErrUnavailable
		}
		job, e := w.Recover(ctx, c.org, c.grant)
		if errors.Is(e, ErrConflict) {
			continue
		}
		if e != nil {
			return out, e
		}
		out = append(out, job)
	}
	return out, nil
}
