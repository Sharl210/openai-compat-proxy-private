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
