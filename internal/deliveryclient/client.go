// Package deliveryclient implements private manual delivery and bounded local
// Context sessions. It never exposes credentials to MCP or decrypts a file.
package deliveryclient

import (
	"bytes"
	"context"
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
	"strings"
	"time"

	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/privatefile"
)

var ErrDelivery = errors.New("private download failed; inspect safe ticket status before retrying")

type Client struct {
	origin, caller, proof string
	http                  *http.Client
	registration          *v02.ClientRegistration
	contextProof          string
}
type Receipt struct {
	DownloadID   string `json:"downloadId"`
	CredentialID string `json:"credentialId"`
	State        string `json:"state"`
	File         string `json:"file"`
}

func New(origin, caller, authorizationFile string, allowLoopback bool) (*Client, error) {
	u, e := url.Parse(origin)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") {
		return nil, ErrDelivery
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || !allowLoopback || ip == nil || !ip.IsLoopback() {
			return nil, ErrDelivery
		}
	}
	if len(caller) < 16 || len(caller) > 4096 {
		return nil, ErrDelivery
	}
	raw, e := privatefile.Read(authorizationFile, 8192)
	if e != nil || v02.CheckJSON(raw) != nil {
		return nil, ErrDelivery
	}
	var auth struct {
		SchemaVersion string                        `json:"schemaVersion"`
		Authorization v02.DeliveryAuthorizationInfo `json:"authorization"`
		Token         string                        `json:"deliveryToken"`
		Client        *v02.ClientRegistration       `json:"client,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&auth) != nil || auth.SchemaVersion != "0.2" {
		return nil, ErrDelivery
	}
	info, _ := json.Marshal(auth.Authorization)
	var checked v02.DeliveryAuthorizationInfo
	if v02.Decode("DeliveryAuthorizationInfo", info, &checked) != nil {
		return nil, ErrDelivery
	}
	if auth.Client != nil {
		raw, _ := json.Marshal(auth.Client)
		var checkedClient v02.ClientRegistration
		if v02.Decode("ClientRegistration", raw, &checkedClient) != nil || checkedClient.State != "ACTIVE" || checkedClient.OwnerID != checked.OwnerID || checkedClient.AuthorizationID != checked.AuthorizationID {
			return nil, ErrDelivery
		}
	}
	expiry, e := time.Parse(time.RFC3339Nano, checked.ExpiresAt)
	if e != nil || !expiry.After(time.Now()) {
		return nil, ErrDelivery
	}
	token, e := base64.RawURLEncoding.Strict().DecodeString(auth.Token)
	if e != nil || len(token) != 32 || base64.RawURLEncoding.EncodeToString(token) != auth.Token {
		return nil, ErrDelivery
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, DisableKeepAlives: true, ResponseHeaderTimeout: 8 * time.Second}
	client := &http.Client{Transport: transport, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Client{origin: strings.TrimSuffix(origin, "/"), caller: caller, proof: auth.Token, http: client, registration: auth.Client}, nil
}
func (c *Client) request(ctx context.Context, method, id, suffix string) (*http.Response, error) {
	in, _ := json.Marshal(v02.DownloadIDInput{DownloadID: id})
	var checked v02.DownloadIDInput
	if v02.Decode("DownloadIDInput", in, &checked) != nil {
		return nil, ErrDelivery
	}
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader("{}")
	}
	r, e := http.NewRequestWithContext(ctx, method, c.origin+"/api/v2/credential-downloads/"+id+suffix, body)
	if e != nil {
		return nil, ErrDelivery
	}
	r.Header.Set("Authorization", "Bearer "+c.caller)
	r.Header.Set("X-MCPGuard-Delivery-Token", c.proof)
	if c.contextProof != "" {
		r.Header.Set("X-MCPGuard-Credential-Context", c.contextProof)
	}
	r.Header.Set("Accept", "application/octet-stream, application/json")
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/json")
	}
	response, e := c.http.Do(r)
	if e != nil {
		return nil, ErrDelivery
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, ErrDelivery
	}
	return response, nil
}
func (c *Client) Status(ctx context.Context, id string) (v02.DownloadTicket, error) {
	var out v02.DownloadTicket
	r, e := c.request(ctx, http.MethodGet, id, "")
	if e != nil {
		return out, e
	}
	defer r.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(r.Body, v02.MaxInputBytes+1))
	if e != nil || len(raw) > v02.MaxInputBytes || v02.Decode("DownloadTicket", raw, &out) != nil || out.DownloadID != id {
		return out, ErrDelivery
	}
	return out, nil
}
func checkEnvelope(raw []byte) error {
	var env v02.CredentialEnvelope
	if len(raw) > v02.MaxPackageBytes || v02.Decode("CredentialEnvelope", raw, &env) != nil {
		return ErrDelivery
	}
	canonical, _ := json.Marshal(env)
	if !bytes.Equal(raw, canonical) {
		return ErrDelivery
	}
	for _, field := range []struct {
		value  string
		length int
	}{{env.Nonce, 12}, {env.AuthenticationTag, 16}, {env.Ciphertext, 0}} {
		data, e := base64.RawURLEncoding.Strict().DecodeString(field.value)
		if e != nil || (field.length > 0 && len(data) != field.length) {
			return ErrDelivery
		}
	}
	return nil
}
func (c *Client) Claim(ctx context.Context, id, dir string) (Receipt, error) {
	var out Receipt
	if privatefile.CheckDir(dir) != nil {
		return out, ErrDelivery
	}
	status, e := c.Status(ctx, id)
	if e != nil || status.State != "READY" {
		return out, ErrDelivery
	}
	name := "mcpguard-" + status.CredentialID + ".mgcred"
	if privatefile.CheckNew(dir, name) != nil {
		return out, ErrDelivery
	}
	response, e := c.request(ctx, http.MethodPost, id, "/claim")
	if e != nil {
		return out, e
	}
	defer response.Body.Close()
	if response.Header.Get("Content-Type") != "application/octet-stream" || response.Header.Get("Content-Disposition") != `attachment; filename="`+name+`"` || response.Header.Get("Cache-Control") != "no-store" || response.ContentLength < 1 || response.ContentLength > v02.MaxPackageBytes {
		return out, ErrDelivery
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, v02.MaxPackageBytes+1))
	if e != nil || int64(len(raw)) != response.ContentLength || checkEnvelope(raw) != nil {
		return out, ErrDelivery
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != response.Header.Get("X-MCPGuard-Package-SHA256") {
		return out, ErrDelivery
	}
	if privatefile.WriteNew(dir, name, raw) != nil {
		return out, ErrDelivery
	}
	return Receipt{DownloadID: id, CredentialID: status.CredentialID, State: "SAVED", File: name}, nil
}
