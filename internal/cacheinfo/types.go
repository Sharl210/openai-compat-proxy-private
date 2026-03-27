package cacheinfo

import "time"

// TokenTotals 表示 token 累计
type TokenTotals struct {
	InputTokens  int64 `json:"input_tokens"`
	CachedTokens int64 `json:"cached_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

// ProviderStats 表示单个 provider 的统计状态
type ProviderStats struct {
	Timezone      string      `json:"timezone"`
	TodayDate     string      `json:"today_date"`     // e.g. "2026-03-27"
	YesterdayDate string      `json:"yesterday_date"` // e.g. "2026-03-26"
	Today         TokenTotals `json:"today"`
	Yesterday     TokenTotals `json:"yesterday"`
	HistoryTotal  TokenTotals `json:"history_total"`
	UpdatedAt     time.Time   `json:"updated_at"`
}
