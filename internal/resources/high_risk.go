package resources

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

const highRiskSchemaVersion = 14
const HighRiskToolName = "pg.plan_high_risk"

type HighRiskPolicy struct {
	ResourceID string
	ReviewerID string
	Channel    string
	CodeTTL    time.Duration
	Notifier   *HighRiskNotifier
}

func (s *WriteService) EnableFixtureHighRisk(policy HighRiskPolicy) error {
	resource, resourceErr := parseUUID(policy.ResourceID)
	reviewer, reviewerErr := parseUUID(policy.ReviewerID)
	if s.Catalog.SchemaVersion < highRiskSchemaVersion || resourceErr != nil || resource != policy.ResourceID ||
		reviewerErr != nil || reviewer != policy.ReviewerID || policy.Notifier == nil ||
		(policy.Channel != HighRiskWeCom && policy.Channel != HighRiskFeishu) ||
		policy.CodeTTL < time.Second || policy.CodeTTL > 15*time.Minute {
		return ErrInput
	}
	s.HighRisk = &policy
	return nil
}

type HighRiskPlanInput struct {
	ResourceID      string            `json:"resourceId"`
	CredentialID    string            `json:"credentialId"`
	Operation       HighRiskOperation `json:"operation"`
	Target          string            `json:"target"`
	ExpectedVersion int64             `json:"expectedVersion"`
}

func (s *WriteService) PlanHighRisk(ctx context.Context, p metadata.Principal, in HighRiskPlanInput, key string) (WriteStatus, error) {
	if s.HighRisk == nil || in.ResourceID != s.HighRisk.ResourceID || !ValidHighRiskTarget(in.Operation, in.Target, in.ExpectedVersion) {
		return WriteStatus{}, ErrDenied
	}
	if id, err := parseUUID(in.CredentialID); err != nil || id != in.CredentialID {
		return WriteStatus{}, ErrInput
	}
	return s.plan(ctx, p, v02.UpdateNoteInput{
		ResourceID: in.ResourceID, CredentialID: in.CredentialID,
		ItemID: in.Target, ExpectedVersion: v02.Version(strconv.FormatInt(in.ExpectedVersion, 10)),
	}, key, &in)
}

