package httpapi

import (
	"encoding/json"
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/errorsx"
	modelpkg "openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/tokenestimator"
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
		w.Header().Del(headerProxyEstimatedInputTokens)
		return false
	}
	rawEstimatedTokens := estimateCanonicalInputTokens(canon)
	effectiveLimit := limit
	confidence := "cold"
	if ctx != nil {
		effectiveLimit = conservativeContextAdmissionLimit(ctx, limit, canon)
		if current := currentEstimatorConfidence(ctx, canon); current != "" {
			confidence = current
		}
	}
	displayedEstimatedTokens := presentDisplayedEstimatedTokens(rawEstimatedTokens, effectiveLimit, limit)
	displayedEstimateText := formatDisplayedEstimatedTokens(displayedEstimatedTokens, confidence)
	w.Header().Set(headerProxyEstimatedInputTokens, displayedEstimateText)
	if rawEstimatedTokens <= effectiveLimit {
		return false
	}
	message := buildContextLimitExceededMessage(displayedEstimateText, strconv.Itoa(limit))
	switch protocol {
	case clientReasoningProtocolMessages:
		writeAnthropicContextLimitExceeded(w, message)
	default:
		errorsx.WriteJSON(w, http.StatusBadRequest, "context_length_exceeded", message)
	}
	return true
}

func currentEstimatorConfidence(ctx context.Context, canon modelpkg.CanonicalRequest) string {
	if ctx == nil {
		return ""
	}
	mgr, _ := ctx.Value(tokenEstimatorManagerKey).(*tokenestimator.Manager)
	if mgr == nil {
		return ""
	}
	input, _ := ctx.Value(tokenEstimatorObservationKey).(tokenEstimatorObservationInput)
	if input.ProviderID == "" || input.EndpointType == "" || input.FinalUpstreamModel == "" {
		return ""
	}
	key := tokenestimator.BucketKey{
		ProviderID:   strings.TrimSpace(input.ProviderID),
		EndpointType: strings.TrimSpace(input.EndpointType),
		Model:        strings.TrimSpace(input.FinalUpstreamModel),
	}
	state := mgr.GetBucketState(key)
	if state == nil {
		return ""
	}
	return strings.TrimSpace(state.ConfidenceLevel)
}

func presentDisplayedEstimatedTokens(rawEstimatedTokens int, effectiveLimit int, configuredLimit int) int {
	if rawEstimatedTokens <= 0 {
		return 0
	}
	if effectiveLimit <= 0 || configuredLimit <= 0 || effectiveLimit >= configuredLimit {
		return rawEstimatedTokens
	}
	scaled := int(math.Round(float64(rawEstimatedTokens) / float64(effectiveLimit) * float64(configuredLimit)))
	if scaled < 1 {
		return 1
	}
	return scaled
}

func formatDisplayedEstimatedTokens(tokens int, confidence string) string {
	if confidence == "" {
		confidence = "cold"
	}
	return strconv.Itoa(tokens) + "(置信度:" + confidence + ")"
}


func conservativeContextAdmissionLimit(ctx context.Context, configuredLimit int, canon modelpkg.CanonicalRequest) int {
	mgr, _ := ctx.Value(tokenEstimatorManagerKey).(*tokenestimator.Manager)
	if mgr == nil {
		return configuredLimit
	}
	input, _ := ctx.Value(tokenEstimatorObservationKey).(tokenEstimatorObservationInput)
	if input.ProviderID == "" || input.EndpointType == "" || input.FinalUpstreamModel == "" {
		return configuredLimit
	}
	key := tokenestimator.BucketKey{
		ProviderID:   strings.TrimSpace(input.ProviderID),
		EndpointType: strings.TrimSpace(input.EndpointType),
		Model:        strings.TrimSpace(input.FinalUpstreamModel),
	}
	shape := tokenestimator.ShapeClass("")
	snap := buildEstimatorSnapshot(canon)
	shape = classifyEstimatorShape(snap)
	if tightened, ok := mgr.ConservativeAdmissionLimit(key, configuredLimit, shape); ok {
		return tightened
	}
	return configuredLimit
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

func estimateCanonicalInputTokens(canon modelpkg.CanonicalRequest) int {
	snap := buildEstimatorSnapshot(canon)
	if snap.BaseEstimate <= 0 {
		return 0
	}
	return int(snap.BaseEstimate)
}

func estimateContentPartChars(part modelpkg.CanonicalContentPart) int {
	chars := utf8.RuneCountInString(part.Type)
	chars += utf8.RuneCountInString(part.Text)
	chars += utf8.RuneCountInString(part.ImageURL)
	chars += utf8.RuneCountInString(part.MimeType)
	if encoded, err := json.Marshal(part.Raw); err == nil {
		chars += utf8.RuneCount(encoded)
	}
	return chars
}
