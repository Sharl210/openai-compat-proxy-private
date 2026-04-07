package cacheinfo

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrProviderHistoryNotFound = errors.New("cacheinfo provider history not found")

type Usage struct {
	InputTokens         int64
	CachedTokens        int64
	CacheCreationTokens int64
	OutputTokens        int64
	TotalTokens         int64
}

type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type Manager struct {
	providersDir string
	location     *time.Location
	clock        Clock
	enabledFn    func() []string
	defaultFn    func() []string

	mu              sync.RWMutex
	stats           map[string]*ProviderStats
	submitted       map[string]bool
	submittedOrder  []string
	submittedLimit  int
	enabledProvider map[string]struct{}

	ticker *time.Ticker
	stopCh chan struct{}
	wg     sync.WaitGroup
}

const defaultSubmittedLimit = 4096

func NewManager(providersDir string, location *time.Location, enabledProviders []string, clock Clock) *Manager {
	_ = EnsureCacheInfoDir(providersDir)

	if clock == nil {
		clock = systemClock{}
	}

	m := &Manager{
		providersDir:    providersDir,
		location:        location,
		clock:           clock,
		stats:           make(map[string]*ProviderStats),
		submitted:       make(map[string]bool),
		submittedLimit:  defaultSubmittedLimit,
		enabledProvider: make(map[string]struct{}),
		stopCh:          make(chan struct{}),
	}

	tzName := location.String()
	now := clock.Now().In(location)
	todayStr := now.Format("2006-01-02")
	yesterdayStr := now.AddDate(0, 0, -1).Format("2006-01-02")

	for _, pid := range enabledProviders {
		if pid == "" {
			continue
		}
		m.enabledProvider[pid] = struct{}{}
		loaded, err := LoadProviderStats(providersDir, pid)
		if err != nil {
			log.Printf("[cacheinfo] 加载 provider %s 失败，使用空状态: %v", pid, err)
			loaded = nil
		}

		if loaded != nil {
			if loaded.Timezone != tzName {
				log.Printf("[cacheinfo] provider %s 时区变更: %s -> %s，保留 history_total", pid, loaded.Timezone, tzName)
				loaded.Timezone = tzName
				loaded.RecentDays = []DailyStats{{Date: todayStr}}
				syncLegacyFields(loaded)
				loaded.UpdatedAt = now
				_ = SaveProviderStats(providersDir, pid, loaded)
			}
			normalizeRecentDays(loaded, todayStr)
			m.stats[pid] = loaded
		} else {
			m.stats[pid] = &ProviderStats{
				Timezone:      tzName,
				TodayDate:     todayStr,
				YesterdayDate: yesterdayStr,
				RecentDays:    []DailyStats{{Date: todayStr}},
				UpdatedAt:     now,
			}
			syncLegacyFields(m.stats[pid])
		}
	}

	return m
}

func (m *Manager) SetEnabledProvidersSource(fn func() []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabledFn = fn
	m.syncEnabledProvidersLocked()
}

func (m *Manager) SetDefaultProvidersSource(fn func() []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultFn = fn
}

func (m *Manager) RecordFinalUsage(requestID, providerID string, usage *Usage) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	submissionKey := fmt.Sprintf("%d:%s%d:%s", len(providerID), providerID, len(requestID), requestID)
	if m.submitted[submissionKey] {
		return nil
	}

	stats, ok := m.stats[providerID]
	if !ok {
		m.syncEnabledProvidersLocked()
		stats, ok = m.stats[providerID]
	}
	if !ok {
		return nil
	}

	m.checkCrossDayAndReset(providerID)
	now := m.clock.Now().In(m.location)

	day := ensureCurrentDay(stats, now.Format("2006-01-02"))
	day.InputTokens += usage.InputTokens
	day.CachedTokens += usage.CachedTokens
	day.CacheCreationTokens += usage.CacheCreationTokens
	day.OutputTokens += usage.OutputTokens
	day.TotalTokens += usage.TotalTokens
	day.RequestCount++
	stats.HistoryTotal.InputTokens += usage.InputTokens
	stats.HistoryTotal.CachedTokens += usage.CachedTokens
	stats.HistoryTotal.CacheCreationTokens += usage.CacheCreationTokens
	stats.HistoryTotal.OutputTokens += usage.OutputTokens
	stats.HistoryTotal.TotalTokens += usage.TotalTokens
	stats.HistoryTotal.RequestCount++
	syncLegacyFields(stats)
	stats.UpdatedAt = now

	m.submitted[submissionKey] = true
	m.submittedOrder = append(m.submittedOrder, submissionKey)
	m.pruneSubmittedLocked()
	return nil
}

func (m *Manager) pruneSubmittedLocked() {
	limit := m.submittedLimit
	if limit <= 0 {
		limit = defaultSubmittedLimit
	}
	for len(m.submittedOrder) > limit {
		oldest := m.submittedOrder[0]
		m.submittedOrder = m.submittedOrder[1:]
		delete(m.submitted, oldest)
	}
}

