package runtimeconfig

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestJenkinsOptInRequiresApprovalConsole(t *testing.T) {
	t.Setenv("MCPGUARD_AUTH_MODE", "dev-tokens")
	t.Setenv("MCPGUARD_LISTEN", "127.0.0.1:8080")
	t.Setenv("MCPGUARD_METADATA_DSN", "configured")
	t.Setenv("MCPGUARD_BUSINESS_DSN", "")
	t.Setenv("MCPGUARD_LEGACY_DEMO", "disabled")
	t.Setenv("MCPGUARD_RESOURCE_CATALOG", "disabled")
	t.Setenv("MCPGUARD_HIGH_RISK_WRITES", "disabled")
	t.Setenv("MCPGUARD_CONSOLE_CONFIG", "")
	t.Setenv("MCPGUARD_JENKINS_CONFIG", "")
	t.Setenv("MCPGUARD_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	t.Setenv("MCPGUARD_PLAN_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)))
	if cfg, err := Load(); err != nil || cfg.JenkinsConfig != "" {
		t.Fatalf("startup without Jenkins config failed or enabled Jenkins: %v", err)
	}

	const jenkinsConfig = "jenkins.private.json"
	t.Setenv("MCPGUARD_JENKINS_CONFIG", jenkinsConfig)
	if _, err := Load(); err == nil {
		t.Fatal("Jenkins accepted without the approval console")
	}

	t.Setenv("MCPGUARD_CONSOLE_CONFIG", "console.private.json")
	if cfg, err := Load(); err != nil || cfg.JenkinsConfig != jenkinsConfig || cfg.LegacyDemo {
		t.Fatalf("Jenkins-only startup rejected: %v", err)
	}
}
