package deliveryclient

import (
	"encoding/json"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"io"
	"net/http"
	"strings"
)

// PrivateHeaders may only cross a host-owned header-helper channel or an HTTP
// transport. It deliberately has no exported secret fields or JSON marshaler.
type PrivateHeaders struct {
	caller, delivery, context string
	packageParts              []string
}

func (PrivateHeaders) String() string { return "[private MCP host headers]" }
func (s *Session) HeadersForHost() (PrivateHeaders, error) {
	if s.state.Context == nil || s.state.Context.Context.State != "ACTIVE" || s.client.contextProof == "" {
		return PrivateHeaders{}, ErrSession
	}
	h := PrivateHeaders{caller: s.client.caller, delivery: s.client.proof, context: s.client.contextProof}
	for _, entry := range s.state.Entries {
		if raw, e := s.file(entry); e == nil {
			var env v02.CredentialEnvelope
			if json.Unmarshal(raw, &env) == nil {
				h.packageParts = append(h.packageParts, env.Nonce, env.Ciphertext, env.AuthenticationTag)
			}
		}
	}
	return h, nil
}
func (h PrivateHeaders) Apply(r *http.Request) error {
	if h.caller == "" || h.context == "" {
		return ErrSession
	}
	r.Header.Set("Authorization", "Bearer "+h.caller)
	r.Header.Set("X-MCPGuard-Credential-Context", h.context)
	// The delivery proof belongs exclusively to private lifecycle requests.
	r.Header.Del("X-MCPGuard-Delivery-Token")
	return nil
}

// WritePrivateHost is explicitly a non-MCP output channel. Its output must
// never be routed to a tool result, MCP stdout, a model transcript or a log.
func (h PrivateHeaders) WritePrivateHost(w io.Writer) error {
	if h.caller == "" || h.context == "" {
		return ErrSession
	}
	return json.NewEncoder(w).Encode(map[string]string{"Authorization": "Bearer " + h.caller, "X-MCPGuard-Credential-Context": h.context})
}

// PublicResult rejects known secrets and private envelope fields before a
// result crosses the stdio protocol boundary. It does not rewrite business data.
func (h PrivateHeaders) PublicResult(value any, strict bool) bool {
	raw, e := json.Marshal(value)
	if e != nil || len(raw) > 128<<10 {
		return false
	}
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		return false
	}
	secrets := []string{h.caller, h.delivery, h.context}
	secrets = append(secrets, h.packageParts...)
	if parts := strings.SplitN(h.context, ".", 2); len(parts) == 2 {
		secrets = append(secrets, parts[1])
	}
	var safe func(any, int) bool
	safe = func(v any, depth int) bool {
		if depth > 32 {
			return false
		}
		switch x := v.(type) {
		case string:
			for _, s := range secrets {
				if s != "" && strings.Contains(x, s) {
					return false
				}
			}
			// JSON-in-text is also common in MCP content. Inspect its fields.
			if strict && len(x) > 0 && (x[0] == '{' || x[0] == '[') {
				var nested any
				if json.Unmarshal([]byte(x), &nested) == nil && !safe(nested, depth+1) {
					return false
				}
			}
		case []any:
			for _, item := range x {
				if !safe(item, depth+1) {
					return false
				}
			}
		case map[string]any:
			for key, item := range x {
				if strict {
					switch strings.ToLower(key) {
					case "contexttoken", "deliverytoken", "credentialpackage", "ciphertext", "authenticationtag", "nonce", "password", "refreshtoken", "nativedsn", "nativepassword", "privatekey", "authorization", "x-mcpguard-credential-context", "x-mcpguard-delivery-token":
						return false
					}
				}
				if !safe(item, depth+1) {
					return false
				}
			}
		}
		return true
	}
	return safe(decoded, 0)
}
