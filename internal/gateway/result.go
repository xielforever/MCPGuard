package gateway

import (
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"time"
)

type SafeError struct {
	Code             string `json:"code"`
	Category         string `json:"category"`
	SafeMessage      string `json:"safeMessage"`
	RetryDisposition string `json:"retryDisposition"`
}
type Result struct {
	Kind             string         `json:"kind"`
	ActionID         *string        `json:"actionId"`
	AttemptID        *string        `json:"attemptId"`
	ExecutionID      *string        `json:"executionId"`
	RequestStatus    string         `json:"requestStatus"`
	DispatchState    string         `json:"dispatchState"`
	SideEffectStatus string         `json:"sideEffectStatus"`
	Data             any            `json:"data"`
	Error            *SafeError     `json:"error"`
	Metadata         map[string]any `json:"metadata"`
}

func failure(code, category, message string) Result {
	return Result{Kind: "FINAL", RequestStatus: "FAILED", DispatchState: "NOT_DISPATCHED", SideEffectStatus: "NONE", Error: &SafeError{code, category, message, "NEVER"}, Metadata: map[string]any{}}
}
func unknown(t metadata.Ticket) Result {
	r := failure("OUTCOME_UNKNOWN", "OUTCOME_UNKNOWN", "Execution outcome is unavailable; this action will not be replayed.")
	r.ActionID = &t.ActionID
	r.AttemptID = &t.AttemptID
	r.ExecutionID = &t.ExecutionID
	r.RequestStatus = "OUTCOME_UNKNOWN"
	r.DispatchState = "DISPATCH_UNKNOWN"
	r.Error.RetryDisposition = "RECONCILE"
	return r
}
func (r Result) MCP() *mcp.CallToolResult {
	b, _ := json.Marshal(r)
	return &mcp.CallToolResult{IsError: r.RequestStatus != "SUCCEEDED", StructuredContent: r, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}
func observed() map[string]any {
	return map[string]any{"encoding": "json", "truncated": false, "observedAt": time.Now().UTC().Format(time.RFC3339Nano)}
}
