package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr           string
	ProxyAPIKey          string
	UpstreamBaseURL      string
	UpstreamAPIKey       string
	ProvidersDir         string
	DefaultProvider      string
	EnableLegacyV1Routes bool
	Providers            []ProviderConfig
	LogEnable            bool
	ConnectTimeout       time.Duration
	FirstByteTimeout     time.Duration
	IdleTimeout          time.Duration
	TotalTimeout         time.Duration
	LogFilePath          string
	LogIncludeBodies     bool
	LogMaxSizeMB         int
	LogMaxBackups        int
}

func Default() Config {
	return Config{
		ListenAddr:       ":21021",
		LogEnable:        false,
		ConnectTimeout:   10 * time.Second,
		FirstByteTimeout: 90 * time.Second,
		IdleTimeout:      3 * time.Minute,
		TotalTimeout:     time.Hour,
		LogFilePath:      ".proxy.requests.jsonl",
		LogMaxSizeMB:     100,
		LogMaxBackups:    10,
	}
}

func LoadFromEnv() Config {
	cfg := Default()
	if value := os.Getenv("LISTEN_ADDR"); value != "" {
		cfg.ListenAddr = value
	}
	if value := os.Getenv("PROXY_API_KEY"); value != "" {
		cfg.ProxyAPIKey = value
	}
	if value := os.Getenv("PROVIDERS_DIR"); value != "" {
		cfg.ProvidersDir = value
	}
	if value := os.Getenv("DEFAULT_PROVIDER"); value != "" {
		cfg.DefaultProvider = value
	}
	if value := os.Getenv("ENABLE_LEGACY_V1_ROUTES"); value != "" {
		cfg.EnableLegacyV1Routes = strings.EqualFold(value, "true") || value == "1"
	}
	if value := os.Getenv("LOG_ENABLE"); value != "" {
		cfg.LogEnable = strings.EqualFold(value, "true") || value == "1"
	}
	if value := os.Getenv("LOG_FILE_PATH"); value != "" {
		cfg.LogFilePath = value
	}
	if value := os.Getenv("LOG_INCLUDE_BODIES"); value != "" {
		cfg.LogIncludeBodies = strings.EqualFold(value, "true") || value == "1"
	}
	if value := os.Getenv("LOG_MAX_SIZE_MB"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			cfg.LogMaxSizeMB = parsed
		}
	}
	if value := os.Getenv("LOG_MAX_BACKUPS"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed >= 0 {
			cfg.LogMaxBackups = parsed
		}
	}
	if value := os.Getenv("CONNECT_TIMEOUT"); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			cfg.ConnectTimeout = parsed
		}
	}
	if value := os.Getenv("FIRST_BYTE_TIMEOUT"); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			cfg.FirstByteTimeout = parsed
		}
	}
	if value := os.Getenv("IDLE_TIMEOUT"); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			cfg.IdleTimeout = parsed
		}
	}
	if value := os.Getenv("TOTAL_TIMEOUT"); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			cfg.TotalTimeout = parsed
		}
	}
	return cfg
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.ProvidersDir) == "" {
		return ErrInvalidConfig("providers dir is required")
	}
	if len(c.Providers) == 0 {
		return ErrInvalidConfig("at least one provider must be configured")
	}
	if c.EnableLegacyV1Routes && strings.TrimSpace(c.DefaultProvider) == "" {
		return ErrInvalidConfig("default provider is required when legacy v1 routes are enabled")
	}
	if c.EnableLegacyV1Routes && len(c.Providers) > 0 && strings.TrimSpace(c.DefaultProvider) == "" {
		return ErrInvalidConfig("legacy v1 routes require a default provider")
	}
	return nil
}
