package cacheinfo

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func aggregateStats(timezone string, today time.Time, statsList []*ProviderStats) ProviderStats {
	aggregated := ProviderStats{
		Timezone:   timezone,
		RecentDays: []DailyStats{{Date: today.Format("2006-01-02")}},
		UpdatedAt:  today,
	}
	if len(statsList) == 0 {
		syncLegacyFields(&aggregated)
		return aggregated
	}

	dayTotals := make(map[string]TokenTotals)
	var latestUpdatedAt time.Time
	for _, stats := range statsList {
		if stats == nil {
			continue
		}
		aggregated.HistoryTotal = addTokenTotals(aggregated.HistoryTotal, stats.HistoryTotal)
		for _, day := range statsDaysForAggregation(stats) {
			dayTotals[day.Date] = addTokenTotals(dayTotals[day.Date], day.Totals)
		}
		if stats.UpdatedAt.After(latestUpdatedAt) {
			latestUpdatedAt = stats.UpdatedAt
		}
	}

	if len(dayTotals) > 0 {
		dates := make([]string, 0, len(dayTotals))
		for date := range dayTotals {
			dates = append(dates, date)
		}
		sort.Strings(dates)
		aggregated.RecentDays = make([]DailyStats, 0, len(dates))
		for _, date := range dates {
			aggregated.RecentDays = append(aggregated.RecentDays, DailyStats{Date: date, Totals: dayTotals[date]})
		}
		trimRecentDays(&aggregated)
	}
	if !latestUpdatedAt.IsZero() {
		aggregated.UpdatedAt = latestUpdatedAt
	}
	syncLegacyFields(&aggregated)
	return aggregated
}

func addTokenTotals(dst, src TokenTotals) TokenTotals {
	dst.InputTokens += src.InputTokens
	dst.CachedTokens += src.CachedTokens
	dst.OutputTokens += src.OutputTokens
	dst.TotalTokens += src.TotalTokens
	dst.RequestCount += src.RequestCount
	return dst
}

func statsDaysForAggregation(stats *ProviderStats) []DailyStats {
	if stats == nil {
		return nil
	}
	if len(stats.RecentDays) > 0 {
		return append([]DailyStats(nil), stats.RecentDays...)
	}
	days := make([]DailyStats, 0, 2)
	if stats.YesterdayDate != "" || stats.Yesterday != (TokenTotals{}) {
		days = append(days, DailyStats{Date: stats.YesterdayDate, Totals: stats.Yesterday})
	}
	if stats.TodayDate != "" || stats.Today != (TokenTotals{}) {
		days = append(days, DailyStats{Date: stats.TodayDate, Totals: stats.Today})
	}
	return days
}

func writeAggregateTXTFiles(providersDir string, enabledStats map[string]*ProviderStats, now time.Time, timezone string) error {
	allStats, err := loadAllProviderStats(providersDir)
	if err != nil {
		return err
	}
	if err := writeAggregateTXT(providersDir, "全提供商总计.txt", aggregateStats(timezone, now, allStats)); err != nil {
		return err
	}
	enabledList := make([]*ProviderStats, 0, len(enabledStats))
	for _, stats := range enabledStats {
		enabledList = append(enabledList, stats)
	}
	return writeAggregateTXT(providersDir, "已启用提供商总计.txt", aggregateStats(timezone, now, enabledList))
}

func writeAggregateTXT(providersDir, fileName string, stats ProviderStats) error {
	path := filepath.Join(cacheInfoDir(providersDir), fileName)
	if err := atomicWriteTXT(path, RenderProviderStats(stats)); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", fileName, err)
	}
	return nil
}

func loadAllProviderStats(providersDir string) ([]*ProviderStats, error) {
	providerIDs, err := loadAggregateProviderIDs(providersDir)
	if err != nil {
		return nil, err
	}
	statsList := make([]*ProviderStats, 0, len(providerIDs))
	for _, providerID := range providerIDs {
		stats, loadErr := LoadProviderStats(providersDir, providerID)
		if loadErr != nil {
			log.Printf("[cacheinfo] 跳过损坏 provider 统计 %s: %v", providerID, loadErr)
			continue
		}
		if stats != nil {
			statsList = append(statsList, stats)
		}
	}
	return statsList, nil
}

func loadAggregateProviderIDs(providersDir string) ([]string, error) {
	providerIDs := make(map[string]struct{})
	for _, dir := range []string{systemJSONDir(providersDir), cacheInfoDir(providersDir)} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			providerID := strings.TrimSuffix(entry.Name(), ".json")
			if providerID == "" {
				continue
			}
			providerIDs[providerID] = struct{}{}
		}
	}
	ids := make([]string, 0, len(providerIDs))
	for providerID := range providerIDs {
		ids = append(ids, providerID)
	}
	sort.Strings(ids)
	return ids, nil
}
