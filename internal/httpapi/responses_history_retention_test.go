package httpapi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"openai-compat-proxy/internal/model"
)

var benchmarkResponsesHistorySnapshot responsesConversationSnapshot
var benchmarkResponsesHistoryMessages []model.CanonicalMessage
var benchmarkResponsesHistoryToolCall responsesHistoryToolCallEntry
var benchmarkResponsesHistoryToolCallArguments string

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

func TestResponsesHistoryStoreReleasesDuplicatedRawImageURLAfterCompression(t *testing.T) {
	// Given
	imageURL := "data:image/png;base64," + strings.Repeat("A", int(responsesHistoryCompressionMinSnapshotBytes))
	messages := []model.CanonicalMessage{{
		Role: "user",
		Parts: []model.CanonicalContentPart{{
			Type:     "input_image",
			ImageURL: imageURL,
			Raw: map[string]any{
				"image_url": map[string]any{"url": imageURL, "detail": "high"},
			},
		}},
	}}
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")

	// When
	store.Save("openai", "resp-image", messages)

	// Then
	snapshot := store.entries[responsesHistoryKey("openai", "resp-image")]
	if len(snapshot.CompressedFields) == 0 {
		t.Fatal("expected large image URL to be compressed in the snapshot")
	}
	storedRaw, _ := snapshot.Messages[0].Parts[0].Raw["image_url"].(map[string]any)
	if _, ok := storedRaw["url"]; ok {
		t.Fatal("expected compressed snapshot to release the raw image URL duplicate")
	}
	if storedRaw["detail"] != "high" {
		t.Fatalf("expected image metadata to remain in the snapshot, got %#v", storedRaw)
	}
	originalRaw, _ := messages[0].Parts[0].Raw["image_url"].(map[string]any)
	if originalRaw["url"] != imageURL {
		t.Fatalf("expected caller message to remain unchanged, got %#v", originalRaw)
	}

	loaded := store.Load("openai", "resp-image")
	if len(loaded) != 1 || len(loaded[0].Parts) != 1 || loaded[0].Parts[0].ImageURL != imageURL {
		t.Fatalf("expected image URL to round-trip, got %#v", loaded)
	}
	loadedRaw, _ := loaded[0].Parts[0].Raw["image_url"].(map[string]any)
	if loadedRaw["url"] != imageURL || loadedRaw["detail"] != "high" {
		t.Fatalf("expected loaded image metadata to be restored, got %#v", loadedRaw)
	}
	if _, ok := storedRaw["url"]; ok {
		t.Fatal("expected loading to leave the stored compressed snapshot compact")
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

func TestResponsesHistoryCompressesLargeToolCallRecoveryArguments(t *testing.T) {
	arguments := `{"payload":"` + strings.Repeat("tool argument ", 8192) + `"}`
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")

	store.Save("openai", "resp-large-call", []model.CanonicalMessage{{
		Role: "assistant",
		ToolCalls: []model.CanonicalToolCall{{
			ID:        "call-large",
			Type:      "function",
			Name:      "process",
			Arguments: arguments,
		}},
	}})

	entry, ok := store.toolCalls[responsesHistoryToolCallKey("openai", "call-large")]
	if !ok {
		t.Fatal("expected large tool call to be indexed")
	}
	if entry.Call.Arguments != "" {
		t.Fatalf("expected uncompressed recovery argument to be released, got %d bytes", len(entry.Call.Arguments))
	}
	if len(entry.ArgumentsCompressed) == 0 || entry.ArgumentsOriginalSize != len(arguments) {
		t.Fatalf("expected compressed recovery argument metadata, got compressed=%d original=%d", len(entry.ArgumentsCompressed), entry.ArgumentsOriginalSize)
	}

	loaded, _, ok := store.LoadToolCall("openai", "call-large")
	if !ok || loaded.Arguments != arguments {
		t.Fatalf("expected large recovery argument to round-trip, got ok=%t bytes=%d", ok, len(loaded.Arguments))
	}
}

func TestResponsesHistorySharesCompressedToolArgumentsWithSnapshot(t *testing.T) {
	// Given
	arguments := `{"payload":"` + strings.Repeat("shared compressed tool argument ", 8192) + `"}`
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")

	// When
	store.Save("openai", "resp-shared-tool", []model.CanonicalMessage{{
		Role: "assistant",
		ToolCalls: []model.CanonicalToolCall{{
			ID:        "call-shared-tool",
			Type:      "function",
			Name:      "process",
			Arguments: arguments,
		}},
	}})

	// Then
	snapshot := store.entries[responsesHistoryKey("openai", "resp-shared-tool")]
	var snapshotArguments []byte
	for _, field := range snapshot.CompressedFields {
		if field.Kind == responsesHistoryCompressedToolArguments && field.MessageIndex == 0 && field.ItemIndex == 0 {
			snapshotArguments = field.Data
			break
		}
	}
	entry, ok := store.toolCalls[responsesHistoryToolCallKey("openai", "call-shared-tool")]
	if !ok || len(snapshotArguments) == 0 || len(entry.ArgumentsCompressed) == 0 {
		t.Fatalf("expected snapshot and recovery entry to hold compressed arguments, got snapshot=%d recovery=%d present=%t", len(snapshotArguments), len(entry.ArgumentsCompressed), ok)
	}
	if &snapshotArguments[0] != &entry.ArgumentsCompressed[0] {
		t.Fatal("expected snapshot and recovery entry to share one compressed argument allocation")
	}

	loaded, _, ok := store.LoadToolCall("openai", "call-shared-tool")
	if !ok || loaded.Arguments != arguments {
		t.Fatalf("expected shared compressed arguments to remain recoverable, got ok=%t bytes=%d", ok, len(loaded.Arguments))
	}
}

func TestResponsesHistoryCompressesLargeRecoveredToolCallArguments(t *testing.T) {
	arguments := `{"payload":"` + strings.Repeat("recovered tool argument ", 8192) + `"}`
	recovered := model.CanonicalToolCall{ID: "call-recovered-large", Type: "function", Name: "process", Arguments: arguments}
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")

	store.Save("openai", "resp-recovered-large", []model.CanonicalMessage{{
		Role:              "tool",
		ToolCallID:        recovered.ID,
		RecoveredToolCall: &recovered,
		ReasoningBlocks: []map[string]any{{
			"type":     "thinking",
			"thinking": "preserve this block",
		}},
	}})

	entry, ok := store.toolCalls[responsesHistoryToolCallKey("openai", recovered.ID)]
	if !ok || entry.Call.Arguments != "" || len(entry.ArgumentsCompressed) == 0 {
		t.Fatalf("expected recovered tool arguments to be compressed, got ok=%t raw=%d compressed=%d", ok, len(entry.Call.Arguments), len(entry.ArgumentsCompressed))
	}
	loaded, reasoningBlocks, ok := store.LoadToolCall("openai", recovered.ID)
	if !ok || loaded.Arguments != arguments || len(reasoningBlocks) != 1 || reasoningBlocks[0]["thinking"] != "preserve this block" {
		t.Fatalf("expected recovered tool argument and reasoning to round-trip, got ok=%t bytes=%d reasoning=%#v", ok, len(loaded.Arguments), reasoningBlocks)
	}
}

func TestResponsesHistoryCompressedToolCallArgumentsCountLogicalBytes(t *testing.T) {
	entry := responsesHistoryToolCallEntry{
		Call:                  model.CanonicalToolCall{ID: "call", Type: "function", Name: "process"},
		ArgumentsCompressed:   []byte("compressed"),
		ArgumentsOriginalSize: 128,
	}

	want := int64(len(entry.Call.ID) + len(entry.Call.Type) + len(entry.Call.Name) + entry.ArgumentsOriginalSize)
	if got := estimateResponsesHistoryToolCallEntryBytes(entry); got != want {
		t.Fatalf("expected logical recovery size %d, got %d", want, got)
	}
}

func TestDecompressResponsesHistoryStringRejectsOversizedOriginalSize(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("expected oversized original size to fail closed, panicked with %v", recovered)
		}
	}()

	if _, ok := decompressResponsesHistoryString([]byte("invalid"), int(defaultResponsesHistoryMaxBytes+1)); ok {
		t.Fatal("expected oversized original size to be rejected")
	}
}

