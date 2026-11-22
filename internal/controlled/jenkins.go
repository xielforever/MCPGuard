package controlled

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"regexp"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xielforever/MCPGuard/internal/jenkins"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

const JenkinsSubmitTool = "jenkins.submit_build"
const jenkinsResource = "jenkins"

var ErrJenkinsSchemaChanged = errors.New("Jenkins parameter definition changed")
var jenkinsRequestKey = regexp.MustCompile(`^[A-Za-z0-9._-]{16,128}$`)

type jenkinsIntegration struct {
	client *jenkins.Client
	tool   Tool
	app    JenkinsAppResolver
}

// JenkinsAppResolver resolves an enabled App granted to the current principal.
// The caller holds the organization's authorization lock in tx.
type JenkinsAppResolver func(context.Context, pgx.Tx, metadata.Principal, string) (*jenkins.Client, int64, error)

// EnableJenkins is called before publishing tools. Operators provision the
// resource, submit grant and reviewer grant independently in metadata.
func (s *Service) EnableJenkins(ctx context.Context, client *jenkins.Client) error {
	if client == nil || s.jenkins != nil && s.jenkins.client != nil {
		return ErrInput
	}
	var migrated bool
	if err := s.Store.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT FROM mcpguard.schema_versions WHERE version=5)`).Scan(&migrated); err != nil {
		return err
	}
	if !migrated {
		return errors.New("apply the metadata migration before enabling Jenkins")
	}
	if err := s.initJenkins(); err != nil {
		return err
	}
	s.jenkins.client = client
	return nil
}

// AttachJenkinsApps is called before serving requests and may coexist with an
// operator-configured file controller. Both use the same controlled action tool.
func (s *Service) AttachJenkinsApps(resolve JenkinsAppResolver) error {
	if resolve == nil || s.jenkins != nil && s.jenkins.app != nil {
		return ErrInput
	}
	if err := s.initJenkins(); err != nil {
		return err
	}
	s.jenkins.app = resolve
	return nil
}

func (s *Service) initJenkins() error {
	if s.jenkins != nil {
		return nil
	}
	ref := map[string]any{"type": "string", "minLength": 1, "maxLength": 2048}
	revision := map[string]any{"type": "string", "minLength": 1, "maxLength": 128}
	key := map[string]any{"type": "string", "pattern": jenkinsRequestKey.String()}
	tool := Tool{
		Name:        JenkinsSubmitTool,
		Resource:    jenkinsResource,
		Effect:      "WRITE",
		Description: "Request approval to build one visible Jenkins job with the parameters from its current definition.",
		Input: map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"jobRef", "jobFullName", "schemaRevision", "parameters", "requestKey"},
			"properties": map[string]any{
				"jobRef": ref, "jobFullName": ref, "schemaRevision": revision, "requestKey": key,
				"parameters":        map[string]any{"type": "object"},
				"connectorId":       map[string]any{"type": "string", "format": "uuid"},
				"connectorRevision": map[string]any{"type": "string", "pattern": "^[1-9][0-9]{0,18}$"},
			},
		},
	}
	compiled, err := compile(tool.Input)
	if err != nil {
		return err
	}
	tool.input = compiled
	tool.Limits.Timeout = 4500
	s.jenkins = &jenkinsIntegration{tool: tool}
	s.Tools = append(s.Tools, tool)
	return nil
}

func (s *Service) JenkinsEnabled() bool { return s.jenkins != nil }

func (s *Service) jenkinsForOrg(orgID string) bool {
	return s.JenkinsEnabled() && (s.jenkins.app != nil || s.legacyJenkinsForOrg(orgID))
}

func (s *Service) legacyJenkinsForOrg(orgID string) bool {
	return s.JenkinsEnabled() && s.jenkins.client != nil && s.jenkins.client.OrgID() == orgID
}

func (s *Service) available(resource string) bool {
	if resource == jenkinsResource {
		return s.JenkinsEnabled()
	}
	return s.Native != nil && s.Native.Available(resource)
}

func (s *Service) resourceBinding(resource string) string {
	if resource == jenkinsResource {
		if s.JenkinsEnabled() && s.jenkins.client != nil {
			return s.jenkins.client.Binding()
		}
		return ""
	}
	if s.Native == nil {
		return ""
	}
	return s.Native.Binding()
}

func (s *Service) jenkinsAccess(ctx context.Context, tx pgx.Tx, p metadata.Principal) (metadata.Principal, error) {
	if !s.jenkinsForOrg(p.OrgID) {
		return p, ErrDenied
	}
	cur, err := s.current(ctx, tx, p)
	if err != nil {
		return p, err
	}
	if !cur.CanRead {
		return cur, ErrDenied
	}
	var granted bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM mcpguard.control_resources r
 WHERE r.org_id=$1 AND r.name='jenkins' AND r.enabled AND mcpguard.has_tool_grant($1,$2,$3))`, cur.OrgID, cur.ID, JenkinsSubmitTool).Scan(&granted)
	if err != nil {
		return cur, err
	}
	if !granted {
		return cur, ErrDenied
	}
	return cur, nil
}

