package httpapi

import (
	"bytes"
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

func TestSaveResponsesHistorySnapshotUsesStoreDefensiveClone(t *testing.T) {
	base := []model.CanonicalMessage{{
		Role:  "user",
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "original user message"}},
	}}
	assistant := []model.CanonicalMessage{{
		Role:      "assistant",
		ToolCalls: []model.CanonicalToolCall{{ID: "call-1", Type: "function", Name: "lookup", Arguments: `{"query":"weather"}`}},
	}}
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")

	saveResponsesHistorySnapshot(store, "openai", "resp-defensive-clone", base, assistant)
	base[0].Parts[0].Text = "mutated user message"
	assistant[0].ToolCalls[0].Name = "mutated"

	loaded := store.Load("openai", "resp-defensive-clone")
	if len(loaded) != 2 {
		t.Fatalf("expected user and assistant tool-call messages, got %#v", loaded)
	}
	if loaded[0].Parts[0].Text != "original user message" {
		t.Fatalf("expected stored user message to remain isolated, got %#v", loaded[0])
	}
	if loaded[1].ToolCalls[0].Name != "lookup" {
		t.Fatalf("expected stored tool call to remain isolated, got %#v", loaded[1])
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

func TestResponsesHistoryStoreRoundTripsCompressedDynamicRawWithoutRetainingCallerData(t *testing.T) {
	// Given
	payload := strings.Repeat("dynamic raw payload ", 8192)
	raw := map[string]any{
		"input_audio": map[string]any{
			"data":   payload,
			"format": "wav",
			"chunks": []any{map[string]any{"payload": payload, "sequence": json.Number("7")}},
		},
		"object_blocks": []map[string]any{{"payload": payload}},
		"opaque":        json.RawMessage(`{"kind":"vendor"}`),
		"bytes":         []byte("binary payload"),
	}
	messages := []model.CanonicalMessage{{
		Role:  "user",
		Parts: []model.CanonicalContentPart{{Type: "input_audio", Raw: raw}},
	}}
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")

	// When
	store.Save("openai", "resp-dynamic-raw", messages)
	snapshot := store.entries[responsesHistoryKey("openai", "resp-dynamic-raw")]

	// Then
	var compressedRawFields int
	for _, field := range snapshot.CompressedFields {
		if field.Kind == responsesHistoryCompressedPartRawString {
			compressedRawFields++
		}
	}
	if compressedRawFields < 3 {
		t.Fatalf("expected nested dynamic raw strings to be compressed, got %d fields", compressedRawFields)
	}
	storedAudio, _ := snapshot.Messages[0].Parts[0].Raw["input_audio"].(map[string]any)
	if storedAudio["data"] != "" {
		t.Fatalf("expected compressed raw payload to be released, got %d bytes", len(stringValue(storedAudio["data"])))
	}
	storedChunks, _ := storedAudio["chunks"].([]any)
	storedChunk, _ := storedChunks[0].(map[string]any)
	if storedChunk["payload"] != "" {
		t.Fatalf("expected nested compressed raw payload to be released, got %d bytes", len(stringValue(storedChunk["payload"])))
	}
	storedBlocks, _ := snapshot.Messages[0].Parts[0].Raw["object_blocks"].([]map[string]any)
	if storedBlocks[0]["payload"] != "" {
		t.Fatalf("expected typed map slice payload to be released, got %d bytes", len(stringValue(storedBlocks[0]["payload"])))
	}

	originalAudio, _ := raw["input_audio"].(map[string]any)
	if originalAudio["data"] != payload {
		t.Fatalf("expected caller raw data to remain unchanged, got %#v", originalAudio)
	}
	originalAudio["data"] = "caller mutation"
	originalChunks, _ := originalAudio["chunks"].([]any)
	originalChunk, _ := originalChunks[0].(map[string]any)
	originalChunk["payload"] = "caller mutation"

	loaded := store.Load("openai", "resp-dynamic-raw")
	if len(loaded) != 1 || len(loaded[0].Parts) != 1 {
		t.Fatalf("expected dynamic raw snapshot to load, got %#v", loaded)
	}
	loadedRaw := loaded[0].Parts[0].Raw
	loadedAudio, _ := loadedRaw["input_audio"].(map[string]any)
	if loadedAudio["data"] != payload {
		t.Fatalf("expected restored raw payload, got %d bytes", len(stringValue(loadedAudio["data"])))
	}
	loadedChunks, _ := loadedAudio["chunks"].([]any)
	loadedChunk, _ := loadedChunks[0].(map[string]any)
	if loadedChunk["payload"] != payload || loadedChunk["sequence"] != json.Number("7") {
		t.Fatalf("expected nested dynamic values to round-trip, got %#v", loadedChunk)
	}
	loadedBlocks, _ := loadedRaw["object_blocks"].([]map[string]any)
	if len(loadedBlocks) != 1 || loadedBlocks[0]["payload"] != payload {
		t.Fatalf("expected typed map slice payload to round-trip, got %#v", loadedBlocks)
	}
	if got, ok := loadedRaw["opaque"].(json.RawMessage); !ok || string(got) != `{"kind":"vendor"}` {
		t.Fatalf("expected json.RawMessage type to round-trip, got %#v", loadedRaw["opaque"])
	}
	if got, ok := loadedRaw["bytes"].([]byte); !ok || string(got) != "binary payload" {
		t.Fatalf("expected []byte type to round-trip, got %#v", loadedRaw["bytes"])
	}
	loadedAudio["data"] = "load mutation"
	if reloaded := store.Load("openai", "resp-dynamic-raw"); reloaded[0].Parts[0].Raw["input_audio"].(map[string]any)["data"] != payload {
		t.Fatalf("expected loaded raw mutation not to affect the stored snapshot, got %#v", reloaded)
	}
}

func TestResponsesHistoryStoreSharesCompressedReasoningWithToolRecovery(t *testing.T) {
	// Given
	payload := strings.Repeat("opaque reasoning state ", 8192)
	reasoning := map[string]any{
		"id":                "rs_upstream",
		"type":              "reasoning",
		"encrypted_content": payload,
		"summary":           []any{map[string]any{"type": "summary_text", "text": payload}},
	}
	messages := []model.CanonicalMessage{{
		Role:            "assistant",
		ReasoningBlocks: []map[string]any{reasoning},
		ToolCalls: []model.CanonicalToolCall{{
			ID:        "call-reasoning-share",
			Type:      "function",
			Name:      "lookup",
			Arguments: `{"query":"weather"}`,
		}},
	}}
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")

	// When
	store.Save("openai", "resp-reasoning-share", messages)

	// Then
	snapshot := store.entries[responsesHistoryKey("openai", "resp-reasoning-share")]
	var compressedReasoningFields int
	for _, field := range snapshot.CompressedFields {
		if field.Kind == responsesHistoryCompressedReasoningBlockString {
			compressedReasoningFields++
		}
	}
	if compressedReasoningFields < 2 {
		t.Fatalf("expected reasoning state fields to be compressed, got %d", compressedReasoningFields)
	}
	storedReasoning := snapshot.Messages[0].ReasoningBlocks[0]
	if storedReasoning["encrypted_content"] != "" {
		t.Fatalf("expected compressed reasoning state to release raw payload, got %d bytes", len(stringValue(storedReasoning["encrypted_content"])))
	}
	entry, found := store.toolCalls[responsesHistoryToolCallKey("openai", "call-reasoning-share")]
	if !found || !entry.ReasoningBlocksFromSnapshot || len(entry.ReasoningBlocks) != 0 {
		t.Fatalf("expected tool recovery to reference the compressed snapshot, got %#v", entry)
	}

	call, blocks, ok := store.LoadToolCall("openai", "call-reasoning-share")
	if !ok || call.Arguments != `{"query":"weather"}` || !reflect.DeepEqual(blocks, []map[string]any{reasoning}) {
		t.Fatalf("expected tool recovery reasoning to round-trip, got ok=%t call=%#v blocks=%#v", ok, call, blocks)
	}

	blocks[0]["encrypted_content"] = "caller mutation"
	_, reloadedBlocks, ok := store.LoadToolCall("openai", "call-reasoning-share")
	if !ok || reloadedBlocks[0]["encrypted_content"] != payload {
		t.Fatalf("expected tool recovery load to remain isolated, got ok=%t blocks=%#v", ok, reloadedBlocks)
	}
}

func TestResponsesHistoryStoreLoadsToolMetadataWithoutHydratingUnrelatedCompressedFields(t *testing.T) {
	// Given
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")
	store.Save("openai", "resp-tool-metadata", []model.CanonicalMessage{{
		Role:  "assistant",
		Parts: []model.CanonicalContentPart{{Type: "text", Text: strings.Repeat("unrelated snapshot payload ", 8192)}},
		ToolCalls: []model.CanonicalToolCall{{
			ID:             "call-metadata-only",
			ResponseItemID: "fc_metadata_only",
			Type:           "function",
			Name:           "lookup",
			Arguments:      `{"query":"weather"}`,
		}},
	}})
	key := responsesHistoryKey("openai", "resp-tool-metadata")
	snapshot := store.entries[key]
	corrupted := false
	for index := range snapshot.CompressedFields {
		if snapshot.CompressedFields[index].Kind != responsesHistoryCompressedPartText {
			continue
		}
		snapshot.CompressedFields[index].Data = []byte("corrupt")
		corrupted = true
		break
	}
	if !corrupted {
		t.Fatal("expected unrelated text field to be compressed")
	}
	store.entries[key] = snapshot

	// When
	call, reasoningBlocks, ok := store.LoadToolCall("openai", "call-metadata-only")

	// Then
	if !ok || call.ID != "call-metadata-only" || call.Arguments != `{"query":"weather"}` || len(reasoningBlocks) != 0 {
		t.Fatalf("expected tool metadata to remain recoverable without hydrating unrelated fields, got ok=%t call=%#v reasoning=%#v", ok, call, reasoningBlocks)
	}
	recovered := recoverToolCallsForMessages(store, []model.CanonicalMessage{{Role: "tool", ToolCallID: "call-metadata-only"}}, "openai")
	if recovered[0].RecoveredToolCall == nil || recovered[0].RecoveredToolCall.ID != "call-metadata-only" {
		t.Fatalf("expected tool recovery to avoid unrelated snapshot hydration, got %#v", recovered)
	}
	references := recoverResponseItemReferencesForMessages(store, []model.CanonicalMessage{{Role: "tool", ToolCallID: "call-metadata-only"}}, "openai")
	if references["call-metadata-only"] != "fc_metadata_only" {
		t.Fatalf("expected response-item reference recovery to avoid unrelated snapshot hydration, got %#v", references)
	}
}

func TestResponsesHistoryStoreFallbackSharesCompressedReasoningAcrossToolCalls(t *testing.T) {
	// Given
	payload := strings.Repeat("fallback reasoning payload ", 4096)
	reasoning := map[string]any{"id": "rs-fallback", "type": "reasoning", "encrypted_content": payload}
	indexPath := filepath.Join(t.TempDir(), "tool_call_recovery_index.json")
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)
	store.maxBytes = 240 << 10
	messages := []model.CanonicalMessage{{
		Role:            "assistant",
		Parts:           []model.CanonicalContentPart{{Type: "text", Text: strings.Repeat("snapshot payload ", 8192)}},
		ReasoningBlocks: []map[string]any{reasoning},
		ToolCalls: []model.CanonicalToolCall{
			{ID: "call-fallback-1", Type: "function", Name: "lookup", Arguments: `{}`},
			{ID: "call-fallback-2", Type: "function", Name: "lookup", Arguments: `{}`},
		},
	}}

	// When
	store.Save("openai", "resp-fallback", messages)

	// Then
	key := responsesHistoryKey("openai", "resp-fallback")
	snapshot := store.entries[key]
	if len(snapshot.Messages) != 0 {
		t.Fatalf("expected oversized history to use tool-recovery fallback, got %#v", snapshot)
	}
	first, firstFound := store.toolCalls[responsesHistoryToolCallKey("openai", "call-fallback-1")]
	second, secondFound := store.toolCalls[responsesHistoryToolCallKey("openai", "call-fallback-2")]
	if !firstFound || !secondFound || first.SharedReasoningSnapshot == nil || first.SharedReasoningSnapshot != second.SharedReasoningSnapshot || len(first.ReasoningBlocks) != 0 || len(first.SharedReasoningSnapshot.CompressedFields) == 0 {
		t.Fatalf("expected fallback tool entries to share compressed reasoning, got first=%#v second=%#v", first, second)
	}
	persistedData, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read persisted fallback recovery index: %v", err)
	}
	var persisted responsesHistoryToolCallRecoveryIndexFile
	if err := json.Unmarshal(persistedData, &persisted); err != nil {
		t.Fatalf("decode persisted fallback recovery index: %v", err)
	}
	if len(persisted.ReasoningSnapshots) != 1 {
		t.Fatalf("expected one shared persisted reasoning snapshot, got %d", len(persisted.ReasoningSnapshots))
	}
	firstPersisted := persisted.ToolCalls[responsesHistoryToolCallKey("openai", "call-fallback-1")]
	secondPersisted := persisted.ToolCalls[responsesHistoryToolCallKey("openai", "call-fallback-2")]
	if firstPersisted.ReasoningSnapshotKey == "" || firstPersisted.ReasoningSnapshotKey != secondPersisted.ReasoningSnapshotKey || len(firstPersisted.ReasoningBlocks) != 0 || len(secondPersisted.ReasoningBlocks) != 0 {
		t.Fatalf("expected persisted tool entries to reference one reasoning snapshot, got first=%#v second=%#v", firstPersisted, secondPersisted)
	}
	persistedSnapshot := persisted.ReasoningSnapshots[firstPersisted.ReasoningSnapshotKey]
	if len(persistedSnapshot.CompressedFields) == 0 || persistedSnapshot.Blocks[0]["encrypted_content"] != "" {
		t.Fatalf("expected persisted reasoning snapshot to stay compressed, got %#v", persistedSnapshot)
	}
	for _, callID := range []string{"call-fallback-1", "call-fallback-2"} {
		_, blocks, ok := store.LoadToolCall("openai", callID)
		if !ok || !reflect.DeepEqual(blocks, []map[string]any{reasoning}) {
			t.Fatalf("expected fallback tool reasoning to round-trip for %s, got ok=%t blocks=%#v", callID, ok, blocks)
		}
	}

	reloaded := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)
	if _, _, ok := reloaded.LoadToolCall("openai", "call-fallback-1"); !ok {
		t.Fatal("expected persisted fallback tool call to load before checking shared state")
	}
	reloadedFirst := reloaded.toolCalls[responsesHistoryToolCallKey("openai", "call-fallback-1")]
	reloadedSecond := reloaded.toolCalls[responsesHistoryToolCallKey("openai", "call-fallback-2")]
	if reloadedFirst.SharedReasoningSnapshot == nil || reloadedFirst.SharedReasoningSnapshot != reloadedSecond.SharedReasoningSnapshot || len(reloadedFirst.ReasoningBlocks) != 0 || len(reloadedSecond.ReasoningBlocks) != 0 {
		t.Fatalf("expected reload to retain one shared compressed reasoning snapshot, got first=%#v second=%#v", reloadedFirst, reloadedSecond)
	}
	for _, callID := range []string{"call-fallback-1", "call-fallback-2"} {
		_, blocks, ok := reloaded.LoadToolCall("openai", callID)
		if !ok || !reflect.DeepEqual(blocks, []map[string]any{reasoning}) {
			t.Fatalf("expected persisted fallback tool reasoning to round-trip for %s, got ok=%t blocks=%#v", callID, ok, blocks)
		}
	}
}

