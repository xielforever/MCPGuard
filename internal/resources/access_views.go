package resources

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"strconv"
	"time"
)

func (s *AccessService) readGrant(ctx context.Context, tx pgx.Tx, org, id string) (*v02.AccessGrant, error) {
	g := &v02.AccessGrant{}
	var expiry time.Time
	accountColumns := "NULL::text,NULL::text"
	if s.Catalog.SchemaVersion >= 3 {
		accountColumns = "v.account_id::text,v.binding_id::text"
	}
	e := tx.QueryRow(ctx, `SELECT v.grant_id::text,v.request_id::text,v.decision_id::text,v.subject_id::text,v.resource_id::text,v.resource_version::text,v.profile_id::text,v.profile_version::text,v.version::text,v.expires_at,h.state,h.revision::text,h.auth_epoch::text,`+accountColumns+` FROM mcpguard_ra.access_grant_versions v JOIN mcpguard_ra.access_grant_heads h ON (h.org_id,h.grant_id,h.version)=(v.org_id,v.grant_id,v.version) WHERE v.org_id=$1 AND v.grant_id=$2`, org, id).Scan(&g.GrantID, &g.RequestID, &g.DecisionID, &g.SubjectID, &g.ResourceID, &g.ResourceVersion, &g.ProfileID, &g.ProfileVersion, &g.GrantVersion, &expiry, &g.State, &g.Revision, &g.AuthEpoch, &g.AccountID, &g.BindingID)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ErrDenied
	}
	if e != nil {
		return nil, ErrUnavailable
	}
	g.OrgID = org
	g.ExpiresAt = expiry.UTC().Format(time.RFC3339Nano)
	return g, nil
}
func (s *AccessService) refreshGrant(ctx context.Context, tx pgx.Tx, p metadata.Principal, r *accessRequest, g *v02.AccessGrant) error {
	if g.State != "PROVISIONING" && g.State != "ACTIVE" && g.State != "PROVISIONING_UNKNOWN" {
		return nil
	}
	now, e := databaseTime(ctx, tx)
	if e != nil {
		return e
	}
	expiry, _, _ := normalizedTime(g.ExpiresAt)
	next := ""
	if !expiry.After(now) {
		next = "EXPIRED"
	} else {
		yes, e := s.qualified(ctx, tx, r)
		if e != nil {
			return e
		}
		if !yes {
			next = "REVOKED"
		}
	}
	if next == "" {
		return nil
	}
	var epoch int64
	if e = tx.QueryRow(ctx, "SELECT mcpguard_ra.advance_access_epoch($1)", p.OrgID).Scan(&epoch); e != nil {
		return e
	}
	tag, e := tx.Exec(ctx, "UPDATE mcpguard_ra.access_grant_heads SET state=$3,revision=revision+1,auth_epoch=$4 WHERE org_id=$1 AND grant_id=$2 AND state=$5", p.OrgID, g.GrantID, next, epoch, g.State)
	if e != nil || tag.RowsAffected() != 1 {
		return ErrConflict
	}
	g.State = next
	g.Revision = strconv.FormatInt(number(g.Revision)+1, 10)
	g.AuthEpoch = strconv.FormatInt(epoch, 10)
	return s.audit(ctx, tx, p, r, g.GrantID, "GRANT_"+next)
}
func (s *AccessService) grantForRequest(ctx context.Context, tx pgx.Tx, p metadata.Principal, r *accessRequest) (*v02.AccessGrant, error) {
	var id string
	e := tx.QueryRow(ctx, "SELECT grant_id::text FROM mcpguard_ra.access_grant_versions WHERE org_id=$1 AND request_id=$2 ORDER BY version DESC LIMIT 1", p.OrgID, r.View.RequestID).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, ErrUnavailable
	}
	g, e := s.readGrant(ctx, tx, p.OrgID, id)
	if e != nil {
		return nil, e
	}
	if e = s.refreshGrant(ctx, tx, p, r, g); e != nil {
		return nil, e
	}
	return g, nil
}
func (s *AccessService) Grant(ctx context.Context, p metadata.Principal, id string) (v02.AccessGrant, error) {
	var out v02.AccessGrant
	if !valid("GrantIDInput", v02.GrantIDInput{GrantID: id}) {
		return out, ErrInput
	}
	tx, _, e := s.begin(ctx, p)
	if e != nil {
		return out, e
	}
	defer rollback(tx)
	g, e := s.readGrant(ctx, tx, p.OrgID, id)
	if e != nil {
		return out, e
	}
	r, e := s.request(ctx, tx, p.OrgID, g.RequestID)
	if e != nil {
		return out, e
	}
	if g.SubjectID != p.ID {
		yes, e := s.reviewer(ctx, tx, p, r)
		if e != nil {
			return out, e
		}
		if !yes {
			return out, ErrDenied
		}
	}
	if e = s.refreshGrant(ctx, tx, p, r, g); e != nil {
		return out, e
	}
	return *g, commit(ctx, tx)
}
func (s *AccessService) page(p metadata.Principal, kind string, epoch int64, after *string) (catalogCursor, any, error) {
	cursor := catalogCursor{Org: p.OrgID, Principal: p.ID, Kind: kind, Epoch: epoch}
	var last any
	if after != nil && *after != "" {
		id, e := s.Catalog.parseCursor(*after, cursor)
		if e != nil {
			return cursor, nil, e
		}
		last = id
	}
	return cursor, last, nil
}
func currentEpoch(ctx context.Context, tx pgx.Tx, org string) (int64, error) {
	var epoch int64
	e := tx.QueryRow(ctx, "SELECT auth_epoch FROM mcpguard.authorization_heads WHERE org_id=$1", org).Scan(&epoch)
	return epoch, e
}
func (s *AccessService) Grants(ctx context.Context, p metadata.Principal, in v02.ListGrantsInput) (v02.ListGrantsResult, error) {
	out := v02.ListGrantsResult{Grants: []v02.AccessGrant{}}
	if !valid("ListGrantsInput", in) {
		return out, ErrInput
	}
	tx, epoch, e := s.begin(ctx, p)
	if e != nil {
		return out, e
	}
	defer rollback(tx)
	limit := 100
	resource := ""
	if in.Limit != nil {
		limit = *in.Limit
	}
	if in.ResourceID != nil {
		resource = *in.ResourceID
	}
	cursor, last, e := s.page(p, "access-grants/"+resource, epoch, in.After)
	if e != nil {
		return out, e
	}
	ids, e := accessIDs(ctx, tx, `SELECT v.grant_id::text FROM mcpguard_ra.access_grant_versions v JOIN mcpguard_ra.access_grant_heads h ON (h.org_id,h.grant_id,h.version)=(v.org_id,v.grant_id,v.version) WHERE v.org_id=$1 AND v.subject_id=$2 AND ($3='' OR v.resource_id::text=$3) AND ($4::uuid IS NULL OR v.grant_id>$4) ORDER BY v.grant_id LIMIT $5`, p.OrgID, p.ID, resource, last, limit+1)
	if e != nil {
		return out, e
	}
	more := len(ids) > limit
	if more {
		ids = ids[:limit]
	}
	for _, id := range ids {
		g, e := s.readGrant(ctx, tx, p.OrgID, id)
		if e != nil {
			return out, e
		}
		r, e := s.request(ctx, tx, p.OrgID, g.RequestID)
		if e != nil {
			return out, e
		}
		if e = s.refreshGrant(ctx, tx, p, r, g); e != nil {
			return out, e
		}
		out.Grants = append(out.Grants, *g)
	}
	if more {
		cursor.Last = ids[len(ids)-1]
		cursor.Epoch, e = currentEpoch(ctx, tx, p.OrgID)
		if e != nil {
			return out, e
		}
		out.NextAfter = s.Catalog.cursor(cursor)
	}
	return out, commit(ctx, tx)
}
func (s *AccessService) ReviewQueue(ctx context.Context, p metadata.Principal, in v02.ListAccessRequestsInput) (v02.ListAccessRequestsResult, error) {
	out := v02.ListAccessRequestsResult{Requests: []v02.AccessRequest{}}
	if !valid("ListAccessRequestsInput", in) {
		return out, ErrInput
	}
	tx, epoch, e := s.begin(ctx, p)
	if e != nil {
		return out, e
	}
	defer rollback(tx)
	limit := 100
	resource := ""
	if in.Limit != nil {
		limit = *in.Limit
	}
	if in.ResourceID != nil {
		resource = *in.ResourceID
	}
	cursor, last, e := s.page(p, "access-review/"+resource, epoch, in.After)
	if e != nil {
		return out, e
	}
	ids, e := accessIDs(ctx, tx, `SELECT r.request_id::text FROM mcpguard_ra.access_requests r JOIN mcpguard_ra.access_reviewers a ON (a.org_id,a.resource_id)=(r.org_id,r.resource_id) WHERE r.org_id=$1 AND a.principal_id=$2 AND a.can_approve AND r.subject_id<>$2 AND r.state='PENDING_APPROVAL' AND ($3='' OR r.resource_id::text=$3) AND ($4::uuid IS NULL OR r.request_id>$4) ORDER BY r.request_id LIMIT $5`, p.OrgID, p.ID, resource, last, limit+1)
	if e != nil {
		return out, e
	}
	more := len(ids) > limit
	if more {
		ids = ids[:limit]
	}
	for _, id := range ids {
		r, e := s.request(ctx, tx, p.OrgID, id)
		if e != nil {
			return out, e
		}
		if e = s.refreshRequest(ctx, tx, p, r); e != nil {
			return out, e
		}
		if r.View.State == "PENDING_APPROVAL" {
			out.Requests = append(out.Requests, r.View)
		}
	}
	if more {
		cursor.Last = ids[len(ids)-1]
		out.NextAfter = s.Catalog.cursor(cursor)
	}
	return out, commit(ctx, tx)
}

