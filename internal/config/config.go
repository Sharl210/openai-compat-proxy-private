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
	ListenAddr                     string
	CacheInfoTimezone              string
	ProxyAPIKey                    string
	UpstreamBaseURL                string
	UpstreamAPIKey                 string
	UpstreamEndpointType           string
	AnthropicVersion               string
	UpstreamUserAgent              string
	MasqueradeTarget               string
	InjectClaudeCodeMetadataUserID bool
	InjectClaudeCodeSystemPrompt   bool
	ProvidersDir                   string
	DefaultProvider                string
	EnableLegacyV1Routes           bool
	DownstreamNonStreamStrategy    string
	Providers                      []ProviderConfig
	LogEnable                      bool
	ConnectTimeout                 time.Duration
	FirstByteTimeout               time.Duration
	IdleTimeout                    time.Duration
	TotalTimeout                   time.Duration
	UpstreamRetryCount             int
	UpstreamRetryDelay             time.Duration
	UpstreamThinkingTagStyle       string
	LogFilePath                    string
	LogMaxRequests                 int
	LogMaxBodySizeMB               float64
	DebugArchiveRootDir            string
}

const (
	DownstreamNonStreamStrategyProxyBuffer       = "proxy_buffer"
	DownstreamNonStreamStrategyUpstreamNonStream = "upstream_non_stream"
)

func Default() Config {
	return Config{
		ListenAddr:                  ":21021",
		CacheInfoTimezone:           "Asia/Shanghai",
		LogEnable:                   true,
		ConnectTimeout:              30 * time.Second,
		FirstByteTimeout:            20 * time.Minute,
		IdleTimeout:                 3 * time.Minute,
		TotalTimeout:                time.Hour,
		UpstreamEndpointType:        UpstreamEndpointTypeResponses,
		UpstreamThinkingTagStyle:    UpstreamThinkingTagStyleOff,
		DownstreamNonStreamStrategy: DownstreamNonStreamStrategyProxyBuffer,
		LogFilePath:                 "logs",
		LogMaxRequests:              200,
		LogMaxBodySizeMB:            5.0,
		DebugArchiveRootDir:         "OPENAI_COMPAT_DEBUG_ARCHIVE_DIR",
	}
}

func LoadFromEnv() Config {
	return loadFromLookup(os.LookupEnv)
}

func LoadFromValues(values map[string]string) Config {
	return loadFromLookup(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
}

func loadFromLookup(lookup func(string) (string, bool)) Config {
	cfg := Default()
	if value, ok := lookup("LISTEN_ADDR"); ok && value != "" {
		cfg.ListenAddr = normalizeListenAddr(value)
	}
	if value, ok := lookup("CACHE_INFO_TIMEZONE"); ok && value != "" {
		cfg.CacheInfoTimezone = value
	}
	if value, ok := lookup("PROXY_API_KEY"); ok && value != "" {
		cfg.ProxyAPIKey = value
	}
	if value, ok := lookup("PROVIDERS_DIR"); ok && value != "" {
		cfg.ProvidersDir = value
	}
	if value, ok := lookup("DEFAULT_PROVIDER"); ok && value != "" {
		cfg.DefaultProvider = value
	}
	if value, ok := lookup("ENABLE_LEGACY_V1_ROUTES"); ok && value != "" {
		cfg.EnableLegacyV1Routes = parseRootBool(value)
	}
	if value, ok := lookup("DOWNSTREAM_NON_STREAM_STRATEGY"); ok && value != "" {
		if normalized, err := normalizeDownstreamNonStreamStrategy(value); err == nil {
			cfg.DownstreamNonStreamStrategy = normalized
		}
	}
	if value, ok := lookup("LOG_ENABLE"); ok && value != "" {
		cfg.LogEnable = parseRootBool(value)
	}
	if value, ok := lookup("LOG_FILE_PATH"); ok && value != "" {
		cfg.LogFilePath = value
	}
	if value, ok := lookup("LOG_MAX_REQUESTS"); ok && value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			cfg.LogMaxRequests = parsed
		}
	}
	if value, ok := lookup("LOG_MAX_BODY_SIZE_MB"); ok && value != "" {
		if parsed, err := strconv.ParseFloat(value, 64); err == nil && parsed >= 0 {
			cfg.LogMaxBodySizeMB = parsed
		}
	}
	if value, ok := lookup("OPENAI_COMPAT_DEBUG_ARCHIVE_DIR"); ok {
		cfg.DebugArchiveRootDir = value
	}
	if value, ok := lookup("CONNECT_TIMEOUT"); ok && value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			cfg.ConnectTimeout = parsed
		}
	}
	if value, ok := lookup("FIRST_BYTE_TIMEOUT"); ok && value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			cfg.FirstByteTimeout = parsed
		}
	}
	if value, ok := lookup("IDLE_TIMEOUT"); ok && value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			cfg.IdleTimeout = parsed
		}
	}
	if value, ok := lookup("TOTAL_TIMEOUT"); ok && value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			cfg.TotalTimeout = parsed
		}
	}
	if value, ok := lookup("UPSTREAM_USER_AGENT"); ok && value != "" {
		cfg.UpstreamUserAgent = value
	}
	if value, ok := lookup("UPSTREAM_MASQUERADE_TARGET"); ok && value != "" {
		cfg.MasqueradeTarget = strings.ToLower(value)
	}
	if value, ok := lookup("UPSTREAM_INJECT_METADATA_USER_ID"); ok {
		cfg.InjectClaudeCodeMetadataUserID = strings.ToLower(value) == "true"
	}
	if value, ok := lookup("UPSTREAM_INJECT_CLAUDE_SYSTEM_PROMPT"); ok {
		cfg.InjectClaudeCodeSystemPrompt = strings.ToLower(value) == "true"
	}
	return cfg
}

