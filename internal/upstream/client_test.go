package upstream

import (
	"encoding/json"
	"testing"

	"openai-compat-proxy/internal/model"
)

func TestBuildRequestBodyOmitsCacheControlForUpstreamCompatibility(t *testing.T) {
	body, err := buildRequestBody(model.CanonicalRequest{
		Model: "gpt-5",
		Messages: []model.CanonicalMessage{{
			Role: "user",
			Parts: []model.CanonicalContentPart{{
				Type: "text",
				Text: "hello",
			}},
		}},
	})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if _, exists := payload["cache_control"]; exists {
		t.Fatalf("expected top-level cache_control to be omitted, got %#v", payload["cache_control"])
	}

	input, _ := payload["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("expected one input item, got %#v", input)
	}
	message, _ := input[0].(map[string]any)
	content, _ := message["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected one content item, got %#v", content)
	}
	part, _ := content[0].(map[string]any)
	if _, exists := part["cache_control"]; exists {
		t.Fatalf("expected content cache_control to be omitted, got %#v", part["cache_control"])
	}
	if got := part["text"]; got != "hello" {
		t.Fatalf("expected content text hello, got %#v", got)
	}
}
