package runtimeconfig

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func setNacosMetadataEnv(t *testing.T, endpoint string) {
	t.Helper()
	t.Setenv("ZHIQI_METADATA_SOURCE", "nacos")
	t.Setenv("MCPGUARD_METADATA_DSN", "")
	t.Setenv("ZHIQI_NACOS_URL", endpoint)
	t.Setenv("ZHIQI_NACOS_NAMESPACE_ID", "zhiqi-acceptance")
	t.Setenv("ZHIQI_NACOS_GROUP", "ZHIQI")
	t.Setenv("ZHIQI_NACOS_DATA_ID", "metadata-pg.json")
	t.Setenv("ZHIQI_NACOS_USERNAME", "readonly")
	t.Setenv("ZHIQI_NACOS_PASSWORD", "nacos & password")
	t.Setenv("ZHIQI_METADATA_DATABASE", "zhiqi")
	t.Setenv("ZHIQI_METADATA_PORT", "")
	t.Setenv("ZHIQI_METADATA_CA_FILE", "/etc/zhiqi/pg ca.crt")
}

func TestMetadataDSNFromNacos(t *testing.T) {
	const dbPassword = "db @:/+ password"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/nacos/v3/auth/user/login":
			if r.Method != http.MethodPost || r.FormValue("username") != "readonly" || r.FormValue("password") != "nacos & password" {
				t.Error("unexpected Nacos login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"accessToken":"read-token","globalAdmin":false}`))
		case "/nacos/v3/client/cs/config":
			query := r.URL.Query()
			if r.Method != http.MethodGet || query.Get("namespaceId") != "zhiqi-acceptance" ||
				query.Get("groupName") != "ZHIQI" || query.Get("dataId") != "metadata-pg.json" ||
				query.Has("accessToken") || r.Header.Get("accessToken") != "read-token" {
				t.Error("unexpected Nacos configuration request")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			config, _ := json.Marshal(map[string]string{"host": "fd00::88", "username": "zhiqi@app", "password": dbPassword})
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"content": string(config)}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	setNacosMetadataEnv(t, server.URL)

	dsn, err := loadMetadataDSN(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("PostgreSQL URL could not be parsed")
	}
	password, _ := parsed.User.Password()
	if parsed.Host != "[fd00::88]:5432" || parsed.Path != "/zhiqi" ||
		parsed.User.Username() != "zhiqi@app" || password != dbPassword ||
		parsed.Query().Get("sslmode") != "verify-ca" || parsed.Query().Get("sslrootcert") != "/etc/zhiqi/pg ca.crt" {
		t.Fatal("PostgreSQL host, identity or TLS settings differ")
	}
}

func TestNacosMetadataFailsWithoutLeakingSecrets(t *testing.T) {
	for _, scenario := range []struct {
		name, login, config, want string
	}{
		{
			name: "login refused", login: `{"message":"private-marker"}`,
			want: "Nacos metadata login failed",
		},
		{
			name: "admin identity", login: `{"accessToken":"private-marker","globalAdmin":true}`,
			want: "Nacos metadata login requires a configuration reader",
		},
		{
			name: "not found", login: `{"accessToken":"private-marker"}`, config: `{"code":20004,"message":"private-marker"}`,
			want: "Nacos metadata configuration could not be read",
		},
		{
			name: "malformed", login: `{"accessToken":"private-marker"}`, config: `{"code":0,"data":{"content":"private-marker"}}`,
			want: "Nacos metadata configuration must be a JSON object",
		},
		{
			name: "not an IP", login: `{"accessToken":"private-marker"}`,
			config: `{"code":0,"data":{"content":"{\"host\":\"db\",\"username\":\"app\",\"password\":\"private-marker\"}"}}`,
			want:   "Nacos metadata configuration requires host IP, username and password",
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/nacos/v3/auth/user/login":
					_, _ = w.Write([]byte(scenario.login))
				case "/nacos/v3/client/cs/config":
					_, _ = w.Write([]byte(scenario.config))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			setNacosMetadataEnv(t, server.URL)
			dsn, err := loadMetadataDSN(context.Background())
			if dsn != "" || err == nil || err.Error() != scenario.want || strings.Contains(err.Error(), "private-marker") {
				t.Fatal("unexpected startup result or unsanitized message")
			}
		})
	}
}

func TestNacosLoginDoesNotFollowRedirect(t *testing.T) {
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Nacos credentials were sent to another endpoint")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer redirect.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirect.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	setNacosMetadataEnv(t, server.URL)
	if dsn, err := loadMetadataDSN(context.Background()); dsn != "" || err == nil {
		t.Fatal("Nacos login accepted a redirect")
	}
}

func TestMetadataSourceDoesNotSilentlyFallBack(t *testing.T) {
	t.Setenv("MCPGUARD_METADATA_DSN", "existing-local-dsn")
	t.Setenv("ZHIQI_METADATA_SOURCE", "")
	if dsn, err := loadMetadataDSN(context.Background()); dsn != "existing-local-dsn" || err != nil {
		t.Fatal("local metadata configuration changed")
	}
	for _, source := range []string{"nacos", "invalid"} {
		t.Setenv("ZHIQI_METADATA_SOURCE", source)
		if dsn, err := loadMetadataDSN(context.Background()); dsn != "" || err == nil {
			t.Fatal("explicit metadata source fell back to the local DSN")
		}
	}
}
