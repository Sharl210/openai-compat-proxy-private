package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ProviderConfig struct {
	ID                                     string
	Enabled                                bool
	UpstreamBaseURL                        string
	UpstreamAPIKey                         string
	UpstreamEndpointType                   string
	SupportsChat                           bool
	SupportsResponses                      bool
	SupportsModels                         bool
	SupportsAnthropicMessages              bool
	ModelMap                               map[string]string
	EnableReasoningEffortSuffix            bool
	ExposeReasoningSuffixModels            bool
	MapReasoningSuffixToAnthropicThinking  bool
	UpstreamFirstByteTimeout               time.Duration
	UpstreamRetryCount                     int
	UpstreamRetryDelay                     time.Duration
	DownstreamNonStreamStrategyOverride    string
	DownstreamNonStreamStrategyOverrideSet bool
	ProxyAPIKeyOverride                    string
	ProxyAPIKeyOverrideSet                 bool
	SystemPromptFiles                      []string
	SystemPromptFilesRaw                   string
	SystemPromptPosition                   string
	SystemPromptText                       string
	AnthropicVersion                       string
}

const (
	SystemPromptPositionPrepend   = "prepend"
	SystemPromptPositionAppend    = "append"
	DefaultUpstreamRetryCount     = 5
	DefaultUpstreamRetryDelay     = 5 * time.Second
	UpstreamEndpointTypeResponses = "responses"
	UpstreamEndpointTypeChat      = "chat"
	UpstreamEndpointTypeAnthropic = "anthropic"
)

type invalidConfigError string

func (e invalidConfigError) Error() string { return string(e) }

func ErrInvalidConfig(message string) error { return invalidConfigError(message) }

func LoadProvidersFromDir(dir string) ([]ProviderConfig, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	providers := make([]ProviderConfig, 0, len(entries))
	seen := map[string]struct{}{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".env") || strings.HasSuffix(name, ".env.example") {
			continue
		}
		provider, err := loadProviderFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if provider.ID == "" {
			return nil, ErrInvalidConfig(fmt.Sprintf("provider file %s is missing PROVIDER_ID", name))
		}
		if _, exists := seen[provider.ID]; exists {
			return nil, ErrInvalidConfig(fmt.Sprintf("duplicate provider id: %s", provider.ID))
		}
		seen[provider.ID] = struct{}{}
		providers = append(providers, provider)
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].ID < providers[j].ID })
	return providers, nil
}

