package metadata

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrDenied = errors.New("access denied")
var ErrInvalidToken = errors.New("invalid test token")

type Principal struct {
	OrgID, ID, ScopeID string
	CanRead            bool
	ExpiresAt          time.Time
	TokenHash          []byte
}

type Store struct {
	Pool     *pgxpool.Pool
	Vault    *Vault
	Instance string
}

func Open(ctx context.Context, dsn string, vault *Vault) (*Store, error) {
	c, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		return nil, e
	}
	c.MaxConns = 8
	c.MinConns = 0
	c.ConnConfig.ConnectTimeout = 3 * time.Second
	c.ConnConfig.RuntimeParams["application_name"] = "mcpguard-metadata"
	c.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	c.ConnConfig.RuntimeParams["lock_timeout"] = "1000"
	c.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "5000"
	p, e := pgxpool.NewWithConfig(ctx, c)
	if e != nil {
		return nil, e
	}
	s := &Store{p, vault, uuid.NewString()}
	var unsafe bool
	e = p.QueryRow(ctx, `SELECT rolsuper OR rolcreatedb OR rolcreaterole OR has_schema_privilege(current_user,'mcpguard','CREATE') OR has_table_privilege(current_user,'mcpguard.audit_events','UPDATE,DELETE,TRUNCATE') FROM pg_roles WHERE rolname=current_user`).Scan(&unsafe)
	if e == nil && unsafe {
		e = errors.New("metadata runtime role must be unprivileged")
	}
	if e == nil {
		var version int
		e = p.QueryRow(ctx, "SELECT max(version) FROM mcpguard.schema_versions").Scan(&version)
		if e == nil && version != 2 {
			e = errors.New("unsupported metadata schema")
		}
	}
	if e != nil {
		p.Close()
		return nil, e
	}
	return s, nil
}
func (s *Store) Close() { s.Pool.Close() }

func scanPrincipal(row pgx.Row) (p Principal, err error) {
	err = row.Scan(&p.OrgID, &p.ID, &p.ScopeID, &p.CanRead, &p.ExpiresAt, &p.TokenHash)
	return
}

const principalColumns = `org_id::text,id::text,scope_id,can_read,expires_at,token_hash`

func (s *Store) VerifyToken(ctx context.Context, token string) (Principal, error) {
	if len(token) < 32 || len(token) > 256 {
		return Principal{}, ErrInvalidToken
	}
	h := sha256.Sum256([]byte(token))
	p, e := scanPrincipal(s.Pool.QueryRow(ctx, `SELECT `+principalColumns+` FROM mcpguard.principals WHERE token_hash=$1 AND enabled AND expires_at>clock_timestamp()`, h[:]))
	if errors.Is(e, pgx.ErrNoRows) {
		return p, ErrInvalidToken
	}
	return p, e
}

func (s *Store) Current(ctx context.Context, p Principal) (Principal, error) {
	v, e := scanPrincipal(s.Pool.QueryRow(ctx, `SELECT `+principalColumns+` FROM mcpguard.principals WHERE org_id=$1 AND id=$2 AND token_hash=$3 AND enabled AND expires_at>clock_timestamp()`, p.OrgID, p.ID, p.TokenHash))
	if errors.Is(e, pgx.ErrNoRows) {
		return v, ErrDenied
	}
	return v, e
}

type Ticket struct {
	OrgID, PrincipalID, ActionID, AttemptID, ExecutionID, PlanHash string
	PlanCiphertext                                                 []byte
	Deadline                                                       time.Time
}

func (t Ticket) binding(purpose string) string {
	return t.OrgID + "/" + t.ActionID + "/" + purpose + "/local-v1"
}

