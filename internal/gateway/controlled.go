package gateway

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xielforever/MCPGuard/internal/controlled"
)

//go:embed controlled-result.schema.json
var controlledResultSchema json.RawMessage

//go:embed controlled-action.schema.json
var controlledActionSchema json.RawMessage

func managedResult(v any, isError bool) *mcp.CallToolResult {
	b, _ := json.Marshal(v)
	return &mcp.CallToolResult{StructuredContent: v, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}, IsError: isError}
}
func managedError(err error) *SafeError {
	code, category, message := "DEPENDENCY_UNAVAILABLE", "DEPENDENCY", "Required storage is unavailable; check status before any new action."
	if errors.Is(err, controlled.ErrDenied) {
		code, category, message = "ACCESS_DENIED", "AUTHORIZATION", "This operation is not authorized or the object is unavailable."
	}
	if errors.Is(err, controlled.ErrInput) {
		code, category, message = "INVALID_ARGUMENTS", "INPUT", "Arguments do not match this tool's fixed schema."
	}
	if errors.Is(err, controlled.ErrConflict) {
		code, category, message = "STATE_CONFLICT", "CONFLICT", "The action or configuration revision has changed."
	}
	if errors.Is(err, controlled.ErrJenkinsSchemaChanged) {
		return &SafeError{"JENKINS_SCHEMA_CHANGED", "CONFLICT", "The Jenkins parameter schema changed. Describe the job again and submit a new request for approval.", "QUERY_STATUS"}
	}
	return &SafeError{code, category, message, "QUERY_STATUS"}
}
func (g *Gateway) AttachControlled(s *controlled.Service, console *controlled.Console, n *controlled.Notifier) {
	g.Controlled = s
	g.Console = console
	g.Notifier = n
	f, t := false, true
	for _, tool := range s.Tools {
		if g.Reader == nil || tool.Name == "pg.get_demo_item" || tool.Name == controlled.JenkinsSubmitTool {
			continue
		}
		name := tool.Name
		destructive := &f
		if tool.Effect == "WRITE" {
			destructive = &t
		}
		g.MCP.AddTool(&mcp.Tool{Name: name, Description: tool.Description, InputSchema: tool.Input, OutputSchema: controlledResultSchema, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: tool.Effect == "READ", IdempotentHint: tool.Effect == "READ", DestructiveHint: destructive, OpenWorldHint: &f}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			p, ok := principal(req.Extra)
			if !ok {
				return nil, safeProtocolError("Authentication is required")
			}
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			stop := context.AfterFunc(g.Root, cancel)
			defer stop()
			select {
			case g.slots <- struct{}{}:
				defer func() { <-g.slots }()
			default:
				return failure("BUSY", "DEPENDENCY", "Gateway concurrency limit reached.").MCP(), nil
			}
			if req.Params.RequestState != "" || len(req.Params.InputResponses) > 0 {
				return failure("INVALID_ARGUMENTS", "INPUT", "Use the explicit action control tools.").MCP(), nil
			}
			v, e := s.Create(ctx, p, name, req.Params.Arguments)
			if e != nil {
				if errors.Is(e, controlled.ErrDenied) || errors.Is(e, controlled.ErrInput) {
					kind := "ACCESS_DENIED"
					if errors.Is(e, controlled.ErrInput) {
						kind = "INPUT_REJECTED"
					}
					r := failure("REJECTED", "AUTHORIZATION", "The controlled request was rejected.")
					r.Error = managedError(e)
					return g.reject(p, r, kind), nil
				}
				r := failure("METADATA_UNAVAILABLE", "DEPENDENCY", "Qualification could not be confirmed; check action history before a new request.")
				return r.MCP(), nil
			}
			return managedResult(v, v["kind"] == "FINAL" && v["requestStatus"] != "SUCCEEDED"), nil
		})
	}
	for _, op := range []string{"STATUS", "RESUME", "CANCEL"} {
		if g.Reader == nil && !s.JenkinsEnabled() && op == "RESUME" {
			continue
		}
		g.registerActionControl(s, op)
	}
	g.registerActionHistory(s, &f)
	if s.JenkinsEnabled() {
		g.registerJenkins(s)
	}
}

