package runtimeconfig

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestContextRequiresExplicitBoundedPolicy(t *testing.T) {
	for k, v := range map[string]string{"MCPGUARD_AUTH_MODE": "dev-tokens", "MCPGUARD_LISTEN": "127.0.0.1:8080", "MCPGUARD_METADATA_DSN": "configured", "MCPGUARD_BUSINESS_DSN": "configured", "MCPGUARD_ENCRYPTION_KEY": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), "MCPGUARD_PLAN_KEY": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)), "MCPGUARD_RESOURCE_CATALOG": "enabled", "MCPGUARD_RESOURCE_ACCESS": "enabled", "MCPGUARD_RESOURCE_CREDENTIALS": "enabled", "MCPGUARD_CREDENTIAL_KEYS": "private-server-file", "MCPGUARD_RESOURCE_DOWNLOADS": "enabled", "MCPGUARD_DELIVERY_DSN": "private-download", "MCPGUARD_DOWNLOAD_TTL_SECONDS": "60", "MCPGUARD_DOWNLOAD_MAX_READY": "5", "MCPGUARD_RESOURCE_CONTEXTS": "enabled", "MCPGUARD_CONTEXT_DSN": "", "MCPGUARD_CONTEXT_TTL_SECONDS": "", "MCPGUARD_CONTEXT_LIFETIME_SECONDS": "", "MCPGUARD_CONTEXT_MAX_ACTIVATIONS": "", "MCPGUARD_SYNC_PAGE_LIMIT": ""} {
		t.Setenv(k, v)
	}
	if _, e := Load(); e == nil {
		t.Fatal("Context without explicit policy")
	}
	for k, v := range map[string]string{"MCPGUARD_CONTEXT_DSN": "private-context", "MCPGUARD_CONTEXT_TTL_SECONDS": "60", "MCPGUARD_CONTEXT_LIFETIME_SECONDS": "600", "MCPGUARD_CONTEXT_MAX_ACTIVATIONS": "10", "MCPGUARD_SYNC_PAGE_LIMIT": "2"} {
		t.Setenv(k, v)
	}
	if cfg, e := Load(); e != nil || !cfg.ResourceContexts {
		t.Fatal("explicit Context policy")
	}
	for _, item := range [][2]string{{"MCPGUARD_CONTEXT_TTL_SECONDS", "901"}, {"MCPGUARD_CONTEXT_LIFETIME_SECONDS", "3601"}, {"MCPGUARD_CONTEXT_MAX_ACTIVATIONS", "101"}, {"MCPGUARD_SYNC_PAGE_LIMIT", "21"}} {
		prior := map[string]string{"MCPGUARD_CONTEXT_TTL_SECONDS": "60", "MCPGUARD_CONTEXT_LIFETIME_SECONDS": "600", "MCPGUARD_CONTEXT_MAX_ACTIVATIONS": "10", "MCPGUARD_SYNC_PAGE_LIMIT": "2"}[item[0]]
		t.Setenv(item[0], item[1])
		if _, e := Load(); e == nil {
			t.Fatal("unbounded Context policy ", item[0])
		}
		t.Setenv(item[0], prior)
	}
	t.Setenv("MCPGUARD_CONTEXT_LIFETIME_SECONDS", "30")
	if _, e := Load(); e == nil {
		t.Fatal("absolute lifetime shorter than TTL")
	}
	t.Setenv("MCPGUARD_RESOURCE_CONTEXTS", "")
	if cfg, e := Load(); e != nil || cfg.ResourceContexts {
		t.Fatal("legacy mode unexpectedly enabled Contexts")
	}
}
