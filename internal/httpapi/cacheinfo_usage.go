package httpapi

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"

	"openai-compat-proxy/internal/cacheinfo"
	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/tokenestimator"
)

func cacheInfoUsageRecorder(r *http.Request, requestID, providerID, upstreamEndpointType string) usageRecorderFunc {
	manager := cacheInfoManagerFromRequest(r)
	if manager == nil {
		return nil
	}
	if bypassProviderModelAllowanceForRequest(r) || shouldBypassUsageRecorderForRequest(r) {
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

func tokenEstimatorUsageRecorder(ctx context.Context, requestID string, upstreamEndpointType string, bypass bool) usageRecorderFunc {
	if ctx == nil {
		return nil
	}
	manager, _ := ctx.Value(tokenEstimatorManagerKey).(*tokenestimator.Manager)
	if manager == nil {
		return nil
	}
	if bypass {
		return nil
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil
	}
	return func(usage map[string]any) {
		if usage == nil {
			return
		}
		parsed, ok := usageTotalsFromMap(usage, upstreamEndpointType)
		if !ok {
			return
		}
		_ = recordTokenEstimatorUsage(ctx, requestID, parsed)
	}
}

func combinedUsageRecorder(recorders ...usageRecorderFunc) usageRecorderFunc {
	active := make([]usageRecorderFunc, 0, len(recorders))
	for _, recorder := range recorders {
		if recorder != nil {
			active = append(active, recorder)
		}
	}
	if len(active) == 0 {
		return nil
	}
	return func(usage map[string]any) {
		for _, recorder := range active {
			recorder(usage)
		}
	}
}

func shouldBypassUsageRecorderForRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	path := strings.TrimSpace(r.URL.Path)
	switch path {
	case canonicalV1ImagesGenerationsPath,
		canonicalV1ImagesEditsPath,
		canonicalV1ImagesVariationsPath,
		canonicalV1EmbeddingsPath,
		canonicalV1RerankPath,
		"/images/generations",
		"/images/edits",
		"/images/variations",
		"/embeddings",
		"/rerank":
		return true
	default:
		if strings.Contains(path, "/images/") || strings.HasSuffix(path, "/embeddings") || strings.HasSuffix(path, "/rerank") {
			return true
		}
		return false
	}
}

func cacheInfoUsageFromMap(usage map[string]any, upstreamEndpointType string) (*cacheinfo.Usage, bool) {
	parsed, ok := parseUsageMetrics(usage, upstreamEndpointType)
	if !ok {
		return nil, false
	}
	return parsed.cacheInfoUsage(), true
}

type usageMetrics struct {
	InputTokens         int64
	OutputTokens        int64
	TotalTokens         int64
	CachedTokens        int64
	CacheCreationTokens int64
}

func parseUsageMetrics(usage map[string]any, upstreamEndpointType string) (usageMetrics, bool) {
	if len(usage) == 0 {
		return usageMetrics{}, false
	}
	parsed := usageMetrics{}
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
		return usageMetrics{}, false
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

func (m usageMetrics) cacheInfoUsage() *cacheinfo.Usage {
	return &cacheinfo.Usage{
		InputTokens:         m.InputTokens,
		OutputTokens:        m.OutputTokens,
		TotalTokens:         m.TotalTokens,
		CachedTokens:        m.CachedTokens,
		CacheCreationTokens: m.CacheCreationTokens,
	}
}

func usageTotalsFromMap(usage map[string]any, upstreamEndpointType string) (usageTotals, bool) {
	metrics, ok := parseUsageMetrics(usage, upstreamEndpointType)
	if !ok {
		return usageTotals{}, false
	}
	return usageTotals{
		InputTokens:  metrics.InputTokens,
		CachedTokens: metrics.CachedTokens,
	}, true
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
