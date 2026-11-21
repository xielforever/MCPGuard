package gateway

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xielforever/MCPGuard/internal/controlled"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

const (
	jenkinsListTool     = "jenkins.list_jobs"
	jenkinsDescribeTool = "jenkins.describe_job"
	jenkinsQueueTool    = "jenkins.get_queue"
)

func (g *Gateway) registerJenkins(s *controlled.Service) {
	jobRef := map[string]any{"type": "string", "minLength": 1, "maxLength": 2048, "description": "Opaque jobRef returned by jenkins.list_jobs."}
	tools := []struct {
		name, description string
		properties        map[string]any
		required          []string
	}{
		{jenkinsListTool, "List Jenkins jobs in the allowed folders of the configured controller. Use the returned jobRef to describe a job.", nil, nil},
		{jenkinsDescribeTool, "Read the current Jenkins job parameters, choices and defaults. Use the returned schemaRevision when submitting for approval.", map[string]any{"jobRef": jobRef}, []string{"jobRef"}},
		{controlled.JenkinsSubmitTool, "Freeze one Jenkins build request for independent console approval. Supply a new requestKey for each intended build and reuse that key for any retry of the same request. After approval, explicitly call action.resume with the actionId.", map[string]any{
			"jobRef":         jobRef,
			"schemaRevision": map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
			"parameters":     map[string]any{"type": "object", "description": "Parameter values keyed by their names from jenkins.describe_job. Omit individual optional values to use the reviewed defaults."},
			"requestKey":     map[string]any{"type": "string", "pattern": "^[A-Za-z0-9._-]{16,128}$"},
		}, []string{"jobRef", "schemaRevision", "parameters", "requestKey"}},
		{jenkinsQueueTool, "Read Jenkins queue state using the stored receipt of one of your own actions. If the dispatch result is unknown, check action.status and never submit a replacement automatically.", map[string]any{"actionId": map[string]any{"type": "string", "format": "uuid"}}, []string{"actionId"}},
	}
	openWorld, destructive := true, false
	for _, tool := range tools {
		input := map[string]any{"type": "object", "additionalProperties": false}
		if len(tool.properties) != 0 {
			input["properties"] = tool.properties
			input["required"] = tool.required
		}
		g.MCP.AddTool(&mcp.Tool{
			Name: tool.name, Description: tool.description, InputSchema: input, OutputSchema: map[string]any{"type": "object"},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: tool.name != controlled.JenkinsSubmitTool, IdempotentHint: true, OpenWorldHint: &openWorld, DestructiveHint: &destructive},
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			p, ok := principal(req.Extra)
			if !ok {
				return nil, safeProtocolError("Authentication is required")
			}
			if req.Params.RequestState != "" || len(req.Params.InputResponses) > 0 {
				return jenkinsToolError(controlled.ErrInput), nil
			}
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			stop := context.AfterFunc(g.Root, cancel)
			defer stop()
			select {
			case g.slots <- struct{}{}:
				defer func() { <-g.slots }()
			default:
				return managedResult(map[string]any{"error": &SafeError{"BUSY", "DEPENDENCY", "Gateway concurrency limit reached.", "QUERY_STATUS"}}, true), nil
			}
			result, err := callJenkins(ctx, s, p, tool.name, req.Params.Arguments)
			if err != nil {
				return jenkinsToolError(err), nil
			}
			return managedResult(result, false), nil
		})
	}
}

func callJenkins(ctx context.Context, s *controlled.Service, p metadata.Principal, name string, raw json.RawMessage) (any, error) {
	if name == controlled.JenkinsSubmitTool {
		return s.SubmitJenkinsBuild(ctx, p, raw)
	}
	if name == jenkinsListTool && len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	args, err := controlled.Object(raw)
	if err != nil {
		return nil, controlled.ErrInput
	}
	switch name {
	case jenkinsListTool:
		if len(args) != 0 {
			return nil, controlled.ErrInput
		}
		jobs, err := s.ListJenkinsJobs(ctx, p)
		return map[string]any{"jobs": jobs}, err
	case jenkinsDescribeTool:
		ref, _ := args["jobRef"].(string)
		if len(args) != 1 || ref == "" || len(ref) > 2048 {
			return nil, controlled.ErrInput
		}
		return s.DescribeJenkinsJob(ctx, p, ref)
	case jenkinsQueueTool:
		id, _ := args["actionId"].(string)
		if len(args) != 1 || id == "" {
			return nil, controlled.ErrInput
		}
		return s.JenkinsQueue(ctx, p, id)
	}
	return nil, controlled.ErrInput
}

func jenkinsToolError(err error) *mcp.CallToolResult {
	return managedResult(map[string]any{"error": managedError(err)}, true)
}

func (g *Gateway) controlManagedAction(ctx context.Context, s *controlled.Service, p metadata.Principal, id, operation string) (map[string]any, error) {
	if operation == "RESUME" && g.Reader == nil {
		if !s.JenkinsEnabled() {
			return nil, controlled.ErrDenied
		}
		owned, err := s.OwnsJenkinsAction(ctx, p, id)
		if err != nil {
			return nil, err
		}
		if !owned {
			return nil, controlled.ErrDenied
		}
	}
	return s.Control(ctx, p, id, operation)
}
