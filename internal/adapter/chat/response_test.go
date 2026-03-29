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