func ValidateRootEnvValues(values map[string]string) error {
	if err := validateTimezone(values, "CACHE_INFO_TIMEZONE"); err != nil {
		return err
	}
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
	if err := validateDownstreamNonStreamStrategy(values, "DOWNSTREAM_NON_STREAM_STRATEGY"); err != nil {
		return err
	}
	if err := validateStrictBool(values, "ENABLE_LEGACY_V1_ROUTES"); err != nil {
		return err
	}
	if err := validateStrictBool(values, "LOG_ENABLE"); err != nil {
		return err
	}
	if err := validateMasqueradeTarget(values, "UPSTREAM_MASQUERADE_TARGET"); err != nil {
		return err
	}
	if err := validateMinInt(values, "LOG_MAX_REQUESTS", 1); err != nil {
		return err
	}
	if err := validateMinFloat(values, "LOG_MAX_BODY_SIZE_MB", 0); err != nil {
		return err
	}

	if err := validateProvidersDir(values, "PROVIDERS_DIR"); err != nil {
		return err
	}
	if err := validateListenAddr(values, "LISTEN_ADDR"); err != nil {
		return err
	}
	return nil
}

func validateListenAddr(values map[string]string, key string) error {
	value := strings.TrimSpace(values[key])
	if value == "" {
		return nil
	}
	normalized := normalizeListenAddr(value)
	if strings.HasPrefix(normalized, ":") {
		if _, err := strconv.Atoi(strings.TrimPrefix(normalized, ":")); err != nil {
			return ErrInvalidConfig(fmt.Sprintf("invalid %s: %q", key, value))
		}
		return nil
	}
	if _, _, found := strings.Cut(normalized, ":"); !found {
		return ErrInvalidConfig(fmt.Sprintf("invalid %s: %q", key, value))
	}
	return nil
}

func normalizeListenAddr(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return trimmed
	}
	if _, err := strconv.Atoi(trimmed); err == nil {
		return ":" + trimmed
	}
	return trimmed
}

func validateTimezone(values map[string]string, key string) error {
	value := strings.TrimSpace(values[key])
	if value == "" {
		return nil
	}
	if _, err := time.LoadLocation(value); err != nil {
		return ErrInvalidConfig(fmt.Sprintf("invalid %s: %q", key, value))
	}
	return nil
}

func validateDownstreamNonStreamStrategy(values map[string]string, key string) error {
	value := strings.TrimSpace(values[key])
	if value == "" {
		return nil
	}
	if _, err := normalizeDownstreamNonStreamStrategy(value); err != nil {
		return ErrInvalidConfig(fmt.Sprintf("invalid %s: %q", key, value))
	}
	return nil
}

func normalizeDownstreamNonStreamStrategy(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case DownstreamNonStreamStrategyProxyBuffer:
		return DownstreamNonStreamStrategyProxyBuffer, nil
	case DownstreamNonStreamStrategyUpstreamNonStream:
		return DownstreamNonStreamStrategyUpstreamNonStream, nil
	default:
		return "", ErrInvalidConfig(fmt.Sprintf("invalid downstream non-stream strategy: %q", value))
	}
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

