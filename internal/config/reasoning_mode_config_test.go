package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadFromValuesParsesReasoningModeRootDefaultsAndExclusions(t *testing.T) {
	// Given
	cfg := LoadFromValues(map[string]string{
		"ENABLE_REASONING_MODE_SUFFIX":               "false",
		"DEFAULT_PRO_REASONING_MODE_EXCLUDED_MODELS": "gpt-5.6,#re:claude-.*",
	})

	// Then
	if cfg.EnableReasoningModeSuffix {
		t.Fatal("expected root reasoning-mode suffix to be disabled")
	}
	if !Default().DefaultProReasoningMode {
		t.Fatal("expected default pro reasoning mode to be enabled")
	}
	if cfg.DefaultProReasoningModeEnabledForFinalUpstreamModel("gpt-5.6") {
		t.Fatal("expected literal exclusion to disable only the proxy default")
	}
	if cfg.DefaultProReasoningModeEnabledForFinalUpstreamModel("claude-sonnet") {
		t.Fatal("expected regex exclusion to disable only the proxy default")
	}
}

func TestProviderReasoningModeConfigurationHonorsInheritanceAndCapabilityPrecedence(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "openai.env")
	contents := "PROVIDER_ID=openai\nENABLE_REASONING_MODE_SUFFIX=\nEXPOSE_REASONING_MODE_SUFFIX_MODELS=true\nREASONING_MODE_PRO_CAPABILITY=probe\nREASONING_MODE_PRO_CAPABILITY_RULES=#re:gpt-.*:unsupported,#re:gpt-5.*:supported,gpt-5.6:unsupported\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write provider: %v", err)
	}

	// When
	provider, err := loadProviderFile(path)
	if err != nil {
		t.Fatalf("load provider: %v", err)
	}
	root := Default()
	root.EnableReasoningModeSuffix = true
	root.Providers = []ProviderConfig{provider}
	normalizeRuntimeConfigDefaults(&root)
	provider = root.Providers[0]

	// Then
	if !provider.EnableReasoningModeSuffix || !provider.ExposeReasoningModeSuffixModels {
		t.Fatalf("expected inherited suffix settings, got %#v", provider)
	}
	if got := provider.ResolveReasoningModeProCapability("gpt-5.6"); got != ReasoningModeProCapabilityUnsupported {
		t.Fatalf("expected exact rule to win, got %q", got)
	}
	if got := provider.ResolveReasoningModeProCapability("gpt-5.5"); got != ReasoningModeProCapabilitySupported {
		t.Fatalf("expected later regex rule to win, got %q", got)
	}
	if got := provider.ResolveReasoningModeProCapability("claude-sonnet"); got != ReasoningModeProCapabilityProbe {
		t.Fatalf("expected provider default capability, got %q", got)
	}
	provider.EnableReasoningModeSuffixSet = true
	provider.EnableReasoningModeSuffix = false
	provider.ManualModels = []string{"gpt-5.6"}
	if _, ok := provider.ParseProxyModelIntent("gpt-5.6-pro", false); ok {
		t.Fatal("expected provider false to keep -pro as a literal model suffix")
	}
}

func TestReasoningModeConfigurationRejectsInvalidValues(t *testing.T) {
	for name, values := range map[string]map[string]string{
		"root enum":  {"DEFAULT_PRO_REASONING_MODE": "enabled"},
		"root regex": {"DEFAULT_PRO_REASONING_MODE_EXCLUDED_MODELS": "#re:["},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateRootEnvValues(values); err == nil {
				t.Fatalf("expected invalid %s to fail", name)
			}
		})
	}

	for name, value := range map[string]string{
		"provider enum":  "REASONING_MODE_PRO_CAPABILITY=unknown",
		"provider regex": "REASONING_MODE_PRO_CAPABILITY_RULES=#re:[:supported",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "openai.env")
			contents := "PROVIDER_ID=openai\n" + value + "\n"
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatalf("write provider: %v", err)
			}
			if _, err := loadProviderFile(path); err == nil {
				t.Fatalf("expected invalid %s to fail", name)
			}
		})
	}
}

func TestProviderToolControlCapabilitiesDefaultsAndOverrides(t *testing.T) {
	for name, contents := range map[string]string{
		"defaults": "PROVIDER_ID=openai\n",
		"enabled":  "PROVIDER_ID=openai\nSUPPORTS_PROGRAMMATIC_TOOL_CALLING=true\nSUPPORTS_RESPONSES_MULTI_AGENT=true\nSUPPORTS_PARALLEL_TOOL_CALLS_CONTROL=true\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "openai.env")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatalf("write provider: %v", err)
			}
			provider, err := loadProviderFile(path)
			if err != nil {
				t.Fatalf("load provider: %v", err)
			}
			wantExplicitControls := name == "enabled"
			if provider.SupportsProgrammaticToolCalling != wantExplicitControls || provider.SupportsParallelToolCallsControl != wantExplicitControls || !provider.SupportsResponsesMultiAgent {
				t.Fatalf("unexpected tool-control capabilities: %#v", provider)
			}
		})
	}
}

