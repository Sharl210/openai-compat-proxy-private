package config

import (
	"reflect"
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

func TestDefaultFirstByteTimeoutIsTwentyMinutes(t *testing.T) {
	if got := Default().FirstByteTimeout; got != 20*time.Minute {
		t.Fatalf("expected default FirstByteTimeout 20m, got %v", got)
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
				ManualModels: []string{"openai-manual"},
			},
			{
				ID:      "azure",
				Enabled: true,
				ModelMap: []ModelMapEntry{
					{Key: "shared-model", Target: "gpt-shared-azure"},
					{Key: "azure-only", Target: "gpt-azure"},
				},
				ManualModels: []string{"azure-manual"},
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
						NewModelMapEntry("gpt-*", "openai-$1"),
					},
					ManualModels: []string{"listed-model"},
				},
				{
					ID:      "azure",
					Enabled: true,
					ModelMap: []ModelMapEntry{
						NewModelMapEntry("gpt-*", "azure-$1"),
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
		t.Fatalf("expected wildcard-only model outside visible list to miss default provider routing, got owner=%q ok=%v", owner, ok)
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
				ManualModels: []string{"manual-openai"},
				HiddenModels: []string{"gpt-4*", "manual-openai"},
			},
			{
				ID:      "azure",
				Enabled: true,
				ModelMap: []ModelMapEntry{
					NewModelMapEntry("gpt-4o", "azure-gpt-4o"),
					NewModelMapEntry("azure-only", "azure-only-upstream"),
				},
			},
		},
	}

	_, owners, visible, _, _, err := buildDefaultOverlayModelIndex(cfg)
	if err != nil {
		t.Fatalf("expected overlay model index build to succeed, got %v", err)
	}
	if want := []string{"gpt-4o", "azure-only", "openai-only"}; !reflect.DeepEqual(visible, want) {
		t.Fatalf("expected hidden models removed from visible list and gpt-4o to fall through to azure, want %v got %v", want, visible)
	}
	if got := owners["gpt-4o"]; got != "azure" {
		t.Fatalf("expected hidden wildcard model gpt-4o to fall through to azure, got %q", got)
	}
	if _, ok := owners["manual-openai"]; ok {
		t.Fatalf("expected hidden manual model to be absent from overlay owners, got %#v", owners)
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
	if want := []string{"reason-model", "reason-model-xhigh", "reason-model-medium"}; !reflect.DeepEqual(visible, want) {
		t.Fatalf("expected hidden suffix variants to be filtered from visible list, want %v got %v", want, visible)
	}
}
