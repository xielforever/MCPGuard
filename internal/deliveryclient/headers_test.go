package deliveryclient

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestPrivateHeadersHaveExplicitHostOnlySink(t *testing.T) {
	h := PrivateHeaders{caller: strings.Repeat("b", 32), delivery: strings.Repeat("d", 43), context: "context-id." + strings.Repeat("x", 43)}
	r, _ := http.NewRequest(http.MethodPost, "https://gateway.invalid/mcp", nil)
	r.Header.Set("X-MCPGuard-Delivery-Token", "untrusted")
	if h.Apply(r) != nil || r.Header.Get("Authorization") != "Bearer "+h.caller || r.Header.Get("X-MCPGuard-Credential-Context") != h.context || r.Header.Get("X-MCPGuard-Delivery-Token") != "" {
		t.Fatal("wrong transport headers")
	}
	var buf bytes.Buffer
	if h.WritePrivateHost(&buf) != nil {
		t.Fatal("private sink")
	}
	var headers map[string]string
	if json.Unmarshal(buf.Bytes(), &headers) != nil || len(headers) != 2 {
		t.Fatal("private output shape")
	}
	encoded, _ := json.Marshal(h)
	if string(encoded) != "{}" || strings.Contains(h.String(), h.caller) {
		t.Fatal("implicit secret encoding")
	}
}
func TestProtocolGuardRejectsPrivateMaterialAndJSONInText(t *testing.T) {
	h := PrivateHeaders{caller: strings.Repeat("a", 32), context: "id." + strings.Repeat("b", 43), packageParts: []string{"opaque-encrypted-packet-value"}}
	for _, v := range []any{map[string]any{"text": h.caller}, map[string]any{"text": strings.Repeat("b", 43)}, map[string]any{"text": "opaque-encrypted-packet-value"}, map[string]any{"contextToken": "different-value"}, map[string]any{"text": `{"ciphertext":"different-value"}`}, map[string]any{"text": `{"password":"native-value"}`}} {
		if h.PublicResult(v, true) {
			t.Fatal("private material accepted")
		}
	}
	if !h.PublicResult(map[string]any{"credentialId": "safe-id", "state": "ACTIVE", "text": "private sync pending"}, true) {
		t.Fatal("safe receipt rejected")
	}
}
