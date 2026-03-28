package httpapi

import "testing"

func TestAnthropicUsageFromEventIncludesCacheCreationAndReadTokens(t *testing.T) {
	usage := anthropicUsageFromEvent(map[string]any{
		"response": map[string]any{
			"usage": map[string]any{
				"input_tokens":  66000,
				"output_tokens": 8,
				"input_tokens_details": map[string]any{
					"cached_tokens":         33000,
					"cache_creation_tokens": 33000,
				},
			},
		},
	})
	if got := usage["input_tokens"]; got != 66000 {
		t.Fatalf("expected raw input_tokens 66000, got %#v", got)
	}

	if got := usage["cache_read_input_tokens"]; got != 33000 {
		t.Fatalf("expected cache_read_input_tokens 33000, got %#v", got)
	}
	if got := usage["cache_creation_input_tokens"]; got != 33000 {
		t.Fatalf("expected cache_creation_input_tokens 33000, got %#v", got)
	}
}
