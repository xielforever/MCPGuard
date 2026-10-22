package gateway

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"github.com/xielforever/MCPGuard/internal/nativepg"
	"github.com/xielforever/MCPGuard/internal/wireids"
)

const Profile = "2025-11-25"

// Mirror of docs/specs/tool-result.schema.json, equality checked by a test.
//
//go:embed tool-result.schema.json
var resultSchema json.RawMessage
var itemPattern = regexp.MustCompile(`^item_demo_[a-z0-9_]{1,32}$`)

type Gateway struct {
	Store  *metadata.Store
	Reader *nativepg.Reader
	Root   context.Context
	MCP    *mcp.Server
	slots  chan struct{}
}

func New(root context.Context, s *metadata.Store, r *nativepg.Reader) *Gateway {
	g := &Gateway{Store: s, Reader: r, Root: root, slots: make(chan struct{}, 16)}
	g.MCP = mcp.NewServer(&mcp.Implementation{Name: "MCPGuard", Version: "0.1.0-read-slice"}, &mcp.ServerOptions{SupportedProtocolVersions: []string{Profile}, Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}}})
	f := false
	g.MCP.AddTool(&mcp.Tool{Name: "pg.get_demo_item", Description: "Read one demo item in your authorized scope. Scope is injected by the gateway.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"itemId": map[string]any{"type": "string", "pattern": itemPattern.String(), "maxLength": 42}}, "required": []string{"itemId"}, "additionalProperties": false}, OutputSchema: resultSchema, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, DestructiveHint: &f, OpenWorldHint: &f}}, g.call)
	g.MCP.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/list" {
				return next(ctx, method, req)
			}
			p, ok := principal(req.GetExtra())
			if !ok {
				return nil, safeProtocolError("Authentication is required")
			}
			c, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			cur, e := g.Store.Current(c, p)
			if e != nil {
				return nil, safeProtocolError("Identity or authorization is unavailable")
			}
			res, e := next(ctx, method, req)
			if e != nil {
				return nil, e
			}
			if !cur.CanRead {
				res.(*mcp.ListToolsResult).Tools = []*mcp.Tool{}
			}
			return res, nil
		}
	})
	return g
}
func safeProtocolError(message string) error {
	return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: message}
}
func principal(extra *mcp.RequestExtra) (metadata.Principal, bool) {
	if extra == nil || extra.TokenInfo == nil {
		return metadata.Principal{}, false
	}
	p, ok := extra.TokenInfo.Extra["principal"].(metadata.Principal)
	return p, ok
}

// Strictly decode the one permitted field, including duplicate-key rejection.
func parseItem(raw []byte) (string, error) {
	if len(raw) > 256 {
		return "", errors.New("arguments too large")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	t, e := d.Token()
	if e != nil || t != json.Delim('{') {
		return "", errors.New("object required")
	}
	var item string
	seen := false
	for d.More() {
		k, e := d.Token()
		if e != nil || k != "itemId" || seen {
			return "", errors.New("unexpected or duplicate field")
		}
		seen = true
		if e = d.Decode(&item); e != nil {
			return "", e
		}
	}
	if _, e = d.Token(); e != nil {
		return "", e
	}
	if _, e = d.Token(); e != io.EOF {
		return "", errors.New("trailing input")
	}
	if !seen || !itemPattern.MatchString(item) {
		return "", errors.New("invalid item id")
	}
	return item, nil
}

func (g *Gateway) reject(p metadata.Principal, r Result, kind string) *mcp.CallToolResult {
	c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if e := g.Store.Rejection(c, p, kind); e != nil {
		return failure("AUDIT_UNAVAILABLE", "DEPENDENCY", "Required audit storage is unavailable; nothing was dispatched.").MCP()
	}
	return r.MCP()
}

func (g *Gateway) call(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
		return failure("BUSY", "DEPENDENCY", "Gateway concurrency limit reached; nothing was dispatched.").MCP(), nil
	}
	item, e := parseItem(req.Params.Arguments)
	if e != nil || req.Params.RequestState != "" || len(req.Params.InputResponses) > 0 {
		return g.reject(p, failure("INVALID_ARGUMENTS", "INPUT", "Expected exactly one valid itemId; scope and SQL cannot be supplied."), "INPUT_REJECTED"), nil
	}
	t, e := g.Store.Claim(ctx, p, item)
	if errors.Is(e, metadata.ErrDenied) {
		return g.reject(p, failure("ACCESS_DENIED", "AUTHORIZATION", "This tool is not authorized for the current principal."), "ACCESS_DENIED"), nil
	}
	if e != nil {
		return failure("METADATA_UNAVAILABLE", "DEPENDENCY", "Execution qualification could not be confirmed; nothing was dispatched.").MCP(), nil
	}
	r := failure("PLAN_INVALID", "EXECUTION", "The protected plan is unavailable; nothing was dispatched.")
	r.ActionID = &t.ActionID
	r.AttemptID = &t.AttemptID
	r.ExecutionID = &t.ExecutionID
	scope, plannedItem, e := g.Store.ReadPlan(t)
	if e == nil {
		nctx, ncancel := context.WithDeadline(ctx, t.Deadline)
		o := g.Reader.Read(nctx, scope, plannedItem)
		ncancel()
		r.DispatchState = o.Dispatch
		r.Metadata = observed()
		if o.Dispatch == "DISPATCH_UNKNOWN" {
			r = unknown(t)
		} else if o.Err == nil {
			r.RequestStatus = "SUCCEEDED"
			r.Error = nil
			r.Data = o.Data
		} else {
			var pe *pgconn.PgError
			if errors.Is(o.Err, context.Canceled) || errors.Is(o.Err, context.DeadlineExceeded) || errors.As(o.Err, &pe) && pe.Code == "57014" {
				r.RequestStatus = "CANCELLED"
				r.Error = &SafeError{"READ_CANCELLED", "CANCELLED", "The read was cancelled or exceeded its deadline.", "MANUAL_NEW_ACTION"}
			} else {
				r.Error = &SafeError{"NATIVE_READ_FAILED", "EXECUTION", "The native read failed; backend details are not disclosed.", "MANUAL_NEW_ACTION"}
			}
		}
	}
	b, _ := json.Marshal(r)
	if len(b) > 16384 {
		r.Data = nil
		r.RequestStatus = "FAILED"
		r.Error = &SafeError{"RESULT_LIMIT", "EXECUTION", "The result exceeded the allowed size.", "NEVER"}
		b, _ = json.Marshal(r)
	}
	// Completion must survive cancellation of the originating MCP request.
	fctx, fcancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer fcancel()
	if e = g.Store.Finish(fctx, t, r.RequestStatus, r.DispatchState, b); e != nil {
		slog.Error("could not persist terminal read outcome", "actionId", t.ActionID)
		u := unknown(t)
		if r.DispatchState == "DISPATCHED" {
			u.DispatchState = "DISPATCHED"
		}
		return u.MCP(), nil
	}
	return r.MCP(), nil
}

