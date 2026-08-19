package httpapi

import "testing"

func TestReasoningChunkContextStoreFormatsEveryChunkForImmediateSend(t *testing.T) {
	store := reasoningChunkContextStore{tails: make(map[string]string)}
	chunks := []string{"第一段正文", "第二段正文", "**标题一**", "**标题二**", "最后一段正文"}
	want := []string{"第一段正文", "第二段正文", "**标题一**", "\n\n**标题二**", "最后一段正文"}
	for index, chunk := range chunks {
		if got := store.formatForSend("req-a", chunk, false); got != want[index] {
			t.Fatalf("chunk %d = %q, want %q", index, got, want[index])
		}
	}
	if got := store.formatForSend("req-b", "**标题二**", false); got != "**标题二**" {
		t.Fatalf("different request was affected: %q", got)
	}
	if got := store.formatForSend("", "**标题三**", false); got != "**标题三**" {
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
	if got := store.formatForSend("req", "**opaque**", true); got != "**opaque**" {
		t.Fatalf("opaque chunk changed: %q", got)
	}
	if got := store.formatForSend("req", "\n\n**标题二**", false); got != "\n\n**标题二**" {
		t.Fatalf("existing separator duplicated: %q", got)
	}
}
