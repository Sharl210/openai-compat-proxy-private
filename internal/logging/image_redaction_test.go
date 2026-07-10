package logging

import (
	"strings"
	"testing"
)

func TestRedactImageDataForLogReplacesTruncatedImageJSONWithPlaceholder(t *testing.T) {
	// Given
	const imageDataSentinel = "VHJ1bmNhdGVkSW1hZ2VEYXRhU2VudGluZWw="
	body := []byte(`{"input":[{"type":"input_image","image_url":"data:image/png;base64,` + imageDataSentinel)

	// When
	got := RedactImageDataForLog(body)

	// Then
	if strings.Contains(got, imageDataSentinel) {
		t.Fatalf("expected truncated image payload to be redacted, got %s", got)
	}
	if !strings.HasSuffix(got, imagePlaceholder) {
		t.Fatalf("expected stable image placeholder suffix, got %q", got)
	}
}

func TestRedactImageDataForLogRedactsNestedImageURLAndAnthropicBase64Source(t *testing.T) {
	// Given
	const dataURLSentinel = "RGF0YVVSTEltYWdlU2VudGluZWw="
	const anthropicBase64Sentinel = "QW50aHJvcGljQmFzZTY0SW1hZ2VTZW50aW5lbA=="
	body := []byte(`{"input":[{"type":"input_image","image_url":{"url":"data:image/png;base64,` + dataURLSentinel + `","detail":"high"}},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + anthropicBase64Sentinel + `"}}]}`)

	// When
	got := RedactImageDataForLog(body)

	// Then
	if strings.Contains(got, dataURLSentinel) || strings.Contains(got, anthropicBase64Sentinel) {
		t.Fatalf("expected nested image payloads to be redacted, got %s", got)
	}
	if !strings.Contains(got, `"url":"image"`) || !strings.Contains(got, `"data":"image"`) {
		t.Fatalf("expected one image placeholder per image payload, got %s", got)
	}
}

func TestRedactImageDataForLogPreservesNonImageBase64(t *testing.T) {
	// Given
	const toolValueSentinel = "VG9vbFZhbHVlQmFzZTY0U2VudGluZWw="
	body := []byte(`{"tools":[{"type":"function","name":"upload","parameters":{"example":"` + toolValueSentinel + `"}}]}`)

	// When
	got := RedactImageDataForLog(body)

	// Then
	if !strings.Contains(got, toolValueSentinel) {
		t.Fatalf("expected non-image base64 tool value to remain readable, got %s", got)
	}
}

func TestRedactImageDataForLogReplacesGenericImageDataURL(t *testing.T) {
	// Given
	const imageDataSentinel = "R2VuZXJpY0ltYWdlRGF0YVVybEJhc2U2NFNlbnRpbmVs"
	body := []byte(`{"attachment":"data:image/png;base64,` + imageDataSentinel + `","text":"keep"}`)

	// When
	got := RedactImageDataForLog(body)

	// Then
	if strings.Contains(got, imageDataSentinel) || strings.Contains(got, "data:image/") {
		t.Fatalf("expected generic image data URL to be redacted, got %s", got)
	}
	if !strings.Contains(got, `"attachment":"image"`) {
		t.Fatalf("expected generic image data URL placeholder, got %s", got)
	}
}
