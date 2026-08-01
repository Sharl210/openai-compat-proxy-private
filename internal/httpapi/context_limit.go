package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/errorsx"
	modelpkg "openai-compat-proxy/internal/model"
)

const contextOverflowMessage = "prompt is too long: context_length_exceeded by proxy model limit"

func setProxyModelLimitContextHeader(w http.ResponseWriter, provider config.ProviderConfig, canon modelpkg.CanonicalRequest) int {
	effort := ""
	if canon.Reasoning != nil {
		effort = strings.TrimSpace(canon.Reasoning.Effort)
	}
	limit := provider.ResolveModelLimitContextTokensForReasoning(strings.TrimSpace(canon.Model), effort)
	w.Header().Set(headerProxyModelLimitContextTokens, strconv.Itoa(limit))
	return limit
}

func writeContextLimitExceededIfNeeded(ctx context.Context, w http.ResponseWriter, provider config.ProviderConfig, canon modelpkg.CanonicalRequest, protocol string) bool {
	limit := setProxyModelLimitContextHeader(w, provider, canon)
	if limit < 0 {
		return false
	}
	estimate := estimatorEstimateForContext(ctx, canon)
	w.Header().Set(headerProxyEstimatedInputTokens, formatEstimatedInputTokensHeader(ctx, canon, estimate))
	if !estimateAllowsLocalContextBlock(estimate, int64(limit)) {
		return false
	}
	clearClaudeMetadataObservabilityHeaders(w)
	message := buildContextLimitExceededMessage(strconv.FormatInt(estimate.Point, 10), strconv.Itoa(limit))
	switch protocol {
	case clientReasoningProtocolMessages:
		writeAnthropicContextLimitExceeded(w, message)
	default:
		errorsx.WriteJSON(w, http.StatusBadRequest, "context_length_exceeded", message)
	}
	return true
}

func estimatorEstimateForContext(ctx context.Context, canon modelpkg.CanonicalRequest) estimatorEstimate {
	if input, ok := tokenEstimatorObservationFromContext(ctx); ok {
		return estimateFromObservationInput(ctx, input)
	}
	snap := buildEstimatorSnapshot(canon)
	return localEstimatorInterval(snap.StaticUnits+snap.DynamicUnits, estimatorStaticStructuralUnitsFromSnapshot(snap), snap.BaseEstimate)
}

func estimateAllowsLocalContextBlock(estimate estimatorEstimate, limit int64) bool {
	if limit < 0 || estimate.Point <= 0 {
		return false
	}
	if estimate.Source != "exact-prefix" || estimate.Confidence != "exact" {
		return estimate.Source == "local" && estimate.Lower > limit && limit <= 1
	}
	return estimate.Lower > limit
}

func buildContextLimitExceededMessage(estimatedTokens string, limit string) string {
	if strings.TrimSpace(estimatedTokens) == "" || strings.TrimSpace(limit) == "" {
		return contextOverflowMessage
	}
	return contextOverflowMessage + ": estimated input tokens " + estimatedTokens + " exceed maximum " + limit
}

func writeAnthropicContextLimitExceeded(w http.ResponseWriter, message string) {
	payload := map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    "invalid_request_error",
			"message": message,
			"code":    "context_length_exceeded",
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		errorsx.WriteJSON(w, http.StatusBadRequest, "context_length_exceeded", message)
		return
	}
	errorsx.WriteRawJSON(w, http.StatusBadRequest, encoded)
}
