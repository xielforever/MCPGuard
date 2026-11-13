package resources

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

// DownloadService is server-only. Prepare returns safe metadata; only Deliver
// may pass encrypted bytes to a private HTTP response writer. No MCP claim tool.
type DownloadService struct {
	Credentials         *CredentialService
	TicketTTL, MaxReady int
	contexts            *ContextService
}

func NewDownloadService(c *CredentialService, ttl, maxReady int) (*DownloadService, error) {
	if c == nil || c.Access.Catalog.SchemaVersion < 6 || ttl < 1 || ttl > v02.MaxDownloadTtlSeconds || maxReady < 1 || maxReady > v02.MaxReadyDownloadTickets {
		return nil, ErrInput
	}
	return &DownloadService{Credentials: c, TicketTTL: ttl, MaxReady: maxReady}, nil
}
func (s *DownloadService) audit(ctx context.Context, tx pgx.Tx, p metadata.Principal, did, cid, aid, event string) error {
	var d, c, a any
	if did != "" {
		d = did
	}
	if cid != "" {
		c = cid
	}
	if aid != "" {
		a = aid
	}
	_, e := tx.Exec(ctx, "INSERT INTO mcpguard_ra.download_audit(org_id,subject_id,download_id,credential_id,authorization_id,event_type) VALUES($1,$2,$3,$4,$5,$6)", p.OrgID, p.ID, d, c, a, event)
	if e != nil {
		return ErrUnavailable
	}
	return nil
}
func (s *DownloadService) ticket(ctx context.Context, tx pgx.Tx, p metadata.Principal, id string) (v02.DownloadTicket, error) {
	var out v02.DownloadTicket
	var expiry, credentialExpiry time.Time
	clientColumn := "NULL::text"
	if s.Credentials.Access.Catalog.SchemaVersion >= 7 {
		clientColumn = "d.client_id::text"
	}
	e := tx.QueryRow(ctx, `SELECT d.download_id::text,d.credential_id::text,d.lease_id::text,d.subject_id::text,d.expires_at,l.expires_at,d.state,d.revision::text,CASE WHEN d.state='CLAIMED' THEN coalesce((SELECT CASE a.event_type WHEN 'DELIVERY_WRITE_COMPLETED' THEN 'WRITE_COMPLETED' ELSE 'WRITE_INTERRUPTED' END FROM mcpguard_ra.download_audit a WHERE a.org_id=d.org_id AND a.download_id=d.download_id AND a.event_type IN ('DELIVERY_WRITE_COMPLETED','DELIVERY_WRITE_INTERRUPTED') ORDER BY a.seq DESC LIMIT 1),'UNKNOWN') ELSE 'NOT_STARTED' END,`+clientColumn+`,l.resource_id::text,l.grant_id::text FROM mcpguard_ra.download_tickets d JOIN mcpguard_ra.credential_leases l ON (l.org_id,l.lease_id,l.credential_id)=(d.org_id,d.lease_id,d.credential_id) WHERE d.org_id=$1 AND d.subject_id=$2 AND d.download_id=$3 FOR UPDATE OF d`, p.OrgID, p.ID, id).Scan(&out.DownloadID, &out.CredentialID, &out.LeaseID, &out.OwnerID, &expiry, &credentialExpiry, &out.State, &out.Revision, &out.DeliveryOutcome, &out.ClientInstanceID, &out.ResourceID, &out.GrantID)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, ErrDenied
	}
	if e != nil {
		return out, ErrUnavailable
	}
	out.ExpiresAt = expiry.UTC().Format(time.RFC3339Nano)
	out.CredentialExpiresAt = credentialExpiry.UTC().Format(time.RFC3339Nano)
	out.DeliveryMode = "MANUAL_OWNER"
	if out.ClientInstanceID != nil {
		out.DeliveryMode = "ENROLLED_CLIENT"
	} else {
		// Preserve the RA-08 manual response shape and value equality.
		out.ResourceID = nil
		out.GrantID = nil
	}
	return out, nil
}
func prepared(t v02.DownloadTicket) v02.PrepareDownloadResult {
	return v02.PrepareDownloadResult{CredentialID: t.CredentialID, LeaseID: t.LeaseID, DownloadID: t.DownloadID, ExpiresAt: t.ExpiresAt, CredentialExpiresAt: t.CredentialExpiresAt, State: t.State, Revision: t.Revision, DeliveryMode: t.DeliveryMode, DeliveryOutcome: t.DeliveryOutcome, ClientInstanceID: t.ClientInstanceID, ResourceID: t.ResourceID, GrantID: t.GrantID}
}
func (s *DownloadService) transition(ctx context.Context, tx pgx.Tx, p metadata.Principal, t *v02.DownloadTicket, state, aid string) error {
	var auth any
	if aid != "" {
		auth = aid
	}
	_, e := tx.Exec(ctx, "UPDATE mcpguard_ra.download_tickets SET state=$3,revision=revision+1,claimed_authorization_id=$4,claimed_at=CASE WHEN $3='CLAIMED' THEN clock_timestamp() ELSE NULL END WHERE org_id=$1 AND download_id=$2", p.OrgID, t.DownloadID, state, auth)
	if e != nil {
		return ErrUnavailable
	}
	event := "TICKET_" + state
	if state == "CLAIMED" {
		event = "CLAIM_COMMITTED"
	}
	if e = s.audit(ctx, tx, p, t.DownloadID, t.CredentialID, aid, event); e != nil {
		return e
	}
	t.State = state
	t.Revision = strconv.FormatInt(number(t.Revision)+1, 10)
	if state == "CLAIMED" {
		t.DeliveryOutcome = "UNKNOWN"
	}
	return nil
}
func (s *DownloadService) refresh(ctx context.Context, tx pgx.Tx, p metadata.Principal, t *v02.DownloadTicket) error {
	if t.State != "READY" {
		return nil
	}
	now, e := databaseTime(ctx, tx)
	if e != nil {
		return ErrUnavailable
	}
	expiry, _, _ := normalizedTime(t.ExpiresAt)
	if !expiry.After(now) {
		return s.transition(ctx, tx, p, t, "EXPIRED", "")
	}
	row, e := s.Credentials.row(ctx, tx, p, t.CredentialID)
	if e != nil {
		return e
	}
	if e = s.Credentials.refresh(ctx, tx, p, row); e != nil {
		return e
	}
	if row.state != "ACTIVE" {
		return s.transition(ctx, tx, p, t, "CANCELLED", "")
	}
	return nil
}
func (s *DownloadService) prior(ctx context.Context, tx pgx.Tx, p metadata.Principal, in v02.PrepareDownloadInput, client string) (*v02.PrepareDownloadResult, error) {
	var did, hash string
	e := tx.QueryRow(ctx, "SELECT download_id::text,payload_hash FROM mcpguard_ra.download_commands WHERE org_id=$1 AND subject_id=$2 AND request_key=$3", p.OrgID, p.ID, s.requestHash(in.RequestKey)).Scan(&did, &hash)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, ErrUnavailable
	}
	payload, _ := json.Marshal(in)
	if !hmac.Equal([]byte(hash), []byte(s.Credentials.Access.Catalog.Store.Vault.Hash(payload))) {
		return nil, ErrIdempotency
	}
	t, e := s.ticket(ctx, tx, p, did)
	if e != nil {
		return nil, e
	}
	if client == "" && t.ClientInstanceID != nil || client != "" && (t.ClientInstanceID == nil || *t.ClientInstanceID != client) {
		return nil, ErrIdempotency
	}
	if e = s.refresh(ctx, tx, p, &t); e != nil {
		return nil, e
	}
	out := prepared(t)
	return &out, nil
}
func (s *DownloadService) requestHash(key string) string {
	return s.Credentials.Access.Catalog.Store.Vault.Hash([]byte("download-request/" + key))
}
func (s *DownloadService) Prepare(ctx context.Context, p metadata.Principal, in v02.PrepareDownloadInput) (v02.PrepareDownloadResult, error) {
	return s.prepare(ctx, p, in, nil)
}
func (s *DownloadService) binding(ctx context.Context, tx pgx.Tx, p metadata.Principal, proof *ContextProof) (contextRecord, error) {
	if proof == nil {
		return contextRecord{}, nil
	}
	if s.contexts == nil {
		return contextRecord{}, ErrDenied
	}
	row, e := s.contexts.current(ctx, tx, p, *proof)
	if e != nil {
		return row, e
	}
	if row.view.State != "ACTIVE" {
		if e = commit(ctx, tx); e != nil {
			return row, e
		}
		return row, ErrDenied
	}
	return row, nil
}
func (s *DownloadService) prepare(ctx context.Context, p metadata.Principal, in v02.PrepareDownloadInput, proof *ContextProof) (v02.PrepareDownloadResult, error) {
	var out v02.PrepareDownloadResult
	if !valid("PrepareDownloadInput", in) {
		return out, ErrInput
	}
	tx, _, e := s.Credentials.Access.begin(ctx, p)
	if e != nil {
		return out, e
	}
	defer rollback(tx)
	binding, e := s.binding(ctx, tx, p, proof)
	if e != nil {
		return out, e
	}
	prior, e := s.prior(ctx, tx, p, in, binding.view.ClientInstanceID)
	if e != nil {
		return out, e
	}
	if prior != nil {
		return *prior, commit(ctx, tx)
	}
	if _, e = s.Credentials.selectGrant(ctx, tx, p, in.GrantID); e != nil {
		return out, e
	}
	// Reject an already-full owner queue before reserving or encrypting. The
	// final transaction repeats this count to cover concurrent preparations.
	var pending int
	if tx.QueryRow(ctx, "SELECT count(*) FROM mcpguard_ra.download_tickets WHERE org_id=$1 AND subject_id=$2 AND state='READY' AND expires_at>clock_timestamp()", p.OrgID, p.ID).Scan(&pending) != nil {
		return out, ErrUnavailable
	}
	if pending >= s.MaxReady {
		return out, ErrConflict
	}
	if e = commit(ctx, tx); e != nil {
		return out, e
	}
	// Stable server-side issue key: a new ticket never creates a new package,
	// lease, grant or native account. Renewal of terminal packages is RA-09.
	issue := v02.IssueCredentialInput{GrantID: in.GrantID, RequestKey: "ra08_" + s.Credentials.Access.Catalog.Store.Vault.Hash([]byte(p.OrgID+"/"+p.ID+"/"+in.GrantID))}
	prep, existing, e := s.Credentials.prepareIssue(ctx, p, issue)
	if e != nil {
		return out, e
	}
	var blob []byte
	if prep != nil {
		blob, e = s.Credentials.encryptPreparation(ctx, p, prep)
		if e != nil {
			return out, e
		}
	}
	final, _, e := s.Credentials.Access.begin(ctx, p)
	if e != nil {
		return out, e
	}
	defer rollback(final)
	binding, e = s.binding(ctx, final, p, proof)
	if e != nil {
		return out, e
	}
	prior, e = s.prior(ctx, final, p, in, binding.view.ClientInstanceID)
	if e != nil {
		return out, e
	}
	if prior != nil {
		return *prior, commit(ctx, final)
	}
	var status v02.CredentialStatus
	if prep != nil {
		status, e = s.Credentials.issueWithin(ctx, final, p, issue, prep, blob)
	} else if existing != nil {
		var row *credentialRow
		row, e = s.Credentials.row(ctx, final, p, existing.CredentialID)
		if e == nil {
			e = s.Credentials.refresh(ctx, final, p, row)
		}
		if e == nil {
			status = statusOf(row)
		}
	} else {
		e = ErrUnavailable
	}
	if e != nil {
		return out, e
	}
	if status.State != "ACTIVE" {
		if e = commit(ctx, final); e != nil {
			return out, e
		}
		return out, ErrDenied
	}
	out, e = s.insertTicket(ctx, final, p, in, status, binding.view.ClientInstanceID)
	if e != nil {
		return out, e
	}
	return out, commit(ctx, final)
}
func (s *DownloadService) insertTicket(ctx context.Context, final pgx.Tx, p metadata.Principal, in v02.PrepareDownloadInput, status v02.CredentialStatus, client string) (v02.PrepareDownloadResult, error) {
	var out v02.PrepareDownloadResult
	now, e := databaseTime(ctx, final)
	if e != nil {
		return out, ErrUnavailable
	}
	now = now.UTC().Truncate(time.Microsecond)
	var ready int
	if final.QueryRow(ctx, "SELECT count(*) FROM mcpguard_ra.download_tickets WHERE org_id=$1 AND subject_id=$2 AND state='READY' AND expires_at>$3", p.OrgID, p.ID, now).Scan(&ready) != nil {
		return out, ErrUnavailable
	}
	if ready >= s.MaxReady {
		return out, ErrConflict
	}
	expiry := now.Add(time.Duration(s.TicketTTL) * time.Second)
	leaseExpiry, _, _ := normalizedTime(status.ExpiresAt)
	if expiry.After(leaseExpiry) {
		expiry = leaseExpiry
	}
	if !expiry.After(now) {
		return out, ErrDenied
	}
	did := uuid.NewString()
	query := "INSERT INTO mcpguard_ra.download_tickets(org_id,download_id,subject_id,credential_id,lease_id,issued_at,expires_at,state,revision) VALUES($1,$2,$3,$4,$5,$6,$7,'READY',1)"
	args := []any{p.OrgID, did, p.ID, status.CredentialID, status.LeaseID, now, expiry}
	if client != "" {
		query = "INSERT INTO mcpguard_ra.download_tickets(org_id,download_id,subject_id,credential_id,lease_id,issued_at,expires_at,state,revision,client_id) VALUES($1,$2,$3,$4,$5,$6,$7,'READY',1,$8)"
		args = append(args, client)
	}
	_, e = final.Exec(ctx, query, args...)
	if e != nil {
		return out, ErrUnavailable
	}
	payload, _ := json.Marshal(in)
	_, e = final.Exec(ctx, "INSERT INTO mcpguard_ra.download_commands VALUES($1,$2,$3,$4,$5)", p.OrgID, p.ID, s.requestHash(in.RequestKey), s.Credentials.Access.Catalog.Store.Vault.Hash(payload), did)
	if e != nil {
		return out, ErrUnavailable
	}
	if e = s.audit(ctx, final, p, did, status.CredentialID, "", "TICKET_READY"); e != nil {
		return out, e
	}
	ticket, e := s.ticket(ctx, final, p, did)
	if e != nil {
		return out, e
	}
	return prepared(ticket), nil
}

