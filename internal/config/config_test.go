package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func responsesToolCompatModeFromField(t *testing.T, v any) string {
	t.Helper()
	field := reflect.ValueOf(v).FieldByName("ResponsesToolCompatMode")
	if !field.IsValid() {
		t.Fatal("expected ResponsesToolCompatMode field to be present")
	}
	if field.Kind() != reflect.String {
		t.Fatalf("expected ResponsesToolCompatMode to be a string field, got %s", field.Kind())
	}
	return field.String()
}

func TestDefaultDownstreamNonStreamStrategyIsProxyBuffer(t *testing.T) {
	if got := Default().DownstreamNonStreamStrategy; got != DownstreamNonStreamStrategyProxyBuffer {
		t.Fatalf("expected default downstream non-stream strategy %q, got %q", DownstreamNonStreamStrategyProxyBuffer, got)
	}
}

func TestDefaultCacheInfoTimezoneIsAsiaShanghai(t *testing.T) {
	if got := Default().CacheInfoTimezone; got != "Asia/Shanghai" {
		t.Fatalf("expected default cache info timezone %q, got %q", "Asia/Shanghai", got)
	}
}

func TestDefaultDebugArchiveRootDirUsesNamedDirectory(t *testing.T) {
	if got := Default().DebugArchiveRootDir; got != "OPENAI_COMPAT_DEBUG_ARCHIVE_DIR" {
		t.Fatalf("expected default debug archive root dir %q, got %q", "OPENAI_COMPAT_DEBUG_ARCHIVE_DIR", got)
	}
}

func TestDefaultDebugArchiveMaxRequestsIsTwoHundred(t *testing.T) {
	if got := Default().DebugArchiveMaxRequests; got != 200 {
		t.Fatalf("expected default debug archive max requests 200, got %d", got)
	}
}

func TestDefaultResponsesToolCompatModeIsPreserve(t *testing.T) {
	if got := responsesToolCompatModeFromField(t, Default()); got != "preserve" {
		t.Fatalf("expected default responses tool compat mode %q, got %q", "preserve", got)
	}
}

func TestDefaultUpstreamXMLToolCallStyleIsLegacy(t *testing.T) {
	if got := Default().UpstreamXMLToolCallStyle; got != UpstreamXMLToolCallStyleLegacy {
		t.Fatalf("expected default upstream XML tool call style %q, got %q", UpstreamXMLToolCallStyleLegacy, got)
	}
}

func TestDefaultLogMaxRequestsIsTwoHundred(t *testing.T) {
	if got := Default().LogMaxRequests; got != 200 {
		t.Fatalf("expected default log max requests 200, got %d", got)
	}
}

func TestLoadFromEnvParsesDownstreamNonStreamStrategy(t *testing.T) {
	t.Setenv("DOWNSTREAM_NON_STREAM_STRATEGY", DownstreamNonStreamStrategyUpstreamNonStream)

	cfg := LoadFromEnv()
	if got := cfg.DownstreamNonStreamStrategy; got != DownstreamNonStreamStrategyUpstreamNonStream {
		t.Fatalf("expected downstream non-stream strategy %q, got %q", DownstreamNonStreamStrategyUpstreamNonStream, got)
	}
}

func TestLoadFromEnvParsesDefaultProviderModelTagsFlag(t *testing.T) {
	t.Setenv("ENABLE_DEFAULT_PROVIDER_MODEL_TAGS", "true")

	cfg := LoadFromEnv()
	if !cfg.EnableDefaultProviderModelTags {
		t.Fatalf("expected ENABLE_DEFAULT_PROVIDER_MODEL_TAGS=true to enable tagged mode")
	}
}

func TestLoadFromEnvParsesAllDefaultProviderModelTagsFlag(t *testing.T) {
	t.Setenv("ENABLE_ALL_DEFAULT_PROVIDER_MODEL_TAGS", "true")

	cfg := LoadFromEnv()
	if !cfg.EnableAllDefaultProviderModelTags {
		t.Fatalf("expected ENABLE_ALL_DEFAULT_PROVIDER_MODEL_TAGS=true to enable all-tag mode")
	}
}

func TestLoadFromValuesParsesNoPromptModelSuffixFlag(t *testing.T) {
	cfg := LoadFromValues(map[string]string{"ENABLE_NOPROMPT_MODEL_SUFFIX": "true"})

	if !cfg.EnableNoPromptModelSuffix {
		t.Fatalf("expected ENABLE_NOPROMPT_MODEL_SUFFIX=true to enable noprompt suffix parsing")
	}
}

