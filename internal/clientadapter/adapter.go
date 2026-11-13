// Package clientadapter owns private lifecycle work outside model-visible MCP.
package clientadapter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/deliveryclient"
)

const CapabilityName = "io.mcpguard/private-client"

var ErrAdapter = errors.New("private client synchronization unavailable; business requests are never replayed")
var ErrReconnect = errors.New("private Context requires host reconnect or explicit private repair")

type Config struct {
	Endpoint, Token, AuthorizationFile, Directory string
	AllowLoopbackHTTP                             bool
	Limits                                        deliveryclient.CacheLimits
}
type Controller struct {
	config    Config
	origin    string
	mu        sync.Mutex
	headersMu sync.RWMutex
	headers   deliveryclient.PrivateHeaders
}

func New(c Config) (*Controller, error) {
	if len(c.Token) < 32 || len(c.Token) > 256 {
		return nil, ErrAdapter
	}
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.Host == "" || u.User != nil || u.Path != "/mcp" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, ErrAdapter
	}
	origin := u.Scheme + "://" + u.Host
	if _, e = deliveryclient.New(origin, c.Token, c.AuthorizationFile, c.AllowLoopbackHTTP); e != nil {
		return nil, ErrAdapter
	}
	return &Controller{config: c, origin: origin}, nil
}

// Step is serialized locally and by the cross-process profile lock. Recovery
// may replace only naturally EXPIRED Contexts. Revocation always needs an
// explicit private repair; leases are revalidated by the RA-09 component.
func (c *Controller) Step(ctx context.Context, recoverExpired bool) (deliveryclient.SessionSummary, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var out deliveryclient.SessionSummary
	client, e := deliveryclient.New(c.origin, c.config.Token, c.config.AuthorizationFile, c.config.AllowLoopbackHTTP)
	if e != nil {
		return out, ErrAdapter
	}
	var session *deliveryclient.Session
	// Only local lock/file acquisition is retried. No lifecycle or MCP request
	// has been sent when these attempts fail.
	for attempt := 0; attempt < 8; attempt++ {
		session, e = deliveryclient.OpenSession(client, c.config.Directory, true, c.config.Limits)
		if e == nil {
			break
		}
		select {
		case <-ctx.Done():
			return out, ErrAdapter
		case <-time.After(25 * time.Millisecond):
		}
	}
	if e != nil {
		return out, ErrAdapter
	}
	defer session.Close()
	out, e = session.Init(ctx, false)
	if e != nil {
		return out, ErrAdapter
	}
	if out.Context.State == "EXPIRED" && recoverExpired {
		out, e = session.Init(ctx, true)
		if e != nil {
			return out, ErrAdapter
		}
	}
	if out.Context.State != "ACTIVE" {
		return out, ErrReconnect
	}
	out, e = session.Sync(ctx)
	if e != nil {
		return out, ErrAdapter
	}
	headers, e := session.HeadersForHost()
	if e != nil {
		return out, ErrAdapter
	}
	c.headersMu.Lock()
	c.headers = headers
	c.headersMu.Unlock()
	return out, nil
}

// WritePrivateHost is called exclusively by the native host's header hook.
// It is intentionally absent from the stdio MCP entry point.
func (c *Controller) WritePrivateHost(w io.Writer) error {
	c.headersMu.RLock()
	defer c.headersMu.RUnlock()
	if c.headers.WritePrivateHost(w) != nil {
		return ErrAdapter
	}
	return nil
}
func (c *Controller) Apply(r *http.Request) error {
	c.headersMu.RLock()
	defer c.headersMu.RUnlock()
	return c.headers.Apply(r)
}
func (c *Controller) PublicResult(v any, strict bool) bool {
	c.headersMu.RLock()
	defer c.headersMu.RUnlock()
	return c.headers.PublicResult(v, strict)
}

type privateTransport struct {
	controller *Controller
	base       http.RoundTripper
}

func (c *Controller) Transport(base http.RoundTripper) http.RoundTripper {
	return privateTransport{c, base}
}
func (t privateTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() != t.controller.config.Endpoint {
		return nil, ErrAdapter
	}
	copy := r.Clone(r.Context())
	copy.Header = r.Header.Clone()
	if t.controller.Apply(copy) != nil {
		return nil, ErrAdapter
	}
	return t.base.RoundTrip(copy)
}

// Watch never replays a business call and never rotates a Context behind a
// cached native-host header. Its total lifetime and consecutive errors are bounded.
func (c *Controller) Watch(ctx context.Context, interval, maxRuntime time.Duration, once bool) error {
	if interval < 100*time.Millisecond || interval > time.Minute || maxRuntime < time.Second || maxRuntime > time.Hour {
		return ErrAdapter
	}
	ctx, cancel := context.WithTimeout(ctx, maxRuntime)
	defer cancel()
	failures := 0
	for {
		_, e := c.Step(ctx, false)
		if errors.Is(e, ErrReconnect) {
			return e
		}
		if e != nil {
			failures++
			if failures >= 3 || once {
				return ErrAdapter
			}
		} else {
			failures = 0
			if once {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) && e == nil {
				return nil
			}
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
func Capabilities(entry string) v02.ClientAdapterCapabilities {
	c := v02.ClientAdapterCapabilities{SchemaVersion: "0.2", AdapterVersion: v02.ContractRevision, ProtocolVersion: "2025-11-25", Entry: entry, PrivateDelivery: true, MultiResourceActivation: true, SyncDriver: "BOUNDED_WORKER", HeaderRefresh: "HOST_RECONNECT", ExpiredContextRecovery: "EXPLICIT_RECONNECT", RevokedContextRecovery: "EXPLICIT_PRIVATE_REPAIR", ModelVisibleSecrets: "FORBIDDEN", NativeDatabaseConnection: false, RequiresPrivateHostHook: true}
	switch entry {
	case "STDIO_PRIVATE":
		c.SyncDriver = "BEFORE_AND_AFTER_PREPARE"
		c.HeaderRefresh = "PER_REMOTE_REQUEST"
		c.ExpiredContextRecovery = "BEFORE_NEXT_CALL"
		c.RequiresPrivateHostHook = false
	case "URL_ONLY":
		c.PrivateDelivery = false
		c.MultiResourceActivation = false
		c.SyncDriver = "UNAVAILABLE"
		c.HeaderRefresh = "UNAVAILABLE"
		c.ExpiredContextRecovery = "UNAVAILABLE"
		c.RevokedContextRecovery = "UNAVAILABLE"
		c.RequiresPrivateHostHook = false
	}
	return c
}
