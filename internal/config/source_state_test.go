package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildRuntimeSnapshotFromValuesUsesPreReadRootStamp(t *testing.T) {
	rootDir := t.TempDir()
	providersDir := filepath.Join(rootDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers dir: %v", err)
	}

	rootEnvPath := filepath.Join(rootDir, ".env")
	providerEnvPath := filepath.Join(providersDir, "openai.env")
	preReadMTime := time.Date(2026, 7, 14, 11, 0, 0, 0, time.UTC)
	postReadMTime := time.Date(2026, 7, 14, 11, 1, 0, 0, time.UTC)
	rootBody := "PROVIDERS_DIR=" + providersDir + "\nDEFAULT_PROVIDER=openai\n"
	writeConfigFileWithMTime(t, rootEnvPath, rootBody, preReadMTime)
	writeConfigFileWithMTime(t, providerEnvPath, "PROVIDER_ID=openai\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL=https://example.test\nUPSTREAM_API_KEY=test-key\nSUPPORTS_RESPONSES=true\n", time.Date(2026, 7, 14, 11, 0, 30, 0, time.UTC))

	preReadInfo, err := os.Stat(rootEnvPath)
	if err != nil {
		t.Fatalf("stat root env: %v", err)
	}
	values, err := parseEnvFile(rootEnvPath)
	if err != nil {
		t.Fatalf("parseEnvFile returned error: %v", err)
	}
	writeConfigFileWithMTime(t, rootEnvPath, rootBody, postReadMTime)

	snapshot, err := buildRuntimeSnapshotFromValues(rootEnvPath, preReadInfo, values)
	if err != nil {
		t.Fatalf("buildRuntimeSnapshotFromValues returned error: %v", err)
	}
	stamp := snapshot.sourceState.files[filepath.Clean(rootEnvPath)]
	if !stamp.exists || stamp.mode != preReadInfo.Mode() || stamp.size != preReadInfo.Size() || !stamp.modTime.Equal(preReadInfo.ModTime()) {
		t.Fatalf("expected source state to keep pre-read root stamp mode=%v size=%d mtime=%v, got %+v", preReadInfo.Mode(), preReadInfo.Size(), preReadInfo.ModTime(), stamp)
	}
}

func TestBuildRuntimeSnapshotFromValuesPreservesRootStampWhenPromptOverlapsRootEnv(t *testing.T) {
	rootDir := t.TempDir()
	providersDir := filepath.Join(rootDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers dir: %v", err)
	}

	rootEnvPath := filepath.Join(rootDir, ".env")
	providerEnvPath := filepath.Join(providersDir, "openai.env")
	preReadMTime := time.Date(2026, 7, 14, 11, 3, 0, 0, time.UTC)
	postReadMTime := time.Date(2026, 7, 14, 11, 4, 0, 0, time.UTC)
	rootBody := "PROVIDERS_DIR=" + providersDir + "\nDEFAULT_PROVIDER=openai\n"
	writeConfigFileWithMTime(t, rootEnvPath, rootBody, preReadMTime)
	writeConfigFileWithMTime(t, providerEnvPath, "PROVIDER_ID=openai\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL=https://example.test\nUPSTREAM_API_KEY=test-key\nSUPPORTS_RESPONSES=true\nSYSTEM_PROMPT_FILES=../.env\n", time.Date(2026, 7, 14, 11, 3, 30, 0, time.UTC))

	preReadInfo, err := os.Stat(rootEnvPath)
	if err != nil {
		t.Fatalf("stat root env: %v", err)
	}
	values, err := parseEnvFile(rootEnvPath)
	if err != nil {
		t.Fatalf("parseEnvFile returned error: %v", err)
	}
	writeConfigFileWithMTime(t, rootEnvPath, rootBody, postReadMTime)

	snapshot, err := buildRuntimeSnapshotFromValues(rootEnvPath, preReadInfo, values)
	if err != nil {
		t.Fatalf("buildRuntimeSnapshotFromValues returned error: %v", err)
	}
	if got := snapshot.sourceState.files[filepath.Clean(rootEnvPath)]; !got.modTime.Equal(preReadMTime) {
		t.Fatalf("expected overlapping prompt path to retain pre-read root mtime %v, got %+v", preReadMTime, got)
	}
}

func TestLoadSystemPromptTextPreservesEarliestSourceStamps(t *testing.T) {
	rootDir := t.TempDir()
	promptPath := filepath.Join(rootDir, "prompt.md")
	promptMTime := time.Date(2026, 7, 14, 11, 2, 0, 0, time.UTC)
	writeConfigFileWithMTime(t, promptPath, "prompt body\n", promptMTime)

	cleanPromptPath := filepath.Clean(promptPath)
	cleanDirPath := filepath.Clean(rootDir)
	earliestFileStamp := runtimeSourceStamp{exists: true, mode: 0o600, size: 17, modTime: time.Date(2026, 7, 14, 11, 0, 0, 0, time.UTC)}
	earliestDirStamp := runtimeSourceStamp{exists: true, mode: os.ModeDir | 0o700, size: 23, modTime: time.Date(2026, 7, 14, 11, 0, 30, 0, time.UTC)}
	state := runtimeSourceState{
		files: map[string]runtimeSourceStamp{cleanPromptPath: earliestFileStamp},
		dirs:  map[string]runtimeSourceStamp{cleanDirPath: earliestDirStamp},
	}

	text, latest, err := loadSystemPromptText([]string{promptPath}, time.Time{}, &state)
	if err != nil {
		t.Fatalf("loadSystemPromptText returned error: %v", err)
	}
	if text != "prompt body" || !latest.Equal(promptMTime) {
		t.Fatalf("expected prompt content and current version metadata, got text=%q latest=%v", text, latest)
	}
	if got := state.files[cleanPromptPath]; got != earliestFileStamp {
		t.Fatalf("expected earliest file stamp to remain, got %+v", got)
	}
	if got := state.dirs[cleanDirPath]; got != earliestDirStamp {
		t.Fatalf("expected earliest directory stamp to remain, got %+v", got)
	}
}

