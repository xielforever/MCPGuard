package resources

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

var ErrIdempotency = errors.New("access idempotency conflict")

type AccessService struct{ Catalog *Catalog }
type qualification struct {
	ResourceRevision, AccessRevision, SubjectRevision, PolicyRevision int64
	PolicyVersion                                                     string
	ProvisionTargetVersion, RegistrationVersion                       int64
}
type selection struct {
	ResourceVersion string
	Profile         v02.PermissionProfile
	Policy          v02.AccessPolicy
	Qualification   qualification
}
type accessRequest struct {
	View          v02.AccessRequest
	Qualification qualification
	Deadline      time.Time
}

func NewAccessService(c *Catalog) (*AccessService, error) {
	if c == nil || c.SchemaVersion < 2 {
		return nil, ErrUnavailable
	}
	s := &AccessService{Catalog: c}
	c.Access = s
	return s, nil
}
func (s *AccessService) begin(ctx context.Context, p metadata.Principal) (pgx.Tx, int64, error) {
	tx, e := s.Catalog.Store.Pool.Begin(ctx)
	if e != nil {
		return nil, 0, ErrUnavailable
	}
	var epoch int64
	if e = tx.QueryRow(ctx, "SELECT mcpguard.lock_auth_epoch_exclusive($1)", p.OrgID).Scan(&epoch); e != nil {
		rollback(tx)
		return nil, 0, ErrUnavailable
	}
	var current bool
	e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT FROM mcpguard.principals WHERE org_id=$1 AND id=$2 AND token_hash=$3 AND enabled AND expires_at>clock_timestamp())", p.OrgID, p.ID, p.TokenHash).Scan(&current)
	if e != nil {
		rollback(tx)
		return nil, 0, ErrUnavailable
	}
	if !current {
		rollback(tx)
		return nil, 0, ErrDenied
	}
	return tx, epoch, nil
}
func commit(ctx context.Context, tx pgx.Tx) error {
	if tx.Commit(ctx) != nil {
		return ErrUnavailable
	}
	return nil
}

type databaseClock interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func databaseTime(ctx context.Context, db databaseClock) (time.Time, error) {
	var now time.Time
	e := db.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now)
	return now, e
}

// Recheck the wall clock after every wait since the timer measures elapsed time.
func waitForDatabaseTime(ctx context.Context, db databaseClock, target time.Time) error {
	for {
		now, err := databaseTime(ctx, db)
		if err != nil {
			return err
		}
		if !now.Before(target) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(target.Sub(now)):
		}
	}
}

