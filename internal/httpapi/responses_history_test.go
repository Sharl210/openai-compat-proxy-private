package httpapi

import (
	"testing"

	"openai-compat-proxy/internal/aggregate"
	"openai-compat-proxy/internal/model"
)

func TestResponsesHistoryEvictsOldestWhenLimitExceeded(t *testing.T) {
	store := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, maxSize: 2}

	store.Save("openai", "resp-1", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "one"}}}})
	store.Save("openai", "resp-2", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "two"}}}})
	store.Save("openai", "resp-3", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "three"}}}})

	if got := store.Load("openai", "resp-1"); got != nil {
		t.Fatalf("expected oldest entry to be evicted, got %#v", got)
	}
	if got := store.Load("openai", "resp-2"); len(got) != 1 {
		t.Fatalf("expected resp-2 to remain, got %#v", got)
	}
	if got := store.Load("openai", "resp-3"); len(got) != 1 {
		t.Fatalf("expected resp-3 to remain, got %#v", got)
	}
}

func TestResponsesHistorySaveSameKeyReplacesWithoutGrowingOrder(t *testing.T) {
	store := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, maxSize: 2}

	store.Save("openai", "resp-1", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "one"}}}})
	store.Save("openai", "resp-1", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "updated"}}}})
	store.Save("openai", "resp-2", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "two"}}}})
	store.Save("openai", "resp-3", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "three"}}}})

	if got := store.Load("openai", "resp-1"); len(got) != 0 {
		t.Fatalf("expected resp-1 to be evicted after capacity exceeded, got %#v", got)
	}
	if got := store.Load("openai", "resp-2"); len(got) != 1 {
		t.Fatalf("expected resp-2 to remain, got %#v", got)
	}
	if got := store.Load("openai", "resp-3"); len(got) != 1 {
		t.Fatalf("expected resp-3 to remain, got %#v", got)
	}
}

func TestResponsesHistoryLoadReturnsClone(t *testing.T) {
	store := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, maxSize: 2}
	store.Save("openai", "resp-1", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "one"}}}})

	loaded := store.Load("openai", "resp-1")
	if len(loaded) != 1 {
		t.Fatalf("expected one loaded message, got %#v", loaded)
	}
	loaded[0].Parts[0].Text = "mutated"

	reloaded := store.Load("openai", "resp-1")
	if reloaded[0].Parts[0].Text != "one" {
		t.Fatalf("expected stored history to stay immutable, got %#v", reloaded)
	}
}

func TestResponsesHistorySaveDropsSyntheticReasoningContentFromStoredMessages(t *testing.T) {
	store := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, maxSize: 2}
	store.Save("openai", "resp-1", []model.CanonicalMessage{{
		Role:             "assistant",
		ReasoningContent: "**推理中**\n\n代理层占位，以兼容不同上游情况，便于客户端记录推理时长\n\n",
		Parts:            []model.CanonicalContentPart{{Type: "text", Text: "answer"}},
	}})

	loaded := store.Load("openai", "resp-1")
	if len(loaded) != 1 {
		t.Fatalf("expected one loaded message, got %#v", loaded)
	}
	if loaded[0].ReasoningContent != "" {
		t.Fatalf("expected synthetic reasoning content to be dropped from stored history, got %#v", loaded[0])
	}
	if len(loaded[0].Parts) != 1 || loaded[0].Parts[0].Text != "answer" {
		t.Fatalf("expected assistant text preserved, got %#v", loaded[0])
	}
}

func TestBuildResponsesHistorySnapshotKeepsOnlyFollowUpRelevantMessages(t *testing.T) {
	base := []model.CanonicalMessage{
		{Role: "developer", Parts: []model.CanonicalContentPart{{Type: "text", Text: "tool registry and developer prompt"}}},
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "what is the weather"}}},
		{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "search_web", Arguments: `{"query":"weather"}`}}},
		{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: "plain assistant text that should not be persisted"}}},
	}
	assistant := []model.CanonicalMessage{{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: "done"}}}}

	snapshot := buildResponsesHistorySnapshot(base, assistant)
	if len(snapshot) != 3 {
		t.Fatalf("expected 3 messages in narrow snapshot, got %#v", snapshot)
	}
	if snapshot[0].Role != "user" || snapshot[0].Parts[0].Text != "what is the weather" {
		t.Fatalf("expected original user message to remain, got %#v", snapshot)
	}
	if snapshot[1].Role != "assistant" || len(snapshot[1].ToolCalls) != 1 || snapshot[1].ToolCalls[0].ID != "call_1" {
		t.Fatalf("expected assistant tool call message to remain, got %#v", snapshot)
	}
	if snapshot[2].Role != "assistant" || snapshot[2].Parts[0].Text != "done" {
		t.Fatalf("expected new assistant output to remain, got %#v", snapshot)
	}
	for _, msg := range snapshot {
		if len(msg.Parts) > 0 && msg.Parts[0].Text == "tool registry and developer prompt" {
			t.Fatalf("expected developer message to be excluded, got %#v", snapshot)
		}
		if len(msg.Parts) > 0 && msg.Parts[0].Text == "plain assistant text that should not be persisted" {
			t.Fatalf("expected plain assistant text to be excluded, got %#v", snapshot)
		}
	}
}