func loadProviderFile(path string) (ProviderConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return ProviderConfig{}, err
	}
	defer file.Close()

	provider := ProviderConfig{
		UpstreamRetryCount:   DefaultUpstreamRetryCount,
		UpstreamRetryDelay:   DefaultUpstreamRetryDelay,
		UpstreamEndpointType: UpstreamEndpointTypeResponses,
	}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			return ProviderConfig{}, ErrInvalidConfig(fmt.Sprintf("invalid provider line in %s: %s", path, line))
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "PROVIDER_ID":
			provider.ID = value
		case "PROVIDER_ENABLED":
			provider.Enabled, err = parseProviderStrictBool(value, key, path)
			if err != nil {
				return ProviderConfig{}, err
			}
		case "UPSTREAM_BASE_URL":
			provider.UpstreamBaseURL = value
		case "UPSTREAM_API_KEY":
			provider.UpstreamAPIKey = value
		case "UPSTREAM_ENDPOINT_TYPE":
			normalized, err := normalizeUpstreamEndpointType(value)
			if err != nil {
				return ProviderConfig{}, ErrInvalidConfig(fmt.Sprintf("invalid UPSTREAM_ENDPOINT_TYPE in %s: %q", path, value))
			}
			provider.UpstreamEndpointType = normalized
		case "SUPPORTS_CHAT":
			provider.SupportsChat, err = parseProviderSupportsBool(value, key, path)
			if err != nil {
				return ProviderConfig{}, err
			}
		case "SUPPORTS_RESPONSES":
			provider.SupportsResponses, err = parseProviderSupportsBool(value, key, path)
			if err != nil {
				return ProviderConfig{}, err
			}
		case "SUPPORTS_MODELS":
			provider.SupportsModels, err = parseProviderSupportsBool(value, key, path)
			if err != nil {
				return ProviderConfig{}, err
			}
		case "SUPPORTS_ANTHROPIC_MESSAGES":
			provider.SupportsAnthropicMessages, err = parseProviderSupportsBool(value, key, path)
			if err != nil {
				return ProviderConfig{}, err
			}
		case "MODEL_MAP_JSON":
			if value != "" {
				provider.ModelMap = map[string]string{}
				if err := json.Unmarshal([]byte(value), &provider.ModelMap); err != nil {
					return ProviderConfig{}, ErrInvalidConfig(fmt.Sprintf("invalid MODEL_MAP_JSON in %s: %v", path, err))
				}
			}
		case "ENABLE_REASONING_EFFORT_SUFFIX":
			provider.EnableReasoningEffortSuffix, err = parseProviderStrictBool(value, key, path)
			if err != nil {
				return ProviderConfig{}, err
			}
		case "EXPOSE_REASONING_SUFFIX_MODELS":
			provider.ExposeReasoningSuffixModels, err = parseProviderStrictBool(value, key, path)
			if err != nil {
				return ProviderConfig{}, err
			}
		case "MAP_REASONING_SUFFIX_TO_ANTHROPIC_THINKING":
			provider.MapReasoningSuffixToAnthropicThinking, err = parseProviderStrictBool(value, key, path)
			if err != nil {
				return ProviderConfig{}, err
			}
		case "UPSTREAM_FIRST_BYTE_TIMEOUT":
			parsed, err := parseProviderPositiveDuration(value, "UPSTREAM_FIRST_BYTE_TIMEOUT", path)
			if err != nil {
				return ProviderConfig{}, err
			}
			provider.UpstreamFirstByteTimeout = parsed
		case "UPSTREAM_RETRY_COUNT":
			parsed, err := parseProviderRetryCount(value, path)
			if err != nil {
				return ProviderConfig{}, err
			}
			provider.UpstreamRetryCount = parsed
		case "UPSTREAM_RETRY_DELAY":
			parsed, err := parseProviderRetryDelay(value, path)
			if err != nil {
				return ProviderConfig{}, err
			}
			provider.UpstreamRetryDelay = parsed
		case "DOWNSTREAM_NON_STREAM_STRATEGY_OVERRIDE":
			provider.DownstreamNonStreamStrategyOverrideSet = true
			if strings.TrimSpace(value) == "" {
				provider.DownstreamNonStreamStrategyOverride = ""
				break
			}
			normalized, err := normalizeDownstreamNonStreamStrategy(value)
			if err != nil {
				return ProviderConfig{}, ErrInvalidConfig(fmt.Sprintf("invalid DOWNSTREAM_NON_STREAM_STRATEGY_OVERRIDE in %s: %q", path, value))
			}
			provider.DownstreamNonStreamStrategyOverride = normalized
		case "PROXY_API_KEY_OVERRIDE":
			provider.ProxyAPIKeyOverrideSet = true
			provider.ProxyAPIKeyOverride = value
		case "SYSTEM_PROMPT_FILES":
			provider.SystemPromptFilesRaw = value
			provider.SystemPromptFiles = resolveProviderRelativePaths(path, value)
		case "SYSTEM_PROMPT_POSITION":
			provider.SystemPromptPosition = normalizeSystemPromptPosition(value)
		case "ANTHROPIC_VERSION":
			provider.AnthropicVersion = value
		}
	}
	if err := scanner.Err(); err != nil {
		return ProviderConfig{}, err
	}
	provider.UpstreamRetryCount = normalizeProviderRetryCount(provider.UpstreamRetryCount)
	provider.UpstreamRetryDelay = normalizeProviderRetryDelay(provider.UpstreamRetryDelay)
	provider.SystemPromptPosition = normalizeSystemPromptPosition(provider.SystemPromptPosition)
	if provider.AnthropicVersion == "" {
		provider.AnthropicVersion = "2023-06-01"
	}
	if provider.Enabled && strings.TrimSpace(provider.UpstreamBaseURL) == "" {
		return ProviderConfig{}, ErrInvalidConfig(fmt.Sprintf("UPSTREAM_BASE_URL is required for enabled provider %q", provider.ID))
	}
	return provider, nil
}

