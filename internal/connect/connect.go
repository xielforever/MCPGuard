// Package connect terminates stdio MCP and forwards tools to the same HTTP gateway.
// Both transports use the official SDK, with the same explicit protocol profile.
package connect

import (
	"context"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net"
	"net/http"
	"net/url"
	"time"
)

const Profile = "2025-11-25"

type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c := r.Clone(r.Context())
	c.Header = r.Header.Clone()
	c.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(c)
}

func Run(ctx context.Context, endpoint, token string, allowLoopbackHTTP bool, transport mcp.Transport) error {
	u, e := url.Parse(endpoint)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/mcp" {
		return errors.New("gateway must be an absolute /mcp URL without credentials, query or fragment")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || !allowLoopbackHTTP || ip == nil || !ip.IsLoopback() {
			return errors.New("HTTPS required; explicit loopback HTTP flag is available for local tests")
		}
	}
	if len(token) < 32 || len(token) > 256 {
		return errors.New("MCPGUARD_TOKEN must contain the employee's test access token")
	}
	hc := &http.Client{Timeout: 10 * time.Second, Transport: bearerTransport{http.DefaultTransport.(*http.Transport).Clone(), token}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("gateway redirect refused") }}
	client := mcp.NewClient(&mcp.Implementation{Name: "mcpguard-connect", Version: "0.1.0-read-slice"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}})
	boot, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	remote, e := client.Connect(boot, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: hc, MaxRetries: -1, DisableStandaloneSSE: true, MaxEventSize: 128 << 10}, &mcp.ClientSessionOptions{ProtocolVersion: Profile})
	if e != nil {
		return errors.New("gateway connection or employee authentication failed")
	}
	defer remote.Close()
	if remote.InitializeResult().ProtocolVersion != Profile {
		return errors.New("gateway did not negotiate the required profile")
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "mcpguard-connect", Version: "0.1.0-read-slice"}, &mcp.ServerOptions{SupportedProtocolVersions: []string{Profile}, Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}}})
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(c context.Context, method string, req mcp.Request) (mcp.Result, error) {
			switch method {
			case "tools/list":
				return remote.ListTools(c, req.(*mcp.ListToolsRequest).Params)
			case "tools/call":
				p := req.(*mcp.CallToolRequest).Params
				return remote.CallTool(c, &mcp.CallToolParams{Name: p.Name, Arguments: p.Arguments, Meta: p.Meta, RequestState: p.RequestState, InputResponses: p.InputResponses})
			default:
				return next(c, method, req)
			}
		}
	})
	// SDK handles concurrent requests, ID translation, explicit cancellation and
	// serialized protocol output. No diagnostics are written to stdout.
	return server.Run(ctx, transport)
}
