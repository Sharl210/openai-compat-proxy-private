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
	ID                          string
	Enabled                     bool
	UpstreamBaseURL             string
	UpstreamAPIKey              string
	SupportsChat                bool
	SupportsResponses           bool
	SupportsModels              bool
	SupportsAnthropicMessages   bool
	ModelMap                    map[string]string
	EnableReasoningEffortSuffix bool
	ExposeReasoningSuffixModels bool
	UpstreamFirstByteTimeout    time.Duration
	UpstreamRetryCount          int
	UpstreamRetryDelay          time.Duration
	ProxyAPIKeyOverride         string
	ProxyAPIKeyOverrideSet      bool
	SystemPromptFiles           []string
	SystemPromptFilesRaw        string
	SystemPromptPosition        string
	SystemPromptText            string
}

const (
	SystemPromptPositionPrepend = "prepend"
	SystemPromptPositionAppend  = "append"
	DefaultUpstreamRetryCount   = 5
	DefaultUpstreamRetryDelay   = 5 * time.Second
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
		UpstreamRetryCount: DefaultUpstreamRetryCount,
		UpstreamRetryDelay: DefaultUpstreamRetryDelay,
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
			provider.Enabled = parseBool(value)
		case "UPSTREAM_BASE_URL":
			provider.UpstreamBaseURL = value
		case "UPSTREAM_API_KEY":
			provider.UpstreamAPIKey = value
		case "SUPPORTS_CHAT":
			provider.SupportsChat = parseBool(value)
		case "SUPPORTS_RESPONSES":
			provider.SupportsResponses = parseBool(value)
		case "SUPPORTS_MODELS":
			provider.SupportsModels = parseBool(value)
		case "SUPPORTS_ANTHROPIC_MESSAGES":
			provider.SupportsAnthropicMessages = parseBool(value)
		case "MODEL_MAP_JSON":
			if value != "" {
				provider.ModelMap = map[string]string{}
				if err := json.Unmarshal([]byte(value), &provider.ModelMap); err != nil {
					return ProviderConfig{}, ErrInvalidConfig(fmt.Sprintf("invalid MODEL_MAP_JSON in %s: %v", path, err))
				}
			}
		case "ENABLE_REASONING_EFFORT_SUFFIX":
			provider.EnableReasoningEffortSuffix = parseBool(value)
		case "EXPOSE_REASONING_SUFFIX_MODELS":
			provider.ExposeReasoningSuffixModels = parseBool(value)
		case "UPSTREAM_FIRST_BYTE_TIMEOUT":
			parsed, err := parseProviderPositiveDuration(value, "UPSTREAM_FIRST_BYTE_TIMEOUT", path)
			if err != nil {
				return ProviderConfig{}, err
			}
			provider.UpstreamFirstByteTimeout = parsed
		case "UPSTREAM_RETRY_COUNT":
			provider.UpstreamRetryCount = parseProviderRetryCount(value)
		case "UPSTREAM_RETRY_DELAY":
			provider.UpstreamRetryDelay = parseProviderRetryDelay(value)
		case "PROXY_API_KEY_OVERRIDE":
			provider.ProxyAPIKeyOverrideSet = true
			provider.ProxyAPIKeyOverride = value
		case "SYSTEM_PROMPT_FILES":
			provider.SystemPromptFilesRaw = value
			provider.SystemPromptFiles = resolveProviderRelativePaths(path, value)
		case "SYSTEM_PROMPT_POSITION":
			provider.SystemPromptPosition = normalizeSystemPromptPosition(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return ProviderConfig{}, err
	}
	provider.UpstreamRetryCount = normalizeProviderRetryCount(provider.UpstreamRetryCount)
	provider.UpstreamRetryDelay = normalizeProviderRetryDelay(provider.UpstreamRetryDelay)
	provider.SystemPromptPosition = normalizeSystemPromptPosition(provider.SystemPromptPosition)
	return provider, nil
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

func parseBool(value string) bool {
	return strings.EqualFold(value, "true") || value == "1"
}

func parseProviderRetryCount(value string) int {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return DefaultUpstreamRetryCount
	}
	parsed, err := strconv.Atoi(trimmed)
	if err != nil {
		return DefaultUpstreamRetryCount
	}
	return normalizeProviderRetryCount(parsed)
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

func parseProviderRetryDelay(value string) time.Duration {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return DefaultUpstreamRetryDelay
	}
	parsed, err := time.ParseDuration(trimmed)
	if err != nil {
		return DefaultUpstreamRetryDelay
	}
	return normalizeProviderRetryDelay(parsed)
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
