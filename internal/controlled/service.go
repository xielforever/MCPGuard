package controlled

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

var ErrDenied = errors.New("operation not authorized or object unavailable")
var ErrConflict = errors.New("revision, idempotency key or state conflict")
var ErrInput = errors.New("invalid input")

type Executor interface {
	Available(string) bool
	Binding() string
	Execute(context.Context, Tool, string, map[string]any) Outcome
}
type Service struct {
	Store  *metadata.Store
	Native Executor
	Tools  []Tool
	Origin string
}

func New(s *metadata.Store, n Executor, origin string) (*Service, error) {
	ts, e := Catalog()
	if e != nil {
		return nil, e
	}
	return &Service{s, n, ts, origin}, nil
}
func (s *Service) Tool(name string) (Tool, bool) {
	for _, t := range s.Tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}
func rollback(tx pgx.Tx) {
	c, done := context.WithTimeout(context.Background(), time.Second)
	defer done()
	_ = tx.Rollback(c)
}
func (s *Service) current(ctx context.Context, tx pgx.Tx, p metadata.Principal) (metadata.Principal, error) {
	var epoch int64
	if e := tx.QueryRow(ctx, `SELECT mcpguard.lock_auth_epoch($1)`, p.OrgID).Scan(&epoch); e != nil {
		return p, e
	}
	var cur metadata.Principal
	e := tx.QueryRow(ctx, `SELECT org_id::text,id::text,scope_id,can_read,expires_at,token_hash FROM mcpguard.principals WHERE org_id=$1 AND id=$2 AND token_hash=$3 AND enabled AND expires_at>clock_timestamp()`, p.OrgID, p.ID, p.TokenHash).Scan(&cur.OrgID, &cur.ID, &cur.ScopeID, &cur.CanRead, &cur.ExpiresAt, &cur.TokenHash)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrDenied
	}
	return cur, e
}
func (s *Service) head(ctx context.Context, tx pgx.Tx, org string) (int64, error) {
	var v int64
	e := tx.QueryRow(ctx, `SELECT revision FROM mcpguard.control_heads WHERE org_id=$1 FOR SHARE`, org).Scan(&v)
	return v, e
}
func (s *Service) authorize(ctx context.Context, tx pgx.Tx, p metadata.Principal, t Tool, args map[string]any) (approval bool, err error) {
	if !p.CanRead || !s.Native.Available(t.Resource) {
		return false, ErrDenied
	}
	var allowed bool
	err = tx.QueryRow(ctx, `SELECT r.enabled AND mcpguard.has_tool_grant(r.org_id,$2,$3) FROM mcpguard.control_resources r WHERE r.org_id=$1 AND r.name=$4`, p.OrgID, p.ID, t.Name, t.Resource).Scan(&allowed)
	if err != nil || !allowed {
		return false, ErrDenied
	}
	approval = t.Effect == "WRITE"
	b, _ := json.Marshal(args)
	rows, e := tx.Query(ctx, `SELECT effect,pattern FROM mcpguard.control_rules WHERE org_id=$1 AND tool_name=$2 AND enabled ORDER BY id`, p.OrgID, t.Name)
	if e != nil {
		return false, e
	}
	defer rows.Close()
	for rows.Next() {
		var effect, pattern string
		if e = rows.Scan(&effect, &pattern); e != nil {
			return false, e
		}
		re, e := regexp.Compile(pattern)
		if e != nil {
			return false, ErrDenied
		}
		if re.Match(b) {
			switch effect {
			case "DENY":
				return false, ErrDenied
			case "REQUIRE_APPROVAL":
				approval = true
			}
		}
	}
	return approval, rows.Err()
}
func (s *Service) Visible(ctx context.Context, p metadata.Principal) ([]string, error) {
	if _, e := s.Store.Current(ctx, p); e != nil {
		return nil, e
	}
	names, resources := []string{}, []string{}
	for _, tool := range s.Tools {
		if tool.Name != "pg.get_demo_item" {
			names = append(names, tool.Name)
			resources = append(resources, tool.Resource)
		}
	}
	rows, e := s.Store.Pool.Query(ctx, `SELECT t.name,t.resource FROM unnest($3::text[],$4::text[]) AS t(name,resource) JOIN mcpguard.control_resources r ON r.org_id=$1 AND r.name=t.resource WHERE r.enabled AND mcpguard.has_tool_grant($1,$2,t.name)`, p.OrgID, p.ID, names, resources)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n, r string
		if e = rows.Scan(&n, &r); e != nil {
			return nil, e
		}
		if s.Native.Available(r) {
			out = append(out, n)
		}
	}
	return out, rows.Err()
}

