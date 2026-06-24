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
	if got := store.LoadAny("resp-1"); got != nil {
		t.Fatalf("expected evicted response id to be removed from LoadAny index, got %#v", got)
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

func TestResponsesHistoryLoadToolCallByCallIDReturnsClone(t *testing.T) {
	store := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, maxSize: 2}
	store.Save("anthropic", "resp-1", []model.CanonicalMessage{{
		Role:      "assistant",
		ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "run_in_terminal", Arguments: `{"cmd":"pwd"}`}},
	}})

	loaded, ok := store.LoadToolCall("anthropic", "call_1")
	if !ok {
		t.Fatal("expected tool call to be indexed by call_id")
	}
	if loaded.ID != "call_1" || loaded.Name != "run_in_terminal" || loaded.Arguments != `{"cmd":"pwd"}` {
		t.Fatalf("expected stored tool call metadata, got %#v", loaded)
	}

	loaded.Name = "mutated"
	reloaded, ok := store.LoadToolCall("anthropic", "call_1")
	if !ok || reloaded.Name != "run_in_terminal" {
		t.Fatalf("expected tool call lookup to return a clone, got ok=%t call=%#v", ok, reloaded)
	}
}

func TestResponsesHistoryEvictsToolCallIndexWithSnapshot(t *testing.T) {
	store := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, maxSize: 1}
	store.Save("anthropic", "resp-1", []model.CanonicalMessage{{
		Role:      "assistant",
		ToolCalls: []model.CanonicalToolCall{{ID: "call_old", Type: "function", Name: "old", Arguments: `{}`}},
	}})
	store.Save("anthropic", "resp-2", []model.CanonicalMessage{{
		Role:      "assistant",
		ToolCalls: []model.CanonicalToolCall{{ID: "call_new", Type: "function", Name: "new", Arguments: `{}`}},
	}})

	if _, ok := store.LoadToolCall("anthropic", "call_old"); ok {
		t.Fatal("expected evicted snapshot tool call to be removed from index")
	}
	if call, ok := store.LoadToolCall("anthropic", "call_new"); !ok || call.Name != "new" {
		t.Fatalf("expected newest tool call to remain indexed, got ok=%t call=%#v", ok, call)
	}
}

func TestResponsesHistoryToolCallLookupIsProviderScoped(t *testing.T) {
	store := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, maxSize: 3}
	store.Save("anthropic-a", "resp-1", []model.CanonicalMessage{{
		Role:      "assistant",
		ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "tool_a", Arguments: `{}`}},
	}})
	store.Save("anthropic-b", "resp-1", []model.CanonicalMessage{{
		Role:      "assistant",
		ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "tool_b", Arguments: `{}`}},
	}})

	callA, okA := store.LoadToolCall("anthropic-a", "call_1")
	callB, okB := store.LoadToolCall("anthropic-b", "call_1")
	if !okA || callA.Name != "tool_a" {
		t.Fatalf("expected provider A scoped tool call, got ok=%t call=%#v", okA, callA)
	}
	if !okB || callB.Name != "tool_b" {
		t.Fatalf("expected provider B scoped tool call, got ok=%t call=%#v", okB, callB)
	}
}

func TestResponsesHistoryToolCallLookupIsConversationScoped(t *testing.T) {
	store := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, maxSize: 3}
	store.Save("anthropic", "resp-a", []model.CanonicalMessage{{
		Role:      "assistant",
		ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "tool_a", Arguments: `{}`}},
	}}, "scope-a")
	store.Save("anthropic", "resp-b", []model.CanonicalMessage{{
		Role:      "assistant",
		ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "tool_b", Arguments: `{}`}},
	}}, "scope-b")

	callA, okA := store.LoadToolCall("anthropic", "call_1", "scope-a")
	callB, okB := store.LoadToolCall("anthropic", "call_1", "scope-b")
	if !okA || callA.Name != "tool_a" {
		t.Fatalf("expected scope A tool call, got ok=%t call=%#v", okA, callA)
	}
	if !okB || callB.Name != "tool_b" {
		t.Fatalf("expected scope B tool call, got ok=%t call=%#v", okB, callB)
	}
}

func TestResponsesHistoryLoadAnyReturnsLatestProviderSnapshot(t *testing.T) {
	store := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, maxSize: 3}

	store.Save("codex-my", "resp-1", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "codex snapshot"}}}})
	store.Save("mimo", "resp-1", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "mimo snapshot"}}}})
	store.Save("codex-my", "resp-1", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "latest codex snapshot"}}}})

	if got := store.Load("mimo", "resp-1"); len(got) != 1 || got[0].Parts[0].Text != "mimo snapshot" {
		t.Fatalf("expected exact provider lookup to keep provider-specific snapshot, got %#v", got)
	}

	loaded := store.LoadAny("resp-1")
	if len(loaded) != 1 || loaded[0].Parts[0].Text != "latest codex snapshot" {
		t.Fatalf("expected LoadAny to return latest saved provider snapshot, got %#v", loaded)
	}

	loaded[0].Parts[0].Text = "mutated"
	reloaded := store.LoadAny("resp-1")
	if len(reloaded) != 1 || reloaded[0].Parts[0].Text != "latest codex snapshot" {
		t.Fatalf("expected LoadAny to return a clone, got %#v", reloaded)
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

func TestAssistantHistoryMessagesFromResultKeepsToolCalls(t *testing.T) {
	messages := assistantHistoryMessagesFromResult(aggregate.Result{
		ToolCalls: []aggregate.ToolCall{{CallID: "call_1", ID: "fc_1", Name: "search_web", Arguments: `{"query":"weather"}`}},
	})
	if len(messages) != 1 {
		t.Fatalf("expected one assistant history message, got %#v", messages)
	}
	if len(messages[0].ToolCalls) != 1 {
		t.Fatalf("expected tool call to be preserved, got %#v", messages[0])
	}
	call := messages[0].ToolCalls[0]
	if call.ID != "call_1" || call.Name != "search_web" || call.Arguments != `{"query":"weather"}` {
		t.Fatalf("expected call_1/search_web tool call preserved, got %#v", call)
	}
}

func TestAssistantHistoryMessagesFromResultDropsInvisibleSyntheticReasoningSummary(t *testing.T) {
	messages := assistantHistoryMessagesFromResult(aggregate.Result{
		ResponseMessageContent: []map[string]any{{"type": "output_text", "text": "final answer"}},
		Reasoning: map[string]any{
			"summary": "\u200b \ufeff\n\t",
		},
	})
	if len(messages) != 1 {
		t.Fatalf("expected one assistant history message, got %#v", messages)
	}
	if got := messages[0].ReasoningContent; got != "" {
		t.Fatalf("expected invisible synthetic reasoning summary to be dropped from history message, got %#v", messages[0])
	}
	if len(messages[0].ReasoningBlocks) != 0 {
		t.Fatalf("expected no reasoning blocks when only invisible synthetic summary exists, got %#v", messages[0])
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
