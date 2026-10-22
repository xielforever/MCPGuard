// Package oauthfixture is an opt-in, single-process OAuth interoperability
// fixture, not an enterprise identity provider. Restarting it revokes all grants.
package oauthfixture

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

const Scope = "mcp:read"

type User struct {
	Username       string `json:"username"`
	PasswordSHA256 string `json:"password_sha256"`
	OrgID          string `json:"org_id"`
	PrincipalID    string `json:"principal_id"`
}

// All secrets must be independently generated random 32-byte values. This
// fixture accepts no user-selected passwords and stores only their digests.
type Config struct {
	PublicOrigin       string   `json:"public_origin"`
	ClientID           string   `json:"client_id"`
	ClientSecretSHA256 string   `json:"client_secret_sha256"`
	RedirectURIs       []string `json:"redirect_uris"`
	Users              []User   `json:"users"`
}

type Lookup func(context.Context, string, string) (metadata.Principal, error)

type authorization struct {
	redirect, state, challenge, csrf string
	expires                          time.Time
	failures                         int
}
type code struct {
	authorization
	user   User
	used   bool
	family string
}
type family struct {
	user    User
	expires time.Time
	revoked bool
}
type token struct {
	family  string
	expires time.Time
	used    bool
}

type Server struct {
	Config          Config
	lookup          Lookup
	mu              sync.Mutex
	pending         map[string]authorization
	codes           map[string]code
	families        map[string]*family
	access, refresh map[string]token
	now             func() time.Time
}

func Load(path string, lookup Lookup) (*Server, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, errors.New("cannot open OAuth fixture config")
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 32769))
	d.DisallowUnknownFields()
	var c Config
	if e = d.Decode(&c); e != nil {
		return nil, errors.New("invalid OAuth fixture config")
	}
	if e = d.Decode(new(any)); e != io.EOF {
		return nil, errors.New("trailing OAuth fixture config data")
	}
	return New(c, lookup)
}

func New(c Config, lookup Lookup) (*Server, error) {
	u, e := url.Parse(c.PublicOrigin)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("OAuth fixture public_origin must be an exact HTTPS origin without a trailing slash")
	}
	if c.ClientID == "" || len(c.ClientID) > 128 || !validDigest(c.ClientSecretSHA256) || len(c.RedirectURIs) == 0 || len(c.Users) == 0 || lookup == nil {
		return nil, errors.New("OAuth fixture requires a fixed confidential client, exact redirects and test subjects")
	}
	for _, v := range c.RedirectURIs {
		r, err := url.Parse(v)
		if err != nil || r.Scheme != "https" || r.Host == "" || r.User != nil || r.Fragment != "" || strings.ContainsAny(v, "'\"; \t\r\n") {
			return nil, errors.New("OAuth redirects must be exact HTTPS URLs")
		}
	}
	seen := map[string]bool{}
	for _, v := range c.Users {
		_, oe := uuid.Parse(v.OrgID)
		_, pe := uuid.Parse(v.PrincipalID)
		if v.Username == "" || len(v.Username) > 64 || seen[v.Username] || !validDigest(v.PasswordSHA256) || oe != nil || pe != nil {
			return nil, errors.New("invalid OAuth fixture subject")
		}
		seen[v.Username] = true
	}
	return &Server{Config: c, lookup: lookup, pending: map[string]authorization{}, codes: map[string]code{}, families: map[string]*family{}, access: map[string]token{}, refresh: map[string]token{}, now: time.Now}, nil
}

func validDigest(v string) bool { b, e := hex.DecodeString(v); return e == nil && len(b) == 32 }
func random() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func hash(v string) string { b := sha256.Sum256([]byte(v)); return hex.EncodeToString(b[:]) }
func matches(v, digest string) bool {
	return subtle.ConstantTimeCompare([]byte(hash(v)), []byte(digest)) == 1
}
func (s *Server) Resource() string { return s.Config.PublicOrigin + "/mcp" }
func (s *Server) MetadataURL() string {
	return s.Config.PublicOrigin + "/.well-known/oauth-protected-resource/mcp"
}