func (s *WriteService) recordHighRiskPlan(ctx context.Context, tx pgx.Tx, p metadata.Principal, actionID string, in HighRiskPlanInput, hash string) error {
	policy := s.HighRisk
	if policy == nil || policy.Notifier == nil || p.ID == policy.ReviewerID {
		return ErrDenied
	}
	var reviewer bool
	var expires time.Time
	err := tx.QueryRow(ctx, `
 SELECT EXISTS (
   SELECT FROM mcpguard_ra.access_reviewers a
   JOIN mcpguard.principals r ON r.org_id=a.org_id AND r.id=a.principal_id
   WHERE a.org_id=$1 AND a.resource_id=$2 AND a.principal_id=$3 AND a.can_approve
     AND r.enabled AND r.expires_at>clock_timestamp()),
   expires_at FROM mcpguard_ra.credential_leases WHERE org_id=$1 AND credential_id=$4`,
		p.OrgID, in.ResourceID, policy.ReviewerID, in.CredentialID).Scan(&reviewer, &expires)
	if err != nil {
		return ErrUnavailable
	}
	if !reviewer {
		return ErrDenied
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mcpguard_ra.high_risk_writes(org_id,action_id,reviewer_id,channel) VALUES($1,$2,$3,$4)`,
		p.OrgID, actionID, policy.ReviewerID, policy.Channel); err != nil {
		return ErrUnavailable
	}
	preview, _ := HighRiskPreview(in.Operation, in.Target, in.ExpectedVersion)
	if err := highRiskAudit(ctx, tx, p, actionID, "PLANNED", map[string]any{
		"operation": in.Operation, "plan": preview, "planHash": hash,
		"resourceId": in.ResourceID, "credentialId": in.CredentialID,
	}); err != nil {
		return err
	}
	return policy.Notifier.Enqueue(ctx, tx, p.OrgID, actionID, policy.ReviewerID, policy.Channel, HighRiskApprovalRequired, &expires)
}

func (s *WriteService) recordHighRiskDecision(ctx context.Context, tx pgx.Tx, p metadata.Principal, actionID, subject, resource, hash string, approved bool) error {
	if s.HighRisk == nil {
		return nil
	}
	var reviewer, channel string
	var now, leaseExpires time.Time
	err := tx.QueryRow(ctx, `
 SELECT h.reviewer_id::text,h.channel,clock_timestamp(),l.expires_at
 FROM mcpguard_ra.high_risk_writes h JOIN mcpguard_ra.write_requests w
  ON w.org_id=h.org_id AND w.request_id=h.action_id
 JOIN mcpguard_ra.credential_leases l ON l.org_id=w.org_id AND l.credential_id=w.credential_id
 WHERE h.org_id=$1 AND h.action_id=$2 FOR UPDATE OF h`, p.OrgID, actionID).Scan(&reviewer, &channel, &now, &leaseExpires)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return ErrUnavailable
	}
	policy := s.HighRisk
	if policy == nil || policy.Notifier == nil || p.ID != reviewer || resource != policy.ResourceID {
		return ErrDenied
	}
	detail := map[string]any{"planHash": hash}
	if !approved {
		return highRiskAudit(ctx, tx, p, actionID, "REJECTED", detail)
	}
	var notified bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM mcpguard_ra.high_risk_notifications
 WHERE org_id=$1 AND action_id=$2 AND event_kind='APPROVAL_REQUIRED' AND recipient_id=$3 AND state='ACKNOWLEDGED')`,
		p.OrgID, actionID, reviewer).Scan(&notified); err != nil {
		return ErrUnavailable
	}
	if !notified {
		return ErrConflict
	}
	expiry := now.Add(policy.CodeTTL)
	if leaseExpires.Before(expiry) {
		expiry = leaseExpires
	}
	if !now.Before(expiry) {
		return ErrDenied
	}
	record, err := issueHighRiskCode(s.Catalog.Store.Vault, highRiskCodeBinding{p.OrgID, subject, actionID, hash}, now, expiry)
	if err != nil {
		return ErrUnavailable
	}
	if _, err = tx.Exec(ctx, `UPDATE mcpguard_ra.high_risk_writes SET code_id=$3,code_digest=$4,code_ciphertext=$5,expires_at=$6
 WHERE org_id=$1 AND action_id=$2`, p.OrgID, actionID, record.ID, record.Digest, record.Ciphertext, record.ExpiresAt); err != nil {
		return ErrUnavailable
	}
	if err := highRiskAudit(ctx, tx, p, actionID, "APPROVED", detail); err != nil {
		return err
	}
	if err := highRiskAudit(ctx, tx, p, actionID, "CODE_ISSUED", map[string]any{
		"codeId": record.ID, "expiresAt": record.ExpiresAt, "planHash": hash,
	}); err != nil {
		return err
	}
	return policy.Notifier.Enqueue(ctx, tx, p.OrgID, actionID, subject, channel, HighRiskApproved, &record.ExpiresAt)
}

type highRiskAction struct {
	binding    highRiskCodeBinding
	record     highRiskCodeRecord
	state      string
	resource   string
	credential string
	plan       []byte
	now        time.Time
	delivered  bool
}