func validateMinFloat(values map[string]string, key string, min float64) error {
	value := strings.TrimSpace(values[key])
	if value == "" {
		return nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed < min {
		return ErrInvalidConfig(fmt.Sprintf("invalid %s: %q", key, value))
	}
	return nil
}

func validateMasqueradeTarget(values map[string]string, key string) error {
	value := strings.TrimSpace(values[key])
	if value == "" {
		return nil
	}
	switch strings.ToLower(value) {
	case MasqueradeTargetOpenCode, MasqueradeTargetClaude, MasqueradeTargetCodex, MasqueradeTargetNone:
		return nil
	default:
		return ErrInvalidConfig(fmt.Sprintf("invalid %s: %q (allowed: opencode, claude, codex, none)", key, value))
	}
}

func validateStrictBool(values map[string]string, key string) error {
	value := strings.TrimSpace(values[key])
	if value == "" {
		return nil
	}
	if _, err := strconv.ParseBool(value); err != nil {
		return ErrInvalidConfig(fmt.Sprintf("invalid %s: %q", key, value))
	}
	return nil
}

func parseRootBool(value string) bool {
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	return parsed
}

func validateProvidersDir(values map[string]string, key string) error {
	value := strings.TrimSpace(values[key])
	if value == "" {
		return nil
	}
	info, err := os.Stat(value)
	if err != nil {
		return ErrInvalidConfig(fmt.Sprintf("invalid %s: %v", key, err))
	}
	if !info.IsDir() {
		return ErrInvalidConfig(fmt.Sprintf("invalid %s: %q is not a directory", key, value))
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
	enabledCount := 0
	for _, p := range c.Providers {
		if p.Enabled {
			enabledCount++
		}
	}
	if enabledCount == 0 {
		return ErrInvalidConfig("at least one provider must be enabled")
	}
	if strings.TrimSpace(c.DefaultProvider) != "" {
		ids, err := c.DefaultProviderIDs()
		if err != nil {
			return err
		}
		for _, id := range ids {
			provider, err := c.ProviderByID(id)
			if err != nil {
				return ErrInvalidConfig(fmt.Sprintf("default provider not found: %s", id))
			}
			if !provider.Enabled {
				return ErrInvalidConfig(fmt.Sprintf("default provider must be enabled: %s", id))
			}
		}
		if len(ids) == 0 {
			return ErrInvalidConfig("default provider not found")
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

func (c Config) CacheInfoLocation() (*time.Location, error) {
	timezone := strings.TrimSpace(c.CacheInfoTimezone)
	if timezone == "" {
		timezone = Default().CacheInfoTimezone
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, ErrInvalidConfig(fmt.Sprintf("invalid CACHE_INFO_TIMEZONE: %q", timezone))
	}
	return location, nil
}

func ResolveProvidersDir(rootEnvPath string, providersDir string) string {
	if providersDir == "" || filepath.IsAbs(providersDir) {
		return providersDir
	}
	return filepath.Join(filepath.Dir(rootEnvPath), providersDir)
}

func (c *Config) applyStartupOnlyFrom(previous Config) {
	c.ListenAddr = previous.ListenAddr
	c.CacheInfoTimezone = previous.CacheInfoTimezone
	c.LogEnable = previous.LogEnable
	c.LogFilePath = previous.LogFilePath
	c.LogMaxRequests = previous.LogMaxRequests
	c.LogMaxBodySizeMB = previous.LogMaxBodySizeMB
	c.DebugArchiveRootDir = previous.DebugArchiveRootDir
}

func (c Config) hotReloadableRootEquals(other Config) bool {
	return c.ProxyAPIKey == other.ProxyAPIKey &&
		c.ProvidersDir == other.ProvidersDir &&
		c.DefaultProvider == other.DefaultProvider &&
		c.EnableLegacyV1Routes == other.EnableLegacyV1Routes &&
		c.DownstreamNonStreamStrategy == other.DownstreamNonStreamStrategy &&
		c.ConnectTimeout == other.ConnectTimeout &&
		c.FirstByteTimeout == other.FirstByteTimeout &&
		c.IdleTimeout == other.IdleTimeout &&
		c.TotalTimeout == other.TotalTimeout &&
		c.UpstreamUserAgent == other.UpstreamUserAgent &&
		c.MasqueradeTarget == other.MasqueradeTarget &&
		c.InjectClaudeCodeMetadataUserID == other.InjectClaudeCodeMetadataUserID &&
		c.InjectClaudeCodeSystemPrompt == other.InjectClaudeCodeSystemPrompt
}