func TestResponsesHistoryRejectsInvalidCompressedToolCallMetadata(t *testing.T) {
	indexPath := filepath.Join(t.TempDir(), "tool_call_recovery_index.json")
	index := responsesHistoryToolCallRecoveryIndexFile{
		Version: 1,
		Order:   []string{"openai::resp-corrupt"},
		ToolCalls: map[string]responsesHistoryToolCallEntry{
			"openai::call-corrupt": {
				SnapshotKey: "openai::resp-corrupt",
				Call: model.CanonicalToolCall{
					ID:   "call-corrupt",
					Type: "function",
					Name: "process",
				},
				ArgumentsCompressed:   []byte(strings.Repeat("x", 64<<10)),
				ArgumentsOriginalSize: 1,
			},
		},
	}
	data, err := json.Marshal(index)
	if err != nil {
		t.Fatalf("marshal corrupt recovery index: %v", err)
	}
	if err := os.WriteFile(indexPath, data, 0o600); err != nil {
		t.Fatalf("write corrupt recovery index: %v", err)
	}

	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)
	if _, _, ok := store.LoadToolCall("openai", "call-corrupt"); ok {
		t.Fatal("expected invalid compressed tool-call metadata to be rejected")
	}
	if store.retainedBytes != 0 || len(store.toolCalls) != 0 {
		t.Fatalf("expected invalid compressed entry not to consume retained state, bytes=%d calls=%d", store.retainedBytes, len(store.toolCalls))
	}
}

