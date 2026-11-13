package runtimeconfig

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestDownloadsRequireExplicitPrivatePolicy(t *testing.T) {
	for k, v := range map[string]string{"MCPGUARD_AUTH_MODE": "dev-tokens", "MCPGUARD_LISTEN": "127.0.0.1:8080", "MCPGUARD_METADATA_DSN": "configured", "MCPGUARD_BUSINESS_DSN": "configured", "MCPGUARD_ENCRYPTION_KEY": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), "MCPGUARD_PLAN_KEY": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)), "MCPGUARD_RESOURCE_CATALOG": "enabled", "MCPGUARD_RESOURCE_ACCESS": "enabled", "MCPGUARD_RESOURCE_CREDENTIALS": "", "MCPGUARD_CREDENTIAL_KEYS": "private-server-file", "MCPGUARD_RESOURCE_DOWNLOADS": "enabled", "MCPGUARD_DELIVERY_DSN": "", "MCPGUARD_DOWNLOAD_TTL_SECONDS": "", "MCPGUARD_DOWNLOAD_MAX_READY": ""} {
		t.Setenv(k, v)
	}
	if _, e := Load(); e == nil {
		t.Fatal("downloads enabled without credentials")
	}
	t.Setenv("MCPGUARD_RESOURCE_CREDENTIALS", "enabled")
	if _, e := Load(); e == nil {
		t.Fatal("missing private policy accepted")
	}
	t.Setenv("MCPGUARD_DELIVERY_DSN", "private-runtime")
	t.Setenv("MCPGUARD_DOWNLOAD_TTL_SECONDS", "60")
	t.Setenv("MCPGUARD_DOWNLOAD_MAX_READY", "5")
	if cfg, e := Load(); e != nil || !cfg.ResourceDownloads {
		t.Fatal("explicit delivery configuration failed")
	}
	for _, value := range []string{"0", "301", "invalid"} {
		t.Setenv("MCPGUARD_DOWNLOAD_TTL_SECONDS", value)
		if _, e := Load(); e == nil {
			t.Fatal("invalid ticket TTL accepted")
		}
	}
	t.Setenv("MCPGUARD_DOWNLOAD_TTL_SECONDS", "60")
	t.Setenv("MCPGUARD_DOWNLOAD_MAX_READY", "51")
	if _, e := Load(); e == nil {
		t.Fatal("unbounded pending tickets accepted")
	}
	t.Setenv("MCPGUARD_RESOURCE_DOWNLOADS", "")
	if cfg, e := Load(); e != nil || cfg.ResourceDownloads {
		t.Fatal("legacy mode enabled private delivery")
	}
}
