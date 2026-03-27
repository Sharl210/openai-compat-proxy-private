package cacheinfo

import (
	"context"
	"log"
	"sync"
	"time"
)

type Usage struct {
	InputTokens  int64
	CachedTokens int64
	OutputTokens int64
	TotalTokens  int64
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

	mu        sync.RWMutex
	stats     map[string]*ProviderStats
	submitted map[string]bool

	ticker *time.Ticker
	stopCh chan struct{}
}

func NewManager(providersDir string, location *time.Location, enabledProviders []string, clock Clock) *Manager {
	_ = EnsureCacheInfoDir(providersDir)

	if clock == nil {
		clock = systemClock{}
	}

	m := &Manager{
		providersDir: providersDir,
		location:     location,
		clock:        clock,
		stats:        make(map[string]*ProviderStats),
		submitted:    make(map[string]bool),
		stopCh:       make(chan struct{}),
	}

	tzName := location.String()
	now := clock.Now().In(location)
	todayStr := now.Format("2006-01-02")
	yesterdayStr := now.AddDate(0, 0, -1).Format("2006-01-02")

	for _, pid := range enabledProviders {
		loaded, err := LoadProviderStats(providersDir, pid)
		if err != nil {
			log.Printf("[cacheinfo] 加载 provider %s 失败，使用空状态: %v", pid, err)
			loaded = nil
		}

		if loaded != nil {
			if loaded.Timezone != tzName {
				log.Printf("[cacheinfo] provider %s 时区变更: %s -> %s，保留 history_total", pid, loaded.Timezone, tzName)
				loaded.Timezone = tzName
				loaded.TodayDate = todayStr
				loaded.YesterdayDate = yesterdayStr
				loaded.Today = TokenTotals{}
				loaded.Yesterday = TokenTotals{}
				loaded.UpdatedAt = now
				_ = SaveProviderStats(providersDir, pid, loaded)
			}
			m.stats[pid] = loaded
		} else {
			m.stats[pid] = &ProviderStats{
				Timezone:      tzName,
				TodayDate:     todayStr,
				YesterdayDate: yesterdayStr,
				UpdatedAt:     now,
			}
		}
	}

	return m
}

func (m *Manager) RecordFinalUsage(requestID, providerID string, usage *Usage) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.submitted[requestID] {
		return nil
	}

	stats, ok := m.stats[providerID]
	if !ok {
		return nil
	}

	now := m.clock.Now().In(m.location)
	m.checkAndRollOver(providerID, now)

	stats.Today.InputTokens += usage.InputTokens
	stats.Today.CachedTokens += usage.CachedTokens
	stats.Today.OutputTokens += usage.OutputTokens
	stats.Today.TotalTokens += usage.TotalTokens
	stats.HistoryTotal.InputTokens += usage.InputTokens
	stats.HistoryTotal.CachedTokens += usage.CachedTokens
	stats.HistoryTotal.OutputTokens += usage.OutputTokens
	stats.HistoryTotal.TotalTokens += usage.TotalTokens
	stats.UpdatedAt = now

	m.submitted[requestID] = true

	_ = SaveProviderStats(m.providersDir, providerID, stats)
	return nil
}

func (m *Manager) checkAndRollOver(providerID string, now time.Time) {
	stats := m.stats[providerID]
	todayStr := now.Format("2006-01-02")

	todayDate, err := time.ParseInLocation("2006-01-02", stats.TodayDate, m.location)
	if err != nil {
		stats.TodayDate = todayStr
		stats.YesterdayDate = now.AddDate(0, 0, -1).Format("2006-01-02")
		return
	}

	nowDate, _ := time.ParseInLocation("2006-01-02", todayStr, m.location)

	if nowDate.Equal(todayDate) {
		return
	}

	if nowDate.After(todayDate) {
		diff := nowDate.Sub(todayDate)
		if diff <= 24*time.Hour {
			stats.Yesterday = stats.Today
			stats.YesterdayDate = stats.TodayDate
		} else {
			stats.Yesterday = TokenTotals{}
			stats.YesterdayDate = todayStr
		}
		stats.Today = TokenTotals{}
		stats.TodayDate = todayStr
		return
	}

	log.Printf("[cacheinfo] provider %s 当前日期 %s 早于 today_date %s，继续累计", providerID, todayStr, stats.TodayDate)
}

func (m *Manager) Start(ctx context.Context) {
	m.ticker = time.NewTicker(5 * time.Second)
	go func() {
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
	close(m.stopCh)
	if m.ticker != nil {
		m.ticker.Stop()
	}
}

func (m *Manager) flushAll() {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for pid, stats := range m.stats {
		_ = SaveProviderStats(m.providersDir, pid, stats)
	}
}