func TestResponsesHistoryRecoveryIndexMigratesLegacyReasoningToSharedCompressedSnapshot(t *testing.T) {
	// Given
	indexPath := filepath.Join(t.TempDir(), "tool_call_recovery_index.json")
	reasoning := map[string]any{"id": "rs-legacy", "type": "reasoning", "encrypted_content": strings.Repeat("legacy reasoning payload ", 4096)}
	snapshotKey := responsesHistoryKey("openai", "resp-legacy")
	legacy := responsesHistoryToolCallRecoveryIndexFile{
		Version: 1,
		Order:   []string{snapshotKey},
		ToolCalls: map[string]responsesHistoryToolCallEntry{
			responsesHistoryToolCallKey("openai", "call-legacy-1"): {SnapshotKey: snapshotKey, Call: model.CanonicalToolCall{ID: "call-legacy-1", Type: "function", Name: "lookup", Arguments: `{}`}, ReasoningBlocks: []map[string]any{reasoning}},
			responsesHistoryToolCallKey("openai", "call-legacy-2"): {SnapshotKey: snapshotKey, Call: model.CanonicalToolCall{ID: "call-legacy-2", Type: "function", Name: "lookup", Arguments: `{}`}, ReasoningBlocks: []map[string]any{reasoning}},
		},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy recovery index: %v", err)
	}
	if err := os.WriteFile(indexPath, data, 0o600); err != nil {
		t.Fatalf("write legacy recovery index: %v", err)
	}

	// When
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)
	_, blocks, ok := store.LoadToolCall("openai", "call-legacy-1")

	// Then
	if !ok || !reflect.DeepEqual(blocks, []map[string]any{reasoning}) {
		t.Fatalf("expected legacy reasoning to remain recoverable, got ok=%t blocks=%#v", ok, blocks)
	}
	first := store.toolCalls[responsesHistoryToolCallKey("openai", "call-legacy-1")]
	second := store.toolCalls[responsesHistoryToolCallKey("openai", "call-legacy-2")]
	if first.SharedReasoningSnapshot == nil || first.SharedReasoningSnapshot != second.SharedReasoningSnapshot || len(first.ReasoningBlocks) != 0 || len(second.ReasoningBlocks) != 0 {
		t.Fatalf("expected legacy reasoning to migrate into one shared compressed snapshot, got first=%#v second=%#v", first, second)
	}
	store.mu.Lock()
	err = store.saveToolCallRecoveryIndexLocked()
	store.mu.Unlock()
	if err != nil {
		t.Fatalf("rewrite legacy recovery index: %v", err)
	}
	data, err = os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read rewritten recovery index: %v", err)
	}
	var rewritten responsesHistoryToolCallRecoveryIndexFile
	if err := json.Unmarshal(data, &rewritten); err != nil {
		t.Fatalf("decode rewritten recovery index: %v", err)
	}
	if len(rewritten.ReasoningSnapshots) != 1 || len(rewritten.ToolCalls[responsesHistoryToolCallKey("openai", "call-legacy-1")].ReasoningBlocks) != 0 {
		t.Fatalf("expected rewritten legacy index to keep one compressed snapshot, got %#v", rewritten)
	}
}