func TestResponsesHistoryRecoveryIndexAcceptsExactSizeBoundaryAndRejectsOversizeBeforeRead(t *testing.T) {
	indexPath := filepath.Join(t.TempDir(), "tool_call_recovery_index.json")
	index := responsesHistoryToolCallRecoveryIndexFile{
		Version: 1,
		Order:   []string{"openai::resp-boundary"},
		ToolCalls: map[string]responsesHistoryToolCallEntry{
			"openai::call-boundary": {
				SnapshotKey: "openai::resp-boundary",
				Call: model.CanonicalToolCall{
					ID:        "call-boundary",
					Type:      "function",
					Name:      "process",
					Arguments: `{}`,
				},
			},
		},
	}
	data, err := json.Marshal(index)
	if err != nil {
		t.Fatalf("marshal recovery index: %v", err)
	}
	if err := os.WriteFile(indexPath, data, 0o600); err != nil {
		t.Fatalf("write recovery index: %v", err)
	}

	boundaryStore := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)
	boundaryStore.toolCallRecoveryIndexMaxBytes = int64(len(data))
	if call, _, ok := boundaryStore.LoadToolCall("openai", "call-boundary"); !ok || call.Arguments != `{}` {
		t.Fatalf("expected exact boundary legacy recovery index to load, got ok=%t call=%#v", ok, call)
	}

	oversizedStore := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)
	oversizedStore.toolCallRecoveryIndexMaxBytes = int64(len(data) - 1)
	if _, _, ok := oversizedStore.LoadToolCall("openai", "call-boundary"); ok {
		t.Fatal("expected oversized recovery index to be rejected")
	}
	if len(oversizedStore.toolCalls) != 0 || oversizedStore.retainedBytes != 0 {
		t.Fatalf("expected oversized index to leave no retained recovery state, calls=%d bytes=%d", len(oversizedStore.toolCalls), oversizedStore.retainedBytes)
	}
	if !oversizedStore.toolCallRecoveryIndexLoaded {
		t.Fatal("expected oversized recovery index to be marked handled after rejection")
	}
	if err := oversizedStore.loadToolCallRecoveryIndexLocked(); err != nil {
		t.Fatalf("expected repeated oversized-index load to avoid another failure, got %v", err)
	}
}

