package cacheinfo

import (
	"strings"
	"testing"
	"time"
)

func TestRenderProviderStats(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 3, 27, 12, 0, 0, 0, loc)

	tests := []struct {
		name     string
		stats    ProviderStats
		contains []string
	}{
		{
			name: "normal case with comparison",
			stats: ProviderStats{
				Timezone:      "Asia/Shanghai",
				TodayDate:     "2026-03-27",
				YesterdayDate: "2026-03-26",
				Yesterday: TokenTotals{
					InputTokens:  12345,
					CachedTokens: 6789,
					OutputTokens: 2345,
					TotalTokens:  14690,
				},
				Today: TokenTotals{
					InputTokens:  45678,
					CachedTokens: 21000,
					OutputTokens: 9876,
					TotalTokens:  55554,
				},
				HistoryTotal: TokenTotals{
					InputTokens:  99999,
					CachedTokens: 50000,
					OutputTokens: 20000,
					TotalTokens:  119999,
				},
				UpdatedAt: now,
			},
			contains: []string{
				"[前一天]",
				"输入Tokens：12,345",
				"缓存Tokens：6,789",
				"输出Tokens：2,345",
				"总计Tokens：14,690",
				"总调用次数：0",
				"缓存率：54.99 %",
				"* 相较于前一天：输入+ 12,345 | 缓存+ 6,789 | 输出+ 2,345 | 总计+ 14,690 | 缓存率：+ 54.99%",
				"[今天]",
				"输入Tokens：45,678",
				"缓存Tokens：21,000",
				"输出Tokens：9,876",
				"总计Tokens：55,554",
				"总调用次数：0",
				"缓存率：45.97 %",
				"* 相较于前一天：输入+ 270.01% | 缓存+ 209.32% | 输出+ 321.15% | 总计+ 278.18% | 缓存率：- 9.02%",
				"[提供商历史以来总计]",
				"输入Tokens：99,999",
				"缓存Tokens：50,000",
				"输出Tokens：20,000",
				"总计Tokens：119,999",
				"总调用次数：0",
				"缓存率：50.00 %",
			},
		},
		{
			name: "missing yesterday data still renders safely",
			stats: ProviderStats{
				Timezone:      "Asia/Shanghai",
				TodayDate:     "2026-03-27",
				YesterdayDate: "",
				Yesterday:     TokenTotals{},
				Today: TokenTotals{
					InputTokens:  1000,
					CachedTokens: 500,
					OutputTokens: 200,
					TotalTokens:  1200,
				},
				HistoryTotal: TokenTotals{
					InputTokens:  1000,
					CachedTokens: 500,
					OutputTokens: 200,
					TotalTokens:  1200,
				},
				UpdatedAt: now,
			},
			contains: []string{
				"[前一天]",
				"输入Tokens：0",
				"缓存Tokens：0",
				"输出Tokens：0",
				"总计Tokens：0",
				"总调用次数：0",
				"缓存率：0.00 %",
				"* 相较于前一天：输入 = | 缓存 = | 输出 = | 总计 = | 缓存率： =",
				"[今天]",
				"输入Tokens：1,000",
				"缓存Tokens：500",
				"输出Tokens：200",
				"总计Tokens：1,200",
				"总调用次数：0",
				"缓存率：50.00 %",
				"* 相较于前一天：输入+ 1,000 | 缓存+ 500 | 输出+ 200 | 总计+ 1,200 | 缓存率：+ 50.00%",
			},
		},
		{
			name: "both yesterday and today zero",
			stats: ProviderStats{
				Timezone:      "Asia/Shanghai",
				TodayDate:     "2026-03-27",
				YesterdayDate: "2026-03-26",
				Yesterday:     TokenTotals{},
				Today:         TokenTotals{},
				HistoryTotal:  TokenTotals{},
				UpdatedAt:     now,
			},
			contains: []string{
				"[前一天]",
				"输入Tokens：0",
				"缓存Tokens：0",
				"输出Tokens：0",
				"总计Tokens：0",
				"总调用次数：0",
				"缓存率：0.00 %",
				"* 相较于前一天：输入 = | 缓存 = | 输出 = | 总计 = | 缓存率： =",
				"[今天]",
				"输入Tokens：0",
				"缓存Tokens：0",
				"输出Tokens：0",
				"总计Tokens：0",
				"总调用次数：0",
				"缓存率：0.00 %",
				"* 相较于前一天：输入 = | 缓存 = | 输出 = | 总计 = | 缓存率： =",
			},
		},
		{
			name: "renders request count for successful calls",
			stats: ProviderStats{
				Timezone: "Asia/Shanghai",
				RecentDays: []DailyStats{{
					Date:   "2026-03-27",
					Totals: TokenTotals{InputTokens: 100, CachedTokens: 25, CacheCreationTokens: 10, OutputTokens: 40, TotalTokens: 140, RequestCount: 3},
				}},
				HistoryTotal: TokenTotals{InputTokens: 100, CachedTokens: 25, CacheCreationTokens: 10, OutputTokens: 40, TotalTokens: 140, RequestCount: 3},
				UpdatedAt:    now,
			},
			contains: []string{
				"总调用次数：3",
				"缓存Tokens：25",
			},
		},
		{
			name: "older blocks use date labels within 7 day history",
			stats: ProviderStats{
				Timezone: "Asia/Shanghai",
				RecentDays: []DailyStats{
					{Date: "2026-03-21", Totals: TokenTotals{InputTokens: 50, TotalTokens: 50}},
					{Date: "2026-03-22", Totals: TokenTotals{InputTokens: 100, TotalTokens: 100}},
					{Date: "2026-03-27", Totals: TokenTotals{InputTokens: 300, TotalTokens: 300}},
					{Date: "2026-03-28", Totals: TokenTotals{InputTokens: 600, TotalTokens: 600}},
				},
				HistoryTotal: TokenTotals{InputTokens: 1050, TotalTokens: 1050},
				UpdatedAt:    now,
			},
			contains: []string{
				"[2026-03-21]",
				"[2026-03-22]",
				"[前一天]",
				"[今天]",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RenderProviderStats(tt.stats)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("RenderProviderStats() missing %q in:\n%s", want, got)
				}
			}
		})
	}
}

func TestRenderProviderStatsOmitsCacheCreationLabel(t *testing.T) {
	got := RenderProviderStats(ProviderStats{
		RecentDays: []DailyStats{{
			Date:   "2026-03-27",
			Totals: TokenTotals{InputTokens: 100, CachedTokens: 25, CacheCreationTokens: 10, OutputTokens: 40, TotalTokens: 140, RequestCount: 3},
		}},
		HistoryTotal: TokenTotals{InputTokens: 100, CachedTokens: 25, CacheCreationTokens: 10, OutputTokens: 40, TotalTokens: 140, RequestCount: 3},
	})
	if strings.Contains(got, "缓存创建Tokens") || strings.Contains(got, "缓存创建") {
		t.Fatalf("expected txt rendering to omit cache creation wording, got:\n%s", got)
	}
}
