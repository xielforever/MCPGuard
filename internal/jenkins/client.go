package jenkins

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/xielforever/MCPGuard/internal/privatefile"
)

var (
	ErrInput          = errors.New("invalid Jenkins request")
	ErrDenied         = errors.New("Jenkins task is outside the configured scope")
	ErrSchemaChanged  = errors.New("Jenkins task parameter schema changed")
	ErrUnavailable    = errors.New("Jenkins dependency unavailable")
	ErrOutcomeUnknown = errors.New("Jenkins submission outcome unknown")
	ErrRejected       = errors.New("Jenkins rejected the build submission")
)

const (
	maxConfigBytes   = 32 << 10
	maxResponseBytes = 1 << 20
	maxDepth         = 8
)

type Config struct {
	ControllerID string   `json:"controllerId"`
	OrgID        string   `json:"orgId"`
	BaseURL      string   `json:"baseUrl"`
	Username     string   `json:"username"`
	APIToken     string   `json:"apiToken"`
	Folders      []string `json:"folders"`
}

// Client is bound to one operator-configured controller, tenant and folder
// scope. URLs and credentials are never accepted from tool input.
type Client struct {
	cfg     Config
	base    *url.URL
	binding string
	http    *http.Client
}

func Open(path string) (*Client, error) {
	raw, err := privatefile.Read(path, maxConfigBytes)
	if err != nil {
		return nil, ErrInput
	}
	var cfg Config
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&cfg); err != nil || d.Decode(new(any)) != io.EOF {
		return nil, ErrInput
	}
	return New(cfg, nil, nil)
}

// New binds a controller to the provided destination policy and TLS settings.
// Nil settings preserve the behavior of the operator-managed file loader.
func New(cfg Config, dial func(context.Context, string, string) (net.Conn, error), clientTLS *tls.Config) (*Client, error) {
	org, err := uuid.Parse(cfg.OrgID)
	if err != nil || org.String() != cfg.OrgID || org == uuid.Nil ||
		!safeName(cfg.ControllerID) || !validCredential(cfg.Username, 256) ||
		strings.Contains(cfg.Username, ":") || !validCredential(cfg.APIToken, 1024) ||
		len(cfg.Folders) == 0 || len(cfg.Folders) > 32 {
		return nil, ErrInput
	}
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Hostname() == "" || base.User != nil || base.RawQuery != "" ||
		base.ForceQuery || base.Fragment != "" || base.Opaque != "" {
		return nil, ErrInput
	}
	if base.Scheme != "https" && !(base.Scheme == "http" && net.ParseIP(base.Hostname()).IsLoopback()) {
		return nil, ErrInput
	}
	base.Path = strings.TrimSuffix(base.Path, "/")
	if base.RawPath != "" || (base.Path != "" && !validFullName(strings.TrimPrefix(base.Path, "/"))) {
		return nil, ErrInput
	}
	for _, folder := range cfg.Folders {
		if folder != "" && !validFullName(folder) {
			return nil, ErrInput
		}
	}
	folders := slices.Clone(cfg.Folders)
	slices.Sort(folders)
	cfg.Folders = slices.Compact(folders)
	raw, _ := json.Marshal([]any{cfg.ControllerID, cfg.OrgID, base.String(), cfg.Username, cfg.Folders})
	hash := sha256.Sum256(raw)
	if dial == nil {
		dial = (&net.Dialer{Timeout: 3 * time.Second}).DialContext
	}
	if clientTLS == nil {
		clientTLS = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		clientTLS = clientTLS.Clone()
	}
	return &Client{
		cfg:     cfg,
		base:    base,
		binding: hex.EncodeToString(hash[:]),
		http: &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Transport: &http.Transport{
				DialContext:            dial,
				TLSClientConfig:        clientTLS,
				TLSHandshakeTimeout:    3 * time.Second,
				DisableKeepAlives:      true,
				MaxResponseHeaderBytes: 16 << 10,
			},
		},
	}, nil
}

// ValidFolder reports whether folder is a non-root Jenkins folder path.
func ValidFolder(folder string) bool { return validFullName(folder) }

func validCredential(value string, max int) bool {
	return value != "" && len(value) <= max && !strings.ContainsFunc(value, unicode.IsControl)
}

func safeName(name string) bool {
	return name != "" && len(name) <= 128 && name == strings.TrimSpace(name) &&
		name != "." && name != ".." &&
		!strings.ContainsFunc(name, func(r rune) bool {
			return !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_.- ", r))
		})
}

func validFullName(name string) bool {
	parts := strings.Split(name, "/")
	if len(parts) > maxDepth || len(name) > 512 {
		return false
	}
	for _, part := range parts {
		if !safeName(part) {
			return false
		}
	}
	return true
}

func (c *Client) OrgID() string        { return c.cfg.OrgID }
func (c *Client) ControllerID() string { return c.cfg.ControllerID }
func (c *Client) Binding() string      { return c.binding }

func (c *Client) inScope(name string) bool {
	if !validFullName(name) {
		return false
	}
	for _, folder := range c.cfg.Folders {
		if folder == "" || strings.HasPrefix(name, folder+"/") {
			return true
		}
	}
	return false
}

func (c *Client) reference(name string) string {
	mac := hmac.New(sha256.New, []byte(c.cfg.APIToken))
	_, _ = mac.Write([]byte("mcpguard-jenkins-job/v1/" + c.binding + "/" + name))
	return base64.RawURLEncoding.EncodeToString([]byte(name)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (c *Client) jobName(ref string) (string, error) {
	if len(ref) > 768 {
		return "", ErrInput
	}
	payload, _, ok := strings.Cut(ref, ".")
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if !ok || err != nil {
		return "", ErrInput
	}
	name := string(raw)
	if !c.inScope(name) || !hmac.Equal([]byte(ref), []byte(c.reference(name))) {
		return "", ErrDenied
	}
	return name, nil
}

func jobPath(fullName string) string {
	if fullName == "" {
		return ""
	}
	return "/job/" + strings.ReplaceAll(fullName, "/", "/job/")
}

func (c *Client) endpoint(path string, query url.Values) *url.URL {
	u := *c.base
	u.Path += path
	u.RawQuery = query.Encode()
	return &u
}

func (c *Client) get(ctx context.Context, path, tree string, output any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint(path, url.Values{"tree": {tree}}).String(), nil)
	if err != nil {
		return ErrUnavailable
	}
	req.SetBasicAuth(c.cfg.Username, c.cfg.APIToken)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		return ErrDenied
	}
	if resp.StatusCode != http.StatusOK {
		return ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes || json.Unmarshal(raw, output) != nil {
		return ErrUnavailable
	}
	return nil
}
