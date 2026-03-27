package httpapi

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"

	"openai-compat-proxy/internal/cacheinfo"
)

func cacheInfoUsageRecorder(r *http.Request, requestID, providerID string) usageRecorderFunc {
	manager := cacheInfoManagerFromRequest(r)
	if manager == nil {
		return nil
	}
	requestID = strings.TrimSpace(requestID)
	providerID = strings.TrimSpace(providerID)
	if requestID == "" || providerID == "" {
		return nil
	}
	return func(usage map[string]any) {
		if usage == nil {
			return
		}
		parsed, ok := cacheInfoUsageFromMap(usage)
		if !ok {
			return
		}
		_ = manager.RecordFinalUsage(requestID, providerID, parsed)
	}
}

func cacheInfoUsageFromMap(usage map[string]any) (*cacheinfo.Usage, bool) {
	if len(usage) == 0 {
		return nil, false
	}
	parsed := &cacheinfo.Usage{}
	var hasValues bool
	if n, ok := usageNumberAsInt64(usage["input_tokens"]); ok {
		parsed.InputTokens = n
		hasValues = true
	}
	if parsed.InputTokens == 0 {
		if n, ok := usageNumberAsInt64(usage["prompt_tokens"]); ok {
			parsed.InputTokens = n
			hasValues = true
		}
	}
	if n, ok := usageNumberAsInt64(usage["output_tokens"]); ok {
		parsed.OutputTokens = n
		hasValues = true
	}
	if parsed.OutputTokens == 0 {
		if n, ok := usageNumberAsInt64(usage["completion_tokens"]); ok {
			parsed.OutputTokens = n
			hasValues = true
		}
	}
	if n, ok := usageNumberAsInt64(usage["total_tokens"]); ok {
		parsed.TotalTokens = n
		hasValues = true
	}
	if n, ok := cachedTokensFromUsage(usage); ok {
		parsed.CachedTokens = n
		hasValues = true
	}
	if !hasValues {
		return nil, false
	}
	if parsed.TotalTokens == 0 {
		sum := parsed.InputTokens + parsed.OutputTokens
		if sum > 0 {
			parsed.TotalTokens = sum
		}
	}
	return parsed, true
}

func cachedTokensFromUsage(usage map[string]any) (int64, bool) {
	if details, _ := usage["input_tokens_details"].(map[string]any); len(details) > 0 {
		if n, ok := usageNumberAsInt64(details["cached_tokens"]); ok {
			return n, true
		}
	}
	if details, _ := usage["prompt_tokens_details"].(map[string]any); len(details) > 0 {
		if n, ok := usageNumberAsInt64(details["cached_tokens"]); ok {
			return n, true
		}
	}
	if n, ok := usageNumberAsInt64(usage["cache_read_input_tokens"]); ok {
		return n, true
	}
	if n, ok := usageNumberAsInt64(usage["cached_tokens"]); ok {
		return n, true
	}
	return 0, false
}

func usageNumberAsInt64(value any) (int64, bool) {
	switch n := value.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(math.Round(n)), true
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i, true
		}
		if f, err := n.Float64(); err == nil {
			return int64(math.Round(f)), true
		}
	}
	return 0, false
}
