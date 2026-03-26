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
