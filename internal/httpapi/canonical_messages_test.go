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

	deduped := prepareCanonicalMessages(messages)
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

	deduped := prepareCanonicalMessages(messages)
	if len(deduped) != 3 {
		t.Fatalf("expected non-identical messages to remain, got %#v", deduped)
	}
}

func TestPrepareCanonicalMessagesDropsErroredToolOutputs(t *testing.T) {
	messages := []model.CanonicalMessage{
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "open repo"}}},
		{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "scrape_web", Arguments: `{"url":"https://example.com"}`}}},
		{Role: "tool", ToolCallID: "call_1", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"error":"invalid params, invalid function arguments json string"}`}}},
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "continue"}}},
	}

	prepared := prepareCanonicalMessages(messages)
	if len(prepared) != 2 {
		t.Fatalf("expected errored tool pair removed, got %#v", prepared)
	}
	for _, msg := range prepared {
		if msg.Role == "tool" || len(msg.ToolCalls) > 0 {
			t.Fatalf("expected errored tool output and paired assistant tool_call to be removed, got %#v", prepared)
		}
	}
}

func TestPrepareCanonicalMessagesKeepsSuccessfulToolOutputs(t *testing.T) {
	messages := []model.CanonicalMessage{
		{Role: "tool", ToolCallID: "call_1", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"ok":true}`}}},
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "continue"}}},
	}

	prepared := prepareCanonicalMessages(messages)
	if len(prepared) != 2 {
		t.Fatalf("expected successful tool output kept, got %#v", prepared)
	}
}

func TestPrepareCanonicalMessagesKeepsAssistantTextWhileDroppingErroredToolCallPair(t *testing.T) {
	messages := []model.CanonicalMessage{
		{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: "让我先访问这个 GitHub 仓库页面。"}}},
		{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "scrape_web", Arguments: `{"url":"https://example.com"}`}}},
		{Role: "tool", ToolCallID: "call_1", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"error":"boom"}`}}},
	}

	prepared := prepareCanonicalMessages(messages)
	if len(prepared) != 1 {
		t.Fatalf("expected only plain assistant text to remain, got %#v", prepared)
	}
	if prepared[0].Role != "assistant" || len(prepared[0].ToolCalls) != 0 || prepared[0].Parts[0].Text == "" {
		t.Fatalf("expected plain assistant text preserved without tool_call, got %#v", prepared)
	}
}