func TestBuildResponsesHistorySnapshotDropsErroredToolOutputs(t *testing.T) {
	base := []model.CanonicalMessage{
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "open repo"}}},
		{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "scrape_web", Arguments: `{"url":"https://example.com"}`}}},
		{Role: "tool", ToolCallID: "call_1", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"error":"invalid params, invalid function arguments json string"}`}}},
	}
	assistant := []model.CanonicalMessage{{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: "I will retry."}}}}

	snapshot := buildResponsesHistorySnapshot(base, assistant)
	if len(snapshot) != 3 {
		t.Fatalf("expected user + assistant tool_call + new assistant output, got %#v", snapshot)
	}
	for _, msg := range snapshot {
		if msg.Role == "tool" {
			t.Fatalf("expected errored tool output to be excluded from history snapshot, got %#v", snapshot)
		}
	}
}

func TestBuildResponsesHistorySnapshotKeepsSuccessfulToolOutputs(t *testing.T) {
	base := []model.CanonicalMessage{
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "open repo"}}},
		{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "scrape_web", Arguments: `{"url":"https://example.com"}`}}},
		{Role: "tool", ToolCallID: "call_1", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"markdown":"ok"}`}}},
	}
	assistant := []model.CanonicalMessage{{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: "done"}}}}

	snapshot := buildResponsesHistorySnapshot(base, assistant)
	if len(snapshot) != 4 {
		t.Fatalf("expected successful tool output to remain in snapshot, got %#v", snapshot)
	}
	if snapshot[2].Role != "tool" || snapshot[2].ToolCallID != "call_1" {
		t.Fatalf("expected successful tool message to remain, got %#v", snapshot)
	}
}

func TestBuildResponsesHistorySnapshotDropsSyntheticReasoningPlaceholder(t *testing.T) {
	base := []model.CanonicalMessage{{
		Role:  "user",
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}},
	}}
	assistant := []model.CanonicalMessage{{
		Role: "assistant",
		ReasoningBlocks: []map[string]any{{
			"type":    "reasoning",
			"id":      "rs_proxy",
			"summary": []map[string]any{{"type": "summary_text", "text": "**推理中**\n\n代理层占位，以兼容不同上游情况，便于客户端记录推理时长\n\n"}},
		}},
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "final answer"}},
	}}

	snapshot := buildResponsesHistorySnapshot(base, assistant)
	if len(snapshot) != 2 {
		t.Fatalf("expected user + assistant output only, got %#v", snapshot)
	}
	if len(snapshot[1].ReasoningBlocks) != 0 {
		t.Fatalf("expected synthetic rs_proxy reasoning to be excluded from history snapshot, got %#v", snapshot[1])
	}
	if snapshot[1].Parts[0].Text != "final answer" {
		t.Fatalf("expected assistant text preserved, got %#v", snapshot[1])
	}
}

func TestBuildResponsesHistorySnapshotKeepsRealReasoningBlocks(t *testing.T) {
	base := []model.CanonicalMessage{{
		Role:  "user",
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}},
	}}
	assistant := []model.CanonicalMessage{{
		Role: "assistant",
		ReasoningBlocks: []map[string]any{{
			"type":      "thinking",
			"thinking":  "真实推理",
			"signature": "sig_123",
		}},
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "final answer"}},
	}}

	snapshot := buildResponsesHistorySnapshot(base, assistant)
	if len(snapshot) != 2 {
		t.Fatalf("expected user + assistant output, got %#v", snapshot)
	}
	if len(snapshot[1].ReasoningBlocks) != 1 {
		t.Fatalf("expected real reasoning block to remain in history snapshot, got %#v", snapshot[1])
	}
	if got, _ := snapshot[1].ReasoningBlocks[0]["type"].(string); got != "thinking" {
		t.Fatalf("expected real thinking block preserved, got %#v", snapshot[1].ReasoningBlocks)
	}
}

