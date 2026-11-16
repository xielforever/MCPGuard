package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

// WriteStatus is the safe view of one controlled resource write. It never
// carries package bytes, native secrets or the frozen plan.
type WriteStatus struct {
	SchemaVersion   string       `json:"schemaVersion"`
	RequestID       string       `json:"requestId"`
	State           string       `json:"state"`
	ResourceID      string       `json:"resourceId"`
	CredentialID    string       `json:"credentialId"`
	ItemID          string       `json:"itemId"`
	ExpectedVersion v02.Version  `json:"expectedVersion"`
	DecidedBy       *string      `json:"decidedBy"`
	Result          *WriteResult `json:"result"`
}

type WriteResult struct {
	Outcome    string `json:"outcome"`
	NewVersion string `json:"newVersion"`
}

// WriteService owns one fixed resource write. Resource authorization is not a
// write approval: a plan is frozen first, another authorized person decides it,
// and only the requester's explicit resume consumes the single dispatch slot.
type WriteService struct {
	Catalog     *Catalog
	Credentials *CredentialService
	Manager     *ReadManager
	Fence       *RestoreFence
}

func NewWriteService(c *Catalog, credentials *CredentialService, m *ReadManager) *WriteService {
	return &WriteService{Catalog: c, Credentials: credentials, Manager: m}
}

type writeRow struct {
	SubjectID          string
	ResourceID         string
	CredentialID       string
	ItemID             string
	ExpectedVersion    int64
	NativeIdentity     string
	SecretVersion      string
	BindingID          string
	BindingVersion     string
	ResourceVersion    string
	CredentialRevision int64
	AuthEpoch          int64
	State              string
	PlanCipher         []byte
	DecidedBy          *string
}

const writeColumns = `subject_id::text,resource_id::text,credential_id::text,item_id,expected_version,native_identity,secret_version,binding_id::text,binding_version::text,resource_version::text,credential_revision,auth_epoch,state,plan_ciphertext,decided_by::text`


// Plan freezes one write and records it as DEFERRED for another person.
func (s *WriteService) Plan(ctx context.Context, p metadata.Principal, in v02.UpdateNoteInput, requestKey string) (WriteStatus, error) {
	out := WriteStatus{SchemaVersion: "0.2"}
	if !valid("UpdateNoteInput", in) || requestKey == "" || len(requestKey) > 128 {
		return out, ErrInput
	}
	tx, epoch, e := s.Catalog.begin(ctx, p)
	if e != nil {
		return out, fmt.Errorf("plan begin: %w", e)
	}
	defer rollback(tx)
	row, e := s.Credentials.row(ctx, tx, p, in.CredentialID)
	if e != nil || s.Credentials.refresh(ctx, tx, p, row) != nil {
		return out, ErrDenied
	}
	if row.state != "ACTIVE" || row.ref.ResourceID != in.ResourceID {
		return out, ErrDenied
	}
	snap, e := s.Catalog.snapshotBound(ctx, tx, p, in.ResourceID, row.ref.ProfileID, row.ref.ProfileVersion, WriteOperation, WriteToolName, epoch)
	if e != nil {
		return out, fmt.Errorf("plan snapshot: %w", e)
	}
	id := uuid.NewString()
	plan, _ := json.Marshal(struct {
		Principal string
		Snapshot  Snapshot
		Input     v02.UpdateNoteInput
		Revision  int64
	}{p.ID, snap, in, row.revision})
	expected, _ := strconv.ParseInt(string(in.ExpectedVersion), 10, 64)
	_, e = tx.Exec(ctx, `INSERT INTO mcpguard_ra.write_requests(org_id,request_id,subject_id,resource_id,resource_version,binding_id,binding_version,credential_id,credential_revision,native_identity,secret_version,item_id,expected_version,auth_epoch,plan_hash,plan_ciphertext,state,request_key)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,'DEFERRED',$17)`,
		p.OrgID, id, p.ID, in.ResourceID, snap.Resource.Version, snap.Binding.BindingID, snap.Binding.BindingVersion, in.CredentialID, row.revision, snap.Binding.NativeIdentity, snap.Binding.SecretVersion, in.ItemID, expected, epoch, s.Catalog.Store.Vault.Hash(plan), s.Catalog.Store.Vault.Seal(plan, writeAAD(p, id)), requestKey)
	if e != nil {
		return out, fmt.Errorf("plan insert: %w", e)
	}
	if e = writeAudit(ctx, tx, p, id, "PLANNED"); e != nil {
		return out, fmt.Errorf("plan audit: %w", e)
	}
	if e = commit(ctx, tx); e != nil {
		return out, fmt.Errorf("plan commit: %w", e)
	}
	return s.Status(ctx, p, id)
}

