// Package runtimeconfig configures the first, deliberately local-only read slice.
// The former proxy configuration is not loaded by the new gateway.
package runtimeconfig

import (
	"encoding/base64"
	"errors"
	"net"
	"os"
)

type Config struct {
	Listen, MetadataDSN, BusinessDSN string
	EncryptionKey, PlanKey           []byte
}

func Load() (Config, error) {
	c := Config{Listen: os.Getenv("MCPGUARD_LISTEN"), MetadataDSN: os.Getenv("MCPGUARD_METADATA_DSN"), BusinessDSN: os.Getenv("MCPGUARD_BUSINESS_DSN")}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return c, errors.New("test identity mode requires an explicit loopback listen address")
	}
	if os.Getenv("MCPGUARD_AUTH_MODE") != "dev-tokens" {
		return c, errors.New("set MCPGUARD_AUTH_MODE=dev-tokens for this local test slice; enterprise OAuth is not implemented")
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