func (s *Service) JenkinsVisible(ctx context.Context, p metadata.Principal) (bool, error) {
	if !s.jenkinsForOrg(p.OrgID) {
		return false, nil
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer rollback(tx)
	_, err = s.jenkinsAccess(ctx, tx, p)
	if errors.Is(err, ErrDenied) {
		return false, nil
	}
	return err == nil, err
}

func (s *Service) LegacyJenkinsVisible(ctx context.Context, p metadata.Principal) (bool, error) {
	if !s.legacyJenkinsForOrg(p.OrgID) {
		return false, nil
	}
	return s.JenkinsVisible(ctx, p)
}

func (s *Service) ListJenkinsJobs(ctx context.Context, p metadata.Principal) ([]jenkins.Job, error) {
	if ok, err := s.LegacyJenkinsVisible(ctx, p); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrDenied
	}
	jobs, err := s.jenkins.client.ListJobs(ctx)
	return jobs, publicJenkinsError(err)
}

func (s *Service) DescribeJenkinsJob(ctx context.Context, p metadata.Principal, ref string) (jenkins.JobDescription, error) {
	if ok, err := s.LegacyJenkinsVisible(ctx, p); err != nil {
		return jenkins.JobDescription{}, err
	} else if !ok {
		return jenkins.JobDescription{}, ErrDenied
	}
	job, err := s.jenkins.client.DescribeJob(ctx, ref)
	return job, publicJenkinsError(err)
}

func publicJenkinsError(err error) error {
	switch {
	case errors.Is(err, jenkins.ErrInput):
		return ErrInput
	case errors.Is(err, jenkins.ErrDenied):
		return ErrDenied
	case errors.Is(err, jenkins.ErrSchemaChanged):
		return ErrJenkinsSchemaChanged
	default:
		return err
	}
}

type jenkinsTarget struct {
	client   *jenkins.Client
	binding  string
	revision string
}

func (s *Service) jenkinsTarget(ctx context.Context, tx pgx.Tx, p metadata.Principal, connectorID string) (jenkinsTarget, error) {
	if connectorID == "" {
		if !s.legacyJenkinsForOrg(p.OrgID) {
			return jenkinsTarget{}, ErrDenied
		}
		return jenkinsTarget{client: s.jenkins.client, binding: s.jenkins.client.Binding()}, nil
	}
	if !s.JenkinsEnabled() || s.jenkins.app == nil {
		return jenkinsTarget{}, ErrDenied
	}
	client, revision, err := s.jenkins.app(ctx, tx, p, connectorID)
	if err != nil {
		return jenkinsTarget{}, err
	}
	if client == nil || revision < 1 || client.OrgID() != p.OrgID || client.ControllerID() != connectorID {
		return jenkinsTarget{}, ErrDenied
	}
	version := strconv.FormatInt(revision, 10)
	return jenkinsTarget{client: client, binding: "jenkins-app-v1/" + connectorID + "/" + version + "/" + client.Binding(), revision: version}, nil
}

func (s *Service) jenkinsPlanTarget(ctx context.Context, tx pgx.Tx, owner metadata.Principal, a action, args map[string]any) (jenkinsTarget, error) {
	if (text(args, "connectorId") == "") != (text(args, "connectorRevision") == "") {
		return jenkinsTarget{}, ErrConflict
	}
	target, err := s.jenkinsTarget(ctx, tx, owner, text(args, "connectorId"))
	if err != nil {
		return jenkinsTarget{}, err
	}
	if target.binding != a.Binding || target.revision != text(args, "connectorRevision") {
		return jenkinsTarget{}, ErrConflict
	}
	return target, nil
}

