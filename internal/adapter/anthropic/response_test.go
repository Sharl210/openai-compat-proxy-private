package anthropic

import "testing"

import "openai-compat-proxy/internal/aggregate"

func TestMapUsageIncludesCacheCreationAndReadTokens(t *testing.T) {
	usage := map[string]any{
		"input_tokens":  66000,
		"output_tokens": 12,
		"input_tokens_details": map[string]any{
			"cached_tokens":         33000,
			"cache_creation_tokens": 33000,
		},
	}

	mapped := mapUsage(usage)
	if got := mapped["input_tokens"]; got != 0 {
		t.Fatalf("expected effective input_tokens 0, got %#v", got)
	}
	if got := mapped["cache_read_input_tokens"]; got != 33000 {
		t.Fatalf("expected cache_read_input_tokens 33000, got %#v", got)
	}
	if got := mapped["cache_creation_input_tokens"]; got != 33000 {
		t.Fatalf("expected cache_creation_input_tokens 33000, got %#v", got)
	}
}

func TestBuildResponsePreservesTextAlongsideToolUse(t *testing.T) {
	resp := BuildResponse(aggregate.Result{
		Text: "先查一下。",
		ToolCalls: []aggregate.ToolCall{{
			CallID:    "call_1",
			Name:      "search_web",
			Arguments: `{"query":"Quectel"}`,
		}},
	}, "req_123", "claude-sonnet-4-5")

	if got, _ := resp["id"].(string); got != "req_123" {
		t.Fatalf("expected response id req_123, got %#v", resp)
	}

	content, _ := resp["content"].([]map[string]any)
	if len(content) != 2 {
		t.Fatalf("expected text and tool_use blocks, got %#v", resp)
	}
	if got, _ := content[0]["type"].(string); got != "text" {
		t.Fatalf("expected first content block text, got %#v", content)
	}
	if got, _ := content[1]["type"].(string); got != "tool_use" {
		t.Fatalf("expected second content block tool_use, got %#v", content)
	}
}
