package gateway

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/resources"
)

// AttachReads publishes the RA-11 planned read. The tool exists only when the
// operator attaches this service explicitly; the resource catalog, credential
// package and context stages are still required for a call to succeed.
func (g *Gateway) AttachReads(r *resources.ReadService) {
	g.Reads = r
	f := false
	g.MCP.AddTool(&mcp.Tool{
		Name: "pg.get_item",
		Description: "Read one fixed item through an active resource credential. " +
			"The resource and credential are explicit; identity, scope and native secrets stay on the server. " +
			"Nothing is dispatched when the credential, binding or resource version is no longer current.",
		InputSchema:  v02.Schema("ReadItemInput"),
		OutputSchema: resultSchema,
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, DestructiveHint: &f, OpenWorldHint: &f},
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
			return failure("BUSY", "DEPENDENCY", "Gateway concurrency limit reached; nothing was dispatched.").MCP(), nil
		}
		raw := req.Params.Arguments
		if len(raw) == 0 {
			raw = []byte("{}")
		}
		if req.Params.RequestState != "" || len(req.Params.InputResponses) > 0 {
			return readError(resources.ErrInput).MCP(), nil
		}
		var in v02.ReadItemInput
		if v02.Decode("ReadItemInput", raw, &in) != nil {
			return readError(resources.ErrInput).MCP(), nil
		}
		out, err := r.ReadItem(call, p, in)
		if err != nil {
			return readError(err).MCP(), nil
		}
		return Result{Kind: "FINAL", RequestStatus: "SUCCEEDED", DispatchState: "DISPATCHED", SideEffectStatus: "NONE", Data: out, Metadata: observed()}.MCP(), nil
	})
}

func readError(err error) Result {
	switch {
	case errors.Is(err, resources.ErrInput):
		return failure("INVALID_ARGUMENTS", "INPUT", "Expected one valid resourceId, credentialId and itemId; scope and SQL cannot be supplied.")
	case errors.Is(err, resources.ErrDenied):
		return failure("ACCESS_DENIED", "AUTHORIZATION", "The credential, binding or resource is not current for this identity; nothing was dispatched.")
	default:
		return failure("DEPENDENCY_UNAVAILABLE", "DEPENDENCY", "The native read could not be planned or completed; nothing was dispatched.")
	}
}
