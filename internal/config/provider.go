package config

import (
	"bufio"
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
	MasqueradeTarget                       string
	UpstreamUserAgent                      string
	InjectClaudeCodeMetadataUserID         bool
	InjectClaudeCodeMetadataUserIDSet      bool
	InjectClaudeCodeSystemPrompt           bool
	InjectClaudeCodeSystemPromptSet        bool
	SupportsChat                           bool
	SupportsResponses                      bool
	SupportsModels                         bool
	SupportsAnthropicMessages              bool
	ModelMap                               []ModelMapEntry
	ManualModels                           []string
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
	UpstreamThinkingTagStyle               string
}

type ModelMapEntry struct {
	Key               string
	Target            string
	UnescapedKey      string
	UnescapedTarget   string
	HasWildcard       bool
	KeyParts          []string
	TargetHasCaptures bool
	CaptureCount      int
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

const (
	UpstreamThinkingTagStyleOff    = "off"
	UpstreamThinkingTagStyleLegacy = "legacy"

	MasqueradeTargetOpenCode = "opencode"
	MasqueradeTargetClaude   = "claude" // 模拟 Claude Code 客户端
	MasqueradeTargetCodex    = "codex"  // 模拟 OpenAI Codex CLI 客户端
	MasqueradeTargetNone     = "none"   // 不做任何伪装
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
		case "MODEL_MAP":
			provider.ModelMap, err = parseModelMap(value, path)
			if err != nil {
				return ProviderConfig{}, err
			}
		case "MANUAL_MODELS":
			provider.ManualModels = parseCommaSeparatedList(value)
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
		case "MASQUERADE_TARGET":
			provider.MasqueradeTarget = strings.ToLower(value)
		case "UPSTREAM_USER_AGENT":
			provider.UpstreamUserAgent = value
		case "INJECT_CLAUDE_CODE_METADATA_USER_ID":
			if strings.TrimSpace(value) == "" {
				break
			}
			parsed, parseErr := parseProviderStrictBool(value, key, path)
			if parseErr != nil {
				return ProviderConfig{}, parseErr
			}
			provider.InjectClaudeCodeMetadataUserIDSet = true
			provider.InjectClaudeCodeMetadataUserID = parsed
		case "INJECT_CLAUDE_CODE_SYSTEM_PROMPT":
			if strings.TrimSpace(value) == "" {
				break
			}
			parsed, parseErr := parseProviderStrictBool(value, key, path)
			if parseErr != nil {
				return ProviderConfig{}, parseErr
			}
			provider.InjectClaudeCodeSystemPromptSet = true
			provider.InjectClaudeCodeSystemPrompt = parsed
		case "UPSTREAM_THINKING_TAG_STYLE":
			enabled, parseErr := parseProviderStrictBool(value, key, path)
			if parseErr != nil {
				return ProviderConfig{}, parseErr
			}
			if enabled {
				provider.UpstreamThinkingTagStyle = UpstreamThinkingTagStyleLegacy
			} else {
				provider.UpstreamThinkingTagStyle = UpstreamThinkingTagStyleOff
			}
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
	if err := normalizeMasqueradeTarget(&provider, path); err != nil {
		return ProviderConfig{}, err
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

func normalizeMasqueradeTarget(provider *ProviderConfig, path string) error {
	trimmed := strings.TrimSpace(provider.MasqueradeTarget)
	if trimmed == "" {
		provider.MasqueradeTarget = ""
		return nil
	}
	switch trimmed {
	case MasqueradeTargetOpenCode, MasqueradeTargetClaude, MasqueradeTargetCodex, MasqueradeTargetNone:
		provider.MasqueradeTarget = trimmed
		return nil
	default:
		return ErrInvalidConfig(fmt.Sprintf("invalid MASQUERADE_TARGET in %s: %q (allowed: opencode, claude, codex, none)", path, trimmed))
	}
}

func normalizeSystemPromptPosition(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case SystemPromptPositionAppend:
		return SystemPromptPositionAppend
	default:
		return SystemPromptPositionPrepend
	}
}

func parseModelMap(raw string, path string) ([]ModelMapEntry, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ','
	})
	entries := make([]ModelMapEntry, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, ":", 2)
		if len(kv) != 2 || strings.TrimSpace(kv[0]) == "" || strings.TrimSpace(kv[1]) == "" {
			return nil, ErrInvalidConfig(fmt.Sprintf("invalid MODEL_MAP entry in %s: %q (expected format: src:target,src2:target2)", path, part))
		}
		key := strings.TrimSpace(kv[0])
		target := strings.TrimSpace(kv[1])
		entries = append(entries, NewModelMapEntry(key, target))
	}
	return entries, nil
}

