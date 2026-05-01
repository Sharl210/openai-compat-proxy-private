package aggregate

import "testing"

func TestResultFromResponsePayloadPreservesReasoningSummaryOutputItems(t *testing.T) {
	payload := map[string]any{
		"output": []any{
			map[string]any{
				"id":   "rs_1",
				"type": "reasoning",
				"summary": []any{
					map[string]any{"type": "summary_text", "text": "alpha"},
					map[string]any{"type": "summary_text", "text": "beta"},
				},
			},
		},
	}

	result, err := ResultFromResponsePayload(payload)
	if err != nil {
		t.Fatalf("ResultFromResponsePayload returned error: %v", err)
	}
	if got := stringValue(result.Reasoning["summary"]); got != "alphabeta" {
		t.Fatalf("expected reasoning summary alphabeta, got %q", got)
	}
	if len(result.ResponseOutputItems) != 1 {
		t.Fatalf("expected reasoning output item to be preserved, got %#v", result.ResponseOutputItems)
	}
}

func TestResultFromResponsePayloadCopiesUsageIntoReasoning(t *testing.T) {
	payload := map[string]any{
		"reasoning": map[string]any{
			"summary": "thinking",
		},
		"usage": map[string]any{
			"input_tokens":  11,
			"output_tokens": 7,
			"total_tokens":  18,
		},
	}

	result, err := ResultFromResponsePayload(payload)
	if err != nil {
		t.Fatalf("ResultFromResponsePayload returned error: %v", err)
	}

	usage, _ := result.Reasoning["usage"].(map[string]any)
	if got := usage["total_tokens"]; got != 18 {
		t.Fatalf("expected reasoning.usage.total_tokens to be preserved, got %#v", result.Reasoning)
	}
	if got := result.Usage["total_tokens"]; got != 18 {
		t.Fatalf("expected top-level usage.total_tokens to be preserved, got %#v", result.Usage)
	}
}

func TestResultFromResponsePayloadPreservesServiceTier(t *testing.T) {
	payload := map[string]any{
		"service_tier": "default",
		"output": []any{
			map[string]any{
				"type": "message",
				"content": []any{
					map[string]any{"type": "output_text", "text": "ok"},
				},
			},
		},
	}

	result, err := ResultFromResponsePayload(payload)
	if err != nil {
		t.Fatalf("ResultFromResponsePayload returned error: %v", err)
	}
	if result.ServiceTier != "default" {
		t.Fatalf("expected service tier default, got %q", result.ServiceTier)
	}
}

func TestResultFromResponsePayloadRepairsMalformedToolArguments(t *testing.T) {
	payload := map[string]any{
		"output": []any{
			map[string]any{
				"type":      "function_call",
				"id":        "fc_1",
				"call_id":   "call_1",
				"name":      "search_web",
				"arguments": `{"query":"hello"`,
			},
		},
	}

	result, err := ResultFromResponsePayload(payload)
	if err != nil {
		t.Fatalf("ResultFromResponsePayload returned error: %v", err)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %#v", result.ToolCalls)
	}
	if got := result.ToolCalls[0].Arguments; got != `{"query":"hello"}` {
		t.Fatalf("expected malformed function arguments to be repaired, got %q", got)
	}
}
