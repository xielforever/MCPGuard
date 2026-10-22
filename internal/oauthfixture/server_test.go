package oauthfixture

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xielforever/MCPGuard/internal/metadata"
)

type fixture struct {
	s                          *Server
	h                          http.Handler
	now                        time.Time
	disabled                   bool
	secret, password, verifier string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{now: time.Now(), secret: random(), password: random(), verifier: random()}
	c := Config{PublicOrigin: "https://guard.example", ClientID: "chatgpt-test", ClientSecretSHA256: hash(f.secret), RedirectURIs: []string{"https://chatgpt.com/connector_platform_oauth_redirect"}, Users: []User{{Username: "alice", PasswordSHA256: hash(f.password), OrgID: "10000000-0000-4000-8000-000000000001", PrincipalID: "20000000-0000-4000-8000-000000000001"}}}
	var err error
	f.s, err = New(c, func(ctx context.Context, org, id string) (metadata.Principal, error) {
		if f.disabled {
			return metadata.Principal{}, metadata.ErrDenied
		}
		if org != c.Users[0].OrgID || id != c.Users[0].PrincipalID {
			t.Fatal("untrusted subject mapping")
		}
		return metadata.Principal{OrgID: org, ID: id, ScopeID: "scope_a", CanRead: true, ExpiresAt: f.now.Add(24 * time.Hour)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.s.now = func() time.Time { return f.now }
	mux := http.NewServeMux()
	f.s.Register(mux)
	f.h = mux
	return f
}
func (f *fixture) request(method, path string, v url.Values, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
	var body io.Reader
	if v != nil {
		body = strings.NewReader(v.Encode())
	}
	r := httptest.NewRequest(method, path, body)
	if v != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}
func (f *fixture) authQuery() url.Values {
	h := sha256.Sum256([]byte(f.verifier))
	return url.Values{"client_id": {f.s.Config.ClientID}, "redirect_uri": {f.s.Config.RedirectURIs[0]}, "response_type": {"code"}, "scope": {Scope}, "resource": {f.s.Resource()}, "state": {"opaque-state"}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(h[:])}}
}
func (f *fixture) pending(t *testing.T) (*http.Cookie, string) {
	t.Helper()
	w := f.request("GET", "/oauth/authorize?"+f.authQuery().Encode(), nil, nil, "")
	if w.Code != 200 {
		t.Fatalf("authorize=%d", w.Code)
	}
	if w.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatal("login policy would suppress the browser POST Origin")
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "form-action 'self' https://chatgpt.com;") {
		t.Fatal("browser would block the configured OAuth callback")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("insecure consent cookie")
	}
	m := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(w.Body.String())
	if len(m) != 2 {
		t.Fatal("CSRF absent")
	}
	return cookies[0], m[1]
}
func (f *fixture) code(t *testing.T) string {
	t.Helper()
	c, csrf := f.pending(t)
	w := f.request("POST", "/oauth/authorize", url.Values{"csrf": {csrf}, "username": {"alice"}, "password": {f.password}, "decision": {"allow"}}, c, f.s.Config.PublicOrigin)
	if w.Code != 303 {
		t.Fatalf("consent=%d", w.Code)
	}
	u, e := url.Parse(w.Header().Get("Location"))
	if e != nil {
		t.Fatal(e)
	}
	if u.Query().Get("state") != "opaque-state" || u.Query().Get("iss") != f.s.Config.PublicOrigin || u.Query().Get("code") == "" {
		t.Fatal("callback binding missing")
	}
	return u.Query().Get("code")
}
func (f *fixture) grant(code string) url.Values {
	return url.Values{"client_id": {f.s.Config.ClientID}, "client_secret": {f.secret}, "grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {f.s.Config.RedirectURIs[0]}, "code_verifier": {f.verifier}, "resource": {f.s.Resource()}}
}
func tokens(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("token status=%d body=%s", w.Code, w.Body.String())
	}
	var v map[string]any
	if json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatal("invalid token JSON")
	}
	return v
}
func (f *fixture) verify(raw string) error {
	_, e := f.s.Verify(context.Background(), raw, httptest.NewRequest("POST", "/mcp", nil))
	return e
}

