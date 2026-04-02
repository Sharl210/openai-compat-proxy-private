package httpapi

import (
	"testing"

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
