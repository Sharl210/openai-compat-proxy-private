package cacheinfo

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// mockClock 允许测试中控制时间
type mockClock struct {
	mu  sync.Mutex
	now time.Time
}

func newMockClock(loc *time.Location) *mockClock {
	return &mockClock{now: time.Date(2026, 3, 27, 12, 0, 0, 0, loc)}
}

func (c *mockClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *mockClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

func (c *mockClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestManager_SameDayMultipleRecords(t *testing.T) {
	tmp := t.TempDir()
	loc := time.FixedZone("CST", 8*3600)
	m := NewManager(tmp, loc, []string{"openai"}, nil)

	// 同一天记录 3 次
	reqs := []struct {
		reqID string
		usage Usage
	}{
		{"req-1", Usage{InputTokens: 100, CachedTokens: 40, OutputTokens: 50, TotalTokens: 150}},
		{"req-2", Usage{InputTokens: 200, CachedTokens: 80, OutputTokens: 100, TotalTokens: 300}},
		{"req-3", Usage{InputTokens: 150, CachedTokens: 60, OutputTokens: 75, TotalTokens: 225}},
	}

	for _, r := range reqs {
		if err := m.RecordFinalUsage(r.reqID, "openai", &r.usage); err != nil {
			t.Fatalf("RecordFinalUsage(%s) error: %v", r.reqID, err)
		}
	}

	stats := m.stats["openai"]
	if stats.Today.InputTokens != 450 {
		t.Errorf("Today.InputTokens = %d, want 450", stats.Today.InputTokens)
	}
	if stats.Today.CachedTokens != 180 {
		t.Errorf("Today.CachedTokens = %d, want 180", stats.Today.CachedTokens)
	}
	if stats.HistoryTotal.InputTokens != 450 {
		t.Errorf("HistoryTotal.InputTokens = %d, want 450", stats.HistoryTotal.InputTokens)
	}
}

func TestManager_RolloverOneDay(t *testing.T) {
	tmp := t.TempDir()
	loc := time.FixedZone("CST", 8*3600)
	clock := newMockClock(loc)
	m := NewManager(tmp, loc, []string{"openai"}, clock)

	// 今天记录
	usage1 := Usage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150}
	if err := m.RecordFinalUsage("req-1", "openai", &usage1); err != nil {
		t.Fatal(err)
	}

	// 跨到明天
	clock.Set(time.Date(2026, 3, 28, 10, 0, 0, 0, loc))

	usage2 := Usage{InputTokens: 200, OutputTokens: 100, TotalTokens: 300}
	if err := m.RecordFinalUsage("req-2", "openai", &usage2); err != nil {
		t.Fatal(err)
	}

	stats := m.stats["openai"]
	if stats.TodayDate != "2026-03-28" {
		t.Errorf("TodayDate = %s, want 2026-03-28", stats.TodayDate)
	}
	if stats.YesterdayDate != "2026-03-27" {
		t.Errorf("YesterdayDate = %s, want 2026-03-27", stats.YesterdayDate)
	}
	// yesterday 应该是旧的 today 值
	if stats.Yesterday.InputTokens != 100 {
		t.Errorf("Yesterday.InputTokens = %d, want 100", stats.Yesterday.InputTokens)
	}
	// today 应该是新的值
	if stats.Today.InputTokens != 200 {
		t.Errorf("Today.InputTokens = %d, want 200", stats.Today.InputTokens)
	}
	// history_total 累加
	if stats.HistoryTotal.InputTokens != 300 {
		t.Errorf("HistoryTotal.InputTokens = %d, want 300", stats.HistoryTotal.InputTokens)
	}
}

func TestManager_RolloverTwoDaysOrMore(t *testing.T) {
	tmp := t.TempDir()
	loc := time.FixedZone("CST", 8*3600)
	clock := newMockClock(loc)
	m := NewManager(tmp, loc, []string{"openai"}, clock)

	// 今天记录
	usage1 := Usage{InputTokens: 100, TotalTokens: 100}
	if err := m.RecordFinalUsage("req-1", "openai", &usage1); err != nil {
		t.Fatal(err)
	}

	// 跨到 3 天后
	clock.Set(time.Date(2026, 3, 30, 10, 0, 0, 0, loc))

	usage2 := Usage{InputTokens: 200, TotalTokens: 200}
	if err := m.RecordFinalUsage("req-2", "openai", &usage2); err != nil {
		t.Fatal(err)
	}

	stats := m.stats["openai"]
	if stats.TodayDate != "2026-03-30" {
		t.Errorf("TodayDate = %s, want 2026-03-30", stats.TodayDate)
	}
	// yesterday 被清空（因为跳过了多天）
	if stats.Yesterday.InputTokens != 0 {
		t.Errorf("Yesterday.InputTokens = %d, want 0", stats.Yesterday.InputTokens)
	}
	// history_total 仍然累加
	if stats.HistoryTotal.InputTokens != 300 {
		t.Errorf("HistoryTotal.InputTokens = %d, want 300", stats.HistoryTotal.InputTokens)
	}
}

func TestManager_DateBeforeToday(t *testing.T) {
	tmp := t.TempDir()
	loc := time.FixedZone("CST", 8*3600)
	clock := newMockClock(loc)
	m := NewManager(tmp, loc, []string{"openai"}, clock)

	// 先记录 3月28日
	clock.Set(time.Date(2026, 3, 28, 10, 0, 0, 0, loc))
	usage1 := Usage{InputTokens: 200, TotalTokens: 200}
	if err := m.RecordFinalUsage("req-1", "openai", &usage1); err != nil {
		t.Fatal(err)
	}

	// 然后回退到 3月27日（时钟可能被手动调回）
	clock.Set(time.Date(2026, 3, 27, 10, 0, 0, 0, loc))
	usage2 := Usage{InputTokens: 100, TotalTokens: 100}
	if err := m.RecordFinalUsage("req-2", "openai", &usage2); err != nil {
		t.Fatal(err)
	}

	stats := m.stats["openai"]
	// today_date 应该还是 3月28日（因为新日期 < today_date，只记录错误继续累计）
	if stats.TodayDate != "2026-03-28" {
		t.Errorf("TodayDate = %s, want 2026-03-28", stats.TodayDate)
	}
	// 新的 usage 应该继续累加到 today
	if stats.Today.InputTokens != 300 {
		t.Errorf("Today.InputTokens = %d, want 300", stats.Today.InputTokens)
	}
}

func TestManager_TimezoneChange(t *testing.T) {
	tmp := t.TempDir()
	loc := time.FixedZone("CST", 8*3600)
	clock := newMockClock(loc)
	m := NewManager(tmp, loc, []string{"openai"}, clock)

	// 记录一些数据
	usage := Usage{InputTokens: 100, TotalTokens: 100}
	if err := m.RecordFinalUsage("req-1", "openai", &usage); err != nil {
		t.Fatal(err)
	}

	// 手动写入一个不同时区的文件
	stats := m.stats["openai"]
	stats.Timezone = "America/New_York" // 模拟之前用的是纽约时区
	data, _ := json.Marshal(stats)
	os.WriteFile(filepath.Join(tmp, "Cache_Info", "openai.json"), data, 0644)

	// 创建新 manager，时区是 Asia/Shanghai
	m2 := NewManager(tmp, loc, []string{"openai"}, clock)

	// 应该检测到时区变化，清空 today/yesterday，保留 history_total
	stats2 := m2.stats["openai"]
	if stats2.Today.InputTokens != 0 {
		t.Errorf("Today.InputTokens = %d, want 0 (timezone change should reset)", stats2.Today.InputTokens)
	}
	if stats2.HistoryTotal.InputTokens != 100 {
		t.Errorf("HistoryTotal.InputTokens = %d, want 100 (preserve history)", stats2.HistoryTotal.InputTokens)
	}
}

func TestManager_DuplicateRequestID(t *testing.T) {
	tmp := t.TempDir()
	loc := time.FixedZone("CST", 8*3600)
	m := NewManager(tmp, loc, []string{"openai"}, nil)

	usage := Usage{InputTokens: 100, TotalTokens: 100}

	// 第一次提交
	if err := m.RecordFinalUsage("req-dup", "openai", &usage); err != nil {
		t.Fatal(err)
	}

	// 重复提交同一个 requestID
	if err := m.RecordFinalUsage("req-dup", "openai", &usage); err != nil {
		t.Fatal(err)
	}

	stats := m.stats["openai"]
	// 应该只入账一次
	if stats.Today.InputTokens != 100 {
		t.Errorf("Today.InputTokens = %d, want 100 (duplicate should be ignored)", stats.Today.InputTokens)
	}
}

func TestManager_DisabledProviderDoesNotDeleteOldFile(t *testing.T) {
	tmp := t.TempDir()
	loc := time.FixedZone("CST", 8*3600)

	// 先创建一个旧的 provider 文件
	if err := EnsureCacheInfoDir(tmp); err != nil {
		t.Fatal(err)
	}
	oldStats := ProviderStats{
		Timezone:     "Asia/Shanghai",
		TodayDate:    "2026-03-27",
		Today:        TokenTotals{InputTokens: 999},
		HistoryTotal: TokenTotals{InputTokens: 999},
		UpdatedAt:    time.Now(),
	}
	data, _ := json.Marshal(oldStats)
	os.WriteFile(filepath.Join(tmp, "Cache_Info", "disabled-provider.json"), data, 0644)

	// 创建 manager，不包含 disabled-provider
	_ = NewManager(tmp, loc, []string{"openai"}, nil)

	// 文件应该仍然存在
	if _, err := os.Stat(filepath.Join(tmp, "Cache_Info", "disabled-provider.json")); os.IsNotExist(err) {
		t.Error("old file for disabled provider was deleted")
	}
}

func TestManager_ProviderRenameCreatesNewFile(t *testing.T) {
	tmp := t.TempDir()
	loc := time.FixedZone("CST", 8*3600)

	// 先用旧名字记录
	m := NewManager(tmp, loc, []string{"old-name"}, nil)
	usage := Usage{InputTokens: 100, TotalTokens: 100}
	m.RecordFinalUsage("req-1", "old-name", &usage)

	// 文件应该存在
	if _, err := os.Stat(filepath.Join(tmp, "Cache_Info", "old-name.json")); os.IsNotExist(err) {
		t.Error("old-name.json not created")
	}

	// 创建新 manager，provider 改名
	m2 := NewManager(tmp, loc, []string{"new-name"}, nil)
	m2.RecordFinalUsage("req-2", "new-name", &usage)

	// 新文件应该存在
	if _, err := os.Stat(filepath.Join(tmp, "Cache_Info", "new-name.json")); os.IsNotExist(err) {
		t.Error("new-name.json not created")
	}
	// 旧文件仍然存在
	if _, err := os.Stat(filepath.Join(tmp, "Cache_Info", "old-name.json")); os.IsNotExist(err) {
		t.Error("old-name.json should still exist")
	}
}

func TestManager_FlushWritesFile(t *testing.T) {
	tmp := t.TempDir()
	loc := time.FixedZone("CST", 8*3600)
	m := NewManager(tmp, loc, []string{"openai"}, nil)

	usage := Usage{InputTokens: 100, CachedTokens: 40, OutputTokens: 50, TotalTokens: 150}
	if err := m.RecordFinalUsage("req-1", "openai", &usage); err != nil {
		t.Fatal(err)
	}

	// 检查文件是否写入
	jsonPath := filepath.Join(tmp, "Cache_Info", "openai.json")
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("failed to read json file: %v", err)
	}

	var loaded ProviderStats
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if loaded.Today.InputTokens != 100 {
		t.Errorf("file Today.InputTokens = %d, want 100", loaded.Today.InputTokens)
	}
}

