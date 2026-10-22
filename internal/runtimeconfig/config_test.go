package runtimeconfig

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestLocalOnlyAndFailClosed(t *testing.T) {
	t.Setenv("MCPGUARD_AUTH_MODE", "dev-tokens")
	t.Setenv("MCPGUARD_METADATA_DSN", "configured")
	t.Setenv("MCPGUARD_BUSINESS_DSN", "configured")
	t.Setenv("MCPGUARD_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	t.Setenv("MCPGUARD_PLAN_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)))
	for _, addr := range []string{"0.0.0.0:8080", ":8080", "localhost:8080", "192.0.2.1:8080", "malformed"} {
		t.Setenv("MCPGUARD_LISTEN", addr)
		if _, e := Load(); e == nil {
			t.Fatal("test identities exposed on", addr)
		}
	}
	t.Setenv("MCPGUARD_LISTEN", "127.0.0.1:8080")
	if _, e := Load(); e != nil {
		t.Fatal(e)
	}
	t.Setenv("MCPGUARD_AUTH_MODE", "")
	if _, e := Load(); e == nil {
		t.Fatal("silently enabled test identity mode")
	}
}