func TestUltraMultiAgentConfigurationDefaultsInheritanceAndOptOut(t *testing.T) {
	rootDir := t.TempDir()
	providersDir := filepath.Join(rootDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers: %v", err)
	}
	rootEnvPath := filepath.Join(rootDir, ".env")
	writeConfigFileWithMTime(t, rootEnvPath, "PROVIDERS_DIR="+providersDir+"\nDEFAULT_PROVIDER=inherit\nULTRA_MAX_CONCURRENT_SUBAGENTS=7\n", time.Now())
	writeConfigFileWithMTime(t, filepath.Join(providersDir, "inherit.env"), "PROVIDER_ID=inherit\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL=https://inherit.example\n", time.Now())
	writeConfigFileWithMTime(t, filepath.Join(providersDir, "override.env"), "PROVIDER_ID=override\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL=https://override.example\nULTRA_MAX_CONCURRENT_SUBAGENTS=3\n", time.Now())
	writeConfigFileWithMTime(t, filepath.Join(providersDir, "optout.env"), "PROVIDER_ID=optout\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL=https://optout.example\nSUPPORTS_RESPONSES_MULTI_AGENT=false\n", time.Now())

	snapshot, err := BuildRuntimeSnapshot(rootEnvPath)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	if snapshot.Config.UltraMaxConcurrentSubagents != 7 {
		t.Fatalf("expected root concurrency 7, got %d", snapshot.Config.UltraMaxConcurrentSubagents)
	}
	for providerID, wantConcurrency := range map[string]int{"inherit": 7, "override": 3, "optout": 7} {
		provider, err := snapshot.Config.ProviderByID(providerID)
		if err != nil {
			t.Fatalf("provider %s: %v", providerID, err)
		}
		if provider.UltraMaxConcurrentSubagents != wantConcurrency {
			t.Fatalf("provider %s concurrency=%d, want %d", providerID, provider.UltraMaxConcurrentSubagents, wantConcurrency)
		}
	}
	inherit, _ := snapshot.Config.ProviderByID("inherit")
	if !inherit.SupportsResponsesMultiAgent {
		t.Fatal("expected omitted multi-agent capability to default to true")
	}
	optout, _ := snapshot.Config.ProviderByID("optout")
	if optout.SupportsResponsesMultiAgent {
		t.Fatal("expected explicit multi-agent opt-out")
	}
}

func TestUltraMultiAgentConfigurationRejectsInvalidConcurrency(t *testing.T) {
	for name, contents := range map[string]string{
		"root zero":        "ULTRA_MAX_CONCURRENT_SUBAGENTS=0",
		"root negative":    "ULTRA_MAX_CONCURRENT_SUBAGENTS=-1",
		"provider zero":    "PROVIDER_ID=openai\nULTRA_MAX_CONCURRENT_SUBAGENTS=0\n",
		"provider invalid": "PROVIDER_ID=openai\nULTRA_MAX_CONCURRENT_SUBAGENTS=fast\n",
	} {
		t.Run(name, func(t *testing.T) {
			if strings.HasPrefix(name, "root") {
				if err := ValidateRootEnvValues(map[string]string{"ULTRA_MAX_CONCURRENT_SUBAGENTS": strings.TrimPrefix(contents, "ULTRA_MAX_CONCURRENT_SUBAGENTS=")}); err == nil {
					t.Fatal("expected invalid root concurrency")
				}
				return
			}
			path := filepath.Join(t.TempDir(), "openai.env")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatalf("write provider: %v", err)
			}
			if _, err := loadProviderFile(path); err == nil {
				t.Fatal("expected invalid provider concurrency")
			}
		})
	}
}

func TestProviderProxyModelIntentAlwaysRecognizesUltraSuffix(t *testing.T) {
	provider := ProviderConfig{ManualModels: []string{"gpt-5.6"}}
	intent, ok := provider.ParseProxyModelIntent("gpt-5.6-ultra", false)
	if !ok || !intent.HasUltra || intent.BaseModel != "gpt-5.6" {
		t.Fatalf("expected ultra intent, got %#v, ok=%v", intent, ok)
	}
}

func TestRuntimeStoreRefreshPreservesActiveSnapshotForInvalidReasoningModeConfiguration(t *testing.T) {
	// Given
	rootDir := t.TempDir()
	providersDir := filepath.Join(rootDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers: %v", err)
	}
	rootEnvPath := filepath.Join(rootDir, ".env")
	providerPath := filepath.Join(providersDir, "openai.env")
	initial := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	writeConfigFileWithMTime(t, rootEnvPath, "PROVIDERS_DIR="+providersDir+"\nDEFAULT_PROVIDER=openai\nENABLE_REASONING_MODE_SUFFIX=true\n", initial)
	writeConfigFileWithMTime(t, providerPath, "PROVIDER_ID=openai\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL=https://example.test\nREASONING_MODE_PRO_CAPABILITY=probe\n", initial)
	store, err := NewRuntimeStore(rootEnvPath)
	if err != nil {
		t.Fatalf("new runtime store: %v", err)
	}

	// When
	writeConfigFileWithMTime(t, rootEnvPath, "PROVIDERS_DIR="+providersDir+"\nDEFAULT_PROVIDER=openai\nDEFAULT_PRO_REASONING_MODE_EXCLUDED_MODELS=#re:[\n", initial.Add(time.Minute))
	if err := store.Refresh(); err == nil {
		t.Fatal("expected invalid hot-reloadable root pattern to fail refresh")
	}
	if !store.Active().Config.DefaultProReasoningMode {
		t.Fatal("expected active snapshot to retain the prior root default")
	}
	writeConfigFileWithMTime(t, providerPath, "PROVIDER_ID=openai\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL=https://example.test\nREASONING_MODE_PRO_CAPABILITY=invalid\n", initial.Add(time.Minute))
	err = store.Refresh()

	// Then
	if err == nil {
		t.Fatal("expected invalid hot-reloadable provider value to fail refresh")
	}
	active := store.Active()
	provider, err := active.Config.ProviderByID("openai")
	if err != nil {
		t.Fatalf("provider by id: %v", err)
	}
	if got := provider.ResolveReasoningModeProCapability("gpt-5.6"); got != ReasoningModeProCapabilityProbe {
		t.Fatalf("expected active snapshot to retain probe capability, got %q", got)
	}
}
