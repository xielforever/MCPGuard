package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xielforever/MCPGuard/internal/resources"
)

const highRiskCodeMeta = "mcpguard.dev/authorization-code"

func (g *Gateway) attachHighRiskWrites(w *resources.WriteService) {
	destructive, local := true, false
	g.MCP.AddTool(&mcp.Tool{
		Name: resources.HighRiskToolName,
		Description: "Freeze one approved-schema test order change. Choose a fixture amount update, index creation, or cancellation deletion. " +
			"Only the requester can submit the one-time code using the trusted Harness input after the reviewer approves.",
		InputSchema: map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"resourceId", "credentialId", "operation", "target", "expectedVersion"},
			"properties": map[string]any{
				"resourceId":      map[string]any{"type": "string", "format": "uuid"},
				"credentialId":    map[string]any{"type": "string", "format": "uuid"},
				"operation":       map[string]any{"type": "string", "enum": []string{string(resources.HighRiskUpdateOrder), string(resources.HighRiskCreateIndex), string(resources.HighRiskDeleteOrder)}},
				"target":          map[string]any{"type": "string", "enum": []string{resources.HighRiskUpdateOrderID, resources.HighRiskIndexName, resources.HighRiskDeleteOrderID}},
				"expectedVersion": map[string]any{"type": "integer", "enum": []int{0, 1}},
			},
		},
		OutputSchema: controlledResultSchema,
		Annotations:  &mcp.ToolAnnotations{DestructiveHint: &destructive, OpenWorldHint: &local},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p, ok := principal(req.Extra)
		if !ok {
			return nil, safeProtocolError("Authentication is required")
		}
		call, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		stop := context.AfterFunc(g.Root, cancel)
		defer stop()
		select {
		case g.slots <- struct{}{}:
			defer func() { <-g.slots }()
		default:
			return failure("BUSY", "DEPENDENCY", "Gateway concurrency limit reached; no plan was created.").MCP(), nil
		}
		var in resources.HighRiskPlanInput
		var arguments map[string]json.RawMessage
		decoder := json.NewDecoder(bytes.NewReader(req.Params.Arguments))
		decoder.DisallowUnknownFields()
		if req.Params.RequestState != "" || len(req.Params.InputResponses) > 0 ||
			json.Unmarshal(req.Params.Arguments, &arguments) != nil || len(arguments) != 5 ||
			bytes.Equal(arguments["expectedVersion"], []byte("null")) || decoder.Decode(&in) != nil ||
			!resources.ValidHighRiskTarget(in.Operation, in.Target, in.ExpectedVersion) {
			return failure("INVALID_ARGUMENTS", "INPUT", "Expected one fixed high-risk fixture operation, target and version.").MCP(), nil
		}
		proof, err := contextProof(req)
		if err != nil || g.Contexts == nil {
			return writeError(resources.ErrDenied).MCP(), nil
		}
		if _, err := g.Contexts.CheckActivation(call, p, proof, in.ResourceID, in.CredentialID); err != nil {
			return writeError(err).MCP(), nil
		}
		out, err := w.PlanHighRisk(call, p, in, uuid.NewString())
		if err != nil {
			return writeError(err).MCP(), nil
		}
		return managedResult(map[string]any{
			"kind": "DEFERRED", "actionId": out.RequestID, "attemptId": out.RequestID, "state": "PENDING_APPROVAL",
			"executionId": nil, "dispatchState": "NOT_DISPATCHED", "sideEffectStatus": "NONE",
			"approvalUrl": g.Controlled.Origin + "/web/", "nextActions": []string{"STATUS", "CANCEL"}, "expiresAt": out.ExpiresAt,
		}, false), nil
	})
}
