package deliveryclient

// This package is the private CLI component. None of these state files or
// methods is an MCP resource/tool result, and it has no package decryption key.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/privatefile"
)

var ErrSession = errors.New("private session operation failed; inspect current identity, context and safe task status")

type CacheLimits struct {
	Files              int   `json:"files"`
	Bytes              int64 `json:"bytes"`
	PageSize           int   `json:"pageSize"`
	Pages              int   `json:"pages"`
	RenewBeforeSeconds int   `json:"renewBeforeSeconds"`
}

func DefaultCacheLimits() CacheLimits { return CacheLimits{128, 16 << 20, 20, 5, 30} }
func (l CacheLimits) valid() bool {
	return l.Files > 0 && l.Files <= v02.MaxCacheFiles && l.Bytes >= v02.MaxPackageBytes && l.Bytes <= v02.MaxCacheBytes && l.PageSize > 0 && l.PageSize <= v02.MaxSyncPageSize && l.Pages > 0 && l.Pages <= v02.MaxSyncPages && l.RenewBeforeSeconds >= 0 && l.RenewBeforeSeconds <= v02.MaxContextTtlSeconds
}

type cacheEntry struct {
	CredentialID string `json:"credentialId"`
	ResourceID   string `json:"resourceId"`
	GrantID      string `json:"grantId"`
	LeaseID      string `json:"leaseId"`
	ExpiresAt    string `json:"expiresAt"`
	DownloadID   string `json:"downloadId"`
	Digest       string `json:"digest"`
	State        string `json:"state"`
}
type cacheState struct {
	Version    int                       `json:"version"`
	Gateway    string                    `json:"gateway"`
	OrgID      string                    `json:"orgId"`
	OwnerID    string                    `json:"ownerId"`
	ClientID   string                    `json:"clientInstanceId"`
	CallerHash string                    `json:"callerHash"`
	Limits     CacheLimits               `json:"limits"`
	Context    *v02.ContextPrivateResult `json:"context,omitempty"`
	CreateKey  string                    `json:"createKey,omitempty"`
	Entries    map[string]*cacheEntry    `json:"entries"`
	Selected   map[string]string         `json:"selected"`
	Commands   map[string]string         `json:"commands"`
}
type Session struct {
	client *Client
	dir    string
	state  cacheState
	unlock func()
}

// SessionSummary contains safe metadata only. The ContextPrivateResult stays in
// the private cache and must never be marshaled by a command output path.
type SessionSummary struct {
	Context          v02.CredentialContext   `json:"context"`
	Activations      []v02.ContextActivation `json:"activations"`
	CachedFiles      int                     `json:"cachedFiles"`
	Pages            int                     `json:"pages"`
	Tasks            int                     `json:"tasks"`
	Saved            int                     `json:"saved"`
	Activated        int                     `json:"activated"`
	Replacements     int                     `json:"replacements"`
	UnavailableFiles int                     `json:"unavailableFiles"`
	More             bool                    `json:"more"`
}
type wireFailure int

