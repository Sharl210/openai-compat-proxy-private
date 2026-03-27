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
				"[昨日]",
				"输入Tokens：12345",
				"缓存Tokens：6789",
				"输出Tokens：2345",
				"总计Tokens：14690",
				"缓存率：54.99 %",
				"[今日]",
				"输入Tokens：45678",
				"缓存Tokens：21000",
				"输出Tokens：9876",
				"总计Tokens：55554",
				"缓存率：45.97 %",
				"* 相较于昨日：输入+270.01% | 缓存+209.32% | 输出+321.15% | 总计+278.18% | 缓存率：-9.02%",
				"[提供商历史以来总计]",
				"输入Tokens：99999",
				"缓存Tokens：50000",
				"输出Tokens：20000",
				"总计Tokens：119999",
				"缓存率：50.00 %",
			},
		},
		{
			name: "yesterday zero today has value",
			stats: ProviderStats{
				Timezone:      "Asia/Shanghai",
				TodayDate:     "2026-03-27",
				YesterdayDate: "2026-03-26",
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
				"[昨日]",
				"输入Tokens：0",
				"缓存Tokens：0",
				"输出Tokens：0",
				"总计Tokens：0",
				"缓存率：0.00 %",
				"[今日]",
				"输入Tokens：1000",
				"缓存Tokens：500",
				"输出Tokens：200",
				"总计Tokens：1200",
				"缓存率：50.00 %",
				"* 相较于昨日：输入新增 | 缓存新增 | 输出新增 | 总计新增 | 缓存率：新增",
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
				"[昨日]",
				"输入Tokens：0",
				"缓存Tokens：0",
				"输出Tokens：0",
				"总计Tokens：0",
				"缓存率：0.00 %",
				"[今日]",
				"输入Tokens：0",
				"缓存Tokens：0",
				"输出Tokens：0",
				"总计Tokens：0",
				"缓存率：0.00 %",
				"* 相较于昨日：输入0.00% | 缓存0.00% | 输出0.00% | 总计0.00% | 缓存率：0.00%",
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
