package controlled

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConsoleDAssets(t *testing.T) {
	console := &Console{host: "127.0.0.1:18080"}
	mux := http.NewServeMux()
	console.Register(mux)
	for _, asset := range []struct{ path, mime, marker string }{
		{"/web/", "text/html", "<title>执契控制台</title>"},
		{"/web/console.js", "text/javascript", "MCPGuardUI"},
		{"/web/console.css", "text/css", "--mg-color-separator"},
		{"/web/ui/tokens.css", "text/css", "--mg-color-primary: #83f2ed"},
		{"/web/ui/components.css", "text/css", ".mg-dialog"},
		{"/web/ui/state-labels.js", "text/javascript", "MCPGuardUIStateLabels"},
		{"/web/ui/components.js", "text/javascript", "globalThis.MCPGuardUI"},
		{"/web/ui/zhiqi-seal-mark.png", "image/png", "\x89PNG\r\n\x1a\n"},
	} {
		t.Run(asset.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18080"+asset.path, nil)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), asset.mime) || !strings.Contains(response.Body.String(), asset.marker) {
				t.Fatalf("missing or invalid public UI asset %s", asset.path)
			}
			policy := response.Header().Get("Content-Security-Policy")
			if !strings.Contains(policy, "style-src 'self'") || !strings.Contains(policy, "img-src 'self'") || strings.Contains(policy, "unsafe-inline") || !strings.Contains(policy, "frame-ancestors 'none'") {
				t.Fatal("public console asset has weakened content policy")
			}
			if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("public console asset may be cached or sniffed")
			}
			request.Host = "untrusted.example"
			response = httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatal("asset host restriction bypassed")
			}
		})
	}
	for _, path := range []string{"/web/ui/", "/web/ui/assets.go", "/web/ui/tokens.generated.css", "/web/console.go", "/web/ui/private.json"} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18080"+path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("non-public asset exposed: %s", path)
		}
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18080/console/api/state", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatal("UI assets granted an anonymous management session")
	}
}
