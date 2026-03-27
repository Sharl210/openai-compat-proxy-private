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