func TestAuthorizationBindings(t *testing.T) {
	for name, value := range map[string]string{"redirect_uri": "https://attacker.example/cb", "client_id": "other", "resource": "https://other.example/mcp", "scope": "admin", "code_challenge_method": "plain", "code_challenge": "bad", "state": "", "response_type": "token"} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			q := f.authQuery()
			q.Set(name, value)
			w := f.request("GET", "/oauth/authorize?"+q.Encode(), nil, nil, "")
			if w.Code != 400 || w.Header().Get("Location") != "" {
				t.Fatal("untrusted authorization redirected or accepted")
			}
		})
	}
	f := setup(t)
	q := f.authQuery()
	q.Add("resource", f.s.Resource())
	if f.request("GET", "/oauth/authorize?"+q.Encode(), nil, nil, "").Code != 400 {
		t.Fatal("duplicate accepted")
	}
}
func TestConsentCSRFAndCredentials(t *testing.T) {
	f := setup(t)
	c, csrf := f.pending(t)
	v := url.Values{"csrf": {csrf}, "username": {"alice"}, "password": {f.password}, "decision": {"allow"}}
	if f.request("POST", "/oauth/authorize", v, c, "https://attacker.example").Code != 403 {
		t.Fatal("foreign origin accepted")
	}
	v.Set("csrf", "wrong")
	if f.request("POST", "/oauth/authorize", v, c, f.s.Config.PublicOrigin).Code != 400 {
		t.Fatal("wrong CSRF accepted")
	}
	v.Set("csrf", csrf)
	v.Set("password", "wrong")
	for i := 0; i < 5; i++ {
		w := f.request("POST", "/oauth/authorize", v, c, f.s.Config.PublicOrigin)
		if w.Header().Get("Location") != "" {
			t.Fatal("bad password authorized")
		}
	}
	v.Set("password", f.password)
	if f.request("POST", "/oauth/authorize", v, c, f.s.Config.PublicOrigin).Code != 400 {
		t.Fatal("exhausted login reused")
	}
}
func TestCodeExchangeAndCurrentPrincipal(t *testing.T) {
	f := setup(t)
	v := tokens(t, f.request("POST", "/oauth/token", f.grant(f.code(t)), nil, ""))
	raw := v["access_token"].(string)
	if f.verify(raw) != nil {
		t.Fatal("valid access rejected")
	}
	f.disabled = true
	if f.verify(raw) == nil {
		t.Fatal("disabled subject retained access")
	}
	f.disabled = false
	f.now = f.now.Add(11 * time.Minute)
	if f.verify(raw) == nil {
		t.Fatal("expired access accepted")
	}
}
func TestGrantRejections(t *testing.T) {
	for name, value := range map[string]string{"client_secret": "wrong", "client_id": "other", "code_verifier": "wrong", "redirect_uri": "https://attacker.example/cb", "resource": "https://other.example/mcp"} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			v := f.grant(f.code(t))
			v.Set(name, value)
			if f.request("POST", "/oauth/token", v, nil, "").Code == 200 {
				t.Fatal("invalid binding accepted")
			}
		})
	}
	f := setup(t)
	v := f.grant(f.code(t))
	f.now = f.now.Add(3 * time.Minute)
	if f.request("POST", "/oauth/token", v, nil, "").Code == 200 {
		t.Fatal("expired code accepted")
	}
}
func TestCodeReplayRevokesFamily(t *testing.T) {
	f := setup(t)
	v := f.grant(f.code(t))
	issued := tokens(t, f.request("POST", "/oauth/token", v, nil, ""))
	if f.request("POST", "/oauth/token", v, nil, "").Code == 200 {
		t.Fatal("code reused")
	}
	if f.verify(issued["access_token"].(string)) == nil {
		t.Fatal("replayed code retained grants")
	}
}
func TestRefreshRotationReplayAndRevocation(t *testing.T) {
	f := setup(t)
	issued := tokens(t, f.request("POST", "/oauth/token", f.grant(f.code(t)), nil, ""))
	v := url.Values{"client_id": {f.s.Config.ClientID}, "client_secret": {f.secret}, "grant_type": {"refresh_token"}, "refresh_token": {issued["refresh_token"].(string)}, "resource": {f.s.Resource()}}
	next := tokens(t, f.request("POST", "/oauth/token", v, nil, ""))
	if next["refresh_token"] == issued["refresh_token"] || f.verify(next["access_token"].(string)) != nil {
		t.Fatal("rotation failed")
	}
	if f.request("POST", "/oauth/token", v, nil, "").Code == 200 || f.verify(next["access_token"].(string)) == nil {
		t.Fatal("refresh replay retained family")
	}
	f = setup(t)
	issued = tokens(t, f.request("POST", "/oauth/token", f.grant(f.code(t)), nil, ""))
	v = url.Values{"client_id": {f.s.Config.ClientID}, "client_secret": {f.secret}, "token": {issued["refresh_token"].(string)}}
	if f.request("POST", "/oauth/revoke", v, nil, "").Code != 200 || f.verify(issued["access_token"].(string)) == nil {
		t.Fatal("revocation failed")
	}
}
func TestMetadataAndInvalidConfiguration(t *testing.T) {
	f := setup(t)
	for _, path := range []string{"/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource", "/.well-known/oauth-authorization-server"} {
		w := f.request("GET", path, nil, nil, "")
		if w.Code != 200 || !json.Valid(w.Body.Bytes()) || strings.Contains(w.Body.String(), f.secret) {
			t.Fatal("metadata invalid or contains secret")
		}
	}
	for _, origin := range []string{"http://guard.example", "https://guard.example/", "https://user@guard.example", "https://guard.example?query=1"} {
		c := f.s.Config
		c.PublicOrigin = origin
		if _, e := New(c, f.s.lookup); e == nil {
			t.Fatal("invalid origin accepted")
		}
	}
}