// EnrollDelivery is an administrative server operation, never an HTTP/MCP
// endpoint. The caller must store the returned token through a private channel.
func (s *DownloadService) EnrollDelivery(ctx context.Context, p metadata.Principal, ttl int) (v02.DeliveryAuthorizationInfo, string, error) {
	var info v02.DeliveryAuthorizationInfo
	if ttl < 1 || ttl > v02.MaxDeliveryAuthTtlSeconds {
		return info, "", ErrInput
	}
	tx, _, e := s.Credentials.Access.begin(ctx, p)
	if e != nil {
		return info, "", e
	}
	defer rollback(tx)
	now, e := databaseTime(ctx, tx)
	if e != nil {
		return info, "", ErrUnavailable
	}
	now = now.UTC().Truncate(time.Microsecond)
	expiry := now.Add(time.Duration(ttl) * time.Second)
	if expiry.After(p.ExpiresAt) {
		expiry = p.ExpiresAt.UTC().Truncate(time.Microsecond)
	}
	if !expiry.After(now) {
		return info, "", ErrDenied
	}
	random := make([]byte, 32)
	if _, e = rand.Read(random); e != nil {
		return info, "", ErrUnavailable
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	aid := uuid.NewString()
	hash := s.Credentials.Access.Catalog.Store.Vault.Hash([]byte("private-delivery/" + token))
	_, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.delivery_authorizations VALUES($1,$2,$3,$4,$5,'mcpguard-delivery','credential:claim',$6,$7,'ACTIVE',1)", p.OrgID, aid, p.ID, p.TokenHash, hash, now, expiry)
	if e != nil {
		return info, "", ErrUnavailable
	}
	if e = s.audit(ctx, tx, p, "", "", aid, "AUTHORIZATION_ENROLLED"); e != nil {
		return info, "", e
	}
	if e = commit(ctx, tx); e != nil {
		return info, "", e
	}
	info = v02.DeliveryAuthorizationInfo{AuthorizationID: aid, OwnerID: p.ID, ExpiresAt: expiry.Format(time.RFC3339Nano), Audience: "mcpguard-delivery", Scope: "credential:claim"}
	return info, token, nil
}
func (s *DownloadService) authority(ctx context.Context, tx pgx.Tx, p metadata.Principal, token string) (string, error) {
	raw, e := base64.RawURLEncoding.Strict().DecodeString(token)
	if e != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != token {
		return "", ErrDenied
	}
	var id string
	e = tx.QueryRow(ctx, "SELECT authorization_id::text FROM mcpguard_ra.delivery_authorizations WHERE org_id=$1 AND subject_id=$2 AND principal_token_hash=$3 AND verifier_hash=$4 AND audience='mcpguard-delivery' AND scope='credential:claim' AND state='ACTIVE' AND expires_at>clock_timestamp()", p.OrgID, p.ID, p.TokenHash, s.Credentials.Access.Catalog.Store.Vault.Hash([]byte("private-delivery/"+token))).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return "", ErrDenied
	}
	if e != nil {
		return "", ErrUnavailable
	}
	return id, nil
}
func (s *DownloadService) Status(ctx context.Context, p metadata.Principal, token string, in v02.DownloadIDInput) (v02.DownloadTicket, error) {
	var out v02.DownloadTicket
	if !valid("DownloadIDInput", in) {
		return out, ErrInput
	}
	tx, _, e := s.Credentials.Access.begin(ctx, p)
	if e != nil {
		return out, e
	}
	defer rollback(tx)
	if _, e = s.authority(ctx, tx, p, token); e != nil {
		return out, e
	}
	out, e = s.ticket(ctx, tx, p, in.DownloadID)
	if e != nil {
		return out, e
	}
	if out.ClientInstanceID != nil {
		return out, ErrDenied
	}
	if e = s.refresh(ctx, tx, p, &out); e != nil {
		return out, e
	}
	return out, commit(ctx, tx)
}

