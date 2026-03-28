package cacheinfo

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadAllProviderStats_IncludesLegacyRootJSON(t *testing.T) {
	tmp := t.TempDir()
	if err := EnsureCacheInfoDir(tmp); err != nil {
		t.Fatal(err)
	}

	legacy := ProviderStats{
		Timezone: "Asia/Shanghai",
		RecentDays: []DailyStats{{
			Date:   "2026-03-27",
			Totals: TokenTotals{InputTokens: 12, TotalTokens: 12, RequestCount: 1},
		}},
		HistoryTotal: TokenTotals{InputTokens: 12, TotalTokens: 12, RequestCount: 1},
		UpdatedAt:    time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(expectedCacheInfoDir(tmp), "legacy-only.json")
	if err := os.WriteFile(legacyPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	statsList, err := loadAllProviderStats(tmp)
	if err != nil {
		t.Fatalf("loadAllProviderStats: %v", err)
	}
	if len(statsList) != 1 {
		t.Fatalf("len(statsList) = %d, want 1", len(statsList))
	}
	if statsList[0].HistoryTotal.InputTokens != 12 || statsList[0].HistoryTotal.RequestCount != 1 {
		t.Fatalf("unexpected loaded legacy stats: %#v", statsList[0])
	}
}

func TestLoadAllProviderStats_SkipsCorruptJSONWithLog(t *testing.T) {
	tmp := t.TempDir()
	if err := EnsureCacheInfoDir(tmp); err != nil {
		t.Fatal(err)
	}
	good := ProviderStats{
		Timezone:     "Asia/Shanghai",
		HistoryTotal: TokenTotals{InputTokens: 5, TotalTokens: 5, RequestCount: 1},
		UpdatedAt:    time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(good)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(expectedCacheInfoJSONPath(tmp, "good"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(expectedCacheInfoJSONPath(tmp, "bad"), []byte("{bad json"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	prevWriter := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prevWriter)

	statsList, err := loadAllProviderStats(tmp)
	if err != nil {
		t.Fatalf("loadAllProviderStats: %v", err)
	}
	if len(statsList) != 1 || statsList[0].HistoryTotal.InputTokens != 5 {
		t.Fatalf("unexpected statsList: %#v", statsList)
	}
	if !strings.Contains(buf.String(), "跳过损坏 provider 统计 bad") {
		t.Fatalf("expected corrupt-json log, got %q", buf.String())
	}
}