func TestLoadFromValuesParsesV1ModelMap(t *testing.T) {
	cfg := LoadFromValues(map[string]string{
		"V1_MODEL_MAP": "alias-alpha:alpha-chat, #re:alias-(.*):owned-$1",
	})

	if got, want := len(cfg.V1ModelMap), 2; got != want {
		t.Fatalf("expected %d V1_MODEL_MAP entries, got %d", want, got)
	}
	if got := cfg.ResolveV1Model("alias-alpha"); got != "alpha-chat" {
		t.Fatalf("expected exact V1 model alias to resolve to alpha-chat, got %q", got)
	}
	if got := cfg.ResolveV1Model("alias-5"); got != "owned-5" {
		t.Fatalf("expected regex V1 model alias to resolve to owned-5, got %q", got)
	}
	if got := cfg.ResolveV1Model("already-canonical"); got != "already-canonical" {
		t.Fatalf("expected unmatched V1 model to remain unchanged, got %q", got)
	}
}

func TestResolveV1ModelTreatsReasoningFamilyMembersAsOneSourceFamily(t *testing.T) {
	cfg := LoadFromValues(map[string]string{
		"V1_MODEL_MAP":                 "gpt-5.5:gpt-5.4",
		"ENABLE_NOPROMPT_MODEL_SUFFIX": "true",
	})

	if got := cfg.ResolveV1Model("gpt-5.5"); got != "gpt-5.4" {
		t.Fatalf("expected base family member to map to base target, got %q", got)
	}
	if got := cfg.ResolveV1Model("gpt-5.5-high"); got != "gpt-5.4-high" {
		t.Fatalf("expected suffixed family member to map via same root family rule, got %q", got)
	}
	if got := cfg.ResolveV1ModelForRequest("gpt-5.5-noprompt-high", ""); got != "gpt-5.4-high" {
		t.Fatalf("expected noprompt marker to stay out of root family map key, got %q", got)
	}
}

func TestResolveV1ModelTreatsExplicitReasoningRequestAsSameFamilyMember(t *testing.T) {
	cfg := LoadFromValues(map[string]string{
		"V1_MODEL_MAP": "gpt-5.5:gpt-5.4",
	})

	if got := cfg.ResolveV1Model("gpt-5.5-high"); got != "gpt-5.4-high" {
		t.Fatalf("expected explicit-effort equivalent family member to map to same root target, got %q", got)
	}
}

func TestLoadFromValuesParsesRootUpstreamMaxOutputTokens(t *testing.T) {
	cfg := LoadFromValues(map[string]string{
		"UPSTREAM_MAX_OUTPUT_TOKENS":       "64000,gpt-5.5:128000,#re:.*gpt-.*:100000",
		"FORCE_UPSTREAM_MAX_OUTPUT_TOKENS": "true",
	})

	if cfg.UpstreamMaxOutputTokens != 64000 {
		t.Fatalf("expected root upstream max output tokens 64000, got %d", cfg.UpstreamMaxOutputTokens)
	}
	if len(cfg.UpstreamMaxOutputTokenRules) != 2 {
		t.Fatalf("expected 2 root scoped max output rules, got %#v", cfg.UpstreamMaxOutputTokenRules)
	}
	if cfg.UpstreamMaxOutputTokenRules[0].Pattern != "gpt-5.5" || cfg.UpstreamMaxOutputTokenRules[0].Tokens != 128000 {
		t.Fatalf("expected exact root token rule first, got %#v", cfg.UpstreamMaxOutputTokenRules)
	}
	if cfg.UpstreamMaxOutputTokenRules[1].Pattern != "#re:.*gpt-.*" || cfg.UpstreamMaxOutputTokenRules[1].Tokens != 100000 {
		t.Fatalf("expected regex root token rule second, got %#v", cfg.UpstreamMaxOutputTokenRules)
	}
	if !cfg.ForceUpstreamMaxOutputTokens {
		t.Fatalf("expected root force upstream max output tokens to be true")
	}
}

func TestLoadFromValuesParsesRootModelLimitContextTokens(t *testing.T) {
	cfg := LoadFromValues(map[string]string{
		"MODEL_LIMIT_CONTEXT_TOKENS": "-1,gpt-5.5:256000,#re:.*gpt-.*:64000",
	})

	if cfg.ModelLimitContextTokens != -1 {
		t.Fatalf("expected root model limit context tokens -1, got %d", cfg.ModelLimitContextTokens)
	}
	if len(cfg.ModelLimitContextTokenRules) != 2 {
		t.Fatalf("expected 2 root context limit rules, got %#v", cfg.ModelLimitContextTokenRules)
	}
	if cfg.ModelLimitContextTokenRules[0].Pattern != "gpt-5.5" || cfg.ModelLimitContextTokenRules[0].Tokens != 256000 {
		t.Fatalf("expected exact root context limit rule first, got %#v", cfg.ModelLimitContextTokenRules)
	}
	if cfg.ModelLimitContextTokenRules[1].Pattern != "#re:.*gpt-.*" || cfg.ModelLimitContextTokenRules[1].Tokens != 64000 {
		t.Fatalf("expected regex root context limit rule second, got %#v", cfg.ModelLimitContextTokenRules)
	}
}