func (m *Manager) checkCrossDayAndReset(providerID string) {
	stats := m.stats[providerID]
	now := m.clock.Now().In(m.location)
	todayStr := now.Format("2006-01-02")
	normalizeRecentDays(stats, todayStr)

	todayDate, err := time.ParseInLocation("2006-01-02", stats.TodayDate, m.location)
	if err != nil {
		stats.RecentDays = []DailyStats{{Date: todayStr}}
		syncLegacyFields(stats)
		return
	}

	nowDate, _ := time.ParseInLocation("2006-01-02", todayStr, m.location)

	if nowDate.Equal(todayDate) {
		return
	}

	if nowDate.After(todayDate) {
		daysSinceToday := daysBetweenDates(todayDate, nowDate)
		for i := 1; i <= daysSinceToday; i++ {
			stats.RecentDays = append(stats.RecentDays, DailyStats{Date: todayDate.AddDate(0, 0, i).Format("2006-01-02")})
		}
		trimRecentDays(stats)
		syncLegacyFields(stats)
		return
	}

	log.Printf("[cacheinfo] provider %s 当前日期 %s 早于 today_date %s，继续累计", providerID, todayStr, stats.TodayDate)
}

func daysBetweenDates(start, end time.Time) int {
	days := 0
	for current := start; current.Before(end); current = current.AddDate(0, 0, 1) {
		days++
	}
	return days
}

func normalizeRecentDays(stats *ProviderStats, todayStr string) {
	if stats == nil {
		return
	}
	filtered := make([]DailyStats, 0, len(stats.RecentDays)+2)
	seen := map[string]TokenTotals{}
	if stats.YesterdayDate != "" || stats.Yesterday != (TokenTotals{}) {
		seen[stats.YesterdayDate] = stats.Yesterday
	}
	if stats.TodayDate != "" || stats.Today != (TokenTotals{}) {
		seen[stats.TodayDate] = stats.Today
	}
	for _, day := range stats.RecentDays {
		if day.Date == "" {
			continue
		}
		seen[day.Date] = day.Totals
	}
	for date, totals := range seen {
		filtered = append(filtered, DailyStats{Date: date, Totals: totals})
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Date < filtered[j].Date })
	stats.RecentDays = filtered
	if len(stats.RecentDays) == 0 {
		stats.RecentDays = []DailyStats{{Date: todayStr}}
	} else if lastDate := stats.RecentDays[len(stats.RecentDays)-1].Date; lastDate < todayStr {
		lastDay, err := time.ParseInLocation("2006-01-02", lastDate, time.UTC)
		todayDay, todayErr := time.ParseInLocation("2006-01-02", todayStr, time.UTC)
		if err == nil && todayErr == nil && !todayDay.Before(lastDay) {
			for day := lastDay.AddDate(0, 0, 1); !day.After(todayDay); day = day.AddDate(0, 0, 1) {
				stats.RecentDays = append(stats.RecentDays, DailyStats{Date: day.Format("2006-01-02")})
			}
		}
	}
	trimRecentDays(stats)
	syncLegacyFields(stats)
}

func trimRecentDays(stats *ProviderStats) {
	if len(stats.RecentDays) <= 7 {
		return
	}
	stats.RecentDays = append([]DailyStats(nil), stats.RecentDays[len(stats.RecentDays)-7:]...)
}

func syncLegacyFields(stats *ProviderStats) {
	stats.Today = TokenTotals{}
	stats.TodayDate = ""
	stats.Yesterday = TokenTotals{}
	stats.YesterdayDate = ""
	if n := len(stats.RecentDays); n > 0 {
		stats.TodayDate = stats.RecentDays[n-1].Date
		stats.Today = stats.RecentDays[n-1].Totals
	}
	if n := len(stats.RecentDays); n > 1 {
		stats.YesterdayDate = stats.RecentDays[n-2].Date
		stats.Yesterday = stats.RecentDays[n-2].Totals
	}
}

func ensureCurrentDay(stats *ProviderStats, date string) *TokenTotals {
	normalizeRecentDays(stats, date)
	if len(stats.RecentDays) == 0 {
		stats.RecentDays = append(stats.RecentDays, DailyStats{Date: date})
		trimRecentDays(stats)
		syncLegacyFields(stats)
	}
	lastIdx := len(stats.RecentDays) - 1
	if stats.RecentDays[lastIdx].Date > date {
		return &stats.RecentDays[lastIdx].Totals
	}
	if stats.RecentDays[lastIdx].Date != date {
		stats.RecentDays = append(stats.RecentDays, DailyStats{Date: date})
		trimRecentDays(stats)
		syncLegacyFields(stats)
	}
	return &stats.RecentDays[len(stats.RecentDays)-1].Totals
}