func TestBasicClientAndDuplicateParameters(t *testing.T) {
	f := setup(t)
	v := f.grant(f.code(t))
	v.Del("client_id")
	v.Del("client_secret")
	r := httptest.NewRequest("POST", "/oauth/token", strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetBasicAuth(url.QueryEscape(f.s.Config.ClientID), url.QueryEscape(f.secret))
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	_ = tokens(t, w)
	v = f.grant(f.code(t))
	v.Add("code", v.Get("code"))
	if f.request("POST", "/oauth/token", v, nil, "").Code != 400 {
		t.Fatal("duplicate token parameter accepted")
	}
	v.Set("code", v.Get("code"))
	r = httptest.NewRequest("POST", "/oauth/token", strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetBasicAuth(f.s.Config.ClientID, f.secret)
	w = httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code == 200 {
		t.Fatal("multiple client auth methods accepted")
	}
}

func TestExpiredConsentRefreshAndRestart(t *testing.T) {
	f := setup(t)
	c, csrf := f.pending(t)
	f.now = f.now.Add(11 * time.Minute)
	if f.request("POST", "/oauth/authorize", url.Values{"csrf": {csrf}, "decision": {"deny"}}, c, f.s.Config.PublicOrigin).Code != 400 {
		t.Fatal("expired consent accepted")
	}
	issued := tokens(t, f.request("POST", "/oauth/token", f.grant(f.code(t)), nil, ""))
	if setup(t).verify(issued["access_token"].(string)) == nil {
		t.Fatal("restart retained token")
	}
	f.now = f.now.Add(9 * time.Hour)
	values := url.Values{"client_id": {f.s.Config.ClientID}, "client_secret": {f.secret}, "grant_type": {"refresh_token"}, "refresh_token": {issued["refresh_token"].(string)}}
	if f.request("POST", "/oauth/token", values, nil, "").Code == 200 {
		t.Fatal("expired refresh accepted")
	}
}