// Claim creates one immutable Action, Attempt, qualification and audit in the same
// transaction. There is no background execution queue and no retry of a claim.
func (s *Store) Claim(ctx context.Context, p Principal, item string) (t Ticket, err error) {
	t = Ticket{OrgID: p.OrgID, PrincipalID: p.ID, ActionID: uuid.NewString(), AttemptID: uuid.NewString(), ExecutionID: uuid.NewString(), Deadline: time.Now().UTC().Add(3 * time.Second)}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return t, e
	}
	defer rollback(tx)
	var epoch int64
	if e = tx.QueryRow(ctx, `SELECT mcpguard.lock_auth_epoch($1)`, p.OrgID).Scan(&epoch); e != nil {
		return t, e
	}
	cur, e := scanPrincipal(tx.QueryRow(ctx, `SELECT `+principalColumns+` FROM mcpguard.principals WHERE org_id=$1 AND id=$2 AND token_hash=$3 AND enabled AND expires_at>clock_timestamp()`, p.OrgID, p.ID, p.TokenHash))
	if errors.Is(e, pgx.ErrNoRows) || e == nil && !cur.CanRead {
		return t, ErrDenied
	}
	if e != nil {
		return t, e
	}
	// Only ASCII strings and integers-as-strings: sorted map encoding is the
	// RFC8785-compatible subset used by this one fixed plan, not a general JCS encoder.
	plan, e := json.Marshal(map[string]string{"encodingVersion": "1", "orgId": p.OrgID, "principalId": p.ID, "scopeId": cur.ScopeID, "itemId": item, "tool": "pg.get_demo_item", "toolVersion": "1", "resource": "demo.pg.v1", "credentialBinding": "demo.reader.v1", "operation": "demo.item.read.v1", "authEpoch": fmt.Sprint(epoch), "deadline": t.Deadline.Format(time.RFC3339Nano), "maxRows": "1", "maxResultBytes": "16384", "timeoutMs": "3000"})
	if e != nil {
		return t, e
	}
	t.PlanHash = s.Vault.Hash(plan)
	t.PlanCiphertext = s.Vault.Seal(plan, t.binding("PLAN"))
	_, e = tx.Exec(ctx, `INSERT INTO mcpguard.actions(org_id,id,principal_id,tool_name,tool_version,scope_id,auth_epoch,plan_hash,plan_ciphertext,key_version,authorization_source,state) VALUES($1,$2,$3,'pg.get_demo_item','1',$4,$5,$6,$7,'local-v1','POLICY','EXECUTING')`, p.OrgID, t.ActionID, p.ID, cur.ScopeID, epoch, t.PlanHash, t.PlanCiphertext)
	if e != nil {
		return t, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO mcpguard.attempts VALUES($1,$2,$3,'2025-11-25','RUNNING')`, p.OrgID, t.AttemptID, t.ActionID)
	if e != nil {
		return t, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO mcpguard.executions(org_id,id,action_id,attempt_id,owner_instance,hard_deadline,dispatch_state,request_status) VALUES($1,$2,$3,$4,$5,$6,'INTENT_RECORDED','RUNNING')`, p.OrgID, t.ExecutionID, t.ActionID, t.AttemptID, s.Instance, t.Deadline)
	if e != nil {
		return t, e
	}
	if e = s.audit(ctx, tx, t, "EXECUTION_INTENT"); e != nil {
		return t, e
	}
	err = tx.Commit(ctx) // Unknown commit outcome MUST NOT lead to native dispatch.
	return
}

func (s *Store) ReadPlan(t Ticket) (scope, item string, err error) {
	b, e := s.Vault.Open(t.PlanCiphertext, t.binding("PLAN"))
	if e != nil {
		return "", "", e
	}
	if s.Vault.Hash(b) != t.PlanHash {
		return "", "", errors.New("plan integrity mismatch")
	}
	var p map[string]string
	if e = json.Unmarshal(b, &p); e != nil {
		return "", "", e
	}
	if p["orgId"] != t.OrgID || p["principalId"] != t.PrincipalID || p["operation"] != "demo.item.read.v1" {
		return "", "", errors.New("plan binding mismatch")
	}
	return p["scopeId"], p["itemId"], nil
}

func (s *Store) audit(ctx context.Context, tx pgx.Tx, t Ticket, kind string) error {
	_, e := tx.Exec(ctx, `INSERT INTO mcpguard.audit_events(id,org_id,principal_id,action_id,attempt_id,execution_id,event_type) VALUES($1,$2,$3,$4,$5,$6,$7)`, uuid.NewString(), t.OrgID, t.PrincipalID, t.ActionID, t.AttemptID, t.ExecutionID, kind)
	return e
}

