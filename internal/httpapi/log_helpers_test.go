package httpapi

import "testing"

func TestNestedCachedTokensReadsTopLevelCachedTokens(t *testing.T) {
	usage := map[string]any{"cached_tokens": 321}
	if got := nestedCachedTokens(usage); got != 321 {
		t.Fatalf("expected top-level cached_tokens to be returned, got %#v", got)
	}
}

func TestNestedCachedTokensReadsTopLevelCacheReadInputTokens(t *testing.T) {
	usage := map[string]any{"cache_read_input_tokens": 654}
	if got := nestedCachedTokens(usage); got != 654 {
		t.Fatalf("expected top-level cache_read_input_tokens to be returned, got %#v", got)
	}
}
