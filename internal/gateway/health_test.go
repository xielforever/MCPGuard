package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
)

func TestHealthArgumentsAndSafeOutput(t *testing.T) {
	for _, raw := range []string{"", " ", `{}`, " { } \n"} {
		if !validHealthArguments([]byte(raw)) {
			t.Errorf("rejected empty arguments %q", raw)
		}
	}
	for _, raw := range []string{`null`, `[]`, `true`, `{"host":"other"}`, `{"sql":"SELECT 1"}`, `{"principalId":"other"}`, `{"timeoutMs":999999}`, `{"x":1,"x":2}`, `{} {}`, `{`, "{" + strings.Repeat(" ", 256) + "}"} {
		if validHealthArguments([]byte(raw)) {
			t.Errorf("accepted health arguments %q", raw)
		}
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(healthSchema, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	privateError := errors.New("postgres://user:private-password@127.0.0.1/database SELECT private_column")
	for _, failure := range []error{nil, privateError} {
		r := probeHealth(context.Background(), func(context.Context) error { return failure }, func(context.Context) error { return nil })
		if !r.Live || r.Ready != (failure == nil) {
			t.Fatal("liveness confused with dependency readiness", r)
		}
		b, _ := json.Marshal(r)
		if strings.Contains(string(b), "private-password") || strings.Contains(string(b), "SELECT") || strings.Contains(string(b), "127.0.0.1") {
			t.Fatal("dependency details disclosed", string(b))
		}
		var value any
		_ = json.Unmarshal(b, &value)
		if err := resolved.Validate(value); err != nil {
			t.Fatal("invalid health output", string(b), err)
		}
	}
}

func TestHealthProbesRunConcurrentlyAndRespectDeadline(t *testing.T) {
	started := make(chan struct{})
	r := probeHealth(context.Background(), func(ctx context.Context) error {
		select {
		case <-started:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, func(context.Context) error { close(started); return nil })
	if !r.Ready {
		t.Fatal("dependency probes were not independent", r)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	r = probeHealth(ctx, func(context.Context) error { return nil }, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	if !r.Live || r.Ready || r.Dependencies["businessPostgres"].Status != "TIMEOUT" || time.Since(start) > time.Second {
		t.Fatal("dependency timeout did not produce a bounded not-ready result", r)
	}
}

func TestHealthzIsOnlyLivenessWithoutDependencies(t *testing.T) {
	g := New(context.Background(), nil, nil)
	defer g.Close()
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"up"`) || strings.Contains(w.Body.String(), `"ready"`) {
		t.Fatal("process liveness depends on database readiness", w.Code, w.Body.String())
	}
}
