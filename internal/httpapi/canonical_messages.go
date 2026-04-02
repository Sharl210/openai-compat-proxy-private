package httpapi

import "openai-compat-proxy/internal/model"

func prepareCanonicalMessages(messages []model.CanonicalMessage) []model.CanonicalMessage {
	if len(messages) == 0 {
		return messages
	}
	filtered := make([]model.CanonicalMessage, 0, len(messages))
	for _, msg := range messages {
		if shouldDropToolMessageFromHistory(msg) {
			continue
		}
		filtered = append(filtered, msg)
	}
	if len(filtered) < 2 {
		return filtered
	}
	result := make([]model.CanonicalMessage, 0, len(filtered))
	for _, msg := range filtered {
		if len(result) > 0 && isDuplicateToolMessage(result[len(result)-1], msg) {
			continue
		}
		result = append(result, msg)
	}
	return result
}

func isDuplicateToolMessage(prev, curr model.CanonicalMessage) bool {
	if prev.Role != "tool" || curr.Role != "tool" {
		return false
	}
	if prev.ToolCallID == "" || prev.ToolCallID != curr.ToolCallID {
		return false
	}
	if len(prev.ToolCalls) != 0 || len(curr.ToolCalls) != 0 {
		return false
	}
	return canonicalPartsEqual(prev.Parts, curr.Parts)
}

func canonicalPartsEqual(a, b []model.CanonicalContentPart) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type || a[i].Text != b[i].Text || a[i].ImageURL != b[i].ImageURL || a[i].MimeType != b[i].MimeType {
			return false
		}
	}
	return true
}
