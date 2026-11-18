package gateway

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/controlled"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"github.com/xielforever/MCPGuard/internal/resources"
)

// AttachWrites publishes the RA-12 controlled write. A call only freezes a plan
// and returns it as DEFERRED; another authorized person must decide it and the
// requester must resume explicitly. Nothing is written by this tool.
func (g *Gateway) AttachWrites(w *resources.WriteService) {
	g.Writes = w
	if g.Controlled != nil && g.Reader == nil {
		g.registerActionControl(g.Controlled, "RESUME")
	}
	f := false
	g.MCP.AddTool(&mcp.Tool{
		Name: "pg.update_note",
		Description: "Plan one controlled note update through an active credential. " +
			"The call only freezes a plan and defers it to another authorized person; " +
			"the write is dispatched only after that approval and an explicit resume.",
		InputSchema:  v02.Schema("UpdateNoteInput"),
		OutputSchema: controlledResultSchema,
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &f, OpenWorldHint: &f},
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
		raw := req.Params.Arguments
		if len(raw) == 0 {
			raw = []byte("{}")
		}
		if req.Params.RequestState != "" || len(req.Params.InputResponses) > 0 {
			return writeError(resources.ErrInput).MCP(), nil
		}
		var in v02.UpdateNoteInput
		if v02.Decode("UpdateNoteInput", raw, &in) != nil {
			return writeError(resources.ErrInput).MCP(), nil
		}
		proof, err := contextProof(req)
		if err != nil || g.Contexts == nil {
			return writeError(resources.ErrDenied).MCP(), nil
		}
		if _, err = g.Contexts.CheckActivation(call, p, proof, in.ResourceID, in.CredentialID); err != nil {
			return writeError(err).MCP(), nil
		}
		out, err := w.Plan(call, p, in, uuid.NewString())
		if err != nil {
			return writeError(err).MCP(), nil
		}
		return managedResult(map[string]any{
			"kind": "DEFERRED", "actionId": out.RequestID, "attemptId": out.RequestID,
			"state": "PENDING_APPROVAL", "executionId": nil, "dispatchState": "NOT_DISPATCHED", "sideEffectStatus": "NONE",
			"approvalUrl": g.Controlled.Origin + "/web/", "nextActions": []string{"STATUS", "CANCEL"}, "expiresAt": out.ExpiresAt,
		}, false), nil
	})
}

func (g *Gateway) controlAction(ctx context.Context, s *controlled.Service, p metadata.Principal, req *mcp.CallToolRequest, id, operation string) (map[string]any, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, controlled.ErrInput
	}
	if g.Writes == nil {
		return s.Control(ctx, p, id, operation)
	}
	call, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	owned, err := g.Writes.OwnsAction(call, p, id)
	if err != nil {
		return nil, err
	}
	if !owned {
		if operation == "RESUME" && g.Reader == nil {
			return nil, controlled.ErrDenied
		}
		return s.Control(ctx, p, id, operation)
	}
	out, err := g.Writes.Status(call, p, id)
	if err != nil {
		return nil, err
	}
	outcome := "STATE_RETURNED"
	var safe *SafeError
	var result *Result
	switch operation {
	case "CANCEL":
		out, err = g.Writes.Cancel(call, p, id)
		outcome = "CANCELLED"
	case "RESUME":
		switch out.State {
		case "DEFERRED":
			outcome = "APPROVAL_PENDING"
		case "EXECUTING":
			outcome = "EXECUTION_IN_PROGRESS"
		case "APPROVED":
			proof, proofErr := contextProof(req)
			if proofErr != nil || g.Contexts == nil {
				return nil, controlled.ErrDenied
			}
			out, err = g.Writes.ResumeInContext(call, p, id, g.Contexts, proof)
			if out.Result != nil {
				outcome, result = "RESULT_RETURNED", writeExecutionResult(out)
			}
		default:
			outcome = "NOT_RESUMABLE"
			safe = &SafeError{"STATE_CONFLICT", "CONFLICT", "This action cannot be dispatched again. Check its status.", "QUERY_STATUS"}
		}
	}
	if errors.Is(err, resources.ErrDenied) {
		return nil, controlled.ErrDenied
	}
	if errors.Is(err, resources.ErrConflict) {
		return nil, controlled.ErrConflict
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"kind": "ACTION_CONTROL", "requestId": uuid.NewString(), "operation": operation, "outcome": outcome,
		"action": writeActionView(out), "result": result, "error": safe}, nil
}

func writeActionView(out resources.WriteStatus) map[string]any {
	state, revision := out.State, 4
	var source, requestStatus, executionID any
	dispatch, side := "NOT_DISPATCHED", "NONE"
	actions := []string{"STATUS"}
	if out.DecidedBy != nil && state != "REJECTED" {
		source = "HUMAN"
	}
	switch state {
	case "DEFERRED":
		state, revision, actions = "PENDING_APPROVAL", 1, append(actions, "CANCEL")
	case "APPROVED":
		state, revision, actions = "READY", 2, append(actions, "RESUME")
	case "EXECUTING":
		revision, requestStatus, executionID, dispatch = 3, "RUNNING", out.RequestID, "INTENT_RECORDED"
	case "SUCCEEDED":
		requestStatus, executionID, dispatch, side = "SUCCEEDED", out.RequestID, "DISPATCHED", "APPLIED"
	case "FAILED":
		requestStatus = "FAILED"
		if out.HasResult {
			executionID, dispatch = out.RequestID, "DISPATCHED"
		}
	case "CANCELLED":
		revision, requestStatus = 2, "CANCELLED"
	case "REJECTED":
		state, revision, requestStatus = "DENIED", 2, "FAILED"
	case "OUTCOME_UNKNOWN":
		requestStatus, executionID, dispatch, side = "OUTCOME_UNKNOWN", out.RequestID, "DISPATCH_UNKNOWN", "UNKNOWN"
	}
	return map[string]any{"actionId": out.RequestID, "state": state, "authorizationSource": source,
		"interactionMode": "CONTROL_FLOW", "revision": revision, "planHash": out.PlanHash, "toolName": resources.WriteToolName,
		"resourceId": out.ResourceID, "createdAt": out.CreatedAt, "expiresAt": out.ExpiresAt, "executionId": executionID,
		"dispatchState": dispatch, "requestStatus": requestStatus, "sideEffectStatus": side, "availableActions": actions}
}

func writeExecutionResult(out resources.WriteStatus) *Result {
	r := failure("EXECUTION_FAILED", "EXECUTION", "The controlled update did not complete successfully.")
	r.ActionID, r.AttemptID, r.ExecutionID = &out.RequestID, &out.RequestID, &out.RequestID
	r.DispatchState, r.Metadata, r.Data = "DISPATCHED", observed(), out.Result
	if out.State == "SUCCEEDED" {
		r.RequestStatus, r.SideEffectStatus, r.Error = "SUCCEEDED", "APPLIED", nil
	}
	return &r
}

func writeError(err error) Result {
	switch {
	case errors.Is(err, resources.ErrInput):
		return failure("INVALID_ARGUMENTS", "INPUT", "Expected one valid resourceId, credentialId, itemId, note and expectedVersion.")
	case errors.Is(err, resources.ErrDenied):
		return failure("ACCESS_DENIED", "AUTHORIZATION", "The credential or resource is not current for this identity; no plan was created.")
	case errors.Is(err, resources.ErrConflict):
		return failure("CONFLICT", "STATE", "The plan state changed; re-read it before acting.")
	default:
		return failure("DEPENDENCY_UNAVAILABLE", "DEPENDENCY", "The write plan could not be recorded; nothing was dispatched.")
	}
}
