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

func TestResultFromResponsePayloadSeparatesReasoningBoldTitleFromFollowingContent(t *testing.T) {
	payload := map[string]any{
		"reasoning": map[string]any{
			"summary": "**标题**正文",
		},
		"output": []any{
			map[string]any{
				"id":   "rs_1",
				"type": "reasoning",
				"summary": []any{
					map[string]any{"type": "summary_text", "text": "**标题****后续**"},
				},
			},
		},
	}

	result, err := ResultFromResponsePayload(payload)
	if err != nil {
		t.Fatalf("ResultFromResponsePayload returned error: %v", err)
	}
	if got := stringValue(result.Reasoning["summary"]); got != "**标题**正文" {
		t.Fatalf("expected reasoning summary title break, got %q", got)
	}
	if got := stringValue(result.ResponseOutputItems[0]["summary"].([]any)[0].(map[string]any)["text"]); got != "\n**标题**\n\n**后续**\n" {
		t.Fatalf("expected exact adjacent reasoning pair formatting, got %q", got)
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

func TestResultFromResponsePayloadPreservesNativeToolCallItems(t *testing.T) {
	payload := map[string]any{
		"output": []any{
			map[string]any{
				"type":      "web_search_call",
				"id":        "ws_1",
				"call_id":   "call_ws_1",
				"name":      "web_search",
				"arguments": `{"query":"latest news"}`,
			},
		},
	}

	result, err := ResultFromResponsePayload(payload)
	if err != nil {
		t.Fatalf("ResultFromResponsePayload returned error: %v", err)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected one native tool call, got %#v", result.ToolCalls)
	}
	if result.ToolCalls[0].Name != "web_search" {
		t.Fatalf("expected native tool call name web_search, got %#v", result.ToolCalls[0])
	}
	if result.ToolCalls[0].Arguments != `{"query":"latest news"}` {
		t.Fatalf("expected native tool call arguments preserved, got %#v", result.ToolCalls[0])
	}
}

func TestResultFromResponsePayloadPreservesAnthropicMessageToolCalls(t *testing.T) {
	payload := map[string]any{
		"content": []any{
			map[string]any{"type": "thinking", "thinking": "internal reasoning", "signature": "sig_123"},
			map[string]any{"type": "tool_use", "id": "call_1", "name": "search_web", "input": map[string]any{"query": "weather"}},
		},
	}

	result, err := ResultFromResponsePayload(payload)
	if err != nil {
		t.Fatalf("ResultFromResponsePayload returned error: %v", err)
	}
	if len(result.ReasoningBlocks) != 1 {
		t.Fatalf("expected anthropic thinking block preserved, got %#v", result.ReasoningBlocks)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected anthropic tool_use preserved, got %#v", result.ToolCalls)
	}
	if result.ToolCalls[0].CallID != "call_1" && result.ToolCalls[0].ID != "call_1" {
		t.Fatalf("expected anthropic tool call id preserved, got %#v", result.ToolCalls[0])
	}
}
