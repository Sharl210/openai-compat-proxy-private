package cacheinfo

import (
	"fmt"
	"strings"
)

func RenderProviderStats(stats ProviderStats) string {
	var b strings.Builder

	renderSection := func(title string, totals TokenTotals) {
		fmt.Fprintf(&b, "[%s]\n", title)
		fmt.Fprintf(&b, "输入Tokens：%s\n", formatTokenCount(totals.InputTokens))
		fmt.Fprintf(&b, "缓存Tokens：%s\n", formatTokenCount(totals.CachedTokens))
		fmt.Fprintf(&b, "输出Tokens：%s\n", formatTokenCount(totals.OutputTokens))
		fmt.Fprintf(&b, "总计Tokens：%s\n", formatTokenCount(totals.TotalTokens))
		fmt.Fprintf(&b, "总调用次数：%s\n", formatTokenCount(totals.RequestCount))
		fmt.Fprintf(&b, "缓存率：%.2f %%\n", cacheRate(totals))
	}

	days := renderDays(stats)
	for i, day := range days {
		title := day.Date
		switch {
		case i == len(days)-1:
			title = "今天"
		case i == len(days)-2:
			title = "前一天"
		}
		renderSection(title, day.Totals)
		baseline := TokenTotals{}
		if i > 0 {
			baseline = days[i-1].Totals
		}
		b.WriteString("* 相较于前一天：")
		b.WriteString(formatComparison(baseline, day.Totals))
		b.WriteString("\n")
		if i != len(days)-1 {
			b.WriteString("\n")
		} else {
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")

	renderSection("提供商历史以来总计", stats.HistoryTotal)

	return b.String()
}

func renderDays(stats ProviderStats) []DailyStats {
	if len(stats.RecentDays) > 0 {
		return stats.RecentDays
	}
	days := make([]DailyStats, 0, 2)
	if stats.YesterdayDate != "" || stats.Yesterday != (TokenTotals{}) || stats.TodayDate != "" || stats.Today != (TokenTotals{}) {
		days = append(days, DailyStats{Date: stats.YesterdayDate, Totals: stats.Yesterday})
	}
	if stats.TodayDate != "" || stats.Today != (TokenTotals{}) {
		days = append(days, DailyStats{Date: stats.TodayDate, Totals: stats.Today})
	}
	if len(days) == 0 {
		days = append(days, DailyStats{Date: "今天"})
	}
	return days
}

func cacheRate(t TokenTotals) float64 {
	if t.InputTokens == 0 {
		return 0
	}
	return float64(t.CachedTokens*10000/t.InputTokens) / 100
}

func formatComparison(yesterday, today TokenTotals) string {
	rateYesterday := cacheRate(yesterday)
	rateToday := cacheRate(today)

	parts := []string{
		formatChange("输入", yesterday.InputTokens, today.InputTokens),
		formatChange("缓存", yesterday.CachedTokens, today.CachedTokens),
		formatChange("输出", yesterday.OutputTokens, today.OutputTokens),
		formatChange("总计", yesterday.TotalTokens, today.TotalTokens),
		formatRateChange(rateYesterday, rateToday),
	}
	return strings.Join(parts, " | ")
}

func formatChange(label string, old, new int64) string {
	if old == 0 {
		return label + formatSignedTokenDiff(new-old)
	}
	pct := float64(new-old) / float64(old) * 100
	if pct == 0 {
		return label + "-"
	}
	sign := "+ "
	if pct < 0 {
		sign = "- "
		pct = -pct
	}
	return fmt.Sprintf("%s%s%.2f%%", label, sign, pct)
}

func formatRateChange(old, new float64) string {
	if old == 0 {
		if new == 0 {
			return "缓存率： ="
		}
		return fmt.Sprintf("缓存率：+ %.2f%%", new)
	}
	diff := new - old
	if diff == 0 {
		return "缓存率： ="
	}
	sign := "+ "
	if diff < 0 {
		sign = "- "
		diff = -diff
	}
	return fmt.Sprintf("缓存率：%s%.2f%%", sign, diff)
}

func formatTokenCount(n int64) string {
	negative := n < 0
	if negative {
		n = -n
	}
	text := fmt.Sprintf("%d", n)
	if len(text) <= 3 {
		if negative {
			return "-" + text
		}
		return text
	}
	var parts []string
	for len(text) > 3 {
		parts = append([]string{text[len(text)-3:]}, parts...)
		text = text[:len(text)-3]
	}
	parts = append([]string{text}, parts...)
	joined := strings.Join(parts, ",")
	if negative {
		return "-" + joined
	}
	return joined
}

func formatSignedTokenDiff(diff int64) string {
	if diff > 0 {
		return "+ " + formatTokenCount(diff)
	}
	if diff < 0 {
		return "- " + formatTokenCount(-diff)
	}
	return " ="
}