func TestCloneResponsesHistoryDynamicValuePreservesNonNilEmptySlices(t *testing.T) {
	// Given
	messages := []model.CanonicalMessage{
		{
			Role: "user",
			Parts: []model.CanonicalContentPart{{
				Type: "input_file",
				Raw: map[string]any{
					"strings": []string{},
					"raw":     json.RawMessage{},
					"bytes":   []byte{},
				},
			}},
		},
		{Role: "assistant", Parts: []model.CanonicalContentPart{}, ToolCalls: []model.CanonicalToolCall{}, ReasoningBlocks: []map[string]any{}},
	}

	// When
	cloned := cloneCanonicalMessages(messages)

	// Then
	if cloned[1].Parts == nil || cloned[1].ToolCalls == nil || cloned[1].ReasoningBlocks == nil {
		t.Fatalf("expected non-nil empty top-level slices to remain non-nil, got %#v", cloned[1])
	}
	raw := cloned[0].Parts[0].Raw
	if values, ok := raw["strings"].([]string); !ok || values == nil || len(values) != 0 {
		t.Fatalf("expected non-nil empty []string, got %#v", raw["strings"])
	}
	if value, ok := raw["raw"].(json.RawMessage); !ok || value == nil || len(value) != 0 {
		t.Fatalf("expected non-nil empty json.RawMessage, got %#v", raw["raw"])
	}
	if value, ok := raw["bytes"].([]byte); !ok || value == nil || len(value) != 0 {
		t.Fatalf("expected non-nil empty []byte, got %#v", raw["bytes"])
	}
}