func (s *WriteService) IsHighRisk(ctx context.Context, p metadata.Principal, id string) (bool, error) {
	if s.HighRisk == nil {
		return false, nil
	}
	var found bool
	if err := s.Catalog.Store.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT FROM mcpguard_ra.high_risk_writes WHERE org_id=$1 AND action_id=$2)`, p.OrgID, id).Scan(&found); err != nil {
		return false, ErrUnavailable
	}
	return found, nil
}

func loadHighRiskAction(ctx context.Context, tx pgx.Tx, p metadata.Principal, id string) (highRiskAction, error) {
	a := highRiskAction{binding: highRiskCodeBinding{OrgID: p.OrgID, ActionID: id}}
	var expires *time.Time
	err := tx.QueryRow(ctx, `
 SELECT w.subject_id::text,w.plan_hash,w.state,w.resource_id::text,w.credential_id::text,w.plan_ciphertext,
        coalesce(h.code_id::text,''),coalesce(h.code_digest,''),h.code_ciphertext,h.expires_at,clock_timestamp(),
        EXISTS (SELECT FROM mcpguard_ra.high_risk_notifications n WHERE n.org_id=h.org_id AND n.action_id=h.action_id
         AND n.event_kind='APPROVED' AND n.recipient_id=w.subject_id AND n.state='ACKNOWLEDGED')
 FROM mcpguard_ra.write_requests w JOIN mcpguard_ra.high_risk_writes h ON h.org_id=w.org_id AND h.action_id=w.request_id
 WHERE w.org_id=$1 AND w.request_id=$2 FOR UPDATE OF w,h`, p.OrgID, id).Scan(
		&a.binding.SubjectID, &a.binding.PlanHash, &a.state, &a.resource, &a.credential, &a.plan,
		&a.record.ID, &a.record.Digest, &a.record.Ciphertext, &expires, &a.now, &a.delivered)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrDenied
	}
	if err != nil {
		return a, ErrUnavailable
	}
	if expires != nil {
		a.record.ExpiresAt = *expires
	}
	return a, nil
}

func (s *WriteService) ClaimHighRiskCode(ctx context.Context, p metadata.Principal, id string) (string, time.Time, error) {
	if s.HighRisk == nil {
		return "", time.Time{}, ErrDenied
	}
	if _, err := parseUUID(id); err != nil {
		return "", time.Time{}, ErrInput
	}
	tx, epoch, err := s.Catalog.begin(ctx, p)
	if err != nil {
		return "", time.Time{}, err
	}
	defer rollback(tx)
	a, err := loadHighRiskAction(ctx, tx, p, id)
	if err != nil {
		return "", time.Time{}, err
	}
	if a.binding.SubjectID != p.ID || a.state != "APPROVED" || !a.delivered {
		return "", time.Time{}, ErrDenied
	}
	if _, err := s.checkHighRiskPlan(ctx, tx, p, epoch, a); err != nil {
		return "", time.Time{}, err
	}
	var now time.Time
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return "", time.Time{}, ErrUnavailable
	}
	code, err := claimHighRiskCode(s.Catalog.Store.Vault, a.binding, a.record, now)
	if err != nil {
		return "", time.Time{}, ErrDenied
	}
	if err := highRiskAudit(ctx, tx, p, id, "CODE_CLAIMED", map[string]any{"codeId": a.record.ID}); err != nil {
		return "", time.Time{}, err
	}
	if err := commit(ctx, tx); err != nil {
		return "", time.Time{}, ErrUnavailable
	}
	return code, a.record.ExpiresAt, nil
}

func (s *WriteService) checkHighRiskPlan(ctx context.Context, tx pgx.Tx, p metadata.Principal, epoch int64, a highRiskAction) (frozenWritePlan, error) {
	var frozen frozenWritePlan
	vault := s.Catalog.Store.Vault
	plan, err := vault.Open(a.plan, writeAAD(p, a.binding.ActionID))
	if err != nil || !hmac.Equal([]byte(vault.Hash(plan)), []byte(a.binding.PlanHash)) || json.Unmarshal(plan, &frozen) != nil {
		return frozen, ErrDenied
	}
	in := frozen.HighRisk
	if in == nil || frozen.Principal != p.ID || in.ResourceID != a.resource || in.ResourceID != s.HighRisk.ResourceID ||
		in.CredentialID != a.credential || !ValidHighRiskTarget(in.Operation, in.Target, in.ExpectedVersion) {
		return frozen, ErrDenied
	}
	if s.Fence != nil {
		if err := s.Fence.Check(ctx, p.OrgID, frozen.Snapshot.Resource.EndpointConfigRef); err != nil {
			return frozen, err
		}
	}
	row, err := s.Credentials.row(ctx, tx, p, a.credential)
	if err != nil || s.Credentials.refresh(ctx, tx, p, row) != nil || row.state != "ACTIVE" ||
		row.revision != frozen.Revision || row.ref.ResourceID != a.resource {
		return frozen, ErrDenied
	}
	snap, err := s.Catalog.snapshotBound(ctx, tx, p, a.resource, row.ref.ProfileID, row.ref.ProfileVersion, WriteOperation, WriteToolName, epoch)
	original := frozen.Snapshot
	if err != nil || snap.Epoch != original.Epoch || snap.Resource.Version != original.Resource.Version ||
		snap.Binding.BindingID != original.Binding.BindingID || snap.Binding.BindingVersion != original.Binding.BindingVersion {
		return frozen, ErrDenied
	}
	return frozen, nil
}

func (s *WriteService) ResumeHighRisk(ctx context.Context, p metadata.Principal, id, code string, contexts *ContextService, proof ContextProof) (WriteStatus, error) {
	if s.HighRisk == nil {
		return WriteStatus{}, ErrDenied
	}
	frozen, err := s.claimHighRiskDispatch(ctx, p, id, code, contexts, proof)
	if err != nil {
		return WriteStatus{}, err
	}
	in := frozen.HighRisk
	if _, err := contexts.CheckActivation(ctx, p, proof, in.ResourceID, in.CredentialID); err != nil {
		if finishErr := s.finishWrite(p, id, "FAILED", nil, map[string]any{"reason": "ACTIVATION_CHANGED_BEFORE_NATIVE_CALL"}); finishErr != nil {
			return WriteStatus{}, finishErr
		}
		return WriteStatus{}, err
	}
	call, stop := watchDispatch(ctx, s.Catalog.Store.Pool, p.OrgID, id, true)
	native, nativeErr := s.nativeHighRisk(call, p, frozen.Snapshot, in.Operation, in.Target, in.ExpectedVersion)
	stop()
	state := "FAILED"
	detail := map[string]any{"reason": "NATIVE_FAILED"}
	var raw []byte
	switch {
	case errors.Is(nativeErr, errCommitOutcomeUnknown):
		state = "OUTCOME_UNKNOWN"
		detail["reason"] = "NATIVE_COMMIT_UNCONFIRMED"
	case nativeErr == nil:
		if native.Outcome == "UPDATED" || native.Outcome == "INDEX_CREATED" || native.Outcome == "DELETED" {
			state = "SUCCEEDED"
		}
		detail = map[string]any{"native": native}
		raw, _ = json.Marshal(WriteResult{Outcome: native.Outcome, Native: &native})
	}
	if err := s.finishWrite(p, id, state, raw, detail); err != nil && !errors.Is(err, ErrConflict) {
		return WriteStatus{}, err
	}
	return s.Status(ctx, p, id)
}

func (s *WriteService) claimHighRiskDispatch(ctx context.Context, p metadata.Principal, id, code string, contexts *ContextService, proof ContextProof) (frozenWritePlan, error) {
	var frozen frozenWritePlan
	// Context checking has its own transaction. Run it before taking the action
	// lock, and recheck the activation after the durable claim before native I/O.
	var activationErr error
	if contexts != nil && code != "" {
		row, err := s.row(ctx, p, id)
		if err != nil || row.SubjectID != p.ID {
			activationErr = ErrDenied
		} else {
			_, activationErr = contexts.CheckActivation(ctx, p, proof, row.ResourceID, row.CredentialID)
		}
	}
	tx, epoch, err := s.Catalog.begin(ctx, p)
	if err != nil {
		return frozen, err
	}
	defer rollback(tx)
	a, err := loadHighRiskAction(ctx, tx, p, id)
	if err != nil {
		return frozen, err
	}
	deny := func(reason string) (frozenWritePlan, error) {
		if err := highRiskAudit(ctx, tx, p, id, "CODE_DENIED", map[string]any{"reason": reason}); err != nil {
			return frozenWritePlan{}, err
		}
		if err := commit(ctx, tx); err != nil {
			return frozenWritePlan{}, ErrUnavailable
		}
		return frozenWritePlan{}, ErrDenied
	}
	switch {
	case a.binding.SubjectID != p.ID:
		return deny("WRONG_APPLICANT")
	case a.state != "APPROVED":
		if a.record.ID != "" {
			return deny("ALREADY_CONSUMED")
		}
		return deny("NOT_APPROVED")
	case code == "":
		return deny("MISSING_CODE")
	case !a.now.Before(a.record.ExpiresAt):
		return deny("CODE_EXPIRED")
	case !a.delivered:
		return deny("NOTICE_NOT_ACKNOWLEDGED")
	case contexts == nil || activationErr != nil:
		return deny("ACTIVATION_DENIED")
	}
	if err := verifyHighRiskCode(s.Catalog.Store.Vault, a.binding, a.record, a.now, code); err != nil {
		return deny("INVALID_CODE")
	}
	frozen, err = s.checkHighRiskPlan(ctx, tx, p, epoch, a)
	if err != nil {
		return deny("PLAN_OR_AUTHORIZATION_CHANGED")
	}
	tag, err := tx.Exec(ctx, `UPDATE mcpguard_ra.high_risk_writes SET code_digest=NULL,code_ciphertext=NULL,
 consumed_at=clock_timestamp(),dispatch_at=clock_timestamp() WHERE org_id=$1 AND action_id=$2 AND consumed_at IS NULL
 AND expires_at>clock_timestamp()`, p.OrgID, id)
	if err != nil {
		return frozenWritePlan{}, ErrUnavailable
	}
	if tag.RowsAffected() != 1 {
		return deny("CODE_EXPIRED_OR_CONSUMED")
	}
	if _, err = tx.Exec(ctx, `UPDATE mcpguard_ra.write_requests SET state='EXECUTING' WHERE org_id=$1 AND request_id=$2 AND state='APPROVED'`, p.OrgID, id); err != nil {
		return frozenWritePlan{}, ErrUnavailable
	}
	if err := highRiskAudit(ctx, tx, p, id, "CODE_CONSUMED", map[string]any{"codeId": a.record.ID}); err != nil {
		return frozenWritePlan{}, err
	}
	if err := highRiskAudit(ctx, tx, p, id, "DISPATCH_INTENT", map[string]any{"planHash": a.binding.PlanHash}); err != nil {
		return frozenWritePlan{}, err
	}
	if err := writeAudit(ctx, tx, p, id, "DISPATCH_INTENT"); err != nil {
		return frozenWritePlan{}, ErrUnavailable
	}
	if err := commit(ctx, tx); err != nil {
		return frozenWritePlan{}, ErrUnavailable
	}
	return frozen, nil
}

type HighRiskAuditEvent struct {
	ActorID string          `json:"actorId"`
	Kind    string          `json:"kind"`
	Detail  json.RawMessage `json:"detail"`
	At      time.Time       `json:"at"`
}

type HighRiskEvidence struct {
	ActionID      string                        `json:"actionId"`
	State         string                        `json:"state"`
	Events        []HighRiskAuditEvent          `json:"events"`
	Notifications []HighRiskNotificationReceipt `json:"notifications"`
}

func (s *WriteService) HighRiskEvidence(ctx context.Context, p metadata.Principal, id string) (HighRiskEvidence, error) {
	var evidence HighRiskEvidence
	if s.HighRisk == nil {
		return evidence, ErrDenied
	}
	if _, err := parseUUID(id); err != nil {
		return evidence, ErrInput
	}
	status, err := s.Status(ctx, p, id)
	if err != nil {
		return evidence, err
	}
	if status.HighRisk == nil {
		return evidence, ErrDenied
	}
	rows, err := s.Catalog.Store.Pool.Query(ctx, `
 SELECT actor_id::text,event_kind,detail,created_at FROM (
   SELECT a.seq,a.actor_id,a.event_kind,
     CASE WHEN w.subject_id=$3 AND w.auth_epoch=h.auth_epoch THEN a.detail ELSE a.detail-'native' END AS detail,a.created_at
   FROM mcpguard_ra.high_risk_audit a
   JOIN mcpguard_ra.write_requests w ON w.org_id=a.org_id AND w.request_id=a.action_id
   JOIN mcpguard.authorization_heads h ON h.org_id=a.org_id
   WHERE a.org_id=$1 AND a.action_id=$2
   UNION ALL
   SELECT w.seq,w.subject_id,w.event_type,
     CASE WHEN w.event_type='OUTCOME_UNKNOWN' THEN '{"reason":"NATIVE_OUTCOME_UNCONFIRMED"}'::jsonb ELSE '{}'::jsonb END,w.created_at
   FROM mcpguard_ra.write_audit w WHERE w.org_id=$1 AND w.request_id=$2 AND w.event_type IN ('CANCELLED','OUTCOME_UNKNOWN')
   AND NOT EXISTS(SELECT FROM mcpguard_ra.high_risk_audit h WHERE h.org_id=$1 AND h.action_id=$2 AND h.event_kind=w.event_type)
 ) events ORDER BY seq`, p.OrgID, id, p.ID)
	if err != nil {
		return evidence, ErrUnavailable
	}
	events, err := pgx.CollectRows(rows, pgx.RowToStructByPos[HighRiskAuditEvent])
	if err != nil {
		return evidence, ErrUnavailable
	}
	receipts, err := s.HighRisk.Notifier.Receipts(ctx, p.OrgID, id)
	if err != nil {
		return evidence, err
	}
	return HighRiskEvidence{ActionID: id, State: status.State, Events: events, Notifications: receipts}, nil
}

// RecoverHighRisk records interrupted claims without attempting native I/O.
// This interval is longer than the native call and its result-write timeout.
func (s *WriteService) RecoverHighRisk(ctx context.Context) error {
	if s.HighRisk == nil {
		return nil
	}
	_, err := s.Catalog.Store.Pool.Exec(ctx, `
 WITH stale AS (
  UPDATE mcpguard_ra.write_requests w SET state='OUTCOME_UNKNOWN'
  FROM mcpguard_ra.high_risk_writes h WHERE h.org_id=w.org_id AND h.action_id=w.request_id
    AND w.state='EXECUTING' AND h.dispatch_at < clock_timestamp()-interval '1 minute'
  RETURNING w.org_id,w.subject_id,w.request_id
 )
 INSERT INTO mcpguard_ra.write_audit(org_id,subject_id,request_id,event_type)
 SELECT org_id,subject_id,request_id,'OUTCOME_UNKNOWN' FROM stale`)
	return err
}

func highRiskAudit(ctx context.Context, tx pgx.Tx, p metadata.Principal, id, kind string, detail map[string]any) error {
	data, err := json.Marshal(detail)
	if err != nil {
		return ErrUnavailable
	}
	_, err = tx.Exec(ctx, `INSERT INTO mcpguard_ra.high_risk_audit(org_id,action_id,actor_id,event_kind,detail)
 VALUES($1,$2,$3,$4,$5)`, p.OrgID, id, p.ID, kind, data)
	if err != nil {
		return ErrUnavailable
	}
	return nil
}