func TestCaptureRuntimeSourceStampsIntoPreservesEarliestStamp(t *testing.T) {
	rootDir := t.TempDir()
	path := filepath.Join(rootDir, "source.env")
	writeConfigFileWithMTime(t, path, "KEY=value\n", time.Date(2026, 7, 14, 11, 6, 0, 0, time.UTC))

	cleanPath := filepath.Clean(path)
	earliest := runtimeSourceStamp{exists: true, mode: 0o600, size: 7, modTime: time.Date(2026, 7, 14, 11, 5, 0, 0, time.UTC)}
	stamps := map[string]runtimeSourceStamp{cleanPath: earliest}
	if err := captureRuntimeSourceStampsInto(stamps, []string{path}); err != nil {
		t.Fatalf("captureRuntimeSourceStampsInto returned error: %v", err)
	}
	if got := stamps[cleanPath]; got != earliest {
		t.Fatalf("expected duplicate path capture to preserve earliest stamp, got %+v", got)
	}
}

func TestRuntimeStoreWatcherSkipsResyncWhenSourcesUnchanged(t *testing.T) {
	rootDir := t.TempDir()
	providersDir := filepath.Join(rootDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers dir: %v", err)
	}

	rootEnvPath := filepath.Join(rootDir, ".env")
	providerEnvPath := filepath.Join(providersDir, "openai.env")
	writeConfigFileWithMTime(t, rootEnvPath, "PROVIDERS_DIR="+providersDir+"\nDEFAULT_PROVIDER=openai\n", time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC))
	writeConfigFileWithMTime(t, providerEnvPath, "PROVIDER_ID=openai\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL=https://example.test\nUPSTREAM_API_KEY=test-key\nSUPPORTS_RESPONSES=true\n", time.Date(2026, 7, 14, 10, 1, 0, 0, time.UTC))

	store, err := NewRuntimeStore(rootEnvPath)
	if err != nil {
		t.Fatalf("NewRuntimeStore returned error: %v", err)
	}
	initial := store.Active()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := store.StartWatching(ctx, 200*time.Millisecond, 25*time.Millisecond); err != nil {
		t.Fatalf("StartWatching returned error: %v", err)
	}

	time.Sleep(250 * time.Millisecond)
	if store.Active() != initial {
		t.Fatal("expected unchanged resync tick to preserve active snapshot pointer")
	}
}

func TestRuntimeStoreWatcherRefreshesWhenSourceComparisonFails(t *testing.T) {
	rootDir := t.TempDir()
	providersDir := filepath.Join(rootDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers dir: %v", err)
	}

	rootEnvPath := filepath.Join(rootDir, ".env")
	providerEnvPath := filepath.Join(providersDir, "openai.env")
	writeConfigFileWithMTime(t, rootEnvPath, "PROVIDERS_DIR="+providersDir+"\nDEFAULT_PROVIDER=openai\n", time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC))
	writeConfigFileWithMTime(t, providerEnvPath, "PROVIDER_ID=openai\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL=https://example.test\nUPSTREAM_API_KEY=before-key\nSUPPORTS_RESPONSES=true\n", time.Date(2026, 7, 14, 12, 0, 30, 0, time.UTC))

	store, err := NewRuntimeStore(rootEnvPath)
	if err != nil {
		t.Fatalf("NewRuntimeStore returned error: %v", err)
	}
	writeConfigFileWithMTime(t, providerEnvPath, "PROVIDER_ID=openai\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL=https://example.test\nUPSTREAM_API_KEY=after-key\nSUPPORTS_RESPONSES=true\n", time.Date(2026, 7, 14, 12, 1, 0, 0, time.UTC))

	loopPath := filepath.Join(rootDir, "source-loop")
	if err := os.Symlink(filepath.Base(loopPath), loopPath); err != nil {
		t.Fatalf("create source symlink loop: %v", err)
	}
	store.Active().RootEnvPath = loopPath

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := store.StartWatching(ctx, 200*time.Millisecond, 25*time.Millisecond); err != nil {
		t.Fatalf("StartWatching returned error: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := store.Active().Config.Providers[0].UpstreamAPIKey; got == "after-key" {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("expected resync stat error to trigger conservative refresh, got key %q", store.Active().Config.Providers[0].UpstreamAPIKey)
}