func (p ProviderConfig) EffectiveDownstreamNonStreamStrategy(rootStrategy string) string {
	if p.DownstreamNonStreamStrategyOverrideSet {
		if strings.TrimSpace(p.DownstreamNonStreamStrategyOverride) == "" {
			return rootStrategy
		}
		return p.DownstreamNonStreamStrategyOverride
	}
	return rootStrategy
}

func (p ProviderConfig) UsesUpstreamNonStreamForDownstreamNonStream(rootStrategy string) bool {
	return p.EffectiveDownstreamNonStreamStrategy(rootStrategy) == DownstreamNonStreamStrategyUpstreamNonStream
}

func (p ProviderConfig) ProxyAPIKeyDisabled() bool {
	return p.ProxyAPIKeyOverrideSet && strings.EqualFold(strings.TrimSpace(p.ProxyAPIKeyOverride), "empty")
}

func (p ProviderConfig) EffectiveProxyAPIKey(rootKey string) string {
	if p.ProxyAPIKeyDisabled() {
		return ""
	}
	if p.ProxyAPIKeyOverrideSet {
		if strings.TrimSpace(p.ProxyAPIKeyOverride) == "" {
			return rootKey
		}
		return p.ProxyAPIKeyOverride
	}
	return rootKey
}

func (p ProviderConfig) StatusCheckProxyAPIKey(rootKey string, preferRoot bool) string {
	if p.ProxyAPIKeyDisabled() {
		return ""
	}
	if preferRoot && rootKey != "" {
		return rootKey
	}
	return p.EffectiveProxyAPIKey(rootKey)
}

func (c Config) ProviderByID(id string) (ProviderConfig, error) {
	for _, provider := range c.Providers {
		if provider.ID == id {
			return provider, nil
		}
	}
	return ProviderConfig{}, errors.New("provider not found")
}

func (c Config) DefaultProviderConfig() (ProviderConfig, error) {
	if c.DefaultProvider != "" {
		return c.ProviderByID(c.DefaultProvider)
	}
	return ProviderConfig{}, errors.New("default provider not found")
}

func parseProviderSupportsBool(value string, key string, path string) (bool, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(trimmed)
	if err != nil {
		return false, ErrInvalidConfig(fmt.Sprintf("invalid %s in %s: %q", key, path, value))
	}
	return parsed, nil
}

func parseProviderStrictBool(value string, key string, path string) (bool, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(trimmed)
	if err != nil {
		return false, ErrInvalidConfig(fmt.Sprintf("invalid %s in %s: %q", key, path, value))
	}
	return parsed, nil
}

func normalizeUpstreamEndpointType(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return UpstreamEndpointTypeResponses, nil
	}
	switch strings.ToLower(trimmed) {
	case UpstreamEndpointTypeResponses:
		return UpstreamEndpointTypeResponses, nil
	case UpstreamEndpointTypeChat:
		return UpstreamEndpointTypeChat, nil
	case UpstreamEndpointTypeAnthropic:
		return UpstreamEndpointTypeAnthropic, nil
	default:
		return "", ErrInvalidConfig(fmt.Sprintf("invalid upstream endpoint type: %q", value))
	}
}

func parseProviderRetryCount(value string, path string) (int, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return DefaultUpstreamRetryCount, nil
	}
	parsed, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, ErrInvalidConfig(fmt.Sprintf("invalid UPSTREAM_RETRY_COUNT in %s: %q", path, value))
	}
	if parsed < 0 {
		return 0, ErrInvalidConfig(fmt.Sprintf("invalid UPSTREAM_RETRY_COUNT in %s: %q", path, value))
	}
	return parsed, nil
}

func parseProviderPositiveDuration(value string, key string, path string) (time.Duration, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, nil
	}
	parsed, err := time.ParseDuration(trimmed)
	if err != nil || parsed <= 0 {
		return 0, ErrInvalidConfig(fmt.Sprintf("invalid %s in %s: %q", key, path, value))
	}
	return parsed, nil
}

func normalizeProviderRetryCount(value int) int {
	if value < 0 {
		return DefaultUpstreamRetryCount
	}
	return value
}

