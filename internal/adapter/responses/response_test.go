package responses

import (
	"testing"

	"openai-compat-proxy/internal/aggregate"
)

func TestBuildResponseID(t *testing.T) {
	tests := []struct {
		name   string
		result aggregate.Result
		wantID string
	}{
		{
			name:   "uses custom response ID",
			result: aggregate.Result{ResponseID: "resp_custom_abc"},
			wantID: "resp_custom_abc",
		},
		{
			name:   "defaults to resp_proxy when no custom ID",
			result: aggregate.Result{ResponseID: ""},
			wantID: "resp_proxy",
		},
		{
			name:   "defaults to resp_proxy when ID is zero-value struct",
			result: aggregate.Result{},
			wantID: "resp_proxy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := BuildResponse(tt.result)
			if got, _ := resp["id"].(string); got != tt.wantID {
				t.Errorf("id = %q, want %q", got, tt.wantID)
			}
		})
	}
}

func TestBuildOutputItemID(t *testing.T) {
	tests := []struct {
		name   string
		result aggregate.Result
		want   string
	}{
		{
			name: "uses msg_proxy for plain text",
			result: aggregate.Result{
				Text: "hello world",
			},
			want: "msg_proxy",
		},
		{
			name:   "uses msg_output as last resort when no text",
			result: aggregate.Result{},
			want:   "msg_output",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := BuildResponse(tt.result)
			output := resp["output"].([]map[string]any)
			if len(output) == 0 {
				t.Fatal("output is empty")
			}
			if got, _ := output[0]["id"].(string); got != tt.want {
				t.Errorf("output[0].id = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResponsesStatus(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		want   string
	}{
		{"stop yields completed", "stop", "completed"},
		{"tool_calls yields completed", "tool_calls", "completed"},
		{"tool_use yields completed", "tool_use", "completed"},
		{"end_turn yields completed", "end_turn", "completed"},
		{"empty yields completed", "", "completed"},
		{"length yields incomplete", "length", "incomplete"},
		{"max_tokens yields incomplete", "max_tokens", "incomplete"},
		{"content_filter yields incomplete", "content_filter", "incomplete"},
		{"unknown yields incomplete", "unknown_reason", "incomplete"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := BuildResponse(aggregate.Result{FinishReason: tt.reason})
			if got := resp["status"].(string); got != tt.want {
				t.Errorf("status = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResponsesIncompleteDetails(t *testing.T) {
	resp := BuildResponse(aggregate.Result{FinishReason: "content_filter"})
	if resp["incomplete_details"] == nil {
		t.Error("incomplete_details should be non-nil for unknown finish reason")
	}
	details := resp["incomplete_details"].(map[string]any)
	if details["reason"] != "content_filter" {
		t.Errorf("incomplete_details.reason = %q, want content_filter", details["reason"])
	}

	// Completed status → incomplete_details should be nil
	respOK := BuildResponse(aggregate.Result{FinishReason: "stop"})
	if respOK["incomplete_details"] != nil {
		t.Errorf("incomplete_details should be nil for completed status, got %#v", respOK["incomplete_details"])
	}
}

func TestBuildResponsePreservesServiceTier(t *testing.T) {
	resp := BuildResponse(aggregate.Result{ServiceTier: "default"})
	if got, _ := resp["service_tier"].(string); got != "default" {
		t.Fatalf("expected service_tier default, got %#v", resp["service_tier"])
	}
}

func TestBuildResponseWithMixedContent(t *testing.T) {
	result := aggregate.Result{
		ResponseOutputItems: []map[string]any{
			{
				"id":      "msg_pre",
				"type":    "message",
				"status":  "completed",
				"role":    "assistant",
				"content": []map[string]any{{"type": "output_text", "text": "from items"}},
			},
		},
		Text: "from result.Text",
	}

	resp := BuildResponse(result)
	output := resp["output"].([]map[string]any)
	if len(output) != 1 {
		t.Fatalf("output length = %d, want 1", len(output))
	}
	if output[0]["id"] != "msg_pre" {
		t.Errorf("output[0].id = %q, want msg_pre", output[0]["id"])
	}
}

func TestBuildResponsePreservesUpstreamReasoning(t *testing.T) {
	resp := BuildResponse(aggregate.Result{
		ResponseID: "resp_123",
		Text:       "final answer",
		Usage: map[string]any{
			"input_tokens":  1,
			"output_tokens": 2,
			"total_tokens":  3,
		},
		Reasoning: map[string]any{
			"_proxy_reasoning_source": "upstream",
			"summary":                 "secret upstream reasoning",
		},
	})

	reasoning, _ := resp["reasoning"].(map[string]any)
	if got, _ := reasoning["summary"].(string); got != "secret upstream reasoning" {
		t.Fatalf("expected upstream reasoning summary preserved, got %#v", resp["reasoning"])
	}
	if got, _ := resp["id"].(string); got != "resp_123" {
		t.Fatalf("expected response id preserved, got %#v", resp["id"])
	}
	output := resp["output"].([]map[string]any)
	if len(output) == 0 {
		t.Fatalf("expected output preserved, got %#v", resp)
	}
	content, _ := output[0]["content"].([]map[string]any)
	if len(content) == 0 || content[0]["text"] != "final answer" {
		t.Fatalf("expected final answer preserved, got %#v", output)
	}
}

func TestBuildResponseEmitsReasoningOutputItemBeforeFunctionCall(t *testing.T) {
	resp := BuildResponse(aggregate.Result{
		ResponseID: "resp_123",
		ReasoningBlocks: []map[string]any{{
			"type":              "reasoning",
			"id":                "rs_123",
			"summary":           []map[string]any{{"type": "summary_text", "text": "internal reasoning"}},
			"encrypted_content": "enc_123",
		}},
		ToolCalls: []aggregate.ToolCall{{ID: "call_1", CallID: "call_1", Name: "search_web", Arguments: `{"query":"weather"}`}},
	})

	output := resp["output"].([]map[string]any)
	if len(output) != 2 {
		t.Fatalf("expected reasoning item and function call item, got %#v", output)
	}
	reasoning := output[0]
	if got, _ := reasoning["type"].(string); got != "reasoning" {
		t.Fatalf("expected first output item to be reasoning, got %#v", output)
	}
	if got, _ := reasoning["encrypted_content"].(string); got != "enc_123" {
		t.Fatalf("expected reasoning encrypted_content preserved, got %#v", reasoning)
	}
	call := output[1]
	if got, _ := call["type"].(string); got != "function_call" {
		t.Fatalf("expected second output item to be function_call, got %#v", output)
	}
}

func TestBuildResponsePrependsReasoningBeforePreservedFunctionCall(t *testing.T) {
	resp := BuildResponse(aggregate.Result{
		ResponseID: "resp_123",
		ResponseOutputItems: []map[string]any{{
			"id":        "call_1",
			"type":      "function_call",
			"status":    "completed",
			"call_id":   "call_1",
			"name":      "search_web",
			"arguments": `{"query":"weather"}`,
		}},
		ReasoningBlocks: []map[string]any{{
			"type":      "thinking",
			"thinking":  "internal reasoning",
			"signature": "sig_123",
		}},
	})

	output := resp["output"].([]map[string]any)
	if len(output) != 2 {
		t.Fatalf("expected reasoning item and preserved function_call item, got %#v", output)
	}
	if got, _ := output[0]["type"].(string); got != "reasoning" {
		t.Fatalf("expected reasoning before function_call for client replay, got %#v", output)
	}
	if got, _ := output[1]["type"].(string); got != "function_call" {
		t.Fatalf("expected preserved function_call after reasoning, got %#v", output)
	}
}