func TestLoadFromValuesTreatsScopedOnlyModelLimitContextTokensDefaultAsUnlimited(t *testing.T) {
	cfg := LoadFromValues(map[string]string{
		"MODEL_LIMIT_CONTEXT_TOKENS": "gpt-5.5:256000",
	})

	if cfg.ModelLimitContextTokens != -1 {
		t.Fatalf("expected scoped-only root context limit default -1, got %d", cfg.ModelLimitContextTokens)
	}
	if len(cfg.ModelLimitContextTokenRules) != 1 {
		t.Fatalf("expected one scoped context limit rule, got %#v", cfg.ModelLimitContextTokenRules)
	}
}

func TestValidateRootEnvValuesRejectsInvalidModelLimitContextTokens(t *testing.T) {
	for _, value := range []string{"0", "-2", "bad"} {
		t.Run(value, func(t *testing.T) {
			err := ValidateRootEnvValues(map[string]string{"MODEL_LIMIT_CONTEXT_TOKENS": value})
			if err == nil {
				t.Fatalf("expected invalid MODEL_LIMIT_CONTEXT_TOKENS=%q to fail validation", value)
			}
		})
	}
}

func TestValidateRootEnvValuesRejectsInvalidV1ModelMap(t *testing.T) {
	err := ValidateRootEnvValues(map[string]string{"V1_MODEL_MAP": "missing-target:"})
	if err == nil {
		t.Fatalf("expected invalid V1_MODEL_MAP to fail validation")
	}
}

func TestValidateRootEnvValuesRejectsInvalidRootUpstreamMaxOutputTokens(t *testing.T) {
	for _, value := range []string{"0", "-2", "bad"} {
		t.Run(value, func(t *testing.T) {
			err := ValidateRootEnvValues(map[string]string{"UPSTREAM_MAX_OUTPUT_TOKENS": value})
			if err == nil {
				t.Fatalf("expected invalid UPSTREAM_MAX_OUTPUT_TOKENS=%q to fail validation", value)
			}
		})
	}
}

func TestValidateRootEnvValuesRejectsInvalidRootForceUpstreamMaxOutputTokens(t *testing.T) {
	err := ValidateRootEnvValues(map[string]string{"FORCE_UPSTREAM_MAX_OUTPUT_TOKENS": "maybe"})
	if err == nil {
		t.Fatalf("expected invalid FORCE_UPSTREAM_MAX_OUTPUT_TOKENS to fail validation")
	}
}

func TestLoadFromValuesParsesRootAndProviderUpstreamAnthropicCacheControl(t *testing.T) {
	cfg := LoadFromValues(map[string]string{
		"UPSTREAM_ANTHROPIC_CACHE_CONTROL": "1h",
	})
	if got := cfg.UpstreamCacheControl; got != UpstreamCacheControl1H {
		t.Fatalf("expected root cache control %q, got %q", UpstreamCacheControl1H, got)
	}

	tmp := t.TempDir()
	providerPath := filepath.Join(tmp, "openai.env")
	if err := os.WriteFile(providerPath, []byte("PROVIDER_ID=openai\nUPSTREAM_ANTHROPIC_CACHE_CONTROL=false\n"), 0o600); err != nil {
		t.Fatalf("write provider env: %v", err)
	}
	providerCfg, err := loadProviderFile(providerPath)
	if err != nil {
		t.Fatalf("load provider env: %v", err)
	}
	if got := providerCfg.UpstreamCacheControl; got != UpstreamCacheControlFalse {
		t.Fatalf("expected provider cache control %q, got %q", UpstreamCacheControlFalse, got)
	}
	if !providerCfg.UpstreamCacheControlSet {
		t.Fatalf("expected provider cache control to be marked as explicitly set")
	}
}

func TestValidateRootEnvValuesRejectsInvalidUpstreamAnthropicCacheControl(t *testing.T) {
	if err := ValidateRootEnvValues(map[string]string{"UPSTREAM_ANTHROPIC_CACHE_CONTROL": ""}); err != nil {
		t.Fatalf("expected empty UPSTREAM_ANTHROPIC_CACHE_CONTROL to be treated as unset, got %v", err)
	}
	for _, value := range []string{"maybe", "5m"} {
		t.Run(value, func(t *testing.T) {
			err := ValidateRootEnvValues(map[string]string{"UPSTREAM_ANTHROPIC_CACHE_CONTROL": value})
			if err == nil {
				t.Fatalf("expected invalid UPSTREAM_ANTHROPIC_CACHE_CONTROL=%q to fail validation", value)
			}
		})
	}
}

