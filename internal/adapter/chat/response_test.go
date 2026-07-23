package chat

import (
	"testing"

	"openai-compat-proxy/internal/aggregate"
)

func TestBuildResponseUsesToolCallsFinishReasonWhenToolCallsPresent(t *testing.T) {
	resp := BuildResponse(aggregate.Result{
		ToolCalls: []aggregate.ToolCall{{
			CallID:    "call_1",
			Name:      "get_weather",
			Arguments: `{"city":"Shanghai"}`,
		}},
	})

	choices, _ := resp["choices"].([]map[string]any)
	if len(choices) != 1 {
		t.Fatalf("expected one choice, got %#v", resp)
	}
	if got, _ := choices[0]["finish_reason"].(string); got != "tool_calls" {
		t.Fatalf("expected finish_reason tool_calls, got %#v", choices[0]["finish_reason"])
	}
}

func TestBuildResponseUsesNullContentForToolCallTurns(t *testing.T) {
	resp := BuildResponse(aggregate.Result{
		ToolCalls: []aggregate.ToolCall{{
			CallID:    "call_1",
			Name:      "get_weather",
			Arguments: `{"city":"Shanghai"}`,
		}},
	})

	choices, _ := resp["choices"].([]map[string]any)
	if len(choices) != 1 {
		t.Fatalf("expected one choice, got %#v", resp)
	}
	message, _ := choices[0]["message"].(map[string]any)
	content, exists := message["content"]
	if !exists {
		t.Fatalf("expected message content key to exist, got %#v", message)
	}
	if content != nil {
		t.Fatalf("expected tool-call message content to be null, got %#v", content)
	}
	if got, _ := choices[0]["finish_reason"].(string); got != "tool_calls" {
		t.Fatalf("expected finish_reason tool_calls, got %#v", choices[0]["finish_reason"])
	}
}

func TestBuildResponsePreservesExplicitFinishReason(t *testing.T) {
	resp := BuildResponse(aggregate.Result{FinishReason: "length"})
	choices, _ := resp["choices"].([]map[string]any)
	if got, _ := choices[0]["finish_reason"].(string); got != "length" {
		t.Fatalf("expected finish_reason length, got %#v", choices[0]["finish_reason"])
	}
}

func TestBuildResponsePreservesServiceTier(t *testing.T) {
	resp := BuildResponse(aggregate.Result{ServiceTier: "default"})
	if got, _ := resp["service_tier"].(string); got != "default" {
		t.Fatalf("expected service_tier default, got %#v", resp["service_tier"])
	}
}

func TestBuildResponsePromotesCachedTokenUsageFields(t *testing.T) {
	resp := BuildResponse(aggregate.Result{Usage: map[string]any{
		"input_tokens":  12,
		"output_tokens": 3,
		"total_tokens":  15,
		"input_tokens_details": map[string]any{
			"cached_tokens":         5,
			"cache_creation_tokens": 2,
		},
	}})

	usage, _ := resp["usage"].(map[string]any)
	if got := usage["cached_tokens"]; got != 5 {
		t.Fatalf("expected usage.cached_tokens 5, got %#v", got)
	}
	if got := usage["cache_creation_tokens"]; got != 2 {
		t.Fatalf("expected usage.cache_creation_tokens 2, got %#v", got)
	}
	details, _ := usage["prompt_tokens_details"].(map[string]any)
	if got := details["cached_tokens"]; got != 5 {
		t.Fatalf("expected prompt_tokens_details.cached_tokens 5, got %#v", got)
	}
	if got := details["cache_creation_tokens"]; got != 2 {
		t.Fatalf("expected prompt_tokens_details.cache_creation_tokens 2, got %#v", got)
	}
}

