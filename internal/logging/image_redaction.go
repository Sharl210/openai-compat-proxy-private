package logging

import (
	"encoding/json"
	"strings"
)

const imagePlaceholder = "image"

func RedactImageDataForLog(body []byte) string {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return redactImageDataURLsInString(string(body))
	}
	redactImageData(payload)
	redacted, err := json.Marshal(payload)
	if err != nil {
		return string(body)
	}
	return string(redacted)
}

func redactImageData(value any) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			redactImageData(item)
		}
	case map[string]any:
		if isDataURLImage(typed) {
			if imageURL, ok := typed["image_url"].(map[string]any); ok {
				imageURL["url"] = imagePlaceholder
			} else {
				typed["image_url"] = imagePlaceholder
			}
		}
		if isBase64ImageSource(typed) {
			source, _ := typed["source"].(map[string]any)
			source["data"] = imagePlaceholder
		}
		for key, item := range typed {
			if text, ok := item.(string); ok {
				typed[key] = redactImageDataURLsInString(text)
				continue
			}
			redactImageData(item)
		}
	}
}

func isDataURLImage(value map[string]any) bool {
	typeName, _ := value["type"].(string)
	imageURL, _ := value["image_url"].(string)
	if typeName != "input_image" && typeName != "image_url" {
		return false
	}
	if hasDataURLPrefix(imageURL) {
		return true
	}
	imageURLPayload, _ := value["image_url"].(map[string]any)
	return hasDataURLPrefix(stringValue(imageURLPayload["url"]))
}

func isBase64ImageSource(value map[string]any) bool {
	typeName, _ := value["type"].(string)
	source, ok := value["source"].(map[string]any)
	if !ok {
		return false
	}
	sourceType, _ := source["type"].(string)
	return typeName == "image" && sourceType == "base64"
}

func hasDataURLPrefix(value string) bool {
	return len(value) >= 5 && value[:5] == "data:"
}

func isImageDataURL(value string) bool {
	return strings.HasPrefix(value, "data:image/") && strings.Contains(value, ";base64,")
}

func redactImageDataURLsInString(value string) string {
	const prefix = "data:image/"
	const delimiter = ";base64,"

	var redacted strings.Builder
	redacted.Grow(len(value))
	for offset := 0; offset < len(value); {
		start := strings.Index(value[offset:], prefix)
		if start < 0 {
			redacted.WriteString(value[offset:])
			break
		}
		start += offset
		redacted.WriteString(value[offset:start])
		remainder := value[start:]
		delimiterIndex := strings.Index(remainder, delimiter)
		if delimiterIndex < 0 {
			redacted.WriteString(remainder)
			break
		}
		end := start + delimiterIndex + len(delimiter)
		for end < len(value) && isBase64DataByte(value[end]) {
			end++
		}
		redacted.WriteString(imagePlaceholder)
		offset = end
	}
	return redacted.String()
}

func isBase64DataByte(value byte) bool {
	return value >= 'A' && value <= 'Z' ||
		value >= 'a' && value <= 'z' ||
		value >= '0' && value <= '9' ||
		value == '+' || value == '/' || value == '='
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
