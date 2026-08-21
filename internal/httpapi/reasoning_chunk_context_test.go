package httpapi

import "testing"

func TestReasoningChunkContextStoreFormatsOnlyAdjacentStandaloneTitles(t *testing.T) {
	store := reasoningChunkContextStore{contexts: make(map[string]reasoningChunkContext)}
	if got := store.formatForSend("req-a", "第一段正文", false); got != "第一段正文" {
		t.Fatalf("first text chunk = %q", got)
	}
	if got := store.formatForSend("req-a", "**标题一**", false); got != "**标题一**" {
		t.Fatalf("title after prose changed: %q", got)
	}
	if got := store.formatForSend("req-a", "**标题二**", false); got != "**标题二**" {
		t.Fatalf("title after prose inserted a separator: %q", got)
	}
	store.delete("req-a")
	if got := store.formatForSend("req-a", "**标题一**", false); got != "**标题一**" {
		t.Fatalf("first standalone title = %q", got)
	}
	if got := store.formatForSend("req-a", "**标题二**", false); got != "\n\n**标题二**" {
		t.Fatalf("adjacent standalone titles = %q", got)
	}
	if got := store.formatForSend("req-b", "**标题二**", false); got != "**标题二**" {
		t.Fatalf("different request was affected: %q", got)
	}
	if got := store.formatForSend("", "**标题三**", false); got != "**标题三**" {
		t.Fatalf("empty request ID changed chunk: %q", got)
	}
	store.delete("req-a")
	store.delete("req-a")
	if _, ok := store.tail("req-a"); ok {
		t.Fatal("expected deleted request context to miss")
	}
}

func TestReasoningChunkContextStoreDoesNotInferTitleOutsideTenRuneTail(t *testing.T) {
	store := reasoningChunkContextStore{contexts: make(map[string]reasoningChunkContext)}
	for _, title := range []string{
		"**这是超过十个字符的标题**",
		"**an-ascii-title-longer-than-ten-runes**",
	} {
		if got := store.formatForSend("req", title, false); got != title {
			t.Fatalf("first title = %q, want %q", got, title)
		}
		if tail, ok := store.tail("req"); !ok || len([]rune(tail)) != reasoningChunkTailLimit {
			t.Fatalf("tail = %q, ok=%v", tail, ok)
		}
		if got := store.formatForSend("req", "**后续标题**", false); got != "**后续标题**" {
			t.Fatalf("truncated tail inserted a title separator: %q", got)
		}
		store.delete("req")
	}
	shortTitle := "**短标题**"
	if got := store.formatForSend("short", shortTitle, false); got != shortTitle {
		t.Fatalf("first short title = %q", got)
	}
	if got := store.formatForSend("short", "**后续标题**", false); got != "\n\n**后续标题**" {
		t.Fatalf("short title inside tail window missed separator: %q", got)
	}
}

func TestReasoningChunkContextStoreDoesNotInferStandaloneTitleFromTruncatedInlineTail(t *testing.T) {
	store := reasoningChunkContextStore{contexts: make(map[string]reasoningChunkContext)}
	const inlineEmphasis = "正文 **123456**"
	if got := store.formatForSend("req-inline", inlineEmphasis, false); got != inlineEmphasis {
		t.Fatalf("inline emphasis changed: %q", got)
	}
	if tail, ok := store.tail("req-inline"); !ok || tail != "**123456**" {
		t.Fatalf("tail=%q ok=%v, want truncated inline emphasis tail", tail, ok)
	}
	if got := store.formatForSend("req-inline", "**下一标题**", false); got != "**下一标题**" {
		t.Fatalf("truncated inline tail inserted a title separator: %q", got)
	}
}
func TestReasoningChunkContextStoreRetainsPriorTailForSplitInlineEmphasis(t *testing.T) {
	store := reasoningChunkContextStore{contexts: make(map[string]reasoningChunkContext)}
	if got := store.formatForSend("req-inline-split", "正文 ", false); got != "正文 " {
		t.Fatalf("prefix = %q", got)
	}
	if got := store.formatForSend("req-inline-split", "**短**", false); got != "**短**" {
		t.Fatalf("inline emphasis = %q", got)
	}
	if got := store.formatForSend("req-inline-split", "**下一标题**", false); got != "**下一标题**" {
		t.Fatalf("split inline emphasis inserted a title separator: %q", got)
	}
}

func TestReasoningChunkContextStoreRetainsFormatterAcrossArbitraryStarBoundaries(t *testing.T) {
	store := reasoningChunkContextStore{contexts: make(map[string]reasoningChunkContext)}
	state := &reasoningTextState{}
	chunks := []string{"**标题一**", "*", "*标题二", "**", "**标题三**"}
	want := []string{"**标题一**", "", "", "\n\n**标题二**", "\n\n**标题三**"}
	for index, chunk := range chunks {
		if got := store.formatStreamingDelta("req-a", state, chunk, false); got != want[index] {
			t.Fatalf("chunk %d = %q, want %q", index, got, want[index])
		}
	}
}

func TestReasoningChunkContextStoreSeparatesEmptyBoldMarkerAfterTitle(t *testing.T) {
	store := reasoningChunkContextStore{contexts: make(map[string]reasoningChunkContext)}
	if got := store.formatForSend("req", "**标题一**", false); got != "**标题一**" {
		t.Fatalf("first title = %q", got)
	}
	for _, marker := range []string{"****", "****标题二**"} {
		store.delete("req")
		_ = store.formatForSend("req", "**标题一**", false)
		if got := store.formatForSend("req", marker, false); got != "\n\n"+marker {
			t.Fatalf("marker %q = %q, want separator", marker, got)
		}
	}
}

func TestReasoningChunkContextStorePreservesOpaqueAndExistingSeparators(t *testing.T) {
	store := reasoningChunkContextStore{contexts: make(map[string]reasoningChunkContext)}
	state := &reasoningTextState{}
	if got := store.formatStreamingDelta("req", state, "**标题一**", false); got != "**标题一**" {
		t.Fatalf("first title = %q", got)
	}
	if got := store.formatStreamingDelta("req", state, "**opaque**", true); got != "**opaque**" {
		t.Fatalf("opaque chunk changed: %q", got)
	}
	if got := store.formatStreamingDelta("req", state, "**标题二**", false); got != "**标题二**" {
		t.Fatalf("opaque chunk leaked a title boundary: %q", got)
	}
}

func TestReasoningChunkContextStoreMaintainsIndependentRequestTails(t *testing.T) {
	store := reasoningChunkContextStore{contexts: make(map[string]reasoningChunkContext)}
	if got := store.formatForSend("req-a", "**第一标题**", false); got != "**第一标题**" {
		t.Fatalf("first request initial title = %q", got)
	}
	if got := store.formatForSend("req-b", "**另一标题**", false); got != "**另一标题**" {
		t.Fatalf("second request initial title = %q", got)
	}
	if got := store.formatForSend("req-a", "**第二标题**", false); got != "\n\n**第二标题**" {
		t.Fatalf("first request lost its own title boundary: %q", got)
	}
	if got := store.formatForSend("req-b", "正文", false); got != "正文" {
		t.Fatalf("second request inherited another request boundary: %q", got)
	}
	store.delete("req-a")
	if got := store.formatForSend("req-a", "**重启标题**", false); got != "**重启标题**" {
		t.Fatalf("deleted request kept stale context: %q", got)
	}
	if _, ok := store.tail("req-a"); !ok {
		t.Fatal("expected restarted request to record a fresh tail")
	}
}