func TestBuildResponseFormatsReasoningContentTitle(t *testing.T) {
	resp := BuildResponse(aggregate.Result{
		Reasoning: map[string]any{"reasoning_content": "**重点**正文"},
	})

	choices, _ := resp["choices"].([]map[string]any)
	message, _ := choices[0]["message"].(map[string]any)
	if got, _ := message["reasoning_content"].(string); got != "**重点**\n正文" {
		t.Fatalf("expected reasoning_content title to be separated, got %#v", message)
	}
}

func TestBuildResponseSeparatesAdjacentReasoningTitles(t *testing.T) {
	resp := BuildResponse(aggregate.Result{
		Reasoning: map[string]any{"reasoning_content": "**标题****后续**"},
	})

	choices, _ := resp["choices"].([]map[string]any)
	message, _ := choices[0]["message"].(map[string]any)
	if got, _ := message["reasoning_content"].(string); got != "**标题**\n\n**后续**" {
		t.Fatalf("expected adjacent reasoning titles to be separated, got %#v", message)
	}
}

func TestBuildResponseDerivesReasoningContentFromThinkingBlocks(t *testing.T) {
	resp := BuildResponse(aggregate.Result{
		ReasoningBlocks: []map[string]any{{
			"type":     "thinking",
			"thinking": "**标题****后续**",
		}},
	})

	choices, _ := resp["choices"].([]map[string]any)
	message, _ := choices[0]["message"].(map[string]any)
	if got, _ := message["reasoning_content"].(string); got != "**标题**\n\n**后续**" {
		t.Fatalf("expected thinking blocks to become formatted reasoning_content, got %#v", message)
	}
}

func TestBuildResponsePrefersReasoningOverThinkingBlocks(t *testing.T) {
	resp := BuildResponse(aggregate.Result{
		Reasoning: map[string]any{"reasoning_content": "**主标题****主后续**"},
		ReasoningBlocks: []map[string]any{{
			"type":     "thinking",
			"thinking": "**备用标题****备用后续**",
		}},
	})

	choices, _ := resp["choices"].([]map[string]any)
	message, _ := choices[0]["message"].(map[string]any)
	if got, _ := message["reasoning_content"].(string); got != "**主标题**\n\n**主后续**" {
		t.Fatalf("expected direct reasoning to take precedence over thinking blocks, got %#v", message)
	}
}

func TestBuildResponseTrimsOnlyVisibleTextTrailingCRLF(t *testing.T) {
	result := aggregate.Result{
		Text:      "first\r\nsecond \t\r\n",
		Refusal:   "refusal\r\n",
		Reasoning: map[string]any{"summary": "reasoning\r\n"},
		ToolCalls: []aggregate.ToolCall{{
			CallID:    "call_1",
			Name:      "get_weather",
			Arguments: `{"location":"Shanghai\r\n"}`,
		}},
	}

	wantReasoning := reasoningContentValue(result.Reasoning)
	resp := BuildResponse(result)
	choices := resp["choices"].([]map[string]any)
	message := choices[0]["message"].(map[string]any)
	if got, _ := message["content"].(string); got != "first\r\nsecond \t" {
		t.Fatalf("expected visible text tail normalized without changing internal CRLF or terminal whitespace, got %q", got)
	}
	if got, _ := message["refusal"].(string); got != "refusal\r\n" {
		t.Fatalf("expected refusal unchanged, got %q", got)
	}
	if got, _ := message["reasoning_content"].(string); got != wantReasoning {
		t.Fatalf("expected reasoning unchanged, got %q", got)
	}
	toolCalls := message["tool_calls"].([]map[string]any)
	function := toolCalls[0]["function"].(map[string]any)
	if got, _ := function["arguments"].(string); got != `{"location":"Shanghai\r\n"}` {
		t.Fatalf("expected tool arguments unchanged, got %q", got)
	}
	if result.Text != "first\r\nsecond \t\r\n" {
		t.Fatalf("expected source result text unchanged, got %q", result.Text)
	}
	if got, _ := result.Reasoning["summary"].(string); got != "reasoning\r\n" {
		t.Fatalf("expected source reasoning unchanged, got %q", got)
	}
}
