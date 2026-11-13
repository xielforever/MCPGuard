package gateway

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/controlled"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"github.com/xielforever/MCPGuard/internal/resources"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (g *Gateway) AttachAccess(s *resources.AccessService, console *controlled.Console) {
	g.Access = s
	if console != nil {
		console.APIExtension = g.accessConsoleHTTP
	}
	for _, spec := range []struct {
		name, input, output string
		read                bool
	}{
		{"access.request", "AccessRequestInput", "AccessRequest", false}, {"access.status", "RequestIDInput", "AccessStatusResult", true}, {"access.cancel", "CancelAccessInput", "AccessRequest", false}, {"access.grants", "ListGrantsInput", "ListGrantsResult", true},
	} {
		tool := spec
		no := false
		g.MCP.AddTool(&mcp.Tool{Name: tool.name, Description: "Manage your resource access requests and inspect approval/provisioning status. Approval does not activate database access.", InputSchema: v02.Schema(tool.input), OutputSchema: v02.Schema(tool.output), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: tool.read, IdempotentHint: true, DestructiveHint: &no, OpenWorldHint: &no}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			p, ok := principal(req.Extra)
			if !ok {
				return nil, safeProtocolError("Authentication required")
			}
			if req.Params.RequestState != "" || len(req.Params.InputResponses) > 0 {
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
			out, e := g.accessCommand(call, p, tool.name, req.Params.Arguments)
			if e != nil {
				return resourceToolError(e), nil
			}
			return managedResult(out, false), nil
		})
	}
}
func (g *Gateway) accessCommand(ctx context.Context, p metadata.Principal, name string, raw []byte) (any, error) {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	switch name {
	case "access.request":
		var in v02.AccessRequestInput
		if v02.Decode("AccessRequestInput", raw, &in) != nil {
			return nil, resources.ErrInput
		}
		return g.Access.Request(ctx, p, in)
	case "access.status":
		var in v02.RequestIDInput
		if v02.Decode("RequestIDInput", raw, &in) != nil {
			return nil, resources.ErrInput
		}
		return g.Access.Status(ctx, p, in.RequestID)
	case "access.cancel":
		var in v02.CancelAccessInput
		if v02.Decode("CancelAccessInput", raw, &in) != nil {
			return nil, resources.ErrInput
		}
		return g.Access.Cancel(ctx, p, in)
	case "access.grants":
		var in v02.ListGrantsInput
		if v02.Decode("ListGrantsInput", raw, &in) != nil {
			return nil, resources.ErrInput
		}
		return g.Access.Grants(ctx, p, in)
	}
	return nil, resources.ErrInput
}
func accessBody(w http.ResponseWriter, r *http.Request, schema string, out any) error {
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, v02.MaxInputBytes))
	if e != nil || v02.Decode(schema, raw, out) != nil {
		return resources.ErrInput
	}
	return nil
}
func accessQuery(raw string) (v02.ListGrantsInput, error) {
	var in v02.ListGrantsInput
	q, e := url.ParseQuery(raw)
	if e != nil {
		return in, resources.ErrInput
	}
	for key, values := range q {
		if len(values) != 1 {
			return in, resources.ErrInput
		}
		switch key {
		case "resourceId":
			in.ResourceID = &values[0]
		case "after":
			in.After = &values[0]
		case "limit":
			v, e := strconv.Atoi(values[0])
			if e != nil {
				return in, resources.ErrInput
			}
			in.Limit = &v
		default:
			return in, resources.ErrInput
		}
	}
	return in, nil
}
func (g *Gateway) accessHTTP(w http.ResponseWriter, r *http.Request) {
	p, ok := requestPrincipal(r)
	if !ok {
		writeResourceHTTP(w, nil, resources.ErrDenied)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	stop := context.AfterFunc(g.Root, cancel)
	defer stop()
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		writeResourceHTTP(w, nil, resources.ErrUnavailable)
		return
	}
	var out any
	var e error
	switch {
	case r.Method == "GET" && r.URL.Path == "/api/v2/access-grants":
		var in v02.ListGrantsInput
		in, e = accessQuery(r.URL.RawQuery)
		if e == nil {
			out, e = g.Access.Grants(ctx, p, in)
		}
	case r.URL.RawQuery != "":
		e = resources.ErrInput
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/v2/access-grants/"):
		out, e = g.Access.Grant(ctx, p, r.PathValue("id"))
	case r.Method == "GET":
		out, e = g.Access.Status(ctx, p, r.PathValue("id"))
	case strings.HasSuffix(r.URL.Path, "/cancel"):
		var in v02.CancelAccessInput
		e = accessBody(w, r, "CancelAccessInput", &in)
		if e == nil && in.RequestID != r.PathValue("id") {
			e = resources.ErrInput
		}
		if e == nil {
			out, e = g.Access.Cancel(ctx, p, in)
		}
	default:
		var in v02.AccessRequestInput
		e = accessBody(w, r, "AccessRequestInput", &in)
		if e == nil {
			out, e = g.Access.Request(ctx, p, in)
		}
	}
	writeResourceHTTP(w, out, e)
}
func (g *Gateway) accessConsoleHTTP(w http.ResponseWriter, r *http.Request, p metadata.Principal) bool {
	const prefix = "/console/api/v2/access-requests"
	if r.URL.Path != prefix && !strings.HasPrefix(r.URL.Path, prefix+"/") {
		return false
	}
	if r.Method == "GET" && r.URL.Path == prefix {
		in, e := accessQuery(r.URL.RawQuery)
		var out any
		if e == nil {
			out, e = g.Access.ReviewQueue(r.Context(), p, v02.ListAccessRequestsInput{ResourceID: in.ResourceID, After: in.After, Limit: in.Limit})
		}
		writeResourceHTTP(w, out, e)
		return true
	}
	rest := strings.TrimPrefix(r.URL.Path, prefix+"/")
	id := strings.TrimSuffix(rest, "/decisions")
	if r.URL.RawQuery != "" {
		writeResourceHTTP(w, nil, resources.ErrInput)
		return true
	}
	if r.Method == "GET" && !strings.Contains(rest, "/") {
		out, e := g.Access.Status(r.Context(), p, id)
		writeResourceHTTP(w, out, e)
		return true
	}
	if r.Method == "POST" && strings.HasSuffix(rest, "/decisions") && !strings.Contains(id, "/") {
		var in v02.DecisionInput
		e := accessBody(w, r, "DecisionInput", &in)
		if e == nil && (r.Header.Get("If-Match") != `"`+in.ExpectedRevision+`"` || r.Header.Get("Idempotency-Key") != in.RequestKey) {
			e = resources.ErrInput
		}
		var out any
		if e == nil {
			out, e = g.Access.Decide(r.Context(), p, id, in)
		}
		writeResourceHTTP(w, out, e)
		return true
	}
	http.NotFound(w, r)
	return true
}
