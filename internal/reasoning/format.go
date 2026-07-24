package reasoning

import "strings"

var reasoningTextKeys = []string{"summary", "thinking", "reasoning_content", "reasoning", "content", "delta", "text"}

func FormatText(text string) string {
	if text == "" || !strings.Contains(text, "**") {
		return text
	}

	var builder strings.Builder
	lineStart := true
	for index := 0; index < len(text); {
		if strings.HasPrefix(text[index:], "**") {
			closeOffset := strings.Index(text[index+2:], "**")
			if closeOffset >= 1 {
				endIndex := index + 2 + closeOffset + 2
				if !lineStart {
					builder.WriteByte('\n')
				}
				builder.WriteString(text[index:endIndex])
				lineStart = false
				if endIndex < len(text) && text[endIndex] != '\n' && text[endIndex] != '\r' {
					next := text[endIndex:]
					if strings.HasPrefix(next, "**") && strings.Index(next[2:], "**") >= 1 {
						builder.WriteString("\n\n")
					} else {
						builder.WriteByte('\n')
					}
					lineStart = true
				}
				index = endIndex
				continue
			}
		}

		builder.WriteByte(text[index])
		lineStart = text[index] == '\n'
		index++
	}
	return builder.String()
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
	if signature, ok := formatted["signature"].(string); ok && signature != "" {
		return formatted
	}
	for _, key := range reasoningTextKeys {
		if text, ok := formatted[key].(string); ok {
			formatted[key] = FormatText(text)
		}
	}
	if parts, ok := formatted["summary"].([]any); ok && parts != nil {
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
	if parts, ok := formatted["summary"].([]map[string]any); ok && parts != nil {
		formattedParts := make([]map[string]any, len(parts))
		for index, part := range parts {
			if part == nil {
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
