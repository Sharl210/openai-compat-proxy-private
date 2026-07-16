package model

import "strings"

// IsSyntheticResponsesReasoningPlaceholder reports whether item is the proxy's
// own synthetic Responses reasoning lifecycle placeholder.
func IsSyntheticResponsesReasoningPlaceholder(item map[string]any) bool {
	if stringMapValue(item, "type") != "reasoning" || stringMapValue(item, "id") != "rs_proxy" {
		return false
	}
	if HasResponsesReasoningState(item) {
		return false
	}
	if summary, hasSummary := item["summary"]; hasSummary {
		return isSyntheticResponsesReasoningSummary(summary)
	}
	return isSyntheticResponsesReasoningText(stringMapValue(item, "thinking")) && isSyntheticResponsesReasoningText(stringMapValue(item, "text"))
}

// HasResponsesReasoningState reports whether a Responses reasoning item carries
// state beyond its display-only summary or text fields.
func HasResponsesReasoningState(item map[string]any) bool {
	for key := range item {
		switch key {
		case "id", "type", "summary", "thinking", "text":
			continue
		default:
			return true
		}
	}
	return false
}

func isSyntheticResponsesReasoningSummary(summary any) bool {
	switch typed := summary.(type) {
	case nil:
		return true
	case []any:
		return allSyntheticResponsesReasoningSummaryItems(typed)
	case []map[string]any:
		items := make([]any, len(typed))
		for index := range typed {
			items[index] = typed[index]
		}
		return allSyntheticResponsesReasoningSummaryItems(items)
	default:
		return isSyntheticResponsesReasoningSummaryItem(typed)
	}
}

func allSyntheticResponsesReasoningSummaryItems(items []any) bool {
	for _, item := range items {
		if !isSyntheticResponsesReasoningSummaryItem(item) {
			return false
		}
	}
	return true
}

func isSyntheticResponsesReasoningSummaryItem(item any) bool {
	switch typed := item.(type) {
	case nil:
		return true
	case string:
		return isSyntheticResponsesReasoningText(typed)
	case map[string]any:
		return isSyntheticResponsesReasoningSummaryMap(typed)
	default:
		return false
	}
}

func isSyntheticResponsesReasoningSummaryMap(item map[string]any) bool {
	if len(item) == 0 {
		return true
	}
	for key, value := range item {
		switch key {
		case "type":
			continue
		case "text":
			if !isSyntheticResponsesReasoningText(stringMapValue(item, "text")) {
				return false
			}
		case "summary_text":
			switch nested := value.(type) {
			case string:
				if !isSyntheticResponsesReasoningText(nested) {
					return false
				}
			case map[string]any:
				if !isSyntheticResponsesReasoningText(stringMapValue(nested, "text")) {
					return false
				}
			default:
				return false
			}
		default:
			return false
		}
	}
	return true
}

func isSyntheticResponsesReasoningText(text string) bool {
	stripped := strings.ReplaceAll(text, "\u200b", "")
	stripped = strings.ReplaceAll(stripped, "\ufeff", "")
	return strings.TrimSpace(stripped) == "" || strings.Contains(strings.TrimSpace(stripped), "代理层占位")
}

func stringMapValue(item map[string]any, key string) string {
	value, _ := item[key].(string)
	return value
}
