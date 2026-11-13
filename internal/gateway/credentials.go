package gateway

import (
	"context"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/resources"
)

// Only non-secret state is public in RA-07. No issue, delivery, decrypt or
// validation-with-plaintext tool is registered before the later client stages.
func (g *Gateway) AttachCredentials(service *resources.CredentialService) {
	g.Credentials = service
	no := false
	g.MCP.AddTool(&mcp.Tool{Name: "credential.status", Description: "Inspect your credential lease state. This returns no package bytes or database secrets and does not grant execution access.", InputSchema: v02.Schema("CredentialIDInput"), OutputSchema: v02.Schema("CredentialStatus"), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, DestructiveHint: &no, OpenWorldHint: &no}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p, ok := principal(req.Extra)
		if !ok {
			return nil, safeProtocolError("Authentication is required")
		}
		if req.Params.RequestState != "" || len(req.Params.InputResponses) > 0 {
			return resourceToolError(resources.ErrInput), nil
		}
		var in v02.CredentialIDInput
		if v02.Decode("CredentialIDInput", req.Params.Arguments, &in) != nil {
			return resourceToolError(resources.ErrInput), nil
		}
		call, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		stop := context.AfterFunc(g.Root, cancel)
		defer stop()
		select {
		case g.slots <- struct{}{}:
			defer func() { <-g.slots }()
		default:
			return resourceToolError(resources.ErrUnavailable), nil
		}
		out, e := service.Status(call, p, in)
		if e != nil {
			return resourceToolError(e), nil
		}
		return managedResult(out, false), nil
	})
}
func (g *Gateway) credentialHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := requestPrincipal(r)
	if !ok {
		writeResourceHTTP(w, nil, resources.ErrDenied)
		return
	}
	if r.URL.RawQuery != "" {
		writeResourceHTTP(w, nil, resources.ErrInput)
		return
	}
	call, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		writeResourceHTTP(w, nil, resources.ErrUnavailable)
		return
	}
	out, e := g.Credentials.Status(call, p, v02.CredentialIDInput{CredentialID: r.PathValue("id")})
	writeResourceHTTP(w, out, e)
}
