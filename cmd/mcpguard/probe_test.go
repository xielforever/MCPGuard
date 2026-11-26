package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeDistinguishesLivenessAndReadiness(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	t.Setenv("MCPGUARD_LISTEN", server.Listener.Addr().String())
	t.Setenv("ZHIQI_METADATA_SOURCE", "nacos")
	t.Setenv("ZHIQI_NACOS_URL", "")

	if err := probe(context.Background(), "live"); err != nil {
		t.Fatal(err)
	}
	if err := probe(context.Background(), "ready"); err == nil {
		t.Fatal("unready gateway passed readiness probe")
	}
}
