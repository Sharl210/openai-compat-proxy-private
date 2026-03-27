package cacheinfo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func expectedCacheInfoDir(root string) string {
	return filepath.Join(root, "SYSTEM_JSON_FILES", "Cache_Info")
}

func expectedCacheInfoJSONPath(root, providerID string) string {
	return filepath.Join(expectedCacheInfoDir(root), providerID+".json")
}

func expectedCacheInfoTXTPath(root, providerID string) string {
	return filepath.Join(expectedCacheInfoDir(root), providerID+".txt")
}

func TestEnsureCacheInfoDir(t *testing.T) {
	tmp := t.TempDir()

	if err := EnsureCacheInfoDir(tmp); err != nil {
		t.Fatalf("EnsureCacheInfoDir() error: %v", err)
	}

	info, err := os.Stat(filepath.Join(tmp, "SYSTEM_JSON_FILES"))
	if err != nil {
		t.Fatalf("SYSTEM_JSON_FILES directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("SYSTEM_JSON_FILES is not a directory")
	}
	jsonDir := expectedCacheInfoDir(tmp)
	info, err = os.Stat(jsonDir)
	if err != nil {
		t.Fatalf("Cache_Info not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("Cache_Info is not a directory")
	}

	// calling again should be idempotent
	if err := EnsureCacheInfoDir(tmp); err != nil {
		t.Fatalf("EnsureCacheInfoDir() second call error: %v", err)
	}
}

func TestLoadProviderStats_Missing(t *testing.T) {
	tmp := t.TempDir()
	if err := EnsureCacheInfoDir(tmp); err != nil {
		t.Fatal(err)
	}

	stats, err := LoadProviderStats(tmp, "openai")
	if err != nil {
		t.Fatalf("LoadProviderStats() error: %v", err)
	}
	if stats != nil {
		t.Fatalf("expected nil stats for missing file, got %+v", stats)
	}
}

func TestLoadProviderStats_NormalRecovery(t *testing.T) {
	tmp := t.TempDir()
	if err := EnsureCacheInfoDir(tmp); err != nil {
		t.Fatal(err)
	}

	loc := time.FixedZone("CST", 8*3600)
	original := ProviderStats{
		Timezone:      "Asia/Shanghai",
		TodayDate:     "2026-03-27",
		YesterdayDate: "2026-03-26",
		Today: TokenTotals{
			InputTokens:  100,
			CachedTokens: 40,
			OutputTokens: 50,
			TotalTokens:  150,
		},
		Yesterday: TokenTotals{
			InputTokens:  200,
			CachedTokens: 80,
			OutputTokens: 100,
			TotalTokens:  300,
		},
		HistoryTotal: TokenTotals{
			InputTokens:  300,
			CachedTokens: 120,
			OutputTokens: 150,
			TotalTokens:  450,
		},
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, loc),
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	path := expectedCacheInfoJSONPath(tmp, "openai")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	stats, err := LoadProviderStats(tmp, "openai")
	if err != nil {
		t.Fatalf("LoadProviderStats() error: %v", err)
	}
	if stats == nil {
		t.Fatal("expected non-nil stats")
	}
	if stats.Today.InputTokens != 100 {
		t.Errorf("Today.InputTokens = %d, want 100", stats.Today.InputTokens)
	}
	if stats.HistoryTotal.TotalTokens != 450 {
		t.Errorf("HistoryTotal.TotalTokens = %d, want 450", stats.HistoryTotal.TotalTokens)
	}
}

func TestLoadProviderStats_LegacyFallback(t *testing.T) {
	tmp := t.TempDir()
	if err := EnsureCacheInfoDir(tmp); err != nil {
		t.Fatal(err)
	}

	original := ProviderStats{
		Timezone:      "Asia/Shanghai",
		TodayDate:     "2026-03-27",
		YesterdayDate: "2026-03-26",
		Today:         TokenTotals{InputTokens: 123, TotalTokens: 123},
		HistoryTotal:  TokenTotals{InputTokens: 456, TotalTokens: 456},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(tmp, "Cache_Info", "openai.json")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	stats, err := LoadProviderStats(tmp, "openai")
	if err != nil {
		t.Fatalf("LoadProviderStats() error: %v", err)
	}
	if stats == nil {
		t.Fatal("expected non-nil stats")
	}
	if stats.Today.InputTokens != 123 {
		t.Errorf("Today.InputTokens = %d, want 123", stats.Today.InputTokens)
	}
	if stats.HistoryTotal.InputTokens != 456 {
		t.Errorf("HistoryTotal.InputTokens = %d, want 456", stats.HistoryTotal.InputTokens)
	}
}

func TestLoadProviderStats_CorruptJSON(t *testing.T) {
	tmp := t.TempDir()
	if err := EnsureCacheInfoDir(tmp); err != nil {
		t.Fatal(err)
	}

	path := expectedCacheInfoJSONPath(tmp, "openai")
	if err := os.WriteFile(path, []byte("{invalid json"), 0644); err != nil {
		t.Fatal(err)
	}

	stats, err := LoadProviderStats(tmp, "openai")
	if err == nil {
		t.Fatal("expected error for corrupt JSON")
	}
	if stats != nil {
		t.Fatalf("expected nil stats for corrupt JSON, got %+v", stats)
	}
}

func TestLoadProviderStats_IgnoresTmpFiles(t *testing.T) {
	tmp := t.TempDir()
	if err := EnsureCacheInfoDir(tmp); err != nil {
		t.Fatal(err)
	}

	// write only a .tmp file, no real json
	tmpPath := expectedCacheInfoJSONPath(tmp, "openai") + ".tmp"
	if err := os.WriteFile(tmpPath, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	stats, err := LoadProviderStats(tmp, "openai")
	if err != nil {
		t.Fatalf("LoadProviderStats() error: %v", err)
	}
	if stats != nil {
		t.Fatalf("expected nil stats when only .tmp exists, got %+v", stats)
	}
}

func TestSaveProviderStats_AtomicWrite(t *testing.T) {
	tmp := t.TempDir()
	if err := EnsureCacheInfoDir(tmp); err != nil {
		t.Fatal(err)
	}

	loc := time.FixedZone("CST", 8*3600)
	stats := ProviderStats{
		Timezone:      "Asia/Shanghai",
		TodayDate:     "2026-03-27",
		YesterdayDate: "2026-03-26",
		Today: TokenTotals{
			InputTokens:  100,
			CachedTokens: 40,
			OutputTokens: 50,
			TotalTokens:  150,
		},
		Yesterday: TokenTotals{
			InputTokens:  200,
			CachedTokens: 80,
			OutputTokens: 100,
			TotalTokens:  300,
		},
		HistoryTotal: TokenTotals{
			InputTokens:  300,
			CachedTokens: 120,
			OutputTokens: 150,
			TotalTokens:  450,
		},
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, loc),
	}

	if err := SaveProviderStats(tmp, "openai", &stats); err != nil {
		t.Fatalf("SaveProviderStats() error: %v", err)
	}

	// verify JSON file exists and has correct content
	jsonPath := expectedCacheInfoJSONPath(tmp, "openai")
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read json: %v", err)
	}
	var loaded ProviderStats
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if loaded.Today.InputTokens != 100 {
		t.Errorf("Today.InputTokens = %d, want 100", loaded.Today.InputTokens)
	}

	// verify TXT file exists
	txtPath := expectedCacheInfoTXTPath(tmp, "openai")
	txtData, err := os.ReadFile(txtPath)
	if err != nil {
		t.Fatalf("read txt: %v", err)
	}
	txtStr := string(txtData)
	if !strings.Contains(txtStr, "[昨日]") {
		t.Error("TXT missing [昨日]")
	}
	if !strings.Contains(txtStr, "[今日]") {
		t.Error("TXT missing [今日]")
	}
	if !strings.Contains(txtStr, "[提供商历史以来总计]") {
		t.Error("TXT missing [提供商历史以来总计]")
	}

	// verify no .tmp files remain
	entries, err := os.ReadDir(expectedCacheInfoDir(tmp))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("残留临时文件: %s", e.Name())
		}
	}
}