func parseProviderRetryDelay(value string, path string) (time.Duration, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return DefaultUpstreamRetryDelay, nil
	}
	parsed, err := time.ParseDuration(trimmed)
	if err != nil {
		return 0, ErrInvalidConfig(fmt.Sprintf("invalid UPSTREAM_RETRY_DELAY in %s: %q", path, value))
	}
	if parsed < 0 {
		return 0, ErrInvalidConfig(fmt.Sprintf("invalid UPSTREAM_RETRY_DELAY in %s: %q", path, value))
	}
	return parsed, nil
}

func normalizeProviderRetryDelay(value time.Duration) time.Duration {
	if value < 0 {
		return DefaultUpstreamRetryDelay
	}
	return value
}

func normalizeSystemPromptPosition(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case SystemPromptPositionAppend:
		return SystemPromptPositionAppend
	default:
		return SystemPromptPositionPrepend
	}
}

func resolveProviderRelativePaths(providerEnvPath string, raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', '\n', '\r':
			return true
		default:
			return false
		}
	})
	if len(parts) == 0 {
		return nil
	}
	baseDir := filepath.Dir(providerEnvPath)
	resolved := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		if !filepath.IsAbs(trimmed) {
			trimmed = filepath.Join(baseDir, trimmed)
		}
		resolved = append(resolved, filepath.Clean(trimmed))
	}
	if len(resolved) == 0 {
		return nil
	}
	return resolved
}

func (p ProviderConfig) ResolveModel(model string, enableSuffix bool) string {
	if mapped, ok := p.ModelMap[model]; ok && mapped != "" {
		return mapped
	}
	if mapped, ok := p.ModelMap["*"]; ok && mapped != "" {
		return mapped
	}
	if enableSuffix {
		baseModel, _, ok := splitReasoningSuffix(model)
		if ok && baseModel != model {
			if mapped, ok := p.ModelMap[baseModel]; ok && mapped != "" {
				return mapped
			}
			if mapped, ok := p.ModelMap["*"]; ok && mapped != "" {
				return mapped
			}
		}
	}
	return model
}

func (p ProviderConfig) ResolveModelAndEffort(model string, enableSuffix bool) (string, string) {
	requestedModel := model
	requestedEffort := ""
	if enableSuffix {
		if base, effort, ok := splitReasoningSuffix(model); ok {
			requestedModel = base
			requestedEffort = effort
		}
	}
	for key, mapped := range p.ModelMap {
		if strings.HasPrefix(key, "*-") {
			if strings.HasSuffix(model, key[1:]) {
				return finalizeResolvedModelAndEffort(mapped, requestedEffort, enableSuffix)
			}
		}
	}
	if mapped, ok := p.ModelMap[model]; ok && mapped != "" {
		return finalizeResolvedModelAndEffort(mapped, requestedEffort, enableSuffix)
	}
	if mapped, ok := p.ModelMap[requestedModel]; ok && mapped != "" {
		return finalizeResolvedModelAndEffort(mapped, requestedEffort, enableSuffix)
	}
	if mapped, ok := p.ModelMap["*"]; ok && mapped != "" {
		return finalizeResolvedModelAndEffort(mapped, requestedEffort, enableSuffix)
	}
	return finalizeResolvedModelAndEffort(requestedModel, requestedEffort, enableSuffix)
}

func finalizeResolvedModelAndEffort(model string, requestedEffort string, enableSuffix bool) (string, string) {
	if !enableSuffix {
		return model, ""
	}
	baseModel, mappedEffort, ok := splitReasoningSuffix(model)
	if requestedEffort != "" {
		if ok {
			return baseModel, requestedEffort
		}
		return model, requestedEffort
	}
	if ok {
		return baseModel, mappedEffort
	}
	return model, ""
}

func splitReasoningSuffix(modelName string) (string, string, bool) {
	supportedSuffixes := []string{"-xhigh", "-medium", "-high", "-low"}
	for _, suffix := range supportedSuffixes {
		if len(modelName) > len(suffix) && modelName[len(modelName)-len(suffix):] == suffix {
			return modelName[:len(modelName)-len(suffix)], suffix[1:], true
		}
	}
	return modelName, "", false
}
