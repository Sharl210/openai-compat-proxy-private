package httpapi

import (
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"openai-compat-proxy/internal/model"
)

var benchmarkResponsesHistorySnapshot responsesConversationSnapshot
var benchmarkResponsesHistoryMessages []model.CanonicalMessage

func TestResponsesHistoryStore_releases_large_canonical_payload_after_operation(t *testing.T) {
	// Given
	const (
		historyEntries      = 64
		messagePayloadBytes = 1 << 20
	)
	logicalPayloadBytes := int64(historyEntries * messagePayloadBytes)
	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)

	// When
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")
	for index := range historyEntries {
		text := fmt.Sprintf("%08d%s", index, strings.Repeat("x", messagePayloadBytes-8))
		store.Save("openai", fmt.Sprintf("resp-%03d", index), []model.CanonicalMessage{{
			Role:  "user",
			Parts: []model.CanonicalContentPart{{Type: "text", Text: text}},
		}})
	}
	var postOperationBeforeGC runtime.MemStats
	runtime.ReadMemStats(&postOperationBeforeGC)
	runtime.GC()
	var postOperationRooted runtime.MemStats
	runtime.ReadMemStats(&postOperationRooted)
	runtime.KeepAlive(store)

	store = newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")
	runtime.GC()
	var postRootReplacement runtime.MemStats
	runtime.ReadMemStats(&postRootReplacement)
	runtime.KeepAlive(store)

	// Then
	operationAllocationBytes := postOperationBeforeGC.TotalAlloc - baseline.TotalAlloc
	preGCHeapBytes := int64(postOperationBeforeGC.HeapAlloc) - int64(baseline.HeapAlloc)
	rootedHeapBytes := int64(postOperationRooted.HeapAlloc) - int64(baseline.HeapAlloc)
	releasedHeapBytes := int64(postRootReplacement.HeapAlloc) - int64(baseline.HeapAlloc)
	rootReleaseBytes := int64(postOperationRooted.HeapAlloc) - int64(postRootReplacement.HeapAlloc)
	t.Logf(
		"history lifecycle bytes: logical_payload=%d operation_alloc=%d pre_gc_heap=%d rooted_after_gc=%d released_after_gc=%d root_release=%d",
		logicalPayloadBytes,
		operationAllocationBytes,
		preGCHeapBytes,
		rootedHeapBytes,
		releasedHeapBytes,
		rootReleaseBytes,
	)

	if operationAllocationBytes < uint64(logicalPayloadBytes) {
		t.Fatalf("operation allocated %d bytes, below the %d-byte deterministic payload", operationAllocationBytes, logicalPayloadBytes)
	}
	attributionFloor := logicalPayloadBytes * 3 / 4
	if rootedHeapBytes <= attributionFloor {
		return
	}
	if rootReleaseBytes <= attributionFloor {
		t.Fatalf("post-GC heap remained high without following the history root: rooted=%d released=%d root_release=%d", rootedHeapBytes, releasedHeapBytes, rootReleaseBytes)
	}
	t.Fatalf("responsesHistoryStore retained %d bytes after GC and released %d bytes only when its root was replaced", rootedHeapBytes, rootReleaseBytes)
}

func TestResponsesHistoryStore_roundTrips_compressed_canonical_snapshot(t *testing.T) {
	// Given
	toolArguments := `{"payload":"` + strings.Repeat("tool argument ", 2048) + `"}`
	messages := []model.CanonicalMessage{{
		Role: "assistant",
		Parts: []model.CanonicalContentPart{
			{Type: "text", Text: strings.Repeat("compressible conversation text ", 4096), Raw: map[string]any{"kind": "output_text", "complete": true, "weight": 1.5}},
			{Type: "image_url", ImageURL: "data:image/png;base64," + strings.Repeat("A", 32768)},
		},
		ToolCalls:        []model.CanonicalToolCall{{ID: "call-1", Type: "function", Name: "lookup", Arguments: toolArguments}},
		ReasoningContent: strings.Repeat("reasoning ", 2048),
		ReasoningBlocks:  []map[string]any{{"type": "reasoning", "score": 0.75}},
	}}
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")

	// When
	store.Save("openai", "resp-compressed", messages)
	loaded := store.Load("openai", "resp-compressed")

	// Then
	snapshot := store.entries[responsesHistoryKey("openai", "resp-compressed")]
	if len(snapshot.CompressedFields) == 0 || len(snapshot.Messages) == 0 {
		t.Fatalf("expected typed snapshot with compressed fields, got compressed=%d messages=%d", len(snapshot.CompressedFields), len(snapshot.Messages))
	}
	want := cloneCanonicalMessages(messages)
	if !reflect.DeepEqual(loaded, want) {
		t.Fatalf("compressed snapshot changed canonical messages:\nwant=%#v\ngot=%#v", want, loaded)
	}
}

