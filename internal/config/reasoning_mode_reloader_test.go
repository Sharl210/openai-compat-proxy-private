package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeStoreRefreshAppliesRootReasoningModeSuffixToInheritingProvider(t *testing.T) {
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
	writeConfigFileWithMTime(t, providerPath, "PROVIDER_ID=openai\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL=https://example.test\nENABLE_REASONING_MODE_SUFFIX=\n", initial)
	store, err := NewRuntimeStore(rootEnvPath)
	if err != nil {
		t.Fatalf("new runtime store: %v", err)
	}

	// When
	updated := initial.Add(time.Minute)
	writeConfigFileWithMTime(t, rootEnvPath, "PROVIDERS_DIR="+providersDir+"\nDEFAULT_PROVIDER=openai\nENABLE_REASONING_MODE_SUFFIX=false\n", updated)
	if err := store.Refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	// Then
	active := store.Active()
	provider, err := active.Config.ProviderByID("openai")
	if err != nil {
		t.Fatalf("provider by id: %v", err)
	}
	if active.RootEnvVersion != formatVersionTime(updated) || active.Config.EnableReasoningModeSuffix || provider.EnableReasoningModeSuffix {
		t.Fatalf("expected refreshed root suffix setting to reach inheriting provider, got %#v", active.Config)
	}
}
