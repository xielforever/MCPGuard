// Package runtimeconfig configures the first, deliberately local-only read slice.
// The former proxy configuration is not loaded by the new gateway.
package runtimeconfig

import (
	"encoding/base64"
	"errors"
	"net"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Listen, MetadataDSN, BusinessDSN                                  string
	EncryptionKey, PlanKey                                            []byte
	AuthMode, OAuthFixtureConfig                                      string
	NativeConfig, ConsoleConfig, NotificationConfig                   string
	LegacyDemo                                                        bool
	ResourceCatalog                                                   bool
	ResourceAccess                                                    bool
	ResourceCredentials                                               bool
	CredentialKeys                                                    string
	ResourceDownloads                                                 bool
	DeliveryDSN                                                       string
	DownloadTTL, DownloadMaxReady                                     int
	ResourceContexts                                                  bool
	ContextDSN                                                        string
	ContextTTL, ContextLifetime, ContextMaxActivations, SyncPageLimit int
	ResourceReads                                                     bool
	ResourceWrites                                                    bool
	NativeSecrets                                                     string
	HighRiskWrites                                                    bool
	HighRiskResourceID, HighRiskReviewerID, HighRiskChannel           string
	HighRiskWeComURL, HighRiskFeishuURL                               string
	HighRiskCodeTTL                                                   time.Duration
}

func Load() (Config, error) {
	c := Config{Listen: os.Getenv("MCPGUARD_LISTEN"), MetadataDSN: os.Getenv("MCPGUARD_METADATA_DSN"), BusinessDSN: os.Getenv("MCPGUARD_BUSINESS_DSN")}
	switch os.Getenv("MCPGUARD_LEGACY_DEMO") {
	case "", "enabled":
		c.LegacyDemo = true
		c.NativeConfig = os.Getenv("MCPGUARD_NATIVE_CONFIG")
	case "disabled":
	default:
		return c, errors.New("MCPGUARD_LEGACY_DEMO must be empty, enabled or disabled")
	}
	c.ConsoleConfig = os.Getenv("MCPGUARD_CONSOLE_CONFIG")
	c.NotificationConfig = os.Getenv("MCPGUARD_NOTIFICATION_CONFIG")
	if os.Getenv("MCPGUARD_RESOURCE_CATALOG") != "disabled" {
		if err := c.loadResources(); err != nil {
			return c, err
		}
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return c, errors.New("test identity mode requires an explicit loopback listen address")
	}
	c.AuthMode = os.Getenv("MCPGUARD_AUTH_MODE")
	switch c.AuthMode {
	case "dev-tokens":
	case "oauth-fixture":
		c.OAuthFixtureConfig = os.Getenv("MCPGUARD_OAUTH_FIXTURE_CONFIG")
		if c.OAuthFixtureConfig == "" {
			return c, errors.New("oauth-fixture requires MCPGUARD_OAUTH_FIXTURE_CONFIG")
		}
	default:
		return c, errors.New("explicit MCPGUARD_AUTH_MODE=dev-tokens or oauth-fixture is required; enterprise SSO is not implemented")
	}
	if value := os.Getenv("MCPGUARD_HIGH_RISK_WRITES"); value != "" && value != "disabled" {
		if value != "enabled" || !c.ResourceWrites || c.ConsoleConfig == "" || c.AuthMode != "dev-tokens" {
			return c, errors.New("MCPGUARD_HIGH_RISK_WRITES=enabled requires local dev tokens, resource writes and the browser console")
		}
		c.HighRiskWrites = true
		c.HighRiskResourceID = os.Getenv("MCPGUARD_HIGH_RISK_RESOURCE_ID")
		c.HighRiskReviewerID = os.Getenv("MCPGUARD_HIGH_RISK_REVIEWER_ID")
		c.HighRiskChannel = os.Getenv("MCPGUARD_HIGH_RISK_CHANNEL")
		c.HighRiskWeComURL = os.Getenv("MCPGUARD_HIGH_RISK_WECOM_URL")
		c.HighRiskFeishuURL = os.Getenv("MCPGUARD_HIGH_RISK_FEISHU_URL")
		c.HighRiskCodeTTL = 5 * time.Minute
		if value := os.Getenv("MCPGUARD_HIGH_RISK_CODE_TTL"); value != "" {
			c.HighRiskCodeTTL, err = time.ParseDuration(value)
			if err != nil || c.HighRiskCodeTTL < time.Second || c.HighRiskCodeTTL > 15*time.Minute {
				return c, errors.New("MCPGUARD_HIGH_RISK_CODE_TTL must be between 1s and 15m")
			}
		}
	}
	if c.MetadataDSN == "" || c.LegacyDemo && c.BusinessDSN == "" {
		return c, errors.New("metadata DSN and, when legacy demo is enabled, business DSN are required")
	}
	c.EncryptionKey, err = base64.StdEncoding.DecodeString(os.Getenv("MCPGUARD_ENCRYPTION_KEY"))
	if err != nil || len(c.EncryptionKey) != 32 {
		return c, errors.New("MCPGUARD_ENCRYPTION_KEY must be base64 of 32 random bytes")
	}
	c.PlanKey, err = base64.StdEncoding.DecodeString(os.Getenv("MCPGUARD_PLAN_KEY"))
	if err != nil || len(c.PlanKey) != 32 {
		return c, errors.New("MCPGUARD_PLAN_KEY must be base64 of 32 random bytes")
	}
	if string(c.PlanKey) == string(c.EncryptionKey) {
		return c, errors.New("encryption and plan keys must differ")
	}
	return c, nil
}

