package gateway

import (
	"context"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/resources"
)

func (g *Gateway) AttachDownloads(service *resources.DownloadService) {
	g.Downloads = service
	no := false
	g.MCP.AddTool(&mcp.Tool{Name: "credential.prepare_download", Description: "Prepare a short-lived credential download ticket for your active grant. Returns only safe identifiers and state. An independently authenticated private client must claim the file; no package bytes, token or download URL is returned here.", InputSchema: v02.Schema("PrepareDownloadInput"), OutputSchema: v02.Schema("PrepareDownloadResult"), Annotations: &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: &no, OpenWorldHint: &no}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p, ok := principal(req.Extra)
		if !ok {
			return nil, safeProtocolError("Authentication is required")
		}
		if req.Params.RequestState != "" || len(req.Params.InputResponses) > 0 {
			return resourceToolError(resources.ErrInput), nil
		}
		var in v02.PrepareDownloadInput
		if v02.Decode("PrepareDownloadInput", req.Params.Arguments, &in) != nil {
			return resourceToolError(resources.ErrInput), nil
		}
		call, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		stop := context.AfterFunc(g.Root, cancel)
		defer stop()
		select {
		case g.slots <- struct{}{}:
			defer func() { <-g.slots }()
		default:
			return resourceToolError(resources.ErrUnavailable), nil
		}
		var out v02.PrepareDownloadResult
		var e error
		if len(req.Extra.Header.Values(resources.ContextHeader)) > 0 {
			if g.Contexts == nil || len(req.Extra.Header.Values(resources.ContextHeader)) != 1 {
				return resourceToolError(resources.ErrDenied), nil
			}
			var proof resources.ContextProof
			proof, e = resources.ParseContextProof(req.Extra.Header.Get(resources.ContextHeader))
			if e == nil {
				out, e = g.Contexts.Prepare(call, p, proof, in)
			}
		} else {
			out, e = service.Prepare(call, p, in)
		}
		if e != nil {
			return resourceToolError(e), nil
		}
		return managedResult(out, false), nil
	})
}
func privateDownloadPath(path string) bool {
	return path == "/api/v2/credential-downloads" || strings.HasPrefix(path, "/api/v2/credential-downloads/")
}
func (g *Gateway) prepareDownloadHTTP(w http.ResponseWriter, r *http.Request) {
	p, ok := requestPrincipal(r)
	if !ok {
		writeResourceHTTP(w, nil, resources.ErrDenied)
		return
	}
	if r.URL.RawQuery != "" {
		writeResourceHTTP(w, nil, resources.ErrInput)
		return
	}
	var in v02.PrepareDownloadInput
	if e := accessBody(w, r, "PrepareDownloadInput", &in); e != nil {
		writeResourceHTTP(w, nil, e)
		return
	}
	call, cancel := context.WithTimeout(r.Context(), 8*time.Second)
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
	var out v02.PrepareDownloadResult
	var e error
	if len(r.Header.Values(resources.ContextHeader)) > 0 {
		if g.Contexts == nil || len(r.Header.Values(resources.ContextHeader)) != 1 {
			writeResourceHTTP(w, nil, resources.ErrDenied)
			return
		}
		var proof resources.ContextProof
		proof, e = resources.ParseContextProof(r.Header.Get(resources.ContextHeader))
		if e == nil {
			out, e = g.Contexts.Prepare(call, p, proof, in)
		}
	} else {
		out, e = g.Downloads.Prepare(call, p, in)
	}
	writeResourceHTTP(w, out, e)
}
func (g *Gateway) privateDownloadHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	p, ok := requestPrincipal(r)
	if !ok {
		writeResourceHTTP(w, nil, resources.ErrDenied)
		return
	}
	if r.URL.RawQuery != "" || r.Header.Get("Origin") != "" || r.Header.Get("Cookie") != "" || len(r.Header.Values("X-MCPGuard-Delivery-Token")) != 1 || len(r.Header.Values("Authorization")) != 1 {
		writeResourceHTTP(w, nil, resources.ErrDenied)
		return
	}
	if r.Method == http.MethodPost {
		media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || media != "application/json" {
			writeResourceHTTP(w, nil, resources.ErrInput)
			return
		}
		raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, v02.MaxClaimInputBytes))
		var in v02.ClaimDownloadInput
		if e != nil || v02.Decode("ClaimDownloadInput", raw, &in) != nil {
			writeResourceHTTP(w, nil, resources.ErrInput)
			return
		}
	} else if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
		writeResourceHTTP(w, nil, resources.ErrInput)
		return
	}
	call, cancel := context.WithTimeout(r.Context(), 8*time.Second)
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
	proof := r.Header.Get("X-MCPGuard-Delivery-Token")
	in := v02.DownloadIDInput{DownloadID: r.PathValue("id")}
	var contextProof *resources.ContextProof
	if raw := r.Header.Get(resources.ContextHeader); len(r.Header.Values(resources.ContextHeader)) > 0 {
		if g.Contexts == nil || len(r.Header.Values(resources.ContextHeader)) != 1 {
			writeResourceHTTP(w, nil, resources.ErrDenied)
			return
		}
		parsed, e := resources.ParseContextProof(raw)
		if e != nil {
			writeResourceHTTP(w, nil, e)
			return
		}
		contextProof = &parsed
	}
	if r.Method == http.MethodGet {
		var out v02.DownloadTicket
		var e error
		if contextProof != nil {
			e = g.Contexts.VerifyDelivery(call, p, *contextProof, proof)
			if e == nil {
				out, e = g.Contexts.TicketStatus(call, p, *contextProof, in)
			}
		} else {
			out, e = g.Downloads.Status(call, p, proof, in)
		}
		writeResourceHTTP(w, out, e)
		return
	}
	started := false
	send := func(ticket v02.DownloadTicket, digest string, blob []byte) error {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(3 * time.Second))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="mcpguard-`+ticket.CredentialID+`.mgcred"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(blob)))
		w.Header().Set("X-MCPGuard-Package-SHA256", digest)
		started = true
		w.WriteHeader(http.StatusOK)
		n, e := w.Write(blob)
		if e != nil {
			return e
		}
		if n != len(blob) {
			return io.ErrShortWrite
		}
		return http.NewResponseController(w).Flush()
	}
	var e error
	if contextProof != nil {
		e = g.Contexts.Deliver(call, p, proof, *contextProof, in, send)
	} else {
		e = g.Downloads.Deliver(call, p, proof, in, send)
	}
	// Once any private response has started, never append a JSON error or replay.
	if e != nil && !started {
		writeResourceHTTP(w, nil, e)
	}
}
