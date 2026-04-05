package syntaxrepair

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"strings"
)

func RepairJSON(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return text, false
	}
	if json.Valid([]byte(trimmed)) {
		return trimmed, true
	}
	if repaired, ok := sanitizeRepeatedJSONObject(trimmed); ok {
		return repaired, true
	}
	if repaired, ok := repairTruncatedJSON(trimmed); ok {
		return repaired, true
	}
	return text, false
}

func ParseJSONValue(text string) (any, string, bool) {
	normalized, ok := RepairJSON(text)
	if !ok {
		return nil, text, false
	}
	var decoded any
	if err := json.Unmarshal([]byte(normalized), &decoded); err != nil {
		return nil, text, false
	}
	return decoded, normalized, true
}

func ParseJSONObject(text string) (map[string]any, string, bool) {
	decoded, normalized, ok := ParseJSONValue(text)
	if !ok {
		return nil, text, false
	}
	obj, _ := decoded.(map[string]any)
	if len(obj) == 0 {
		return nil, normalized, false
	}
	return obj, normalized, true
}

func RepairStructuredText(text string) (string, string, bool) {
	if repaired, ok := RepairJSON(text); ok {
		return repaired, "json", repaired != text
	}
	return text, "", false
}

func RepairXML(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || !strings.HasPrefix(trimmed, "<") {
		return text, false
	}
	decoder := xml.NewDecoder(strings.NewReader(trimmed))
	stack := make([]xml.Name, 0, 8)
	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return text, false
		}
		switch typed := tok.(type) {
		case xml.StartElement:
			stack = append(stack, typed.Name)
		case xml.EndElement:
			if len(stack) == 0 {
				return text, false
			}
			if stack[len(stack)-1] != typed.Name {
				return text, false
			}
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) == 0 {
		return trimmed, true
	}
	var builder strings.Builder
	builder.WriteString(trimmed)
	for i := len(stack) - 1; i >= 0; i-- {
		builder.WriteString("</")
		if stack[i].Space != "" {
			builder.WriteString(stack[i].Space)
			builder.WriteByte(':')
		}
		builder.WriteString(stack[i].Local)
		builder.WriteByte('>')
	}
	repaired := builder.String()
	decoder = xml.NewDecoder(strings.NewReader(repaired))
	for {
		_, err := decoder.Token()
		if err == io.EOF {
			return repaired, true
		}
		if err != nil {
			return text, false
		}
	}
}

func repairTruncatedJSON(input string) (string, bool) {
	var builder strings.Builder
	builder.Grow(len(input) + 8)
	stack := make([]byte, 0, 8)
	inString := false
	escaping := false
	for i := 0; i < len(input); i++ {
		ch := input[i]
		builder.WriteByte(ch)
		if inString {
			if escaping {
				escaping = false
				continue
			}
			switch ch {
			case '\\':
				escaping = true
			case '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) == 0 || stack[len(stack)-1] != ch {
				return "", false
			}
			stack = stack[:len(stack)-1]
		}
	}
	if inString {
		if escaping {
			builder.WriteByte('\\')
		}
		builder.WriteByte('"')
	}
	for i := len(stack) - 1; i >= 0; i-- {
		builder.WriteByte(stack[i])
	}
	candidate := trimTrailingCommas(builder.String())
	if !json.Valid([]byte(candidate)) {
		return "", false
	}
	return candidate, true
}

func trimTrailingCommas(input string) string {
	var builder strings.Builder
	builder.Grow(len(input))
	inString := false
	escaping := false
	for i := 0; i < len(input); i++ {
		ch := input[i]
		if inString {
			builder.WriteByte(ch)
			if escaping {
				escaping = false
				continue
			}
			switch ch {
			case '\\':
				escaping = true
			case '"':
				inString = false
			}
			continue
		}
		if ch == '"' {
			inString = true
			builder.WriteByte(ch)
			continue
		}
		if ch == ',' {
			j := i + 1
			for j < len(input) {
				next := input[j]
				if next == ' ' || next == '\n' || next == '\r' || next == '\t' {
					j++
					continue
				}
				break
			}
			if j < len(input) && (input[j] == '}' || input[j] == ']') {
				continue
			}
		}
		builder.WriteByte(ch)
	}
	return builder.String()
}

func sanitizeRepeatedJSONObject(input string) (string, bool) {
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.UseNumber()
	var first any
	if err := decoder.Decode(&first); err != nil {
		return "", false
	}
	canonical, err := json.Marshal(first)
	if err != nil {
		return "", false
	}
	canonicalTrimmed := strings.TrimSpace(string(canonical))
	if canonicalTrimmed == "" {
		return "", false
	}
	offset := int(decoder.InputOffset())
	if offset < 0 || offset > len(input) {
		return "", false
	}
	remainder := strings.TrimSpace(input[offset:])
	if remainder == "" {
		return canonicalTrimmed, true
	}
	count := 1
	for remainder != "" {
		if !strings.HasPrefix(remainder, canonicalTrimmed) {
			return "", false
		}
		remainder = strings.TrimSpace(strings.TrimPrefix(remainder, canonicalTrimmed))
		count++
	}
	if count < 2 {
		return "", false
	}
	return canonicalTrimmed, true
}
