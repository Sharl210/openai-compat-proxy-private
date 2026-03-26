package config

import (
	"fmt"
	"os"
	"path/filepath"
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
	UpstreamRetryCount   int
	UpstreamRetryDelay   time.Duration
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
	return loadFromLookup(os.Getenv)
}

func LoadFromValues(values map[string]string) Config {
	return loadFromLookup(func(key string) string {
		return values[key]
	})
}

func loadFromLookup(lookup func(string) string) Config {
	cfg := Default()
	if value := lookup("LISTEN_ADDR"); value != "" {
		cfg.ListenAddr = value
	}
	if value := lookup("PROXY_API_KEY"); value != "" {
		cfg.ProxyAPIKey = value
	}
	if value := lookup("PROVIDERS_DIR"); value != "" {
		cfg.ProvidersDir = value
	}
	if value := lookup("DEFAULT_PROVIDER"); value != "" {
		cfg.DefaultProvider = value
	}
	if value := lookup("ENABLE_LEGACY_V1_ROUTES"); value != "" {
		cfg.EnableLegacyV1Routes = strings.EqualFold(value, "true") || value == "1"
	}
	if value := lookup("LOG_ENABLE"); value != "" {
		cfg.LogEnable = strings.EqualFold(value, "true") || value == "1"
	}
	if value := lookup("LOG_FILE_PATH"); value != "" {
		cfg.LogFilePath = value
	}
	if value := lookup("LOG_INCLUDE_BODIES"); value != "" {
		cfg.LogIncludeBodies = strings.EqualFold(value, "true") || value == "1"
	}
	if value := lookup("LOG_MAX_SIZE_MB"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			cfg.LogMaxSizeMB = parsed
		}
	}
	if value := lookup("LOG_MAX_BACKUPS"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed >= 0 {
			cfg.LogMaxBackups = parsed
		}
	}
	if value := lookup("CONNECT_TIMEOUT"); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			cfg.ConnectTimeout = parsed
		}
	}
	if value := lookup("FIRST_BYTE_TIMEOUT"); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			cfg.FirstByteTimeout = parsed
		}
	}
	if value := lookup("IDLE_TIMEOUT"); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			cfg.IdleTimeout = parsed
		}
	}
	if value := lookup("TOTAL_TIMEOUT"); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			cfg.TotalTimeout = parsed
		}
	}
	return cfg
}

func ValidateRootEnvValues(values map[string]string) error {
	if err := validatePositiveDuration(values, "CONNECT_TIMEOUT"); err != nil {
		return err
	}
	if err := validatePositiveDuration(values, "FIRST_BYTE_TIMEOUT"); err != nil {
		return err
	}
	if err := validatePositiveDuration(values, "IDLE_TIMEOUT"); err != nil {
		return err
	}
	if err := validatePositiveDuration(values, "TOTAL_TIMEOUT"); err != nil {
		return err
	}
	if err := validateMinInt(values, "LOG_MAX_SIZE_MB", 1); err != nil {
		return err
	}
	if err := validateMinInt(values, "LOG_MAX_BACKUPS", 0); err != nil {
		return err
	}
	return nil
}

func validatePositiveDuration(values map[string]string, key string) error {
	value := strings.TrimSpace(values[key])
	if value == "" {
		return nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return ErrInvalidConfig(fmt.Sprintf("invalid %s: %q", key, value))
	}
	return nil
}

func validateMinInt(values map[string]string, key string, min int) error {
	value := strings.TrimSpace(values[key])
	if value == "" {
		return nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < min {
		return ErrInvalidConfig(fmt.Sprintf("invalid %s: %q", key, value))
	}
	return nil
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.ProvidersDir) == "" {
		return ErrInvalidConfig("providers dir is required")
	}
	if len(c.Providers) == 0 {
		return ErrInvalidConfig("at least one provider must be configured")
	}
	if strings.TrimSpace(c.DefaultProvider) != "" {
		provider, err := c.DefaultProviderConfig()
		if err != nil {
			return ErrInvalidConfig("default provider not found")
		}
		if !provider.Enabled {
			return ErrInvalidConfig("default provider must be enabled")
		}
	}
	if c.EnableLegacyV1Routes && strings.TrimSpace(c.DefaultProvider) == "" {
		return ErrInvalidConfig("default provider is required when legacy v1 routes are enabled")
	}
	if c.EnableLegacyV1Routes && len(c.Providers) > 0 && strings.TrimSpace(c.DefaultProvider) == "" {
		return ErrInvalidConfig("legacy v1 routes require a default provider")
	}
	return nil
}

func ResolveProvidersDir(rootEnvPath string, providersDir string) string {
	if providersDir == "" || filepath.IsAbs(providersDir) {
		return providersDir
	}
	return filepath.Join(filepath.Dir(rootEnvPath), providersDir)
}

func (c *Config) applyStartupOnlyFrom(previous Config) {
	c.ListenAddr = previous.ListenAddr
	c.LogEnable = previous.LogEnable
	c.LogFilePath = previous.LogFilePath
	c.LogIncludeBodies = previous.LogIncludeBodies
	c.LogMaxSizeMB = previous.LogMaxSizeMB
	c.LogMaxBackups = previous.LogMaxBackups
}

func (c Config) hotReloadableRootEquals(other Config) bool {
	return c.ProxyAPIKey == other.ProxyAPIKey &&
		c.ProvidersDir == other.ProvidersDir &&
		c.DefaultProvider == other.DefaultProvider &&
		c.EnableLegacyV1Routes == other.EnableLegacyV1Routes &&
		c.ConnectTimeout == other.ConnectTimeout &&
		c.FirstByteTimeout == other.FirstByteTimeout &&
		c.IdleTimeout == other.IdleTimeout &&
		c.TotalTimeout == other.TotalTimeout
}
