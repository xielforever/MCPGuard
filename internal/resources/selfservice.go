package resources

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/xielforever/MCPGuard/internal/metadata"
)

// SelfServiceConsole is the v0.2 operator surface used by the embedded console.
// It only returns metadata needed by the pages: never native secrets, package
// ciphertext, plan bodies or tokens.
type SelfServiceConsole struct {
	Catalog *Catalog
	Access  *AccessService
	Writes  *WriteService
	Revoker *RevocationService
}

// ConsoleResource is one row of the resource page.
type ConsoleResource struct {
	ResourceID    string `json:"resourceId"`
	Kind          string `json:"kind"`
	Enabled       bool   `json:"enabled"`
	Version       string `json:"version"`
	Profiles      int    `json:"profiles"`
	Bindings      int    `json:"bindings"`
	ActiveGrants  int    `json:"activeGrants"`
	ActiveCreds   int    `json:"activeCredentials"`
	PendingWrites int    `json:"pendingWrites"`
}

// ConsoleRevocation is one row of the revocation page.
type ConsoleRevocation struct {
	Seq       string `json:"seq"`
	Kind      string `json:"kind"`
	TargetID  string `json:"targetId"`
	ActorID   string `json:"actorId"`
	SubjectID string `json:"subjectId,omitempty"`
	Resource  string `json:"resourceId,omitempty"`
	Grants    int    `json:"grants"`
	Creds     int    `json:"credentials"`
	Tickets   int    `json:"tickets"`
	Contexts  int    `json:"contexts"`
	CreatedAt string `json:"createdAt"`
}

// ConsoleCredential is one row of the credential page.
type ConsoleCredential struct {
	CredentialID string `json:"credentialId"`
	SubjectID    string `json:"subjectId"`
	ResourceID   string `json:"resourceId"`
	State        string `json:"state"`
	ExpiresAt    string `json:"expiresAt"`
}

