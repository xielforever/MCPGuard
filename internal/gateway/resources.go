package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"github.com/xielforever/MCPGuard/internal/resources"
	"net/http"
	"strconv"
	"time"
)

// AttachResources publishes only resource discovery. The RA-03 read harness
// lives in *_test.go; pg.get_item remains unregistered until RA-11.
func (g *Gateway) AttachResources(c *resources.Catalog) {
	g.Resources = c
	f := false
	for _, name := range []string{"resource.list", "resource.get"} {
		toolName := name
		input, output := "ListResourcesInput", "ListResourcesResult"
		if name == "resource.get" {
			input, output = "GetResourceInput", "GetResourceResult"
		}
		g.MCP.AddTool(&mcp.Tool{Name: name, Description: "Discover resources visible to your current authenticated identity. Discovery does not grant execution access.", InputSchema: v02.Schema(input), OutputSchema: v02.Schema(output), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, DestructiveHint: &f, OpenWorldHint: &f}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			p, ok := principal(req.Extra)
			if !ok {
				return nil, safeProtocolError("Authentication is required")
			}
			call, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			stop := context.AfterFunc(g.Root, cancel)
			defer stop()
			select {
			case g.slots <- struct{}{}:
				defer func() { <-g.slots }()
			default:
				return resourceToolError(resources.ErrUnavailable), nil
			}
			raw := req.Params.Arguments
			if len(raw) == 0 {
				raw = []byte("{}")
			}
			if req.Params.RequestState != "" || len(req.Params.InputResponses) > 0 {
				return resourceToolError(resources.ErrInput), nil
			}
			var result any
			var err error
			if toolName == "resource.list" {
				var in v02.ListResourcesInput
				if v02.Decode("ListResourcesInput", raw, &in) != nil {
					return resourceToolError(resources.ErrInput), nil
				}
				result, err = c.List(call, p, in)
			} else {
				var in v02.GetResourceInput
				if v02.Decode("GetResourceInput", raw, &in) != nil {
					return resourceToolError(resources.ErrInput), nil
				}
				result, err = c.Get(call, p, in)
			}
			if err != nil {
				return resourceToolError(err), nil
			}
			return managedResult(result, false), nil
		})
	}
}
func resourceError(err error) (int, v02.Error) {
	if errors.Is(err, resources.ErrIdempotency) {
		return 409, v02.Error{Code: "IDEMPOTENCY_CONFLICT", Message: "This request key was already used with different arguments."}
	}
	if errors.Is(err, resources.ErrConflict) {
		return 409, v02.Error{Code: "REVISION_CONFLICT", Message: "The current workflow or resource version no longer matches this command."}
	}
	if errors.Is(err, resources.ErrInput) {
		return 400, v02.Error{Code: "INVALID_ARGUMENTS", Message: "Arguments do not match the resource contract."}
	}
	if errors.Is(err, resources.ErrDenied) {
		return 403, v02.Error{Code: "ACCESS_DENIED", Message: "This resource is not authorized or unavailable."}
	}
	return 503, v02.Error{Code: "DEPENDENCY_UNAVAILABLE", Message: "Current resource authority is unavailable."}
}
func resourceToolError(err error) *mcp.CallToolResult {
	_, safe := resourceError(err)
	raw, _ := json.Marshal(safe)
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}
}
func requestPrincipal(r *http.Request) (metadata.Principal, bool) {
	info := auth.TokenInfoFromContext(r.Context())
	if info == nil {
		return metadata.Principal{}, false
	}
	p, ok := info.Extra["principal"].(metadata.Principal)
	return p, ok
}
func writeResourceHTTP(w http.ResponseWriter, result any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		code, safe := resourceError(err)
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(safe)
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}
func (g *Gateway) resourcesHTTP(w http.ResponseWriter, r *http.Request) {
	p, ok := requestPrincipal(r)
	if !ok {
		writeResourceHTTP(w, nil, resources.ErrDenied)
		return
	}
	var in v02.ListResourcesInput
	q := r.URL.Query()
	for key, values := range q {
		if len(values) != 1 {
			writeResourceHTTP(w, nil, resources.ErrInput)
			return
		}
		switch key {
		case "kind":
			v := values[0]
			in.Kind = &v
		case "after":
			v := values[0]
			in.After = &v
		case "limit":
			v, e := strconv.Atoi(values[0])
			if e != nil {
				writeResourceHTTP(w, nil, resources.ErrInput)
				return
			}
			in.Limit = &v
		default:
			writeResourceHTTP(w, nil, resources.ErrInput)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	out, err := g.Resources.List(ctx, p, in)
	writeResourceHTTP(w, out, err)
}
func (g *Gateway) resourceHTTP(w http.ResponseWriter, r *http.Request) {
	p, ok := requestPrincipal(r)
	if !ok {
		writeResourceHTTP(w, nil, resources.ErrDenied)
		return
	}
	if len(r.URL.Query()) != 0 {
		writeResourceHTTP(w, nil, resources.ErrInput)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	out, err := g.Resources.Get(ctx, p, v02.GetResourceInput{ResourceID: r.PathValue("id")})
	writeResourceHTTP(w, out, err)
}
