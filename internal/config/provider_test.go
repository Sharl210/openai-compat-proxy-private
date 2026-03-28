package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveModelAndEffortPrefersRequestSuffixOverMappedSuffix(t *testing.T) {
	p := ProviderConfig{ModelMap: map[string]string{"gpt-5": "claude-sonnet-4-5-low"}}

	model, effort := p.ResolveModelAndEffort("gpt-5-high", true)
	if model != "claude-sonnet-4-5" {
		t.Fatalf("expected mapped model without suffix, got %q", model)
	}
	if effort != "high" {
		t.Fatalf("expected request suffix high to win, got %q", effort)
	}
}

func TestResolveModelAndEffortDoesNotParseSuffixWhenDisabled(t *testing.T) {
	p := ProviderConfig{ModelMap: map[string]string{"*": "claude-sonnet-4-5-low"}}

	model, effort := p.ResolveModelAndEffort("gpt-5-high", false)
	if model != "claude-sonnet-4-5-low" {
		t.Fatalf("expected mapped model to remain untouched when disabled, got %q", model)
	}
	if effort != "" {
		t.Fatalf("expected no effort override when disabled, got %q", effort)
	}
}

func TestResolveModelAndEffortUsesMappedSuffixWhenNoRequestSuffix(t *testing.T) {
	p := ProviderConfig{ModelMap: map[string]string{"gpt-5": "claude-sonnet-4-5-low"}}

	model, effort := p.ResolveModelAndEffort("gpt-5", true)
	if model != "claude-sonnet-4-5" {
		t.Fatalf("expected mapped model without suffix, got %q", model)
	}
	if effort != "low" {
		t.Fatalf("expected mapped suffix low, got %q", effort)
	}
}

func TestLoadProviderFileResolvesSystemPromptFilesRelativeToProviderEnv(t *testing.T) {
	rootDir := t.TempDir()
	promptDir := filepath.Join(rootDir, "prompts")
	if err := os.MkdirAll(promptDir, 0o755); err != nil {
		t.Fatalf("mkdir prompt dir: %v", err)
	}
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\n" +
		"SYSTEM_PROMPT_FILES=prompt.md, prompts/extra.md\n" +
		"SYSTEM_PROMPT_POSITION=append\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}

	expectedPaths := []string{
		filepath.Join(rootDir, "prompt.md"),
		filepath.Join(rootDir, "prompts", "extra.md"),
	}
	if len(provider.SystemPromptFiles) != len(expectedPaths) {
		t.Fatalf("expected %d resolved prompt paths, got %#v", len(expectedPaths), provider.SystemPromptFiles)
	}
	for i, expected := range expectedPaths {
		if provider.SystemPromptFiles[i] != expected {
			t.Fatalf("expected prompt path %q at index %d, got %q", expected, i, provider.SystemPromptFiles[i])
		}
	}
	if provider.SystemPromptPosition != SystemPromptPositionAppend {
		t.Fatalf("expected append position, got %q", provider.SystemPromptPosition)
	}
}

func TestLoadProviderFileTreatsBlankOrInvalidPromptPositionAsPrepend(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nSYSTEM_PROMPT_POSITION=sideways\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}

	if provider.SystemPromptPosition != SystemPromptPositionPrepend {
		t.Fatalf("expected invalid prompt position to fall back to prepend, got %q", provider.SystemPromptPosition)
	}

	providerBody = "PROVIDER_ID=openai\nSYSTEM_PROMPT_POSITION=\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("rewrite provider env: %v", err)
	}

	provider, err = loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error after blank position: %v", err)
	}
	if provider.SystemPromptPosition != SystemPromptPositionPrepend {
		t.Fatalf("expected blank prompt position to fall back to prepend, got %q", provider.SystemPromptPosition)
	}
}

func TestLoadProviderFileAllowsBlankSystemPromptFiles(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nSYSTEM_PROMPT_FILES=\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}

	if len(provider.SystemPromptFiles) != 0 {
		t.Fatalf("expected blank prompt files to resolve to empty slice, got %#v", provider.SystemPromptFiles)
	}
	if provider.SystemPromptText != "" {
		t.Fatalf("expected blank prompt text, got %q", provider.SystemPromptText)
	}
}

