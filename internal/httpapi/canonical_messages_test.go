package httpapi

import (
	"testing"

	"openai-compat-proxy/internal/model"
)

func TestDedupeCanonicalToolMessagesRemovesAdjacentDuplicateTools(t *testing.T) {
	messages := []model.CanonicalMessage{
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}}},
		{Role: "tool", ToolCallID: "call_1", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"ok":true}`}}},
		{Role: "tool", ToolCallID: "call_1", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"ok":true}`}}},
	}

	deduped := dedupeCanonicalToolMessages(messages)
	if len(deduped) != 2 {
		t.Fatalf("expected duplicate tool message removed, got %#v", deduped)
	}
}

func TestDedupeCanonicalToolMessagesKeepsDistinctMessages(t *testing.T) {
	messages := []model.CanonicalMessage{
		{Role: "tool", ToolCallID: "call_1", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"ok":true}`}}},
		{Role: "tool", ToolCallID: "call_1", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"ok":false}`}}},
		{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: "next"}}},
	}

	deduped := dedupeCanonicalToolMessages(messages)
	if len(deduped) != 3 {
		t.Fatalf("expected non-identical messages to remain, got %#v", deduped)
	}
}