// Decisions are made by a separate reviewer. Verify the App binding under
// the original owner's current authorization, without requiring reviewers to
// hold a caller grant or to handle the App credentials.
func (s *Service) checkReviewBinding(ctx context.Context, tx pgx.Tx, a action, args map[string]any) error {
	if a.Tool != JenkinsSubmitTool || text(args, "connectorId") == "" {
		if a.Binding != s.resourceBinding(a.Resource) {
			return ErrConflict
		}
		return nil
	}
	owner := metadata.Principal{OrgID: a.Org, ID: a.Owner}
	if err := tx.QueryRow(ctx, `SELECT token_hash FROM mcpguard.principals
 WHERE org_id=$1 AND id=$2 AND enabled AND expires_at>clock_timestamp()`, a.Org, a.Owner).Scan(&owner.TokenHash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		return err
	}
	owner, err := s.jenkinsAccess(ctx, tx, owner)
	if err != nil {
		return err
	}
	if owner.ScopeID != a.Scope {
		return ErrConflict
	}
	_, err = s.jenkinsPlanTarget(ctx, tx, owner, a, args)
	return err
}

func (s *Service) SubmitJenkinsBuild(ctx context.Context, p metadata.Principal, raw []byte) (map[string]any, error) {
	input, err := Object(raw)
	if err != nil {
		return nil, ErrInput
	}
	var connectorID string
	if _, hasConnector := input["connectorId"]; hasConnector {
		id, err := uuid.Parse(text(input, "connectorId"))
		if err != nil || !exact(input, "connectorId", "jobRef", "schemaRevision", "parameters", "requestKey") {
			return nil, ErrInput
		}
		connectorID = id.String()
		input["connectorId"] = connectorID
	} else if !exact(input, "jobRef", "schemaRevision", "parameters", "requestKey") {
		return nil, ErrInput
	}
	ref, rev, key := text(input, "jobRef"), text(input, "schemaRevision"), text(input, "requestKey")
	parameters, ok := input["parameters"].(map[string]any)
	if !ok || len(ref) == 0 || len(ref) > 2048 || len(rev) == 0 || len(rev) > 128 || !jenkinsRequestKey.MatchString(key) {
		return nil, ErrInput
	}
	old, target, err := s.previousJenkinsRequest(ctx, p, connectorID, key, input)
	if err != nil || old != nil {
		return old, err
	}
	plan, err := target.client.Prepare(ctx, ref, rev, maps.Clone(parameters))
	if err != nil {
		return nil, publicJenkinsError(err)
	}
	args := map[string]any{
		"jobRef": plan.JobRef, "jobFullName": plan.FullName, "schemaRevision": plan.SchemaRevision,
		"parameters": plan.Parameters, "requestKey": key,
	}
	if connectorID != "" {
		args["connectorId"] = connectorID
		args["connectorRevision"] = target.revision
	}
	frozen, err := json.Marshal(args)
	if err != nil {
		return nil, ErrInput
	}
	if _, err = s.jenkins.tool.Arguments(frozen); err != nil {
		return nil, ErrInput
	}
	return s.createJenkinsRequest(ctx, p, connectorID, key, target.binding, input, args)
}

func (s *Service) previousJenkinsRequest(ctx context.Context, p metadata.Principal, connectorID, key string, input map[string]any) (map[string]any, jenkinsTarget, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, jenkinsTarget{}, err
	}
	defer rollback(tx)
	cur, err := s.jenkinsAccess(ctx, tx, p)
	if err != nil {
		return nil, jenkinsTarget{}, err
	}
	target, err := s.jenkinsTarget(ctx, tx, cur, connectorID)
	if err != nil {
		return nil, jenkinsTarget{}, err
	}
	previous, _, err := s.operation(ctx, tx, p, JenkinsSubmitTool, key, input)
	return previous, target, err
}

