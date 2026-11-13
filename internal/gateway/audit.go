package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

const auditMaxLimit = 100

type auditQuery struct {
	After int64
	Limit int
}

type auditPage struct {
	Events    []metadata.AuditEvent `json:"events"`
	NextAfter int64                 `json:"nextAfter"`
}

// MCP clients decode untyped JSON numbers as float64. Encode bigint cursors
// as decimal strings so that using nextAfter never skips records by rounding.
type auditMCPEvent struct {
	Seq         string    `json:"seq"`
	ID          string    `json:"id"`
	ActionID    *string   `json:"actionId"`
	AttemptID   *string   `json:"attemptId"`
	ExecutionID *string   `json:"executionId"`
	EventType   string    `json:"eventType"`
	CreatedAt   time.Time `json:"createdAt"`
}

type auditMCPPage struct {
	Events    []auditMCPEvent `json:"events"`
	NextAfter string          `json:"nextAfter"`
}

func (page auditPage) mcp() auditMCPPage {
	out := auditMCPPage{Events: []auditMCPEvent{}, NextAfter: strconv.FormatInt(page.NextAfter, 10)}
	for _, event := range page.Events {
		out.Events = append(out.Events, auditMCPEvent{
			Seq: strconv.FormatInt(event.Seq, 10), ID: event.ID,
			ActionID: event.ActionID, AttemptID: event.AttemptID, ExecutionID: event.ExecutionID,
			EventType: event.EventType, CreatedAt: event.CreatedAt,
		})
	}
	return out
}

var auditPageSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "events": {
      "type": "array", "maxItems": 100,
      "items": {
        "type": "object",
        "properties": {
          "seq": {"type": "string", "pattern": "^[1-9][0-9]{0,18}$"},
          "id": {"type": "string"},
          "actionId": {"type": ["string", "null"]},
          "attemptId": {"type": ["string", "null"]},
          "executionId": {"type": ["string", "null"]},
          "eventType": {"type": "string"},
          "createdAt": {"type": "string", "format": "date-time"}
        },
        "required": ["seq", "id", "actionId", "attemptId", "executionId", "eventType", "createdAt"],
        "additionalProperties": false
      }
    },
    "nextAfter": {"type": "string", "pattern": "^(0|[1-9][0-9]{0,18})$"}
  },
  "required": ["events", "nextAfter"],
  "additionalProperties": false
}`)

func (g *Gateway) registerAuditTool() {
	f := false
	g.MCP.AddTool(&mcp.Tool{
		Name:        "audit.list",
		Description: "List only your own audit event summaries. Identity is supplied by the gateway. Use the decimal string nextAfter as after for the next page (default \"0\"). limit is 1 to 100 (default 100).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"after": map[string]any{"type": "string", "pattern": "^(0|[1-9][0-9]{0,18})$", "maxLength": 19, "default": "0"},
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": auditMaxLimit, "default": auditMaxLimit},
			},
			"additionalProperties": false,
		},
		OutputSchema: auditPageSchema,
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, DestructiveHint: &f, OpenWorldHint: &f},
	}, g.callAudit)
}

// Decode raw arguments to reject duplicate keys, nulls, fractions, overflow and
// identity selectors, including calls which bypass a client's schema checks.
func parseAuditQuery(raw []byte) (auditQuery, error) {
	q := auditQuery{Limit: auditMaxLimit}
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	if len(raw) > 256 {
		return q, errors.New("audit arguments too large")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	t, e := d.Token()
	if e != nil || t != json.Delim('{') {
		return q, errors.New("audit arguments must be an object")
	}
	seen := map[string]bool{}
	for d.More() {
		key, e := d.Token()
		k, ok := key.(string)
		if e != nil || !ok || (k != "after" && k != "limit") || seen[k] {
			return q, errors.New("unexpected or duplicate audit field")
		}
		seen[k] = true
		value, e := d.Token()
		if e != nil {
			return q, e
		}
		if k == "after" {
			cursor, ok := value.(string)
			n, e := strconv.ParseInt(cursor, 10, 64)
			if !ok || e != nil || n < 0 || strconv.FormatInt(n, 10) != cursor {
				return q, errors.New("audit cursor must be a nonnegative decimal string")
			}
			q.After = n
		} else {
			number, ok := value.(json.Number)
			if !ok {
				return q, errors.New("audit limit must be an integer")
			}
			n, e := strconv.ParseInt(number.String(), 10, 64)
			if e != nil || n < 1 || n > auditMaxLimit {
				return q, errors.New("audit limit is out of range")
			}
			q.Limit = int(n)
		}
	}
	if _, e = d.Token(); e != nil {
		return q, e
	}
	if _, e = d.Token(); e != io.EOF {
		return q, errors.New("trailing audit arguments")
	}
	return q, nil
}

// Both HTTP and MCP use the same owner-filtered store and cursor calculation.
func (g *Gateway) listAudit(ctx context.Context, p metadata.Principal, q auditQuery) (auditPage, error) {
	c, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	events, e := g.Store.Audit(c, p, q.After, q.Limit)
	if e != nil {
		return auditPage{}, e
	}
	next := q.After
	if len(events) > 0 {
		next = events[len(events)-1].Seq
	}
	return auditPage{Events: events, NextAfter: next}, nil
}

func auditToolError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: message}}}
}

func (g *Gateway) callAudit(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	p, ok := principal(req.Extra)
	if !ok {
		return nil, safeProtocolError("Authentication is required")
	}
	q, e := parseAuditQuery(req.Params.Arguments)
	if e != nil || req.Params.RequestState != "" || len(req.Params.InputResponses) > 0 {
		return auditToolError("Expected only after (nonnegative decimal string) and limit (integer from 1 to 100); identity selectors are not allowed."), nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(g.Root, cancel)
	defer stop()
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		return auditToolError("Gateway concurrency limit reached; no audit records were returned."), nil
	}
	page, e := g.listAudit(ctx, p, q)
	if errors.Is(e, metadata.ErrDenied) {
		return auditToolError("The current identity is no longer authorized; no audit records were returned."), nil
	}
	if e != nil {
		return auditToolError("Audit storage is unavailable; no audit records were returned."), nil
	}
	result := page.mcp()
	b, e := json.Marshal(result)
	if e != nil {
		return auditToolError("Audit results are unavailable; no audit records were returned."), nil
	}
	return &mcp.CallToolResult{StructuredContent: result, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil
}
