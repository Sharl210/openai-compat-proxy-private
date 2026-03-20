package upstream

import (
	"encoding/json"
	"testing"

	"openai-compat-proxy/internal/model"
)

func TestBuildRequestBodyIncludesCacheControl(t *testing.T) {
	body, err := buildRequestBody(model.CanonicalRequest{
		Model:        "gpt-5",
		CacheControl: map[string]any{"type": "ephemeral", "ttl": "5m"},
		Messages: []model.CanonicalMessage{{
			Role: "user",
			Parts: []model.CanonicalContentPart{{
				Type: "text",
				Text: "hello",
				Raw:  map[string]any{"cache_control": map[string]any{"type": "ephemeral"}},
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

	cacheControl, _ := payload["cache_control"].(map[string]any)
	if cacheControl == nil {
		t.Fatal("expected top-level cache_control in upstream payload")
	}
	if got := cacheControl["type"]; got != "ephemeral" {
		t.Fatalf("expected top-level cache_control.type ephemeral, got %#v", got)
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
	partCache, _ := part["cache_control"].(map[string]any)
	if partCache == nil {
		t.Fatal("expected content cache_control in upstream payload")
	}
	if got := partCache["type"]; got != "ephemeral" {
		t.Fatalf("expected content cache_control.type ephemeral, got %#v", got)
	}
	if got := part["text"]; got != "hello" {
		t.Fatalf("expected content text hello, got %#v", got)
	}
}
