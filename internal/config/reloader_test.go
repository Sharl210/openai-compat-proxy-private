package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildRuntimeSnapshotCapturesSourceFileModTimes(t *testing.T) {
	rootDir := t.TempDir()
	providersDir := filepath.Join(rootDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers dir: %v", err)
	}

	rootEnvPath := filepath.Join(rootDir, ".env")
	providerEnvPath := filepath.Join(providersDir, "openai.env")
	rootMTime := time.Date(2026, 3, 25, 9, 10, 11, 123000000, time.UTC)
	providerMTime := time.Date(2026, 3, 25, 9, 20, 21, 456000000, time.UTC)

	writeConfigFileWithMTime(t, rootEnvPath, "PROVIDERS_DIR="+providersDir+"\nDEFAULT_PROVIDER=openai\n", rootMTime)
	writeConfigFileWithMTime(t, providerEnvPath, "PROVIDER_ID=openai\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL=https://example.test\nUPSTREAM_API_KEY=test-key\nSUPPORTS_RESPONSES=true\n", providerMTime)

	snapshot, err := BuildRuntimeSnapshot(rootEnvPath)
	if err != nil {
		t.Fatalf("BuildRuntimeSnapshot returned error: %v", err)
	}

	if got := snapshot.RootEnvVersion; got != formatVersionTime(rootMTime) {
		t.Fatalf("expected root env version %q, got %q", formatVersionTime(rootMTime), got)
	}
	if got := snapshot.ProviderVersionByID["openai"]; got != formatVersionTime(providerMTime) {
		t.Fatalf("expected provider version %q, got %q", formatVersionTime(providerMTime), got)
	}
}

func TestRuntimeStoreRefreshKeepsLastGoodSnapshotOnInvalidProviderConfig(t *testing.T) {
	rootDir := t.TempDir()
	providersDir := filepath.Join(rootDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers dir: %v", err)
	}

	rootEnvPath := filepath.Join(rootDir, ".env")
	providerEnvPath := filepath.Join(providersDir, "openai.env")
	initialRootMTime := time.Date(2026, 3, 25, 10, 0, 0, 111000000, time.UTC)
	initialProviderMTime := time.Date(2026, 3, 25, 10, 1, 0, 222000000, time.UTC)

	writeConfigFileWithMTime(t, rootEnvPath, "PROVIDERS_DIR="+providersDir+"\nDEFAULT_PROVIDER=openai\n", initialRootMTime)
	writeConfigFileWithMTime(t, providerEnvPath, "PROVIDER_ID=openai\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL=https://example.test\nUPSTREAM_API_KEY=good-key\nSUPPORTS_RESPONSES=true\n", initialProviderMTime)

	store, err := NewRuntimeStore(rootEnvPath)
	if err != nil {
		t.Fatalf("NewRuntimeStore returned error: %v", err)
	}

	brokenProviderMTime := time.Date(2026, 3, 25, 10, 2, 0, 333000000, time.UTC)
	writeConfigFileWithMTime(t, providerEnvPath, "PROVIDER_ID=openai\nMODEL_MAP_JSON={broken\n", brokenProviderMTime)

	if err := store.Refresh(); err == nil {
		t.Fatalf("expected Refresh to fail for broken provider config")
	}

	active := store.Active()
	if got := active.RootEnvVersion; got != formatVersionTime(initialRootMTime) {
		t.Fatalf("expected root version to remain %q, got %q", formatVersionTime(initialRootMTime), got)
	}
	if got := active.ProviderVersionByID["openai"]; got != formatVersionTime(initialProviderMTime) {
		t.Fatalf("expected provider version to remain %q, got %q", formatVersionTime(initialProviderMTime), got)
	}
	if got := active.Config.Providers[0].UpstreamAPIKey; got != "good-key" {
		t.Fatalf("expected active provider key to remain last good value, got %q", got)
	}
}

func TestRuntimeStoreRefreshIgnoresStartupOnlyRootConfigChanges(t *testing.T) {
	rootDir := t.TempDir()
	providersDir := filepath.Join(rootDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers dir: %v", err)
	}

	rootEnvPath := filepath.Join(rootDir, ".env")
	providerEnvPath := filepath.Join(providersDir, "openai.env")
	initialRootMTime := time.Date(2026, 3, 25, 10, 5, 0, 111000000, time.UTC)
	startupOnlyMTime := time.Date(2026, 3, 25, 10, 6, 0, 222000000, time.UTC)

	writeConfigFileWithMTime(t, rootEnvPath, "LISTEN_ADDR=:21021\nLOG_ENABLE=false\nPROVIDERS_DIR="+providersDir+"\nDEFAULT_PROVIDER=openai\nENABLE_LEGACY_V1_ROUTES=true\nPROXY_API_KEY=before\n", initialRootMTime)
	writeConfigFileWithMTime(t, providerEnvPath, "PROVIDER_ID=openai\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL=https://example.test\nUPSTREAM_API_KEY=test-key\nSUPPORTS_RESPONSES=true\n", time.Date(2026, 3, 25, 10, 5, 30, 0, time.UTC))

	store, err := NewRuntimeStore(rootEnvPath)
	if err != nil {
		t.Fatalf("NewRuntimeStore returned error: %v", err)
	}

	writeConfigFileWithMTime(t, rootEnvPath, "LISTEN_ADDR=:29999\nLOG_ENABLE=true\nPROVIDERS_DIR="+providersDir+"\nDEFAULT_PROVIDER=openai\nENABLE_LEGACY_V1_ROUTES=true\nPROXY_API_KEY=before\n", startupOnlyMTime)
	if err := store.Refresh(); err != nil {
		t.Fatalf("expected startup-only change refresh to succeed, got %v", err)
	}

	active := store.Active()
	if got := active.RootEnvVersion; got != formatVersionTime(initialRootMTime) {
		t.Fatalf("expected root version to stay %q, got %q", formatVersionTime(initialRootMTime), got)
	}
	if got := active.Config.ListenAddr; got != ":21021" {
		t.Fatalf("expected listen addr to stay startup value, got %q", got)
	}
	if got := active.Config.LogEnable; got {
		t.Fatalf("expected log enable to stay false, got true")
	}
	if got := active.Config.ProxyAPIKey; got != "before" {
		t.Fatalf("expected hot root value to stay loaded, got %q", got)
	}
}

