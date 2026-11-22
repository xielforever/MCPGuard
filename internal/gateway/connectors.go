package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xielforever/MCPGuard/internal/connectors"
	"github.com/xielforever/MCPGuard/internal/controlled"
	"github.com/xielforever/MCPGuard/internal/jenkins"
	"github.com/xielforever/MCPGuard/internal/metadata"
	connector "github.com/xielforever/MCPGuard/pkg/connectors"
)

type connectorRuntime struct {
	registry  *connectors.Registry
	providers *connector.Providers
}

type connectorTool struct {
	name, description string
	kind              connector.Kind
	operation, arg    string
	limit             int
}

var connectorTools = []connectorTool{
	{name: "connector.list", description: "List enabled connectors granted to the current caller. Use their IDs with the matching connector tools."},
	{name: "connector.pg.list_tables", description: "List accessible tables in a PostgreSQL connector's configured schema.", kind: connector.PostgreSQL, operation: connector.ListTables},
	{name: "connector.redis.get", description: "Get a key within a Redis connector's configured key prefix.", kind: connector.Redis, operation: connector.GetKey, arg: "key", limit: connector.MaxRedisKeyBytes},
	{name: "connector.jenkins.list_jobs", description: "List jobs in a Jenkins connector's configured folder.", kind: connector.Jenkins, operation: connector.ListJobs},
	{name: "connector.jenkins.describe_job", description: "Read the current parameters of a job returned by connector.jenkins.list_jobs.", kind: connector.Jenkins, operation: connector.DescribeJob, arg: "jobRef", limit: 2048},
	{name: "connector.k8s.list_deployments", description: "Read deployments in a Kubernetes connector's configured namespace.", kind: connector.Kubernetes, operation: connector.ListDeployments},
}

func (g *Gateway) AttachConnectors(ctx context.Context, cidrs string) error {
	if g.Console == nil || g.Controlled == nil || g.connectors != nil {
		return errors.New("connector management requires a single configured console")
	}
	policy, err := connector.NewEgressPolicy(cidrs)
	if err != nil {
		return errors.New("invalid MCPGUARD_CONNECTOR_ALLOWED_CIDRS")
	}
	registry := connectors.NewRegistry(g.Store)
	if err := registry.Check(ctx); err != nil {
		return err
	}
	rt := &connectorRuntime{registry, connector.NewProviders(policy)}
	if err := g.Controlled.AttachJenkinsApps(rt.jenkinsForBuild); err != nil {
		return err
	}
	g.connectors = rt
	g.Console.ConnectorRegistry = registry
	previous := g.Console.APIExtension
	g.Console.APIExtension = func(w http.ResponseWriter, r *http.Request, p metadata.Principal) bool {
		if rt.serveConsole(w, r, p) {
			return true
		}
		return previous != nil && previous(w, r, p)
	}
	for _, tool := range connectorTools {
		g.registerConnectorTool(rt, tool)
	}
	g.registerJenkins(g.Controlled)
	g.registerActionControl(g.Controlled, "RESUME")
	return nil
}

func (rt *connectorRuntime) jenkinsForBuild(ctx context.Context, tx pgx.Tx, p metadata.Principal, id string) (*jenkins.Client, int64, error) {
	entry, err := rt.registry.GetUsableInTx(ctx, tx, p, id)
	if err != nil {
		return nil, 0, connectorManagedError(err)
	}
	if entry.Type != connector.Jenkins || !entry.Config.AllowBuilds {
		return nil, 0, controlled.ErrDenied
	}
	client, err := rt.providers.JenkinsForBuild(entry)
	return client, entry.Revision, connectorManagedError(err)
}

func (g *Gateway) registerConnectorTool(rt *connectorRuntime, tool connectorTool) {
	fields := map[string]any{}
	required := []string{}
	if tool.kind != "" {
		fields["connectorId"] = map[string]any{"type": "string", "format": "uuid"}
		required = append(required, "connectorId")
	}
	if tool.arg != "" {
		fields[tool.arg] = map[string]any{"type": "string", "minLength": 1, "maxLength": tool.limit}
		required = append(required, tool.arg)
	}
	openWorld, destructive := true, false
	g.MCP.AddTool(&mcp.Tool{
		Name: tool.name, Description: tool.description,
		InputSchema:  map[string]any{"type": "object", "properties": fields, "required": required, "additionalProperties": false},
		OutputSchema: map[string]any{"type": "object"},
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &openWorld, DestructiveHint: &destructive},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p, ok := principal(req.Extra)
		if !ok {
			return nil, safeProtocolError("Authentication is required")
		}
		if req.Params.RequestState != "" || len(req.Params.InputResponses) > 0 {
			return connectorToolError(connectors.ErrInput), nil
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		stop := context.AfterFunc(g.Root, cancel)
		defer stop()
		select {
		case g.slots <- struct{}{}:
			defer func() { <-g.slots }()
		default:
			return managedResult(map[string]any{"error": &SafeError{"BUSY", "DEPENDENCY", "Gateway concurrency limit reached.", "QUERY_STATUS"}}, true), nil
		}
		out, err := rt.call(ctx, p, tool, req.Params.Arguments, len(required))
		if err != nil {
			return connectorToolError(err), nil
		}
		return managedResult(out, false), nil
	})
}

