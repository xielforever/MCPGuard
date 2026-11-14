package gateway

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/resources"
)

// AttachWrites publishes the RA-12 controlled write. A call only freezes a plan
// and returns it as DEFERRED; another authorized person must decide it and the
// requester must resume explicitly. Nothing is written by this tool.
func (g *Gateway) AttachWrites(w *resources.WriteService) {
	g.Writes = w
	f := false
	g.MCP.AddTool(&mcp.Tool{
		Name: "pg.update_note",
		Description: "Plan one controlled note update through an active credential. " +
			"The call only freezes a plan and defers it to another authorized person; " +
			"the write is dispatched only after that approval and an explicit resume.",
		InputSchema:  v02.Schema("UpdateNoteInput"),
		OutputSchema: resultSchema,
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: true, DestructiveHint: &f, OpenWorldHint: &f},
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
		out, err := w.Plan(call, p, in, uuid.NewString())
		if err != nil {
			return writeError(err).MCP(), nil
		}
		return Result{Kind: "FINAL", RequestStatus: "SUCCEEDED", DispatchState: "NOT_DISPATCHED", SideEffectStatus: "NONE", Data: out, Metadata: observed()}.MCP(), nil
	})
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