func TestSaveProviderStats_DoesNotCorruptExisting(t *testing.T) {
	tmp := t.TempDir()
	if err := EnsureCacheInfoDir(tmp); err != nil {
		t.Fatal(err)
	}

	loc := time.FixedZone("CST", 8*3600)

	// write first version
	stats1 := ProviderStats{
		Timezone:      "Asia/Shanghai",
		TodayDate:     "2026-03-27",
		YesterdayDate: "2026-03-26",
		Today:         TokenTotals{InputTokens: 100},
		HistoryTotal:  TokenTotals{InputTokens: 100},
		UpdatedAt:     time.Date(2026, 3, 27, 12, 0, 0, 0, loc),
	}
	if err := SaveProviderStats(tmp, "openai", &stats1); err != nil {
		t.Fatal(err)
	}

	// write second version (update)
	stats2 := stats1
	stats2.Today.InputTokens = 200
	stats2.HistoryTotal.InputTokens = 200
	stats2.UpdatedAt = time.Date(2026, 3, 27, 12, 5, 0, 0, loc)
	if err := SaveProviderStats(tmp, "openai", &stats2); err != nil {
		t.Fatal(err)
	}

	// verify the latest version is correct
	loaded, err := LoadProviderStats(tmp, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil stats")
	}
	if loaded.Today.InputTokens != 200 {
		t.Errorf("Today.InputTokens = %d, want 200", loaded.Today.InputTokens)
	}
}

func TestSaveProviderStats_CustomProvidersDir(t *testing.T) {
	// use a nested custom dir
	customDir := filepath.Join(t.TempDir(), "my", "custom", "providers")
	if err := os.MkdirAll(customDir, 0755); err != nil {
		t.Fatal(err)
	}

	loc := time.FixedZone("CST", 8*3600)
	stats := ProviderStats{
		Timezone:      "Asia/Shanghai",
		TodayDate:     "2026-03-27",
		YesterdayDate: "2026-03-26",
		Today:         TokenTotals{InputTokens: 42},
		HistoryTotal:  TokenTotals{InputTokens: 42},
		UpdatedAt:     time.Date(2026, 3, 27, 12, 0, 0, 0, loc),
	}

	if err := SaveProviderStats(customDir, "test-provider", &stats); err != nil {
		t.Fatalf("SaveProviderStats() error: %v", err)
	}

	// verify files are in the correct location
	jsonPath := expectedCacheInfoJSONPath(customDir, "test-provider")
	if _, err := os.Stat(jsonPath); err != nil {
		t.Fatalf("JSON file not at expected path %s: %v", jsonPath, err)
	}

	txtPath := expectedCacheInfoTXTPath(customDir, "test-provider")
	if _, err := os.Stat(txtPath); err != nil {
		t.Fatalf("TXT file not at expected path %s: %v", txtPath, err)
	}
}