func TestResponsesHistoryStore_keeps_incompressible_strings_inline(t *testing.T) {
	// Given
	randomText := make([]byte, 128<<10)
	state := uint32(0x9e3779b9)
	for index := range randomText {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		randomText[index] = byte(32 + state%95)
	}
	messages := []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: string(randomText)}}}}
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")

	// When
	store.Save("openai", "resp-random", messages)
	loaded := store.Load("openai", "resp-random")

	// Then
	snapshot := store.entries[responsesHistoryKey("openai", "resp-random")]
	if len(snapshot.CompressedFields) != 0 {
		t.Fatalf("expected incompressible text to remain inline, got %d compressed fields", len(snapshot.CompressedFields))
	}
	if !reflect.DeepEqual(loaded, cloneCanonicalMessages(messages)) {
		t.Fatalf("inline fallback changed canonical messages: %#v", loaded)
	}
}

func TestResponsesHistoryStore_preserves_dynamic_types_while_compressing_text(t *testing.T) {
	// Given
	messages := []model.CanonicalMessage{{
		Role: "user",
		Parts: []model.CanonicalContentPart{{
			Type: "text",
			Text: strings.Repeat("typed fallback ", 8192),
			Raw:  map[string]any{"integer": 1},
		}},
	}}
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")

	// When
	store.Save("openai", "resp-fallback", messages)
	loaded := store.Load("openai", "resp-fallback")

	// Then
	snapshot := store.entries[responsesHistoryKey("openai", "resp-fallback")]
	if len(snapshot.Messages) == 0 || len(snapshot.CompressedFields) == 0 {
		t.Fatalf("expected typed snapshot with compressed text, got messages=%d compressed=%d", len(snapshot.Messages), len(snapshot.CompressedFields))
	}
	if !reflect.DeepEqual(loaded, cloneCanonicalMessages(messages)) {
		t.Fatalf("typed fallback changed canonical messages: %#v", loaded)
	}
}

func TestResponsesHistoryStore_preserves_compressed_tool_arguments_in_recovery_index(t *testing.T) {
	// Given
	arguments := `{"payload":"` + strings.Repeat("x", int(responsesHistoryCompressionMinSnapshotBytes)) + `"}`
	messages := []model.CanonicalMessage{{
		Role:      "assistant",
		ToolCalls: []model.CanonicalToolCall{{ID: "call-large", Type: "function", Name: "process", Arguments: arguments}},
	}}
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")

	// When
	store.Save("openai", "resp-large-tool", messages)
	loaded, _, ok := store.LoadToolCall("openai", "call-large")

	// Then
	if !ok || loaded.Arguments != arguments {
		t.Fatalf("expected complete compressed tool arguments in recovery index, got ok=%t bytes=%d", ok, len(loaded.Arguments))
	}
}

func BenchmarkResponsesHistorySnapshotRepresentation(b *testing.B) {
	messages := []model.CanonicalMessage{{
		Role:  "user",
		Parts: []model.CanonicalContentPart{{Type: "text", Text: strings.Repeat("representative conversation text ", 32768)}},
	}}
	logicalBytes := estimateCanonicalMessagesBytes(messages)

	b.Run("typed_clone", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			benchmarkResponsesHistoryMessages = cloneCanonicalMessages(messages)
		}
	})
	b.Run("compressed_save", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			benchmarkResponsesHistorySnapshot, _ = newResponsesConversationSnapshot(messages, logicalBytes)
		}
	})
	snapshot, _ := newResponsesConversationSnapshot(messages, logicalBytes)
	b.Run("compressed_load", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			benchmarkResponsesHistoryMessages = loadResponsesConversationSnapshot(snapshot)
		}
	})
}