func TestResponsesHistoryStorePersistsMaterializedCompressedReasoningForToolRecovery(t *testing.T) {
	// Given
	payload := strings.Repeat("persisted opaque reasoning ", 8192)
	reasoning := map[string]any{"id": "rs_upstream", "type": "reasoning", "encrypted_content": payload}
	indexPath := filepath.Join(t.TempDir(), "tool_call_recovery_index.json")
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)
	store.Save("openai", "resp-persisted-reasoning", []model.CanonicalMessage{{
		Role:            "assistant",
		ReasoningBlocks: []map[string]any{reasoning},
		ToolCalls: []model.CanonicalToolCall{{
			ID:        "call-persisted-reasoning",
			Type:      "function",
			Name:      "lookup",
			Arguments: `{"query":"weather"}`,
		}},
	}})

	// When
	stored := store.toolCalls[responsesHistoryToolCallKey("openai", "call-persisted-reasoning")]
	if !stored.ReasoningBlocksFromSnapshot {
		t.Fatalf("expected in-memory tool entry to reference its full snapshot, got %#v", stored)
	}
	inMemorySnapshot, snapshotOK := newResponsesHistoryReasoningSnapshotFromConversationSnapshot(store.entries[responsesHistoryKey("openai", "resp-persisted-reasoning")], 0)
	if !snapshotOK || inMemorySnapshot == nil || len(inMemorySnapshot.CompressedFields) == 0 {
		t.Fatalf("expected in-memory full snapshot reasoning to remain serializable, got snapshot=%#v ok=%t", inMemorySnapshot, snapshotOK)
	}
	persistedData, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read persisted reasoning recovery index: %v", err)
	}
	var persisted responsesHistoryToolCallRecoveryIndexFile
	if err := json.Unmarshal(persistedData, &persisted); err != nil {
		t.Fatalf("decode persisted reasoning recovery index: %v", err)
	}
	persistedEntry := persisted.ToolCalls[responsesHistoryToolCallKey("openai", "call-persisted-reasoning")]
	if len(persisted.ReasoningSnapshots) != 1 || persistedEntry.ReasoningSnapshotKey == "" || len(persistedEntry.ReasoningBlocks) != 0 {
		t.Fatalf("expected persisted reasoning reference and one compressed snapshot, got entry=%#v snapshots=%#v", persistedEntry, persisted.ReasoningSnapshots)
	}
	reloaded := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)
	call, blocks, ok := reloaded.LoadToolCall("openai", "call-persisted-reasoning")

	// Then
	if !ok || call.Arguments != `{"query":"weather"}` || !reflect.DeepEqual(blocks, []map[string]any{reasoning}) {
		t.Fatalf("expected persisted tool recovery reasoning to round-trip, got ok=%t call=%#v blocks=%#v", ok, call, blocks)
	}
}

