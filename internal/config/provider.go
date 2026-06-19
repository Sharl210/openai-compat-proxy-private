package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"openai-compat-proxy/internal/reasoning"
)

type ProviderConfig struct {
	ID                                     string
	Enabled                                bool
	UpstreamBaseURL                        string
	UpstreamAPIKey                         string
	OpenAIServiceTier                      string
	AnthropicMaxThinkingBudget             int
	AnthropicMaxThinkingBudgetSet          bool
	UpstreamMaxOutputTokens                int
	UpstreamMaxOutputTokensSet             bool
	UpstreamMaxOutputTokenRules            []ScopedIntRule
	ForceUpstreamMaxOutputTokens           bool
	ForceUpstreamMaxOutputTokensSet        bool
	ModelLimitContextTokens                int
	ModelLimitContextTokensSet             bool
	ModelLimitContextTokenRules            []ScopedIntRule
	UpstreamEndpointType                   string
	ResponsesToolCompatMode                string
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
	ModelIDTemplate                        string
	ModelIDTemplateRootOnly                bool
	ModelMap                               []ModelMapEntry
	ManualModels                           []string
	HiddenModels                           []string
	EnableReasoningEffortSuffix            bool
	ExposeReasoningSuffixModels            bool
	MapReasoningSuffixToAnthropicThinking  bool
	EnableNoPromptModelSuffix              bool
	EnableNoPromptModelSuffixSet           bool
	UpstreamFirstByteTimeout               time.Duration
	UpstreamRetryCount                     int
	UpstreamRetryCountSet                  bool
	UpstreamRetryDelay                     time.Duration
	UpstreamRetryDelaySet                  bool
	UpstreamCacheControl                   string
	UpstreamCacheControlSet                bool
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
	UpstreamXMLToolCallStyle               string
}

type ModelMapEntry struct {
	Key               string
	Target            string
	UnescapedKey      string
	UnescapedTarget   string
	Pattern           *regexp.Regexp
	IsStaticKey       bool
	TargetHasCaptures bool
}

type ScopedIntRule struct {
	Pattern   string
	Tokens    int
	IsExact   bool
	PatternRE *regexp.Regexp
}

const (
	SystemPromptPositionPrepend         = "prepend"
	SystemPromptPositionAppend          = "append"
	DefaultUpstreamRetryCount           = 3
	DefaultUpstreamRetryDelay           = 5 * time.Second
	UpstreamEndpointTypeResponses       = "responses"
	UpstreamEndpointTypeChat            = "chat"
	UpstreamEndpointTypeAnthropic       = "anthropic"
	OpenAIServiceTierAuto               = "auto"
	OpenAIServiceTierDefault            = "default"
	OpenAIServiceTierFlex               = "flex"
	OpenAIServiceTierPriority           = "priority"
	UpstreamCacheControl5Min            = "5min"
	UpstreamCacheControl1H              = "1h"
	UpstreamCacheControlFalse           = "false"
	UpstreamCacheControlNoChange        = "nochange"
	ResponsesToolCompatModePreserve     = "preserve"
	ResponsesToolCompatModeFunctionOnly = "function_only"
)

