package reasoning

import "strings"

var reasoningTextKeys = []string{"summary", "thinking", "reasoning_content", "reasoning", "content", "delta", "text"}

// StreamFormatter formats reasoning text without changing bytes that were
// already emitted to a downstream stream.
type StreamFormatter struct {
	pending       []byte
	pendingStart  int
	boldScanStart int
	initialized   bool
	lineStart     bool
	afterHeading  bool
}

func (formatter *StreamFormatter) Reset() {
	if formatter == nil {
		return
	}
	formatter.pending = nil
	formatter.pendingStart = 0
	formatter.boldScanStart = 0
	formatter.initialized = true
	formatter.lineStart = true
	formatter.afterHeading = false
}

func (formatter *StreamFormatter) Push(delta string) string {
	if formatter == nil || delta == "" {
		return delta
	}
	formatter.ensureInitialized()
	formatter.pending = append(formatter.pending, delta...)
	return formatter.drain(false)
}

func (formatter *StreamFormatter) Finish() string {
	if formatter == nil {
		return ""
	}
	formatter.ensureInitialized()
	return formatter.drain(true)
}

// FinishAtBoundary flushes a streaming segment without changing a lone title.
func (formatter *StreamFormatter) FinishAtBoundary() string {
	if formatter == nil {
		return ""
	}
	output := formatter.Finish()
	if !formatter.afterHeading {
		return output
	}
	return output
}

func (formatter *StreamFormatter) ensureInitialized() {
	if formatter.initialized {
		return
	}
	formatter.initialized = true
	formatter.lineStart = true
}

func (formatter *StreamFormatter) drain(final bool) string {
	var output strings.Builder
	for len(formatter.pending)-formatter.pendingStart > 0 {
		pending := formatter.pending[formatter.pendingStart:]
		if formatter.afterHeading {
			if pending[0] == '\n' || pending[0] == '\r' {
				formatter.afterHeading = false
				continue
			}
			if pending[0] == '*' && len(pending) == 1 && !final {
				break
			}
			if startsWithBoldSpan(pending) {
				if endIndex := formatter.completeBoldSpanEnd(pending); endIndex > 0 {
					output.WriteString("\n\n")
					_, _ = output.Write(pending[:endIndex])
					formatter.lineStart = false
					formatter.afterHeading = true
					formatter.discardPendingPrefix(endIndex)
					continue
				}
				if !startsWithEmptyBoldSpan(pending) && !final {
					break
				}
			}
			formatter.afterHeading = false
			continue
		}

		if pending[0] == '*' && len(pending) == 1 && !final {
			break
		}
		if startsWithBoldSpan(pending) {
			if endIndex := formatter.completeBoldSpanEnd(pending); endIndex > 0 {
				_, _ = output.Write(pending[:endIndex])
				formatter.lineStart = false
				formatter.afterHeading = true
				formatter.discardPendingPrefix(endIndex)
				continue
			}
			if !startsWithEmptyBoldSpan(pending) && !final {
				break
			}
		}

		_ = output.WriteByte(pending[0])
		formatter.lineStart = pending[0] == '\n'
		formatter.discardPendingPrefix(1)
	}
	return output.String()
}

func (formatter *StreamFormatter) discardPendingPrefix(size int) {
	formatter.pendingStart += size
	formatter.boldScanStart = 0
	if formatter.pendingStart != len(formatter.pending) {
		if formatter.pendingStart < 4096 || formatter.pendingStart*2 < len(formatter.pending) {
			return
		}
		copy(formatter.pending, formatter.pending[formatter.pendingStart:])
		formatter.pending = formatter.pending[:len(formatter.pending)-formatter.pendingStart]
		formatter.pendingStart = 0
		return
	}
	if cap(formatter.pending) > 64*1024 {
		formatter.pending = nil
	} else {
		formatter.pending = formatter.pending[:0]
	}
	formatter.pendingStart = 0
}

func startsWithBoldSpan(text []byte) bool {
	return len(text) >= 2 && text[0] == '*' && text[1] == '*'
}

func (formatter *StreamFormatter) completeBoldSpanEnd(text []byte) int {
	if !startsWithBoldSpan(text) {
		formatter.boldScanStart = 0
		return 0
	}
	start := formatter.boldScanStart
	if start < 2 {
		start = 2
	}
	for index := start; index+1 < len(text); index++ {
		if text[index] != '*' || text[index+1] != '*' {
			continue
		}
		if index == 2 {
			return 0
		}
		return index + 2
	}
	if len(text) > 2 {
		formatter.boldScanStart = len(text) - 1
	} else {
		formatter.boldScanStart = 2
	}
	return 0
}

func startsWithEmptyBoldSpan(text []byte) bool {
	return len(text) >= 4 && text[2] == '*' && text[3] == '*'
}

func FormatText(text string) string {
	if text == "" || !strings.Contains(text, "**") {
		return text
	}
	var formatter StreamFormatter
	return formatter.Push(text) + formatter.Finish()
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

func containsOpaqueReasoningPayload(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			switch key {
			case "signature", "encrypted_content", "opaque":
				switch protected := nested.(type) {
				case nil:
				case string:
					if protected != "" {
						return true
					}
				case bool:
					if protected {
						return true
					}
				default:
					return true
				}
			}
			if containsOpaqueReasoningPayload(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if containsOpaqueReasoningPayload(nested) {
				return true
			}
		}
	case []map[string]any:
		for _, nested := range typed {
			if containsOpaqueReasoningPayload(nested) {
				return true
			}
		}
	}
	return false
}

func FormatBlock(block map[string]any) map[string]any {
	if len(block) == 0 {
		return nil
	}
	formatted := make(map[string]any, len(block))
	for key, value := range block {
		formatted[key] = value
	}
	if containsOpaqueReasoningPayload(formatted) {
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