// Decorate reports the latest workflow for each resource. This summary never
// grants execution; individual grant status remains authoritative.
func (s *AccessService) Decorate(ctx context.Context, p metadata.Principal, views []v02.ResourceView) (int64, error) {
	tx, epoch, e := s.begin(ctx, p)
	if e != nil {
		return 0, e
	}
	defer rollback(tx)
	for i := range views {
		var requestable bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM mcpguard_ra.resource_heads h JOIN mcpguard_ra.resource_access a ON (a.org_id,a.resource_id)=(h.org_id,h.resource_id) JOIN mcpguard_ra.access_policy_heads ph ON (ph.org_id,ph.resource_id,ph.resource_version)=(h.org_id,h.resource_id,h.version) JOIN mcpguard_ra.tool_bindings t ON (t.org_id,t.resource_id,t.resource_version,t.profile_id,t.profile_version)=(ph.org_id,ph.resource_id,ph.resource_version,ph.profile_id,ph.profile_version) WHERE h.org_id=$1 AND h.resource_id=$2 AND a.principal_id=$3 AND a.can_discover AND a.can_request AND h.enabled AND ph.enabled)`, p.OrgID, views[i].ResourceID, p.ID).Scan(&requestable); e != nil {
			return 0, ErrUnavailable
		}
		views[i].CanRequest = views[i].CanRequest && requestable
		var id string
		e = tx.QueryRow(ctx, "SELECT request_id::text FROM mcpguard_ra.access_requests WHERE org_id=$1 AND subject_id=$2 AND resource_id=$3 ORDER BY created_at DESC,request_id DESC LIMIT 1", p.OrgID, p.ID, views[i].ResourceID).Scan(&id)
		if errors.Is(e, pgx.ErrNoRows) {
			continue
		}
		if e != nil {
			return 0, ErrUnavailable
		}
		r, e := s.request(ctx, tx, p.OrgID, id)
		if e != nil {
			return 0, e
		}
		status, e := s.status(ctx, tx, p, r)
		if e != nil {
			return 0, e
		}
		if status.Grant != nil {
			views[i].AccessState = status.Grant.State
		} else {
			switch status.Request.State {
			case "PENDING_APPROVAL":
				views[i].AccessState = "PENDING"
			default:
				views[i].AccessState = status.Request.State
			}
		}
	}
	epoch, e = currentEpoch(ctx, tx, p.OrgID)
	if e != nil {
		return 0, e
	}
	return epoch, commit(ctx, tx)
}

func (s *AccessService) ProfileLimits(ctx context.Context, p metadata.Principal, resource v02.ResourceView, profiles []v02.ProfileView) error {
	tx, _, e := s.begin(ctx, p)
	if e != nil {
		return e
	}
	defer rollback(tx)
	for i := range profiles {
		choice, e := s.choose(ctx, tx, p.OrgID, p.ID, resource.ResourceID, profiles[i].ProfileID)
		if errors.Is(e, ErrDenied) {
			continue
		}
		if e != nil {
			return e
		}
		if choice.ResourceVersion != resource.Version || choice.Profile.Version != profiles[i].Version {
			return ErrConflict
		}
		maxTTL, reviewTTL := choice.Policy.MaxGrantTtlSeconds, choice.Policy.ReviewTtlSeconds
		profiles[i].MaxGrantTtlSeconds = &maxTTL
		profiles[i].ReviewTtlSeconds = &reviewTTL
	}
	return commit(ctx, tx)
}
