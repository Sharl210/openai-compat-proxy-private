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