func (s *Server) Register(mux *http.ServeMux) {
	resource := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		writeJSON(w, 200, map[string]any{"resource": s.Resource(), "authorization_servers": []string{s.Config.PublicOrigin}, "scopes_supported": []string{Scope}, "bearer_methods_supported": []string{"header"}, "resource_name": "MCPGuard local read fixture"})
	}
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", resource)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", resource)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"issuer": s.Config.PublicOrigin, "authorization_endpoint": s.Config.PublicOrigin + "/oauth/authorize", "token_endpoint": s.Config.PublicOrigin + "/oauth/token", "revocation_endpoint": s.Config.PublicOrigin + "/oauth/revoke", "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"}, "code_challenge_methods_supported": []string{"S256"}, "scopes_supported": []string{Scope}, "authorization_response_iss_parameter_supported": true})
	})
	mux.HandleFunc("GET /oauth/authorize", s.authorize)
	mux.HandleFunc("POST /oauth/authorize", s.consent)
	mux.HandleFunc("POST /oauth/token", s.exchange)
	mux.HandleFunc("POST /oauth/revoke", s.revoke)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func oauthError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
func single(v url.Values) bool {
	for _, values := range v {
		if len(values) != 1 {
			return false
		}
	}
	return true
}
func form(w http.ResponseWriter, r *http.Request) (url.Values, bool) {
	if ct := strings.Split(r.Header.Get("Content-Type"), ";")[0]; ct != "application/x-www-form-urlencoded" {
		oauthError(w, 400, "invalid_request")
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if r.ParseForm() != nil || !single(r.PostForm) || len(r.URL.RawQuery) > 0 {
		oauthError(w, 400, "invalid_request")
		return nil, false
	}
	return r.PostForm, true
}
func (s *Server) clean() {
	now := s.now()
	for k, v := range s.pending {
		if !now.Before(v.expires) {
			delete(s.pending, k)
		}
	}
	for k, v := range s.codes {
		if !now.Before(v.expires) {
			delete(s.codes, k)
		}
	}
	for k, v := range s.access {
		if !now.Before(v.expires) {
			delete(s.access, k)
		}
	}
	for k, v := range s.refresh {
		if !now.Before(v.expires) {
			delete(s.refresh, k)
		}
	}
	for k, v := range s.families {
		if !now.Before(v.expires) {
			delete(s.families, k)
		}
	}
}

var login = template.Must(template.New("consent").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>MCPGuard test authorization</title><style>body{font:18px system-ui;max-width:620px;margin:50px auto;padding:20px;background:#f4f7fb;color:#172438}main{background:white;padding:32px;border-radius:16px}label{display:block;margin:18px 0}input{display:block;padding:10px;width:90%}button{padding:12px 22px;margin:12px 8px 0 0}small{color:#526176}</style><main><h1>MCPGuard test authorization</h1><p>Connect this ChatGPT client to the local demo database with <strong>mcp:read</strong>.</p><p>Only <code>pg.get_demo_item</code> is available. The server applies the signed-in test user's scope and records each read in its audit ledger.</p><small>This is a temporary test identity service. Do not enter a company or ChatGPT password.</small>{{if .Error}}<p role="alert">{{.Error}}</p>{{end}}<form method="post" action="/oauth/authorize"><input type="hidden" name="csrf" value="{{.CSRF}}"><label>Test username<input name="username" autocomplete="username" required maxlength="64"></label><label>Generated test password<input name="password" type="password" autocomplete="current-password" required maxlength="128"></label><button name="decision" value="allow">Allow read access</button><button name="decision" value="deny" formnovalidate>Cancel</button></form></main></html>`))

func (s *Server) showLogin(w http.ResponseWriter, a authorization, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Chrome also checks the redirect following a form POST against form-action.
	// Permit only the preconfigured OAuth callback origins; redirect URI matching
	// at /authorize and /token remains exact, including the path and query.
	formTargets := "'self'"
	for _, target := range s.Config.RedirectURIs {
		u, _ := url.Parse(target)
		formTargets += " " + u.Scheme + "://" + u.Host
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action "+formTargets+"; frame-ancestors 'none'; base-uri 'none'")
	// no-referrer turns a browser form POST's Origin into "null". Preserve
	// same-origin POST validation while suppressing referrers on the callback.
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	_ = login.Execute(w, struct{ CSRF, Error string }{a.csrf, message})
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	challenge, e := base64.RawURLEncoding.DecodeString(q.Get("code_challenge"))
	redirectOK := false
	for _, v := range s.Config.RedirectURIs {
		redirectOK = redirectOK || v == q.Get("redirect_uri")
	}
	if len(r.URL.RawQuery) > 8192 || !single(q) || q.Get("client_id") != s.Config.ClientID || !redirectOK || q.Get("response_type") != "code" || q.Get("scope") != Scope || q.Get("resource") != s.Resource() || len(q.Get("state")) < 1 || len(q.Get("state")) > 1024 || q.Get("code_challenge_method") != "S256" || e != nil || len(challenge) != 32 || base64.RawURLEncoding.EncodeToString(challenge) != q.Get("code_challenge") {
		oauthError(w, 400, "invalid_request")
		return
	}
	a := authorization{redirect: q.Get("redirect_uri"), state: q.Get("state"), challenge: q.Get("code_challenge"), csrf: random(), expires: s.now().Add(10 * time.Minute)}
	s.mu.Lock()
	s.clean()
	if len(s.pending)+len(s.families) >= 1024 {
		s.mu.Unlock()
		oauthError(w, 503, "temporarily_unavailable")
		return
	}
	id := random()
	s.pending[hash(id)] = a
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "__Host-mcpguard-consent", Value: id, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	s.showLogin(w, a, "")
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request, a authorization, field, value string) {
	u, _ := url.Parse(a.redirect)
	q := u.Query()
	q.Set(field, value)
	q.Set("state", a.state)
	q.Set("iss", s.Config.PublicOrigin)
	u.RawQuery = q.Encode()
	http.SetCookie(w, &http.Cookie{Name: "__Host-mcpguard-consent", Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

func (s *Server) consent(w http.ResponseWriter, r *http.Request) {
	f, ok := form(w, r)
	if !ok {
		return
	}
	if r.Header.Get("Origin") != s.Config.PublicOrigin {
		oauthError(w, 403, "invalid_request")
		return
	}
	c, e := r.Cookie("__Host-mcpguard-consent")
	if e != nil || len(c.Value) > 128 {
		oauthError(w, 400, "invalid_request")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clean()
	id := hash(c.Value)
	a, ok := s.pending[id]
	if !ok || subtle.ConstantTimeCompare([]byte(a.csrf), []byte(f.Get("csrf"))) != 1 {
		oauthError(w, 400, "invalid_request")
		return
	}
	if f.Get("decision") == "deny" {
		delete(s.pending, id)
		s.redirect(w, r, a, "error", "access_denied")
		return
	}
	var subject User
	found := false
	for _, u := range s.Config.Users {
		valid := matches(f.Get("password"), u.PasswordSHA256)
		if u.Username == f.Get("username") && valid {
			subject = u
			found = true
		}
	}
	if !found || f.Get("decision") != "allow" {
		a.failures++
		if a.failures >= 5 {
			delete(s.pending, id)
			oauthError(w, 403, "access_denied")
			return
		}
		s.pending[id] = a
		s.showLogin(w, a, "Invalid test credentials.")
		return
	}
	delete(s.pending, id)
	raw := random()
	a.expires = s.now().Add(2 * time.Minute)
	s.codes[hash(raw)] = code{authorization: a, user: subject}
	slog.Info("OAuth fixture consent granted", "test_subject", subject.Username, "scope", Scope)
	s.redirect(w, r, a, "code", raw)
}

func (s *Server) client(w http.ResponseWriter, r *http.Request, f url.Values) bool {
	id, secret, basic := r.BasicAuth()
	if basic {
		var e error
		id, e = url.QueryUnescape(id)
		if e != nil {
			oauthError(w, 401, "invalid_client")
			return false
		}
		secret, e = url.QueryUnescape(secret)
		if e != nil {
			oauthError(w, 401, "invalid_client")
			return false
		}
		if f.Has("client_secret") || f.Has("client_id") {
			oauthError(w, 400, "invalid_request")
			return false
		}
	} else {
		if r.Header.Get("Authorization") != "" {
			oauthError(w, 401, "invalid_client")
			return false
		}
		id = f.Get("client_id")
		secret = f.Get("client_secret")
	}
	if id != s.Config.ClientID || len(secret) > 256 || !matches(secret, s.Config.ClientSecretSHA256) {
		w.Header().Set("WWW-Authenticate", `Basic realm="MCPGuard test client"`)
		oauthError(w, 401, "invalid_client")
		return false
	}
	return true
}

func (s *Server) issue(w http.ResponseWriter, fid string) {
	now := s.now()
	access, refresh := random(), random()
	s.access[hash(access)] = token{family: fid, expires: now.Add(10 * time.Minute)}
	s.refresh[hash(refresh)] = token{family: fid, expires: s.families[fid].expires}
	writeJSON(w, 200, map[string]any{"access_token": access, "refresh_token": refresh, "token_type": "Bearer", "expires_in": 600, "scope": Scope})
}

func validVerifier(v string) bool {
	if len(v) < 43 || len(v) > 128 {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", c)) {
			return false
		}
	}
	return true
}

func (s *Server) exchange(w http.ResponseWriter, r *http.Request) {
	f, ok := form(w, r)
	if !ok || !s.client(w, r, f) {
		return
	}
	if f.Has("resource") && f.Get("resource") != s.Resource() {
		oauthError(w, 400, "invalid_target")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clean()
	if len(s.codes)+len(s.families)+len(s.access)+len(s.refresh) >= 8192 {
		oauthError(w, 503, "temporarily_unavailable")
		return
	}
	switch f.Get("grant_type") {
	case "authorization_code":
		key := hash(f.Get("code"))
		c, ok := s.codes[key]
		if ok && c.used {
			if fam := s.families[c.family]; fam != nil {
				fam.revoked = true
			}
		}
		verifier := f.Get("code_verifier")
		challenge := sha256.Sum256([]byte(verifier))
		if !ok || c.used || c.redirect != f.Get("redirect_uri") || !validVerifier(verifier) || subtle.ConstantTimeCompare([]byte(c.challenge), []byte(base64.RawURLEncoding.EncodeToString(challenge[:]))) != 1 {
			oauthError(w, 400, "invalid_grant")
			return
		}
		fid := random()
		s.families[fid] = &family{user: c.user, expires: s.now().Add(8 * time.Hour)}
		c.used = true
		c.family = fid
		s.codes[key] = c
		s.issue(w, fid)
	case "refresh_token":
		key := hash(f.Get("refresh_token"))
		t, ok := s.refresh[key]
		fam := s.families[t.family]
		if ok && t.used && fam != nil {
			fam.revoked = true
		}
		if !ok || t.used || fam == nil || fam.revoked || f.Has("scope") && f.Get("scope") != Scope {
			oauthError(w, 400, "invalid_grant")
			return
		}
		t.used = true
		s.refresh[key] = t
		s.issue(w, t.family)
	default:
		oauthError(w, 400, "unsupported_grant_type")
	}
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	f, ok := form(w, r)
	if !ok || !s.client(w, r, f) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clean()
	key := hash(f.Get("token"))
	t, ok := s.access[key]
	if !ok {
		t, ok = s.refresh[key]
	}
	if ok {
		if fam := s.families[t.family]; fam != nil {
			fam.revoked = true
		}
	}
	w.WriteHeader(200)
}

func (s *Server) Verify(ctx context.Context, raw string, r *http.Request) (*auth.TokenInfo, error) {
	if len(raw) != 43 {
		return nil, auth.ErrInvalidToken
	}
	s.mu.Lock()
	s.clean()
	t, ok := s.access[hash(raw)]
	fam := s.families[t.family]
	if !ok || fam == nil || fam.revoked {
		s.mu.Unlock()
		return nil, auth.ErrInvalidToken
	}
	u := fam.user
	expires := t.expires
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	p, e := s.lookup(ctx, u.OrgID, u.PrincipalID)
	if errors.Is(e, metadata.ErrInvalidToken) || errors.Is(e, metadata.ErrDenied) {
		return nil, auth.ErrInvalidToken
	}
	if e != nil {
		return nil, errors.New("identity storage unavailable")
	}
	if p.ExpiresAt.Before(expires) {
		expires = p.ExpiresAt
	}
	return &auth.TokenInfo{UserID: p.OrgID + "/" + p.ID, Scopes: []string{Scope}, Expiration: expires, Extra: map[string]any{"principal": p}}, nil
}