func (wireFailure) Error() string { return "private session request denied or unavailable" }
func digestOf(raw []byte) string  { d := sha256.Sum256(raw); return hex.EncodeToString(d[:]) }
func strictLocal(raw []byte, out any) error {
	if v02.CheckJSON(raw) != nil {
		return ErrSession
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return ErrSession
	}
	return nil
}
func OpenSession(c *Client, dir string, create bool, limits CacheLimits) (*Session, error) {
	if c == nil || c.registration == nil || !limits.valid() {
		return nil, ErrSession
	}
	if create {
		if privatefile.EnsureDir(dir) != nil {
			return nil, ErrSession
		}
	}
	if privatefile.CheckDir(dir) != nil {
		return nil, ErrSession
	}
	unlock, e := privatefile.Lock(dir)
	if e != nil {
		return nil, ErrSession
	}
	s := &Session{client: c, dir: dir, unlock: unlock}
	fail := func() (*Session, error) { unlock(); return nil, ErrSession }
	if privatefile.CleanupTemps(dir, 16) != nil {
		return fail()
	}
	path := filepath.Join(dir, "session.json")
	if _, e = os.Lstat(path); os.IsNotExist(e) && create {
		r := c.registration
		s.state = cacheState{Version: 1, Gateway: c.origin, OrgID: r.OrgID, OwnerID: r.OwnerID, ClientID: r.ClientInstanceID, CallerHash: digestOf([]byte(c.caller)), Limits: limits, Entries: map[string]*cacheEntry{}, Selected: map[string]string{}, Commands: map[string]string{}}
	} else {
		raw, e := privatefile.Read(path, 1<<20)
		if e != nil || strictLocal(raw, &s.state) != nil {
			return fail()
		}
	}
	r := c.registration
	st := &s.state
	if st.Version != 1 || st.Gateway != c.origin || st.OrgID != r.OrgID || st.OwnerID != r.OwnerID || st.ClientID != r.ClientInstanceID || st.CallerHash != digestOf([]byte(c.caller)) || !st.Limits.valid() || st.Entries == nil || st.Selected == nil || st.Commands == nil || len(st.Entries) > st.Limits.Files || len(st.Commands) > 2048 {
		return fail()
	}
	for id, entry := range st.Entries {
		if entry == nil || id != entry.CredentialID || !validID(id) || !validID(entry.ResourceID) || !validID(entry.GrantID) || !validID(entry.LeaseID) || !validID(entry.DownloadID) || len(entry.Digest) != 64 {
			return fail()
		}
	}
	for rid, cid := range st.Selected {
		entry := st.Entries[cid]
		if entry == nil || entry.ResourceID != rid {
			return fail()
		}
	}
	if st.Context != nil {
		raw, _ := json.Marshal(st.Context)
		var checked v02.ContextPrivateResult
		if v02.Decode("ContextPrivateResult", raw, &checked) != nil || !s.owns(checked.Context) {
			return fail()
		}
		token := checked.ContextToken
		if _, e := decodeToken(token); e != nil {
			return fail()
		}
		c.contextProof = checked.Context.ContextID + "." + token
	}
	if privatefile.EnsureDir(filepath.Join(dir, "files")) != nil {
		return fail()
	}
	if privatefile.CleanupTemps(filepath.Join(dir, "files"), v02.MaxCacheFiles+16) != nil {
		return fail()
	}
	return s, nil
}
func validID(id string) bool { u, e := uuid.Parse(id); return e == nil && u.String() == id }
func decodeToken(token string) ([]byte, error) {
	b, e := base64.RawURLEncoding.Strict().DecodeString(token)
	if e != nil || len(b) != 32 || base64.RawURLEncoding.EncodeToString(b) != token {
		return nil, ErrSession
	}
	return b, nil
}
func (s *Session) Close() {
	if s.unlock != nil {
		s.unlock()
		s.unlock = nil
	}
}
func (s *Session) owns(c v02.CredentialContext) bool {
	return c.OrgID == s.state.OrgID && c.OwnerID == s.state.OwnerID && c.ClientInstanceID == s.state.ClientID
}
func (s *Session) save() error {
	if len(s.state.Commands) > 2048 {
		return ErrSession
	}
	raw, e := json.Marshal(s.state)
	if e != nil || len(raw) > 1<<20 || privatefile.Replace(s.dir, "session.json", raw) != nil {
		return ErrSession
	}
	return nil
}
func (s *Session) key(operation string) (string, error) {
	if k := s.state.Commands[operation]; k != "" {
		return k, nil
	}
	k := uuid.NewString()
	s.state.Commands[operation] = k
	return k, s.save()
}
func (s *Session) request(ctx context.Context, method, path, name string, in, out any, create bool) error {
	var body io.Reader
	if in != nil {
		raw, e := json.Marshal(in)
		if e != nil || len(raw) > v02.MaxInputBytes {
			return ErrSession
		}
		body = bytes.NewReader(raw)
	}
	r, e := http.NewRequestWithContext(ctx, method, s.client.origin+path, body)
	if e != nil {
		return ErrSession
	}
	r.Header.Set("Authorization", "Bearer "+s.client.caller)
	r.Header.Set("X-MCPGuard-Delivery-Token", s.client.proof)
	r.Header.Set("Accept", "application/json")
	if !create {
		if s.client.contextProof == "" {
			return ErrSession
		}
		r.Header.Set("X-MCPGuard-Credential-Context", s.client.contextProof)
	}
	if in != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	response, e := s.client.http.Do(r)
	if e != nil {
		return ErrSession
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return wireFailure(response.StatusCode)
	}
	if response.Header.Get("Cache-Control") != "no-store" || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		return ErrSession
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, v02.MaxInputBytes+1))
	if e != nil || len(raw) > v02.MaxInputBytes || v02.Decode(name, raw, out) != nil {
		return ErrSession
	}
	return nil
}
func (s *Session) contextPath() string {
	if s.state.Context == nil {
		return ""
	}
	return "/api/v2/credential-contexts/" + s.state.Context.Context.ContextID
}
func (s *Session) Init(ctx context.Context, replace bool) (SessionSummary, error) {
	if s.state.Context != nil && !replace {
		return s.Status(ctx)
	}
	if s.state.CreateKey != "" && !replace {
		return SessionSummary{}, ErrSession
	}
	s.state.CreateKey = uuid.NewString()
	if e := s.save(); e != nil {
		return SessionSummary{}, e
	}
	in := v02.ContextCreateInput{ClientLabel: "private-cli", RequestKey: s.state.CreateKey}
	var out v02.ContextPrivateResult
	if e := s.request(ctx, http.MethodPost, "/api/v2/credential-contexts", "ContextPrivateResult", in, &out, true); e != nil {
		return SessionSummary{}, ErrSession
	}
	if !s.owns(out.Context) || out.Context.State != "ACTIVE" {
		return SessionSummary{}, ErrSession
	}
	if _, e := decodeToken(out.ContextToken); e != nil {
		return SessionSummary{}, ErrSession
	}
	s.state.Context = &out
	s.state.CreateKey = ""
	s.client.contextProof = out.Context.ContextID + "." + out.ContextToken
	// A new Context never inherits server activations. Files are submitted again
	// by the next explicit bounded sync, against current authorization.
	for _, entry := range s.state.Entries {
		entry.State = "CACHED"
	}
	if e := s.save(); e != nil {
		return SessionSummary{}, e
	}
	return s.Status(ctx)
}
func (s *Session) status(ctx context.Context, active bool) (v02.ContextStatusResult, error) {
	var out v02.ContextStatusResult
	if s.state.Context == nil {
		return out, ErrSession
	}
	if e := s.request(ctx, http.MethodGet, s.contextPath(), "ContextStatusResult", nil, &out, false); e != nil {
		return out, ErrSession
	}
	if !s.owns(out.Context) || out.Context.ContextID != s.state.Context.Context.ContextID {
		return out, ErrSession
	}
	s.state.Context.Context = out.Context
	if e := s.save(); e != nil {
		return out, e
	}
	if active && out.Context.State != "ACTIVE" {
		return out, ErrSession
	}
	return out, nil
}
func summaryOf(st v02.ContextStatusResult, n int) SessionSummary {
	return SessionSummary{Context: st.Context, Activations: st.Activations, CachedFiles: n}
}
func (s *Session) Status(ctx context.Context) (SessionSummary, error) {
	st, e := s.status(ctx, false)
	return summaryOf(st, len(s.state.Entries)), e
}
func (s *Session) RenewContext(ctx context.Context, closeContext bool) (SessionSummary, error) {
	st, e := s.status(ctx, !closeContext)
	if e != nil {
		return SessionSummary{}, e
	}
	op := "renew/"
	method := http.MethodPost
	path := s.contextPath() + "/renewals"
	if closeContext {
		op = "close/"
		method = http.MethodDelete
		path = s.contextPath()
	}
	key, e := s.key(op + st.Context.ContextID + "/" + st.Context.Revision)
	if e != nil {
		return SessionSummary{}, e
	}
	var out v02.CredentialContext
	if e = s.request(ctx, method, path, "CredentialContext", v02.ContextRenewInput{ExpectedRevision: st.Context.Revision, RequestKey: key}, &out, false); e != nil {
		return SessionSummary{}, ErrSession
	}
	if !s.owns(out) || out.ContextID != st.Context.ContextID {
		return SessionSummary{}, ErrSession
	}
	s.state.Context.Context = out
	if e = s.save(); e != nil {
		return SessionSummary{}, e
	}
	return s.Status(ctx)
}
func (s *Session) RenewCredential(ctx context.Context, id string) (v02.PrepareDownloadResult, error) {
	var out v02.PrepareDownloadResult
	if !validID(id) {
		return out, ErrSession
	}
	if _, e := s.status(ctx, true); e != nil {
		return out, e
	}
	key, e := s.key("credential-renew/" + id)
	if e != nil {
		return out, e
	}
	if e = s.request(ctx, http.MethodPost, "/api/v2/credentials/"+id+"/renewals", "PrepareDownloadResult", v02.RenewCredentialInput{CredentialID: id, RequestKey: key}, &out, false); e != nil {
		return out, ErrSession
	}
	return out, nil
}
func (s *Session) Prepare(ctx context.Context, grant string) (v02.PrepareDownloadResult, error) {
	var out v02.PrepareDownloadResult
	if !validID(grant) {
		return out, ErrSession
	}
	if _, e := s.status(ctx, true); e != nil {
		return out, e
	}
	// This is an explicit private command, not an implicit resource account application.
	if e := s.request(ctx, http.MethodPost, "/api/v2/credential-packages", "PrepareDownloadResult", v02.PrepareDownloadInput{GrantID: grant, RequestKey: uuid.NewString()}, &out, false); e != nil {
		return out, ErrSession
	}
	return out, nil
}
func cacheName(id string) string { return "mcpguard-" + id + ".mgcred" }
func (s *Session) file(entry *cacheEntry) ([]byte, error) {
	raw, e := privatefile.Read(filepath.Join(s.dir, "files", cacheName(entry.CredentialID)), v02.MaxPackageBytes)
	if e != nil || digestOf(raw) != entry.Digest || checkEnvelope(raw) != nil {
		return nil, ErrSession
	}
	return raw, nil
}
func (s *Session) inventory() (int, int64, error) {
	dir, e := os.Open(filepath.Join(s.dir, "files"))
	if e != nil {
		return 0, 0, ErrSession
	}
	defer dir.Close()
	files, e := dir.ReadDir(s.state.Limits.Files + 1)
	if errors.Is(e, io.EOF) {
		e = nil
	}
	if e != nil || len(files) > s.state.Limits.Files {
		return 0, 0, ErrSession
	}
	var n int
	var size int64
	for _, f := range files {
		name := f.Name()
		id := strings.TrimSuffix(strings.TrimPrefix(name, "mcpguard-"), ".mgcred")
		if name != cacheName(id) || !validID(id) || f.IsDir() {
			return 0, 0, ErrSession
		}
		raw, e := privatefile.Read(filepath.Join(s.dir, "files", name), v02.MaxPackageBytes)
		if e != nil {
			return 0, 0, ErrSession
		}
		n++
		size += int64(len(raw))
	}
	if n > s.state.Limits.Files || size > s.state.Limits.Bytes {
		return 0, 0, ErrSession
	}
	return n, size, nil
}
func (s *Session) deactivate(ctx context.Context, entry *cacheEntry, revision string) error {
	key, e := s.key("deactivate/" + s.state.Context.Context.ContextID + "/" + entry.CredentialID + "/" + revision)
	if e != nil {
		return e
	}
	var out v02.ContextActivation
	return s.request(ctx, http.MethodPost, s.contextPath()+"/deactivations", "ContextActivation", v02.DeactivateInput{ResourceID: entry.ResourceID, RequestKey: key}, &out, false)
}
func (s *Session) activate(ctx context.Context, entry *cacheEntry, raw []byte, revision string) error {
	var env v02.CredentialEnvelope
	if json.Unmarshal(raw, &env) != nil {
		return ErrSession
	}
	key, e := s.key("activate/" + s.state.Context.Context.ContextID + "/" + entry.CredentialID + "/" + entry.DownloadID + "/" + revision)
	if e != nil {
		return e
	}
	var out v02.ContextActivation
	did := entry.DownloadID
	in := v02.ActivationInput{CredentialID: entry.CredentialID, ResourceID: entry.ResourceID, CredentialPackage: env, DownloadID: &did, RequestKey: key}
	if e = s.request(ctx, http.MethodPost, s.contextPath()+"/activations", "ContextActivation", in, &out, false); e != nil {
		return e
	}
	if out.State != "ACTIVE" || out.ContextID != s.state.Context.Context.ContextID || out.CredentialID != entry.CredentialID || out.ResourceID != entry.ResourceID {
		return ErrSession
	}
	entry.State = "ACTIVE"
	return s.save()
}
func (s *Session) replace(ctx context.Context, entry *cacheEntry) (v02.PrepareDownloadResult, error) {
	var out v02.PrepareDownloadResult
	key, e := s.key("replace/" + entry.DownloadID)
	if e != nil {
		return out, e
	}
	e = s.request(ctx, http.MethodPost, "/api/v2/credential-downloads/"+entry.DownloadID+"/replacements", "PrepareDownloadResult", v02.ReplaceDownloadInput{RequestKey: key}, &out, false)
	if e == nil {
		entry.DownloadID = out.DownloadID
		e = s.save()
	}
	return out, e
}
func isFileDenial(e error) bool { var w wireFailure; return errors.As(e, &w) && (w == 403 || w == 409) }
func (s *Session) claim(ctx context.Context, ticket v02.DownloadTicket) (*cacheEntry, error) {
	if ticket.ClientInstanceID == nil || *ticket.ClientInstanceID != s.state.ClientID || ticket.ResourceID == nil || ticket.GrantID == nil || ticket.State != "READY" {
		return nil, ErrSession
	}
	n, size, e := s.inventory()
	if e != nil {
		return nil, e
	}
	path := filepath.Join(s.dir, "files", cacheName(ticket.CredentialID))
	_, exists := os.Lstat(path)
	if os.IsNotExist(exists) && (n >= s.state.Limits.Files || size+v02.MaxPackageBytes > s.state.Limits.Bytes || len(s.state.Entries) >= s.state.Limits.Files && s.state.Entries[ticket.CredentialID] == nil) {
		return nil, ErrSession
	}
	response, e := s.client.request(ctx, http.MethodPost, ticket.DownloadID, "/claim")
	if e != nil {
		return nil, e
	}
	defer response.Body.Close()
	if response.Header.Get("Content-Type") != "application/octet-stream" || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Content-Disposition") != `attachment; filename="`+cacheName(ticket.CredentialID)+`"` || response.ContentLength < 1 || response.ContentLength > v02.MaxPackageBytes {
		return nil, ErrSession
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, v02.MaxPackageBytes+1))
	if e != nil || int64(len(raw)) != response.ContentLength || checkEnvelope(raw) != nil || digestOf(raw) != response.Header.Get("X-MCPGuard-Package-SHA256") {
		return nil, ErrSession
	}
	if os.IsNotExist(exists) {
		if size+int64(len(raw)) > s.state.Limits.Bytes || privatefile.WriteNew(filepath.Dir(path), filepath.Base(path), raw) != nil {
			return nil, ErrSession
		}
	} else {
		old, e := privatefile.Read(path, v02.MaxPackageBytes)
		if e != nil || !bytes.Equal(old, raw) {
			return nil, ErrSession
		}
	}
	entry := &cacheEntry{CredentialID: ticket.CredentialID, ResourceID: *ticket.ResourceID, GrantID: *ticket.GrantID, LeaseID: ticket.LeaseID, ExpiresAt: ticket.CredentialExpiresAt, DownloadID: ticket.DownloadID, Digest: digestOf(raw), State: "CACHED"}
	s.state.Entries[entry.CredentialID] = entry
	s.state.Selected[entry.ResourceID] = entry.CredentialID
	if e = s.save(); e != nil {
		return nil, e
	}
	return entry, nil
}