func TestBuildResponsesHistorySnapshotKeepsRealProxyReasoningBlock(t *testing.T) {
	base := []model.CanonicalMessage{{
		Role:  "user",
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}},
	}}
	assistant := []model.CanonicalMessage{{
		Role: "assistant",
		ReasoningBlocks: []map[string]any{{
			"type":    "reasoning",
			"id":      "rs_proxy",
			"summary": []map[string]any{{"type": "summary_text", "text": "真实推理"}},
		}},
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "final answer"}},
	}}

	snapshot := buildResponsesHistorySnapshot(base, assistant)
	if len(snapshot) != 2 {
		t.Fatalf("expected user + assistant output, got %#v", snapshot)
	}
	if len(snapshot[1].ReasoningBlocks) != 1 {
		t.Fatalf("expected real rs_proxy reasoning block to remain in history snapshot, got %#v", snapshot[1])
	}
	if got, _ := snapshot[1].ReasoningBlocks[0]["id"].(string); got != "rs_proxy" {
		t.Fatalf("expected real rs_proxy reasoning block preserved, got %#v", snapshot[1].ReasoningBlocks)
	}
}

func TestBuildResponsesHistorySnapshotDropsSyntheticNativeThinkingBlocks(t *testing.T) {
	base := []model.CanonicalMessage{{
		Role:  "user",
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}},
	}}
	assistant := []model.CanonicalMessage{{
		Role: "assistant",
		ReasoningBlocks: []map[string]any{{
			"type":     "thinking",
			"thinking": "**推理中**\n\n代理层占位，以兼容不同上游情况，便于客户端记录推理时长\n\n",
		}},
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "final answer"}},
	}}

	snapshot := buildResponsesHistorySnapshot(base, assistant)
	if len(snapshot) != 2 {
		t.Fatalf("expected user + assistant output, got %#v", snapshot)
	}
	if len(snapshot[1].ReasoningBlocks) != 0 {
		t.Fatalf("expected synthetic native thinking block excluded from history snapshot, got %#v", snapshot[1])
	}
	if snapshot[1].Parts[0].Text != "final answer" {
		t.Fatalf("expected assistant text preserved, got %#v", snapshot[1])
	}
}

func TestAssistantHistoryMessagesFromResultDropsSyntheticReasoningSummary(t *testing.T) {
	messages := assistantHistoryMessagesFromResult(aggregate.Result{
		ResponseMessageContent: []map[string]any{{"type": "output_text", "text": "final answer"}},
		Reasoning: map[string]any{
			"summary": "**推理中**\n\n代理层占位，以兼容不同上游情况，便于客户端记录推理时长\n\n",
		},
	})
	if len(messages) != 1 {
		t.Fatalf("expected one assistant history message, got %#v", messages)
	}
	if got := messages[0].ReasoningContent; got != "" {
		t.Fatalf("expected synthetic reasoning summary to be dropped from history message, got %#v", messages[0])
	}
	if len(messages[0].ReasoningBlocks) != 0 {
		t.Fatalf("expected no reasoning blocks when only synthetic summary exists, got %#v", messages[0])
	}
	if len(messages[0].Parts) != 1 || messages[0].Parts[0].Text != "final answer" {
		t.Fatalf("expected assistant text preserved, got %#v", messages[0])
	}
}

func TestShouldRestorePreviousConversationAllowsNewUserTurnWithoutClientHistory(t *testing.T) {
	messages := []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "follow up"}}}}
	if !shouldRestorePreviousConversation(messages) {
		t.Fatalf("expected previous conversation restore for a new user turn without assistant history")
	}
}

func TestShouldRestorePreviousConversationSkipsWhenClientAlreadySendsAssistantHistory(t *testing.T) {
	messages := []model.CanonicalMessage{
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}}},
		{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "search_web", Arguments: `{"query":"weather"}`}}},
		{Role: "tool", ToolCallID: "call_1", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"ok":true}`}}},
	}
	if shouldRestorePreviousConversation(messages) {
		t.Fatalf("expected restore to be skipped when client already sends assistant history")
	}
}

func TestResponsesHistoryCompactionItemsDoNotProvidePreviousResponseID(t *testing.T) {
	items := []map[string]any{
		{"role": "user", "content": []map[string]any{{"type": "input_text", "text": "follow up"}}},
		{"type": "compaction", "id": "cmp_123", "encrypted_content": "enc_payload"},
	}

	if got := previousResponseIDFromItems(items); got != "" {
		t.Fatalf("expected compaction items alone not to provide previous_response_id, got %q", got)
	}

	if !shouldRestorePreviousConversation([]model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "follow up"}}}}) {
		t.Fatalf("expected restore decision to still depend on canonical messages, not compaction item presence")
	}
}

func TestResponsesHistoryPreviousResponseIDStillComesFromPreservedTopLevelFieldsWithCompactionItems(t *testing.T) {
	items := []map[string]any{
		{"type": "compaction", "id": "cmp_123", "encrypted_content": "enc_payload"},
		{"__openai_compat_responses_top_level": map[string]any{"previous_response_id": "resp_123"}},
	}

	if got := previousResponseIDFromItems(items); got != "resp_123" {
		t.Fatalf("expected previous_response_id to come only from preserved top-level fields, got %q", got)
	}
}