// requireReviewer limits console management actions to principals that approve
// resources; the console has no separate admin role in this local slice.
func (s *SelfServiceConsole) requireReviewer(ctx context.Context, p metadata.Principal) error {
	var allowed bool
	if e := s.Catalog.Store.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT FROM mcpguard_ra.access_reviewers WHERE org_id=$1 AND principal_id=$2 AND can_approve)`, p.OrgID, p.ID).Scan(&allowed); e != nil {
		return ErrUnavailable
	}
	if !allowed {
		return ErrDenied
	}
	return nil
}

// Resources lists resource heads with their profile/binding/grant counts.
func (s *SelfServiceConsole) Resources(ctx context.Context, p metadata.Principal) ([]ConsoleResource, error) {
	if e := s.requireReviewer(ctx, p); e != nil {
		return nil, e
	}
	rows, e := s.Catalog.Store.Pool.Query(ctx, `SELECT h.resource_id::text,coalesce(v.definition->>'kind',''),h.enabled,h.version::text,
 (SELECT count(*) FROM mcpguard_ra.profile_versions p WHERE (p.org_id,p.resource_id)=(h.org_id,h.resource_id)),
 (SELECT count(*) FROM mcpguard_ra.identity_bindings b WHERE (b.org_id,b.resource_id)=(h.org_id,h.resource_id) AND b.enabled),
 (SELECT count(*) FROM mcpguard_ra.access_grant_versions g JOIN mcpguard_ra.access_grant_heads gh ON (gh.org_id,gh.grant_id,gh.version)=(g.org_id,g.grant_id,g.version) WHERE g.org_id=h.org_id AND g.resource_id=h.resource_id AND gh.state='ACTIVE'),
 (SELECT count(*) FROM mcpguard_ra.credential_leases l WHERE l.org_id=h.org_id AND l.resource_id=h.resource_id AND l.state='ACTIVE'),
 (SELECT count(*) FROM mcpguard_ra.write_requests w WHERE w.org_id=h.org_id AND w.resource_id=h.resource_id AND w.state IN ('DEFERRED','APPROVED'))
 FROM mcpguard_ra.resource_heads h JOIN mcpguard_ra.resource_versions v ON (v.org_id,v.resource_id,v.version)=(h.org_id,h.resource_id,h.version)
 WHERE h.org_id=$1 ORDER BY h.resource_id`, p.OrgID)
	if e != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	out := []ConsoleResource{}
	for rows.Next() {
		var r ConsoleResource
		if rows.Scan(&r.ResourceID, &r.Kind, &r.Enabled, &r.Version, &r.Profiles, &r.Bindings, &r.ActiveGrants, &r.ActiveCreds, &r.PendingWrites) != nil {
			return nil, ErrUnavailable
		}
		out = append(out, r)
	}
	return out, nil
}

// Credentials lists live credential rows without packages, secrets or ciphertext.
func (s *SelfServiceConsole) Credentials(ctx context.Context, p metadata.Principal) ([]ConsoleCredential, error) {
	if e := s.requireReviewer(ctx, p); e != nil {
		return nil, e
	}
	rows, e := s.Catalog.Store.Pool.Query(ctx, `SELECT credential_id::text,subject_id::text,resource_id::text,state,expires_at::text FROM mcpguard_ra.credential_leases WHERE org_id=$1 AND state<>'EXPIRED' ORDER BY issued_at DESC LIMIT 100`, p.OrgID)
	if e != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	out := []ConsoleCredential{}
	for rows.Next() {
		var c ConsoleCredential
		if rows.Scan(&c.CredentialID, &c.SubjectID, &c.ResourceID, &c.State, &c.ExpiresAt) != nil {
			return nil, ErrUnavailable
		}
		out = append(out, c)
	}
	return out, nil
}

// Revocations lists the operator revocation ledger.
func (s *SelfServiceConsole) Revocations(ctx context.Context, p metadata.Principal) ([]ConsoleRevocation, error) {
	if e := s.requireReviewer(ctx, p); e != nil {
		return nil, e
	}
	rows, e := s.Catalog.Store.Pool.Query(ctx, `SELECT seq::text,target_kind,target_id::text,actor_id::text,coalesce(subject_id::text,''),coalesce(resource_id::text,''),affected_grants,affected_credentials,affected_tickets,affected_contexts,created_at::text FROM mcpguard_ra.revocation_audit WHERE org_id=$1 ORDER BY seq DESC LIMIT 100`, p.OrgID)
	if e != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	out := []ConsoleRevocation{}
	for rows.Next() {
		var r ConsoleRevocation
		if rows.Scan(&r.Seq, &r.Kind, &r.TargetID, &r.ActorID, &r.SubjectID, &r.Resource, &r.Grants, &r.Creds, &r.Tickets, &r.Contexts, &r.CreatedAt) != nil {
			return nil, ErrUnavailable
		}
		out = append(out, r)
	}
	return out, nil
}

// Revoke performs the operator revocation from the console page. The acting
// principal is recorded in the audit.
func (s *SelfServiceConsole) Revoke(ctx context.Context, p metadata.Principal, kind, targetID, reason string) (RevocationResult, error) {
	if e := s.requireReviewer(ctx, p); e != nil {
		return RevocationResult{}, e
	}
	if s.Revoker == nil {
		return RevocationResult{}, ErrUnavailable
	}
	return s.Revoker.Revoke(ctx, p, kind, targetID, reason)
}

// WriteQueue lists pending write approvals visible to this reviewer.
func (s *SelfServiceConsole) WriteQueue(ctx context.Context, p metadata.Principal) ([]WriteStatus, error) {
	if e := s.requireReviewer(ctx, p); e != nil {
		return nil, e
	}
	query := `SELECT request_id::text FROM mcpguard_ra.write_requests w WHERE w.org_id=$1 AND w.state='DEFERRED' AND EXISTS(SELECT FROM mcpguard_ra.access_reviewers r WHERE r.org_id=w.org_id AND r.resource_id=w.resource_id AND r.principal_id=$2 AND r.can_approve)`
	if s.Writes != nil && s.Writes.HighRisk != nil {
		query += ` AND NOT EXISTS(SELECT FROM mcpguard_ra.high_risk_writes h WHERE h.org_id=w.org_id AND h.action_id=w.request_id AND h.reviewer_id<>$2)`
	}
	rows, e := s.Catalog.Store.Pool.Query(ctx, query+` ORDER BY created_at`, p.OrgID, p.ID)
	if e != nil {
		return nil, ErrUnavailable
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			return nil, ErrUnavailable
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := []WriteStatus{}
	for _, id := range ids {
		status, e := s.Writes.Status(ctx, p, id)
		if e != nil {
			continue
		}
		out = append(out, status)
	}
	return out, nil
}

// DecideWrite records the reviewer's decision. The subject can never approve
// their own plan; the service and the database both enforce it.
func (s *SelfServiceConsole) DecideWrite(ctx context.Context, p metadata.Principal, id string, approve bool, reason string) (WriteStatus, error) {
	if e := s.requireReviewer(ctx, p); e != nil {
		return WriteStatus{}, e
	}
	return s.Writes.Approve(ctx, p, id, approve, reason)
}

const selfServicePrefix = "/console/api/v2/self-service/"

// ServeHTTP serves the console self-service API after the browser session,
// identity and CSRF checks already ran in the console handler.
func (s *SelfServiceConsole) ServeHTTP(w http.ResponseWriter, r *http.Request, p metadata.Principal) bool {
	if !strings.HasPrefix(r.URL.Path, selfServicePrefix) {
		return false
	}
	path := strings.TrimPrefix(r.URL.Path, selfServicePrefix)
	write := func(status int, body any) bool {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		raw, _ := json.Marshal(body)
		w.WriteHeader(status)
		_, _ = w.Write(raw)
		return true
	}
	fail := func(e error, code string) bool {
		status := http.StatusBadGateway
		switch {
		case errors.Is(e, ErrInput):
			status, code = http.StatusBadRequest, "INVALID_INPUT"
		case errors.Is(e, ErrDenied):
			status, code = http.StatusForbidden, "DENIED"
		case errors.Is(e, ErrConflict):
			status, code = http.StatusConflict, "STATE_CONFLICT"
		case errors.Is(e, ErrUnavailable):
			status, code = http.StatusServiceUnavailable, "UNAVAILABLE"
		}
		return write(status, map[string]any{"error": map[string]string{"code": code}})
	}
	switch {
	case r.Method == "GET" && path == "resources":
		out, e := s.Resources(r.Context(), p)
		if e != nil {
			return fail(e, "")
		}
		return write(http.StatusOK, map[string]any{"resources": out})
	case r.Method == "GET" && path == "credentials":
		out, e := s.Credentials(r.Context(), p)
		if e != nil {
			return fail(e, "")
		}
		return write(http.StatusOK, map[string]any{"credentials": out})
	case r.Method == "GET" && path == "revocations":
		out, e := s.Revocations(r.Context(), p)
		if e != nil {
			return fail(e, "")
		}
		return write(http.StatusOK, map[string]any{"revocations": out})
	case r.Method == "POST" && path == "revocations":
		var in struct {
			Kind     string `json:"kind"`
			TargetID string `json:"targetId"`
			Reason   string `json:"reason"`
		}
		if e := decodeConsoleBody(w, r, &in); e != nil {
			return fail(ErrInput, "")
		}
		out, e := s.Revoke(r.Context(), p, in.Kind, in.TargetID, in.Reason)
		if e != nil {
			return fail(e, "")
		}
		return write(http.StatusOK, map[string]any{"revocation": out})
	case r.Method == "GET" && path == "write-requests":
		out, e := s.WriteQueue(r.Context(), p)
		if e != nil {
			return fail(e, "")
		}
		return write(http.StatusOK, map[string]any{"writeRequests": out})
	case r.Method == "GET" && strings.HasPrefix(path, "write-requests/") && strings.HasSuffix(path, "/authorization-code"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "write-requests/"), "/authorization-code")
		code, expiry, e := s.Writes.ClaimHighRiskCode(r.Context(), p, id)
		if e != nil {
			return fail(e, "")
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		return write(http.StatusOK, map[string]any{"authorizationCode": code, "expiresAt": expiry})
	case r.Method == "GET" && strings.HasPrefix(path, "write-requests/") && strings.HasSuffix(path, "/evidence"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "write-requests/"), "/evidence")
		out, e := s.Writes.HighRiskEvidence(r.Context(), p, id)
		if e != nil {
			return fail(e, "")
		}
		return write(http.StatusOK, out)
	case r.Method == "POST" && strings.HasPrefix(path, "write-requests/"):
		rest := strings.TrimPrefix(path, "write-requests/")
		if !strings.HasSuffix(rest, "/decisions") {
			return write(http.StatusNotFound, map[string]any{"error": map[string]string{"code": "NOT_FOUND"}})
		}
		id := strings.TrimSuffix(rest, "/decisions")
		if strings.Contains(id, "/") {
			return write(http.StatusNotFound, map[string]any{"error": map[string]string{"code": "NOT_FOUND"}})
		}
		var in struct {
			Approve bool   `json:"approve"`
			Reason  string `json:"reason"`
		}
		if e := decodeConsoleBody(w, r, &in); e != nil {
			return fail(ErrInput, "")
		}
		out, e := s.DecideWrite(r.Context(), p, id, in.Approve, in.Reason)
		if e != nil {
			return fail(e, "")
		}
		return write(http.StatusOK, map[string]any{"writeRequest": out})
	}
	return write(http.StatusNotFound, map[string]any{"error": map[string]string{"code": "NOT_FOUND"}})
}

func decodeConsoleBody(w http.ResponseWriter, r *http.Request, into any) error {
	if r.Body == nil {
		return ErrInput
	}
	raw, e := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if e != nil || len(raw) == 0 {
		return ErrInput
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(into)
}
