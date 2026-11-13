package clientadapter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/deliveryclient"
	"github.com/xielforever/MCPGuard/internal/privatefile"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCapabilitiesDescribeImplementedEntries(t *testing.T) {
	for _, entry := range []string{"NATIVE_HTTP_HELPER", "STDIO_PRIVATE", "URL_ONLY"} {
		c := Capabilities(entry)
		raw, _ := json.Marshal(c)
		var out v02.ClientAdapterCapabilities
		if v02.Decode("ClientAdapterCapabilities", raw, &out) != nil {
			t.Fatal("capabilities violate canonical contract")
		}
		if c.NativeDatabaseConnection || c.ModelVisibleSecrets != "FORBIDDEN" {
			t.Fatal("incorrect boundary")
		}
		if entry == "URL_ONLY" && (c.PrivateDelivery || c.MultiResourceActivation) {
			t.Fatal("URL-only overclaims private delivery")
		}
	}
}
func TestControllerNaturalExpiryRequiresNativeReconnectAndNeverRevivesRevocation(t *testing.T) {
	var mu sync.Mutex
	creates := 0
	clientID, org, owner, aid := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	delivery := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))
	employee := strings.Repeat("e", 32)
	view := v02.CredentialContext{}
	contextToken := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if r.Header.Get("Authorization") != "Bearer "+employee {
			t.Error("employee identity missing")
		}
		if r.URL.Path == "/mcp" {
			if r.Header.Get("X-MCPGuard-Credential-Context") != view.ContextID+"."+contextToken || r.Header.Get("X-MCPGuard-Delivery-Token") != "" {
				t.Error("incorrect per-request private transport headers")
			}
			_, _ = w.Write([]byte(`{"safe":true}`))
			return
		}
		if r.Header.Get("X-MCPGuard-Delivery-Token") != delivery {
			t.Error("private authority missing")
		}
		if r.URL.Path == "/api/v2/credential-contexts" && r.Method == http.MethodPost {
			creates++
			now := time.Now().UTC()
			view = v02.CredentialContext{ContextID: uuid.NewString(), OrgID: org, OwnerID: owner, ClientInstanceID: clientID, ExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano), AbsoluteExpiresAt: now.Add(5 * time.Minute).Format(time.RFC3339Nano), State: "ACTIVE", Revision: "1"}
			contextToken = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(creates + 10)}, 32))
			_ = json.NewEncoder(w).Encode(v02.ContextPrivateResult{Context: view, ContextToken: contextToken})
			return
		}
		if r.Header.Get("X-MCPGuard-Credential-Context") != view.ContextID+"."+contextToken {
			t.Error("Context header missing")
		}
		if r.URL.Path == "/api/v2/credential-downloads" {
			_ = json.NewEncoder(w).Encode(v02.DownloadQueue{Tickets: []v02.DownloadTicket{}, NextAfter: ""})
			return
		}
		_ = json.NewEncoder(w).Encode(v02.ContextStatusResult{Context: view, Activations: []v02.ContextActivation{}})
	}))
	defer server.Close()
	dir := filepath.Join(t.TempDir(), "private")
	if privatefile.EnsureDir(dir) != nil {
		t.Fatal("private directory")
	}
	auth := map[string]any{"schemaVersion": "0.2", "authorization": v02.DeliveryAuthorizationInfo{AuthorizationID: aid, OwnerID: owner, ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), Audience: "mcpguard-delivery", Scope: "credential:claim"}, "deliveryToken": delivery, "client": v02.ClientRegistration{ClientInstanceID: clientID, OrgID: org, OwnerID: owner, AuthorizationID: aid, State: "ACTIVE"}}
	raw, _ := json.Marshal(auth)
	if privatefile.WriteNew(dir, "bootstrap.json", raw) != nil {
		t.Fatal("private bootstrap")
	}
	c, e := New(Config{Endpoint: server.URL + "/mcp", Token: employee, AuthorizationFile: filepath.Join(dir, "bootstrap.json"), Directory: filepath.Join(dir, "cache"), AllowLoopbackHTTP: true, Limits: deliveryclient.DefaultCacheLimits()})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Step(context.Background(), true); e != nil {
		t.Fatal("initial private step")
	}
	var headers bytes.Buffer
	if c.WritePrivateHost(&headers) != nil {
		t.Fatal("private header hook")
	}
	if c.PublicResult(map[string]any{"text": contextToken}, true) {
		t.Fatal("raw Context token passed public guard")
	}
	httpClient := &http.Client{Transport: c.Transport(http.DefaultTransport)}
	response, e := httpClient.Get(server.URL + "/mcp")
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	mu.Lock()
	view.State = "EXPIRED"
	mu.Unlock()
	if _, e = c.Step(context.Background(), false); !errors.Is(e, ErrReconnect) || creates != 1 {
		t.Fatal("native worker silently rotated expired header")
	}
	if _, e = c.Step(context.Background(), true); e != nil || creates != 2 {
		t.Fatal("explicit reconnect did not rebuild natural expiry")
	}
	response, e = httpClient.Get(server.URL + "/mcp")
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	mu.Lock()
	view.State = "REVOKED"
	mu.Unlock()
	if _, e = c.Step(context.Background(), true); !errors.Is(e, ErrReconnect) || creates != 2 {
		t.Fatal("revoked Context automatically recreated")
	}
	if c.Watch(context.Background(), time.Millisecond, time.Second, false) == nil {
		t.Fatal("unbounded polling interval accepted")
	}
	server.Close()
	if c.Watch(context.Background(), 100*time.Millisecond, time.Second, false) == nil {
		t.Fatal("worker reported success after repeated private synchronization failure")
	}
}