type action struct {
	Legacy                                                               bool
	Org, ID, Owner, Attempt, Tool, Resource, Scope, Hash, Binding, State string
	Config, Revision                                                     int64
	Approval, Cancel                                                     bool
	Cipher                                                               []byte
	Created, Expires                                                     time.Time
	Deadline                                                             time.Time
	Execution                                                            *string
	Result                                                               []byte
	Dispatch, Status, Side                                               string
}

const actionSelect = `SELECT a.org_id::text,a.id::text,a.principal_id::text,a.attempt_id::text,a.tool_name,a.resource_name,a.scope_id,a.plan_hash,a.binding_hash,a.state,a.config_revision,a.revision,a.requires_approval,a.cancel_requested,a.plan_ciphertext,a.created_at,a.expires_at,e.id::text,e.result_ciphertext,coalesce(e.dispatch_state,'NOT_DISPATCHED'),coalesce(e.request_status,''),coalesce(e.side_effect_status,'NONE'),coalesce(e.hard_deadline,a.expires_at) FROM mcpguard.managed_actions a LEFT JOIN mcpguard.managed_executions e ON (e.org_id,e.action_id)=(a.org_id,a.id)`

func scan(row pgx.Row) (a action, e error) {
	e = row.Scan(&a.Org, &a.ID, &a.Owner, &a.Attempt, &a.Tool, &a.Resource, &a.Scope, &a.Hash, &a.Binding, &a.State, &a.Config, &a.Revision, &a.Approval, &a.Cancel, &a.Cipher, &a.Created, &a.Expires, &a.Execution, &a.Result, &a.Dispatch, &a.Status, &a.Side, &a.Deadline)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrDenied
	}
	return
}
func (a action) binding(purpose string) string {
	return a.Org + "/" + a.ID + "/" + purpose + "/controlled-v1"
}
func (s *Service) audit(ctx context.Context, tx pgx.Tx, a action, actor, event string) error {
	_, e := tx.Exec(ctx, `INSERT INTO mcpguard.managed_audit(id,org_id,principal_id,action_id,attempt_id,execution_id,event_type) VALUES($1,$2,$3,$4,$5,$6,$7)`, uuid.NewString(), a.Org, actor, a.ID, a.Attempt, a.Execution, event)
	return e
}
func (s *Service) state(ctx context.Context, tx pgx.Tx, a *action, next, event string) error {
	_, e := tx.Exec(ctx, `UPDATE mcpguard.managed_actions SET state=$3,revision=revision+1 WHERE org_id=$1 AND id=$2`, a.Org, a.ID, next)
	if e != nil {
		return e
	}
	a.State = next
	a.Revision++
	return s.audit(ctx, tx, *a, a.Owner, event)
}
func (s *Service) plan(a action) (map[string]any, error) {
	b, e := s.Store.Vault.Open(a.Cipher, a.binding("PLAN"))
	if e != nil {
		return nil, e
	}
	if s.Store.Vault.Hash(b) != a.Hash {
		return nil, ErrDenied
	}
	var v frozenPlan
	if e = json.Unmarshal(b, &v); e != nil {
		return nil, e
	}
	if v.Org != a.Org || v.Owner != a.Owner || v.Scope != a.Scope || v.Tool != a.Tool || v.Resource != a.Resource || v.Version != "1" || v.Expires != a.Expires.UTC().Format(time.RFC3339Nano) || v.Binding != a.Binding || v.Config != a.Config {
		return nil, ErrDenied
	}
	t, ok := s.Tool(a.Tool)
	if !ok {
		return nil, ErrDenied
	}
	raw, _ := json.Marshal(v.Args)
	return t.Arguments(raw)
}

type frozenPlan struct {
	Org, Owner, Scope, Tool, Resource, Binding, Expires, Version string
	Config                                                       int64
	Args                                                         map[string]any
}

