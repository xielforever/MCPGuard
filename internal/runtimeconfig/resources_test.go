package runtimeconfig

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestResourceCatalogRequiresExplicitOptIn(t *testing.T) {
	t.Setenv("MCPGUARD_AUTH_MODE", "dev-tokens")
	t.Setenv("MCPGUARD_LISTEN", "127.0.0.1:8080")
	t.Setenv("MCPGUARD_METADATA_DSN", "configured")
	t.Setenv("MCPGUARD_BUSINESS_DSN", "configured")
	t.Setenv("MCPGUARD_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	t.Setenv("MCPGUARD_PLAN_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)))
	t.Setenv("MCPGUARD_RESOURCE_CATALOG", "")
	if cfg, err := Load(); err != nil || cfg.ResourceCatalog {
		t.Fatal("legacy startup unexpectedly enabled the resource catalog")
	}
	t.Setenv("MCPGUARD_RESOURCE_ACCESS", "enabled")
	if _, err := Load(); err == nil {
		t.Fatal("access workflow enabled without catalog")
	}
	t.Setenv("MCPGUARD_RESOURCE_ACCESS", "")
	t.Setenv("MCPGUARD_RESOURCE_CATALOG", "enabled")
	if cfg, err := Load(); err != nil || !cfg.ResourceCatalog {
		t.Fatal("explicit catalog opt-in rejected")
	}
	t.Setenv("MCPGUARD_RESOURCE_ACCESS", "enabled")
	if cfg, err := Load(); err != nil || !cfg.ResourceAccess {
		t.Fatal("explicit access workflow rejected")
	}
	t.Setenv("MCPGUARD_RESOURCE_ACCESS", "enable")
	if _, err := Load(); err == nil {
		t.Fatal("mistyped access workflow accepted")
	}
	t.Setenv("MCPGUARD_RESOURCE_ACCESS", "")
	t.Setenv("MCPGUARD_RESOURCE_CATALOG", "enable")
	if _, err := Load(); err == nil {
		t.Fatal("mistyped catalog policy silently accepted")
	}
}