func TestResponsesHistoryStoreSharesPersistedReasoningSnapshotForNormalToolCalls(t *testing.T) {
	// Given
	payload := strings.Repeat("persisted shared normal reasoning ", 8192)
	summary := strings.Repeat("persisted shared reasoning summary ", 4096)
	reasoning := map[string]any{"id": "rs-persisted-shared", "type": "reasoning", "encrypted_content": payload, "summary": []any{map[string]any{"type": "summary_text", "text": summary}}}
	indexPath := filepath.Join(t.TempDir(), "tool_call_recovery_index.json")
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)
	store.Save("openai", "resp-persisted-shared", []model.CanonicalMessage{
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "input_text", Text: "preserve the normal snapshot message index"}}},
		{
			Role:            "assistant",
			ReasoningBlocks: []map[string]any{reasoning},
			ToolCalls: []model.CanonicalToolCall{
				{ID: "call-persisted-shared-1", Type: "function", Name: "lookup", Arguments: `{}`},
				{ID: "call-persisted-shared-2", Type: "function", Name: "lookup", Arguments: `{}`},
			},
		},
	})

	// When
	snapshotKey := responsesHistoryKey("openai", "resp-persisted-shared")
	snapshot := store.entries[snapshotKey]
	before := snapshot
	before.Messages = cloneCanonicalMessages(snapshot.Messages)
	before.CompressedFields = cloneResponsesHistoryCompressedFields(snapshot.CompressedFields)
	view, viewOK := newResponsesHistoryReasoningSnapshotPersistenceView(snapshot, 1)
	materialized, materializedOK := newResponsesHistoryReasoningSnapshotFromConversationSnapshot(snapshot, 1)
	if !viewOK || !materializedOK || materialized == nil || len(view.CompressedFields) == 0 {
		t.Fatalf("expected normal snapshot reasoning persistence inputs, got view=%#v materialized=%#v", view, materialized)
	}
	viewData, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal read-only reasoning view: %v", err)
	}
	materializedData, err := json.Marshal(materialized)
	if err != nil {
		t.Fatalf("marshal materialized reasoning snapshot: %v", err)
	}
	if !bytes.Equal(viewData, materializedData) {
		t.Fatalf("expected read-only reasoning view to preserve persisted schema, got view=%s materialized=%s", viewData, materializedData)
	}
	var sourceField *responsesHistoryCompressedField
	for index := range snapshot.CompressedFields {
		field := &snapshot.CompressedFields[index]
		if field.Kind == responsesHistoryCompressedReasoningBlockString && field.MessageIndex == 1 && len(field.Data) > 0 && len(field.DynamicPath) > 0 {
			sourceField = field
			break
		}
	}
	if sourceField == nil || len(view.CompressedFields[0].Data) == 0 || len(view.CompressedFields[0].DynamicPath) == 0 || &view.CompressedFields[0].Data[0] != &sourceField.Data[0] || &view.CompressedFields[0].DynamicPath[0] != &sourceField.DynamicPath[0] {
		t.Fatalf("expected persistence view to share compressed data and dynamic path without copies, got source=%#v view=%#v", sourceField, view.CompressedFields)
	}
	var persistedBuffer bytes.Buffer
	store.mu.Lock()
	err = store.writeToolCallRecoveryIndex(&persistedBuffer)
	store.mu.Unlock()
	if err != nil {
		t.Fatalf("write persisted shared reasoning recovery index: %v", err)
	}
	if !reflect.DeepEqual(store.entries[snapshotKey], before) {
		t.Fatalf("expected persistence to leave the normal snapshot unchanged, got before=%#v after=%#v", before, store.entries[snapshotKey])
	}
	if err := os.WriteFile(indexPath, persistedBuffer.Bytes(), 0o600); err != nil {
		t.Fatalf("write persisted shared reasoning recovery index: %v", err)
	}
	persistedData := persistedBuffer.Bytes()
	var persisted responsesHistoryToolCallRecoveryIndexFile
	if err := json.Unmarshal(persistedData, &persisted); err != nil {
		t.Fatalf("decode persisted shared reasoning recovery index: %v", err)
	}
	first := persisted.ToolCalls[responsesHistoryToolCallKey("openai", "call-persisted-shared-1")]
	second := persisted.ToolCalls[responsesHistoryToolCallKey("openai", "call-persisted-shared-2")]

	// Then
	if len(persisted.ReasoningSnapshots) != 1 || first.ReasoningSnapshotKey == "" || first.ReasoningSnapshotKey != second.ReasoningSnapshotKey {
		t.Fatalf("expected normal tool calls to share one persisted reasoning snapshot, got first=%#v second=%#v snapshots=%#v", first, second, persisted.ReasoningSnapshots)
	}
	reloaded := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)
	for _, callID := range []string{"call-persisted-shared-1", "call-persisted-shared-2"} {
		_, blocks, ok := reloaded.LoadToolCall("openai", callID)
		if !ok || !reflect.DeepEqual(blocks, []map[string]any{reasoning}) {
			t.Fatalf("expected persisted shared reasoning to round-trip for %s, got ok=%t blocks=%#v", callID, ok, blocks)
		}
	}
}