func TestLoadProviderFileRejectsInvalidSupportsBooleanValues(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := strings.Join([]string{
		"PROVIDER_ID=openai",
		"SUPPORTS_CHAT=yes",
		"SUPPORTS_RESPONSES=enabled",
		"SUPPORTS_MODELS=maybe",
		"SUPPORTS_ANTHROPIC_MESSAGES=on",
		"",
	}, "\n")
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	_, err := loadProviderFile(providerEnvPath)
	if err == nil {
		t.Fatalf("expected invalid SUPPORTS_* boolean to fail validation")
	}
	if _, ok := err.(invalidConfigError); !ok {
		t.Fatalf("expected invalidConfigError for invalid SUPPORTS_* boolean, got %T", err)
	}
	if err.Error() != "invalid SUPPORTS_CHAT in "+providerEnvPath+": \"yes\"" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestLoadProviderFileSupportsFlagsKeepWeakSemantics(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := strings.Join([]string{
		"PROVIDER_ID=openai",
		"SUPPORTS_CHAT=false",
		"SUPPORTS_RESPONSES=true",
		"SUPPORTS_MODELS=1",
		"SUPPORTS_ANTHROPIC_MESSAGES=false",
		"",
	}, "\n")
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}

	if provider.SupportsChat {
		t.Fatalf("expected SUPPORTS_CHAT=false to disable only chat exposure")
	}
	if !provider.SupportsResponses {
		t.Fatalf("expected SUPPORTS_RESPONSES=true to keep responses exposure enabled")
	}
	if !provider.SupportsModels {
		t.Fatalf("expected SUPPORTS_MODELS=1 to keep models exposure enabled")
	}
	if provider.SupportsAnthropicMessages {
		t.Fatalf("expected SUPPORTS_ANTHROPIC_MESSAGES=false to disable only messages exposure")
	}
	if provider.SupportsResponses == provider.SupportsChat {
		t.Fatalf("expected responses exposure to remain independent from chat exposure")
	}
	if provider.SupportsModels == provider.SupportsAnthropicMessages {
		t.Fatalf("expected models exposure to remain independent from anthropic messages exposure")
	}
}

func TestLoadProviderFileUsesRetryDefaultsWhenUnset(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if provider.UpstreamRetryCount != DefaultUpstreamRetryCount {
		t.Fatalf("expected default retry count %d, got %d", DefaultUpstreamRetryCount, provider.UpstreamRetryCount)
	}
	if provider.UpstreamRetryDelay != DefaultUpstreamRetryDelay {
		t.Fatalf("expected default retry delay %v, got %v", DefaultUpstreamRetryDelay, provider.UpstreamRetryDelay)
	}
	if provider.UpstreamFirstByteTimeout != 0 {
		t.Fatalf("expected provider first byte timeout to inherit root config by default, got %v", provider.UpstreamFirstByteTimeout)
	}
}

func TestLoadProviderFileParsesFirstByteTimeoutOverride(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nUPSTREAM_FIRST_BYTE_TIMEOUT=20m\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if provider.UpstreamFirstByteTimeout != 20*time.Minute {
		t.Fatalf("expected provider first byte timeout 20m, got %v", provider.UpstreamFirstByteTimeout)
	}

	providerBody = "PROVIDER_ID=openai\nUPSTREAM_FIRST_BYTE_TIMEOUT=bad\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("rewrite provider env: %v", err)
	}
	if _, err := loadProviderFile(providerEnvPath); err == nil {
		t.Fatalf("expected invalid provider first byte timeout to fail validation")
	}
}

func TestLoadProviderFileParsesRetryOverrides(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nUPSTREAM_RETRY_COUNT=2\nUPSTREAM_RETRY_DELAY=750ms\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if provider.UpstreamRetryCount != 2 {
		t.Fatalf("expected retry count 2, got %d", provider.UpstreamRetryCount)
	}
	if provider.UpstreamRetryDelay != 750*time.Millisecond {
		t.Fatalf("expected retry delay 750ms, got %v", provider.UpstreamRetryDelay)
	}
}

