package resources

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"strconv"
	"strings"
	"time"
)

const ContextHeader = "X-MCPGuard-Credential-Context"

type ContextProof struct{ id, secret string }

func (p ContextProof) ContextID() string { return p.id }
func (ContextProof) String() string      { return "[private credential context]" }

func ParseContextProof(raw string) (ContextProof, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return ContextProof{}, ErrDenied
	}
	in, _ := json.Marshal(v02.ContextIDInput{ContextID: parts[0]})
	var checked v02.ContextIDInput
	token, e := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if v02.Decode("ContextIDInput", in, &checked) != nil || e != nil || len(token) != 32 || base64.RawURLEncoding.EncodeToString(token) != parts[1] {
		return ContextProof{}, ErrDenied
	}
	return ContextProof{parts[0], parts[1]}, nil
}

type ContextService struct {
	Downloads                                *DownloadService
	TTL, Lifetime, MaxActivations, PageLimit int
}
type contextRecord struct {
	view          v02.CredentialContext
	authorization string
}

func NewContextService(d *DownloadService, ttl, lifetime, maxActivations, pageLimit int) (*ContextService, error) {
	if d == nil || d.Credentials.Access.Catalog.SchemaVersion < 7 || ttl < 1 || ttl > v02.MaxContextTtlSeconds || lifetime < ttl || lifetime > v02.MaxContextLifetimeSeconds || maxActivations < 1 || maxActivations > v02.MaxContextActivations || pageLimit < 1 || pageLimit > v02.MaxSyncPageSize {
		return nil, ErrInput
	}
	s := &ContextService{d, ttl, lifetime, maxActivations, pageLimit}
	d.contexts = s
	return s, nil
}
func (s *ContextService) audit(ctx context.Context, tx pgx.Tx, p metadata.Principal, client, contextID, rid, cid, did, event string) error {
	var x, r, c, d any
	if contextID != "" {
		x = contextID
	}
	if rid != "" {
		r = rid
	}
	if cid != "" {
		c = cid
	}
	if did != "" {
		d = did
	}
	_, e := tx.Exec(ctx, "INSERT INTO mcpguard_ra.context_audit(org_id,subject_id,client_id,context_id,resource_id,credential_id,download_id,event_type) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", p.OrgID, p.ID, client, x, r, c, d, event)
	if e != nil {
		return ErrUnavailable
	}
	return nil
}
func (s *ContextService) EnrollClient(ctx context.Context, p metadata.Principal, ttl int) (v02.ClientRegistration, v02.DeliveryAuthorizationInfo, string, error) {
	var client v02.ClientRegistration
	info, token, e := s.Downloads.EnrollDelivery(ctx, p, ttl)
	if e != nil {
		return client, info, "", e
	}
	tx, _, e := s.Downloads.Credentials.Access.begin(ctx, p)
	if e != nil {
		return client, info, "", e
	}
	defer rollback(tx)
	id := uuid.NewString()
	if _, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.credential_clients(org_id,client_id,subject_id,authorization_id,state,revision) VALUES($1,$2,$3,$4,'ACTIVE',1)", p.OrgID, id, p.ID, info.AuthorizationID); e != nil {
		return client, info, "", ErrUnavailable
	}
	if e = s.audit(ctx, tx, p, id, "", "", "", "", "CLIENT_ENROLLED"); e != nil {
		return client, info, "", e
	}
	if e = commit(ctx, tx); e != nil {
		return client, info, "", e
	}
	client = v02.ClientRegistration{ClientInstanceID: id, OrgID: p.OrgID, OwnerID: p.ID, AuthorizationID: info.AuthorizationID, State: "ACTIVE"}
	return client, info, token, nil
}
func (s *ContextService) Create(ctx context.Context, p metadata.Principal, deliveryToken string, in v02.ContextCreateInput) (v02.ContextPrivateResult, error) {
	var out v02.ContextPrivateResult
	if !valid("ContextCreateInput", in) {
		return out, ErrInput
	}
	tx, _, e := s.Downloads.Credentials.Access.begin(ctx, p)
	if e != nil {
		return out, e
	}
	defer rollback(tx)
	aid, e := s.Downloads.authority(ctx, tx, p, deliveryToken)
	if e != nil {
		return out, e
	}
	var client string
	var authExpiry, personExpiry time.Time
	e = tx.QueryRow(ctx, "SELECT c.client_id::text,a.expires_at,p.expires_at FROM mcpguard_ra.credential_clients c JOIN mcpguard_ra.delivery_authorizations a USING(org_id,authorization_id) JOIN mcpguard.principals p ON (p.org_id,p.id)=(c.org_id,c.subject_id) WHERE c.org_id=$1 AND c.subject_id=$2 AND c.authorization_id=$3 AND c.state='ACTIVE'", p.OrgID, p.ID, aid).Scan(&client, &authExpiry, &personExpiry)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, ErrDenied
	}
	if e != nil {
		return out, ErrUnavailable
	}
	payload, _ := json.Marshal(in)
	prior, e := s.command(ctx, tx, p, client, "CREATE", "", in.RequestKey, payload)
	if e != nil {
		return out, e
	}
	if prior != "" {
		return out, ErrConflict
	} // Secret creation responses are never replayed.
	now, e := databaseTime(ctx, tx)
	if e != nil {
		return out, ErrUnavailable
	}
	now = now.UTC().Truncate(time.Microsecond)
	rows, e := tx.Query(ctx, "SELECT context_id::text FROM mcpguard_ra.credential_contexts WHERE org_id=$1 AND client_id=$2 AND state='ACTIVE' FOR UPDATE", p.OrgID, client)
	if e != nil {
		return out, ErrUnavailable
	}
	var old []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		old = append(old, id)
	}
	rows.Close()
	for _, id := range old {
		var terminal string
		if e = tx.QueryRow(ctx, "UPDATE mcpguard_ra.credential_contexts SET state=CASE WHEN expires_at<=clock_timestamp() THEN 'EXPIRED' ELSE 'REVOKED' END,revision=revision+1 WHERE org_id=$1 AND context_id=$2 RETURNING state", p.OrgID, id).Scan(&terminal); e != nil {
			return out, ErrUnavailable
		}
		if e = s.audit(ctx, tx, p, client, id, "", "", "", "CONTEXT_"+terminal); e != nil {
			return out, e
		}
	}
	absolute := now.Add(time.Duration(s.Lifetime) * time.Second)
	if absolute.After(authExpiry) {
		absolute = authExpiry
	}
	if absolute.After(personExpiry) {
		absolute = personExpiry
	}
	expiry := now.Add(time.Duration(s.TTL) * time.Second)
	if expiry.After(absolute) {
		expiry = absolute
	}
	if !expiry.After(now) {
		return out, ErrDenied
	}
	random := make([]byte, 32)
	if _, e = rand.Read(random); e != nil {
		return out, ErrUnavailable
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	id := uuid.NewString()
	hash := s.Downloads.Credentials.Access.Catalog.Store.Vault.Hash([]byte("credential-context/" + token))
	if _, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.credential_contexts VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'ACTIVE',1)", p.OrgID, id, p.ID, client, p.TokenHash, hash, now, expiry, absolute); e != nil {
		return out, ErrUnavailable
	}
	if e = s.recordCommand(ctx, tx, p, client, "CREATE", "", in.RequestKey, payload, id); e != nil {
		return out, e
	}
	if e = s.audit(ctx, tx, p, client, id, "", "", "", "CONTEXT_CREATED"); e != nil {
		return out, e
	}
	out = v02.ContextPrivateResult{Context: v02.CredentialContext{ContextID: id, OrgID: p.OrgID, OwnerID: p.ID, ClientInstanceID: client, ExpiresAt: expiry.UTC().Format(time.RFC3339Nano), AbsoluteExpiresAt: absolute.UTC().Format(time.RFC3339Nano), Revision: "1", State: "ACTIVE"}, ContextToken: token}
	return out, commit(ctx, tx)
}
func (s *ContextService) current(ctx context.Context, tx pgx.Tx, p metadata.Principal, proof ContextProof) (contextRecord, error) {
	var out contextRecord
	if proof.id == "" || proof.secret == "" {
		return out, ErrDenied
	}
	var expiry, absolute time.Time
	e := tx.QueryRow(ctx, "SELECT context_id::text,client_id::text,expires_at,absolute_expires_at,state,revision::text FROM mcpguard_ra.credential_contexts WHERE org_id=$1 AND subject_id=$2 AND context_id=$3 AND verifier_hash=$4 FOR UPDATE", p.OrgID, p.ID, proof.id, s.Downloads.Credentials.Access.Catalog.Store.Vault.Hash([]byte("credential-context/"+proof.secret))).Scan(&out.view.ContextID, &out.view.ClientInstanceID, &expiry, &absolute, &out.view.State, &out.view.Revision)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, ErrDenied
	}
	if e != nil {
		return out, ErrUnavailable
	}
	out.view.OrgID = p.OrgID
	out.view.OwnerID = p.ID
	out.view.ExpiresAt = expiry.UTC().Format(time.RFC3339Nano)
	out.view.AbsoluteExpiresAt = absolute.UTC().Format(time.RFC3339Nano)
	var authorized bool
	e = tx.QueryRow(ctx, `SELECT c.authorization_id::text,c.state='ACTIVE' AND a.state='ACTIVE' AND a.expires_at>clock_timestamp() AND a.principal_token_hash=$3 AND x.principal_token_hash=$3 AND a.subject_id=$2 AND a.expires_at>=x.absolute_expires_at AND p.expires_at>=x.absolute_expires_at FROM mcpguard_ra.credential_clients c JOIN mcpguard_ra.delivery_authorizations a USING(org_id,authorization_id) JOIN mcpguard_ra.credential_contexts x USING(org_id,client_id) JOIN mcpguard.principals p ON (p.org_id,p.id)=(c.org_id,c.subject_id) WHERE c.org_id=$1 AND c.subject_id=$2 AND x.context_id=$4`, p.OrgID, p.ID, p.TokenHash, proof.id).Scan(&out.authorization, &authorized)
	if e != nil {
		return out, ErrUnavailable
	}
	now, e := databaseTime(ctx, tx)
	if e != nil {
		return out, ErrUnavailable
	}
	state := ""
	if out.view.State == "ACTIVE" {
		if !expiry.After(now) {
			state = "EXPIRED"
		} else if !authorized {
			state = "REVOKED"
		}
	}
	if state != "" {
		if _, e = tx.Exec(ctx, "UPDATE mcpguard_ra.credential_contexts SET state=$3,revision=revision+1 WHERE org_id=$1 AND context_id=$2", p.OrgID, proof.id, state); e != nil {
			return out, ErrUnavailable
		}
		out.view.State = state
		out.view.Revision = strconv.FormatInt(number(out.view.Revision)+1, 10)
		if e = s.audit(ctx, tx, p, out.view.ClientInstanceID, proof.id, "", "", "", "CONTEXT_"+state); e != nil {
			return out, e
		}
	}
	return out, nil
}
func (s *ContextService) begin(ctx context.Context, p metadata.Principal, proof ContextProof, active bool) (pgx.Tx, contextRecord, error) {
	tx, _, e := s.Downloads.Credentials.Access.begin(ctx, p)
	if e != nil {
		return nil, contextRecord{}, e
	}
	row, e := s.current(ctx, tx, p, proof)
	if e != nil {
		rollback(tx)
		return nil, row, e
	}
	if active && row.view.State != "ACTIVE" {
		e = commit(ctx, tx)
		if e == nil {
			e = ErrDenied
		}
		return nil, row, e
	}
	return tx, row, nil
}
func (s *ContextService) VerifyDelivery(ctx context.Context, p metadata.Principal, proof ContextProof, token string) error {
	// A valid owner can inspect a terminal Context's safe status. Individual
	// queue, activation, claim and renewal operations still require ACTIVE.
	tx, row, e := s.begin(ctx, p, proof, false)
	if e != nil {
		return e
	}
	defer rollback(tx)
	aid, e := s.Downloads.authority(ctx, tx, p, token)
	if e != nil {
		return e
	}
	if aid != row.authorization {
		return ErrDenied
	}
	return commit(ctx, tx)
}
func (s *ContextService) command(ctx context.Context, tx pgx.Tx, p metadata.Principal, client, operation, target, key string, payload []byte) (string, error) {
	var id, hash string
	vault := s.Downloads.Credentials.Access.Catalog.Store.Vault
	e := tx.QueryRow(ctx, "SELECT context_id::text,payload_hash FROM mcpguard_ra.context_commands WHERE org_id=$1 AND subject_id=$2 AND client_id=$3 AND command=$4 AND target=$5 AND request_hash=$6", p.OrgID, p.ID, client, operation, target, vault.Hash([]byte("context-request/"+key))).Scan(&id, &hash)
	if errors.Is(e, pgx.ErrNoRows) {
		return "", nil
	}
	if e != nil {
		return "", ErrUnavailable
	}
	if hash != vault.Hash(payload) {
		return "", ErrIdempotency
	}
	return id, nil
}
func (s *ContextService) recordCommand(ctx context.Context, tx pgx.Tx, p metadata.Principal, client, operation, target, key string, payload []byte, id string) error {
	vault := s.Downloads.Credentials.Access.Catalog.Store.Vault
	_, e := tx.Exec(ctx, "INSERT INTO mcpguard_ra.context_commands VALUES($1,$2,$3,$4,$5,$6,$7,$8)", p.OrgID, p.ID, client, operation, target, vault.Hash([]byte("context-request/"+key)), vault.Hash(payload), id)
	if e != nil {
		return ErrUnavailable
	}
	return nil
}
func (s *ContextService) Renew(ctx context.Context, p metadata.Principal, proof ContextProof, in v02.ContextRenewInput, closeContext bool) (v02.CredentialContext, error) {
	var out v02.CredentialContext
	if !valid("ContextRenewInput", in) {
		return out, ErrInput
	}
	tx, row, e := s.begin(ctx, p, proof, !closeContext)
	if e != nil {
		return out, e
	}
	defer rollback(tx)
	out = row.view
	op := "RENEW"
	if closeContext {
		op = "CLOSE"
	}
	payload, _ := json.Marshal(in)
	prior, e := s.command(ctx, tx, p, out.ClientInstanceID, op, out.ContextID, in.RequestKey, payload)
	if e != nil {
		return out, e
	}
	if prior != "" {
		return out, commit(ctx, tx)
	}
	if out.Revision != in.ExpectedRevision {
		return out, ErrConflict
	}
	if out.State == "ACTIVE" {
		if closeContext {
			out.State = "REVOKED"
		} else {
			now, e := databaseTime(ctx, tx)
			if e != nil {
				return out, ErrUnavailable
			}
			expiry := now.Add(time.Duration(s.TTL) * time.Second).UTC().Truncate(time.Microsecond)
			absolute, _, _ := normalizedTime(out.AbsoluteExpiresAt)
			old, _, _ := normalizedTime(out.ExpiresAt)
			if expiry.After(absolute) {
				expiry = absolute
			}
			if !expiry.After(old) {
				return out, ErrConflict
			}
			out.ExpiresAt = expiry.Format(time.RFC3339Nano)
		}
		if _, e = tx.Exec(ctx, "UPDATE mcpguard_ra.credential_contexts SET state=$3,expires_at=$4,revision=revision+1 WHERE org_id=$1 AND context_id=$2", p.OrgID, out.ContextID, out.State, out.ExpiresAt); e != nil {
			return out, ErrUnavailable
		}
		out.Revision = strconv.FormatInt(number(out.Revision)+1, 10)
		event := "CONTEXT_RENEWED"
		if closeContext {
			event = "CONTEXT_REVOKED"
		}
		if e = s.audit(ctx, tx, p, out.ClientInstanceID, out.ContextID, "", "", "", event); e != nil {
			return out, e
		}
	}
	if e = s.recordCommand(ctx, tx, p, out.ClientInstanceID, op, out.ContextID, in.RequestKey, payload, out.ContextID); e != nil {
		return out, e
	}
	return out, commit(ctx, tx)
}
func (s *ContextService) activation(ctx context.Context, tx pgx.Tx, p metadata.Principal, id, rid string) (*v02.ContextActivation, error) {
	var raw []byte
	e := tx.QueryRow(ctx, "SELECT v.definition FROM mcpguard_ra.context_activation_heads h JOIN mcpguard_ra.context_activation_versions v USING(org_id,context_id,resource_id,revision) WHERE h.org_id=$1 AND h.context_id=$2 AND h.resource_id=$3 FOR UPDATE OF h", p.OrgID, id, rid).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, ErrUnavailable
	}
	var out v02.ContextActivation
	if v02.Decode("ContextActivation", raw, &out) != nil {
		return nil, ErrUnavailable
	}
	return &out, nil
}
func (s *ContextService) putActivation(ctx context.Context, tx pgx.Tx, p metadata.Principal, client string, out *v02.ContextActivation, event string) error {
	raw, _ := json.Marshal(out)
	_, e := tx.Exec(ctx, "INSERT INTO mcpguard_ra.context_activation_versions VALUES($1,$2,$3,$4,$5,$6,$7,$8)", p.OrgID, out.ContextID, out.ResourceID, out.Revision, out.CredentialID, out.LeaseID, out.State, raw)
	if e != nil {
		return ErrUnavailable
	}
	if out.Revision == "1" {
		_, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.context_activation_heads VALUES($1,$2,$3,$4)", p.OrgID, out.ContextID, out.ResourceID, out.Revision)
	} else {
		var n pgconn.CommandTag
		n, e = tx.Exec(ctx, "UPDATE mcpguard_ra.context_activation_heads SET revision=$4 WHERE org_id=$1 AND context_id=$2 AND resource_id=$3 AND revision=$5", p.OrgID, out.ContextID, out.ResourceID, out.Revision, number(out.Revision)-1)
		if e == nil && n.RowsAffected() != 1 {
			e = ErrConflict
		}
	}
	if e != nil {
		return ErrUnavailable
	}
	return s.audit(ctx, tx, p, client, out.ContextID, out.ResourceID, out.CredentialID, "", event)
}
func (s *ContextService) refreshActivation(ctx context.Context, tx pgx.Tx, p metadata.Principal, row contextRecord, a *v02.ContextActivation) error {
	if a.State != "ACTIVE" {
		return nil
	}
	state := ""
	if row.view.State != "ACTIVE" {
		state = row.view.State
	} else {
		credential, e := s.Downloads.Credentials.row(ctx, tx, p, a.CredentialID)
		if e != nil {
			return e
		}
		if e = s.Downloads.Credentials.refresh(ctx, tx, p, credential); e != nil {
			return e
		}
		if credential.state != "ACTIVE" {
			state = credential.state
		}
	}
	if state != "" {
		a.State = state
		a.Revision = strconv.FormatInt(number(a.Revision)+1, 10)
		return s.putActivation(ctx, tx, p, row.view.ClientInstanceID, a, "ACTIVATION_"+state)
	}
	return nil
}
func (s *ContextService) Status(ctx context.Context, p metadata.Principal, proof ContextProof) (v02.ContextStatusResult, error) {
	var out v02.ContextStatusResult
	tx, row, e := s.begin(ctx, p, proof, false)
	if e != nil {
		return out, e
	}
	defer rollback(tx)
	out.Context = row.view
	out.Activations = []v02.ContextActivation{}
	rows, e := tx.Query(ctx, "SELECT resource_id::text FROM mcpguard_ra.context_activation_heads WHERE org_id=$1 AND context_id=$2 ORDER BY resource_id LIMIT $3", p.OrgID, proof.id, s.MaxActivations+1)
	if e != nil {
		return out, ErrUnavailable
	}
	var ids []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) > s.MaxActivations {
		return out, ErrUnavailable
	}
	for _, id := range ids {
		a, e := s.activation(ctx, tx, p, proof.id, id)
		if e != nil {
			return out, e
		}
		if a != nil {
			if e = s.refreshActivation(ctx, tx, p, row, a); e != nil {
				return out, e
			}
			out.Activations = append(out.Activations, *a)
		}
	}
	return out, commit(ctx, tx)
}
func (s *ContextService) Activate(ctx context.Context, p metadata.Principal, proof ContextProof, in v02.ActivationInput) (v02.ContextActivation, error) {
	var out v02.ContextActivation
	if !valid("ActivationInput", in) {
		return out, ErrInput
	}
	tx, _, e := s.begin(ctx, p, proof, true)
	if e != nil {
		return out, e
	}
	defer rollback(tx)
	if e = commit(ctx, tx); e != nil {
		return out, e
	}
	blob, _ := json.Marshal(in.CredentialPackage)
	if _, e = s.Downloads.Credentials.Validate(ctx, p, in.ResourceID, in.CredentialID, blob); e != nil {
		if errors.Is(e, ErrCredential) {
			return out, ErrDenied
		}
		return out, e
	}
	final, current, e := s.begin(ctx, p, proof, true)
	if e != nil {
		return out, e
	}
	defer rollback(final)
	credential, e := s.Downloads.Credentials.row(ctx, final, p, in.CredentialID)
	if e != nil {
		return out, e
	}
	if e = s.Downloads.Credentials.refresh(ctx, final, p, credential); e != nil {
		return out, e
	}
	if credential.state != "ACTIVE" || credential.ref.ResourceID != in.ResourceID {
		if e = commit(ctx, final); e != nil {
			return out, e
		}
		return out, ErrDenied
	}
	if in.DownloadID != nil {
		ticket, e := s.Downloads.ticket(ctx, final, p, *in.DownloadID)
		if e != nil {
			return out, e
		}
		if ticket.State != "CLAIMED" || ticket.CredentialID != in.CredentialID || ticket.ClientInstanceID == nil || *ticket.ClientInstanceID != current.view.ClientInstanceID {
			return out, ErrDenied
		}
	}
	old, e := s.activation(ctx, final, p, proof.id, in.ResourceID)
	if e != nil {
		return out, e
	}
	payload, _ := json.Marshal(in)
	prior, e := s.command(ctx, final, p, current.view.ClientInstanceID, "ACTIVATE", proof.id+"/"+in.ResourceID, in.RequestKey, payload)
	if e != nil {
		return out, e
	}
	if prior != "" {
		if old == nil || old.CredentialID != in.CredentialID {
			return out, ErrConflict
		}
		return *old, commit(ctx, final)
	}
	if old != nil && old.State == "ACTIVE" && old.CredentialID == in.CredentialID {
		out = *old
	} else {
		revision := int64(1)
		if old != nil {
			revision = number(old.Revision) + 1
		} else {
			var count int
			if final.QueryRow(ctx, "SELECT count(*) FROM mcpguard_ra.context_activation_heads WHERE org_id=$1 AND context_id=$2", p.OrgID, proof.id).Scan(&count) != nil {
				return out, ErrUnavailable
			}
			if count >= s.MaxActivations {
				return out, ErrConflict
			}
		}
		now, e := databaseTime(ctx, final)
		if e != nil {
			return out, ErrUnavailable
		}
		ref := credential.ref
		out = v02.ContextActivation{ContextID: proof.id, CredentialID: in.CredentialID, ResourceID: in.ResourceID, LeaseID: ref.LeaseID, ExpiresAt: ref.ExpiresAt, ActivatedAt: now.UTC().Format(time.RFC3339Nano), State: "ACTIVE", Revision: strconv.FormatInt(revision, 10), ResourceVersion: ref.ResourceVersion, GrantVersion: ref.GrantVersion, BindingVersion: ref.BindingVersion}
		if e = s.putActivation(ctx, final, p, current.view.ClientInstanceID, &out, "ACTIVATED"); e != nil {
			return out, e
		}
	}
	if in.DownloadID != nil {
		if e = s.receipt(ctx, final, p, current, *in.DownloadID, "ACTIVATED", ""); e != nil {
			return out, e
		}
	}
	if e = s.recordCommand(ctx, final, p, current.view.ClientInstanceID, "ACTIVATE", proof.id+"/"+in.ResourceID, in.RequestKey, payload, proof.id); e != nil {
		return out, e
	}
	return out, commit(ctx, final)
}
func (s *ContextService) Deactivate(ctx context.Context, p metadata.Principal, proof ContextProof, in v02.DeactivateInput) (v02.ContextActivation, error) {
	var out v02.ContextActivation
	if !valid("DeactivateInput", in) {
		return out, ErrInput
	}
	tx, row, e := s.begin(ctx, p, proof, true)
	if e != nil {
		return out, e
	}
	defer rollback(tx)
	a, e := s.activation(ctx, tx, p, proof.id, in.ResourceID)
	if e != nil {
		return out, e
	}
	if a == nil {
		return out, ErrDenied
	}
	payload, _ := json.Marshal(in)
	prior, e := s.command(ctx, tx, p, row.view.ClientInstanceID, "DEACTIVATE", proof.id+"/"+in.ResourceID, in.RequestKey, payload)
	if e != nil {
		return out, e
	}
	if prior != "" {
		return *a, commit(ctx, tx)
	}
	if a.State == "ACTIVE" {
		a.State = "REVOKED"
		a.Revision = strconv.FormatInt(number(a.Revision)+1, 10)
		if e = s.putActivation(ctx, tx, p, row.view.ClientInstanceID, a, "CLIENT_DEACTIVATED"); e != nil {
			return out, e
		}
	}
	if e = s.recordCommand(ctx, tx, p, row.view.ClientInstanceID, "DEACTIVATE", proof.id+"/"+in.ResourceID, in.RequestKey, payload, proof.id); e != nil {
		return out, e
	}
	return *a, commit(ctx, tx)
}

// CheckActivation is a server admission check, not an execution ticket. RA-11
// must recheck at dispatch and must not treat a cached status as authority.
func (s *ContextService) CheckActivation(ctx context.Context, p metadata.Principal, proof ContextProof, rid, cid string) (v02.ContextActivation, error) {
	var out v02.ContextActivation
	tx, row, e := s.begin(ctx, p, proof, true)
	if e != nil {
		return out, e
	}
	defer rollback(tx)
	a, e := s.activation(ctx, tx, p, proof.id, rid)
	if e != nil {
		return out, e
	}
	if a == nil || a.CredentialID != cid {
		return out, ErrDenied
	}
	if e = s.refreshActivation(ctx, tx, p, row, a); e != nil {
		return out, e
	}
	if a.State != "ACTIVE" {
		if e = commit(ctx, tx); e != nil {
			return out, e
		}
		return out, ErrDenied
	}
	return *a, commit(ctx, tx)
}