func TestResponsesHistoryReasoningSnapshotFromConversationSnapshotPreservesCompressedBlocks(t *testing.T) {
	// Given
	payload := strings.Repeat("normal snapshot reasoning ", 8192)
	reasoning := map[string]any{"id": "rs-normal", "type": "reasoning", "encrypted_content": payload}
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")
	store.Save("openai", "resp-normal-snapshot", []model.CanonicalMessage{{
		Role:            "assistant",
		ReasoningBlocks: []map[string]any{reasoning},
		ToolCalls:       []model.CanonicalToolCall{{ID: "call-normal-snapshot", Type: "function", Name: "lookup", Arguments: `{}`}},
	}})
	snapshot := store.entries[responsesHistoryKey("openai", "resp-normal-snapshot")]

	// When
	reasoningSnapshot, ok := newResponsesHistoryReasoningSnapshotFromConversationSnapshot(snapshot, 0)
	loaded, loadedOK := loadResponsesHistoryReasoningSnapshot(reasoningSnapshot)

	// Then
	if !ok || reasoningSnapshot == nil || len(reasoningSnapshot.CompressedFields) == 0 || !loadedOK || !reflect.DeepEqual(loaded, []map[string]any{reasoning}) {
		t.Fatalf("expected normal snapshot reasoning to remain compressed and recoverable, got snapshot=%#v loaded=%#v ok=%t loaded_ok=%t", reasoningSnapshot, loaded, ok, loadedOK)
	}
}

