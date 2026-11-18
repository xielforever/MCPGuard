package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

const healthTimeout = 2 * time.Second

type dependencyHealth struct {
	Ready  bool   `json:"ready"`
	Status string `json:"status"`
}

type healthResult struct {
	Live         bool                        `json:"live"`
	Ready        bool                        `json:"ready"`
	Dependencies map[string]dependencyHealth `json:"dependencies"`
	CheckedAt    time.Time                   `json:"checkedAt"`
	TimeoutMs    int64                       `json:"timeoutMs"`
}

var healthSchema = json.RawMessage(`{
  "type":"object",
  "properties":{
    "live":{"type":"boolean"},
    "ready":{"type":"boolean"},
    "dependencies":{
      "type":"object",
      "properties":{
        "metadata":{"$ref":"#/$defs/dependency"},
        "businessPostgres":{"$ref":"#/$defs/dependency"},
        "postgresWriter":{"$ref":"#/$defs/dependency"},
        "redis":{"$ref":"#/$defs/dependency"},
        "demoHttp":{"$ref":"#/$defs/dependency"}
      },
      "required":["metadata"],
      "additionalProperties":false
    },
    "checkedAt":{"type":"string","format":"date-time"},
    "timeoutMs":{"type":"integer","const":2000}
  },
  "required":["live","ready","dependencies","checkedAt","timeoutMs"],
  "additionalProperties":false,
  "$defs":{
    "dependency":{
      "type":"object",
      "properties":{
        "ready":{"type":"boolean"},
        "status":{"type":"string","enum":["READY","UNAVAILABLE","TIMEOUT","CANCELLED"]}
      },
      "required":["ready","status"],
      "additionalProperties":false
    }
  }
}`)

func (g *Gateway) registerHealthTool() {
	f := false
	g.MCP.AddTool(&mcp.Tool{
		Name:         "gateway.health",
		Description:  "Check gateway process liveness and configured runtime dependency readiness. Checks share a two-second deadline. Business permissions are checked separately by each tool. No arguments.",
		InputSchema:  map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		OutputSchema: healthSchema,
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, DestructiveHint: &f, OpenWorldHint: &f},
	}, g.callHealth)
}

func validHealthArguments(raw []byte) bool {
	if len(raw) > 256 {
		return false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return true
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') || d.More() {
		return false
	}
	if t, err = d.Token(); err != nil || t != json.Delim('}') {
		return false
	}
	_, err = d.Token()
	return err == io.EOF
}

func healthToolError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: message}}}
}

// Both probes share a deadline and run concurrently. Only safe states leave the
// gateway, never driver errors, DSNs, query text or permission details.
func probeHealth(ctx context.Context, meta, business func(context.Context) error) healthResult {
	return probeDependencies(ctx, map[string]func(context.Context) error{"metadata": meta, "businessPostgres": business})
}
func probeDependencies(ctx context.Context, probes map[string]func(context.Context) error) healthResult {
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	result := healthResult{Live: true, Dependencies: map[string]dependencyHealth{}, TimeoutMs: healthTimeout.Milliseconds()}
	type outcome struct {
		name string
		err  error
	}
	completed := make(chan outcome, len(probes))
	for name, probe := range probes {
		go func(name string, probe func(context.Context) error) { completed <- outcome{name, probe(ctx)} }(name, probe)
	}
	for len(result.Dependencies) < len(probes) {
		select {
		case out := <-completed:
			status := "READY"
			if out.err != nil {
				status = "UNAVAILABLE"
				if errors.Is(out.err, context.DeadlineExceeded) {
					status = "TIMEOUT"
				} else if errors.Is(out.err, context.Canceled) {
					status = "CANCELLED"
				}
			}
			result.Dependencies[out.name] = dependencyHealth{Ready: out.err == nil, Status: status}
		case <-ctx.Done():
			status := "CANCELLED"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				status = "TIMEOUT"
			}
			for name := range probes {
				if _, done := result.Dependencies[name]; !done {
					result.Dependencies[name] = dependencyHealth{Status: status}
				}
			}
		}
	}
	result.Ready = ctx.Err() == nil && result.Dependencies["metadata"].Ready && result.Dependencies["businessPostgres"].Ready
	for _, dep := range result.Dependencies {
		result.Ready = result.Ready && dep.Ready
	}
	result.CheckedAt = time.Now().UTC()
	return result
}

func (g *Gateway) callHealth(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	p, ok := principal(req.Extra)
	if !ok {
		return nil, safeProtocolError("Authentication is required")
	}
	if !validHealthArguments(req.Params.Arguments) || req.Params.RequestState != "" || len(req.Params.InputResponses) > 0 {
		return healthToolError("gateway.health accepts only an empty argument object."), nil
	}
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	stop := context.AfterFunc(g.Root, cancel)
	defer stop()
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		return healthToolError("Gateway concurrency limit reached; health checks were not run."), nil
	}
	if _, err := g.Store.Current(ctx, p); errors.Is(err, metadata.ErrDenied) {
		return healthToolError("The current identity is no longer authorized."), nil
	} else if err != nil {
		return healthToolError("Identity verification is unavailable; authenticated health checks could not be run."), nil
	}
	probes := map[string]func(context.Context) error{"metadata": g.Store.CheckReady}
	if g.Reader != nil {
		probes["businessPostgres"] = g.Reader.CheckReady
	}
	if g.Controlled != nil {
		if n, ok := g.Controlled.Native.(interface {
			Available(string) bool
			Probe(context.Context, string) error
		}); ok {
			for name, resource := range map[string]string{"postgresWriter": "demo.pg", "redis": "demo.redis", "demoHttp": "demo.directory"} {
				if n.Available(resource) {
					resource := resource
					probes[name] = func(c context.Context) error { return n.Probe(c, resource) }
				}
			}
		}
	}
	result := probeDependencies(ctx, probes)
	b, err := json.Marshal(result)
	if err != nil {
		return healthToolError("Health results are unavailable."), nil
	}
	// Not-ready is a valid health result, not a failed tool invocation.
	return &mcp.CallToolResult{StructuredContent: result, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil
}
