package httpapi

import (
	"testing"

	modelpkg "openai-compat-proxy/internal/model"
)

func TestBuildEstimatorSnapshotCountsResponsesReasoningAndToolShape(t *testing.T) {
	canon := modelpkg.CanonicalRequest{
		Model:              "gpt-5.4",
		Instructions:       "follow system",
		ResponseInputItems: []map[string]any{{"type": "reasoning", "summary": []map[string]any{{"text": "trace"}}}},
		Messages: []modelpkg.CanonicalMessage{{
			Role:            "assistant",
			ReasoningBlocks: []map[string]any{{"type": "reasoning", "encrypted_content": "enc_123"}},
			ToolCalls:       []modelpkg.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "search_web", Arguments: `{"q":"hello"}`}},
		}},
	}
	snap := buildEstimatorSnapshot(canon)
	if snap.TextChars <= 0 {
		t.Fatalf("expected text chars, got %#v", snap)
	}
	if snap.ReasoningItemCount == 0 {
		t.Fatalf("expected reasoning item count, got %#v", snap)
	}
	if snap.ToolCallCount == 0 {
		t.Fatalf("expected tool call count, got %#v", snap)
	}
}

func TestEstimateCanonicalInputTokensStillUsesBaseEstimatorOnly(t *testing.T) {
	canon := modelpkg.CanonicalRequest{Model: "gpt-5.4", Messages: []modelpkg.CanonicalMessage{{Role: "user", Parts: []modelpkg.CanonicalContentPart{{Type: "text", Text: "hello world"}}}}}
	if got := estimateCanonicalInputTokens(canon); got <= 0 {
		t.Fatalf("expected positive estimate, got %d", got)
	}
}