func TestProviderEmptyRetryAndCacheControlValuesInheritRoot(t *testing.T) {
	rootDir := t.TempDir()
	providersDir := filepath.Join(rootDir, "providers")
	if err := os.MkdirAll(providersDir, 0o700); err != nil {
		t.Fatalf("mkdir providers dir: %v", err)
	}
	rootPath := filepath.Join(rootDir, ".env")
	root := strings.Join([]string{
		"PROVIDERS_DIR=" + providersDir,
		"DEFAULT_PROVIDER=openai",
		"UPSTREAM_RETRY_COUNT=4",
		"UPSTREAM_RETRY_DELAY=7s",
		"UPSTREAM_ANTHROPIC_CACHE_CONTROL=1h",
		"",
	}, "\n")
	if err := os.WriteFile(rootPath, []byte(root), 0o600); err != nil {
		t.Fatalf("write root env: %v", err)
	}
	provider := strings.Join([]string{
		"PROVIDER_ID=openai",
		"PROVIDER_ENABLED=true",
		"UPSTREAM_BASE_URL=https://example.test",
		"UPSTREAM_API_KEY=test-key",
		"SUPPORTS_RESPONSES=true",
		"UPSTREAM_RETRY_COUNT=",
		"UPSTREAM_RETRY_DELAY=",
		"UPSTREAM_ANTHROPIC_CACHE_CONTROL=",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(providersDir, "openai.env"), []byte(provider), 0o600); err != nil {
		t.Fatalf("write provider env: %v", err)
	}
	snapshot, err := BuildRuntimeSnapshot(rootPath)
	if err != nil {
		t.Fatalf("BuildRuntimeSnapshot error: %v", err)
	}
	providerCfg, err := snapshot.Config.ProviderByID("openai")
	if err != nil {
		t.Fatalf("ProviderByID error: %v", err)
	}
	if providerCfg.UpstreamRetryCount != 4 || providerCfg.UpstreamRetryDelay != 7*time.Second || providerCfg.UpstreamCacheControl != UpstreamCacheControl1H {
		t.Fatalf("expected provider to inherit root retry/cache control values, got count=%d delay=%s cache=%q", providerCfg.UpstreamRetryCount, providerCfg.UpstreamRetryDelay, providerCfg.UpstreamCacheControl)
	}
}

func TestRootRetryCountZeroDisablesInheritedProviderRetry(t *testing.T) {
	rootDir := t.TempDir()
	providersDir := filepath.Join(rootDir, "providers")
	if err := os.MkdirAll(providersDir, 0o700); err != nil {
		t.Fatalf("mkdir providers dir: %v", err)
	}
	rootPath := filepath.Join(rootDir, ".env")
	root := strings.Join([]string{
		"PROVIDERS_DIR=" + providersDir,
		"DEFAULT_PROVIDER=openai",
		"UPSTREAM_RETRY_COUNT=0",
		"UPSTREAM_RETRY_DELAY=0s",
		"",
	}, "\n")
	if err := os.WriteFile(rootPath, []byte(root), 0o600); err != nil {
		t.Fatalf("write root env: %v", err)
	}
	provider := strings.Join([]string{
		"PROVIDER_ID=openai",
		"PROVIDER_ENABLED=true",
		"UPSTREAM_BASE_URL=https://example.test",
		"UPSTREAM_API_KEY=test-key",
		"SUPPORTS_RESPONSES=true",
		"UPSTREAM_RETRY_COUNT=",
		"UPSTREAM_RETRY_DELAY=",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(providersDir, "openai.env"), []byte(provider), 0o600); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	snapshot, err := BuildRuntimeSnapshot(rootPath)
	if err != nil {
		t.Fatalf("BuildRuntimeSnapshot error: %v", err)
	}
	providerCfg, err := snapshot.Config.ProviderByID("openai")
	if err != nil {
		t.Fatalf("ProviderByID error: %v", err)
	}
	if providerCfg.UpstreamRetryCount != 0 || providerCfg.UpstreamRetryDelay != 0 {
		t.Fatalf("expected inherited retry disabled, got count=%d delay=%s", providerCfg.UpstreamRetryCount, providerCfg.UpstreamRetryDelay)
	}
}

func TestLoadFromEnvParsesCacheInfoTimezone(t *testing.T) {
	t.Setenv("CACHE_INFO_TIMEZONE", "UTC")

	cfg := LoadFromEnv()
	if got := cfg.CacheInfoTimezone; got != "UTC" {
		t.Fatalf("expected cache info timezone %q, got %q", "UTC", got)
	}
}

func TestLoadFromEnvNormalizesPlainPortListenAddr(t *testing.T) {
	t.Setenv("LISTEN_ADDR", "21021")

	cfg := LoadFromEnv()
	if got := cfg.ListenAddr; got != ":21021" {
		t.Fatalf("expected LISTEN_ADDR to normalize to %q, got %q", ":21021", got)
	}
}

func TestConfigCacheInfoLocationResolvesTimezone(t *testing.T) {
	cfg := Config{CacheInfoTimezone: "Asia/Shanghai"}

	location, err := cfg.CacheInfoLocation()
	if err != nil {
		t.Fatalf("expected CacheInfoLocation to resolve timezone, got error: %v", err)
	}
	if got := location.String(); got != "Asia/Shanghai" {
		t.Fatalf("expected resolved timezone %q, got %q", "Asia/Shanghai", got)
	}
}

func TestLoadFromEnvParsesTimeouts(t *testing.T) {
	t.Setenv("CONNECT_TIMEOUT", "11s")
	t.Setenv("FIRST_BYTE_TIMEOUT", "45s")
	t.Setenv("IDLE_TIMEOUT", "75s")
	t.Setenv("TOTAL_TIMEOUT", "12m")

	cfg := LoadFromEnv()
	if cfg.ConnectTimeout != 11*time.Second {
		t.Fatalf("expected ConnectTimeout 11s, got %v", cfg.ConnectTimeout)
	}
	if cfg.FirstByteTimeout != 45*time.Second {
		t.Fatalf("expected FirstByteTimeout 45s, got %v", cfg.FirstByteTimeout)
	}
	if cfg.IdleTimeout != 75*time.Second {
		t.Fatalf("expected IdleTimeout 75s, got %v", cfg.IdleTimeout)
	}
	if cfg.TotalTimeout != 12*time.Minute {
		t.Fatalf("expected TotalTimeout 12m, got %v", cfg.TotalTimeout)
	}
}

func TestLoadFromValuesAllowsExplicitlyDisablingDebugArchive(t *testing.T) {
	cfg := LoadFromValues(map[string]string{"OPENAI_COMPAT_DEBUG_ARCHIVE_DIR": ""})
	if cfg.DebugArchiveRootDir != "" {
		t.Fatalf("expected explicit empty debug archive dir to disable archive, got %q", cfg.DebugArchiveRootDir)
	}
}

func TestLoadFromValuesParsesDebugArchiveMaxRequestsIndependently(t *testing.T) {
	cfg := LoadFromValues(map[string]string{
		"LOG_MAX_REQUESTS":                         "9",
		"OPENAI_COMPAT_DEBUG_ARCHIVE_MAX_REQUESTS": "17",
	})
	if got := cfg.LogMaxRequests; got != 9 {
		t.Fatalf("expected log max requests 9, got %d", got)
	}
	if got := cfg.DebugArchiveMaxRequests; got != 17 {
		t.Fatalf("expected debug archive max requests 17, got %d", got)
	}
}

func TestValidateRootEnvValuesRejectsInvalidTimeout(t *testing.T) {
	err := ValidateRootEnvValues(map[string]string{"TOTAL_TIMEOUT": "abc"})
	if err == nil {
		t.Fatalf("expected invalid TOTAL_TIMEOUT to fail validation")
	}
}

func TestValidateRootEnvValuesAcceptsIANATimezone(t *testing.T) {
	testCases := []string{"UTC", "Asia/Shanghai"}
	for _, timezone := range testCases {
		t.Run(timezone, func(t *testing.T) {
			err := ValidateRootEnvValues(map[string]string{"CACHE_INFO_TIMEZONE": timezone})
			if err != nil {
				t.Fatalf("expected valid CACHE_INFO_TIMEZONE %q to pass validation, got %v", timezone, err)
			}
		})
	}
}

func TestValidateRootEnvValuesRejectsInvalidCacheInfoTimezone(t *testing.T) {
	err := ValidateRootEnvValues(map[string]string{"CACHE_INFO_TIMEZONE": "foo"})
	if err == nil {
		t.Fatalf("expected invalid CACHE_INFO_TIMEZONE to fail validation")
	}
}

func TestValidateRootEnvValuesRejectsInvalidDownstreamNonStreamStrategy(t *testing.T) {
	err := ValidateRootEnvValues(map[string]string{"DOWNSTREAM_NON_STREAM_STRATEGY": "bad-mode"})
	if err == nil {
		t.Fatalf("expected invalid DOWNSTREAM_NON_STREAM_STRATEGY to fail validation")
	}
}

func TestValidateRootEnvValuesRejectsInvalidLogMaxBodySizeMB(t *testing.T) {
	for _, value := range []string{"-1", "not-a-number"} {
		t.Run(value, func(t *testing.T) {
			err := ValidateRootEnvValues(map[string]string{"LOG_MAX_BODY_SIZE_MB": value})
			if err == nil {
				t.Fatalf("expected invalid LOG_MAX_BODY_SIZE_MB=%q to fail validation", value)
			}
		})
	}
}

func TestValidateRootEnvValuesRejectsInvalidDebugArchiveMaxRequests(t *testing.T) {
	for _, value := range []string{"0", "-1", "not-a-number"} {
		t.Run(value, func(t *testing.T) {
			err := ValidateRootEnvValues(map[string]string{"OPENAI_COMPAT_DEBUG_ARCHIVE_MAX_REQUESTS": value})
			if err == nil {
				t.Fatalf("expected invalid OPENAI_COMPAT_DEBUG_ARCHIVE_MAX_REQUESTS=%q to fail validation", value)
			}
		})
	}
}

func TestValidateRootEnvValuesRejectsInvalidEnableLegacyV1RoutesBoolean(t *testing.T) {
	err := ValidateRootEnvValues(map[string]string{"ENABLE_LEGACY_V1_ROUTES": "enabled"})
	if err == nil {
		t.Fatalf("expected invalid ENABLE_LEGACY_V1_ROUTES to fail validation")
	}
}

func TestValidateRootEnvValuesRejectsInvalidDefaultProviderModelTagsBoolean(t *testing.T) {
	err := ValidateRootEnvValues(map[string]string{"ENABLE_DEFAULT_PROVIDER_MODEL_TAGS": "enabled"})
	if err == nil {
		t.Fatalf("expected invalid ENABLE_DEFAULT_PROVIDER_MODEL_TAGS to fail validation")
	}
}

func TestValidateRootEnvValuesRejectsInvalidAllDefaultProviderModelTagsBoolean(t *testing.T) {
	err := ValidateRootEnvValues(map[string]string{"ENABLE_ALL_DEFAULT_PROVIDER_MODEL_TAGS": "enabled"})
	if err == nil {
		t.Fatalf("expected invalid ENABLE_ALL_DEFAULT_PROVIDER_MODEL_TAGS to fail validation")
	}
}

func TestValidateRootEnvValuesRejectsInvalidNoPromptModelSuffixBoolean(t *testing.T) {
	err := ValidateRootEnvValues(map[string]string{"ENABLE_NOPROMPT_MODEL_SUFFIX": "enabled"})
	if err == nil {
		t.Fatalf("expected invalid ENABLE_NOPROMPT_MODEL_SUFFIX to fail validation")
	}
}

func TestValidateRootEnvValuesRejectsInvalidStartupBoolValues(t *testing.T) {
	for _, key := range []string{"LOG_ENABLE"} {
		t.Run(key, func(t *testing.T) {
			err := ValidateRootEnvValues(map[string]string{key: "enabled"})
			if err == nil {
				t.Fatalf("expected invalid %s to fail validation", key)
			}
		})
	}
}

func TestValidateRootEnvValuesIgnoresUnknownLegacyVariables(t *testing.T) {
	err := ValidateRootEnvValues(map[string]string{"LOG_INCLUDE_BODIES": "enabled"})
	if err != nil {
		t.Fatalf("expected unknown legacy variable to be ignored, got %v", err)
	}
}

func TestDefaultFirstByteTimeoutIsThirtyMinutes(t *testing.T) {
	if got := Default().FirstByteTimeout; got != 30*time.Minute {
		t.Fatalf("expected default FirstByteTimeout 30m, got %v", got)
	}
}

func TestDefaultEnablesNoPromptModelSuffix(t *testing.T) {
	if !Default().EnableNoPromptModelSuffix {
		t.Fatalf("expected noprompt model suffix parsing to be enabled by default")
	}
}

func TestConfigDefaultProviderIDsParsesOrderedList(t *testing.T) {
	cfg := Config{
		ProvidersDir:    "/tmp/providers",
		DefaultProvider: "openai, azure",
		Providers: []ProviderConfig{
			{ID: "openai", Enabled: true},
			{ID: "azure", Enabled: true},
		},
	}

	ids, err := cfg.DefaultProviderIDs()
	if err != nil {
		t.Fatalf("expected DefaultProviderIDs to parse, got error: %v", err)
	}
	if want := []string{"openai", "azure"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("expected default provider ids %v, got %v", want, ids)
	}

	provider, err := cfg.DefaultProviderConfig()
	if err != nil {
		t.Fatalf("expected DefaultProviderConfig to resolve last provider, got error: %v", err)
	}
	if provider.ID != "azure" {
		t.Fatalf("expected highest-priority default provider %q, got %q", "azure", provider.ID)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected config validation to accept ordered default providers, got %v", err)
	}
}

func TestConfigDefaultProviderIDsRejectInvalidList(t *testing.T) {
	testCases := []string{
		"openai,,azure",
		"openai, azure ,openai",
	}

	for _, raw := range testCases {
		t.Run(raw, func(t *testing.T) {
			cfg := Config{DefaultProvider: raw}
			if _, err := cfg.DefaultProviderIDs(); err == nil {
				t.Fatalf("expected invalid default provider list %q to fail parsing", raw)
			}
		})
	}
}

func TestConfigValidateRejectsDisabledDefaultProviderInList(t *testing.T) {
	cfg := Config{
		ProvidersDir:         "/tmp/providers",
		DefaultProvider:      "openai,azure",
		EnableLegacyV1Routes: true,
		Providers: []ProviderConfig{
			{ID: "openai", Enabled: true},
			{ID: "azure", Enabled: false},
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected disabled provider in default provider list to fail validation")
	}
}

func TestBuildDefaultOverlayModelIndexLastWins(t *testing.T) {
	cfg := Config{
		DefaultProvider: "openai,azure",
		Providers: []ProviderConfig{
			{
				ID:      "openai",
				Enabled: true,
				ModelMap: []ModelMapEntry{
					{Key: "openai-only", Target: "gpt-openai"},
					{Key: "shared-model", Target: "gpt-shared-openai"},
				},
				ManualModels: []string{"openai-only", "shared-model", "openai-manual"},
			},
			{
				ID:      "azure",
				Enabled: true,
				ModelMap: []ModelMapEntry{
					{Key: "shared-model", Target: "gpt-shared-azure"},
					{Key: "azure-only", Target: "gpt-azure"},
				},
				ManualModels: []string{"shared-model", "azure-only", "azure-manual"},
			},
		},
	}

	ids, owners, visible, taggedOwners, taggedVisible, err := buildDefaultOverlayModelIndex(cfg)
	if err != nil {
		t.Fatalf("expected overlay model index build to succeed, got %v", err)
	}
	if want := []string{"openai", "azure"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("expected default provider ids %v, got %v", want, ids)
	}
	if got := owners["shared-model"]; got != "azure" {
		t.Fatalf("expected shared-model owner %q, got %q", "azure", got)
	}
	if want := []string{"shared-model", "azure-only", "azure-manual", "openai-only", "openai-manual"}; !reflect.DeepEqual(visible, want) {
		t.Fatalf("expected visible models %v, got %v", want, visible)
	}
	if taggedOwners["[azure]shared-model"] != "azure" || taggedOwners["[openai]shared-model"] != "openai" {
		t.Fatalf("expected tagged owners for overlapping model, got %#v", taggedOwners)
	}
	if want := []string{"[azure]shared-model", "[azure]azure-only", "[azure]azure-manual", "[openai]openai-only", "[openai]shared-model", "[openai]openai-manual"}; !reflect.DeepEqual(taggedVisible, want) {
		t.Fatalf("expected tagged visible models %v, got %v", want, taggedVisible)
	}
}

func TestRuntimeSnapshotResolveDefaultProviderIDForModelRequiresVisibleModelListMembership(t *testing.T) {
	snapshot := &RuntimeSnapshot{
		Config: Config{
			DefaultProvider: "openai,azure",
			Providers: []ProviderConfig{
				{
					ID:      "openai",
					Enabled: true,
					ModelMap: []ModelMapEntry{
						NewModelMapEntry("#re:gpt-(.*)", "openai-$1"),
					},
					ManualModels: []string{"listed-model"},
				},
				{
					ID:      "azure",
					Enabled: true,
					ModelMap: []ModelMapEntry{
						NewModelMapEntry("#re:gpt-(.*)", "azure-$1"),
					},
				},
			},
		},
		DefaultProviderIDs: []string{"openai", "azure"},
		DefaultModelOwners: map[string]string{"listed-model": "openai"},
	}

	if owner, ok := snapshot.ResolveDefaultProviderIDForModel("listed-model"); !ok || owner != "openai" {
		t.Fatalf("expected listed model to resolve openai, got owner=%q ok=%v", owner, ok)
	}
	if owner, ok := snapshot.ResolveDefaultProviderIDForModel("gpt-5"); ok || owner != "" {
		t.Fatalf("expected regex-only model outside visible list to miss default provider routing, got owner=%q ok=%v", owner, ok)
	}
}

func TestBuildDefaultOverlayModelIndexSkipsHiddenModels(t *testing.T) {
	cfg := Config{
		DefaultProvider: "openai,azure",
		Providers: []ProviderConfig{
			{
				ID:      "openai",
				Enabled: true,
				ModelMap: []ModelMapEntry{
					NewModelMapEntry("gpt-4o", "openai-gpt-4o"),
					NewModelMapEntry("openai-only", "openai-only-upstream"),
				},
				ManualModels: []string{"gpt-4o", "openai-only", "manual-openai"},
				HiddenModels: []string{"#re:gpt-4.*", "manual-openai"},
			},
			{
				ID:      "azure",
				Enabled: true,
				ModelMap: []ModelMapEntry{
					NewModelMapEntry("gpt-4o", "azure-gpt-4o"),
					NewModelMapEntry("azure-only", "azure-only-upstream"),
				},
				ManualModels: []string{"gpt-4o", "azure-only"},
			},
		},
	}

	_, owners, visible, _, _, err := buildDefaultOverlayModelIndex(cfg)
	if err != nil {
		t.Fatalf("expected overlay model index build to succeed, got %v", err)
	}
	if len(visible) != 4 || !containsString(visible, "gpt-4o") || !containsString(visible, "azure-only") || !containsString(visible, "openai-only") || !containsString(visible, "manual-openai") {
		t.Fatalf("expected hidden models removed while manual model stays visible, got %v", visible)
	}
	if got := owners["gpt-4o"]; got != "azure" {
		t.Fatalf("expected hidden regex model gpt-4o to fall through to azure, got %q", got)
	}
	if got := owners["manual-openai"]; got != "openai" {
		t.Fatalf("expected manual model to override hidden rules and stay owned by openai, got %#v", owners)
	}
	if !reflect.DeepEqual(visible[2], "manual-openai") && !containsString(visible, "manual-openai") {
		t.Fatalf("expected manual model to remain visible even when hidden models mention it, got %#v", visible)
	}
}

func TestBuildDefaultOverlayModelIndexHiddenModelsFilterReasoningSuffixVariants(t *testing.T) {
	cfg := Config{
		DefaultProvider: "openai",
		Providers: []ProviderConfig{
			{
				ID:                          "openai",
				Enabled:                     true,
				ManualModels:                []string{"reason-model"},
				EnableReasoningEffortSuffix: true,
				ExposeReasoningSuffixModels: true,
				HiddenModels:                []string{"reason-model-high", "reason-model-low"},
			},
		},
	}

	_, _, visible, _, _, err := buildDefaultOverlayModelIndex(cfg)
	if err != nil {
		t.Fatalf("expected overlay model index build to succeed, got %v", err)
	}
	if want := []string{"reason-model", "reason-model-minimal", "reason-model-xhigh", "reason-model-medium", "reason-model-none", "reason-model-max"}; !reflect.DeepEqual(visible, want) {
		t.Fatalf("expected explicitly hidden suffix variants to stay hidden while the manual model hierarchy remains visible, want %v got %v", want, visible)
	}
}

func TestRuntimeSnapshotResolveDefaultProviderSelectionStripsNoPromptBeforeReasoningSuffix(t *testing.T) {
	cfg := Config{
		DefaultProvider:           "openai",
		EnableNoPromptModelSuffix: true,
		Providers: []ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			ManualModels:                []string{"gpt-5.5"},
			EnableReasoningEffortSuffix: true,
		}},
	}
	providerIDs, owners, visible, taggedOwners, taggedVisible, err := buildDefaultOverlayModelIndex(cfg)
	if err != nil {
		t.Fatalf("build overlay: %v", err)
	}
	snapshot := &RuntimeSnapshot{Config: cfg, DefaultProviderIDs: providerIDs, DefaultModelOwners: owners, DefaultVisibleModels: visible, DefaultTaggedModelOwners: taggedOwners, DefaultTaggedVisibleModels: taggedVisible}

	providerID, model, ok := snapshot.ResolveDefaultProviderSelection("gpt-5.5-low-noprompt")
	if !ok || providerID != "openai" || model != "gpt-5.5-low" {
		t.Fatalf("expected noprompt reasoning suffix selection to resolve to openai/gpt-5.5-low, got provider=%q model=%q ok=%v", providerID, model, ok)
	}
}

func TestRuntimeSnapshotResolveDefaultProviderSelectionStripsNoPromptRegardlessOfSuffixOrder(t *testing.T) {
	cfg := Config{
		DefaultProvider:           "openai",
		EnableNoPromptModelSuffix: true,
		Providers: []ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			ManualModels:                []string{"gpt-5.5"},
			EnableReasoningEffortSuffix: true,
		}},
	}
	providerIDs, owners, visible, taggedOwners, taggedVisible, err := buildDefaultOverlayModelIndex(cfg)
	if err != nil {
		t.Fatalf("build overlay: %v", err)
	}
	snapshot := &RuntimeSnapshot{Config: cfg, DefaultProviderIDs: providerIDs, DefaultModelOwners: owners, DefaultVisibleModels: visible, DefaultTaggedModelOwners: taggedOwners, DefaultTaggedVisibleModels: taggedVisible}

	providerID, model, ok := snapshot.ResolveDefaultProviderSelection("gpt-5.5-noprompt-low")
	if !ok || providerID != "openai" || model != "gpt-5.5-low" {
		t.Fatalf("expected noprompt marker before reasoning suffix to resolve to openai/gpt-5.5-low, got provider=%q model=%q ok=%v", providerID, model, ok)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
