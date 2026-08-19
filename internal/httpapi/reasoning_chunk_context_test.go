package httpapi

import "testing"

func TestReasoningChunkContextStoreIsRequestScopedAndRuneSafe(t *testing.T) {
	store := reasoningChunkContextStore{tails: make(map[string]string)}
	if got := store.appendAndFormat("req-a", "**标题一**", false); got != "**标题一**" {
		t.Fatalf("first chunk = %q", got)
	}
	if got := store.appendAndFormat("req-a", "**标题二**", false); got != "\n\n**标题二**" {
		t.Fatalf("adjacent title chunk = %q", got)
	}
	if got := store.appendAndFormat("req-b", "**标题二**", false); got != "**标题二**" {
		t.Fatalf("different request was affected: %q", got)
	}
	if got := store.appendAndFormat("", "**标题三**", false); got != "**标题三**" {
		t.Fatalf("empty request ID changed chunk: %q", got)
	}
	store.record("req-a", "中文标题尾部", false)
	tail, ok := store.tail("req-a")
	if !ok || len([]rune(tail)) > reasoningChunkTailLimit {
		t.Fatalf("tail = %q, ok=%v", tail, ok)
	}
	store.delete("req-a")
	store.delete("req-a")
	if _, ok := store.tail("req-a"); ok {
		t.Fatal("expected deleted request context to miss")
	}
}

func TestReasoningChunkContextStorePreservesOpaqueAndExistingSeparators(t *testing.T) {
	store := reasoningChunkContextStore{tails: make(map[string]string)}
	store.record("req", "**标题一**", false)
	if got := store.appendAndFormat("req", "**opaque**", true); got != "**opaque**" {
		t.Fatalf("opaque chunk changed: %q", got)
	}
	if got := store.appendAndFormat("req", "\n\n**标题二**", false); got != "\n\n**标题二**" {
		t.Fatalf("existing separator duplicated: %q", got)
	}
}
