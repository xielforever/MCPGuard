package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Proxy    ProxyConfig    `yaml:"proxy"`
	Auth     AuthConfig     `yaml:"auth"`
	Notify   NotifyConfig   `yaml:"notify"`
	Rules    []RuleConfig   `yaml:"rules"`
}

type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type DatabaseConfig struct {
	Driver string `yaml:"driver"`
	DSN    string `yaml:"dsn"`
}

type ProxyConfig struct {
	Mode           string   `yaml:"mode"`
	BackendCommand string   `yaml:"backend_command"`
	BackendArgs    []string `yaml:"backend_args"`
}

type AuthConfig struct {
	CodeLength  int           `yaml:"code_length"`
	CodeExpiry  time.Duration `yaml:"code_expiry"`
	MaxAttempts int           `yaml:"max_attempts"`
}

type NotifyConfig struct {
	Provider   string            `yaml:"provider"`
	WebhookURL string            `yaml:"webhook_url"`
	APIKeys    map[string]string `yaml:"api_keys"`
}

type RuleConfig struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Pattern     string `yaml:"pattern"`
	ToolName    string `yaml:"tool_name"`
	Action      string `yaml:"action"`
	Enabled     bool   `yaml:"enabled"`
	Priority    int    `yaml:"priority"`
}

func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Host: "0.0.0.0",
			Port: 8080,
		},
		Database: DatabaseConfig{
			Driver: "sqlite3",
			DSN:    "mcpguard.db",
		},
		Proxy: ProxyConfig{
			Mode: "stdio",
		},
		Auth: AuthConfig{
			CodeLength:  6,
			CodeExpiry:  5 * time.Minute,
			MaxAttempts: 3,
		},
		Notify: NotifyConfig{
			Provider: "none",
		},
	}
}

func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil // Return defaults if not found
		}
		return nil, err
	}

	err = yaml.Unmarshal(data, cfg)
	if err != nil {
		return nil, err
	}

	return cfg, nil
}
