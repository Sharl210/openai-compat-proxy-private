package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"openai-compat-proxy/internal/model"
)

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
