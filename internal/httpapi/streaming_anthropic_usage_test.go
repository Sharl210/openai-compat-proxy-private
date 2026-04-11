package httpapi

import "testing"

func TestAnthropicUsageFromEventIncludesCacheCreationAndReadTokens(t *testing.T) {
	usage := anthropicUsageFromEvent(map[string]any{
		"response": map[string]any{
			"usage": map[string]any{
				"input_tokens":  100,
				"output_tokens": 8,
				"input_tokens_details": map[string]any{
					"cached_tokens":         30,
					"cache_creation_tokens": 20,
				},
			},
		},
	})
	if got := usage["input_tokens"]; got != float64(50) {
		t.Fatalf("expected anthropic diff input_tokens 50, got %#v", got)
	}

	if got := usage["cache_read_input_tokens"]; got != 30 {
		t.Fatalf("expected cache_read_input_tokens 30, got %#v", got)
	}
	if got := usage["cache_creation_input_tokens"]; got != 20 {
		t.Fatalf("expected cache_creation_input_tokens 20, got %#v", got)
	}
}