func (s *Service) Create(ctx context.Context, p metadata.Principal, name string, raw []byte) (map[string]any, error) {
	t, ok := s.Tool(name)
	if !ok || name == "pg.get_demo_item" {
		return nil, ErrInput
	}
	args, e := t.Arguments(raw)
	if e != nil {
		return nil, ErrInput
	}
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer rollback(tx)
	cur, e := s.current(ctx, tx, p)
	if e != nil {
		return nil, e
	}
	v, e := s.head(ctx, tx, p.OrgID)
	if e != nil {
		return nil, e
	}
	approval, e := s.authorize(ctx, tx, cur, t, args)
	if e != nil {
		return nil, e
	}
	a := action{Org: p.OrgID, ID: uuid.NewString(), Owner: p.ID, Attempt: uuid.NewString(), Tool: t.Name, Resource: t.Resource, Scope: cur.ScopeID, Config: v, Revision: 1, Approval: approval, Binding: s.Native.Binding(), State: "READY", Created: time.Now().UTC(), Expires: time.Now().UTC().Add(15 * time.Minute)}
	if approval {
		a.State = "PENDING_APPROVAL"
	}
	// PostgreSQL persists microseconds. Freeze that exact value in the plan.
	a.Created = a.Created.Truncate(time.Microsecond)
	a.Expires = a.Expires.Truncate(time.Microsecond)
	b, _ := json.Marshal(frozenPlan{a.Org, a.Owner, a.Scope, a.Tool, a.Resource, a.Binding, a.Expires.Format(time.RFC3339Nano), "1", a.Config, args})
	a.Hash = s.Store.Vault.Hash(b)
	a.Cipher = s.Store.Vault.Seal(b, a.binding("PLAN"))
	_, e = tx.Exec(ctx, `INSERT INTO mcpguard.managed_actions(org_id,id,principal_id,attempt_id,tool_name,resource_name,scope_id,config_revision,plan_hash,plan_ciphertext,binding_hash,requires_approval,state,created_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, a.Org, a.ID, a.Owner, a.Attempt, a.Tool, a.Resource, a.Scope, a.Config, a.Hash, a.Cipher, a.Binding, a.Approval, a.State, a.Created, a.Expires)
	if e != nil {
		return nil, e
	}
	if e = s.audit(ctx, tx, a, a.Owner, "ACTION_CREATED"); e != nil {
		return nil, e
	}
	if approval {
		_, e = tx.Exec(ctx, `INSERT INTO mcpguard.notification_outbox(org_id,id,action_id) VALUES($1,$2,$3)`, a.Org, uuid.NewString(), a.ID)
		if e != nil {
			return nil, e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	if approval {
		return s.deferred(a), nil
	}
	res, e := s.control(ctx, p, a.ID, "RESUME", true)
	if e != nil {
		return nil, e
	}
	if r, ok := res["result"].(map[string]any); ok {
		return r, nil
	}
	return res, nil
}
func (s *Service) deferred(a action) map[string]any {
	next := []string{"STATUS", "CANCEL"}
	if a.State == "READY" {
		next = append(next, "RESUME")
	}
	return map[string]any{"kind": "DEFERRED", "actionId": a.ID, "attemptId": a.Attempt, "state": a.State, "executionId": nil, "dispatchState": "NOT_DISPATCHED", "sideEffectStatus": "NONE", "approvalUrl": s.Origin + "/web/#action=" + a.ID, "nextActions": next, "expiresAt": a.Expires}
}
func (s *Service) view(ctx context.Context, a action) map[string]any {
	var rid string
	_ = s.Store.Pool.QueryRow(ctx, `SELECT id::text FROM mcpguard.control_resources WHERE org_id=$1 AND name=$2`, a.Org, a.Resource).Scan(&rid)
	mode, source := "BOUNDED_WAIT", any("POLICY")
	if a.Approval {
		mode = "CONTROL_FLOW"
		source = nil
		if a.State == "READY" || a.Execution != nil {
			source = "HUMAN"
		}
	}
	actions := []string{"STATUS"}
	if a.State == "PENDING_APPROVAL" || a.State == "READY" {
		actions = append(actions, "CANCEL")
		if a.State == "READY" && a.Approval {
			actions = append(actions, "RESUME")
		}
	}
	if a.State == "EXECUTING" && !a.Cancel {
		actions = append(actions, "CANCEL")
	}
	if a.Result != nil {
		actions = append(actions, "VIEW_RESULT")
	}
	var status any
	if a.Status != "" {
		status = a.Status
	} else {
		switch a.State {
		case "DENIED", "EXPIRED", "FAILED":
			status = "FAILED"
		case "CANCELLED":
			status = "CANCELLED"
		}
	}
	return map[string]any{"actionId": a.ID, "requesterId": a.Owner, "state": a.State, "authorizationSource": source, "interactionMode": mode, "revision": a.Revision, "planHash": a.Hash, "toolName": a.Tool, "resourceId": rid, "createdAt": a.Created, "expiresAt": a.Expires, "executionId": a.Execution, "dispatchState": a.Dispatch, "requestStatus": status, "sideEffectStatus": a.Side, "availableActions": actions}
}
func (s *Service) storedResult(a action) (map[string]any, error) {
	if a.Result == nil {
		return nil, nil
	}
	binding := a.binding("RESULT")
	if a.Legacy {
		binding = a.Org + "/" + a.ID + "/RESULT/local-v1"
	}
	b, e := s.Store.Vault.Open(a.Result, binding)
	if e != nil {
		return nil, e
	}
	var r map[string]any
	e = json.Unmarshal(b, &r)
	return r, e
}
func (s *Service) Control(ctx context.Context, p metadata.Principal, id, op string) (map[string]any, error) {
	return s.control(ctx, p, id, op, false)
}
func (s *Service) control(ctx context.Context, p metadata.Principal, id, op string, auto bool) (map[string]any, error) {
	if _, e := uuid.Parse(id); e != nil {
		return nil, ErrInput
	}
	if op != "STATUS" && op != "RESUME" && op != "CANCEL" {
		return nil, ErrInput
	}
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer rollback(tx)
	cur, e := s.current(ctx, tx, p)
	if e != nil {
		return nil, e
	}
	v, e := s.head(ctx, tx, p.OrgID)
	if e != nil {
		return nil, e
	}
	a, e := scan(tx.QueryRow(ctx, actionSelect+` WHERE a.org_id=$1 AND a.id=$2 AND a.principal_id=$3 FOR UPDATE OF a`, p.OrgID, id, p.ID))
	if e != nil {
		if errors.Is(e, ErrDenied) {
			return s.legacyControl(ctx, tx, p, id, op)
		}
		return nil, e
	}
	if op == "RESUME" && !a.Approval && !auto {
		return nil, ErrInput
	}
	outcome := "STATE_RETURNED"
	run := false
	if (a.State == "PENDING_APPROVAL" || a.State == "READY") && !a.Expires.After(time.Now()) {
		if e = s.state(ctx, tx, &a, "EXPIRED", "ACTION_EXPIRED"); e != nil {
			return nil, e
		}
	}
	if op == "CANCEL" {
		switch a.State {
		case "PENDING_APPROVAL", "READY":
			e = s.state(ctx, tx, &a, "CANCELLED", "ACTION_CANCELLED")
			outcome = "CANCELLED"
		case "EXECUTING":
			if !a.Cancel {
				_, e = tx.Exec(ctx, `UPDATE mcpguard.managed_actions SET cancel_requested=true,revision=revision+1 WHERE org_id=$1 AND id=$2`, a.Org, a.ID)
				a.Cancel = true
				a.Revision++
				if e == nil {
					e = s.audit(ctx, tx, a, a.Owner, "CANCELLATION_REQUESTED")
				}
			}
			outcome = "CANCELLATION_REQUESTED"
		default:
			outcome = "NOT_RESUMABLE"
		}
	} else if op == "RESUME" {
		switch a.State {
		case "PENDING_APPROVAL":
			outcome = "APPROVAL_PENDING"
		case "EXECUTING":
			outcome = "EXECUTION_IN_PROGRESS"
		case "READY":
			t, ok := s.Tool(a.Tool)
			args, pe := s.plan(a)
			_, ae := s.authorize(ctx, tx, cur, t, args)
			valid := ok && pe == nil && ae == nil && cur.ScopeID == a.Scope && v == a.Config && a.Binding == s.Native.Binding()
			if valid && a.Approval {
				var reviewerOK bool
				pe = tx.QueryRow(ctx, `SELECT p.enabled AND p.expires_at>clock_timestamp() AND mcpguard.has_reviewer_grant(a.org_id,d.reviewer_id,a.resource_name) AND d.reviewer_id<>a.principal_id AND d.plan_hash=a.plan_hash AND d.vote='APPROVE' FROM mcpguard.managed_decisions d JOIN mcpguard.managed_actions a ON (a.org_id,a.id)=(d.org_id,d.action_id) JOIN mcpguard.principals p ON (p.org_id,p.id)=(d.org_id,d.reviewer_id) WHERE a.org_id=$1 AND a.id=$2`, a.Org, a.ID).Scan(&reviewerOK)
				valid = pe == nil && reviewerOK
			}
			if !valid {
				e = s.state(ctx, tx, &a, "CANCELLED", "PLAN_INVALIDATED")
				outcome = "NOT_RESUMABLE"
			} else {
				eid := uuid.NewString()
				a.Execution = &eid
				a.Dispatch = "INTENT_RECORDED"
				a.Status = "RUNNING"
				e = tx.QueryRow(ctx, `INSERT INTO mcpguard.managed_executions(org_id,id,action_id,owner_instance,hard_deadline,request_status,dispatch_state,side_effect_status) VALUES($1,$2,$3,$4,clock_timestamp()+interval '5 seconds','RUNNING','INTENT_RECORDED','NONE') RETURNING hard_deadline`, a.Org, eid, a.ID, s.Store.Instance).Scan(&a.Deadline)
				if e == nil {
					e = s.state(ctx, tx, &a, "EXECUTING", "EXECUTION_INTENT")
				}
				run = true
			}
		default:
			if a.Result != nil {
				outcome = "RESULT_RETURNED"
			} else {
				outcome = "NOT_RESUMABLE"
			}
		}
	}
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	if run {
		if e = s.execute(ctx, a); e != nil {
			return nil, e
		}
		a, e = scan(s.Store.Pool.QueryRow(ctx, actionSelect+` WHERE a.org_id=$1 AND a.id=$2 AND a.principal_id=$3`, p.OrgID, id, p.ID))
		if e != nil {
			return nil, e
		}
		outcome = "RESULT_RETURNED"
	}
	result, e := s.storedResult(a)
	if e != nil {
		return nil, e
	}
	var safe any
	if outcome == "NOT_RESUMABLE" {
		safe = map[string]any{"code": "NOT_RESUMABLE", "category": "CONFLICT", "safeMessage": "This action cannot be resumed or cancelled in its current state.", "retryDisposition": "NEVER"}
	}
	return map[string]any{"kind": "ACTION_CONTROL", "operation": op, "requestId": uuid.NewString(), "outcome": outcome, "action": s.view(ctx, a), "result": result, "error": safe}, nil
}
func (s *Service) legacyControl(ctx context.Context, tx pgx.Tx, p metadata.Principal, id, op string) (map[string]any, error) {
	if op != "STATUS" {
		return nil, ErrInput
	}
	a := action{Org: p.OrgID, ID: id, Owner: p.ID, Tool: "pg.get_demo_item", Resource: "demo.pg", Legacy: true, Revision: 1}
	e := tx.QueryRow(ctx, `SELECT a.state,a.plan_hash,a.created_at,e.hard_deadline,e.id::text,e.attempt_id::text,e.result_ciphertext,e.dispatch_state,e.request_status,e.side_effect_status FROM mcpguard.actions a JOIN mcpguard.executions e ON (e.org_id,e.action_id)=(a.org_id,a.id) WHERE a.org_id=$1 AND a.id=$2 AND a.principal_id=$3`, p.OrgID, id, p.ID).Scan(&a.State, &a.Hash, &a.Created, &a.Expires, &a.Execution, &a.Attempt, &a.Result, &a.Dispatch, &a.Status, &a.Side)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ErrDenied
	}
	if e != nil {
		return nil, e
	}
	result, e := s.storedResult(a)
	if e != nil {
		return nil, e
	}
	view := s.view(ctx, a)
	view["availableActions"] = []string{"STATUS"}
	if result != nil {
		view["availableActions"] = []string{"STATUS", "VIEW_RESULT"}
	}
	return map[string]any{"kind": "ACTION_CONTROL", "operation": op, "requestId": uuid.NewString(), "outcome": "STATE_RETURNED", "action": view, "result": result, "error": nil}, nil
}
func final(a action, o Outcome) map[string]any {
	status := "SUCCEEDED"
	var safe any
	if o.Code != "" {
		status = "FAILED"
		retry := "MANUAL_NEW_ACTION"
		if o.SideEffect == "UNKNOWN" || o.Dispatch == "DISPATCH_UNKNOWN" {
			status = "OUTCOME_UNKNOWN"
			retry = "RECONCILE"
		}
		safe = map[string]any{"code": o.Code, "category": "EXECUTION", "safeMessage": "The bounded native operation did not produce a confirmed result. Unknown outcomes must be reconciled; this action cannot be replayed.", "retryDisposition": retry}
	}
	if status != "SUCCEEDED" {
		o.Data = nil
	}
	return map[string]any{"kind": "FINAL", "actionId": a.ID, "attemptId": a.Attempt, "executionId": a.Execution, "requestStatus": status, "dispatchState": o.Dispatch, "sideEffectStatus": o.SideEffect, "data": o.Data, "error": safe, "metadata": map[string]any{"encoding": "json", "truncated": false, "observedAt": time.Now().UTC()}}
}
func (s *Service) execute(ctx context.Context, a action) error {
	ctx, cancel := context.WithDeadline(ctx, a.Deadline)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		tick := time.NewTicker(150 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				var stop bool
				e := s.Store.Pool.QueryRow(ctx, `SELECT cancel_requested FROM mcpguard.managed_actions WHERE org_id=$1 AND id=$2`, a.Org, a.ID).Scan(&stop)
				if e != nil || stop {
					cancel()
					return
				}
			}
		}
	}()
	args, e := s.plan(a)
	o := Outcome{Dispatch: "NOT_DISPATCHED", SideEffect: "NONE", Code: "PLAN_INVALID"}
	if e == nil {
		t, _ := s.Tool(a.Tool)
		nctx, done := context.WithTimeout(ctx, time.Duration(t.Limits.Timeout)*time.Millisecond)
		o = s.Native.Execute(nctx, t, a.Scope, args)
		done()
	}
	r := final(a, o)
	return s.complete(a, r, false)
}
func (s *Service) complete(a action, result map[string]any, recovering bool) error {
	ctx, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer rollback(tx)
	// All completion paths acquire the action lock before the execution row.
	var state string
	if e = tx.QueryRow(ctx, `SELECT state FROM mcpguard.managed_actions WHERE org_id=$1 AND id=$2 FOR UPDATE`, a.Org, a.ID).Scan(&state); e != nil {
		return e
	}
	if state != "EXECUTING" {
		return nil
	}
	b, _ := json.Marshal(result)
	query := `UPDATE mcpguard.managed_executions SET request_status=$3,dispatch_state=$4,side_effect_status=$5,result_ciphertext=$6 WHERE org_id=$1 AND action_id=$2 AND request_status='RUNNING'`
	if recovering {
		query += ` AND hard_deadline<clock_timestamp()-interval '15 seconds'`
	} else {
		query += ` AND owner_instance=$7`
	}
	params := []any{a.Org, a.ID, result["requestStatus"], result["dispatchState"], result["sideEffectStatus"], s.Store.Vault.Seal(b, a.binding("RESULT"))}
	if !recovering {
		params = append(params, s.Store.Instance)
	}
	tag, e := tx.Exec(ctx, query, params...)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if e = s.state(ctx, tx, &a, result["requestStatus"].(string), result["requestStatus"].(string)); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Service) Recover(ctx context.Context) error {
	rows, e := s.Store.Pool.Query(ctx, actionSelect+` WHERE e.request_status='RUNNING' AND e.hard_deadline<clock_timestamp()-interval '15 seconds' LIMIT 32`)
	if e != nil {
		return e
	}
	var actions []action
	for rows.Next() {
		a, e := scan(rows)
		if e != nil {
			rows.Close()
			return e
		}
		actions = append(actions, a)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, a := range actions {
		side := "NONE"
		t, _ := s.Tool(a.Tool)
		if t.Effect == "WRITE" {
			side = "UNKNOWN"
		}
		if e = s.complete(a, final(a, Outcome{Dispatch: "DISPATCH_UNKNOWN", SideEffect: side, Code: "OUTCOME_UNKNOWN"}), true); e != nil {
			return e
		}
	}
	_, e = s.Store.Pool.Exec(ctx, `WITH expired AS (UPDATE mcpguard.managed_actions SET state='EXPIRED',revision=revision+1 WHERE state IN ('PENDING_APPROVAL','READY') AND expires_at<clock_timestamp() RETURNING *) INSERT INTO mcpguard.managed_audit(id,org_id,principal_id,action_id,attempt_id,event_type) SELECT gen_random_uuid(),org_id,principal_id,id,attempt_id,'ACTION_EXPIRED' FROM expired`)
	return e
}
func (s *Service) Result(ctx context.Context, p metadata.Principal, id string) (any, error) {
	if _, e := s.Store.Current(ctx, p); e != nil {
		return nil, ErrDenied
	}
	if _, e := uuid.Parse(id); e != nil {
		return nil, ErrInput
	}
	a, e := scan(s.Store.Pool.QueryRow(ctx, actionSelect+` WHERE a.org_id=$1 AND a.id=$2 AND a.principal_id=$3`, p.OrgID, id, p.ID))
	if e == nil {
		return s.storedResult(a)
	}
	if !errors.Is(e, ErrDenied) {
		return nil, e
	}
	var cipher []byte
	e = s.Store.Pool.QueryRow(ctx, `SELECT e.result_ciphertext FROM mcpguard.actions a JOIN mcpguard.executions e ON (e.org_id,e.action_id)=(a.org_id,a.id) WHERE a.org_id=$1 AND a.id=$2 AND a.principal_id=$3`, p.OrgID, id, p.ID).Scan(&cipher)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ErrDenied
	}
	if e != nil {
		return nil, e
	}
	b, e := s.Store.Vault.Open(cipher, p.OrgID+"/"+id+"/RESULT/local-v1")
	if e != nil {
		return nil, e
	}
	var r any
	e = json.Unmarshal(b, &r)
	return r, e
}
func (s *Service) History(ctx context.Context, p metadata.Principal, after string, limit int) (map[string]any, error) {
	if _, e := s.Store.Current(ctx, p); e != nil {
		return nil, ErrDenied
	}
	if limit < 1 || limit > 100 {
		return nil, ErrInput
	}
	var cursor struct {
		Created time.Time
		ID      string
	}
	cursor.Created = time.Unix(0, 0)
	cursor.ID = uuid.Nil.String()
	if after != "" {
		if len(after) > 256 {
			return nil, ErrInput
		}
		b, e := base64.RawURLEncoding.DecodeString(after)
		if e != nil {
			return nil, ErrInput
		}
		if e = json.Unmarshal(b, &cursor); e != nil || cursor.Created.IsZero() {
			return nil, ErrInput
		}
		if _, e = uuid.Parse(cursor.ID); e != nil {
			return nil, ErrInput
		}
	}
	rows, e := s.Store.Pool.Query(ctx, `SELECT id::text,tool_name,state,created_at FROM (SELECT id,tool_name,state,created_at FROM mcpguard.managed_actions WHERE org_id=$1 AND principal_id=$2 UNION ALL SELECT id,tool_name,state,created_at FROM mcpguard.actions WHERE org_id=$1 AND principal_id=$2) a WHERE (created_at,id)>($3,$4::uuid) ORDER BY created_at,id LIMIT $5`, p.OrgID, p.ID, cursor.Created, cursor.ID, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []any{}
	next := after
	for rows.Next() {
		var id, t, state string
		var created time.Time
		if e = rows.Scan(&id, &t, &state, &created); e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"actionId": id, "toolName": t, "state": state, "createdAt": created})
		cursor.Created = created
		cursor.ID = id
		b, _ := json.Marshal(cursor)
		next = base64.RawURLEncoding.EncodeToString(b)
	}
	return map[string]any{"actions": out, "nextAfter": next}, rows.Err()
}
func (s *Service) String() string {
	return fmt.Sprintf("controlled local profile (%d catalog tools)", len(s.Tools))
}