func TestResponsesHistoryStoreLoadsCompressedOpaqueThinking(t *testing.T) {
	// Given
	payload := strings.Repeat("opaque thinking state ", 8192)
	reasoning := map[string]any{"id": "rs_upstream", "type": "reasoning", "encrypted_content": payload}
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")
	store.Save("openai", "resp-opaque-compressed", []model.CanonicalMessage{{Role: "assistant", ReasoningBlocks: []map[string]any{reasoning}}}, "opaque-scope")
	store.mu.Lock()
	store.indexOpaqueThinkingLocked("openai", responsesHistoryKey("openai", "resp-opaque-compressed"), []model.CanonicalMessage{{Role: "assistant", ReasoningBlocks: []map[string]any{reasoning}}}, "opaque-scope")
	store.mu.Unlock()
	public := responsesOpaqueThinkingPublicBlock(reasoning, 0)

	// When
	loaded, ok := store.LoadOpaqueThinking("openai", "opaque-scope", public)

	// Then
	if !ok || !reflect.DeepEqual(loaded, reasoning) {
		t.Fatalf("expected compressed opaque thinking to round-trip, got ok=%t block=%#v", ok, loaded)
	}
}

func TestResponsesHistoryStoreRejectsCorruptedCompressedDynamicRaw(t *testing.T) {
	// Given
	payload := strings.Repeat("dynamic raw payload ", 8192)
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")
	store.Save("openai", "resp-corrupt-dynamic-raw", []model.CanonicalMessage{{
		Role: "user",
		Parts: []model.CanonicalContentPart{{
			Type: "input_audio",
			Raw:  map[string]any{"input_audio": map[string]any{"data": payload}},
		}},
	}})
	key := responsesHistoryKey("openai", "resp-corrupt-dynamic-raw")
	snapshot := store.entries[key]
	corrupted := false
	for index := range snapshot.CompressedFields {
		if snapshot.CompressedFields[index].Kind != responsesHistoryCompressedPartRawString {
			continue
		}
		snapshot.CompressedFields[index].Data = []byte("corrupt")
		corrupted = true
		break
	}
	if !corrupted {
		t.Fatal("expected a dynamic raw compressed field to corrupt")
	}
	store.entries[key] = snapshot

	// When
	loaded := store.Load("openai", "resp-corrupt-dynamic-raw")

	// Then
	if loaded != nil {
		t.Fatalf("expected corrupted dynamic raw compression metadata to fail closed, got %#v", loaded)
	}
}

func TestResponsesHistoryStoreReleasesLargeDynamicRawPayloadAfterCompression(t *testing.T) {
	// Given
	const (
		historyEntries      = 32
		messagePayloadBytes = 1 << 20
	)
	logicalPayloadBytes := int64(historyEntries * messagePayloadBytes)
	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)

	// When
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")
	for index := range historyEntries {
		payload := fmt.Sprintf("%08d%s", index, strings.Repeat("x", messagePayloadBytes-8))
		store.Save("openai", fmt.Sprintf("resp-dynamic-%03d", index), []model.CanonicalMessage{{
			Role: "user",
			Parts: []model.CanonicalContentPart{{
				Type: "input_audio",
				Raw:  map[string]any{"input_audio": map[string]any{"data": payload, "format": "wav"}},
			}},
		}})
	}
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
	rootedHeapBytes := int64(postOperationRooted.HeapAlloc) - int64(baseline.HeapAlloc)
	rootReleaseBytes := int64(postOperationRooted.HeapAlloc) - int64(postRootReplacement.HeapAlloc)
	t.Logf("dynamic raw lifecycle bytes: logical_payload=%d rooted_after_gc=%d root_release=%d", logicalPayloadBytes, rootedHeapBytes, rootReleaseBytes)

	attributionFloor := logicalPayloadBytes * 3 / 4
	if rootedHeapBytes > attributionFloor || rootReleaseBytes > attributionFloor {
		t.Fatalf("compressed dynamic raw payload remained rooted by history: rooted=%d root_release=%d logical=%d", rootedHeapBytes, rootReleaseBytes, logicalPayloadBytes)
	}
}

