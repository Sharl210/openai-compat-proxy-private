package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type ProviderConfig struct {
	ID                          string
	Enabled                     bool
	IsDefault                   bool
	UpstreamBaseURL             string
	UpstreamAPIKey              string
	SupportsChat                bool
	SupportsResponses           bool
	SupportsModels              bool
	SupportsAnthropicMessages   bool
	ModelMap                    map[string]string
	EnableReasoningEffortSuffix bool
	ExposeReasoningSuffixModels bool
}

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
	defaultCount := 0
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
		if provider.IsDefault {
			defaultCount++
		}
		providers = append(providers, provider)
	}
	if defaultCount > 1 {
		return nil, ErrInvalidConfig("multiple default providers configured")
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

	provider := ProviderConfig{}
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
		case "PROVIDER_IS_DEFAULT":
			provider.IsDefault = parseBool(value)
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
		}
	}
	if err := scanner.Err(); err != nil {
		return ProviderConfig{}, err
	}
	return provider, nil
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
	for _, provider := range c.Providers {
		if provider.IsDefault {
			return provider, nil
		}
	}
	return ProviderConfig{}, errors.New("default provider not found")
}

func parseBool(value string) bool {
	return strings.EqualFold(value, "true") || value == "1"
}

func (p ProviderConfig) ResolveModel(model string) string {
	if mapped, ok := p.ModelMap[model]; ok && mapped != "" {
		return mapped
	}
	if mapped, ok := p.ModelMap["*"]; ok && mapped != "" {
		return mapped
	}
	return model
}
