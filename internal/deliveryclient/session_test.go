package deliveryclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/google/uuid"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/privatefile"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func sessionFixture(t *testing.T, limits CacheLimits) (*Session, *atomic.Int32) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if privatefile.EnsureDir(dir) != nil {
		t.Fatal("private directory")
	}
	registration := v02.ClientRegistration{OrgID: uuid.NewString(), OwnerID: uuid.NewString(), ClientInstanceID: uuid.NewString(), AuthorizationID: uuid.NewString(), State: "ACTIVE"}
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32))
	calls := &atomic.Int32{}
	view := v02.CredentialContext{ContextID: uuid.NewString(), OrgID: registration.OrgID, OwnerID: registration.OwnerID, ClientInstanceID: registration.ClientInstanceID, ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), AbsoluteExpiresAt: time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339Nano), State: "ACTIVE", Revision: "1"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("p", 32) || r.Header.Get("X-MCPGuard-Delivery-Token") != "private-proof" {
			t.Error("identity headers missing")
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/v2/credential-contexts" {
			if r.Header.Get("X-MCPGuard-Credential-Context") != "" {
				t.Error("create reused Context")
			}
			_ = json.NewEncoder(w).Encode(v02.ContextPrivateResult{Context: view, ContextToken: token})
			return
		}
		if r.Header.Get("X-MCPGuard-Credential-Context") != view.ContextID+"."+token {
			t.Error("Context header missing")
		}
		if r.URL.Path == "/api/v2/credential-downloads" {
			calls.Add(1)
			_ = json.NewEncoder(w).Encode(v02.DownloadQueue{Tickets: []v02.DownloadTicket{}, NextAfter: "signed-next-page"})
			return
		}
		_ = json.NewEncoder(w).Encode(v02.ContextStatusResult{Context: view, Activations: []v02.ContextActivation{}})
	}))
	t.Cleanup(server.Close)
	c := &Client{origin: server.URL, caller: strings.Repeat("p", 32), proof: "private-proof", http: server.Client(), registration: &registration}
	s, e := OpenSession(c, filepath.Join(dir, "cache"), true, limits)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	if _, e = s.Init(context.Background(), false); e != nil {
		t.Fatal(e)
	}
	return s, calls
}
func TestPrivateSessionScopeLockAndRedaction(t *testing.T) {
	s, _ := sessionFixture(t, DefaultCacheLimits())
	summary, e := s.Status(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(summary)
	state, _ := privatefile.Read(filepath.Join(s.dir, "session.json"), 1<<20)
	if bytes.Contains(raw, []byte(s.state.Context.ContextToken)) || bytes.Contains(raw, []byte("contextToken")) || bytes.Contains(state, []byte(s.client.caller)) || bytes.Contains(state, []byte(s.client.proof)) {
		t.Fatal("private secret escaped or primary bearer persisted")
	}
	if duplicate, e := OpenSession(s.client, s.dir, false, DefaultCacheLimits()); e == nil {
		duplicate.Close()
		t.Fatal("second writer opened profile")
	}
	s.Close()
	for _, mode := range []string{"caller", "owner", "client", "origin"} {
		copy := *s.client
		reg := *s.client.registration
		copy.registration = &reg
		switch mode {
		case "caller":
			copy.caller = strings.Repeat("x", 32)
		case "owner":
			reg.OwnerID = uuid.NewString()
		case "client":
			reg.ClientInstanceID = uuid.NewString()
		case "origin":
			copy.origin = "https://other.invalid"
		}
		if other, e := OpenSession(&copy, s.dir, false, DefaultCacheLimits()); e == nil {
			other.Close()
			t.Fatal("profile accepted changed ", mode)
		}
	}
}
func TestSessionSyncWorkBound(t *testing.T) {
	limits := DefaultCacheLimits()
	limits.Pages = 2
	limits.PageSize = 1
	s, calls := sessionFixture(t, limits)
	out, e := s.Sync(context.Background())
	if e != nil || calls.Load() != 2 || out.Pages != 2 || !out.More {
		t.Fatal("sync exceeded configured work or lost continuation")
	}
}
func TestSessionRejectsUnsafeStateAndQuota(t *testing.T) {
	s, _ := sessionFixture(t, DefaultCacheLimits())
	if os.WriteFile(filepath.Join(s.dir, "files", "unrecognized"), []byte("x"), 0600) != nil {
		t.Fatal("fixture")
	}
	if _, e := s.Sync(context.Background()); e == nil {
		t.Fatal("unknown cache artifact accepted")
	}
	s.Close()
	path := filepath.Join(s.dir, "session.json")
	if privatefile.Replace(s.dir, "session.json", []byte(`{"version":1,"version":1}`)) != nil {
		t.Fatal("fixture")
	}
	if other, e := OpenSession(s.client, filepath.Dir(path), false, DefaultCacheLimits()); e == nil {
		other.Close()
		t.Fatal("duplicate state accepted")
	}
	limits := DefaultCacheLimits()
	limits.Pages = v02.MaxSyncPages + 1
	if other, e := OpenSession(s.client, filepath.Join(t.TempDir(), "quota"), true, limits); e == nil {
		other.Close()
		t.Fatal("unbounded profile accepted")
	}
}
