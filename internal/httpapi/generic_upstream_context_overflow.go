package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"openai-compat-proxy/internal/upstream"
)

const genericUpstreamContextOverflowEstimatedTokenFloor = 1_000_000

func normalizeRetryExhaustedGeneric502ContextOverflow(w http.ResponseWriter, httpErr *upstream.HTTPStatusError) (string, string, bool) {
	if httpErr == nil ||
		httpErr.StatusCode != http.StatusBadGateway ||
		httpErr.RetriesPerformed < 1 ||
		httpErr.RetryEvidence.AttemptCount < 2 ||
		!httpErr.RetryEvidence.AllAttemptsMatchFinal ||
		w.Header().Get(headerProxyModelLimitContextTokens) != "-1" ||
		!isStrictGenericTemporaryUnavailableResponse(httpErr.BodyBytes) {
		return "", "", false
	}
	estimatedTokens, err := parseEstimatedInputTokensHeader(w.Header().Get(headerProxyEstimatedInputTokens))
	if err != nil || estimatedTokens < genericUpstreamContextOverflowEstimatedTokenFloor {
		return "", "", false
	}
	message := fmt.Sprintf(
		"prompt is too long: context_length_exceeded: inferred after %d identical HTTP 502 upstream failures with estimated input tokens %d",
		httpErr.RetryEvidence.AttemptCount,
		estimatedTokens,
	)
	return "context_length_exceeded", message, true
}

func parseEstimatedInputTokensHeader(value string) (int, error) {
	if openingParenthesis := strings.IndexByte(value, '('); openingParenthesis >= 0 {
		value = value[:openingParenthesis]
	}
	firstField := strings.Fields(value)
	if len(firstField) == 0 {
		return 0, fmt.Errorf("estimated input tokens header is empty")
	}
	return strconv.Atoi(firstField[0])
}

func isStrictGenericTemporaryUnavailableResponse(body []byte) bool {
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(body, &topLevel); err != nil || len(topLevel) != 1 {
		return false
	}
	rawError, ok := topLevel["error"]
	if !ok {
		return false
	}
	var errorFields map[string]json.RawMessage
	if err := json.Unmarshal(rawError, &errorFields); err != nil || len(errorFields) != 2 {
		return false
	}
	var message string
	if err := json.Unmarshal(errorFields["message"], &message); err != nil {
		return false
	}
	var errorType string
	if err := json.Unmarshal(errorFields["type"], &errorType); err != nil {
		return false
	}
	return message == "Upstream service temporarily unavailable" && errorType == "upstream_error"
}