func (m *Manager) Start(ctx context.Context) {
	m.ticker = time.NewTicker(5 * time.Second)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		for {
			select {
			case <-m.ticker.C:
				m.flushAll()
			case <-ctx.Done():
				m.flushAll()
				return
			case <-m.stopCh:
				m.flushAll()
				return
			}
		}
	}()
}

func (m *Manager) Stop() {
	select {
	case <-m.stopCh:
	default:
		close(m.stopCh)
	}
	if m.ticker != nil {
		m.ticker.Stop()
	}
	m.wg.Wait()
}

func (m *Manager) ClearProviderHistory(providerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.syncEnabledProvidersLocked()
	stats, ok := m.stats[providerID]
	if !ok {
		return ErrProviderHistoryNotFound
	}
	now := m.clock.Now().In(m.location)
	resetProviderStats(stats, now, m.location.String())
	m.removeSubmittedForProviderLocked(providerID)
	m.persistAllLocked()
	return nil
}

func resetProviderStats(stats *ProviderStats, now time.Time, timezone string) {
	if stats == nil {
		return
	}
	stats.Timezone = timezone
	stats.RecentDays = []DailyStats{{Date: now.Format("2006-01-02")}}
	stats.HistoryTotal = TokenTotals{}
	stats.UpdatedAt = now
	syncLegacyFields(stats)
}

func (m *Manager) removeSubmittedForProviderLocked(providerID string) {
	prefix := fmt.Sprintf("%d:%s", len(providerID), providerID)
	filtered := m.submittedOrder[:0]
	for _, key := range m.submittedOrder {
		if strings.HasPrefix(key, prefix) {
			delete(m.submitted, key)
			continue
		}
		filtered = append(filtered, key)
	}
	m.submittedOrder = filtered
}

func (m *Manager) flushAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.syncEnabledProvidersLocked()

	for pid := range m.stats {
		m.checkCrossDayAndReset(pid)
	}

	m.persistAllLocked()
}

func (m *Manager) persistAllLocked() {
	for pid, stats := range m.stats {
		if err := SaveProviderStats(m.providersDir, pid, stats); err != nil {
			log.Printf("[cacheinfo] 写入 provider %s 失败: %v", pid, err)
		}
	}
	if err := writeAggregateTXTFiles(m.providersDir, m.enabledStatsSnapshotLocked(), m.defaultEnabledStatsSnapshotLocked(), m.clock.Now().In(m.location), m.location.String()); err != nil {
		log.Printf("[cacheinfo] 写入聚合 TXT 失败: %v", err)
	}
}

func (m *Manager) enabledStatsSnapshotLocked() map[string]*ProviderStats {
	stats := make(map[string]*ProviderStats, len(m.enabledProvider))
	for providerID := range m.enabledProvider {
		if providerStats, ok := m.stats[providerID]; ok {
			stats[providerID] = providerStats
		}
	}
	return stats
}

func (m *Manager) defaultEnabledStatsSnapshotLocked() []*ProviderStats {
	if m.defaultFn == nil {
		return nil
	}
	providerIDs := m.defaultFn()
	stats := make([]*ProviderStats, 0, len(providerIDs))
	for _, providerID := range providerIDs {
		if _, ok := m.enabledProvider[providerID]; !ok {
			continue
		}
		if providerStats, ok := m.stats[providerID]; ok {
			stats = append(stats, providerStats)
		}
	}
	return stats
}

func (m *Manager) syncEnabledProvidersLocked() {
	if m.enabledFn == nil {
		return
	}
	m.setEnabledProvidersLocked(m.enabledFn())
}

func (m *Manager) setEnabledProvidersLocked(enabledProviders []string) {
	next := make(map[string]struct{}, len(enabledProviders))
	now := m.clock.Now().In(m.location)
	todayStr := now.Format("2006-01-02")
	yesterdayStr := now.AddDate(0, 0, -1).Format("2006-01-02")
	tzName := m.location.String()
	for _, pid := range enabledProviders {
		if pid == "" {
			continue
		}
		next[pid] = struct{}{}
		if _, ok := m.stats[pid]; ok {
			continue
		}
		loaded, err := LoadProviderStats(m.providersDir, pid)
		if err != nil {
			log.Printf("[cacheinfo] 加载 provider %s 失败，使用空状态: %v", pid, err)
			loaded = nil
		}
		if loaded != nil {
			normalizeRecentDays(loaded, todayStr)
			m.stats[pid] = loaded
			continue
		}
		m.stats[pid] = &ProviderStats{
			Timezone:      tzName,
			TodayDate:     todayStr,
			YesterdayDate: yesterdayStr,
			RecentDays:    []DailyStats{{Date: todayStr}},
			UpdatedAt:     now,
		}
		syncLegacyFields(m.stats[pid])
	}
	m.enabledProvider = next
}
