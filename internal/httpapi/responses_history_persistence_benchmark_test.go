package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"openai-compat-proxy/internal/model"
)

var benchmarkResponsesHistoryReasoningPersistenceJSON []byte

func BenchmarkResponsesHistoryToolCallRecoveryIndexEncoding(b *testing.B) {
	b.Run("legacy_clone_and_indent", func(b *testing.B) {
		store := newBenchmarkResponsesHistoryStore(b)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if _, err := legacyToolCallRecoveryIndexJSON(store); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("streaming", func(b *testing.B) {
		store := newBenchmarkResponsesHistoryStore(b)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if err := store.writeToolCallRecoveryIndex(io.Discard); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("normal_snapshot_reasoning_materialized", func(b *testing.B) {
		snapshot := newBenchmarkResponsesHistoryNormalSnapshot(b)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			materialized, ok := newResponsesHistoryReasoningSnapshotFromConversationSnapshot(snapshot, 1)
			if !ok || materialized == nil {
				b.Fatal("materialize normal snapshot reasoning")
			}
			encoded, err := json.Marshal(materialized)
			if err != nil {
				b.Fatal(err)
			}
			benchmarkResponsesHistoryReasoningPersistenceJSON = encoded
		}
	})
	b.Run("normal_snapshot_reasoning_read_only_view", func(b *testing.B) {
		snapshot := newBenchmarkResponsesHistoryNormalSnapshot(b)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			view, ok := newResponsesHistoryReasoningSnapshotPersistenceView(snapshot, 1)
			if !ok {
				b.Fatal("view normal snapshot reasoning")
			}
			encoded, err := json.Marshal(view)
			if err != nil {
				b.Fatal(err)
			}
			benchmarkResponsesHistoryReasoningPersistenceJSON = encoded
		}
	})
	b.Run("normal_snapshot_recovery_index_view", func(b *testing.B) {
		store := newBenchmarkResponsesHistoryNormalSnapshotStore(b)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if err := store.writeToolCallRecoveryIndex(io.Discard); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func newBenchmarkResponsesHistoryStore(b *testing.B) *responsesHistoryStore {
	b.Helper()
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, b.TempDir()+"/tool_call_recovery_index.json")
	arguments := strings.Repeat("x", 10<<10)
	for index := range 384 {
		callID := fmt.Sprintf("call-%03d", index)
		key := responsesHistoryToolCallKey("openai", callID)
		store.order = append(store.order, fmt.Sprintf("openai::resp-%03d", index))
		store.toolCalls[key] = responsesHistoryToolCallEntry{
			SnapshotKey: fmt.Sprintf("openai::resp-%03d", index),
			Call: model.CanonicalToolCall{
				ID:        callID,
				Type:      "function",
				Name:      "inspect_image",
				Arguments: arguments,
			},
		}
	}
	return store
}

func newBenchmarkResponsesHistoryNormalSnapshot(b *testing.B) responsesConversationSnapshot {
	b.Helper()
	store := newBenchmarkResponsesHistoryNormalSnapshotStore(b)
	snapshot, ok := store.entries[responsesHistoryKey("openai", "resp-normal-reasoning")]
	if !ok || len(snapshot.CompressedFields) == 0 {
		b.Fatal("build compressed normal snapshot reasoning fixture")
	}
	return snapshot
}

func newBenchmarkResponsesHistoryNormalSnapshotStore(b *testing.B) *responsesHistoryStore {
	b.Helper()
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")
	payload := benchmarkResponsesHistoryNormalReasoningPayload(1 << 20)
	store.Save("openai", "resp-normal-reasoning", []model.CanonicalMessage{
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "input_text", Text: "persist normal snapshot reasoning"}}},
		{
			Role: "assistant",
			ReasoningBlocks: []map[string]any{{
				"id":                "rs-benchmark",
				"type":              "reasoning",
				"encrypted_content": payload,
			}},
			ToolCalls: []model.CanonicalToolCall{
				{ID: "call-normal-1", Type: "function", Name: "lookup", Arguments: `{}`},
				{ID: "call-normal-2", Type: "function", Name: "lookup", Arguments: `{}`},
			},
		},
	})
	return store
}

func benchmarkResponsesHistoryNormalReasoningPayload(size int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_"
	if size <= 0 {
		return ""
	}
	payload := make([]byte, size)
	state := uint32(1)
	for index := range payload {
		if index%2 == 0 {
			payload[index] = 'r'
			continue
		}
		state = state*1664525 + 1013904223
		payload[index] = alphabet[state>>26]
	}
	return string(payload)
}

func legacyToolCallRecoveryIndexJSON(store *responsesHistoryStore) ([]byte, error) {
	toolCalls := make(map[string]responsesHistoryToolCallEntry, len(store.toolCalls))
	for key, entry := range store.toolCalls {
		if key == "" || entry.Call.ID == "" || entry.Call.Name == "" {
			continue
		}
		toolCalls[key] = responsesHistoryToolCallEntry{SnapshotKey: entry.SnapshotKey, Call: entry.Call, ReasoningBlocks: cloneReasoningBlocks(entry.ReasoningBlocks)}
	}
	return json.MarshalIndent(responsesHistoryToolCallRecoveryIndexFile{Version: responsesHistoryToolCallRecoveryIndexVersion, Order: append([]string(nil), store.order...), ToolCalls: toolCalls}, "", "  ")
}
