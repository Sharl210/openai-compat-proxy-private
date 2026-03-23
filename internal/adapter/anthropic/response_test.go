package anthropic

import "testing"

func TestMapUsageIncludesCacheCreationAndReadTokens(t *testing.T) {
	usage := map[string]any{
		"input_tokens":  33000,
		"output_tokens": 12,
		"input_tokens_details": map[string]any{
			"cached_tokens":         33000,
			"cache_creation_tokens": 33000,
		},
	}

	mapped := mapUsage(usage)
	if got := mapped["input_tokens"]; got != 33000 {
		t.Fatalf("expected input_tokens 33000, got %#v", got)
	}
	if got := mapped["cache_read_input_tokens"]; got != 33000 {
		t.Fatalf("expected cache_read_input_tokens 33000, got %#v", got)
	}
	if got := mapped["cache_creation_input_tokens"]; got != 33000 {
		t.Fatalf("expected cache_creation_input_tokens 33000, got %#v", got)
	}
}
