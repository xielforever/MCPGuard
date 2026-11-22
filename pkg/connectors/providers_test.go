package connectors

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func testProviders(t *testing.T) *Providers {
	t.Helper()
	policy, err := NewEgressPolicy("127.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	return NewProviders(policy)
}

func TestProviderValidation(t *testing.T) {
	providers := testProviders(t)
	credentials := Credentials{Username: "reader", Secret: "fixture-credential"}
	tests := []struct {
		name        string
		kind        Kind
		config      Config
		credentials Credentials
		want        error
	}{
		{"normalized PG port", PostgreSQL, Config{Host: " 127.0.0.1 ", Database: "orders", Schema: "public"}, credentials, nil},
		{"normalized MySQL port", MySQL, Config{Host: " 127.0.0.1 ", Database: "orders"}, credentials, nil},
		{"qualified MySQL database", MySQL, Config{Host: "127.0.0.1", Database: "other.orders"}, credentials, ErrInput},
		{"extra MySQL schema", MySQL, Config{Host: "127.0.0.1", Database: "orders", Schema: "public"}, credentials, ErrInput},
		{"MySQL database name above identifier limit", MySQL, Config{Host: "127.0.0.1", Database: strings.Repeat("库", 65)}, credentials, ErrInput},
		{"non-loopback MySQL without TLS", MySQL, Config{Host: "192.0.2.1", Database: "orders"}, credentials, ErrInput},
		{"Redis probe outside prefix", Redis, Config{Host: "127.0.0.1", KeyPrefix: "orders:", ProbeKey: "shipping:1"}, credentials, ErrInput},
		{"build permission for Redis", Redis, Config{Host: "127.0.0.1", KeyPrefix: "orders:", ProbeKey: "orders:1", AllowBuilds: true}, credentials, ErrInput},
		{"DNS name without TLS", Redis, Config{Host: "localhost", KeyPrefix: "orders:", ProbeKey: "orders:1"}, credentials, ErrInput},
		{"destination outside egress policy", PostgreSQL, Config{Host: "192.0.2.1", TLS: true, Database: "orders", Schema: "public"}, credentials, ErrDenied},
		{"embedded URL credentials", Jenkins, Config{BaseURL: "https://reader:secret@127.0.0.1", Folder: "/"}, credentials, ErrInput},
		{"implicit Jenkins root", Jenkins, Config{BaseURL: "https://127.0.0.1"}, credentials, ErrInput},
		{"path instead of namespace", Kubernetes, Config{BaseURL: "https://127.0.0.1", Namespace: "orders/production"}, Credentials{Secret: "fixture-token"}, ErrInput},
		{"namespace label", Kubernetes, Config{BaseURL: "https://127.0.0.1/", Namespace: "orders"}, Credentials{Secret: "fixture-token"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := providers.Validate(context.Background(), tt.kind, tt.config, tt.credentials)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Validate error = %v, want %v", err, tt.want)
			}
			if err == nil && tt.kind == PostgreSQL && (config.Host != "127.0.0.1" || config.Port != 5432) {
				t.Fatal("PostgreSQL endpoint was not normalized")
			}
			if err == nil && tt.kind == MySQL && (config.Host != "127.0.0.1" || config.Port != 3306) {
				t.Fatal("MySQL endpoint was not normalized")
			}
			if err == nil && tt.kind == Kubernetes && (!config.TLS || config.BaseURL != "https://127.0.0.1") {
				t.Fatal("Kubernetes TLS endpoint was not normalized")
			}
		})
	}
}

func TestMySQLReadScope(t *testing.T) {
	providers := testProviders(t)
	def := Definition{
		Type: MySQL, Config: Config{Host: "192.0.2.1", Port: 3306, TLS: true, Database: "orders"},
		Credentials: Credentials{Username: "reader", Secret: "fixture-credential"},
	}
	tests := []struct {
		name      string
		operation string
		args      ReadInput
		want      error
	}{
		{"list validates egress at dial", ListTables, ReadInput{}, ErrDenied},
		{"columns validates egress at dial", ListColumns, ReadInput{Table: "orders"}, ErrDenied},
		{"qualified table rejected before dialing", ListColumns, ReadInput{Table: "other.orders"}, ErrInput},
		{"extra selector rejected before dialing", ListColumns, ReadInput{Table: "orders", Key: "unexpected"}, ErrInput},
		{"arbitrary query rejected before dialing", "query", ReadInput{Table: "orders"}, ErrInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := providers.Read(context.Background(), def, tt.operation, tt.args)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Read error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestKubernetesConnectorReadIsScoped(t *testing.T) {
	providers := testProviders(t)
	var namespaceMismatch atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/cluster/apis/apps/v1/namespaces/orders/deployments" ||
			r.URL.Query().Get("limit") != "100" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		namespace := "orders"
		if namespaceMismatch.Load() {
			namespace = "shipping"
		}
		_, _ = io.WriteString(w, `{"apiVersion":"apps/v1","kind":"DeploymentList","metadata":{"continue":"next-page"},"items":[
		 {"metadata":{"name":"order-reader","namespace":"`+namespace+`","annotations":{"irrelevant":"annotation-value"}},
		  "spec":{"replicas":2},"status":{"readyReplicas":1,"availableReplicas":1}}]}`)
	}))
	defer upstream.Close()
	def := Definition{Type: Kubernetes, Config: Config{BaseURL: upstream.URL + "/cluster", Namespace: "orders"}, Credentials: Credentials{Secret: "fixture-token"}}
	result, err := providers.Read(context.Background(), def, ListDeployments, ReadInput{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(result)
	if err != nil || !strings.Contains(string(data), `"name":"order-reader"`) ||
		!strings.Contains(string(data), `"truncated":true`) || strings.Contains(string(data), "annotation-value") {
		t.Fatalf("Unexpected deployment summary: %s", data)
	}
	if _, err := providers.Read(context.Background(), def, "delete", ReadInput{}); !errors.Is(err, ErrInput) {
		t.Fatal("Unknown Kubernetes operation was not rejected")
	}
	namespaceMismatch.Store(true)
	if _, err := providers.Read(context.Background(), def, ListDeployments, ReadInput{}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("Cross-namespace response was not rejected")
	}
}

func TestRedisScopeAndResultBounds(t *testing.T) {
	providers := testProviders(t)
	def := Definition{Type: Redis, Config: Config{Host: "192.0.2.1", TLS: true, KeyPrefix: "orders:"}}
	if _, err := providers.Read(context.Background(), def, GetKey, ReadInput{Key: "shipping:1"}); !errors.Is(err, ErrDenied) {
		t.Fatal("Out-of-scope key must be rejected before dialing")
	}
	for _, raw := range []string{"$16385\r\n", "$1\r\nx\n", "+OK\r\n"} {
		if _, err := redisBulk(bufio.NewReader(strings.NewReader(raw))); !errors.Is(err, ErrUnavailable) {
			t.Fatal("Malformed or oversized Redis value must be rejected")
		}
	}
	if _, err := redisBulk(bufio.NewReader(strings.NewReader("-NOPERM denial detail\r\n"))); !errors.Is(err, ErrDenied) {
		t.Fatal("Redis ACL denial did not map to a safe error")
	}
}
