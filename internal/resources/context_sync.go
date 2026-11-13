package resources

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"strconv"
	"strings"
	"time"
)

func (s *ContextService) Prepare(ctx context.Context, p metadata.Principal, proof ContextProof, in v02.PrepareDownloadInput) (v02.PrepareDownloadResult, error) {
	return s.Downloads.prepare(ctx, p, in, &proof)
}
func (s *ContextService) Deliver(ctx context.Context, p metadata.Principal, delivery string, proof ContextProof, in v02.DownloadIDInput, send func(v02.DownloadTicket, string, []byte) error) error {
	return s.Downloads.deliver(ctx, p, delivery, in, &proof, send)
}
func (s *ContextService) TicketStatus(ctx context.Context, p metadata.Principal, proof ContextProof, in v02.DownloadIDInput) (v02.DownloadTicket, error) {
	var out v02.DownloadTicket
	if !valid("DownloadIDInput", in) {
		return out, ErrInput
	}
	tx, row, e := s.begin(ctx, p, proof, true)
	if e != nil {
		return out, e
	}
	defer rollback(tx)
	out, e = s.Downloads.ticket(ctx, tx, p, in.DownloadID)
	if e != nil {
		return out, e
	}
	if out.ClientInstanceID == nil || *out.ClientInstanceID != row.view.ClientInstanceID {
		return out, ErrDenied
	}
	if e = s.Downloads.refresh(ctx, tx, p, &out); e != nil {
		return out, e
	}
	return out, commit(ctx, tx)
}

type syncCursor struct{ Org, Subject, Client, Context, After, Expiry string }