func TestLoadProviderFileAllowsZeroRetryOverride(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nUPSTREAM_RETRY_COUNT=0\nUPSTREAM_RETRY_DELAY=0s\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if provider.UpstreamRetryCount != 0 {
		t.Fatalf("expected retry count 0, got %d", provider.UpstreamRetryCount)
	}
	if provider.UpstreamRetryDelay != 0 {
		t.Fatalf("expected retry delay 0, got %v", provider.UpstreamRetryDelay)
	}
	if provider.UpstreamFirstByteTimeout != 0 {
		t.Fatalf("expected provider first byte timeout to keep inheriting root config, got %v", provider.UpstreamFirstByteTimeout)
	}
}

func TestLoadProviderFileRejectsInvalidRetryCount(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nUPSTREAM_RETRY_COUNT=-3\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	_, err := loadProviderFile(providerEnvPath)
	if err == nil {
		t.Fatalf("expected invalid retry count to fail validation")
	}
	if _, ok := err.(invalidConfigError); !ok {
		t.Fatalf("expected invalidConfigError for invalid retry count, got %T", err)
	}
	if err.Error() != "invalid UPSTREAM_RETRY_COUNT in "+providerEnvPath+": \"-3\"" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestLoadProviderFileRejectsInvalidRetryDelay(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nUPSTREAM_RETRY_DELAY=bad\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	if _, err := loadProviderFile(providerEnvPath); err == nil {
		t.Fatalf("expected invalid retry delay to fail validation")
	} else {
		if _, ok := err.(invalidConfigError); !ok {
			t.Fatalf("expected invalidConfigError for invalid retry delay, got %T", err)
		}
		if err.Error() != "invalid UPSTREAM_RETRY_DELAY in "+providerEnvPath+": \"bad\"" {
			t.Fatalf("unexpected error message: %v", err)
		}
	}
}

func TestLoadProviderFileParsesProxyAPIKeyOverride(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nPROXY_API_KEY_OVERRIDE=provider-secret\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if !provider.ProxyAPIKeyOverrideSet {
		t.Fatalf("expected proxy api key override to be marked as set")
	}
	if provider.ProxyAPIKeyOverride != "provider-secret" {
		t.Fatalf("expected proxy api key override provider-secret, got %q", provider.ProxyAPIKeyOverride)
	}
	if provider.EffectiveProxyAPIKey("root-secret") != "provider-secret" {
		t.Fatalf("expected provider override to win over root key")
	}
	if provider.StatusCheckProxyAPIKey("root-secret", false) != "provider-secret" {
		t.Fatalf("expected provider-scoped status key to use provider override")
	}
	if provider.StatusCheckProxyAPIKey("root-secret", true) != "root-secret" {
		t.Fatalf("expected legacy status key to prefer root key")
	}
	providerBody = "PROVIDER_ID=openai\nPROXY_API_KEY_OVERRIDE=empty\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("rewrite provider env: %v", err)
	}
	provider, err = loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error after empty override: %v", err)
	}
	if !provider.ProxyAPIKeyDisabled() {
		t.Fatalf("expected empty override to disable proxy auth")
	}
	if provider.EffectiveProxyAPIKey("root-secret") != "" {
		t.Fatalf("expected disabled override to return empty effective proxy key")
	}
}

func TestLoadProviderFileTreatsBlankProxyAPIKeyOverrideAsRootInheritance(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nPROXY_API_KEY_OVERRIDE=\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if !provider.ProxyAPIKeyOverrideSet {
		t.Fatalf("expected blank proxy api key override to be marked as set")
	}
	if provider.ProxyAPIKeyDisabled() {
		t.Fatalf("expected blank proxy api key override to inherit root key, not disable auth")
	}
	if got := provider.EffectiveProxyAPIKey("root-secret"); got != "root-secret" {
		t.Fatalf("expected blank override to inherit root key, got %q", got)
	}
	if got := provider.StatusCheckProxyAPIKey("root-secret", false); got != "root-secret" {
		t.Fatalf("expected provider-scoped status key to inherit root key, got %q", got)
	}
}