func parseCommaSeparatedList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ','
	})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, unescapeString(trimmed))
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func unescapeString(s string) string {
	var result []byte
	i := 0
	for i < len(s) {
		if s[i] == '\\' && i+1 < len(s) {
			next := s[i+1]
			if next == '\\' || next == '*' || next == '$' {
				result = append(result, next)
				i += 2
				continue
			}
		}
		result = append(result, s[i])
		i++
	}
	return string(result)
}

func NewModelMapEntry(key, target string) ModelMapEntry {
	entry := ModelMapEntry{Key: key, Target: target}
	entry.UnescapedKey = unescapeString(key)
	entry.UnescapedTarget = unescapeString(target)
	entry.HasWildcard = strings.Contains(entry.UnescapedKey, "*")
	parts := strings.Split(entry.UnescapedKey, "*")
	entry.KeyParts = make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			entry.KeyParts = append(entry.KeyParts, p)
		}
	}
	entry.CaptureCount = len(parts) - 1
	entry.TargetHasCaptures = strings.Contains(entry.UnescapedTarget, "$")
	return entry
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
	if mapped := p.resolveModel(model, enableSuffix); mapped != "" {
		return mapped
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
	if mapped := p.resolveModel(model, enableSuffix); mapped != "" {
		return finalizeResolvedModelAndEffort(mapped, requestedEffort, enableSuffix)
	}
	if enableSuffix && requestedModel != model {
		if mapped := p.resolveModel(requestedModel, false); mapped != "" {
			return finalizeResolvedModelAndEffort(mapped, requestedEffort, enableSuffix)
		}
	}
	return finalizeResolvedModelAndEffort(requestedModel, requestedEffort, enableSuffix)
}

func (p ProviderConfig) resolveModel(model string, enableSuffix bool) string {
	if p.ModelMap == nil {
		return ""
	}
	for _, entry := range p.ModelMap {
		if matchModelWildcard(model, entry) {
			return applyCaptureReplacement(model, entry)
		}
	}
	return ""
}

func matchModelWildcard(model string, entry ModelMapEntry) bool {
	actualKey := entry.UnescapedKey
	if actualKey == "" {
		actualKey = entry.Key
	}
	if actualKey == "*" {
		return true
	}
	if !entry.HasWildcard {
		return model == actualKey
	}

	parts := entry.KeyParts
	if len(parts) == 0 {
		return false
	}

	if !strings.HasPrefix(model, parts[0]) {
		return false
	}

	pos := len(parts[0])
	for i := 1; i < len(parts); i++ {
		idx := strings.Index(model[pos:], parts[i])
		if idx < 0 {
			return false
		}
		pos += idx + len(parts[i])
	}

	lastPart := parts[len(parts)-1]
	suffixStart := len(model) - len(lastPart)
	if suffixStart < pos {
		return false
	}

	return true
}

func applyCaptureReplacement(model string, entry ModelMapEntry) string {
	target := entry.UnescapedTarget
	if target == "" {
		target = entry.Target
	}
	if !entry.TargetHasCaptures {
		return target
	}

	parts := entry.KeyParts
	captures := make([]string, 0, entry.CaptureCount)
	pos := len(parts[0])

	for i := 1; i < len(parts); i++ {
		idx := strings.Index(model[pos:], parts[i])
		if idx < 0 {
			break
		}
		captures = append(captures, model[pos:pos+idx])
		pos += idx + len(parts[i])
	}

	result := entry.UnescapedTarget
	result = strings.Replace(result, "$0", model, -1)
	for i, cap := range captures {
		result = strings.Replace(result, fmt.Sprintf("$%d", i+1), cap, -1)
	}
	return result
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
