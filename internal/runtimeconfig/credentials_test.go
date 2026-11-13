package runtimeconfig

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestCredentialsRequireAccessAndPrivateKeys(t *testing.T) {
	for k, v := range map[string]string{"MCPGUARD_AUTH_MODE": "dev-tokens", "MCPGUARD_LISTEN": "127.0.0.1:8080", "MCPGUARD_METADATA_DSN": "configured", "MCPGUARD_BUSINESS_DSN": "configured", "MCPGUARD_ENCRYPTION_KEY": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), "MCPGUARD_PLAN_KEY": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)), "MCPGUARD_RESOURCE_CATALOG": "enabled", "MCPGUARD_RESOURCE_ACCESS": "", "MCPGUARD_RESOURCE_CREDENTIALS": "enabled", "MCPGUARD_CREDENTIAL_KEYS": ""} {
		t.Setenv(k, v)
	}
	if _, e := Load(); e == nil {
		t.Fatal("credentials enabled without access workflow")
	}
	t.Setenv("MCPGUARD_RESOURCE_ACCESS", "enabled")
	if _, e := Load(); e == nil {
		t.Fatal("credentials enabled without private keys")
	}
	t.Setenv("MCPGUARD_CREDENTIAL_KEYS", "private-server-file")
	if c, e := Load(); e != nil || !c.ResourceCredentials {
		t.Fatal("explicit credentials configuration failed")
	}
	t.Setenv("MCPGUARD_RESOURCE_CREDENTIALS", "enable")
	if _, e := Load(); e == nil {
		t.Fatal("mistyped credential opt-in accepted")
	}
	t.Setenv("MCPGUARD_RESOURCE_CREDENTIALS", "")
	if c, e := Load(); e != nil || c.ResourceCredentials {
		t.Fatal("legacy mode unexpectedly enabled credentials")
	}
}