// Approve records another person's decision. Self-approval is refused here and
// by a database CHECK constraint.
func (s *WriteService) Approve(ctx context.Context, p metadata.Principal, id string, approve bool, reason string) (WriteStatus, error) {
	out := WriteStatus{SchemaVersion: "0.2"}
	if _, e := parseUUID(id); e != nil || len(reason) > 200 {
		return out, ErrInput
	}
	tx, _, e := s.Catalog.begin(ctx, p)
	if e != nil {
		return out, fmt.Errorf("approve begin2: %w", e)
	}
	defer rollback(tx)
	var subject, resource string
	if tx.QueryRow(ctx, `SELECT subject_id::text,resource_id::text FROM mcpguard_ra.write_requests WHERE org_id=$1 AND request_id=$2`, p.OrgID, id).Scan(&subject, &resource) != nil {
		return out, fmt.Errorf("approve load: %w", ErrDenied)
	}
	if subject == p.ID {
		return out, fmt.Errorf("approve self: %w", ErrDenied)
	}
	var allowed bool
	if tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM mcpguard_ra.access_reviewers WHERE org_id=$1 AND resource_id=$2 AND principal_id=$3 AND can_approve)`, p.OrgID, resource, p.ID).Scan(&allowed) != nil || !allowed {
		var total int
		_ = tx.QueryRow(ctx, `SELECT count(*) FROM mcpguard_ra.access_reviewers WHERE org_id=$1 AND resource_id=$2`, p.OrgID, resource).Scan(&total)
		return out, fmt.Errorf("approve reviewer (org=%s resource=%s principal=%s total=%d): %w", p.OrgID, resource, p.ID, total, ErrDenied)
	}
	next, event := "REJECTED", "REJECTED"
	if approve {
		next, event = "APPROVED", "APPROVED"
	}
	reasonValue := any(nil)
	if reason != "" {
		reasonValue = reason
	}
	tag, e := tx.Exec(ctx, `UPDATE mcpguard_ra.write_requests SET state=$3,decided_by=$4,decided_at=clock_timestamp(),decision_reason=$5 WHERE org_id=$1 AND request_id=$2 AND state='DEFERRED' AND subject_id<>$4`, p.OrgID, id, next, p.ID, reasonValue)
	if e != nil {
		return out, fmt.Errorf("approve update: %w", e)
	}
	if tag.RowsAffected() != 1 {
		return out, fmt.Errorf("approve update rows=%d: %w", tag.RowsAffected(), ErrConflict)
	}
	if e = writeAudit(ctx, tx, p, id, event); e != nil {
		return out, fmt.Errorf("approve audit: %w", e)
	}
	if e = commit(ctx, tx); e != nil {
		return out, fmt.Errorf("approve audit: %w", ErrUnavailable)
	}
	return s.Status(ctx, p, id)
}

// Cancel lets the requester withdraw a plan that has not been decided.
func (s *WriteService) Cancel(ctx context.Context, p metadata.Principal, id string) (WriteStatus, error) {
	tx, e := s.Catalog.Store.Pool.Begin(ctx)
	if e != nil {
		return WriteStatus{SchemaVersion: "0.2"}, ErrUnavailable
	}
	defer rollback(tx)
	tag, e := tx.Exec(ctx, `UPDATE mcpguard_ra.write_requests SET state='CANCELLED' WHERE org_id=$1 AND request_id=$2 AND subject_id=$3 AND state='DEFERRED'`, p.OrgID, id, p.ID)
	if e != nil {
		return WriteStatus{SchemaVersion: "0.2"}, ErrUnavailable
	}
	if tag.RowsAffected() != 1 {
		return WriteStatus{SchemaVersion: "0.2"}, ErrConflict
	}
	if e = writeAudit(ctx, tx, p, id, "CANCELLED"); e != nil {
		return WriteStatus{SchemaVersion: "0.2"}, ErrUnavailable
	}
	if e = commit(ctx, tx); e != nil {
		return WriteStatus{SchemaVersion: "0.2"}, ErrUnavailable
	}
	return s.Status(ctx, p, id)
}

// Resume is the requester's explicit dispatch command. Only one resume can
// claim an approved plan; the native call happens after the plan and the
// current credential state are checked again.
func (s *WriteService) Resume(ctx context.Context, p metadata.Principal, id string) (WriteStatus, error) {
	out := WriteStatus{SchemaVersion: "0.2"}
	tx, e := s.Catalog.Store.Pool.Begin(ctx)
	if e != nil {
		return out, ErrUnavailable
	}
	var subject string
	if e = tx.QueryRow(ctx, `UPDATE mcpguard_ra.write_requests SET state='EXECUTING' WHERE org_id=$1 AND request_id=$2 AND subject_id=$3 AND state='APPROVED' RETURNING subject_id::text`, p.OrgID, id, p.ID).Scan(&subject); e != nil {
		rollback(tx)
		var state string
		_ = s.Catalog.Store.Pool.QueryRow(ctx, `SELECT state FROM mcpguard_ra.write_requests WHERE org_id=$1 AND request_id=$2`, p.OrgID, id).Scan(&state)
		return out, fmt.Errorf("resume claim (state=%s): %w", state, ErrDenied)
	}
	if e = writeAudit(ctx, tx, p, id, "DISPATCH_INTENT"); e != nil {
		rollback(tx)
		return out, fmt.Errorf("resume intent: %w", e)
	}
	if e = commit(ctx, tx); e != nil {
		return out, fmt.Errorf("resume dispatch commit: %w", e)
	}
	row, e := s.row(ctx, p, id)
	if e != nil {
		return out, fmt.Errorf("resume row: %w", e)
	}
	plan, e := s.Catalog.Store.Vault.Open(row.PlanCipher, writeAAD(p, id))
	var frozen struct {
		Principal string
		Snapshot  Snapshot
		Input     v02.UpdateNoteInput
		Revision  int64
	}
	if e != nil || json.Unmarshal(plan, &frozen) != nil {
		s.finish(p, id, "FAILED", nil)
		return out, fmt.Errorf("resume plan: %w", ErrUnavailable)
	}
	// Pre-dispatch re-check: credential, binding and epoch must still match.
	if e = s.current(ctx, p, frozen.Snapshot, frozen.Input); e != nil {
		s.finish(p, id, "FAILED", nil)
		return out, fmt.Errorf("resume current: %w", e)
	}
	outcome, e := s.nativeUpdate(ctx, p, frozen.Snapshot, frozen.Input)
	result := &WriteResult{Outcome: "FAILED", NewVersion: string(frozen.Input.ExpectedVersion)}
	state := "FAILED"
	switch {
	case e != nil:
		state = "FAILED"
	case outcome == "UPDATED":
		state = "SUCCEEDED"
		result.Outcome = "UPDATED"
		if next, err := strconv.ParseInt(string(frozen.Input.ExpectedVersion), 10, 64); err == nil {
			result.NewVersion = strconv.FormatInt(next+1, 10)
		}
	case outcome == "VERSION_CONFLICT":
		state = "FAILED"
		result.Outcome = "VERSION_CONFLICT"
	default:
		state = "FAILED"
		result.Outcome = "NOT_FOUND"
	}
	raw, _ := json.Marshal(result)
	if err := s.finish(p, id, state, raw); err != nil {
		return out, ErrUnavailable
	}
	final, e2 := s.Status(ctx, p, id)
	if e2 != nil {
		return out, fmt.Errorf("resume status: %w", e2)
	}
	if state != "SUCCEEDED" {
		return final, nil
	}
	return final, nil
}

func (s *WriteService) nativeUpdate(ctx context.Context, p metadata.Principal, snap Snapshot, in v02.UpdateNoteInput) (string, error) {
	call, cancel := context.WithTimeout(ctx, time.Duration(snap.Profile.TimeoutMs)*time.Millisecond)
	defer cancel()
	pool, e := s.Manager.writePool(call, p, snap)
	if e != nil {
		return "", fmt.Errorf("native pool: %w", e)
	}
	tx, e := pool.Begin(call)
	if e != nil {
		return "", fmt.Errorf("write pool: %w", e)
	}
	defer rollback(tx)
	var login string
	if tx.QueryRow(call, "SELECT session_user::text").Scan(&login) != nil || login != snap.Binding.NativeIdentity {
		return "", fmt.Errorf("native session (login=%s): %w", login, ErrDenied)
	}
	expected, e := strconv.ParseInt(string(in.ExpectedVersion), 10, 64)
	if e != nil {
		return "", ErrInput
	}
	var outcome string
	tag, e := tx.Exec(call, "UPDATE resource_demo.items SET note=$1, version=version+1 WHERE item_id=$2 AND version=$3", in.Note, in.ItemID, expected)
	if e != nil {
		return "", fmt.Errorf("native update: %w", e)
	}
	if tag.RowsAffected() == 1 {
		outcome = "UPDATED"
	} else {
		var exists bool
		if e := tx.QueryRow(call, "SELECT EXISTS(SELECT 1 FROM resource_demo.items WHERE item_id=$1)", in.ItemID).Scan(&exists); e != nil {
			return "", fmt.Errorf("native exists: %w", e)
		}
		if exists {
			outcome = "VERSION_CONFLICT"
		} else {
			outcome = "NOT_FOUND"
		}
	}
	if e = commit(call, tx); e != nil {
		return "", ErrUnavailable
	}
	return outcome, nil
}

// current re-checks the credential and the binding snapshot before dispatch.
func (s *WriteService) current(ctx context.Context, p metadata.Principal, snap Snapshot, in v02.UpdateNoteInput) error {
	if s.Fence != nil {
		if e := s.Fence.Check(ctx, p.OrgID, snap.Resource.EndpointConfigRef); e != nil {
			return e
		}
	}
	tx, epoch, e := s.Catalog.begin(ctx, p)
	if e != nil {
		return e
	}
	defer rollback(tx)
	row, e := s.Credentials.row(ctx, tx, p, in.CredentialID)
	if e != nil {
		return ErrDenied
	}
	if e = s.Credentials.refresh(ctx, tx, p, row); e != nil {
		return e
	}
	if row.state != "ACTIVE" || row.ref.ResourceID != in.ResourceID {
		return ErrDenied
	}
	now, e := s.Catalog.snapshotBound(ctx, tx, p, in.ResourceID, row.ref.ProfileID, row.ref.ProfileVersion, WriteOperation, WriteToolName, epoch)
	if e != nil {
		return e
	}
	if now.Epoch != snap.Epoch || now.Binding.BindingID != snap.Binding.BindingID || now.Binding.BindingVersion != snap.Binding.BindingVersion {
		return ErrDenied
	}
	return nil
}

func (s *WriteService) row(ctx context.Context, p metadata.Principal, id string) (*writeRow, error) {
	var r writeRow
	e := s.Catalog.Store.Pool.QueryRow(ctx, `SELECT `+writeColumns+` FROM mcpguard_ra.write_requests WHERE org_id=$1 AND request_id=$2`, p.OrgID, id).Scan(&r.SubjectID, &r.ResourceID, &r.CredentialID, &r.ItemID, &r.ExpectedVersion, &r.NativeIdentity, &r.SecretVersion, &r.BindingID, &r.BindingVersion, &r.ResourceVersion, &r.CredentialRevision, &r.AuthEpoch, &r.State, &r.PlanCipher, &r.DecidedBy)
	if e != nil {
		return nil, ErrDenied
	}
	return &r, nil
}

// Status is visible to the requester and to reviewers of the resource.
func (s *WriteService) Status(ctx context.Context, p metadata.Principal, id string) (WriteStatus, error) {
	out := WriteStatus{SchemaVersion: "0.2"}
	r, e := s.row(ctx, p, id)
	if e != nil {
		return out, e
	}
	if r.SubjectID != p.ID {
		var allowed bool
		if s.Catalog.Store.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT FROM mcpguard_ra.access_reviewers WHERE org_id=$1 AND resource_id=$2 AND principal_id=$3 AND can_approve)`, p.OrgID, r.ResourceID, p.ID).Scan(&allowed) != nil || !allowed {
			return out, ErrDenied
		}
	}
	var currentEpoch int64
	if e := s.Catalog.Store.Pool.QueryRow(ctx, `SELECT auth_epoch FROM mcpguard.authorization_heads WHERE org_id=$1`, p.OrgID).Scan(&currentEpoch); e != nil {
		return out, ErrUnavailable
	}
	// The state stays visible, but a result recorded under an older authorization
	// epoch is historical data and is not served after a revocation changed it.
	redactResult := currentEpoch != r.AuthEpoch
	out.RequestID = id
	out.State = r.State
	out.ResourceID = r.ResourceID
	out.CredentialID = r.CredentialID
	out.ItemID = r.ItemID
	out.ExpectedVersion = v02.Version(strconv.FormatInt(r.ExpectedVersion, 10))
	out.DecidedBy = r.DecidedBy
	if !redactResult && (r.State == "SUCCEEDED" || r.State == "FAILED") {
		var cipher []byte
		if s.Catalog.Store.Pool.QueryRow(ctx, `SELECT result_ciphertext FROM mcpguard_ra.write_requests WHERE org_id=$1 AND request_id=$2`, p.OrgID, id).Scan(&cipher) == nil && len(cipher) > 0 {
			if plain, err := s.Catalog.Store.Vault.Open(cipher, writeAAD(p, id)); err == nil {
				var result WriteResult
				if json.Unmarshal(plain, &result) == nil {
					out.Result = &result
				}
			}
		}
	}
	return out, nil
}