func TestResponsesHistoryRejectsTruncatedCompressedToolCallBeforeRetention(t *testing.T) {
	indexPath := filepath.Join(t.TempDir(), "tool_call_recovery_index.json")
	arguments := `{"payload":"` + strings.Repeat("truncated tool argument ", 4096) + `"}`
	compressed, ok := compressResponsesHistoryString(arguments)
	if !ok || len(compressed) < 2 {
		t.Fatal("expected representative tool arguments to compress")
	}
	index := responsesHistoryToolCallRecoveryIndexFile{
		Version: 1,
		Order:   []string{"openai::resp-truncated"},
		ToolCalls: map[string]responsesHistoryToolCallEntry{
			"openai::call-truncated": {
				SnapshotKey: "openai::resp-truncated",
				Call: model.CanonicalToolCall{
					ID:   "call-truncated",
					Type: "function",
					Name: "process",
				},
				ArgumentsCompressed:   compressed[:len(compressed)-1],
				ArgumentsOriginalSize: len(arguments),
			},
		},
	}
	data, err := json.Marshal(index)
	if err != nil {
		t.Fatalf("marshal truncated recovery index: %v", err)
	}
	if err := os.WriteFile(indexPath, data, 0o600); err != nil {
		t.Fatalf("write truncated recovery index: %v", err)
	}

	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)
	if _, _, ok := store.LoadToolCall("openai", "call-truncated"); ok {
		t.Fatal("expected truncated compressed arguments to be rejected before lookup")
	}
	if store.retainedBytes != 0 || len(store.toolCalls) != 0 {
		t.Fatalf("expected truncated entry not to consume retained state, bytes=%d calls=%d", store.retainedBytes, len(store.toolCalls))
	}
}

func TestResponsesHistoryRejectsInvalidCompressedMetadataDespiteRawCompatibilityCopy(t *testing.T) {
	indexPath := filepath.Join(t.TempDir(), "tool_call_recovery_index.json")
	arguments := `{"payload":"` + strings.Repeat("raw compatibility argument ", 4096) + `"}`
	compressed, ok := compressResponsesHistoryString(arguments)
	if !ok || len(compressed) < 2 {
		t.Fatal("expected representative tool arguments to compress")
	}
	index := responsesHistoryToolCallRecoveryIndexFile{
		Version: 1,
		Order:   []string{"openai::resp-raw-corrupt"},
		ToolCalls: map[string]responsesHistoryToolCallEntry{
			"openai::call-raw-corrupt": {
				SnapshotKey: "openai::resp-raw-corrupt",
				Call: model.CanonicalToolCall{
					ID:        "call-raw-corrupt",
					Type:      "function",
					Name:      "process",
					Arguments: arguments,
				},
				ArgumentsCompressed:   compressed[:len(compressed)-1],
				ArgumentsOriginalSize: len(arguments),
			},
		},
	}
	data, err := json.Marshal(index)
	if err != nil {
		t.Fatalf("marshal raw-corrupt recovery index: %v", err)
	}
	if err := os.WriteFile(indexPath, data, 0o600); err != nil {
		t.Fatalf("write raw-corrupt recovery index: %v", err)
	}

	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)
	if _, _, ok := store.LoadToolCall("openai", "call-raw-corrupt"); ok {
		t.Fatal("expected invalid compressed metadata to reject raw compatibility fallback")
	}
	if store.retainedBytes != 0 || len(store.toolCalls) != 0 {
		t.Fatalf("expected raw-corrupt entry not to consume retained state, bytes=%d calls=%d", store.retainedBytes, len(store.toolCalls))
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

func BenchmarkResponsesHistoryToolCallRecoveryRepresentation(b *testing.B) {
	arguments := `{"payload":"` + strings.Repeat("tool argument ", 8192) + `"}`
	raw := responsesHistoryToolCallEntry{Call: model.CanonicalToolCall{
		ID:        "call-large",
		Type:      "function",
		Name:      "process",
		Arguments: arguments,
	}}
	compressed := compressResponsesHistoryToolCallEntry(raw)

	b.Run("raw_entry", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(arguments)))
		for range b.N {
			benchmarkResponsesHistoryToolCall = raw
		}
	})
	b.Run("compressed_entry", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(arguments)))
		b.ReportMetric(float64(len(compressed.ArgumentsCompressed)), "compressed-bytes")
		for range b.N {
			benchmarkResponsesHistoryToolCall = compressed
		}
	})
	b.Run("decompress", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(arguments)))
		for range b.N {
			benchmarkResponsesHistoryToolCallArguments, _ = loadResponsesHistoryToolCallArguments(compressed)
		}
	})
}