type HTTPOptions struct {
	AuthMode, PublicOrigin, ResourceMetadataURL string
	Verifier                                    auth.TokenVerifier
	Register                                    func(*http.ServeMux)
}

func (g *Gateway) Handler() http.Handler {
	return g.HandlerWithOptions(HTTPOptions{AuthMode: "dev-tokens"})
}

func (g *Gateway) HandlerWithOptions(opts HTTPOptions) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "up", "auth": opts.AuthMode})
	})
	verify := func(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		p, e := g.Store.VerifyToken(c, token)
		if errors.Is(e, metadata.ErrInvalidToken) {
			return nil, auth.ErrInvalidToken
		}
		if e != nil {
			return nil, errors.New("identity storage unavailable")
		}
		return &auth.TokenInfo{UserID: p.OrgID + "/" + p.ID, Expiration: p.ExpiresAt, Extra: map[string]any{"principal": p}}, nil
	}
	var authOptions *auth.RequireBearerTokenOptions
	if opts.Verifier != nil {
		verify = opts.Verifier
		authOptions = &auth.RequireBearerTokenOptions{ResourceMetadataURL: opts.ResourceMetadataURL, Scopes: []string{"mcp:read"}}
	}
	secured := auth.RequireBearerToken(verify, authOptions)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return g.MCP }, &mcp.StreamableHTTPOptions{JSONResponse: true, SessionTimeout: 2 * time.Minute, MaxRequestBodyBytes: 70 << 10})
	var transport http.Handler = wireids.HTTP(h)
	if opts.PublicOrigin != "" {
		// Standalone SSE is optional in Streamable HTTP. This temporary ingress
		// profile uses JSON POST responses; it does not claim SSE delivery.
		protected := transport
		transport = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				w.Header().Set("Allow", "POST, DELETE")
				http.Error(w, "standalone SSE is disabled for this fixture", 405)
				return
			}
			protected.ServeHTTP(w, r)
		})
	}
	mux.Handle("/mcp", secured(transport))
	if opts.PublicOrigin == "" {
		mux.Handle("GET /api/v1/audit", secured(http.HandlerFunc(g.auditHTTP)))
	}
	if opts.Register != nil {
		opts.Register(mux)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		origin := "http://" + r.Host
		if opts.PublicOrigin != "" {
			origin = opts.PublicOrigin
		}
		if r.Header.Get("Origin") != "" && r.Header.Get("Origin") != origin {
			if opts.PublicOrigin != "" {
				slog.Warn("OAuth fixture origin rejected", "origin", r.Header.Get("Origin"), "expected", origin, "path", r.URL.Path)
			}
			http.Error(w, "origin is not allowed", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func (g *Gateway) auditHTTP(w http.ResponseWriter, r *http.Request) {
	ti := auth.TokenInfoFromContext(r.Context())
	p, ok := ti.Extra["principal"].(metadata.Principal)
	if !ok {
		http.Error(w, "identity unavailable", 401)
		return
	}
	q := r.URL.Query()
	for k := range q {
		if (k != "after" && k != "limit") || len(q[k]) != 1 {
			http.Error(w, "unsupported audit selector", 400)
			return
		}
	}
	after := int64(0)
	limit := 100
	var e error
	if q.Has("after") {
		after, e = strconv.ParseInt(q.Get("after"), 10, 64)
		if e != nil || after < 0 {
			http.Error(w, "invalid cursor", 400)
			return
		}
	}
	if q.Has("limit") {
		limit, e = strconv.Atoi(q.Get("limit"))
		if e != nil || limit < 1 || limit > 100 {
			http.Error(w, "invalid limit", 400)
			return
		}
	}
	c, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	events, e := g.Store.Audit(c, p, after, limit)
	if e != nil {
		http.Error(w, "audit unavailable", 503)
		return
	}
	next := after
	if len(events) > 0 {
		next = events[len(events)-1].Seq
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"events": events, "nextAfter": next})
}

// RunRecovery records stale claims as unknown. No execution is resumed here.
func (g *Gateway) RunRecovery(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		tickets, e := g.Store.Stale(c)
		if e == nil {
			for _, t := range tickets {
				b, _ := json.Marshal(unknown(t))
				if e = g.Store.Recover(c, t, b); e != nil {
					break
				}
			}
		}
		cancel()
		if e != nil && ctx.Err() == nil {
			slog.Error("stale read recovery storage unavailable")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (g *Gateway) Close() {
	for session := range g.MCP.Sessions() {
		_ = session.Close()
	}
}