func TestRuntimeStoreWatcherReloadsAfterProviderFileChange(t *testing.T) {
	rootDir := t.TempDir()
	providersDir := filepath.Join(rootDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers dir: %v", err)
	}

	rootEnvPath := filepath.Join(rootDir, ".env")
	providerEnvPath := filepath.Join(providersDir, "openai.env")
	writeConfigFileWithMTime(t, rootEnvPath, "PROVIDERS_DIR="+providersDir+"\nDEFAULT_PROVIDER=openai\n", time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC))
	writeConfigFileWithMTime(t, providerEnvPath, "PROVIDER_ID=openai\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL=https://before.test\nUPSTREAM_API_KEY=before-key\nSUPPORTS_RESPONSES=true\n", time.Date(2026, 3, 25, 12, 1, 0, 0, time.UTC))

	store, err := NewRuntimeStore(rootEnvPath)
	if err != nil {
		t.Fatalf("NewRuntimeStore returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := store.StartWatching(ctx, 200*time.Millisecond, 5*time.Second); err != nil {
		t.Fatalf("StartWatching returned error: %v", err)
	}

	targetMTime := time.Date(2026, 3, 25, 12, 2, 0, 0, time.UTC)
	writeConfigFileWithMTime(t, providerEnvPath, "PROVIDER_ID=openai\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL=https://after.test\nUPSTREAM_API_KEY=after-key\nSUPPORTS_RESPONSES=true\n", targetMTime)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		active := store.Active()
		if active != nil && active.ProviderVersionByID["openai"] == formatVersionTime(targetMTime) && active.Config.Providers[0].UpstreamAPIKey == "after-key" {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	active := store.Active()
	t.Fatalf("expected watcher to promote updated provider config, got version=%q key=%q", active.ProviderVersionByID["openai"], active.Config.Providers[0].UpstreamAPIKey)
}

func TestRuntimeStoreWatcherKeepsLastGoodSnapshotOnBrokenThenRecovers(t *testing.T) {
	rootDir := t.TempDir()
	providersDir := filepath.Join(rootDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers dir: %v", err)
	}

	rootEnvPath := filepath.Join(rootDir, ".env")
	providerEnvPath := filepath.Join(providersDir, "openai.env")
	initialMTime := time.Date(2026, 3, 25, 12, 10, 0, 0, time.UTC)
	writeConfigFileWithMTime(t, rootEnvPath, "PROVIDERS_DIR="+providersDir+"\nDEFAULT_PROVIDER=openai\n", time.Date(2026, 3, 25, 12, 9, 0, 0, time.UTC))
	writeConfigFileWithMTime(t, providerEnvPath, "PROVIDER_ID=openai\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL=https://stable.test\nUPSTREAM_API_KEY=stable-key\nSUPPORTS_RESPONSES=true\n", initialMTime)

	store, err := NewRuntimeStore(rootEnvPath)
	if err != nil {
		t.Fatalf("NewRuntimeStore returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := store.StartWatching(ctx, 200*time.Millisecond, 5*time.Second); err != nil {
		t.Fatalf("StartWatching returned error: %v", err)
	}

	writeConfigFileWithMTime(t, providerEnvPath, "PROVIDER_ID=openai\nMODEL_MAP_JSON={broken\n", time.Date(2026, 3, 25, 12, 11, 0, 0, time.UTC))
	time.Sleep(600 * time.Millisecond)
	if got := store.Active().ProviderVersionByID["openai"]; got != formatVersionTime(initialMTime) {
		t.Fatalf("expected broken update to keep old version %q, got %q", formatVersionTime(initialMTime), got)
	}
	if got := store.Active().Config.Providers[0].UpstreamAPIKey; got != "stable-key" {
		t.Fatalf("expected broken update to keep old key, got %q", got)
	}

	recoveredMTime := time.Date(2026, 3, 25, 12, 12, 0, 0, time.UTC)
	writeConfigFileWithMTime(t, providerEnvPath, "PROVIDER_ID=openai\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL=https://stable.test\nUPSTREAM_API_KEY=recovered-key\nSUPPORTS_RESPONSES=true\n", recoveredMTime)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		active := store.Active()
		if active != nil && active.ProviderVersionByID["openai"] == formatVersionTime(recoveredMTime) && active.Config.Providers[0].UpstreamAPIKey == "recovered-key" {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	active := store.Active()
	t.Fatalf("expected watcher to recover after valid rewrite, got version=%q key=%q", active.ProviderVersionByID["openai"], active.Config.Providers[0].UpstreamAPIKey)
}

func writeConfigFileWithMTime(t *testing.T, path string, content string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}