// Sync performs bounded work only: one cache scan, at most five queue pages
// (100 tasks), and no unbounded retry loop. Each invocation starts at the queue
// head; immutable completion receipts prevent replay and cursor staleness.
func (s *Session) Sync(ctx context.Context) (SessionSummary, error) {
	var result SessionSummary
	st, e := s.status(ctx, true)
	if e != nil {
		return result, e
	}
	exp, _ := time.Parse(time.RFC3339Nano, st.Context.ExpiresAt)
	absolute, _ := time.Parse(time.RFC3339Nano, st.Context.AbsoluteExpiresAt)
	if s.state.Limits.RenewBeforeSeconds > 0 && time.Until(exp) < time.Duration(s.state.Limits.RenewBeforeSeconds)*time.Second && exp.Before(absolute) {
		if _, e = s.RenewContext(ctx, false); e != nil {
			return result, e
		}
		st, e = s.status(ctx, true)
		if e != nil {
			return result, e
		}
	}
	if _, _, e = s.inventory(); e != nil {
		return result, e
	}
	active := map[string]v02.ContextActivation{}
	for _, a := range st.Activations {
		active[a.ResourceID] = a
	}
	result = summaryOf(st, len(s.state.Entries))
	for _, entry := range s.state.Entries {
		if s.state.Selected[entry.ResourceID] != entry.CredentialID {
			continue
		}
		a, has := active[entry.ResourceID]
		if has && a.CredentialID != entry.CredentialID {
			continue
		} // retain history, never restore an older selection
		if has && a.State == "EXPIRED" && a.CredentialID == entry.CredentialID {
			entry.State = a.State
			result.UnavailableFiles++
			continue
		}
		raw, fileError := s.file(entry)
		if fileError != nil {
			entry.State = "MISSING_OR_INVALID"
			result.UnavailableFiles++
			if has && a.State == "ACTIVE" {
				if e = s.deactivate(ctx, entry, a.Revision); e != nil {
					return result, ErrSession
				}
			}
			// Missing files may be recovered via a fresh ticket for the same
			// package. Corrupt/unsafe existing files are never overwritten.
			if _, er := os.Lstat(filepath.Join(s.dir, "files", cacheName(entry.CredentialID))); os.IsNotExist(er) {
				if _, e = s.replace(ctx, entry); e == nil {
					result.Replacements++
				} else if !isFileDenial(e) {
					return result, ErrSession
				}
			}
			continue
		}
		if has && a.State == "ACTIVE" && a.CredentialID == entry.CredentialID {
			entry.State = "ACTIVE"
			continue
		}
		if e = s.activate(ctx, entry, raw, a.Revision); e != nil {
			if !isFileDenial(e) {
				return result, ErrSession
			}
			entry.State = "UNUSABLE"
			result.UnavailableFiles++
		} else {
			result.Activated++
		}
	}
	if e = s.save(); e != nil {
		return result, e
	}
	after := ""
	for page := 0; page < s.state.Limits.Pages; page++ {
		path := "/api/v2/credential-downloads"
		separator := "?"
		// The protocol maximum delegates to the server's possibly smaller
		// configured page bound. Explicit tighter local limits remain explicit.
		if s.state.Limits.PageSize < v02.MaxSyncPageSize {
			path += "?limit=" + strconv.Itoa(s.state.Limits.PageSize)
			separator = "&"
		}
		if after != "" {
			path += separator + "after=" + url.QueryEscape(after)
		}
		var queue v02.DownloadQueue
		if e = s.request(ctx, http.MethodGet, path, "DownloadQueue", nil, &queue, false); e != nil {
			return result, ErrSession
		}
		result.Pages++
		for _, ticket := range queue.Tickets {
			result.Tasks++
			if ticket.ClientInstanceID == nil || *ticket.ClientInstanceID != s.state.ClientID {
				return result, ErrSession
			}
			entry := s.state.Entries[ticket.CredentialID]
			if ticket.State == "READY" {
				entry, e = s.claim(ctx, ticket)
				if e != nil {
					return result, ErrSession
				}
				result.Saved++
			} else if ticket.State == "CLAIMED" && entry != nil {
				entry.DownloadID = ticket.DownloadID
			}
			if ticket.State == "READY" || ticket.State == "CLAIMED" {
				if entry != nil {
					if raw, er := s.file(entry); er == nil {
						if e = s.activate(ctx, entry, raw, active[entry.ResourceID].Revision); e == nil {
							result.Activated++
							continue
						} else if !isFileDenial(e) {
							return result, ErrSession
						}
					}
				}
			}
			if ticket.ResourceID == nil || ticket.GrantID == nil {
				return result, ErrSession
			}
			if entry == nil {
				entry = &cacheEntry{CredentialID: ticket.CredentialID, ResourceID: *ticket.ResourceID, GrantID: *ticket.GrantID, LeaseID: ticket.LeaseID, ExpiresAt: ticket.CredentialExpiresAt, DownloadID: ticket.DownloadID}
			}
			if _, e = s.replace(ctx, entry); e == nil {
				result.Replacements++
			} else if !isFileDenial(e) {
				return result, ErrSession
			}
		}
		after = queue.NextAfter
		result.More = after != ""
		if after == "" {
			break
		}
	}
	final, e := s.status(ctx, true)
	if e != nil {
		return result, e
	}
	result.Context = final.Context
	result.Activations = final.Activations
	result.CachedFiles = len(s.state.Entries)
	if e = s.save(); e != nil {
		return result, e
	}
	return result, nil
}
