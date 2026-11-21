package connect

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRunRecoversExpiredUpstreamBeforeToolDispatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var dispatches atomic.Int32
	var loseResponse atomic.Bool
	gateway := mcp.NewServer(&mcp.Implementation{Name: "fixture-gateway", Version: "1"}, nil)
	gateway.AddTool(&mcp.Tool{Name: "fixture.operation", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		dispatches.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "completed"}}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return gateway }, &mcp.StreamableHTTPOptions{
		JSONResponse: true, SessionTimeout: time.Second,
	})
	httpGateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if loseResponse.Load() && r.Method == http.MethodPost {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "fixture request unreadable", http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var frame struct{ Method string }
			if json.Unmarshal(raw, &frame) == nil && frame.Method == "tools/call" {
				handler.ServeHTTP(httptest.NewRecorder(), r)
				http.Error(w, "fixture response lost", http.StatusServiceUnavailable)
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer httpGateway.Close()
	clientTransport, connectorTransport := mcp.NewInMemoryTransports()
	connectorDone := make(chan error, 1)
	go func() {
		connectorDone <- Run(ctx, httpGateway.URL+"/mcp", strings.Repeat("a", 32), true, connectorTransport)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "fixture-client", Version: "1"}, nil)
	local, err := client.Connect(ctx, clientTransport, &mcp.ClientSessionOptions{ProtocolVersion: Profile})
	if err != nil {
		t.Fatalf("connect to connector: %v", err)
	}
	defer func() {
		_ = local.Close()
		cancel()
		<-connectorDone
	}()
	if _, err := local.ListTools(ctx, nil); err != nil {
		t.Fatalf("list tools before idle: %v", err)
	}
	upstream := slices.Collect(gateway.Sessions())
	if len(upstream) != 1 {
		t.Fatalf("upstream sessions before idle: got %d, want 1", len(upstream))
	}
	expired := make(chan struct{})
	go func() {
		_ = upstream[0].Wait()
		close(expired)
	}()
	select {
	case <-expired:
	case <-ctx.Done():
		t.Fatal("upstream session did not expire while the local session remained open")
	}
	params := &mcp.CallToolParams{Name: "fixture.operation", Arguments: map[string]any{}}
	result, err := local.CallTool(ctx, params)
	if err != nil || result == nil || result.IsError || dispatches.Load() != 1 {
		t.Fatalf("call after idle: error=%v, dispatches=%d, want a successful single dispatch", err, dispatches.Load())
	}

	loseResponse.Store(true)
	if _, err := local.CallTool(ctx, params); err == nil || dispatches.Load() != 2 {
		t.Fatalf("call with lost response: error=%v, total dispatches=%d, want an error and one new dispatch", err, dispatches.Load())
	}
}