func (s *Service) createJenkinsRequest(ctx context.Context, p metadata.Principal, connectorID, key, binding string, input, args map[string]any) (map[string]any, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	cur, err := s.jenkinsAccess(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	target, err := s.jenkinsTarget(ctx, tx, cur, connectorID)
	if err != nil {
		return nil, err
	}
	if target.binding != binding {
		return nil, ErrConflict
	}
	config, err := s.head(ctx, tx, p.OrgID)
	if err != nil {
		return nil, err
	}
	// Serialize only callers competing for this request key. The unique
	// operation key and its action are also committed in one transaction.
	lockKey := p.OrgID + "/" + p.ID + "/" + JenkinsSubmitTool + "/" + key
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 1854201949))`, lockKey); err != nil {
		return nil, err
	}
	previous, digest, err := s.operation(ctx, tx, p, JenkinsSubmitTool, key, input)
	if err != nil || previous != nil {
		return previous, err
	}
	tool := s.jenkins.tool
	if _, err := s.authorize(ctx, tx, cur, tool, args); err != nil {
		return nil, err
	}
	a, err := s.recordAction(ctx, tx, cur, tool, config, true, binding, args)
	if err != nil {
		return nil, err
	}
	out := s.deferred(a)
	if err := s.saveOperation(ctx, tx, p, JenkinsSubmitTool, key, digest, out); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func executeJenkins(ctx context.Context, client *jenkins.Client, args map[string]any) Outcome {
	params, _ := args["parameters"].(map[string]any)
	receipt, err := client.Submit(ctx, jenkins.BuildPlan{
		JobRef: text(args, "jobRef"), FullName: text(args, "jobFullName"),
		SchemaRevision: text(args, "schemaRevision"), Parameters: params,
	})
	switch {
	case err == nil:
		return Outcome{Dispatch: "DISPATCHED", SideEffect: "APPLIED", Data: receipt}
	case errors.Is(err, jenkins.ErrSchemaChanged):
		return Outcome{Dispatch: "NOT_DISPATCHED", SideEffect: "NONE", Code: "JENKINS_SCHEMA_CHANGED"}
	case errors.Is(err, jenkins.ErrOutcomeUnknown):
		return Outcome{Dispatch: "DISPATCH_UNKNOWN", SideEffect: "UNKNOWN", Code: "OUTCOME_UNKNOWN"}
	case errors.Is(err, jenkins.ErrRejected):
		return Outcome{Dispatch: "DISPATCHED", SideEffect: "NONE", Code: "JENKINS_BUILD_REJECTED"}
	default:
		return Outcome{Dispatch: "NOT_DISPATCHED", SideEffect: "NONE", Code: "DEPENDENCY_UNAVAILABLE"}
	}
}

func (s *Service) OwnsJenkinsAction(ctx context.Context, p metadata.Principal, id string) (bool, error) {
	if _, err := uuid.Parse(id); err != nil {
		return false, ErrInput
	}
	if !s.jenkinsForOrg(p.OrgID) {
		return false, nil
	}
	var exists bool
	err := s.Store.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT FROM mcpguard.managed_actions
 WHERE org_id=$1 AND id=$2 AND principal_id=$3 AND resource_name='jenkins' AND tool_name=$4)`,
		p.OrgID, id, p.ID, JenkinsSubmitTool).Scan(&exists)
	return exists, err
}

func (s *Service) JenkinsQueue(ctx context.Context, p metadata.Principal, id string) (jenkins.QueueState, error) {
	var result jenkins.QueueState
	if _, err := uuid.Parse(id); err != nil {
		return result, ErrInput
	}
	receipt, client, err := s.jenkinsReceipt(ctx, p, id)
	if err != nil {
		return result, err
	}
	queue, err := client.Queue(ctx, receipt)
	return queue, publicJenkinsError(err)
}

func (s *Service) jenkinsReceipt(ctx context.Context, p metadata.Principal, id string) (jenkins.Receipt, *jenkins.Client, error) {
	var receipt jenkins.Receipt
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return receipt, nil, err
	}
	defer rollback(tx)
	cur, err := s.jenkinsAccess(ctx, tx, p)
	if err != nil {
		return receipt, nil, err
	}
	a, err := scan(tx.QueryRow(ctx, actionSelect+` WHERE a.org_id=$1 AND a.id=$2 AND a.principal_id=$3 AND a.tool_name=$4`, p.OrgID, id, p.ID, JenkinsSubmitTool))
	if err != nil {
		return receipt, nil, err
	}
	if a.State != "SUCCEEDED" {
		return receipt, nil, ErrConflict
	}
	args, err := s.plan(a)
	if err != nil {
		return receipt, nil, err
	}
	target, err := s.jenkinsPlanTarget(ctx, tx, cur, a, args)
	if err != nil {
		return receipt, nil, err
	}
	stored, err := s.storedResult(a)
	if err != nil {
		return receipt, nil, err
	}
	body, err := json.Marshal(stored["data"])
	if err != nil {
		return receipt, nil, err
	}
	if err = json.Unmarshal(body, &receipt); err != nil || receipt.QueueID <= 0 {
		return jenkins.Receipt{}, nil, ErrConflict
	}
	return receipt, target.client, nil
}