func TestManager_StartStop(t *testing.T) {
	tmp := t.TempDir()
	loc := time.FixedZone("CST", 8*3600)
	m := NewManager(tmp, loc, []string{"openai"}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m.Start(ctx)
	defer m.Stop()

	// 记录数据
	usage := Usage{InputTokens: 100, TotalTokens: 100}
	m.RecordFinalUsage("req-1", "openai", &usage)

	// 等待刷新
	time.Sleep(6 * time.Second)

	// 检查文件
	jsonPath := filepath.Join(tmp, "Cache_Info", "openai.json")
	if _, err := os.Stat(jsonPath); os.IsNotExist(err) {
		t.Error("json file not created by periodic flush")
	}
}

func TestManager_MultipleProviders(t *testing.T) {
	tmp := t.TempDir()
	loc := time.FixedZone("CST", 8*3600)
	m := NewManager(tmp, loc, []string{"openai", "anthropic"}, nil)

	usage := Usage{InputTokens: 100, TotalTokens: 100}

	m.RecordFinalUsage("req-1", "openai", &usage)
	m.RecordFinalUsage("req-2", "anthropic", &usage)

	if m.stats["openai"].Today.InputTokens != 100 {
		t.Errorf("openai Today.InputTokens = %d, want 100", m.stats["openai"].Today.InputTokens)
	}
	if m.stats["anthropic"].Today.InputTokens != 100 {
		t.Errorf("anthropic Today.InputTokens = %d, want 100", m.stats["anthropic"].Today.InputTokens)
	}
}

func TestManager_LoadExistingStats(t *testing.T) {
	tmp := t.TempDir()
	loc, _ := time.LoadLocation("Asia/Shanghai")

	// 先写入已有数据
	if err := EnsureCacheInfoDir(tmp); err != nil {
		t.Fatal(err)
	}
	existing := ProviderStats{
		Timezone:      "Asia/Shanghai",
		TodayDate:     "2026-03-27",
		YesterdayDate: "2026-03-26",
		Today:         TokenTotals{InputTokens: 500, TotalTokens: 500},
		Yesterday:     TokenTotals{InputTokens: 300, TotalTokens: 300},
		HistoryTotal:  TokenTotals{InputTokens: 1000, TotalTokens: 1000},
		UpdatedAt:     time.Now(),
	}
	data, _ := json.Marshal(existing)
	os.WriteFile(filepath.Join(tmp, "Cache_Info", "openai.json"), data, 0644)

	// 创建 manager，应该加载已有数据
	clock := newMockClock(loc)
	m := NewManager(tmp, loc, []string{"openai"}, clock)

	// 继续记录
	usage := Usage{InputTokens: 100, TotalTokens: 100}
	if err := m.RecordFinalUsage("req-new", "openai", &usage); err != nil {
		t.Fatal(err)
	}

	stats := m.stats["openai"]
	// 应该在已有数据基础上累加
	if stats.Today.InputTokens != 600 {
		t.Errorf("Today.InputTokens = %d, want 600", stats.Today.InputTokens)
	}
	if stats.HistoryTotal.InputTokens != 1100 {
		t.Errorf("HistoryTotal.InputTokens = %d, want 1100", stats.HistoryTotal.InputTokens)
	}
}
