package httpapi

import (
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestCacheInfoUsageFromMapTracksCacheWriteReportState(t *testing.T) {
	tests := []struct {
		name                        string
		upstreamEndpointType        string
		usage                       map[string]any
		wantCacheWriteTokens        int64
		wantCacheWriteReportedInput int64
	}{
		{
			name:                 "responses reports cache write",
			upstreamEndpointType: config.UpstreamEndpointTypeResponses,
			usage: map[string]any{
				"input_tokens":  2006,
				"output_tokens": 300,
				"total_tokens":  2306,
				"input_tokens_details": map[string]any{
					"cached_tokens":      1920,
					"cache_write_tokens": 86,
				},
			},
			wantCacheWriteTokens:        86,
			wantCacheWriteReportedInput: 2006,
		},
		{
			name:                 "chat reports explicit zero cache write",
			upstreamEndpointType: config.UpstreamEndpointTypeChat,
			usage: map[string]any{
				"prompt_tokens":     500,
				"completion_tokens": 20,
				"total_tokens":      520,
				"prompt_tokens_details": map[string]any{
					"cached_tokens":      400,
					"cache_write_tokens": 0,
				},
			},
			wantCacheWriteTokens:        0,
			wantCacheWriteReportedInput: 500,
		},
		{
			name:                 "anthropic reports cache creation",
			upstreamEndpointType: config.UpstreamEndpointTypeAnthropic,
			usage: map[string]any{
				"input_tokens":                20,
				"output_tokens":               5,
				"cache_read_input_tokens":     6,
				"cache_creation_input_tokens": 4,
			},
			wantCacheWriteTokens:        4,
			wantCacheWriteReportedInput: 30,
		},
		{
			name:                 "missing cache write remains unreported",
			upstreamEndpointType: config.UpstreamEndpointTypeResponses,
			usage: map[string]any{
				"input_tokens":  100,
				"output_tokens": 10,
				"total_tokens":  110,
				"input_tokens_details": map[string]any{
					"cached_tokens": 80,
				},
			},
			wantCacheWriteTokens:        0,
			wantCacheWriteReportedInput: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, ok := cacheInfoUsageFromMap(tt.usage, tt.upstreamEndpointType)
			if !ok {
				t.Fatal("expected usage to parse")
			}
			if parsed.CacheWriteTokens != tt.wantCacheWriteTokens {
				t.Fatalf("CacheWriteTokens = %d, want %d", parsed.CacheWriteTokens, tt.wantCacheWriteTokens)
			}
			if parsed.CacheWriteReportedInputTokens != tt.wantCacheWriteReportedInput {
				t.Fatalf("CacheWriteReportedInputTokens = %d, want %d", parsed.CacheWriteReportedInputTokens, tt.wantCacheWriteReportedInput)
			}
		})
	}
}

func TestFormatThisUsageTokensIncludesReportedCacheWrite(t *testing.T) {
	got := formatThisUsageTokens(map[string]any{
		"input_tokens":  2006,
		"output_tokens": 300,
		"input_tokens_details": map[string]any{
			"cached_tokens":      1920,
			"cache_write_tokens": 86,
		},
	})
	const want = "↑ 2,006(1,920 cached, 86 cache-write) | ↓ 300"
	if got != want {
		t.Fatalf("formatThisUsageTokens() = %q, want %q", got, want)
	}
}

func TestFormatThisUsageTokensIncludesChatCacheWrite(t *testing.T) {
	got := formatThisUsageTokens(map[string]any{
		"prompt_tokens":     500,
		"completion_tokens": 20,
		"prompt_tokens_details": map[string]any{
			"cached_tokens":      400,
			"cache_write_tokens": 0,
		},
	})
	const want = "↑ 500(400 cached, 0 cache-write) | ↓ 20"
	if got != want {
		t.Fatalf("formatThisUsageTokens() = %q, want %q", got, want)
	}
}