const (
	manualReasonSuffixPrefix = "#reason_suffix:"

	UpstreamThinkingTagStyleOff    = "off"
	UpstreamThinkingTagStyleLegacy = "legacy"
	UpstreamXMLToolCallStyleOff    = "off"
	UpstreamXMLToolCallStyleLegacy = "legacy"

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
		UpstreamRetryCount:                    DefaultUpstreamRetryCount,
		UpstreamRetryDelay:                    DefaultUpstreamRetryDelay,
		UpstreamEndpointType:                  UpstreamEndpointTypeResponses,
		ResponsesToolCompatMode:               ResponsesToolCompatModePreserve,
		ModelIDTemplate:                       defaultModelIDTemplate,
		UpstreamXMLToolCallStyle:              UpstreamXMLToolCallStyleLegacy,
		MapReasoningSuffixToAnthropicThinking: true,
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
		case "OPENAI_SERVICE_TIER":
			normalized, err := normalizeOpenAIServiceTier(value)
			if err != nil {
				return ProviderConfig{}, ErrInvalidConfig(fmt.Sprintf("invalid OPENAI_SERVICE_TIER in %s: %q (allowed: auto, default, flex, priority)", path, value))
			}
			provider.OpenAIServiceTier = normalized
		case "ANTHROPIC_MAX_THINKING_BUDGET":
			if strings.TrimSpace(value) == "" {
				break
			}
			parsed, err := parseProviderMinInt(value, key, path, 1024)
			if err != nil {
				return ProviderConfig{}, err
			}
			provider.AnthropicMaxThinkingBudgetSet = true
			provider.AnthropicMaxThinkingBudget = parsed
		case "UPSTREAM_MAX_OUTPUT_TOKENS":
			if strings.TrimSpace(value) == "" {
				break
			}
			parsed, rules, err := parseScopedUpstreamMaxOutputTokens(value, key, path)
			if err != nil {
				return ProviderConfig{}, err
			}
			provider.UpstreamMaxOutputTokensSet = true
			provider.UpstreamMaxOutputTokens = parsed
			provider.UpstreamMaxOutputTokenRules = rules
		case "MODEL_LIMIT_CONTEXT_TOKENS":
			if strings.TrimSpace(value) == "" {
				break
			}
			parsed, rules, err := parseScopedModelLimitContextTokens(value, key, path)
			if err != nil {
				return ProviderConfig{}, err
			}
			provider.ModelLimitContextTokensSet = true
			provider.ModelLimitContextTokens = parsed
			provider.ModelLimitContextTokenRules = rules
		case "FORCE_UPSTREAM_MAX_OUTPUT_TOKENS":
			if strings.TrimSpace(value) == "" {
				break
			}
			provider.ForceUpstreamMaxOutputTokens, err = parseProviderStrictBool(value, key, path)
			if err != nil {
				return ProviderConfig{}, err
			}
			provider.ForceUpstreamMaxOutputTokensSet = true
		case "RESPONSES_TOOL_COMPAT_MODE":
			normalized, err := normalizeResponsesToolCompatMode(value)
			if err != nil {
				return ProviderConfig{}, ErrInvalidConfig(fmt.Sprintf("invalid RESPONSES_TOOL_COMPAT_MODE in %s: %q (allowed: preserve, function_only)", path, value))
			}
			provider.ResponsesToolCompatMode = normalized
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
		case "MODEL_ID_TEMPLATE":
			normalized, err := normalizeModelIDTemplate(value, key, path)
			if err != nil {
				return ProviderConfig{}, err
			}
			provider.ModelIDTemplate = normalized
		case "MODEL_ID_TEMPLATE_ROOT_ONLY":
			provider.ModelIDTemplateRootOnly, err = parseProviderStrictBool(value, key, path)
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
			if err := validateModelPatternList(provider.ManualModels, key, path); err != nil {
				return ProviderConfig{}, err
			}
		case "HIDDEN_MODELS":
			provider.HiddenModels = parseCommaSeparatedList(value)
			if err := validateModelPatternList(provider.HiddenModels, key, path); err != nil {
				return ProviderConfig{}, err
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
		case "ENABLE_NOPROMPT_MODEL_SUFFIX":
			if strings.TrimSpace(value) == "" {
				break
			}
			parsed, parseErr := parseProviderStrictBool(value, key, path)
			if parseErr != nil {
				return ProviderConfig{}, parseErr
			}
			provider.EnableNoPromptModelSuffixSet = true
			provider.EnableNoPromptModelSuffix = parsed
		case "UPSTREAM_FIRST_BYTE_TIMEOUT":
			parsed, err := parseProviderPositiveDuration(value, "UPSTREAM_FIRST_BYTE_TIMEOUT", path)
			if err != nil {
				return ProviderConfig{}, err
			}
			provider.UpstreamFirstByteTimeout = parsed
		case "UPSTREAM_RETRY_COUNT":
			if strings.TrimSpace(value) == "" {
				break
			}
			parsed, err := parseProviderRetryCount(value, path)
			if err != nil {
				return ProviderConfig{}, err
			}
			provider.UpstreamRetryCountSet = true
			provider.UpstreamRetryCount = parsed
		case "UPSTREAM_RETRY_DELAY":
			if strings.TrimSpace(value) == "" {
				break
			}
			parsed, err := parseProviderRetryDelay(value, path)
			if err != nil {
				return ProviderConfig{}, err
			}
			provider.UpstreamRetryDelaySet = true
			provider.UpstreamRetryDelay = parsed
		case "UPSTREAM_ANTHROPIC_CACHE_CONTROL":
			if strings.TrimSpace(value) == "" {
				break
			}
			normalized, err := normalizeUpstreamCacheControl(value)
			if err != nil {
				return ProviderConfig{}, ErrInvalidConfig(fmt.Sprintf("invalid UPSTREAM_ANTHROPIC_CACHE_CONTROL in %s: %q (allowed: 5min, 1h, false, nochange)", path, value))
			}
			provider.UpstreamCacheControlSet = true
			provider.UpstreamCacheControl = normalized
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
		case "UPSTREAM_XML_TOOL_CALL_STYLE":
			enabled, parseErr := parseProviderStrictBool(value, key, path)
			if parseErr != nil {
				return ProviderConfig{}, parseErr
			}
			if enabled {
				provider.UpstreamXMLToolCallStyle = UpstreamXMLToolCallStyleLegacy
			} else {
				provider.UpstreamXMLToolCallStyle = UpstreamXMLToolCallStyleOff
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

const (
	modelIDTemplatePlaceholder = "{{model}}"
	defaultModelIDTemplate     = modelIDTemplatePlaceholder
)

func normalizeModelIDTemplate(value string, key string, path string) (string, error) {
	template := strings.TrimSpace(value)
	if template == "" {
		return "", ErrInvalidConfig(fmt.Sprintf("invalid %s in %s: must contain exactly one %s placeholder", key, path, modelIDTemplatePlaceholder))
	}
	if strings.Count(template, modelIDTemplatePlaceholder) != 1 {
		return "", ErrInvalidConfig(fmt.Sprintf("invalid %s in %s: must contain exactly one %s placeholder", key, path, modelIDTemplatePlaceholder))
	}
	return template, nil
}

func (p ProviderConfig) effectiveModelIDTemplate() string {
	template := strings.TrimSpace(p.ModelIDTemplate)
	if template == "" {
		return defaultModelIDTemplate
	}
	return template
}

func (p ProviderConfig) ExternalModelID(model string, rootRoute bool) string {
	_ = rootRoute
	model = strings.TrimSpace(model)
	if model == "" {
		return model
	}
	template := p.effectiveModelIDTemplate()
	if template == defaultModelIDTemplate {
		return model
	}
	return strings.Replace(template, modelIDTemplatePlaceholder, model, 1)
}

func (p ProviderConfig) InternalModelID(model string, rootRoute bool) (string, bool) {
	_ = rootRoute
	model = strings.TrimSpace(model)
	if model == "" {
		return "", false
	}
	template := p.effectiveModelIDTemplate()
	if template == defaultModelIDTemplate {
		return model, true
	}
	return p.unpackTemplatedModelID(model)
}

func (p ProviderConfig) unpackTemplatedModelID(model string) (string, bool) {
	template := p.effectiveModelIDTemplate()
	if template == defaultModelIDTemplate {
		return model, true
	}
	prefix, suffix, _ := strings.Cut(template, modelIDTemplatePlaceholder)
	if !strings.HasPrefix(model, prefix) || !strings.HasSuffix(model, suffix) {
		return "", false
	}
	internal := strings.TrimSuffix(strings.TrimPrefix(model, prefix), suffix)
	if strings.TrimSpace(internal) == "" {
		return "", false
	}
	return internal, true
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

func (p ProviderConfig) EffectiveNoPromptModelSuffix(rootEnabled bool) bool {
	if p.EnableNoPromptModelSuffixSet {
		return p.EnableNoPromptModelSuffix
	}
	return rootEnabled
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

func (c Config) DefaultProviderIDs() ([]string, error) {
	return parseDefaultProviderIDs(c.DefaultProvider)
}

func (c Config) DefaultProviderConfig() (ProviderConfig, error) {
	ids, err := c.DefaultProviderIDs()
	if err != nil {
		return ProviderConfig{}, err
	}
	if len(ids) > 0 {
		return c.ProviderByID(ids[len(ids)-1])
	}
	return ProviderConfig{}, errors.New("default provider not found")
}

func parseDefaultProviderIDs(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	ids := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		id := strings.TrimSpace(part)
		if id == "" {
			return nil, ErrInvalidConfig("default provider list contains empty item")
		}
		if _, ok := seen[id]; ok {
			return nil, ErrInvalidConfig(fmt.Sprintf("duplicate default provider: %s", id))
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
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

func normalizeResponsesToolCompatMode(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ResponsesToolCompatModePreserve, nil
	}
	switch strings.ToLower(trimmed) {
	case ResponsesToolCompatModePreserve:
		return ResponsesToolCompatModePreserve, nil
	case ResponsesToolCompatModeFunctionOnly:
		return ResponsesToolCompatModeFunctionOnly, nil
	default:
		return "", ErrInvalidConfig(fmt.Sprintf("invalid responses tool compat mode: %q", value))
	}
}

func normalizeOpenAIServiceTier(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	switch strings.ToLower(trimmed) {
	case OpenAIServiceTierAuto:
		return OpenAIServiceTierAuto, nil
	case OpenAIServiceTierDefault:
		return OpenAIServiceTierDefault, nil
	case OpenAIServiceTierFlex:
		return OpenAIServiceTierFlex, nil
	case OpenAIServiceTierPriority:
		return OpenAIServiceTierPriority, nil
	default:
		return "", ErrInvalidConfig(fmt.Sprintf("invalid openai service tier: %q", value))
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

func parseProviderPositiveInt(value string, key string, path string) (int, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(trimmed)
	if err != nil || parsed == 0 || parsed < -1 {
		return 0, ErrInvalidConfig(fmt.Sprintf("invalid %s in %s: %q", key, path, value))
	}
	return parsed, nil
}

func parseProviderMinInt(value string, key string, path string, min int) (int, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(trimmed)
	if err != nil || parsed < min {
		return 0, ErrInvalidConfig(fmt.Sprintf("invalid %s in %s: %q", key, path, value))
	}
	return parsed, nil
}

func parseScopedUpstreamMaxOutputTokens(raw string, key string, path string) (int, []ScopedIntRule, error) {
	return parseScopedIntRules(raw, key, path)
}

func parseScopedModelLimitContextTokens(raw string, key string, path string) (int, []ScopedIntRule, error) {
	parsed, rules, err := parseScopedIntRules(raw, key, path)
	if err != nil {
		return 0, nil, err
	}
	if parsed == 0 && len(rules) > 0 {
		parsed = -1
	}
	return parsed, rules, nil
}

func parseScopedIntRules(raw string, key string, path string) (int, []ScopedIntRule, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, nil, nil
	}
	parts := strings.FieldsFunc(trimmed, func(r rune) bool { return r == ',' })
	defaultValue := 0
	rules := make([]ScopedIntRule, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, ":") {
			parsed, err := parseProviderPositiveInt(part, key, path)
			if err != nil {
				return 0, nil, err
			}
			defaultValue = parsed
			continue
		}
		pattern, value, ok := splitModelMapEntry(part)
		if !ok {
			return 0, nil, ErrInvalidConfig(fmt.Sprintf("invalid %s in %s: %q", key, path, raw))
		}
		pattern = strings.TrimSpace(pattern)
		value = strings.TrimSpace(value)
		if pattern == "" || value == "" {
			return 0, nil, ErrInvalidConfig(fmt.Sprintf("invalid %s in %s: %q", key, path, raw))
		}
		parsed, err := parseProviderPositiveInt(value, key, path)
		if err != nil {
			return 0, nil, err
		}
		rule := ScopedIntRule{Pattern: pattern, Tokens: parsed}
		if strings.HasPrefix(pattern, "#re:") {
			re, err := compileModelPatternStrict(pattern)
			if err != nil {
				return 0, nil, ErrInvalidConfig(fmt.Sprintf("invalid %s in %s: %q", key, path, raw))
			}
			rule.PatternRE = re
		} else {
			rule.IsExact = true
		}
		rules = append(rules, rule)
	}
	if defaultValue == 0 && len(rules) == 0 {
		return 0, nil, nil
	}
	return defaultValue, rules, nil
}

func (p ProviderConfig) ResolveUpstreamMaxOutputTokens(model string) int {
	return p.ResolveUpstreamMaxOutputTokensForReasoning(model, "")
}

func (p ProviderConfig) ResolveUpstreamMaxOutputTokensForReasoning(model string, effort string) int {
	return resolveScopedInt(p.UpstreamMaxOutputTokens, p.UpstreamMaxOutputTokenRules, model, effort)
}

func (p ProviderConfig) ResolveModelLimitContextTokens(model string) int {
	return p.ResolveModelLimitContextTokensForReasoning(model, "")
}

func (p ProviderConfig) ResolveModelLimitContextTokensForReasoning(model string, effort string) int {
	return resolveScopedInt(p.ModelLimitContextTokens, p.ModelLimitContextTokenRules, model, effort)
}

func resolveScopedInt(defaultValue int, rules []ScopedIntRule, model string, effort string) int {
	for _, candidate := range scopedRuleModelCandidates(model, effort) {
		for _, rule := range rules {
			if rule.IsExact && candidate == rule.Pattern {
				return rule.Tokens
			}
		}
	}
	for _, candidate := range scopedRuleModelCandidates(model, effort) {
		for _, rule := range rules {
			if rule.PatternRE != nil && rule.PatternRE.MatchString(candidate) {
				return rule.Tokens
			}
		}
	}
	return defaultValue
}

func scopedRuleModelCandidates(model string, effort string) []string {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	effort = normalizeReasoningEffort(effort)
	if effort == "" {
		return []string{model}
	}
	return []string{model + "-" + effort, model}
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
		key, target, ok := splitModelMapEntry(part)
		if !ok || strings.TrimSpace(key) == "" || strings.TrimSpace(target) == "" {
			return nil, ErrInvalidConfig(fmt.Sprintf("invalid MODEL_MAP entry in %s: %q (expected format: src:target,src2:target2)", path, part))
		}
		key = strings.TrimSpace(key)
		target = strings.TrimSpace(target)
		if _, err := compileModelPatternStrict(key); err != nil {
			return nil, ErrInvalidConfig(fmt.Sprintf("invalid MODEL_MAP pattern in %s: %q", path, key))
		}
		entries = append(entries, NewModelMapEntry(key, target))
	}
	return entries, nil
}

func splitModelMapEntry(part string) (string, string, bool) {
	escaped := false
	colonCount := 0
	for i := 0; i < len(part); i++ {
		ch := part[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch != ':' {
			continue
		}
		colonCount++
		if strings.HasPrefix(strings.TrimSpace(part), "#re:") && colonCount == 1 {
			continue
		}
		return part[:i], part[i+1:], true
	}
	return part, "", false
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
			result = append(result, trimmed)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func validateModelPatternList(patterns []string, key string, path string) error {
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if _, err := compileModelPatternStrict(pattern); err != nil {
			return ErrInvalidConfig(fmt.Sprintf("invalid %s pattern in %s: %q", key, path, pattern))
		}
	}
	return nil
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
	entry.UnescapedKey = key
	entry.UnescapedTarget = target
	entry.Pattern = compileModelPattern(entry.UnescapedKey)
	entry.IsStaticKey = isStaticModelPattern(entry.UnescapedKey)
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
	if mapped, _ := p.resolveModelEntry(model); mapped != "" {
		return mapped
	}
	return model
}

func (p ProviderConfig) ResolveModelAndEffort(model string, enableSuffix bool) (string, string) {
	return p.ResolveModelAndEffortWithRequestEffort(model, "", enableSuffix)
}

func (p ProviderConfig) ResolveModelAndEffortWithRequestEffort(model string, requestEffort string, enableClientSuffix bool) (string, string) {
	explicitRequestEffort := normalizeReasoningEffort(requestEffort)
	configSuffixEnabled := true
	if normalizedModel, normalizedEffort, hasNoPrompt := stripRootProxySuffixMarkers(model); hasNoPrompt {
		model = normalizedModel
		if explicitRequestEffort == "" {
			explicitRequestEffort = normalizedEffort
		}
	}
	enableClientSuffix = enableClientSuffix || (p.HasManualReasonSuffixForModel(model) && !p.HasLiteralManualModel(model))
	requestedModel := model
	requestedEffort := ""
	clientModelEffort := false
	if enableClientSuffix {
		if base, effort, ok := splitReasoningSuffix(model); ok {
			requestedModel = base
			requestedEffort = effort
			clientModelEffort = true
		}
	}
	if requestedEffort == "" {
		requestedEffort = explicitRequestEffort
	}
	synthesizedRequestEffort := explicitRequestEffort != "" && requestedEffort == explicitRequestEffort
	if requestedEffort != "" {
		effectiveModel := requestedModel + "-" + requestedEffort
		if mapped, entry := p.resolveModelEntryWithOrder(effectiveModel, true); mapped != "" {
			resolvedEffort := requestedEffort
			if _, mappedEffort, ok := splitReasoningSuffix(entry.UnescapedTarget); ok {
				resolvedEffort = mappedEffort
			} else if explicitRequestEffort != "" {
				resolvedEffort = explicitRequestEffort
			} else {
				resolvedEffort = ""
			}
			return finalizeResolvedModelAndEffort(mapped, resolvedEffort, configSuffixEnabled)
		}
	}
	if mapped, entry := p.resolveModelEntryWithOrder(model, true); mapped != "" {
		resolvedEffort := sourceEffortForMatchedModel(entry, synthesizedRequestEffort, requestedEffort)
		if _, mappedEffort, ok := splitReasoningSuffix(entry.UnescapedTarget); ok {
			resolvedEffort = mappedEffort
		}
		return finalizeResolvedModelAndEffort(mapped, resolvedEffort, configSuffixEnabled)
	}
	if enableClientSuffix && requestedModel != model {
		if mapped, _ := p.resolveModelEntryWithOrder(requestedModel, true); mapped != "" {
			return finalizeResolvedModelAndEffort(mapped, requestedEffort, configSuffixEnabled)
		}
	}
	if clientModelEffort {
		return requestedModel, requestedEffort
	}
	return requestedModel, ""
}

func stripProviderNoPromptModelSuffix(model string) (string, bool) {
	normalizedModel, _, hasNoPrompt := stripRootProxySuffixMarkers(model)
	if !hasNoPrompt {
		return model, false
	}
	return normalizedModel, true
}

func stripRootProxySuffixMarkers(model string) (string, string, bool) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return trimmed, "", false
	}
	current := trimmed
	resolvedEffort := ""
	hasNoPrompt := false
	for {
		changed := false
		if len(current) > len(providerNoPromptModelSuffix) && strings.HasSuffix(current, providerNoPromptModelSuffix) {
			current = strings.TrimSuffix(current, providerNoPromptModelSuffix)
			hasNoPrompt = true
			changed = true
		}
		if baseModel, effort, ok := splitReasoningSuffix(current); ok {
			current = baseModel
			if resolvedEffort == "" {
				resolvedEffort = effort
			}
			changed = true
		}
		if !changed {
			break
		}
	}
	if resolvedEffort != "" {
		current += "-" + resolvedEffort
	}
	return current, resolvedEffort, hasNoPrompt
}

func stripEnabledNoPromptModelSuffix(model string, enabled bool) (string, bool) {
	if !enabled {
		return model, false
	}
	return stripProviderNoPromptModelSuffix(model)
}

const providerNoPromptModelSuffix = "-noprompt"

func sourceEffortForMatchedModel(entry ModelMapEntry, synthesized bool, fallback string) string {
	if synthesized {
		return fallback
	}
	if fallback != "" {
		if _, _, ok := splitReasoningSuffix(entry.UnescapedTarget); !ok {
			return fallback
		}
	}
	if entry.IsStaticKey {
		if _, effort, ok := splitReasoningSuffix(entry.UnescapedKey); ok {
			return effort
		}
	}
	return ""
}

func normalizeReasoningEffort(effort string) string {
	effort = strings.TrimSpace(effort)
	switch effort {
	case "minimal", "xhigh", "medium", "high", "low", "none", "max":
		return effort
	default:
		return ""
	}
}

func (p ProviderConfig) HasLiteralManualModel(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	for _, manualModel := range p.ManualModels {
		manualModel = strings.TrimSpace(manualModel)
		if manualModel == "" || strings.HasPrefix(manualModel, manualReasonSuffixPrefix) || !isStaticModelPattern(manualModel) {
			continue
		}
		if manualModel == model {
			return true
		}
	}
	return false
}

func (p ProviderConfig) HasManualReasonSuffixForModel(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	baseModel := model
	if stripped, _, ok := splitReasoningSuffix(model); ok {
		baseModel = stripped
	}
	for _, manualModel := range p.ManualModels {
		manualSpec, ok := manualReasonSuffixSpecFromValue(manualModel)
		if ok && manualReasonSuffixSpecMatches(manualSpec, model, baseModel) {
			return true
		}
	}
	return false
}

func (p ProviderConfig) ManualReasonSuffixModelIDs() []string {
	ids := []string{}
	seen := map[string]struct{}{}
	for _, manualModel := range p.ManualModels {
		manualSpec, ok := manualReasonSuffixSpecFromValue(manualModel)
		if !ok || manualSpec.pattern == "" || manualSpec.suffix != "" || !isStaticModelPattern(manualSpec.pattern) {
			continue
		}
		for _, id := range reasoning.ExpandModelIDs([]string{manualSpec.pattern}, nil, true) {
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids
}

func (p ProviderConfig) ManualReasonSuffixModelIDsFrom(modelIDs []string) []string {
	ids := []string{}
	seen := map[string]struct{}{}
	for _, manualModel := range p.ManualModels {
		manualSpec, ok := manualReasonSuffixSpecFromValue(manualModel)
		if !ok {
			continue
		}
		if manualSpec.pattern != "" && manualSpec.suffix == "" && isStaticModelPattern(manualSpec.pattern) {
			for _, id := range reasoning.ExpandModelIDs([]string{manualSpec.pattern}, nil, true) {
				if _, exists := seen[id]; exists {
					continue
				}
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
			continue
		}
		for _, modelID := range modelIDs {
			modelID = strings.TrimSpace(modelID)
			if modelID == "" || manualSpec.matchesSuffix(modelID) || (manualSpec.pattern != "" && !modelPatternMatches(manualSpec.pattern, modelID)) {
				continue
			}
			expanded := reasoning.ExpandModelIDs([]string{modelID}, nil, true)
			if manualSpec.suffix != "" {
				expanded = []string{modelID + "-" + manualSpec.suffix}
			}
			for _, id := range expanded {
				if _, exists := seen[id]; exists {
					continue
				}
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func manualReasonSuffixStaticBase(value string) (string, bool) {
	spec, ok := manualReasonSuffixSpecFromValue(value)
	return spec.pattern, ok && spec.pattern != "" && spec.suffix == "" && isStaticModelPattern(spec.pattern)
}

func manualReasonSuffixPattern(value string) (string, bool) {
	spec, ok := manualReasonSuffixSpecFromValue(value)
	return spec.pattern, ok && spec.pattern != ""
}

type manualReasonSuffixSpec struct {
	pattern string
	suffix  string
}

func manualReasonSuffixSpecFromValue(value string) (manualReasonSuffixSpec, bool) {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, manualReasonSuffixPrefix) {
		return manualReasonSuffixSpecFromTail(strings.TrimSpace(strings.TrimPrefix(trimmed, manualReasonSuffixPrefix)), false)
	}
	return manualReasonSuffixSpec{}, false
}

func manualReasonSuffixSpecFromTail(tail string, forceRegex bool) (manualReasonSuffixSpec, bool) {
	if tail == "" {
		return manualReasonSuffixSpec{}, false
	}
	if suffix := manualReasonSuffixSelector(tail); suffix != "" {
		return manualReasonSuffixSpec{suffix: suffix}, true
	}
	if forceRegex && !strings.HasPrefix(tail, "#re:") {
		tail = "#re:" + tail
	}
	return manualReasonSuffixSpec{pattern: tail}, true
}

func manualReasonSuffixSelector(value string) string {
	if !strings.HasPrefix(value, "-") {
		return ""
	}
	suffix := strings.TrimPrefix(value, "-")
	switch suffix {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return suffix
	default:
		return ""
	}
}

func manualReasonSuffixPatternsEqual(left string, right string) bool {
	leftSpec, leftOK := manualReasonSuffixSpecFromValue(left)
	rightSpec, rightOK := manualReasonSuffixSpecFromValue(right)
	if !leftOK || !rightOK {
		return false
	}
	return leftSpec == rightSpec
}

func manualReasonSuffixSpecMatches(spec manualReasonSuffixSpec, model string, baseModel string) bool {
	if spec.suffix != "" {
		return spec.matchesSuffix(model)
	}
	return spec.pattern != "" && modelPatternMatches(spec.pattern, baseModel)
}

func (spec manualReasonSuffixSpec) matchesSuffix(model string) bool {
	_, effort, ok := reasoning.SplitSuffix(strings.TrimSpace(model))
	return ok && spec.suffix != "" && effort == spec.suffix
}

func manualReasonSuffixPatternMatches(pattern string, model string) bool {
	manualSpec, ok := manualReasonSuffixSpecFromValue(pattern)
	if !ok {
		return false
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	if manualSpec.suffix != "" {
		return manualSpec.matchesSuffix(model)
	}
	if manualSpec.pattern != "" && modelPatternMatches(manualSpec.pattern, model) {
		return true
	}
	if baseModel, _, ok := reasoning.SplitSuffix(model); ok && manualSpec.pattern != "" && modelPatternMatches(manualSpec.pattern, baseModel) {
		return true
	}
	return false
}

func ManualReasonSuffixBasePatternMatches(pattern string, model string) bool {
	manualSpec, ok := manualReasonSuffixSpecFromValue(pattern)
	return ok && manualSpec.pattern != "" && modelPatternMatches(manualSpec.pattern, model)
}

func IsManualReasonSuffixRegexPattern(pattern string) bool {
	manualSpec, ok := manualReasonSuffixSpecFromValue(pattern)
	return ok && strings.HasPrefix(manualSpec.pattern, "#re:")
}

func (p ProviderConfig) resolveModel(model string) string {
	mapped, _ := p.resolveModelEntry(model)
	return mapped
}

func (p ProviderConfig) resolveModelEntry(model string) (string, ModelMapEntry) {
	return p.resolveModelEntryWithOrder(model, false)
}

func (p ProviderConfig) resolveModelEntryWithOrder(model string, reverse bool) (string, ModelMapEntry) {
	if p.ModelMap == nil {
		return "", ModelMapEntry{}
	}
	if reverse {
		for i := len(p.ModelMap) - 1; i >= 0; i-- {
			entry := p.ModelMap[i]
			if matchModelPattern(model, entry) {
				return applyCaptureReplacement(model, entry), entry
			}
		}
		return "", ModelMapEntry{}
	}
	for _, entry := range p.ModelMap {
		if matchModelPattern(model, entry) {
			return applyCaptureReplacement(model, entry), entry
		}
	}
	return "", ModelMapEntry{}
}

func (p ProviderConfig) HidesModel(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	for _, manualModel := range p.ManualModels {
		manualModel = strings.TrimSpace(manualModel)
		if manualModel == "" {
			continue
		}
		if manualSpec, ok := manualReasonSuffixSpecFromValue(manualModel); ok {
			if manualSpec.pattern != "" && modelPatternMatches(manualSpec.pattern, model) {
				return false
			}
			if baseModel, _, suffixOK := reasoning.SplitSuffix(model); suffixOK && manualReasonSuffixSpecMatches(manualSpec, model, baseModel) {
				for _, pattern := range p.HiddenModels {
					pattern = strings.TrimSpace(pattern)
					if manualReasonSuffixPatternsEqual(pattern, manualModel) {
						continue
					}
					if manualReasonSuffixPatternMatches(pattern, model) {
						return true
					}
					if modelPatternMatches(pattern, model) {
						return true
					}
				}
				return false
			}
			continue
		}
		if modelPatternMatches(manualModel, model) {
			return false
		}
		if baseModel, _, ok := reasoning.SplitSuffix(model); ok && modelPatternMatches(manualModel, baseModel) {
			for _, pattern := range p.HiddenModels {
				pattern = strings.TrimSpace(pattern)
				if manualReasonSuffixPatternMatches(pattern, model) {
					return true
				}
				if modelPatternMatches(pattern, model) {
					return true
				}
			}
			return false
		}
	}
	for _, pattern := range p.HiddenModels {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if manualReasonSuffixPatternMatches(pattern, model) {
			return true
		}
		if _, ok := manualReasonSuffixPattern(pattern); ok {
			continue
		}
		if modelPatternMatches(pattern, model) {
			return true
		}
	}
	return false
}

func (p ProviderConfig) VisibleModelIDs() []string {
	base := make([]string, 0, len(p.ManualModels))
	seen := make(map[string]struct{}, len(p.ManualModels))
	for _, manualModel := range p.ManualModels {
		id := strings.TrimSpace(manualModel)
		if manualBase, ok := manualReasonSuffixStaticBase(id); ok {
			for _, expandedID := range reasoning.ExpandModelIDs([]string{manualBase}, nil, true) {
				if _, ok := seen[expandedID]; ok || p.HidesModel(expandedID) {
					continue
				}
				seen[expandedID] = struct{}{}
				base = append(base, expandedID)
			}
			continue
		}
		if id == "" || !isStaticModelPattern(id) {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		base = append(base, id)
	}
	if !(p.ExposeReasoningSuffixModels && p.EnableReasoningEffortSuffix) {
		return base
	}
	expanded := reasoning.ExpandModelIDs(base, nil, true)
	filtered := make([]string, 0, len(expanded))
	seen = make(map[string]struct{}, len(expanded))
	for _, id := range expanded {
		if p.HidesModel(id) {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		filtered = append(filtered, id)
	}
	return filtered
}

func shouldHideVisibleModelAlias(id string) bool {
	id = strings.TrimSpace(id)
	return id == "" || !isStaticModelPattern(id)
}

func matchModelPattern(model string, entry ModelMapEntry) bool {
	pattern := entry.Pattern
	if pattern == nil {
		pattern = compileModelPattern(patternKey(entry))
	}
	return pattern != nil && pattern.MatchString(model)
}

func applyCaptureReplacement(model string, entry ModelMapEntry) string {
	target := entry.UnescapedTarget
	if target == "" {
		target = entry.Target
	}
	if !entry.TargetHasCaptures {
		return target
	}
	pattern := entry.Pattern
	if pattern == nil {
		pattern = compileModelPattern(patternKey(entry))
	}
	if pattern == nil {
		return target
	}
	matches := pattern.FindStringSubmatch(model)
	if len(matches) == 0 {
		return target
	}
	return replaceSingleDigitCaptures(target, matches)
}

func replaceSingleDigitCaptures(target string, matches []string) string {
	var result strings.Builder
	for i := 0; i < len(target); i++ {
		if target[i] == '\\' && i+1 < len(target) && target[i+1] == '$' {
			result.WriteByte('$')
			i++
			continue
		}
		if target[i] != '$' || i+1 >= len(target) || target[i+1] < '0' || target[i+1] > '9' {
			result.WriteByte(target[i])
			continue
		}
		if i+2 < len(target) && target[i+2] >= '0' && target[i+2] <= '9' {
			result.WriteByte(target[i])
			continue
		}
		captureIndex := int(target[i+1] - '0')
		if captureIndex < len(matches) {
			result.WriteString(matches[captureIndex])
		}
		i++
	}
	return result.String()
}

func patternKey(entry ModelMapEntry) string {
	if entry.UnescapedKey != "" {
		return entry.UnescapedKey
	}
	return entry.Key
}

func compileModelPattern(pattern string) *regexp.Regexp {
	compiled, err := compileModelPatternStrict(pattern)
	if err != nil {
		return nil
	}
	return compiled
}

func compileModelPatternStrict(pattern string) (*regexp.Regexp, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, fmt.Errorf("empty model pattern")
	}
	if strings.HasPrefix(pattern, "#re:") {
		return regexp.Compile("^(?:" + strings.TrimPrefix(pattern, "#re:") + ")$")
	}
	return regexp.Compile("^(?:" + regexp.QuoteMeta(pattern) + ")$")
}

func modelPatternMatches(pattern string, model string) bool {
	compiled := compileModelPattern(pattern)
	return compiled != nil && compiled.MatchString(model)
}

func ModelPatternMatches(pattern string, model string) bool {
	return modelPatternMatches(pattern, model)
}

func isStaticModelPattern(pattern string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	return !strings.HasPrefix(pattern, "#re:")
}

func IsStaticModelPattern(pattern string) bool {
	return isStaticModelPattern(pattern)
}

func (entry ModelMapEntry) Resolve(model string) string {
	if !matchModelPattern(model, entry) {
		return ""
	}
	return applyCaptureReplacement(model, entry)
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
	supportedSuffixes := []string{"-minimal", "-xhigh", "-medium", "-high", "-low", "-none", "-max"}
	for _, suffix := range supportedSuffixes {
		if len(modelName) > len(suffix) && modelName[len(modelName)-len(suffix):] == suffix {
			return modelName[:len(modelName)-len(suffix)], suffix[1:], true
		}
	}
	return modelName, "", false
}