func normalizedTime(raw string) (time.Time, string, error) {
	v, e := time.Parse(time.RFC3339Nano, raw)
	if e != nil {
		return v, "", ErrInput
	}
	v = v.UTC().Truncate(time.Microsecond)
	return v, v.Format(time.RFC3339Nano), nil
}
func (s *AccessService) choose(ctx context.Context, tx pgx.Tx, org, owner, rid, pid string) (selection, error) {
	var out selection
	rows, e := tx.Query(ctx, `SELECT DISTINCT h.version::text,h.revision,a.revision,s.revision,p.definition,v.definition,ph.revision
 FROM mcpguard_ra.resource_heads h
 JOIN mcpguard_ra.resource_access a ON (a.org_id,a.resource_id)=(h.org_id,h.resource_id)
 JOIN mcpguard.principals person ON (person.org_id,person.id)=(a.org_id,a.principal_id)
 JOIN mcpguard_ra.subject_revisions s ON (s.org_id,s.principal_id)=(a.org_id,a.principal_id)
 JOIN mcpguard_ra.tool_bindings t ON (t.org_id,t.resource_id,t.resource_version)=(h.org_id,h.resource_id,h.version)
 JOIN mcpguard_ra.profile_versions p ON (p.org_id,p.resource_id,p.profile_id,p.version)=(t.org_id,t.resource_id,t.profile_id,t.profile_version)
 JOIN mcpguard_ra.access_policy_heads ph ON (ph.org_id,ph.resource_id,ph.resource_version,ph.profile_id,ph.profile_version)=(h.org_id,h.resource_id,h.version,p.profile_id,p.version)
 JOIN mcpguard_ra.access_policy_versions v ON (v.org_id,v.resource_id,v.resource_version,v.profile_id,v.profile_version,v.version)=(ph.org_id,ph.resource_id,ph.resource_version,ph.profile_id,ph.profile_version,ph.version)
 WHERE h.org_id=$1 AND a.principal_id=$2 AND h.resource_id=$3 AND p.profile_id=$4
 AND h.enabled AND a.can_discover AND a.can_request AND person.enabled AND person.expires_at>clock_timestamp() AND ph.enabled`, org, owner, rid, pid)
	if e != nil {
		return out, ErrUnavailable
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var profile, policy []byte
		if rows.Scan(&out.ResourceVersion, &out.Qualification.ResourceRevision, &out.Qualification.AccessRevision, &out.Qualification.SubjectRevision, &profile, &policy, &out.Qualification.PolicyRevision) != nil {
			return out, ErrUnavailable
		}
		if v02.Decode("PermissionProfile", profile, &out.Profile) != nil || v02.Decode("AccessPolicy", policy, &out.Policy) != nil {
			return out, ErrUnavailable
		}
		count++
	}
	if rows.Err() != nil {
		return out, ErrUnavailable
	}
	if count == 0 {
		return out, ErrDenied
	}
	if count != 1 {
		return out, ErrConflict
	}
	if out.Profile.ApprovalPolicyRef != out.Policy.ApprovalPolicyRef {
		return out, ErrDenied
	}
	out.Qualification.PolicyVersion = out.Policy.Version
	if s.Catalog.SchemaVersion >= 3 {
		var version int64
		e = tx.QueryRow(ctx, "SELECT version FROM mcpguard_ra.provision_targets WHERE org_id=$1 AND resource_id=$2 AND resource_version=$3 AND profile_id=$4 AND profile_version=$5", org, rid, out.ResourceVersion, pid, out.Profile.Version).Scan(&version)
		if e == nil {
			out.Qualification.ProvisionTargetVersion = version
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return out, ErrUnavailable
		}
		e = tx.QueryRow(ctx, "SELECT version FROM mcpguard_ra.existing_account_registrations WHERE org_id=$1 AND subject_id=$2 AND resource_id=$3 AND resource_version=$4 AND profile_id=$5 AND profile_version=$6", org, owner, rid, out.ResourceVersion, pid, out.Profile.Version).Scan(&version)
		if e == nil {
			out.Qualification.RegistrationVersion = version
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return out, ErrUnavailable
		}
	}
	return out, nil
}
func (s *AccessService) request(ctx context.Context, tx pgx.Tx, org, id string) (*accessRequest, error) {
	r := &accessRequest{}
	var ciphertext, q []byte
	var state, revision string
	e := tx.QueryRow(ctx, `SELECT request_ciphertext,qualification,state,revision::text,request_deadline FROM mcpguard_ra.access_requests WHERE org_id=$1 AND request_id=$2`, org, id).Scan(&ciphertext, &q, &state, &revision, &r.Deadline)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ErrDenied
	}
	if e != nil {
		return nil, ErrUnavailable
	}
	plain, e := s.Catalog.Store.Vault.Open(ciphertext, "access-request/"+org+"/"+id)
	if e != nil || v02.Decode("AccessRequest", plain, &r.View) != nil || json.Unmarshal(q, &r.Qualification) != nil {
		return nil, ErrUnavailable
	}
	if r.View.OrgID != org || r.View.RequestID != id {
		return nil, ErrUnavailable
	}
	r.View.State = state
	r.View.Revision = revision
	return r, nil
}
func (s *AccessService) reviewer(ctx context.Context, tx pgx.Tx, p metadata.Principal, r *accessRequest) (bool, error) {
	if p.ID == r.View.OwnerID {
		return false, nil
	}
	var allowed bool
	e := tx.QueryRow(ctx, "SELECT EXISTS(SELECT FROM mcpguard_ra.access_reviewers WHERE org_id=$1 AND resource_id=$2 AND principal_id=$3 AND can_approve)", p.OrgID, r.View.ResourceID, p.ID).Scan(&allowed)
	if e != nil {
		return false, ErrUnavailable
	}
	return allowed, nil
}
func (s *AccessService) audit(ctx context.Context, tx pgx.Tx, p metadata.Principal, r *accessRequest, grantID, event string) error {
	var grant any
	if grantID != "" {
		grant = grantID
	}
	_, e := tx.Exec(ctx, "INSERT INTO mcpguard_ra.access_audit(org_id,actor_id,subject_id,request_id,grant_id,event_type) VALUES($1,$2,$3,$4,$5,$6)", p.OrgID, p.ID, r.View.OwnerID, r.View.RequestID, grant, event)
	return e
}
func (s *AccessService) state(ctx context.Context, tx pgx.Tx, p metadata.Principal, r *accessRequest, next string) error {
	tag, e := tx.Exec(ctx, "UPDATE mcpguard_ra.access_requests SET state=$3,revision=revision+1 WHERE org_id=$1 AND request_id=$2 AND state='PENDING_APPROVAL'", p.OrgID, r.View.RequestID, next)
	if e != nil || tag.RowsAffected() != 1 {
		return ErrConflict
	}
	r.View.State = next
	r.View.Revision = strconv.FormatInt(number(r.View.Revision)+1, 10)
	return s.audit(ctx, tx, p, r, "", "REQUEST_"+next)
}
func (s *AccessService) qualified(ctx context.Context, tx pgx.Tx, r *accessRequest) (bool, error) {
	current, e := s.choose(ctx, tx, r.View.OrgID, r.View.OwnerID, r.View.ResourceID, r.View.ProfileID)
	if errors.Is(e, ErrDenied) || errors.Is(e, ErrConflict) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	return current.ResourceVersion == r.View.ResourceVersion && current.Profile.Version == r.View.ProfileVersion && current.Qualification == r.Qualification, nil
}
func (s *AccessService) refreshRequest(ctx context.Context, tx pgx.Tx, p metadata.Principal, r *accessRequest) error {
	if r.View.State != "PENDING_APPROVAL" {
		return nil
	}
	now, e := databaseTime(ctx, tx)
	if e != nil {
		return e
	}
	next := ""
	if !now.Before(r.Deadline) {
		next = "EXPIRED"
	} else {
		yes, e := s.qualified(ctx, tx, r)
		if e != nil {
			return e
		}
		if !yes {
			next = "STALE"
		}
	}
	if next != "" {
		return s.state(ctx, tx, p, r, next)
	}
	return nil
}
func (s *AccessService) command(ctx context.Context, tx pgx.Tx, p metadata.Principal, command, target, key string, input, out any) (bool, error) {
	b, _ := json.Marshal(input)
	hash := s.Catalog.Store.Vault.Hash(b)
	var saved string
	var ciphertext []byte
	e := tx.QueryRow(ctx, "SELECT payload_hash,response_ciphertext FROM mcpguard_ra.access_commands WHERE org_id=$1 AND actor_id=$2 AND command=$3 AND target=$4 AND key=$5", p.OrgID, p.ID, command, target, key).Scan(&saved, &ciphertext)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, ErrUnavailable
	}
	if saved != hash {
		return false, ErrIdempotency
	}
	plain, e := s.Catalog.Store.Vault.Open(ciphertext, "access-command/"+p.OrgID+"/"+p.ID+"/"+command+"/"+target+"/"+key)
	if e != nil || json.Unmarshal(plain, out) != nil {
		return false, ErrUnavailable
	}
	return true, nil
}
func (s *AccessService) saveCommand(ctx context.Context, tx pgx.Tx, p metadata.Principal, command, target, key string, input, out any) error {
	b, _ := json.Marshal(input)
	response, _ := json.Marshal(out)
	encrypted := s.Catalog.Store.Vault.Seal(response, "access-command/"+p.OrgID+"/"+p.ID+"/"+command+"/"+target+"/"+key)
	_, e := tx.Exec(ctx, "INSERT INTO mcpguard_ra.access_commands VALUES($1,$2,$3,$4,$5,$6,$7)", p.OrgID, p.ID, command, target, key, s.Catalog.Store.Vault.Hash(b), encrypted)
	return e
}
func (s *AccessService) Request(ctx context.Context, p metadata.Principal, in v02.AccessRequestInput) (v02.AccessRequest, error) {
	var out v02.AccessRequest
	if !valid("AccessRequestInput", in) {
		return out, ErrInput
	}
	expiry, stamp, e := normalizedTime(in.RequestedExpiry)
	if e != nil {
		return out, e
	}
	in.RequestedExpiry = stamp
	tx, _, e := s.begin(ctx, p)
	if e != nil {
		return out, e
	}
	defer rollback(tx)
	// Reauthenticate the current resource request right even for a replay.
	chosen, e := s.choose(ctx, tx, p.OrgID, p.ID, in.ResourceID, in.ProfileID)
	if e != nil {
		return out, e
	}
	replay, e := s.command(ctx, tx, p, "request", in.ResourceID, in.RequestKey, in, &out)
	if e != nil {
		return out, e
	}
	if replay {
		return out, commit(ctx, tx)
	}
	now, e := databaseTime(ctx, tx)
	if e != nil {
		return out, e
	}
	if !expiry.After(now) || expiry.After(now.Add(time.Duration(chosen.Policy.MaxGrantTtlSeconds)*time.Second)) {
		return out, ErrInput
	}
	ids, e := accessIDs(ctx, tx, "SELECT request_id::text FROM mcpguard_ra.access_requests WHERE org_id=$1 AND subject_id=$2 AND state='PENDING_APPROVAL' ORDER BY request_id LIMIT 33", p.OrgID, p.ID)
	if e != nil {
		return out, e
	}
	open := 0
	for _, id := range ids {
		r, e := s.request(ctx, tx, p.OrgID, id)
		if e != nil {
			return out, e
		}
		if e = s.refreshRequest(ctx, tx, p, r); e != nil {
			return out, e
		}
		if r.View.State == "PENDING_APPROVAL" {
			open++
		}
	}
	if open >= v02.MaxAccessOpenRequests {
		if e = commit(ctx, tx); e != nil {
			return out, e
		}
		return out, ErrConflict
	}
	id := uuid.NewString()
	out = v02.AccessRequest{RequestID: id, OrgID: p.OrgID, OwnerID: p.ID, ResourceID: in.ResourceID, ResourceVersion: chosen.ResourceVersion, ProfileID: in.ProfileID, ProfileVersion: chosen.Profile.Version, RequestedExpiry: stamp, Reason: in.Reason, State: "PENDING_APPROVAL", Revision: "1"}
	freeze, _ := json.Marshal(struct {
		Request       v02.AccessRequest
		Qualification qualification
	}{out, chosen.Qualification})
	out.RequestHash = s.Catalog.Store.Vault.Hash(freeze)
	raw, _ := json.Marshal(out)
	encrypted := s.Catalog.Store.Vault.Seal(raw, "access-request/"+p.OrgID+"/"+id)
	q, _ := json.Marshal(chosen.Qualification)
	deadline := now.Add(time.Duration(chosen.Policy.ReviewTtlSeconds) * time.Second)
	if expiry.Before(deadline) {
		deadline = expiry
	}
	_, e = tx.Exec(ctx, `INSERT INTO mcpguard_ra.access_requests(org_id,request_id,subject_id,resource_id,resource_version,profile_id,profile_version,request_hash,requested_expiry,request_deadline,qualification,request_ciphertext,state,revision) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'PENDING_APPROVAL',1)`, p.OrgID, id, p.ID, in.ResourceID, out.ResourceVersion, in.ProfileID, out.ProfileVersion, out.RequestHash, expiry, deadline, q, encrypted)
	if e != nil {
		return out, ErrUnavailable
	}
	r := &accessRequest{View: out, Qualification: chosen.Qualification, Deadline: deadline}
	if e = s.audit(ctx, tx, p, r, "", "REQUEST_SUBMITTED"); e != nil {
		return out, e
	}
	if e = s.saveCommand(ctx, tx, p, "request", in.ResourceID, in.RequestKey, in, out); e != nil {
		return out, e
	}
	return out, commit(ctx, tx)
}
func accessIDs(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]string, error) {
	rows, e := tx.Query(ctx, query, args...)
	if e != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			return nil, ErrUnavailable
		}
		ids = append(ids, id)
	}
	if rows.Err() != nil {
		return nil, ErrUnavailable
	}
	return ids, nil
}
func (s *AccessService) status(ctx context.Context, tx pgx.Tx, p metadata.Principal, r *accessRequest) (v02.AccessStatusResult, error) {
	out := v02.AccessStatusResult{}
	if e := s.refreshRequest(ctx, tx, p, r); e != nil {
		return out, e
	}
	out.Request = r.View
	var cipher []byte
	e := tx.QueryRow(ctx, "SELECT decision_ciphertext FROM mcpguard_ra.access_decisions WHERE org_id=$1 AND request_id=$2", p.OrgID, r.View.RequestID).Scan(&cipher)
	if e == nil {
		raw, e := s.Catalog.Store.Vault.Open(cipher, "access-decision/"+p.OrgID+"/"+r.View.RequestID)
		var decision v02.AccessDecision
		if e != nil || v02.Decode("AccessDecision", raw, &decision) != nil {
			return out, ErrUnavailable
		}
		out.Decision = &decision
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return out, ErrUnavailable
	}
	grant, e := s.grantForRequest(ctx, tx, p, r)
	if e != nil {
		return out, e
	}
	out.Grant = grant
	if s.Catalog.SchemaVersion >= 3 && grant != nil {
		out.Provisioning, e = safeJob(ctx, tx, p.OrgID, grant.GrantID)
		if e != nil {
			return out, e
		}
	}
	return out, nil
}
func (s *AccessService) Status(ctx context.Context, p metadata.Principal, id string) (v02.AccessStatusResult, error) {
	var out v02.AccessStatusResult
	if !valid("RequestIDInput", v02.RequestIDInput{RequestID: id}) {
		return out, ErrInput
	}
	tx, _, e := s.begin(ctx, p)
	if e != nil {
		return out, e
	}
	defer rollback(tx)
	r, e := s.request(ctx, tx, p.OrgID, id)
	if e != nil {
		return out, e
	}
	if r.View.OwnerID != p.ID {
		yes, e := s.reviewer(ctx, tx, p, r)
		if e != nil {
			return out, e
		}
		if !yes {
			return out, ErrDenied
		}
	}
	out, e = s.status(ctx, tx, p, r)
	if e != nil {
		return out, e
	}
	return out, commit(ctx, tx)
}
func (s *AccessService) Cancel(ctx context.Context, p metadata.Principal, in v02.CancelAccessInput) (v02.AccessRequest, error) {
	var out v02.AccessRequest
	if !valid("CancelAccessInput", in) {
		return out, ErrInput
	}
	tx, _, e := s.begin(ctx, p)
	if e != nil {
		return out, e
	}
	defer rollback(tx)
	r, e := s.request(ctx, tx, p.OrgID, in.RequestID)
	if e != nil {
		return out, e
	}
	if r.View.OwnerID != p.ID {
		return out, ErrDenied
	}
	replay, e := s.command(ctx, tx, p, "cancel", in.RequestID, in.RequestKey, in, &out)
	if e != nil {
		return out, e
	}
	if replay {
		return out, commit(ctx, tx)
	}
	if e = s.refreshRequest(ctx, tx, p, r); e != nil {
		return out, e
	}
	if r.View.State != "PENDING_APPROVAL" || r.View.Revision != in.ExpectedRevision {
		if e = commit(ctx, tx); e != nil {
			return out, e
		}
		return out, ErrConflict
	}
	if e = s.state(ctx, tx, p, r, "CANCELLED"); e != nil {
		return out, e
	}
	out = r.View
	if e = s.saveCommand(ctx, tx, p, "cancel", in.RequestID, in.RequestKey, in, out); e != nil {
		return out, e
	}
	return out, commit(ctx, tx)
}
func (s *AccessService) Decide(ctx context.Context, p metadata.Principal, id string, in v02.DecisionInput) (v02.AccessStatusResult, error) {
	var out v02.AccessStatusResult
	if !valid("DecisionInput", in) || !valid("RequestIDInput", v02.RequestIDInput{RequestID: id}) {
		return out, ErrInput
	}
	var expiry time.Time
	if in.ApprovedExpiry != nil {
		var stamp string
		var e error
		expiry, stamp, e = normalizedTime(*in.ApprovedExpiry)
		if e != nil {
			return out, e
		}
		in.ApprovedExpiry = &stamp
	}
	tx, _, e := s.begin(ctx, p)
	if e != nil {
		return out, e
	}
	defer rollback(tx)
	r, e := s.request(ctx, tx, p.OrgID, id)
	if e != nil {
		return out, e
	}
	yes, e := s.reviewer(ctx, tx, p, r)
	if e != nil {
		return out, e
	}
	if !yes {
		return out, ErrDenied
	}
	replay, e := s.command(ctx, tx, p, "decision", id, in.RequestKey, in, &out)
	if e != nil {
		return out, e
	}
	if replay {
		return out, commit(ctx, tx)
	}
	if e = s.refreshRequest(ctx, tx, p, r); e != nil {
		return out, e
	}
	if r.View.State != "PENDING_APPROVAL" || r.View.Revision != in.ExpectedRevision || r.View.RequestHash != in.RequestHash {
		if e = commit(ctx, tx); e != nil {
			return out, e
		}
		return out, ErrConflict
	}
	now, e := databaseTime(ctx, tx)
	if e != nil {
		return out, e
	}
	requested, _, _ := normalizedTime(r.View.RequestedExpiry)
	if in.Decision == "APPROVE" && (!expiry.After(now) || expiry.After(requested)) {
		return out, ErrInput
	}
	decision := v02.AccessDecision{DecisionID: uuid.NewString(), RequestID: id, RequestHash: r.View.RequestHash, RequestRevision: r.View.Revision, DeciderID: p.ID, Source: "HUMAN", Decision: in.Decision, ApprovedExpiry: in.ApprovedExpiry, CreatedAt: now.UTC().Format(time.RFC3339Nano), Reason: in.Reason}
	raw, _ := json.Marshal(decision)
	encrypted := s.Catalog.Store.Vault.Seal(raw, "access-decision/"+p.OrgID+"/"+id)
	var approved any
	if in.ApprovedExpiry != nil {
		approved = expiry
	}
	_, e = tx.Exec(ctx, `INSERT INTO mcpguard_ra.access_decisions(org_id,decision_id,request_id,request_hash,request_revision,subject_id,decider_id,decision,source,approved_expiry,decision_ciphertext) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'HUMAN',$9,$10)`, p.OrgID, decision.DecisionID, id, decision.RequestHash, decision.RequestRevision, r.View.OwnerID, p.ID, in.Decision, approved, encrypted)
	if e != nil {
		return out, ErrUnavailable
	}
	next := "REJECTED"
	if in.Decision == "APPROVE" {
		next = "APPROVED"
		gid := uuid.NewString()
		if s.Catalog.SchemaVersion >= 3 {
			if _, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.access_grant_ids VALUES($1,$2,$3)", p.OrgID, gid, id); e != nil {
				return out, ErrUnavailable
			}
		}
		_, e = tx.Exec(ctx, `INSERT INTO mcpguard_ra.access_grant_versions(org_id,grant_id,version,request_id,decision_id,subject_id,resource_id,resource_version,profile_id,profile_version,expires_at) VALUES($1,$2,1,$3,$4,$5,$6,$7,$8,$9,$10)`, p.OrgID, gid, id, decision.DecisionID, r.View.OwnerID, r.View.ResourceID, r.View.ResourceVersion, r.View.ProfileID, r.View.ProfileVersion, expiry)
		if e != nil {
			return out, ErrUnavailable
		}
		var epoch int64
		if e = tx.QueryRow(ctx, "SELECT mcpguard_ra.advance_access_epoch($1)", p.OrgID).Scan(&epoch); e != nil {
			return out, e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.access_grant_heads VALUES($1,$2,1,'PROVISIONING',1,$3)", p.OrgID, gid, epoch); e != nil {
			return out, e
		}
		if e = s.audit(ctx, tx, p, r, gid, "GRANT_PROVISIONING"); e != nil {
			return out, e
		}
	}
	if e = s.state(ctx, tx, p, r, next); e != nil {
		return out, e
	}
	if e = s.audit(ctx, tx, p, r, "", "DECISION_"+in.Decision); e != nil {
		return out, e
	}
	out, e = s.status(ctx, tx, p, r)
	if e != nil {
		return out, e
	}
	if e = s.saveCommand(ctx, tx, p, "decision", id, in.RequestKey, in, out); e != nil {
		return out, e
	}
	return out, commit(ctx, tx)
}