func (c *Config) loadResources() error {
	switch os.Getenv("MCPGUARD_RESOURCE_CATALOG") {
	case "":
	case "enabled":
		c.ResourceCatalog = true
	default:
		return errors.New("MCPGUARD_RESOURCE_CATALOG must be empty, enabled or disabled")
	}
	if value := os.Getenv("MCPGUARD_RESOURCE_ACCESS"); value != "" {
		if value != "enabled" || !c.ResourceCatalog {
			return errors.New("MCPGUARD_RESOURCE_ACCESS=enabled requires the resource catalog")
		}
		c.ResourceAccess = true
	}
	if value := os.Getenv("MCPGUARD_RESOURCE_CREDENTIALS"); value != "" {
		if value != "enabled" || !c.ResourceAccess {
			return errors.New("MCPGUARD_RESOURCE_CREDENTIALS=enabled requires the resource access workflow")
		}
		c.CredentialKeys = os.Getenv("MCPGUARD_CREDENTIAL_KEYS")
		if c.CredentialKeys == "" {
			return errors.New("explicit private credential keys are required")
		}
		c.ResourceCredentials = true
	}
	if value := os.Getenv("MCPGUARD_RESOURCE_DOWNLOADS"); value != "" {
		if value != "enabled" || !c.ResourceCredentials {
			return errors.New("MCPGUARD_RESOURCE_DOWNLOADS=enabled requires credentials")
		}
		c.DeliveryDSN = os.Getenv("MCPGUARD_DELIVERY_DSN")
		var err error
		c.DownloadTTL, err = strconv.Atoi(os.Getenv("MCPGUARD_DOWNLOAD_TTL_SECONDS"))
		if err != nil || c.DownloadTTL < 1 || c.DownloadTTL > 300 {
			return errors.New("explicit download TTL from 1 to 300 seconds required")
		}
		c.DownloadMaxReady, err = strconv.Atoi(os.Getenv("MCPGUARD_DOWNLOAD_MAX_READY"))
		if err != nil || c.DownloadMaxReady < 1 || c.DownloadMaxReady > 50 || c.DeliveryDSN == "" {
			return errors.New("explicit delivery runtime and ready-ticket limit from 1 to 50 required")
		}
		c.ResourceDownloads = true
	}
	if value := os.Getenv("MCPGUARD_RESOURCE_CONTEXTS"); value != "" {
		if value != "enabled" || !c.ResourceDownloads {
			return errors.New("MCPGUARD_RESOURCE_CONTEXTS=enabled requires private downloads")
		}
		c.ContextDSN = os.Getenv("MCPGUARD_CONTEXT_DSN")
		if c.ContextDSN == "" {
			return errors.New("explicit context runtime required")
		}
		var err error
		c.ContextTTL, err = strconv.Atoi(os.Getenv("MCPGUARD_CONTEXT_TTL_SECONDS"))
		if err != nil || c.ContextTTL < 1 || c.ContextTTL > 900 {
			return errors.New("explicit context TTL from 1 to 900 seconds required")
		}
		c.ContextLifetime, err = strconv.Atoi(os.Getenv("MCPGUARD_CONTEXT_LIFETIME_SECONDS"))
		if err != nil || c.ContextLifetime < c.ContextTTL || c.ContextLifetime > 3600 {
			return errors.New("bounded absolute context lifetime required")
		}
		c.ContextMaxActivations, err = strconv.Atoi(os.Getenv("MCPGUARD_CONTEXT_MAX_ACTIVATIONS"))
		if err != nil || c.ContextMaxActivations < 1 || c.ContextMaxActivations > 100 {
			return errors.New("explicit activation limit from 1 to 100 required")
		}
		c.SyncPageLimit, err = strconv.Atoi(os.Getenv("MCPGUARD_SYNC_PAGE_LIMIT"))
		if err != nil || c.SyncPageLimit < 1 || c.SyncPageLimit > 20 {
			return errors.New("explicit sync page limit from 1 to 20 required")
		}
		c.ResourceContexts = true
	}
	if value := os.Getenv("MCPGUARD_RESOURCE_READS"); value != "" {
		if value != "enabled" || !c.ResourceContexts {
			return errors.New("MCPGUARD_RESOURCE_READS=enabled requires credential contexts")
		}
		c.NativeSecrets = os.Getenv("MCPGUARD_READ_SECRETS")
		if c.NativeSecrets == "" {
			return errors.New("explicit native secret provider required for planned reads")
		}
		c.ResourceReads = true
	}
	if value := os.Getenv("MCPGUARD_RESOURCE_WRITES"); value != "" {
		if value != "enabled" || !c.ResourceReads {
			return errors.New("MCPGUARD_RESOURCE_WRITES=enabled requires planned resource reads")
		}
		c.ResourceWrites = true
	}
	return nil
}
