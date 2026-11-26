package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestConsoleStaticAndProxyPreserveHostAndOrigin(t *testing.T) {
	type captured struct {
		host   string
		origin string
		path   string
	}
	seen := make(chan captured, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- captured{host: r.Host, origin: r.Header.Get("Origin"), path: r.URL.Path}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	handler := newConsoleHandler(target)

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18082/console/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "<title>执契控制台</title>") {
		t.Fatal("console page was not served")
	}
	if !strings.Contains(response.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("console page lacks its content policy")
	}

	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18082/web/console.js", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/javascript") {
		t.Fatal("console JavaScript asset was not served")
	}

	request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:18082/console/api/state", strings.NewReader("{}"))
	request.Host = "127.0.0.1:18082"
	request.Header.Set("Origin", "http://127.0.0.1:18082")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("proxy returned %d", response.Code)
	}
	select {
	case got := <-seen:
		if got.host != "127.0.0.1:18082" || got.origin != "http://127.0.0.1:18082" ||
			got.path != "/console/api/state" {
			t.Fatalf("proxy changed host/origin/path: %+v", got)
		}
	default:
		t.Fatal("proxy did not reach the gateway")
	}

	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18082/livez", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "ok") {
		t.Fatal("frontend liveness endpoint is unavailable")
	}
}

func TestConsoleListenerAndUpstreamValidation(t *testing.T) {
	if err := validateConsoleListen("127.0.0.1:8080", false); err != nil {
		t.Fatal("loopback listener rejected")
	}
	if err := validateConsoleListen("0.0.0.0:8080", false); err == nil {
		t.Fatal("non-loopback listener accepted without the flag")
	}
	if err := validateConsoleListen("0.0.0.0:8080", true); err != nil {
		t.Fatal("explicit non-loopback listener rejected")
	}
	if _, err := parseConsoleUpstream("http://zhiqi-gateway:18082"); err != nil {
		t.Fatal("valid upstream rejected")
	}
	for _, invalid := range []string{"", "ftp://zhiqi-gateway:18082", "http://user:pass@zhiqi-gateway:18082", "http://zhiqi-gateway:18082/path"} {
		if _, err := parseConsoleUpstream(invalid); err == nil {
			t.Fatalf("invalid upstream accepted: %s", invalid)
		}
	}
}

func TestConsoleProxyFailureIsSanitized(t *testing.T) {
	target, err := url.Parse("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	handler := newConsoleHandler(target)
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18082/readyz", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("unexpected proxy failure status: %d", response.Code)
	}
	body, _ := io.ReadAll(response.Result().Body)
	if strings.Contains(string(body), "127.0.0.1:1") {
		t.Fatal("proxy error disclosed the upstream address")
	}
}