func (s *Store) Finish(ctx context.Context, t Ticket, status, dispatch string, result []byte) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer rollback(tx)
	ct, e := tx.Exec(ctx, `UPDATE mcpguard.executions SET request_status=$3,dispatch_state=$4,result_ciphertext=$5 WHERE org_id=$1 AND id=$2 AND request_status='RUNNING' AND owner_instance=$6`, t.OrgID, t.ExecutionID, status, dispatch, s.Vault.Seal(result, t.binding("RESULT")), s.Instance)
	if e != nil {
		return e
	}
	if ct.RowsAffected() != 1 {
		return errors.New("qualification already terminal or not owned")
	}
	if _, e = tx.Exec(ctx, `UPDATE mcpguard.actions SET state=$3 WHERE org_id=$1 AND id=$2`, t.OrgID, t.ActionID, status); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE mcpguard.attempts SET state=$3 WHERE org_id=$1 AND id=$2`, t.OrgID, t.AttemptID, status); e != nil {
		return e
	}
	if e = s.audit(ctx, tx, t, status); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type AuditEvent struct {
	Seq         int64     `json:"seq"`
	ID          string    `json:"id"`
	ActionID    *string   `json:"actionId"`
	AttemptID   *string   `json:"attemptId"`
	ExecutionID *string   `json:"executionId"`
	EventType   string    `json:"eventType"`
	CreatedAt   time.Time `json:"createdAt"`
}

func (s *Store) Rejection(ctx context.Context, p Principal, kind string) error {
	_, e := s.Pool.Exec(ctx, `INSERT INTO mcpguard.audit_events(id,org_id,principal_id,event_type) VALUES($1,$2,$3,$4)`, uuid.NewString(), p.OrgID, p.ID, kind)
	return e
}

func (s *Store) Audit(ctx context.Context, p Principal, after int64, limit int) ([]AuditEvent, error) {
	// No caller-supplied principal or org selector. Recheck revocation even inside a session.
	if _, e := s.Current(ctx, p); e != nil {
		return nil, e
	}
	rows, e := s.Pool.Query(ctx, `SELECT seq,id::text,action_id::text,attempt_id::text,execution_id::text,event_type,created_at FROM mcpguard.audit_events WHERE org_id=$1 AND principal_id=$2 AND seq>$3 ORDER BY seq LIMIT $4`, p.OrgID, p.ID, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []AuditEvent{}
	for rows.Next() {
		var a AuditEvent
		if e = rows.Scan(&a.Seq, &a.ID, &a.ActionID, &a.AttemptID, &a.ExecutionID, &a.EventType, &a.CreatedAt); e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Stale returns facts to terminalize conservatively. It never grants dispatch.
func (s *Store) Stale(ctx context.Context) ([]Ticket, error) {
	rows, e := s.Pool.Query(ctx, `SELECT e.org_id::text,a.principal_id::text,e.action_id::text,e.attempt_id::text,e.id::text FROM mcpguard.executions e JOIN mcpguard.actions a ON (a.org_id,a.id)=(e.org_id,e.action_id) WHERE e.request_status='RUNNING' AND e.hard_deadline < clock_timestamp()-interval '15 seconds' LIMIT 100`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Ticket{}
	for rows.Next() {
		var t Ticket
		if e = rows.Scan(&t.OrgID, &t.PrincipalID, &t.ActionID, &t.AttemptID, &t.ExecutionID); e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) Recover(ctx context.Context, t Ticket, result []byte) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer rollback(tx)
	ct, e := tx.Exec(ctx, `UPDATE mcpguard.executions SET request_status='OUTCOME_UNKNOWN',dispatch_state='DISPATCH_UNKNOWN',result_ciphertext=$3 WHERE org_id=$1 AND id=$2 AND request_status='RUNNING' AND dispatch_state='INTENT_RECORDED' AND hard_deadline<clock_timestamp()-interval '15 seconds'`, t.OrgID, t.ExecutionID, s.Vault.Seal(result, t.binding("RESULT")))
	if e != nil {
		return e
	}
	if ct.RowsAffected() == 0 {
		return nil
	}
	if _, e = tx.Exec(ctx, `UPDATE mcpguard.actions SET state='OUTCOME_UNKNOWN' WHERE org_id=$1 AND id=$2`, t.OrgID, t.ActionID); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE mcpguard.attempts SET state='OUTCOME_UNKNOWN' WHERE org_id=$1 AND id=$2`, t.OrgID, t.AttemptID); e != nil {
		return e
	}
	if e = s.audit(ctx, tx, t, "OUTCOME_UNKNOWN"); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