func (s *WriteService) finish(p metadata.Principal, id, state string, result []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tx, e := s.Catalog.Store.Pool.Begin(ctx)
	if e != nil {
		return ErrUnavailable
	}
	defer rollback(tx)
	var cipher []byte
	if result != nil {
		cipher = s.Catalog.Store.Vault.Seal(result, writeAAD(p, id))
	}
	if _, e = tx.Exec(ctx, `UPDATE mcpguard_ra.write_requests SET state=$3,result_ciphertext=$4 WHERE org_id=$1 AND request_id=$2 AND state='EXECUTING'`, p.OrgID, id, state, cipher); e != nil {
		return ErrUnavailable
	}
	if e = writeAudit(ctx, tx, p, id, state); e != nil {
		return ErrUnavailable
	}
	return commit(ctx, tx)
}

func writeAudit(ctx context.Context, tx pgx.Tx, p metadata.Principal, id, event string) error {
	_, e := tx.Exec(ctx, `INSERT INTO mcpguard_ra.write_audit(org_id,subject_id,request_id,event_type) VALUES($1,$2,$3,$4)`, p.OrgID, p.ID, id, event)
	return e
}

func writeAAD(p metadata.Principal, id string) string {
	return "ra-write/" + p.OrgID + "/" + p.ID + "/" + id
}

func parseUUID(value string) (string, error) {
	parsed, e := uuid.Parse(value)
	if e != nil {
		return "", e
	}
	return parsed.String(), nil
}

func (s *WriteService) Result(ctx context.Context, p metadata.Principal, id string) (any, error) {
	return s.Status(ctx, p, id)
}

var _ = errors.Is