func (g *Gateway) registerActionControl(s *controlled.Service, operation string) {
	if operation == "RESUME" {
		if g.actionResumeRegistered {
			return
		}
		g.actionResumeRegistered = true
	}
	f := false
	g.MCP.AddTool(&mcp.Tool{Name: "action." + strings.ToLower(operation), Description: "Inspect, explicitly resume or cancel one of your own controlled actions. Approval is performed by another authorized person in the local console. Never automatically replay an unknown outcome.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"actionId": map[string]any{"type": "string", "format": "uuid"}}, "required": []string{"actionId"}, "additionalProperties": false}, OutputSchema: controlledActionSchema, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: operation == "STATUS", IdempotentHint: operation != "RESUME", DestructiveHint: &f, OpenWorldHint: &f}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p, ok := principal(req.Extra)
		if !ok {
			return nil, safeProtocolError("Authentication is required")
		}
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(g.Root, cancel)
		defer stop()
		var e error
		var v map[string]any
		args, parse := controlled.Object(req.Params.Arguments)
		if parse != nil || len(args) != 1 || req.Params.RequestState != "" || len(req.Params.InputResponses) > 0 {
			e = controlled.ErrInput
		} else {
			id, ok := args["actionId"].(string)
			if !ok {
				e = controlled.ErrInput
			} else {
				select {
				case g.slots <- struct{}{}:
					defer func() { <-g.slots }()
					v, e = g.controlAction(ctx, s, p, req, id, operation)
				default:
					e = errors.New("gateway busy")
				}
			}
		}
		if e != nil {
			return managedResult(map[string]any{"kind": "CONTROL_ERROR", "operation": operation, "requestId": uuid.NewString(), "error": managedError(e)}, true), nil
		}
		return managedResult(v, false), nil
	})
}

func (g *Gateway) registerActionHistory(s *controlled.Service, f *bool) {
	g.MCP.AddTool(&mcp.Tool{Name: "history.list", Description: "List your own actions in creation order, including original PostgreSQL reads. Optional opaque cursor after and limit 1..100. No identity selectors.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"after": map[string]any{"type": "string", "maxLength": 256}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, "additionalProperties": false}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, DestructiveHint: f, OpenWorldHint: f}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p, ok := principal(req.Extra)
		if !ok {
			return nil, safeProtocolError("Authentication is required")
		}
		args, e := controlled.Object(req.Params.Arguments)
		after := ""
		limit := 100
		if e == nil {
			for k, v := range args {
				switch k {
				case "after":
					var ok bool
					after, ok = v.(string)
					if !ok {
						e = controlled.ErrInput
					}
				case "limit":
					n, ok := v.(float64)
					if !ok || n != float64(int(n)) {
						e = controlled.ErrInput
					} else {
						limit = int(n)
					}
				default:
					e = controlled.ErrInput
				}
			}
		}
		if req.Params.RequestState != "" || len(req.Params.InputResponses) > 0 {
			e = controlled.ErrInput
		}
		if e != nil {
			return healthToolError("Invalid history arguments."), nil
		}
		v, e := s.History(ctx, p, after, limit)
		if e != nil {
			return healthToolError(managedError(e).SafeMessage), nil
		}
		return managedResult(v, false), nil
	})
	g.MCP.AddTool(&mcp.Tool{Name: "history.get_result", Description: "Read the encrypted stored result of one of your own actions. No backend operation is repeated.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"actionId": map[string]any{"type": "string", "format": "uuid"}}, "required": []string{"actionId"}, "additionalProperties": false}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, DestructiveHint: f, OpenWorldHint: f}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p, ok := principal(req.Extra)
		if !ok {
			return nil, safeProtocolError("Authentication is required")
		}
		args, e := controlled.Object(req.Params.Arguments)
		id, _ := args["actionId"].(string)
		if e != nil || len(args) != 1 || req.Params.RequestState != "" || len(req.Params.InputResponses) > 0 {
			return healthToolError("Invalid result arguments."), nil
		}
		if g.Writes != nil {
			if _, err := uuid.Parse(id); err != nil {
				return healthToolError("Invalid result arguments."), nil
			}
			owned, err := g.Writes.OwnsAction(ctx, p, id)
			if err != nil {
				return healthToolError(managedError(err).SafeMessage), nil
			}
			if owned {
				proof, err := contextProof(req)
				out, statusErr := g.Writes.Status(ctx, p, id)
				if err != nil || statusErr != nil || out.Result == nil || g.Contexts == nil {
					return healthToolError(managedError(controlled.ErrDenied).SafeMessage), nil
				}
				if _, err = g.Contexts.CheckActivation(ctx, p, proof, out.ResourceID, out.CredentialID); err != nil {
					return healthToolError(managedError(controlled.ErrDenied).SafeMessage), nil
				}
				return managedResult(map[string]any{"actionId": id, "result": writeExecutionResult(out)}, false), nil
			}
		}
		v, e := s.Result(ctx, p, id)
		if e != nil {
			return healthToolError(managedError(e).SafeMessage), nil
		}
		return managedResult(map[string]any{"actionId": id, "result": v}, false), nil
	})
}
