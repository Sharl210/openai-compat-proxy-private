package httpapi

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"

	"openai-compat-proxy/internal/cacheinfo"
	"openai-compat-proxy/internal/config"
)

func cacheInfoUsageRecorder(r *http.Request, requestID, providerID, upstreamEndpointType string) usageRecorderFunc {
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
		parsed, ok := cacheInfoUsageFromMap(usage, upstreamEndpointType)
		if !ok {
			return
		}
		_ = manager.RecordFinalUsage(requestID, providerID, parsed)
	}
}

func cacheInfoUsageFromMap(usage map[string]any, upstreamEndpointType string) (*cacheinfo.Usage, bool) {
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
	if n, ok := cacheCreationTokensFromUsage(usage); ok {
		parsed.CacheCreationTokens = n
		hasValues = true
	}
	if !hasValues {
		return nil, false
	}
	if upstreamEndpointType == config.UpstreamEndpointTypeAnthropic && anthropicUsageNeedsTotalNormalization(usage) {
		parsed.InputTokens += parsed.CachedTokens + parsed.CacheCreationTokens
	}
	if parsed.TotalTokens == 0 {
		sum := parsed.InputTokens + parsed.OutputTokens
		if sum > 0 {
			parsed.TotalTokens = sum
		}
	} else if upstreamEndpointType == config.UpstreamEndpointTypeAnthropic && anthropicUsageNeedsTotalNormalization(usage) {
		parsed.TotalTokens = parsed.InputTokens + parsed.OutputTokens
	}
	return parsed, true
}

func anthropicUsageNeedsTotalNormalization(usage map[string]any) bool {
	if len(usage) == 0 {
		return false
	}
	hasTopLevelAnthropicCache := usage["cache_read_input_tokens"] != nil || usage["cache_creation_input_tokens"] != nil
	if !hasTopLevelAnthropicCache {
		return false
	}
	if details, _ := usage["input_tokens_details"].(map[string]any); len(details) > 0 {
		if details["cached_tokens"] != nil || details["cache_creation_tokens"] != nil {
			return false
		}
	}
	return true
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

func cacheCreationTokensFromUsage(usage map[string]any) (int64, bool) {
	if details, _ := usage["input_tokens_details"].(map[string]any); len(details) > 0 {
		if n, ok := usageNumberAsInt64(details["cache_creation_tokens"]); ok {
			return n, true
		}
	}
	if details, _ := usage["prompt_tokens_details"].(map[string]any); len(details) > 0 {
		if n, ok := usageNumberAsInt64(details["cache_creation_tokens"]); ok {
			return n, true
		}
	}
	if n, ok := usageNumberAsInt64(usage["cache_creation_input_tokens"]); ok {
		return n, true
	}
	if n, ok := usageNumberAsInt64(usage["cache_creation_tokens"]); ok {
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