// Deliver consumes once before invoking send. A successful send means the
// server's writer accepted bytes, never that the client saved or activated them.
// Request cancellation or post-write audit failure cannot make a ticket READY.
func (s *DownloadService) Deliver(ctx context.Context, p metadata.Principal, token string, in v02.DownloadIDInput, send func(v02.DownloadTicket, string, []byte) error) error {
	return s.deliver(ctx, p, token, in, nil, send)
}
func (s *DownloadService) deliver(ctx context.Context, p metadata.Principal, token string, in v02.DownloadIDInput, proof *ContextProof, send func(v02.DownloadTicket, string, []byte) error) error {
	if !valid("DownloadIDInput", in) || send == nil {
		return ErrInput
	}
	tx, _, e := s.Credentials.Access.begin(ctx, p)
	if e != nil {
		return e
	}
	defer rollback(tx)
	aid, e := s.authority(ctx, tx, p, token)
	if e != nil {
		return e
	}
	binding, e := s.binding(ctx, tx, p, proof)
	if e != nil {
		return e
	}
	ticket, e := s.ticket(ctx, tx, p, in.DownloadID)
	if e != nil {
		return e
	}
	if ticket.ClientInstanceID != nil {
		if proof == nil || *ticket.ClientInstanceID != binding.view.ClientInstanceID || aid != binding.authorization {
			return ErrDenied
		}
	} else if proof != nil {
		return ErrDenied
	}
	if e = s.refresh(ctx, tx, p, &ticket); e != nil {
		return e
	}
	if ticket.State != "READY" {
		if e = commit(ctx, tx); e != nil {
			return e
		}
		return ErrConflict
	}
	var blob []byte
	var digest string
	e = tx.QueryRow(ctx, "SELECT b.envelope,p.digest FROM mcpguard_ra.credential_blobs b JOIN mcpguard_ra.credential_packages p USING(org_id,credential_id) WHERE b.org_id=$1 AND b.credential_id=$2", p.OrgID, ticket.CredentialID).Scan(&blob, &digest)
	if e != nil {
		return ErrUnavailable
	}
	sum := sha256.Sum256(blob)
	if len(blob) > v02.MaxPackageBytes || hex.EncodeToString(sum[:]) != digest {
		return ErrUnavailable
	}
	if proof != nil {
		if _, e = tx.Exec(ctx, "UPDATE mcpguard_ra.download_tickets SET state='CLAIMED',revision=revision+1,claimed_authorization_id=$3,claimed_at=clock_timestamp(),claimed_context_id=$4 WHERE org_id=$1 AND download_id=$2", p.OrgID, ticket.DownloadID, aid, binding.view.ContextID); e != nil {
			return ErrUnavailable
		}
		if e = s.audit(ctx, tx, p, ticket.DownloadID, ticket.CredentialID, aid, "CLAIM_COMMITTED"); e != nil {
			return e
		}
		ticket.State = "CLAIMED"
		ticket.Revision = strconv.FormatInt(number(ticket.Revision)+1, 10)
		ticket.DeliveryOutcome = "UNKNOWN"
	} else if e = s.transition(ctx, tx, p, &ticket, "CLAIMED", aid); e != nil {
		return e
	}
	if e = commit(ctx, tx); e != nil {
		return e
	}
	sendError := ctx.Err()
	if sendError == nil {
		sendError = send(ticket, digest, blob)
	}
	event := "DELIVERY_WRITE_COMPLETED"
	if sendError != nil {
		event = "DELIVERY_WRITE_INTERRUPTED"
	}
	// The request may already be gone. Persist only a fixed outcome code using
	// an independent bounded context; never persist exception text or body bytes.
	record, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	auditTx, e := s.Credentials.Access.Catalog.Store.Pool.Begin(record)
	if e != nil {
		return ErrUnavailable
	}
	defer rollback(auditTx)
	if e = s.audit(record, auditTx, p, ticket.DownloadID, ticket.CredentialID, aid, event); e != nil {
		return e
	}
	if e = commit(record, auditTx); e != nil {
		return e
	}
	if sendError != nil {
		return ErrUnavailable
	}
	return nil
}
