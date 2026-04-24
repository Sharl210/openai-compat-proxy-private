package anthropic

import "testing"

import "openai-compat-proxy/internal/aggregate"

func TestMapUsageConvertsCanonicalTotalInputToAnthropicDiffInput(t *testing.T) {
	usage := map[string]any{
		"input_tokens":  100,
		"output_tokens": 12,
		"input_tokens_details": map[string]any{
			"cached_tokens":         30,
			"cache_creation_tokens": 20,
		},
	}

	mapped := mapUsage(usage)
	if got := mapped["input_tokens"]; got != float64(50) {
		t.Fatalf("expected anthropic diff input_tokens 50, got %#v", got)
	}
	if got := mapped["cache_read_input_tokens"]; got != 30 {
		t.Fatalf("expected cache_read_input_tokens 30, got %#v", got)
	}
	if got := mapped["cache_creation_input_tokens"]; got != 20 {
		t.Fatalf("expected cache_creation_input_tokens 20, got %#v", got)
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

func TestBuildResponseIncludesThinkingBlockBeforeText(t *testing.T) {
	resp := BuildResponse(aggregate.Result{
		Text:      "最终答案",
		Reasoning: map[string]any{"summary": "先想一下"},
	}, "req_456", "claude-sonnet-4-5")

	content, _ := resp["content"].([]map[string]any)
	if len(content) != 2 {
		t.Fatalf("expected thinking and text blocks, got %#v", resp)
	}
	if got, _ := content[0]["type"].(string); got != "thinking" {
		t.Fatalf("expected first content block thinking, got %#v", content)
	}
	if got, _ := content[0]["thinking"].(string); got != "先想一下" {
		t.Fatalf("expected thinking text preserved, got %#v", content[0])
	}
	if got, _ := content[1]["type"].(string); got != "text" {
		t.Fatalf("expected second content block text, got %#v", content)
	}
}

func TestBuildResponsePrefersOriginalReasoningBlocks(t *testing.T) {
	resp := BuildResponse(aggregate.Result{
		Text: "最终答案",
		Reasoning: map[string]any{"summary": "先想一下"},
		ReasoningBlocks: []map[string]any{{
			"type":      "thinking",
			"thinking":  "先想一下",
			"signature": "sig_123",
		}},
	}, "req_789", "claude-sonnet-4-5")

	content, _ := resp["content"].([]map[string]any)
	if len(content) != 2 {
		t.Fatalf("expected preserved thinking block and text block, got %#v", resp)
	}
	if got, _ := content[0]["signature"].(string); got != "sig_123" {
		t.Fatalf("expected original thinking signature preserved, got %#v", content[0])
	}
}

func TestBuildResponsePreservesExplicitStopReason(t *testing.T) {
	resp := BuildResponse(aggregate.Result{FinishReason: "max_tokens"}, "req_456", "claude-sonnet-4-5")
	if got, _ := resp["stop_reason"].(string); got != "max_tokens" {
		t.Fatalf("expected stop_reason max_tokens, got %#v", resp["stop_reason"])
	}
}