func TestLoadProviderFileParsesDownstreamNonStreamStrategyOverride(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nDOWNSTREAM_NON_STREAM_STRATEGY_OVERRIDE=upstream_non_stream\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if !provider.DownstreamNonStreamStrategyOverrideSet {
		t.Fatalf("expected downstream non-stream strategy override to be marked as set")
	}
	if got := provider.DownstreamNonStreamStrategyOverride; got != DownstreamNonStreamStrategyUpstreamNonStream {
		t.Fatalf("expected provider override %q, got %q", DownstreamNonStreamStrategyUpstreamNonStream, got)
	}
	if got := provider.EffectiveDownstreamNonStreamStrategy(DownstreamNonStreamStrategyProxyBuffer); got != DownstreamNonStreamStrategyUpstreamNonStream {
		t.Fatalf("expected provider override to win, got %q", got)
	}

	providerBody = "PROVIDER_ID=openai\nDOWNSTREAM_NON_STREAM_STRATEGY_OVERRIDE=\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("rewrite provider env: %v", err)
	}
	provider, err = loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error after blank override: %v", err)
	}
	if !provider.DownstreamNonStreamStrategyOverrideSet {
		t.Fatalf("expected blank downstream non-stream strategy override to be marked as set")
	}
	if got := provider.EffectiveDownstreamNonStreamStrategy(DownstreamNonStreamStrategyProxyBuffer); got != DownstreamNonStreamStrategyProxyBuffer {
		t.Fatalf("expected blank override to inherit root strategy, got %q", got)
	}

	providerBody = "PROVIDER_ID=openai\nDOWNSTREAM_NON_STREAM_STRATEGY_OVERRIDE=bad-mode\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("rewrite provider env with invalid strategy: %v", err)
	}
	if _, err := loadProviderFile(providerEnvPath); err == nil {
		t.Fatalf("expected invalid downstream non-stream strategy override to fail validation")
	}
}

func TestLoadProviderFileUsesResponsesUpstreamEndpointByDefault(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if provider.UpstreamEndpointType != UpstreamEndpointTypeResponses {
		t.Fatalf("expected default upstream endpoint type %q, got %q", UpstreamEndpointTypeResponses, provider.UpstreamEndpointType)
	}
}

func TestLoadProviderFileParsesUpstreamEndpointType(t *testing.T) {
	for _, endpointType := range []string{UpstreamEndpointTypeResponses, UpstreamEndpointTypeChat, UpstreamEndpointTypeAnthropic} {
		t.Run(endpointType, func(t *testing.T) {
			rootDir := t.TempDir()
			providerEnvPath := filepath.Join(rootDir, "openai.env")
			providerBody := "PROVIDER_ID=openai\nUPSTREAM_ENDPOINT_TYPE=" + endpointType + "\n"
			if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
				t.Fatalf("write provider env: %v", err)
			}

			provider, err := loadProviderFile(providerEnvPath)
			if err != nil {
				t.Fatalf("loadProviderFile returned error: %v", err)
			}
			if provider.UpstreamEndpointType != endpointType {
				t.Fatalf("expected upstream endpoint type %q, got %q", endpointType, provider.UpstreamEndpointType)
			}
		})
	}
}

func TestLoadProviderFileRejectsInvalidUpstreamEndpointType(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nUPSTREAM_ENDPOINT_TYPE=invalid\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	_, err := loadProviderFile(providerEnvPath)
	if err == nil {
		t.Fatalf("expected invalid upstream endpoint type to fail validation")
	}
	if _, ok := err.(invalidConfigError); !ok {
		t.Fatalf("expected invalidConfigError for invalid upstream endpoint type, got %T", err)
	}
	if err.Error() != "invalid UPSTREAM_ENDPOINT_TYPE in "+providerEnvPath+": \"invalid\"" {
		t.Fatalf("unexpected error message: %v", err)
	}
}