func (rt *connectorRuntime) call(ctx context.Context, p metadata.Principal, tool connectorTool, raw json.RawMessage, fieldCount int) (any, error) {
	if fieldCount == 0 && len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	fields, err := controlled.Object(raw)
	if err != nil || len(fields) != fieldCount {
		return nil, connectors.ErrInput
	}
	if tool.kind == "" {
		list, err := rt.registry.ListUsable(ctx, p)
		return map[string]any{"connectors": list}, err
	}
	id, _ := fields["connectorId"].(string)
	if _, err := uuid.Parse(id); err != nil {
		return nil, connectors.ErrInput
	}
	var input connector.ReadInput
	if tool.arg != "" {
		value, _ := fields[tool.arg].(string)
		if value == "" || len(value) > tool.limit {
			return nil, connectors.ErrInput
		}
		if tool.arg == "key" {
			input.Key = value
		} else {
			input.JobRef = value
		}
	}
	entry, err := rt.registry.GetUsable(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if entry.Type != tool.kind {
		return nil, connectors.ErrInput
	}
	return rt.providers.Read(ctx, entry, tool.operation, input)
}

func connectorToolError(err error) *mcp.CallToolResult {
	return managedResult(map[string]any{"error": managedError(connectorManagedError(err))}, true)
}

func connectorManagedError(err error) error {
	switch {
	case errors.Is(err, connectors.ErrInput):
		return controlled.ErrInput
	case errors.Is(err, connectors.ErrDenied), errors.Is(err, connectors.ErrNotFound):
		return controlled.ErrDenied
	case errors.Is(err, connectors.ErrConflict):
		return controlled.ErrConflict
	default:
		return err
	}
}

var connectorRequestKey = regexp.MustCompile(`^[A-Za-z0-9._-]{16,128}$`)

func (rt *connectorRuntime) serveConsole(w http.ResponseWriter, r *http.Request, p metadata.Principal) bool {
	const base = "/console/api/connectors"
	if r.URL.Path != base && !strings.HasPrefix(r.URL.Path, base+"/") {
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 7*time.Second)
	defer cancel()
	admin, err := rt.registry.IsAdmin(ctx, p)
	if err != nil {
		writeConnectorReply(w, nil, err)
		return true
	}
	if !admin {
		writeConnectorReply(w, nil, connectors.ErrDenied)
		return true
	}
	out, err := rt.consoleCommand(ctx, w, r, p, strings.TrimPrefix(r.URL.Path, base))
	writeConnectorReply(w, out, err)
	return true
}

func (rt *connectorRuntime) consoleCommand(ctx context.Context, w http.ResponseWriter, r *http.Request, p metadata.Principal, tail string) (any, error) {
	if tail == "" && r.Method == http.MethodGet {
		list, err := rt.registry.ListAdmin(ctx, p)
		return map[string]any{"connectors": list}, err
	}
	parts := strings.Split(strings.TrimPrefix(tail, "/"), "/")
	id := parts[0]
	if len(parts) == 2 && parts[1] == "events" && r.Method == http.MethodGet {
		events, err := rt.registry.Events(ctx, p, id)
		return map[string]any{"events": events}, err
	}
	key := r.Header.Get("Idempotency-Key")
	if !connectorRequestKey.MatchString(key) {
		return nil, connectors.ErrInput
	}
	if tail == "" && r.Method == http.MethodPost {
		var data connectors.CreateInput
		if err := decodeConnectorBody(w, r, &data); err != nil {
			return nil, err
		}
		config, err := rt.providers.Validate(ctx, data.Type, data.Config, data.Credentials)
		if err != nil {
			return nil, err
		}
		data.Config = config
		return rt.registry.Create(ctx, p, data, key)
	}
	if _, err := uuid.Parse(id); err != nil {
		return nil, connectors.ErrInput
	}
	tag := r.Header.Get("If-Match")
	if len(tag) < 3 || tag[0] != '"' || tag[len(tag)-1] != '"' {
		return nil, connectors.ErrInput
	}
	revision, err := strconv.ParseInt(tag[1:len(tag)-1], 10, 64)
	if err != nil || revision < 1 || strconv.FormatInt(revision, 10) != tag[1:len(tag)-1] {
		return nil, connectors.ErrInput
	}
	if len(parts) == 2 && parts[1] == "test" && r.Method == http.MethodPost {
		if err := decodeConnectorBody(w, r, &struct{}{}); err != nil {
			return nil, err
		}
		return rt.registry.Test(ctx, p, id, revision, key, rt.providers.Probe)
	}
	if len(parts) == 1 && r.Method == http.MethodPatch {
		var data struct {
			Enabled *bool `json:"enabled"`
		}
		if err := decodeConnectorBody(w, r, &data); err != nil || data.Enabled == nil {
			return nil, connectors.ErrInput
		}
		return rt.registry.SetEnabled(ctx, p, id, revision, key, *data.Enabled)
	}
	return nil, connectors.ErrNotFound
}

func decodeConnectorBody(w http.ResponseWriter, r *http.Request, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return connectors.ErrInput
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return connectors.ErrInput
	}
	return nil
}

func writeConnectorReply(w http.ResponseWriter, value any, err error) {
	switch {
	case err == nil:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	case errors.Is(err, connectors.ErrInput):
		http.Error(w, "连接器配置或请求无效", http.StatusBadRequest)
	case errors.Is(err, connectors.ErrDenied):
		http.Error(w, "未获准管理连接器，或目标地址超出允许范围", http.StatusForbidden)
	case errors.Is(err, connectors.ErrNotFound):
		http.Error(w, "连接器不存在", http.StatusNotFound)
	case errors.Is(err, connectors.ErrConflict):
		http.Error(w, "连接器状态已变化，或当前配置尚未通过测试", http.StatusConflict)
	default:
		http.Error(w, "连接器操作暂不可用", http.StatusServiceUnavailable)
	}
}
