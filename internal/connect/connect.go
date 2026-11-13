// Package connect terminates stdio MCP and forwards tools to the same HTTP gateway.
// Both transports use the official SDK, with the same explicit protocol profile.
package connect

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xielforever/MCPGuard/internal/clientadapter"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
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
	return run(ctx, endpoint, token, allowLoopbackHTTP, transport, nil)
}

func RunPrivate(ctx context.Context, cfg clientadapter.Config, transport mcp.Transport) error {
	controller, e := clientadapter.New(cfg)
	if e != nil {
		return clientadapter.ErrAdapter
	}
	if _, e = controller.Step(ctx, true); e != nil {
		return clientadapter.ErrAdapter
	}
	return run(ctx, cfg.Endpoint, cfg.Token, cfg.AllowLoopbackHTTP, transport, controller)
}
func run(ctx context.Context, endpoint, token string, allowLoopbackHTTP bool, transport mcp.Transport, private *clientadapter.Controller) error {
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
	clientCaps := &mcp.ClientCapabilities{}
	serverCaps := &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}}
	version := "0.1.0-read-slice"
	if private != nil {
		version = v02.ContractRevision
		capability := map[string]any{clientadapter.CapabilityName: clientadapter.Capabilities("STDIO_PRIVATE")}
		clientCaps.Experimental = capability
		serverCaps.Experimental = capability
		hc.Transport = private.Transport(http.DefaultTransport.(*http.Transport).Clone())
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "mcpguard-connect", Version: version}, &mcp.ClientOptions{Capabilities: clientCaps})
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
	server := mcp.NewServer(&mcp.Implementation{Name: "mcpguard-connect", Version: version}, &mcp.ServerOptions{SupportedProtocolVersions: []string{Profile}, Capabilities: serverCaps})
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(c context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if private != nil && (method == "tools/list" || method == "tools/call") {
				if _, e := private.Step(c, true); e != nil {
					return nil, errors.New("private client unavailable; refresh identity or repair Context privately")
				}
			}
			switch method {
			case "tools/list":
				out, e := remote.ListTools(c, req.(*mcp.ListToolsRequest).Params)
				if private != nil {
					if e != nil || !private.PublicResult(out, false) {
						return nil, errors.New("remote catalog unavailable")
					}
				}
				return out, e
			case "tools/call":
				p := req.(*mcp.CallToolRequest).Params
				out, e := remote.CallTool(c, &mcp.CallToolParams{Name: p.Name, Arguments: p.Arguments, Meta: p.Meta, RequestState: p.RequestState, InputResponses: p.InputResponses})
				if private == nil {
					return out, e
				}
				if e != nil {
					return nil, errors.New("remote result unavailable; inspect the original safe action or ticket, never replay automatically")
				}
				if !private.PublicResult(out, true) {
					return nil, errors.New("private material rejected at protocol boundary")
				}
				if p.Name == "credential.prepare_download" && !out.IsError {
					var target v02.PrepareDownloadResult
					raw, _ := json.Marshal(out.StructuredContent)
					if v02.Decode("PrepareDownloadResult", raw, &target) != nil || target.ResourceID == nil {
						return nil, errors.New("remote private task metadata unavailable")
					}
					state, recovery := "PENDING_PRIVATE_SYNC", "PRIVATE_SYNC_ONLY"
					if current, e := private.Step(c, true); e == nil {
						for _, a := range current.Activations {
							if a.ResourceID == *target.ResourceID && a.CredentialID == target.CredentialID && a.State == "ACTIVE" {
								state, recovery = "ACTIVE", "NONE"
							}
						}
					}
					if out.Meta == nil {
						out.Meta = map[string]any{}
					}
					out.Meta[clientadapter.CapabilityName] = v02.AdapterDeliveryNotice{State: state, Recovery: recovery, BusinessRequestReplayed: false}
				}
				if !private.PublicResult(out, true) {
					return nil, errors.New("private material rejected at protocol boundary")
				}
				return out, nil
			default:
				return next(c, method, req)
			}
		}
	})
	// SDK handles concurrent requests, ID translation, explicit cancellation and
	// serialized protocol output. No diagnostics are written to stdout.
	return server.Run(ctx, transport)
}
