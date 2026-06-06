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

func TestPrepareCanonicalMessagesKeepsSuccessfulToolOutputsWithNullError(t *testing.T) {
	messages := []model.CanonicalMessage{
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "open apk"}}},
		{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: "我先打开当前 APK。"}}, ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "mcp__mt_apk_open", Arguments: `{"path":"mt://current-apk"}`}}},
		{Role: "tool", ToolCallID: "call_1", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"ok":true,"data":{"workspaceId":"c9m8dlnh"},"error":null}`}}},
	}

	prepared := prepareCanonicalMessages(messages)
	if len(prepared) != 3 {
		t.Fatalf("expected successful tool output with null error kept, got %#v", prepared)
	}
	if len(prepared[1].ToolCalls) != 1 || prepared[2].Role != "tool" {
		t.Fatalf("expected paired tool call/result preserved, got %#v", prepared)
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

func TestPrepareCanonicalMessagesKeepsAssistantTextWhenDroppingErroredToolCallInSameMessage(t *testing.T) {
	messages := []model.CanonicalMessage{
		{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: "我先访问这个工具。"}}, ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "scrape_web", Arguments: `{"url":"https://example.com"}`}}},
		{Role: "tool", ToolCallID: "call_1", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"error":"boom"}`}}},
	}

	prepared := prepareCanonicalMessages(messages)
	if len(prepared) != 1 {
		t.Fatalf("expected assistant text preserved while errored tool pair removed, got %#v", prepared)
	}
	if prepared[0].Role != "assistant" || len(prepared[0].ToolCalls) != 0 || prepared[0].Parts[0].Text == "" {
		t.Fatalf("expected assistant text without tool calls, got %#v", prepared)
	}
}

func TestPrepareCanonicalMessagesDropsAssistantToolCallsWithoutFollowingResult(t *testing.T) {
	messages := []model.CanonicalMessage{
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "search"}}},
		{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "search_web", Arguments: `{"query":"weather"}`}}},
		{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: "继续回答"}}},
	}

	prepared := prepareCanonicalMessages(messages)
	if len(prepared) != 2 {
		t.Fatalf("expected orphan assistant tool_call removed, got %#v", prepared)
	}
	for _, msg := range prepared {
		if len(msg.ToolCalls) > 0 {
			t.Fatalf("expected no orphan tool_calls after prepare, got %#v", prepared)
		}
	}
}

func TestPrepareCanonicalMessagesKeepsAssistantToolCallsWithFollowingResult(t *testing.T) {
	messages := []model.CanonicalMessage{
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "search"}}},
		{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "search_web", Arguments: `{"query":"weather"}`}}},
		{Role: "tool", ToolCallID: "call_1", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"ok":true}`}}},
	}

	prepared := prepareCanonicalMessages(messages)
	if len(prepared) != 3 {
		t.Fatalf("expected paired assistant tool_call and tool result kept, got %#v", prepared)
	}
	if len(prepared[1].ToolCalls) != 1 || prepared[2].Role != "tool" {
		t.Fatalf("expected valid tool_call/result pair preserved, got %#v", prepared)
	}
}

func TestPrepareCanonicalMessagesKeepsAssistantToolCallsWithOrderedToolResult(t *testing.T) {
	messages := []model.CanonicalMessage{
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "search"}}},
		{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "lookup_dynamic_context", Arguments: `{"query":"weather"}`}}},
		{
			Role: "user",
			OrderedContent: []model.CanonicalContentBlock{
				{Type: "tool_result", ToolCallID: "call_1", ToolResultParts: []model.CanonicalContentPart{{Type: "text", Text: `{"ok":true}`}}},
				{Type: "content", Part: model.CanonicalContentPart{Type: "text", Text: "继续"}},
			},
			Parts: []model.CanonicalContentPart{{Type: "text", Text: "继续"}},
		},
	}

	prepared := prepareCanonicalMessages(messages)
	if len(prepared) != 3 {
		t.Fatalf("expected ordered tool result to keep paired assistant tool_call, got %#v", prepared)
	}
	if len(prepared[1].ToolCalls) != 1 || prepared[2].Role != "user" || len(prepared[2].OrderedContent) != 2 {
		t.Fatalf("expected ordered tool_call/result pair preserved, got %#v", prepared)
	}
}

func TestPrepareCanonicalMessagesKeepsMultipleToolCallsWithFollowingResults(t *testing.T) {
	messages := []model.CanonicalMessage{
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "inspect apk"}}},
		{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: "I will inspect it."}}, ToolCalls: []model.CanonicalToolCall{
			{ID: "call_1", Type: "function", Name: "open_apk", Arguments: `{"path":"mt://current-apk"}`},
			{ID: "call_2", Type: "function", Name: "read_manifest", Arguments: `{"workspaceId":"c9m8dlnh"}`},
		}},
		{Role: "tool", ToolCallID: "call_1", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"ok":true,"error":null}`}}},
		{Role: "tool", ToolCallID: "call_2", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"ok":true,"error":null}`}}},
	}

	prepared := prepareCanonicalMessages(messages)
	if len(prepared) != 4 {
		t.Fatalf("expected multi tool call/result chain kept, got %#v", prepared)
	}
	if len(prepared[1].ToolCalls) != 2 || prepared[2].ToolCallID != "call_1" || prepared[3].ToolCallID != "call_2" {
		t.Fatalf("expected both tool calls and results preserved, got %#v", prepared)
	}
}

func TestPrepareCanonicalMessagesDropsAssistantToolCallsWhenResultIsNotImmediate(t *testing.T) {
	messages := []model.CanonicalMessage{
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "search"}}},
		{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "search_web", Arguments: `{"query":"weather"}`}}},
		{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: "中间消息"}}},
		{Role: "tool", ToolCallID: "call_1", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"ok":true}`}}},
	}

	prepared := prepareCanonicalMessages(messages)
	for _, msg := range prepared {
		if len(msg.ToolCalls) > 0 || msg.Role == "tool" {
			t.Fatalf("expected non-immediate tool_call/result pair removed, got %#v", prepared)
		}
	}
}

func TestPrepareCanonicalMessagesDropsSyntheticReasoningContent(t *testing.T) {
	messages := []model.CanonicalMessage{{
		Role:             "assistant",
		ReasoningContent: "**推理中**\n\n代理层占位，以兼容不同上游情况，便于客户端记录推理时长\n\n",
		Parts:            []model.CanonicalContentPart{{Type: "text", Text: "answer"}},
	}}

	prepared := prepareCanonicalMessages(messages)
	if len(prepared) != 1 {
		t.Fatalf("expected one assistant message kept, got %#v", prepared)
	}
	if prepared[0].ReasoningContent != "" {
		t.Fatalf("expected synthetic reasoning content dropped before upstream replay, got %#v", prepared[0])
	}
	if len(prepared[0].Parts) != 1 || prepared[0].Parts[0].Text != "answer" {
		t.Fatalf("expected assistant text preserved, got %#v", prepared[0])
	}
}
