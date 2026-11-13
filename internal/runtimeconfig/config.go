// Package runtimeconfig configures the first, deliberately local-only read slice.
// The former proxy configuration is not loaded by the new gateway.
package runtimeconfig

import (
	"encoding/base64"
	"errors"
	"net"
	"os"
	"strconv"
)

type Config struct {
	Listen, MetadataDSN, BusinessDSN                                  string
	EncryptionKey, PlanKey                                            []byte
	AuthMode, OAuthFixtureConfig                                      string
	NativeConfig, ConsoleConfig, NotificationConfig                   string
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
}

func Load() (Config, error) {
	c := Config{Listen: os.Getenv("MCPGUARD_LISTEN"), MetadataDSN: os.Getenv("MCPGUARD_METADATA_DSN"), BusinessDSN: os.Getenv("MCPGUARD_BUSINESS_DSN")}
	c.NativeConfig = os.Getenv("MCPGUARD_NATIVE_CONFIG")
	c.ConsoleConfig = os.Getenv("MCPGUARD_CONSOLE_CONFIG")
	c.NotificationConfig = os.Getenv("MCPGUARD_NOTIFICATION_CONFIG")
	if value := os.Getenv("MCPGUARD_RESOURCE_CATALOG"); value != "" {
		if value != "enabled" {
			return c, errors.New("MCPGUARD_RESOURCE_CATALOG must be empty or enabled")
		}
		c.ResourceCatalog = true
	}
	if value := os.Getenv("MCPGUARD_RESOURCE_ACCESS"); value != "" {
		if value != "enabled" || !c.ResourceCatalog {
			return c, errors.New("MCPGUARD_RESOURCE_ACCESS=enabled requires the resource catalog")
		}
		c.ResourceAccess = true
	}
	if value := os.Getenv("MCPGUARD_RESOURCE_CREDENTIALS"); value != "" {
		if value != "enabled" || !c.ResourceAccess {
			return c, errors.New("MCPGUARD_RESOURCE_CREDENTIALS=enabled requires the resource access workflow")
		}
		c.CredentialKeys = os.Getenv("MCPGUARD_CREDENTIAL_KEYS")
		if c.CredentialKeys == "" {
			return c, errors.New("explicit private credential keys are required")
		}
		c.ResourceCredentials = true
	}
	if value := os.Getenv("MCPGUARD_RESOURCE_DOWNLOADS"); value != "" {
		if value != "enabled" || !c.ResourceCredentials {
			return c, errors.New("MCPGUARD_RESOURCE_DOWNLOADS=enabled requires credentials")
		}
		c.DeliveryDSN = os.Getenv("MCPGUARD_DELIVERY_DSN")
		var err error
		c.DownloadTTL, err = strconv.Atoi(os.Getenv("MCPGUARD_DOWNLOAD_TTL_SECONDS"))
		if err != nil || c.DownloadTTL < 1 || c.DownloadTTL > 300 {
			return c, errors.New("explicit download TTL from 1 to 300 seconds required")
		}
		c.DownloadMaxReady, err = strconv.Atoi(os.Getenv("MCPGUARD_DOWNLOAD_MAX_READY"))
		if err != nil || c.DownloadMaxReady < 1 || c.DownloadMaxReady > 50 || c.DeliveryDSN == "" {
			return c, errors.New("explicit delivery runtime and ready-ticket limit from 1 to 50 required")
		}
		c.ResourceDownloads = true
	}
	if value := os.Getenv("MCPGUARD_RESOURCE_CONTEXTS"); value != "" {
		if value != "enabled" || !c.ResourceDownloads {
			return c, errors.New("MCPGUARD_RESOURCE_CONTEXTS=enabled requires private downloads")
		}
		c.ContextDSN = os.Getenv("MCPGUARD_CONTEXT_DSN")
		if c.ContextDSN == "" {
			return c, errors.New("explicit context runtime required")
		}
		var err error
		c.ContextTTL, err = strconv.Atoi(os.Getenv("MCPGUARD_CONTEXT_TTL_SECONDS"))
		if err != nil || c.ContextTTL < 1 || c.ContextTTL > 900 {
			return c, errors.New("explicit context TTL from 1 to 900 seconds required")
		}
		c.ContextLifetime, err = strconv.Atoi(os.Getenv("MCPGUARD_CONTEXT_LIFETIME_SECONDS"))
		if err != nil || c.ContextLifetime < c.ContextTTL || c.ContextLifetime > 3600 {
			return c, errors.New("bounded absolute context lifetime required")
		}
		c.ContextMaxActivations, err = strconv.Atoi(os.Getenv("MCPGUARD_CONTEXT_MAX_ACTIVATIONS"))
		if err != nil || c.ContextMaxActivations < 1 || c.ContextMaxActivations > 100 {
			return c, errors.New("explicit activation limit from 1 to 100 required")
		}
		c.SyncPageLimit, err = strconv.Atoi(os.Getenv("MCPGUARD_SYNC_PAGE_LIMIT"))
		if err != nil || c.SyncPageLimit < 1 || c.SyncPageLimit > 20 {
			return c, errors.New("explicit sync page limit from 1 to 20 required")
		}
		c.ResourceContexts = true
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
	if c.MetadataDSN == "" || c.BusinessDSN == "" {
		return c, errors.New("both MCPGUARD_METADATA_DSN and MCPGUARD_BUSINESS_DSN are required")
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
