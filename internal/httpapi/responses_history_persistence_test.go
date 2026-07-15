package httpapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openai-compat-proxy/internal/model"
)

func TestResponsesHistoryPersistsCompressedLargeToolCallRecoveryArguments(t *testing.T) {
	indexPath := filepath.Join(t.TempDir(), "tool_call_recovery_index.json")
	arguments := `{"payload":"` + strings.Repeat("persisted tool argument ", 8192) + `"}`
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)

	store.Save("openai", "resp-persisted-call", []model.CanonicalMessage{{
		Role: "assistant",
		ToolCalls: []model.CanonicalToolCall{{
			ID:        "call-persisted",
			Type:      "function",
			Name:      "process",
			Arguments: arguments,
		}},
	}})

	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read persisted recovery index: %v", err)
	}
	var persisted responsesHistoryToolCallRecoveryIndexFile
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("decode persisted recovery index: %v", err)
	}
	persistedEntry, ok := persisted.ToolCalls[responsesHistoryToolCallKey("openai", "call-persisted")]
	if !ok || persistedEntry.Call.Arguments != arguments {
		t.Fatalf("expected persisted version-1 recovery index to retain raw arguments for older binaries, got ok=%t bytes=%d", ok, len(persistedEntry.Call.Arguments))
	}
	if len(persistedEntry.ArgumentsCompressed) == 0 || persistedEntry.ArgumentsOriginalSize != len(arguments) {
		t.Fatalf("expected persisted recovery index to contain compressed arguments, got compressed=%d original=%d", len(persistedEntry.ArgumentsCompressed), persistedEntry.ArgumentsOriginalSize)
	}

	reloaded := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)
	loaded, _, ok := reloaded.LoadToolCall("openai", "call-persisted")
	if !ok || loaded.Arguments != arguments {
		t.Fatalf("expected persisted large recovery argument to round-trip, got ok=%t bytes=%d", ok, len(loaded.Arguments))
	}
	entry, ok := reloaded.toolCalls[responsesHistoryToolCallKey("openai", "call-persisted")]
	if !ok || entry.Call.Arguments != "" {
		t.Fatalf("expected new loader to release compatibility raw arguments, got ok=%t bytes=%d", ok, len(entry.Call.Arguments))
	}
}
