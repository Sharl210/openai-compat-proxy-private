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

func TestResponsesHistoryRecoveryIndexPreservesRealReasoningStateWithSyntheticSummary(t *testing.T) {
	indexPath := filepath.Join(t.TempDir(), "tool_call_recovery_index.json")
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)

	store.Save("openai", "resp-reasoning-call", []model.CanonicalMessage{{
		Role: "assistant",
		ReasoningBlocks: []map[string]any{{
			"type":              "reasoning",
			"id":                "rs_real",
			"encrypted_content": "enc_real",
			"summary": []map[string]any{{
				"type": "summary_text",
				"text": "代理层占位",
			}},
		}},
		ToolCalls: []model.CanonicalToolCall{{
			ID:        "call-reasoning",
			Type:      "function",
			Name:      "process",
			Arguments: `{}`,
		}},
	}})

	reloaded := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)
	_, reasoning, ok := reloaded.LoadToolCall("openai", "call-reasoning")
	if !ok || len(reasoning) != 1 {
		t.Fatalf("expected recovery index to preserve real reasoning state, got ok=%t reasoning=%#v", ok, reasoning)
	}
	if reasoning[0]["id"] != "rs_real" || reasoning[0]["encrypted_content"] != "enc_real" {
		t.Fatalf("expected reasoning identity and encrypted state to survive recovery index, got %#v", reasoning[0])
	}
}

func TestResponsesHistoryRecoveryIndexRestoresOpaqueThinkingWithinScope(t *testing.T) {
	indexPath := filepath.Join(t.TempDir(), "tool_call_recovery_index.json")
	providerID := "anthropic"
	scope := "provider-and-upstream-scope"
	serverBlock := map[string]any{
		"type":              "reasoning",
		"id":                "rs_persisted",
		"encrypted_content": "opaque-persisted-state",
		"summary": []map[string]any{{
			"type": "summary_text",
			"text": "可信服务端状态",
		}},
	}
	publicBlock := responsesOpaqueThinkingPublicBlock(serverBlock, 0)
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)
	store.Save("anthropic", "resp-persisted-opaque", []model.CanonicalMessage{{
		Role:            "assistant",
		ReasoningBlocks: []map[string]any{serverBlock},
	}}, scope)

	reloaded := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)
	restored, ok := reloaded.LoadOpaqueThinking(providerID, scope, publicBlock)
	if !ok || restored["id"] != "rs_persisted" || restored["encrypted_content"] != "opaque-persisted-state" {
		t.Fatalf("expected opaque thinking state to survive restart, got ok=%t block=%#v", ok, restored)
	}
	if _, ok := reloaded.LoadOpaqueThinking(providerID, "different-scope", publicBlock); ok {
		t.Fatal("expected opaque thinking state to remain bound to its original scope")
	}
}

func TestResponsesHistoryRecoveryIndexRejectsOpaqueThinkingWithMismatchedFingerprint(t *testing.T) {
	indexPath := filepath.Join(t.TempDir(), "tool_call_recovery_index.json")
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)
	serverBlock := map[string]any{
		"type":              "reasoning",
		"id":                "rs_persisted",
		"encrypted_content": "opaque-persisted-state",
	}
	publicBlock := responsesOpaqueThinkingPublicBlock(serverBlock, 0)
	store.Save("anthropic", "resp-persisted-opaque", []model.CanonicalMessage{{
		Role:            "assistant",
		ReasoningBlocks: []map[string]any{serverBlock},
	}}, "scope")

	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read persisted recovery index: %v", err)
	}
	var persisted responsesHistoryToolCallRecoveryIndexFile
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("decode persisted recovery index: %v", err)
	}
	for key, entry := range persisted.OpaqueThinking {
		delete(persisted.OpaqueThinking, key)
		persisted.OpaqueThinking["anthropic::scope::not-the-block-fingerprint"] = entry
		break
	}
	data, err = json.Marshal(persisted)
	if err != nil {
		t.Fatalf("encode corrupted recovery index: %v", err)
	}
	if err := os.WriteFile(indexPath, data, 0o600); err != nil {
		t.Fatalf("write corrupted recovery index: %v", err)
	}

	reloaded := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)
	if _, ok := reloaded.LoadOpaqueThinking("anthropic", "scope", publicBlock); ok {
		t.Fatal("expected mismatched persisted opaque thinking fingerprint to be rejected")
	}
}
