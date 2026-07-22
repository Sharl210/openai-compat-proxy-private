package reasoning

import "strings"

var reasoningTextKeys = []string{"summary", "thinking", "reasoning_content", "reasoning", "content", "delta", "text"}

func FormatText(text string) string {
	firstStart, firstEnd, secondEnd, ok := exactlyTwoAdjacentBoldSpans(text)
	if !ok {
		return text
	}
	return text[:firstStart] + "\n" + text[firstStart:firstEnd] + "\n\n" + text[firstEnd:secondEnd] + "\n" + text[secondEnd:]
}

func exactlyTwoAdjacentBoldSpans(text string) (firstStart, firstEnd, secondEnd int, ok bool) {
	if text == "" || !strings.Contains(text, "**") {
		return 0, 0, 0, false
	}
	for index := 0; index < len(text); {
		nextStart := strings.Index(text[index:], "**")
		if nextStart < 0 {
			break
		}
		firstStart = index + nextStart
		firstEnd, ok = completeBoldSpanEnd(text, firstStart)
		if !ok || !strings.HasPrefix(text[firstEnd:], "**") {
			index = firstStart + 2
			continue
		}
		secondEnd, ok = completeBoldSpanEnd(text, firstEnd)
		if !ok || strings.HasPrefix(text[secondEnd:], "**") {
			index = firstStart + 2
			continue
		}
		if strings.Contains(text[:firstStart], "**") || strings.Contains(text[secondEnd:], "**") {
			return 0, 0, 0, false
		}
		return firstStart, firstEnd, secondEnd, true
	}
	return 0, 0, 0, false
}

func completeBoldSpanEnd(text string, start int) (int, bool) {
	if !strings.HasPrefix(text[start:], "**") {
		return 0, false
	}
	closeOffset := strings.Index(text[start+2:], "**")
	if closeOffset < 1 {
		return 0, false
	}
	return start + 2 + closeOffset + 2, true
}

func FormatDelta(previous, delta string) (formattedDelta, combined string) {
	if delta == "" {
		return "", previous
	}
	candidate := previous + delta
	normalized := FormatText(candidate)
	if normalized == candidate {
		return delta, candidate
	}
	if strings.HasPrefix(normalized, previous) {
		return strings.TrimPrefix(normalized, previous), normalized
	}
	return delta, candidate
}

func FormatBlock(block map[string]any) map[string]any {
	if len(block) == 0 {
		return nil
	}
	formatted := make(map[string]any, len(block))
	for key, value := range block {
		formatted[key] = value
	}
	for _, key := range reasoningTextKeys {
		if text, ok := formatted[key].(string); ok {
			formatted[key] = FormatText(text)
		}
	}
	if parts, ok := formatted["summary"].([]any); ok {
		formattedParts := make([]any, len(parts))
		for index, rawPart := range parts {
			part, ok := rawPart.(map[string]any)
			if !ok {
				formattedParts[index] = rawPart
				continue
			}
			formattedPart := make(map[string]any, len(part))
			for key, value := range part {
				formattedPart[key] = value
			}
			if text, ok := formattedPart["text"].(string); ok {
				formattedPart["text"] = FormatText(text)
			}
			formattedParts[index] = formattedPart
		}
		formatted["summary"] = formattedParts
	}
	return formatted
}