func TestResponsesHistoryStoreReleasesLargeReasoningStateSharedWithToolRecovery(t *testing.T) {
	// Given
	const (
		historyEntries      = 16
		messagePayloadBytes = 1 << 20
	)
	logicalPayloadBytes := int64(historyEntries * messagePayloadBytes)
	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)

	// When
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")
	for index := range historyEntries {
		payload := fmt.Sprintf("%08d%s", index, strings.Repeat("r", messagePayloadBytes-8))
		callID := fmt.Sprintf("call-reasoning-%03d", index)
		store.Save("openai", fmt.Sprintf("resp-reasoning-%03d", index), []model.CanonicalMessage{{
			Role: "assistant",
			ReasoningBlocks: []map[string]any{{
				"id":                fmt.Sprintf("rs-%03d", index),
				"type":              "reasoning",
				"encrypted_content": payload,
			}},
			ToolCalls: []model.CanonicalToolCall{{ID: callID, Type: "function", Name: "lookup", Arguments: `{}`}},
		}})
	}
	lastCall, lastReasoning, loaded := store.LoadToolCall("openai", "call-reasoning-015")
	if !loaded || lastCall.ID != "call-reasoning-015" || len(lastReasoning) != 1 || len(stringValue(lastReasoning[0]["encrypted_content"])) != messagePayloadBytes {
		t.Fatalf("expected compressed reasoning tool recovery to round-trip, got loaded=%t call=%#v blocks=%#v", loaded, lastCall, lastReasoning)
	}
	lastReasoning = nil
	lastCall = model.CanonicalToolCall{}
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
	rootedHeapBytes := int64(postOperationRooted.HeapAlloc) - int64(baseline.HeapAlloc)
	rootReleaseBytes := int64(postOperationRooted.HeapAlloc) - int64(postRootReplacement.HeapAlloc)
	t.Logf("reasoning lifecycle bytes: logical_payload=%d rooted_after_gc=%d root_release=%d", logicalPayloadBytes, rootedHeapBytes, rootReleaseBytes)

	attributionFloor := logicalPayloadBytes * 3 / 4
	if rootedHeapBytes > attributionFloor || rootReleaseBytes > attributionFloor {
		t.Fatalf("compressed reasoning state remained rooted by history or tool recovery: rooted=%d root_release=%d logical=%d", rootedHeapBytes, rootReleaseBytes, logicalPayloadBytes)
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

func BenchmarkResponsesHistoryBuildAndSave(b *testing.B) {
	base := []model.CanonicalMessage{
		{Role: "developer", Parts: []model.CanonicalContentPart{{Type: "text", Text: "excluded developer prompt"}}},
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: strings.Repeat("representative history input ", 8192)}}},
		{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{
			ID:        "call-history-benchmark",
			Type:      "function",
			Name:      "lookup",
			Arguments: `{"query":"` + strings.Repeat("history benchmark ", 2048) + `"}`,
		}}},
	}
	assistant := []model.CanonicalMessage{{
		Role:             "assistant",
		ReasoningContent: strings.Repeat("representative reasoning ", 4096),
		Parts:            []model.CanonicalContentPart{{Type: "text", Text: strings.Repeat("representative history output ", 8192)}},
	}}
	logicalBytes := estimateCanonicalMessagesBytes(selectResponsesHistoryMessages(base, assistant))

	b.Run("legacy_build_then_save", func(b *testing.B) {
		store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")
		b.ReportAllocs()
		b.SetBytes(logicalBytes)
		b.ResetTimer()
		for range b.N {
			store.Save("openai", "resp-history-benchmark", buildResponsesHistorySnapshot(base, assistant))
		}
		b.StopTimer()
		benchmarkResponsesHistorySnapshot = store.entries[responsesHistoryKey("openai", "resp-history-benchmark")]
	})
	b.Run("select_then_save", func(b *testing.B) {
		store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")
		b.ReportAllocs()
		b.SetBytes(logicalBytes)
		b.ResetTimer()
		for range b.N {
			saveResponsesHistorySnapshot(store, "openai", "resp-history-benchmark", base, assistant)
		}
		b.StopTimer()
		benchmarkResponsesHistorySnapshot = store.entries[responsesHistoryKey("openai", "resp-history-benchmark")]
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
