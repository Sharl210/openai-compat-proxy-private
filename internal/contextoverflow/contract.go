package contextoverflow

import "strings"

const ClientMessage = "prompt is too long: context_length_exceeded"

func Normalize(code string, message string) (string, string, bool) {
	if !IsSignal(code, message) {
		return code, message, false
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = ClientMessage
	} else if !isClientRecognizedMessage(message) {
		message = ClientMessage + ": " + message
	}
	return "context_length_exceeded", message, true
}

func NormalizeCandidates(candidates []string, message string) (string, string, bool) {
	primaryCode := ""
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if primaryCode == "" {
			primaryCode = candidate
		}
		if IsQuotaCode(candidate) {
			return primaryCode, message, false
		}
	}
	for _, candidate := range candidates {
		if code, normalizedMessage, ok := Normalize(candidate, message); ok {
			return code, normalizedMessage, true
		}
	}
	return primaryCode, message, false
}

func IsSignal(code string, message string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "context_length_exceeded", "context_too_large", "model_context_window_exceeded":
		return true
	case "insufficient_quota", "quota_exceeded", "billing_hard_limit_reached", "rate_limit_exceeded":
		return false
	}
	message = strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(message, "context_length_exceeded") ||
		strings.Contains(message, "prompt is too long") ||
		strings.Contains(message, "context window") ||
		strings.Contains(message, "context length") ||
		strings.Contains(message, "context too large") ||
		strings.Contains(message, "too many tokens") ||
		(strings.Contains(message, "token limit") && hasContextualTokenSubject(message)) ||
		(strings.Contains(message, "input token") && strings.Contains(message, "exceed")) ||
		(strings.Contains(message, "reduce the length") && strings.Contains(message, "messages"))
}

func IsQuotaCode(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "insufficient_quota", "quota_exceeded", "billing_hard_limit_reached", "rate_limit_exceeded":
		return true
	default:
		return false
	}
}

func hasContextualTokenSubject(message string) bool {
	return strings.Contains(message, "input") ||
		strings.Contains(message, "prompt") ||
		strings.Contains(message, "context")
}

func isClientRecognizedMessage(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(message, "context_length_exceeded") && strings.Contains(message, "prompt is too long")
}