func (s *ContextService) parseCursor(ctx context.Context, tx pgx.Tx, p metadata.Principal, row contextRecord, raw *string) (int64, error) {
	if raw == nil {
		return 0, nil
	}
	parts := strings.Split(*raw, ".")
	if len(parts) != 2 {
		return 0, ErrInput
	}
	body, e := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if e != nil || len(body) > 1500 || v02.CheckJSON(body) != nil {
		return 0, ErrInput
	}
	vault := s.Downloads.Credentials.Access.Catalog.Store.Vault
	if vault.Hash(append([]byte("sync-cursor/"), body...)) != parts[1] {
		return 0, ErrDenied
	}
	var cursor syncCursor
	if json.Unmarshal(body, &cursor) != nil || cursor.Org != p.OrgID || cursor.Subject != p.ID || cursor.Client != row.view.ClientInstanceID || cursor.Context != row.view.ContextID {
		return 0, ErrDenied
	}
	after, e := strconv.ParseInt(cursor.After, 10, 64)
	expiry, _, er := normalizedTime(cursor.Expiry)
	now, et := databaseTime(ctx, tx)
	if e != nil || er != nil || et != nil || after < 0 || !expiry.After(now) {
		return 0, ErrDenied
	}
	return after, nil
}
func (s *ContextService) cursor(row contextRecord, p metadata.Principal, after int64, now time.Time) string {
	expiry := now.Add(time.Minute)
	bound, _, _ := normalizedTime(row.view.ExpiresAt)
	if expiry.After(bound) {
		expiry = bound
	}
	body, _ := json.Marshal(syncCursor{p.OrgID, p.ID, row.view.ClientInstanceID, row.view.ContextID, strconv.FormatInt(after, 10), expiry.UTC().Format(time.RFC3339Nano)})
	return base64.RawURLEncoding.EncodeToString(body) + "." + s.Downloads.Credentials.Access.Catalog.Store.Vault.Hash(append([]byte("sync-cursor/"), body...))
}
func (s *ContextService) Queue(ctx context.Context, p metadata.Principal, proof ContextProof, in v02.ListDownloadsInput) (v02.DownloadQueue, error) {
	out := v02.DownloadQueue{Tickets: []v02.DownloadTicket{}, NextAfter: ""}
	if !valid("ListDownloadsInput", in) {
		return out, ErrInput
	}
	limit := s.PageLimit
	if in.Limit != nil {
		limit = *in.Limit
	}
	if limit > s.PageLimit {
		return out, ErrInput
	}
	tx, row, e := s.begin(ctx, p, proof, true)
	if e != nil {
		return out, e
	}
	defer rollback(tx)
	after, e := s.parseCursor(ctx, tx, p, row, in.After)
	if e != nil {
		return out, e
	}
	rows, e := tx.Query(ctx, `SELECT d.download_id::text,d.queue_seq FROM mcpguard_ra.download_tickets d WHERE d.org_id=$1 AND d.subject_id=$2 AND d.client_id=$3 AND d.queue_seq>$4 AND NOT EXISTS(SELECT FROM mcpguard_ra.download_task_receipts r WHERE (r.org_id,r.download_id)=(d.org_id,d.download_id)) ORDER BY d.queue_seq LIMIT $5`, p.OrgID, p.ID, row.view.ClientInstanceID, after, limit+1)
	if e != nil {
		return out, ErrUnavailable
	}
	type task struct {
		id  string
		seq int64
	}
	var tasks []task
	for rows.Next() {
		var t task
		if rows.Scan(&t.id, &t.seq) != nil {
			rows.Close()
			return out, ErrUnavailable
		}
		tasks = append(tasks, t)
	}
	rows.Close()
	hasMore := len(tasks) > limit
	if hasMore {
		tasks = tasks[:limit]
	}
	for _, task := range tasks {
		ticket, e := s.Downloads.ticket(ctx, tx, p, task.id)
		if e != nil {
			return out, e
		}
		if e = s.Downloads.refresh(ctx, tx, p, &ticket); e != nil {
			return out, e
		}
		out.Tickets = append(out.Tickets, ticket)
	}
	if hasMore {
		now, e := databaseTime(ctx, tx)
		if e != nil {
			return out, ErrUnavailable
		}
		out.NextAfter = s.cursor(row, p, tasks[len(tasks)-1].seq, now)
	}
	return out, commit(ctx, tx)
}
func (s *ContextService) receipt(ctx context.Context, tx pgx.Tx, p metadata.Principal, row contextRecord, did, outcome, replacement string) error {
	var prior string
	e := tx.QueryRow(ctx, "SELECT outcome FROM mcpguard_ra.download_task_receipts WHERE org_id=$1 AND download_id=$2", p.OrgID, did).Scan(&prior)
	if e == nil {
		if prior == outcome {
			return nil
		}
		return ErrConflict
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return ErrUnavailable
	}
	var next any
	if replacement != "" {
		next = replacement
	}
	if _, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.download_task_receipts(org_id,download_id,client_id,context_id,outcome,replacement_id) VALUES($1,$2,$3,$4,$5,$6)", p.OrgID, did, row.view.ClientInstanceID, row.view.ContextID, outcome, next); e != nil {
		return ErrUnavailable
	}
	return s.audit(ctx, tx, p, row.view.ClientInstanceID, row.view.ContextID, "", "", did, "TASK_"+outcome)
}
func (s *ContextService) Replace(ctx context.Context, p metadata.Principal, proof ContextProof, id string, in v02.ReplaceDownloadInput) (v02.PrepareDownloadResult, error) {
	var out v02.PrepareDownloadResult
	if !valid("ReplaceDownloadInput", in) || !valid("DownloadIDInput", v02.DownloadIDInput{DownloadID: id}) {
		return out, ErrInput
	}
	tx, row, e := s.begin(ctx, p, proof, true)
	if e != nil {
		return out, e
	}
	defer rollback(tx)
	old, e := s.Downloads.ticket(ctx, tx, p, id)
	if e != nil {
		return out, e
	}
	if old.ClientInstanceID == nil || *old.ClientInstanceID != row.view.ClientInstanceID {
		return out, ErrDenied
	}
	var replacement *string
	var outcome string
	e = tx.QueryRow(ctx, "SELECT outcome,replacement_id::text FROM mcpguard_ra.download_task_receipts WHERE org_id=$1 AND download_id=$2", p.OrgID, id).Scan(&outcome, &replacement)
	if e == nil && outcome == "REPLACED" && replacement != nil {
		ticket, e := s.Downloads.ticket(ctx, tx, p, *replacement)
		if e != nil {
			return out, e
		}
		if e = s.Downloads.refresh(ctx, tx, p, &ticket); e != nil {
			return out, e
		}
		return prepared(ticket), commit(ctx, tx)
	}
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return out, ErrUnavailable
	}
	if e = s.Downloads.refresh(ctx, tx, p, &old); e != nil {
		return out, e
	}
	if old.State == "READY" {
		return out, ErrConflict
	}
	credential, e := s.Downloads.Credentials.row(ctx, tx, p, old.CredentialID)
	if e != nil {
		return out, e
	}
	if e = s.Downloads.Credentials.refresh(ctx, tx, p, credential); e != nil {
		return out, e
	}
	if credential.state != "ACTIVE" {
		if outcome == "" {
			e = s.receipt(ctx, tx, p, row, id, "DISCARDED", "")
		} else {
			e = nil
		}
		if e != nil {
			return out, e
		}
		if e = commit(ctx, tx); e != nil {
			return out, e
		}
		return out, ErrDenied
	}
	key := "replace_" + s.Downloads.Credentials.Access.Catalog.Store.Vault.Hash([]byte(id+"/"+in.RequestKey))
	input := v02.PrepareDownloadInput{GrantID: credential.ref.GrantID, RequestKey: key}
	prior, e := s.Downloads.prior(ctx, tx, p, input, row.view.ClientInstanceID)
	if e != nil {
		return out, e
	}
	if prior != nil {
		return *prior, commit(ctx, tx)
	}
	out, e = s.Downloads.insertTicket(ctx, tx, p, input, statusOf(credential), row.view.ClientInstanceID)
	if e != nil {
		return out, e
	}
	if outcome == "ACTIVATED" {
		e = s.audit(ctx, tx, p, row.view.ClientInstanceID, proof.id, credential.ref.ResourceID, credential.ref.CredentialID, out.DownloadID, "TASK_REPLACED")
	} else {
		e = s.receipt(ctx, tx, p, row, id, "REPLACED", out.DownloadID)
	}
	if e != nil {
		return out, e
	}
	return out, commit(ctx, tx)
}
func (s *ContextService) Renewable(ctx context.Context, tx pgx.Tx, p metadata.Principal, cid string) (*credentialRow, error) {
	old, e := s.Downloads.Credentials.row(ctx, tx, p, cid)
	if e != nil {
		return nil, e
	}
	if e = s.Downloads.Credentials.refresh(ctx, tx, p, old); e != nil {
		return nil, e
	}
	if old.state == "REVOKED" {
		return nil, ErrDenied
	}
	current, e := s.Downloads.Credentials.selectGrant(ctx, tx, p, old.ref.GrantID)
	if e != nil {
		return nil, e
	}
	if !selectionMatches(old, current) {
		return nil, ErrDenied
	}
	return old, nil
}
func (s *ContextService) RenewCredential(ctx context.Context, p metadata.Principal, proof ContextProof, in v02.RenewCredentialInput) (v02.PrepareDownloadResult, error) {
	var out v02.PrepareDownloadResult
	if !valid("RenewCredentialInput", in) {
		return out, ErrInput
	}
	tx, row, e := s.begin(ctx, p, proof, true)
	if e != nil {
		return out, e
	}
	defer rollback(tx)
	old, e := s.Renewable(ctx, tx, p, in.CredentialID)
	if e != nil {
		return out, e
	}
	grant := old.ref.GrantID
	if e = commit(ctx, tx); e != nil {
		return out, e
	}
	issue := v02.IssueCredentialInput{GrantID: grant, RequestKey: "renew_" + s.Downloads.Credentials.Access.Catalog.Store.Vault.Hash([]byte(in.CredentialID))}
	prep, existing, e := s.Downloads.Credentials.prepareIssue(ctx, p, issue)
	if e != nil {
		return out, e
	}
	var blob []byte
	if prep != nil {
		blob, e = s.Downloads.Credentials.encryptPreparation(ctx, p, prep)
		if e != nil {
			return out, e
		}
	}
	final, row, e := s.begin(ctx, p, proof, true)
	if e != nil {
		return out, e
	}
	defer rollback(final)
	old, e = s.Renewable(ctx, final, p, in.CredentialID)
	if e != nil {
		return out, e
	}
	input := v02.PrepareDownloadInput{GrantID: grant, RequestKey: "renew_ticket_" + s.Downloads.Credentials.Access.Catalog.Store.Vault.Hash([]byte(in.CredentialID+"/"+in.RequestKey))}
	prior, e := s.Downloads.prior(ctx, final, p, input, row.view.ClientInstanceID)
	if e != nil {
		return out, e
	}
	if prior != nil {
		return *prior, commit(ctx, final)
	}
	var status v02.CredentialStatus
	if prep != nil {
		status, e = s.Downloads.Credentials.issueWithin(ctx, final, p, issue, prep, blob)
	} else if existing != nil {
		var successor *credentialRow
		successor, e = s.Downloads.Credentials.row(ctx, final, p, existing.CredentialID)
		if e == nil {
			e = s.Downloads.Credentials.refresh(ctx, final, p, successor)
		}
		if e == nil {
			status = statusOf(successor)
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
	var saved string
	e = final.QueryRow(ctx, "SELECT credential_id::text FROM mcpguard_ra.credential_renewals WHERE org_id=$1 AND previous_id=$2", p.OrgID, in.CredentialID).Scan(&saved)
	if errors.Is(e, pgx.ErrNoRows) {
		if _, e = final.Exec(ctx, "INSERT INTO mcpguard_ra.credential_renewals(org_id,subject_id,previous_id,credential_id) VALUES($1,$2,$3,$4)", p.OrgID, p.ID, in.CredentialID, status.CredentialID); e != nil {
			return out, ErrUnavailable
		}
		if e = s.audit(ctx, final, p, row.view.ClientInstanceID, proof.id, old.ref.ResourceID, status.CredentialID, "", "CREDENTIAL_RENEWED"); e != nil {
			return out, e
		}
	} else if e != nil || saved != status.CredentialID {
		return out, ErrUnavailable
	}
	out, e = s.Downloads.insertTicket(ctx, final, p, input, status, row.view.ClientInstanceID)
	if e != nil {
		return out, e
	}
	return out, commit(ctx, final)
}
