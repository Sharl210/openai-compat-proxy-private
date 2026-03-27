package cacheinfo

import (
	"fmt"
	"strings"
)

func RenderProviderStats(stats ProviderStats) string {
	var b strings.Builder

	renderSection := func(title string, totals TokenTotals) {
		fmt.Fprintf(&b, "[%s]\n", title)
		fmt.Fprintf(&b, "输入Tokens：%d\n", totals.InputTokens)
		fmt.Fprintf(&b, "缓存Tokens：%d\n", totals.CachedTokens)
		fmt.Fprintf(&b, "输出Tokens：%d\n", totals.OutputTokens)
		fmt.Fprintf(&b, "总计Tokens：%d\n", totals.TotalTokens)
		fmt.Fprintf(&b, "缓存率：%.2f %%\n", cacheRate(totals))
	}

	renderSection("昨日", stats.Yesterday)
	b.WriteString("\n")
	renderSection("今日", stats.Today)

	b.WriteString("* 相较于昨日：")
	b.WriteString(formatComparison(stats.Yesterday, stats.Today))
	b.WriteString("\n\n")

	renderSection("提供商历史以来总计", stats.HistoryTotal)

	return b.String()
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
		if new == 0 {
			return label + "0.00%"
		}
		return label + "新增"
	}
	pct := float64(new-old) / float64(old) * 100
	sign := "+"
	if pct < 0 {
		sign = ""
	}
	return fmt.Sprintf("%s%s%.2f%%", label, sign, pct)
}

func formatRateChange(old, new float64) string {
	if old == 0 {
		if new == 0 {
			return "缓存率：0.00%"
		}
		return "缓存率：新增"
	}
	diff := new - old
	sign := "+"
	if diff < 0 {
		sign = ""
	}
	return fmt.Sprintf("缓存率：%s%.2f%%", sign, diff)
}
