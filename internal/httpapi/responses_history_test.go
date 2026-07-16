package httpapi

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openai-compat-proxy/internal/aggregate"
	"openai-compat-proxy/internal/model"
)

func TestResponsesHistoryEvictsOldestWhenLimitExceeded(t *testing.T) {
	store := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, maxSize: 2}

	store.Save("openai", "resp-1", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "one"}}}})
	store.Save("openai", "resp-2", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "two"}}}})
	store.Save("openai", "resp-3", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "three"}}}})

	if got := store.Load("openai", "resp-1"); got != nil {
		t.Fatalf("expected oldest entry to be evicted, got %#v", got)
	}
	if got := store.Load("openai", "resp-2"); len(got) != 1 {
		t.Fatalf("expected resp-2 to remain, got %#v", got)
	}
	if got := store.Load("openai", "resp-3"); len(got) != 1 {
		t.Fatalf("expected resp-3 to remain, got %#v", got)
	}
	if got := store.LoadAny("resp-1"); got != nil {
		t.Fatalf("expected evicted response id to be removed from LoadAny index, got %#v", got)
	}
}

func TestResponsesHistoryEvictsOldestWhenByteLimitExceeded(t *testing.T) {
	// Given
	store := &responsesHistoryStore{
		entries:  map[string]responsesConversationSnapshot{},
		maxSize:  512,
		maxBytes: 180,
	}
	largeText := strings.Repeat("x", 128)

	// When
	store.Save("openai", "resp-1", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: largeText}}}})
	store.Save("openai", "resp-2", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: largeText}}}})

	// Then
	if got := store.Load("openai", "resp-1"); got != nil {
		t.Fatalf("expected oldest byte-heavy entry to be evicted, got %#v", got)
	}
	if got := store.Load("openai", "resp-2"); len(got) != 1 {
		t.Fatalf("expected newest entry to remain, got %#v", got)
	}
	if store.retainedBytes > store.maxBytes {
		t.Fatalf("expected retained bytes within budget, got %d > %d", store.retainedBytes, store.maxBytes)
	}
}

func TestResponsesHistorySkipsSnapshotLargerThanByteLimit(t *testing.T) {
	// Given
	store := &responsesHistoryStore{
		entries:  map[string]responsesConversationSnapshot{},
		maxSize:  512,
		maxBytes: 64,
	}
	message := model.CanonicalMessage{
		Role: "user",
		Parts: []model.CanonicalContentPart{{
			Type:     "image_url",
			ImageURL: "data:image/png;base64," + strings.Repeat("A", 256),
		}},
	}

	// When
	store.Save("openai", "resp-large", []model.CanonicalMessage{message})

	// Then
	if got := store.Load("openai", "resp-large"); got != nil {
		t.Fatalf("expected oversized snapshot not to remain cached, got %#v", got)
	}
	if store.retainedBytes != 0 {
		t.Fatalf("expected no retained bytes for oversized snapshot, got %d", store.retainedBytes)
	}
}

func TestResponsesHistoryKeepsToolRecoveryWhenSnapshotExceedsByteLimit(t *testing.T) {
	// Given
	store := &responsesHistoryStore{
		entries:   map[string]responsesConversationSnapshot{},
		toolCalls: map[string]responsesHistoryToolCallEntry{},
		maxSize:   512,
		maxBytes:  512,
	}
	messages := []model.CanonicalMessage{
		{
			Role: "user",
			Parts: []model.CanonicalContentPart{{
				Type:     "image_url",
				ImageURL: "data:image/png;base64," + strings.Repeat("A", 1024),
			}},
		},
		{
			Role:      "assistant",
			ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "inspect_image", Arguments: `{}`}},
		},
	}

	// When
	store.Save("openai", "resp-large", messages)

	// Then
	if got := store.Load("openai", "resp-large"); got != nil {
		t.Fatalf("expected oversized full snapshot not to remain cached, got %#v", got)
	}
	call, _, ok := store.LoadToolCall("openai", "call_1")
	if !ok || call.Name != "inspect_image" {
		t.Fatalf("expected lightweight tool recovery metadata to remain, got ok=%t call=%#v", ok, call)
	}
	if store.retainedBytes > store.maxBytes {
		t.Fatalf("expected lightweight recovery metadata within budget, got %d > %d", store.retainedBytes, store.maxBytes)
	}
}

func TestNewResponsesHistoryStoreUses256MiBByteBudget(t *testing.T) {
	// Given
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")

	// When
	configuredBudget := store.maxBytes

	// Then
	if configuredBudget != 256<<20 {
		t.Fatalf("expected 256 MiB default history budget, got %d", configuredBudget)
	}
}

func TestResponsesHistorySaveWritesCompactToolRecoveryIndex(t *testing.T) {
	// Given
	indexPath := filepath.Join(t.TempDir(), "tool_call_recovery_index.json")
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)
	messages := []model.CanonicalMessage{{
		Role: "assistant",
		ToolCalls: []model.CanonicalToolCall{{
			ID:        "call-1",
			Type:      "function",
			Name:      "inspect_image",
			Arguments: `{"path":"/tmp/image.png"}`,
		}},
	}}

	// When
	store.Save("openai", "resp-1", messages)

	// Then
	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read compact recovery index: %v", err)
	}
	if strings.Contains(string(data), "\n") {
		t.Fatalf("expected compact recovery index, got %q", data)
	}

	reloaded := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)
	call, _, ok := reloaded.LoadToolCall("openai", "call-1")
	if !ok || call.Name != "inspect_image" {
		t.Fatalf("expected compact recovery index to reload, got ok=%t call=%#v", ok, call)
	}
}

func TestAtomicWriteResponsesHistoryFilePreservesExistingIndexWhenWriterFails(t *testing.T) {
	// Given
	indexPath := filepath.Join(t.TempDir(), "tool_call_recovery_index.json")
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)
	store.Save("openai", "resp-1", []model.CanonicalMessage{{
		Role: "assistant",
		ToolCalls: []model.CanonicalToolCall{{
			ID:        "call-1",
			Type:      "function",
			Name:      "inspect_image",
			Arguments: `{}`,
		}},
	}})
	failure := errors.New("injected write failure")

	// When
	err := atomicWriteResponsesHistoryFile(indexPath, func(writer io.Writer) error {
		if _, writeErr := io.WriteString(writer, `{"version":`); writeErr != nil {
			return writeErr
		}
		return failure
	})

	// Then
	if !errors.Is(err, failure) {
		t.Fatalf("expected injected write failure, got %v", err)
	}
	if _, err := os.Stat(indexPath + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("expected temporary index cleanup, got err=%v", err)
	}
	reloaded := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, indexPath)
	call, _, ok := reloaded.LoadToolCall("openai", "call-1")
	if !ok || call.Name != "inspect_image" {
		t.Fatalf("expected prior index to remain loadable, got ok=%t call=%#v", ok, call)
	}
}

func TestResponsesHistoryDropsOversizedToolRecoveryIndex(t *testing.T) {
	// Given
	indexPath := filepath.Join(t.TempDir(), "tool_call_recovery_index.json")
	store := &responsesHistoryStore{
		entries:                   map[string]responsesConversationSnapshot{},
		byResponseID:              map[string]string{},
		toolCalls:                 map[string]responsesHistoryToolCallEntry{},
		maxSize:                   512,
		maxBytes:                  64,
		toolCallRecoveryIndexPath: indexPath,
	}
	messages := []model.CanonicalMessage{{
		Role: "assistant",
		ToolCalls: []model.CanonicalToolCall{{
			ID:        "call-large",
			Type:      "function",
			Name:      "inspect_image",
			Arguments: strings.Repeat("x", 128),
		}},
	}}

	// When
	store.Save("openai", "resp-large-tool", messages)

	// Then
	if _, _, ok := store.LoadToolCall("openai", "call-large"); ok {
		t.Fatal("expected oversized tool recovery entry to be dropped")
	}
	if _, err := os.Stat(indexPath); !os.IsNotExist(err) {
		t.Fatalf("expected no persisted oversized tool recovery index, got err=%v", err)
	}
}

func TestResponsesHistorySaveSameKeyReplacesWithoutGrowingOrder(t *testing.T) {
	store := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, maxSize: 2}

	store.Save("openai", "resp-1", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "one"}}}})
	store.Save("openai", "resp-1", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "updated"}}}})
	store.Save("openai", "resp-2", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "two"}}}})
	store.Save("openai", "resp-3", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "three"}}}})

	if got := store.Load("openai", "resp-1"); len(got) != 0 {
		t.Fatalf("expected resp-1 to be evicted after capacity exceeded, got %#v", got)
	}
	if got := store.Load("openai", "resp-2"); len(got) != 1 {
		t.Fatalf("expected resp-2 to remain, got %#v", got)
	}
	if got := store.Load("openai", "resp-3"); len(got) != 1 {
		t.Fatalf("expected resp-3 to remain, got %#v", got)
	}
}

func TestResponsesHistorySaveSameKeyReplacesRetainedBytes(t *testing.T) {
	// Given
	store := &responsesHistoryStore{
		entries:  map[string]responsesConversationSnapshot{},
		maxSize:  2,
		maxBytes: 1024,
	}
	first := []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: strings.Repeat("a", 128)}}}}
	second := []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "short"}}}}

	// When
	store.Save("openai", "resp-1", first)
	store.Save("openai", "resp-1", second)

	// Then
	want := estimateCanonicalMessagesBytes(second)
	if store.retainedBytes != want {
		t.Fatalf("expected replacement to retain %d bytes, got %d", want, store.retainedBytes)
	}
}

func TestResponsesHistoryLoadReturnsClone(t *testing.T) {
	store := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, maxSize: 2}
	store.Save("openai", "resp-1", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "one"}}}})

	loaded := store.Load("openai", "resp-1")
	if len(loaded) != 1 {
		t.Fatalf("expected one loaded message, got %#v", loaded)
	}
	loaded[0].Parts[0].Text = "mutated"

	reloaded := store.Load("openai", "resp-1")
	if reloaded[0].Parts[0].Text != "one" {
		t.Fatalf("expected stored history to stay immutable, got %#v", reloaded)
	}
}

func TestResponsesHistoryLoadToolCallByCallIDReturnsClone(t *testing.T) {
	store := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, maxSize: 2}
	store.Save("anthropic", "resp-1", []model.CanonicalMessage{{
		Role:      "assistant",
		ToolCalls: []model.CanonicalToolCall{{ID: "call_1", ResponseItemID: "fc_1", Type: "function", Name: "run_in_terminal", Arguments: `{"cmd":"pwd"}`}},
	}})

	loaded, _, ok := store.LoadToolCall("anthropic", "call_1")
	if !ok {
		t.Fatal("expected tool call to be indexed by call_id")
	}
	if loaded.ID != "call_1" || loaded.ResponseItemID != "fc_1" || loaded.Name != "run_in_terminal" || loaded.Arguments != `{"cmd":"pwd"}` {
		t.Fatalf("expected stored tool call metadata, got %#v", loaded)
	}

	loaded.Name = "mutated"
	loaded.ResponseItemID = "mutated"
	reloaded, _, ok := store.LoadToolCall("anthropic", "call_1")
	if !ok || reloaded.Name != "run_in_terminal" || reloaded.ResponseItemID != "fc_1" {
		t.Fatalf("expected tool call lookup to return a clone, got ok=%t call=%#v", ok, reloaded)
	}
}

func TestAssistantHistoryMessagesFromResultPreservesReasoningOutputItemState(t *testing.T) {
	messages := assistantHistoryMessagesFromResult(aggregate.Result{
		Reasoning: map[string]any{"summary": "thinking"},
		ResponseOutputItems: []map[string]any{{
			"id":                "rs_123",
			"type":              "reasoning",
			"encrypted_content": "enc_123",
			"summary": []any{map[string]any{
				"type": "summary_text",
				"text": "thinking",
			}},
		}},
	})

	if len(messages) != 1 || len(messages[0].ReasoningBlocks) != 1 {
		t.Fatalf("expected one preserved reasoning block, got %#v", messages)
	}
	block := messages[0].ReasoningBlocks[0]
	if block["id"] != "rs_123" || block["encrypted_content"] != "enc_123" {
		t.Fatalf("expected reasoning identity and encrypted state to survive history conversion, got %#v", block)
	}
}

func TestAssistantHistoryMessagesFromResultUsesOrderedReasoningOutputItems(t *testing.T) {
	messages := assistantHistoryMessagesFromResult(aggregate.Result{
		ReasoningBlocks: []map[string]any{{"id": "rs_fallback", "type": "reasoning", "encrypted_content": "fallback"}},
		ResponseOutputItems: []map[string]any{
			{"id": "rs_first", "type": "reasoning", "encrypted_content": "old"},
			{"id": "rs_second", "type": "reasoning", "encrypted_content": "second"},
			{"id": "rs_proxy", "type": "reasoning", "summary": []any{}},
			{"id": "rs_first", "type": "reasoning", "encrypted_content": "latest"},
		},
	})

	if len(messages) != 1 || len(messages[0].ReasoningBlocks) != 2 {
		t.Fatalf("expected two ordered reasoning blocks, got %#v", messages)
	}
	blocks := messages[0].ReasoningBlocks
	if blocks[0]["id"] != "rs_first" || blocks[0]["encrypted_content"] != "latest" {
		t.Fatalf("expected first reasoning block to keep its position and latest state, got %#v", blocks)
	}
	if blocks[1]["id"] != "rs_second" || blocks[1]["encrypted_content"] != "second" {
		t.Fatalf("expected second reasoning block to preserve order and state, got %#v", blocks)
	}
}

func TestAssistantHistoryMessagesFromResultKeepsRealOutputItemWithPlaceholderText(t *testing.T) {
	messages := assistantHistoryMessagesFromResult(aggregate.Result{
		ResponseOutputItems: []map[string]any{{
			"id":                "rs_real",
			"type":              "reasoning",
			"encrypted_content": "enc_real",
			"summary": []any{map[string]any{
				"type": "summary_text",
				"text": "代理层占位",
			}},
		}},
	})

	if len(messages) != 1 || len(messages[0].ReasoningBlocks) != 1 {
		t.Fatalf("expected real output reasoning item to survive placeholder text, got %#v", messages)
	}
	block := messages[0].ReasoningBlocks[0]
	if block["id"] != "rs_real" || block["encrypted_content"] != "enc_real" {
		t.Fatalf("expected real output reasoning state to survive, got %#v", block)
	}
}

func TestResponsesHistoryPersistsRealOutputReasoningWithPlaceholderText(t *testing.T) {
	base := []model.CanonicalMessage{{
		Role:  "user",
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}},
	}}
	assistant := assistantHistoryMessagesFromResult(aggregate.Result{
		ResponseOutputItems: []map[string]any{{
			"id":                "rs_real",
			"type":              "reasoning",
			"encrypted_content": "enc_real",
			"summary": []any{map[string]any{
				"type": "summary_text",
				"text": "代理层占位",
			}},
		}},
	})
	store := newResponsesHistoryStore(2, "")

	store.Save("provider", "resp_1", buildResponsesHistorySnapshot(base, assistant))
	replayed := store.Load("provider", "resp_1")

	if len(replayed) != 2 || len(replayed[1].ReasoningBlocks) != 1 {
		t.Fatalf("expected real output reasoning to survive history persistence, got %#v", replayed)
	}
	block := replayed[1].ReasoningBlocks[0]
	if block["id"] != "rs_real" || block["encrypted_content"] != "enc_real" {
		t.Fatalf("expected persisted real output reasoning state, got %#v", block)
	}
}

func TestResponsesHistoryPersistsRealFallbackReasoningWithPlaceholderText(t *testing.T) {
	base := []model.CanonicalMessage{{
		Role:  "user",
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}},
	}}
	assistant := assistantHistoryMessagesFromResult(aggregate.Result{
		ReasoningBlocks: []map[string]any{{
			"id":                "rs_fallback",
			"type":              "reasoning",
			"encrypted_content": "enc_fallback",
			"summary": []any{map[string]any{
				"type": "summary_text",
				"text": "代理层占位",
			}},
		}},
	})
	store := newResponsesHistoryStore(2, "")

	store.Save("provider", "resp_1", buildResponsesHistorySnapshot(base, assistant))
	replayed := store.Load("provider", "resp_1")

	if len(replayed) != 2 || len(replayed[1].ReasoningBlocks) != 1 {
		t.Fatalf("expected real fallback reasoning to survive history persistence, got %#v", replayed)
	}
	block := replayed[1].ReasoningBlocks[0]
	if block["id"] != "rs_fallback" || block["encrypted_content"] != "enc_fallback" {
		t.Fatalf("expected persisted real fallback reasoning state, got %#v", block)
	}
}

func TestRecoverResponseItemReferencesForMessagesUsesScopedToolCallIndex(t *testing.T) {
	store := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, byResponseID: map[string]string{}, toolCalls: map[string]responsesHistoryToolCallEntry{}, maxSize: 2}

	store.Save("codex-my", "resp-1", []model.CanonicalMessage{{
		Role:      "assistant",
		ToolCalls: []model.CanonicalToolCall{{ID: "call_1", ResponseItemID: "fc_1", Type: "function", Name: "bash", Arguments: `{}`}},
	}}, "scope-a")

	references := recoverResponseItemReferencesForMessages(store, []model.CanonicalMessage{{
		Role:       "tool",
		ToolCallID: "call_1",
		Parts:      []model.CanonicalContentPart{{Type: "text", Text: `{"ok":true}`}},
	}}, "codex-my", "scope-a")

	if references["call_1"] != "fc_1" {
		t.Fatalf("expected call_1 to resolve item_reference fc_1, got %#v", references)
	}
	if references["missing"] != "" {
		t.Fatalf("expected only known calls to be mapped, got %#v", references)
	}
}

func TestResponsesHistoryEvictsToolCallIndexWithSnapshot(t *testing.T) {
	store := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, maxSize: 1}
	store.Save("anthropic", "resp-1", []model.CanonicalMessage{{
		Role:      "assistant",
		ToolCalls: []model.CanonicalToolCall{{ID: "call_old", Type: "function", Name: "old", Arguments: `{}`}},
	}})
	store.Save("anthropic", "resp-2", []model.CanonicalMessage{{
		Role:      "assistant",
		ToolCalls: []model.CanonicalToolCall{{ID: "call_new", Type: "function", Name: "new", Arguments: `{}`}},
	}})

	if _, _, ok := store.LoadToolCall("anthropic", "call_old"); ok {
		t.Fatal("expected evicted snapshot tool call to be removed from index")
	}
	if call, _, ok := store.LoadToolCall("anthropic", "call_new"); !ok || call.Name != "new" {
		t.Fatalf("expected newest tool call to remain indexed, got ok=%t call=%#v", ok, call)
	}
}

func TestResponsesHistoryToolCallLookupIsProviderScoped(t *testing.T) {
	store := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, maxSize: 3}
	store.Save("anthropic-a", "resp-1", []model.CanonicalMessage{{
		Role:      "assistant",
		ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "tool_a", Arguments: `{}`}},
	}})
	store.Save("anthropic-b", "resp-1", []model.CanonicalMessage{{
		Role:      "assistant",
		ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "tool_b", Arguments: `{}`}},
	}})

	callA, _, okA := store.LoadToolCall("anthropic-a", "call_1")
	callB, _, okB := store.LoadToolCall("anthropic-b", "call_1")
	if !okA || callA.Name != "tool_a" {
		t.Fatalf("expected provider A scoped tool call, got ok=%t call=%#v", okA, callA)
	}
	if !okB || callB.Name != "tool_b" {
		t.Fatalf("expected provider B scoped tool call, got ok=%t call=%#v", okB, callB)
	}
}

func TestResponsesHistoryToolCallLookupIsConversationScoped(t *testing.T) {
	store := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, maxSize: 3}
	store.Save("anthropic", "resp-a", []model.CanonicalMessage{{
		Role:      "assistant",
		ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "tool_a", Arguments: `{}`}},
	}}, "scope-a")
	store.Save("anthropic", "resp-b", []model.CanonicalMessage{{
		Role:      "assistant",
		ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "tool_b", Arguments: `{}`}},
	}}, "scope-b")

	callA, _, okA := store.LoadToolCall("anthropic", "call_1", "scope-a")
	callB, _, okB := store.LoadToolCall("anthropic", "call_1", "scope-b")
	if !okA || callA.Name != "tool_a" {
		t.Fatalf("expected scope A tool call, got ok=%t call=%#v", okA, callA)
	}
	if !okB || callB.Name != "tool_b" {
		t.Fatalf("expected scope B tool call, got ok=%t call=%#v", okB, callB)
	}
}

func TestResponsesHistoryIndexesRecoveredToolCallFromStoredToolMessage(t *testing.T) {
	store := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, maxSize: 2}
	recovered := model.CanonicalToolCall{ID: "call_recovered", Type: "function", Name: "run_in_terminal", Arguments: `{"cmd":"pwd"}`}
	store.Save("anthropic", "resp-1", []model.CanonicalMessage{{
		Role:              "tool",
		ToolCallID:        "call_recovered",
		RecoveredToolCall: &recovered,
		Parts:             []model.CanonicalContentPart{{Type: "text", Text: `{"ok":true}`}},
	}})

	loaded, _, ok := store.LoadToolCall("anthropic", "call_recovered")
	if !ok {
		t.Fatal("expected recovered tool call to be indexed from stored tool message")
	}
	if loaded.Name != "run_in_terminal" || loaded.Arguments != `{"cmd":"pwd"}` {
		t.Fatalf("expected recovered tool call metadata, got %#v", loaded)
	}
}

func TestResponsesHistoryReturnsReasoningBlocksWithToolCall(t *testing.T) {
	store := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, maxSize: 2}
	store.Save("anthropic", "resp-1", []model.CanonicalMessage{{
		Role: "assistant",
		ReasoningBlocks: []map[string]any{{
			"type":      "thinking",
			"thinking":  "need tool result",
			"signature": "sig_1",
		}},
		ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "read_file", Arguments: `{"filePath":"/tmp/a"}`}},
	}})

	_, reasoningBlocks, ok := store.LoadToolCall("anthropic", "call_1")
	if !ok {
		t.Fatal("expected tool call to be indexed")
	}
	if len(reasoningBlocks) != 1 || reasoningBlocks[0]["thinking"] != "need tool result" || reasoningBlocks[0]["signature"] != "sig_1" {
		t.Fatalf("expected reasoning blocks to be returned with tool call, got %#v", reasoningBlocks)
	}
}

func TestResponsesHistoryReloadsPersistentToolCallRecoveryIndex(t *testing.T) {
	indexPath := filepath.Join(t.TempDir(), "tool_call_recovery_index.json")
	store := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, byResponseID: map[string]string{}, toolCalls: map[string]responsesHistoryToolCallEntry{}, maxSize: 2, toolCallRecoveryIndexPath: indexPath}
	store.Save("anthropic", "resp-1", []model.CanonicalMessage{
		{
			Role:  "user",
			Parts: []model.CanonicalContentPart{{Type: "text", Text: "do not persist this prompt"}},
		},
		{
			Role: "assistant",
			ReasoningBlocks: []map[string]any{{
				"type":      "thinking",
				"thinking":  "need three tool results",
				"signature": "sig_1",
			}},
			ToolCalls: []model.CanonicalToolCall{
				{ID: "call_1", Type: "function", Name: "read_file", Arguments: `{"filePath":"/tmp/a"}`},
				{ID: "call_2", Type: "function", Name: "glob", Arguments: `{"pattern":"*.go"}`},
				{ID: "call_3", Type: "function", Name: "grep", Arguments: `{"pattern":"TODO"}`},
			},
		},
	}, "scope-a")

	reloaded := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, byResponseID: map[string]string{}, toolCalls: map[string]responsesHistoryToolCallEntry{}, maxSize: 2, toolCallRecoveryIndexPath: indexPath}
	if got := reloaded.Load("anthropic", "resp-1"); got != nil {
		t.Fatalf("expected full previous-response snapshot to stay memory-only, got %#v", got)
	}

	for _, want := range []struct {
		id   string
		name string
	}{
		{id: "call_1", name: "read_file"},
		{id: "call_2", name: "glob"},
		{id: "call_3", name: "grep"},
	} {
		call, reasoningBlocks, ok := reloaded.LoadToolCall("anthropic", want.id, "scope-a")
		if !ok {
			t.Fatalf("expected persisted tool call %s to reload", want.id)
		}
		if call.Name != want.name || call.ID != want.id {
			t.Fatalf("expected persisted tool call metadata for %s, got %#v", want.id, call)
		}
		if len(reasoningBlocks) != 1 || reasoningBlocks[0]["thinking"] != "need three tool results" || reasoningBlocks[0]["signature"] != "sig_1" {
			t.Fatalf("expected persisted reasoning blocks for %s, got %#v", want.id, reasoningBlocks)
		}
	}

	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read persisted recovery index: %v", err)
	}
	if strings.Contains(string(data), "do not persist this prompt") {
		t.Fatalf("expected recovery index not to persist prompt text, got %s", data)
	}
}

func TestResponsesHistorySavePreservesPreviouslyPersistedToolCallRecoveryIndex(t *testing.T) {
	indexPath := filepath.Join(t.TempDir(), "tool_call_recovery_index.json")
	oldProcess := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, byResponseID: map[string]string{}, toolCalls: map[string]responsesHistoryToolCallEntry{}, maxSize: 2, toolCallRecoveryIndexPath: indexPath}
	oldProcess.Save("anthropic", "resp-old", []model.CanonicalMessage{{
		Role:      "assistant",
		ToolCalls: []model.CanonicalToolCall{{ID: "call_old", Type: "function", Name: "read_file", Arguments: `{"filePath":"/tmp/old"}`}},
	}}, "scope-a")

	newProcess := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, byResponseID: map[string]string{}, toolCalls: map[string]responsesHistoryToolCallEntry{}, maxSize: 2, toolCallRecoveryIndexPath: indexPath}
	newProcess.Save("anthropic", "resp-new", []model.CanonicalMessage{{
		Role:      "assistant",
		ToolCalls: []model.CanonicalToolCall{{ID: "call_new", Type: "function", Name: "glob", Arguments: `{"pattern":"*.go"}`}},
	}}, "scope-a")

	if call, _, ok := newProcess.LoadToolCall("anthropic", "call_old", "scope-a"); !ok || call.Name != "read_file" {
		t.Fatalf("expected first save in new process to preserve old persisted tool call, got ok=%t call=%#v", ok, call)
	}
	if call, _, ok := newProcess.LoadToolCall("anthropic", "call_new", "scope-a"); !ok || call.Name != "glob" {
		t.Fatalf("expected new tool call to remain indexed, got ok=%t call=%#v", ok, call)
	}
}

func TestResponsesHistoryReloadedPersistentToolCallRecoveryIndexKeepsEvictionBound(t *testing.T) {
	indexPath := filepath.Join(t.TempDir(), "tool_call_recovery_index.json")
	oldProcess := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, byResponseID: map[string]string{}, toolCalls: map[string]responsesHistoryToolCallEntry{}, maxSize: 2, toolCallRecoveryIndexPath: indexPath}
	oldProcess.Save("anthropic", "resp-1", []model.CanonicalMessage{{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "one", Arguments: `{}`}}}}, "scope-a")
	oldProcess.Save("anthropic", "resp-2", []model.CanonicalMessage{{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_2", Type: "function", Name: "two", Arguments: `{}`}}}}, "scope-a")

	newProcess := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, byResponseID: map[string]string{}, toolCalls: map[string]responsesHistoryToolCallEntry{}, maxSize: 2, toolCallRecoveryIndexPath: indexPath}
	newProcess.Save("anthropic", "resp-3", []model.CanonicalMessage{{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_3", Type: "function", Name: "three", Arguments: `{}`}}}}, "scope-a")

	if _, _, ok := newProcess.LoadToolCall("anthropic", "call_1", "scope-a"); ok {
		t.Fatal("expected oldest persisted tool call to be evicted after reload")
	}
	if call, _, ok := newProcess.LoadToolCall("anthropic", "call_2", "scope-a"); !ok || call.Name != "two" {
		t.Fatalf("expected second persisted tool call to remain, got ok=%t call=%#v", ok, call)
	}
	if call, _, ok := newProcess.LoadToolCall("anthropic", "call_3", "scope-a"); !ok || call.Name != "three" {
		t.Fatalf("expected newest tool call to remain, got ok=%t call=%#v", ok, call)
	}
}

func TestResponsesHistoryReloadedRecoveryIndexCountsTowardsByteBudget(t *testing.T) {
	// Given
	indexPath := filepath.Join(t.TempDir(), "tool_call_recovery_index.json")
	oldMessages := []model.CanonicalMessage{
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "image_url", ImageURL: "data:image/png;base64," + strings.Repeat("A", 256)}}},
		{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_old", Type: "function", Name: "inspect_image", Arguments: `{}`}}},
	}
	newMessages := []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: strings.Repeat("x", 96)}}}}
	maxBytes := estimateToolRecoveryBytes(oldMessages) + estimateCanonicalMessagesBytes(newMessages) - 1
	oldProcess := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, byResponseID: map[string]string{}, toolCalls: map[string]responsesHistoryToolCallEntry{}, maxSize: 2, maxBytes: maxBytes, toolCallRecoveryIndexPath: indexPath}
	oldProcess.Save("anthropic", "resp-old", oldMessages, "scope-a")

	// When
	newProcess := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, byResponseID: map[string]string{}, toolCalls: map[string]responsesHistoryToolCallEntry{}, maxSize: 2, maxBytes: maxBytes, toolCallRecoveryIndexPath: indexPath}
	newProcess.Save("anthropic", "resp-new", newMessages, "scope-a")
	reloaded := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, byResponseID: map[string]string{}, toolCalls: map[string]responsesHistoryToolCallEntry{}, maxSize: 2, maxBytes: maxBytes, toolCallRecoveryIndexPath: indexPath}

	// Then
	if _, _, ok := reloaded.LoadToolCall("anthropic", "call_old", "scope-a"); ok {
		t.Fatal("expected persisted recovery entry to be evicted when its bytes plus the new snapshot exceed the budget")
	}
	if got := reloaded.Load("anthropic", "resp-new"); got != nil {
		t.Fatalf("expected full snapshots to remain memory-only after restart, got %#v", got)
	}
}

func TestResponsesHistoryReplacingOversizedSnapshotReplacesPersistedToolRecovery(t *testing.T) {
	// Given
	indexPath := filepath.Join(t.TempDir(), "tool_call_recovery_index.json")
	oldProcess := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, byResponseID: map[string]string{}, toolCalls: map[string]responsesHistoryToolCallEntry{}, maxSize: 2, maxBytes: 256, toolCallRecoveryIndexPath: indexPath}
	oldProcess.Save("anthropic", "resp-1", []model.CanonicalMessage{{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_old", Type: "function", Name: "read_file", Arguments: `{}`}}}}, "scope-a")
	oversizedReplacement := []model.CanonicalMessage{
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "image_url", ImageURL: "data:image/png;base64," + strings.Repeat("A", 512)}}},
		{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_new", Type: "function", Name: "inspect_image", Arguments: `{}`}}},
	}

	// When
	newProcess := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, byResponseID: map[string]string{}, toolCalls: map[string]responsesHistoryToolCallEntry{}, maxSize: 2, maxBytes: 256, toolCallRecoveryIndexPath: indexPath}
	newProcess.Save("anthropic", "resp-1", oversizedReplacement, "scope-a")
	reloaded := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, byResponseID: map[string]string{}, toolCalls: map[string]responsesHistoryToolCallEntry{}, maxSize: 2, maxBytes: 256, toolCallRecoveryIndexPath: indexPath}

	// Then
	if _, _, ok := reloaded.LoadToolCall("anthropic", "call_old", "scope-a"); ok {
		t.Fatal("expected replaced tool recovery entry to stay deleted after restart")
	}
	if call, _, ok := reloaded.LoadToolCall("anthropic", "call_new", "scope-a"); !ok || call.Name != "inspect_image" {
		t.Fatalf("expected replacement recovery entry to persist, got ok=%t call=%#v", ok, call)
	}
}

func TestResponsesHistoryReplacingWithToolFreeOversizedSnapshotDeletesPersistedRecovery(t *testing.T) {
	// Given
	indexPath := filepath.Join(t.TempDir(), "tool_call_recovery_index.json")
	oldProcess := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, byResponseID: map[string]string{}, toolCalls: map[string]responsesHistoryToolCallEntry{}, maxSize: 2, maxBytes: 256, toolCallRecoveryIndexPath: indexPath}
	oldProcess.Save("anthropic", "resp-1", []model.CanonicalMessage{{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_old", Type: "function", Name: "read_file", Arguments: `{}`}}}}, "scope-a")
	toolFreeOversizedReplacement := []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "image_url", ImageURL: "data:image/png;base64," + strings.Repeat("A", 512)}}}}

	// When
	newProcess := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, byResponseID: map[string]string{}, toolCalls: map[string]responsesHistoryToolCallEntry{}, maxSize: 2, maxBytes: 256, toolCallRecoveryIndexPath: indexPath}
	newProcess.Save("anthropic", "resp-1", toolFreeOversizedReplacement, "scope-a")
	reloaded := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, byResponseID: map[string]string{}, toolCalls: map[string]responsesHistoryToolCallEntry{}, maxSize: 2, maxBytes: 256, toolCallRecoveryIndexPath: indexPath}

	// Then
	if _, _, ok := reloaded.LoadToolCall("anthropic", "call_old", "scope-a"); ok {
		t.Fatal("expected tool-free oversized replacement to delete persisted recovery data")
	}
}

func TestResponsesHistoryLoadAnyReturnsLatestProviderSnapshot(t *testing.T) {
	store := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, maxSize: 3}

	store.Save("codex-my", "resp-1", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "codex snapshot"}}}})
	store.Save("mimo", "resp-1", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "mimo snapshot"}}}})
	store.Save("codex-my", "resp-1", []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "latest codex snapshot"}}}})

	if got := store.Load("mimo", "resp-1"); len(got) != 1 || got[0].Parts[0].Text != "mimo snapshot" {
		t.Fatalf("expected exact provider lookup to keep provider-specific snapshot, got %#v", got)
	}

	loaded := store.LoadAny("resp-1")
	if len(loaded) != 1 || loaded[0].Parts[0].Text != "latest codex snapshot" {
		t.Fatalf("expected LoadAny to return latest saved provider snapshot, got %#v", loaded)
	}

	loaded[0].Parts[0].Text = "mutated"
	reloaded := store.LoadAny("resp-1")
	if len(reloaded) != 1 || reloaded[0].Parts[0].Text != "latest codex snapshot" {
		t.Fatalf("expected LoadAny to return a clone, got %#v", reloaded)
	}
}

func TestResponsesHistorySaveDropsSyntheticReasoningContentFromStoredMessages(t *testing.T) {
	store := &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, maxSize: 2}
	store.Save("openai", "resp-1", []model.CanonicalMessage{{
		Role:             "assistant",
		ReasoningContent: "**推理中**\n\n代理层占位，以兼容不同上游情况，便于客户端记录推理时长\n\n",
		Parts:            []model.CanonicalContentPart{{Type: "text", Text: "answer"}},
	}})

	loaded := store.Load("openai", "resp-1")
	if len(loaded) != 1 {
		t.Fatalf("expected one loaded message, got %#v", loaded)
	}
	if loaded[0].ReasoningContent != "" {
		t.Fatalf("expected synthetic reasoning content to be dropped from stored history, got %#v", loaded[0])
	}
	if len(loaded[0].Parts) != 1 || loaded[0].Parts[0].Text != "answer" {
		t.Fatalf("expected assistant text preserved, got %#v", loaded[0])
	}
}

func TestBuildResponsesHistorySnapshotKeepsOnlyFollowUpRelevantMessages(t *testing.T) {
	base := []model.CanonicalMessage{
		{Role: "developer", Parts: []model.CanonicalContentPart{{Type: "text", Text: "tool registry and developer prompt"}}},
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "what is the weather"}}},
		{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "search_web", Arguments: `{"query":"weather"}`}}},
		{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: "plain assistant text that should not be persisted"}}},
	}
	assistant := []model.CanonicalMessage{{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: "done"}}}}

	snapshot := buildResponsesHistorySnapshot(base, assistant)
	if len(snapshot) != 3 {
		t.Fatalf("expected 3 messages in narrow snapshot, got %#v", snapshot)
	}
	if snapshot[0].Role != "user" || snapshot[0].Parts[0].Text != "what is the weather" {
		t.Fatalf("expected original user message to remain, got %#v", snapshot)
	}
	if snapshot[1].Role != "assistant" || len(snapshot[1].ToolCalls) != 1 || snapshot[1].ToolCalls[0].ID != "call_1" {
		t.Fatalf("expected assistant tool call message to remain, got %#v", snapshot)
	}
	if snapshot[2].Role != "assistant" || snapshot[2].Parts[0].Text != "done" {
		t.Fatalf("expected new assistant output to remain, got %#v", snapshot)
	}
	for _, msg := range snapshot {
		if len(msg.Parts) > 0 && msg.Parts[0].Text == "tool registry and developer prompt" {
			t.Fatalf("expected developer message to be excluded, got %#v", snapshot)
		}
		if len(msg.Parts) > 0 && msg.Parts[0].Text == "plain assistant text that should not be persisted" {
			t.Fatalf("expected plain assistant text to be excluded, got %#v", snapshot)
		}
	}
}

func TestBuildResponsesHistorySnapshotDropsErroredToolOutputs(t *testing.T) {
	base := []model.CanonicalMessage{
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "open repo"}}},
		{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "scrape_web", Arguments: `{"url":"https://example.com"}`}}},
		{Role: "tool", ToolCallID: "call_1", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"error":"invalid params, invalid function arguments json string"}`}}},
	}
	assistant := []model.CanonicalMessage{{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: "I will retry."}}}}

	snapshot := buildResponsesHistorySnapshot(base, assistant)
	if len(snapshot) != 3 {
		t.Fatalf("expected user + assistant tool_call + new assistant output, got %#v", snapshot)
	}
	for _, msg := range snapshot {
		if msg.Role == "tool" {
			t.Fatalf("expected errored tool output to be excluded from history snapshot, got %#v", snapshot)
		}
	}
}

func TestBuildResponsesHistorySnapshotKeepsSuccessfulToolOutputs(t *testing.T) {
	base := []model.CanonicalMessage{
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "open repo"}}},
		{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "scrape_web", Arguments: `{"url":"https://example.com"}`}}},
		{Role: "tool", ToolCallID: "call_1", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"markdown":"ok"}`}}},
	}
	assistant := []model.CanonicalMessage{{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: "done"}}}}

	snapshot := buildResponsesHistorySnapshot(base, assistant)
	if len(snapshot) != 4 {
		t.Fatalf("expected successful tool output to remain in snapshot, got %#v", snapshot)
	}
	if snapshot[2].Role != "tool" || snapshot[2].ToolCallID != "call_1" {
		t.Fatalf("expected successful tool message to remain, got %#v", snapshot)
	}
}

func TestBuildResponsesHistorySnapshotDropsSyntheticReasoningPlaceholder(t *testing.T) {
	base := []model.CanonicalMessage{{
		Role:  "user",
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}},
	}}
	assistant := []model.CanonicalMessage{{
		Role: "assistant",
		ReasoningBlocks: []map[string]any{{
			"type":    "reasoning",
			"id":      "rs_proxy",
			"summary": []map[string]any{{"type": "summary_text", "text": "**推理中**\n\n代理层占位，以兼容不同上游情况，便于客户端记录推理时长\n\n"}},
		}},
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "final answer"}},
	}}

	snapshot := buildResponsesHistorySnapshot(base, assistant)
	if len(snapshot) != 2 {
		t.Fatalf("expected user + assistant output only, got %#v", snapshot)
	}
	if len(snapshot[1].ReasoningBlocks) != 0 {
		t.Fatalf("expected synthetic rs_proxy reasoning to be excluded from history snapshot, got %#v", snapshot[1])
	}
	if snapshot[1].Parts[0].Text != "final answer" {
		t.Fatalf("expected assistant text preserved, got %#v", snapshot[1])
	}
}

func TestBuildResponsesHistorySnapshotKeepsRealReasoningBlocks(t *testing.T) {
	base := []model.CanonicalMessage{{
		Role:  "user",
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}},
	}}
	assistant := []model.CanonicalMessage{{
		Role: "assistant",
		ReasoningBlocks: []map[string]any{{
			"type":      "thinking",
			"thinking":  "真实推理",
			"signature": "sig_123",
		}},
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "final answer"}},
	}}

	snapshot := buildResponsesHistorySnapshot(base, assistant)
	if len(snapshot) != 2 {
		t.Fatalf("expected user + assistant output, got %#v", snapshot)
	}
	if len(snapshot[1].ReasoningBlocks) != 1 {
		t.Fatalf("expected real reasoning block to remain in history snapshot, got %#v", snapshot[1])
	}
	if got, _ := snapshot[1].ReasoningBlocks[0]["type"].(string); got != "thinking" {
		t.Fatalf("expected real thinking block preserved, got %#v", snapshot[1].ReasoningBlocks)
	}
}

func TestBuildResponsesHistorySnapshotKeepsRealProxyReasoningBlock(t *testing.T) {
	base := []model.CanonicalMessage{{
		Role:  "user",
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}},
	}}
	assistant := []model.CanonicalMessage{{
		Role: "assistant",
		ReasoningBlocks: []map[string]any{{
			"type":    "reasoning",
			"id":      "rs_proxy",
			"summary": []map[string]any{{"type": "summary_text", "text": "真实推理"}},
		}},
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "final answer"}},
	}}

	snapshot := buildResponsesHistorySnapshot(base, assistant)
	if len(snapshot) != 2 {
		t.Fatalf("expected user + assistant output, got %#v", snapshot)
	}
	if len(snapshot[1].ReasoningBlocks) != 1 {
		t.Fatalf("expected real rs_proxy reasoning block to remain in history snapshot, got %#v", snapshot[1])
	}
	if got, _ := snapshot[1].ReasoningBlocks[0]["id"].(string); got != "rs_proxy" {
		t.Fatalf("expected real rs_proxy reasoning block preserved, got %#v", snapshot[1].ReasoningBlocks)
	}
}

func TestResponsesHistoryReplaysProxyReasoningStateWithoutSummaryOrEncryption(t *testing.T) {
	// Given
	base := []model.CanonicalMessage{{
		Role:  "user",
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}},
	}}
	assistant := []model.CanonicalMessage{{
		Role: "assistant",
		ReasoningBlocks: []map[string]any{{
			"type":             "reasoning",
			"id":               "rs_proxy",
			"context":          "ctx_opaque",
			"phase":            "analysis",
			"vendor_reasoning": map[string]any{"opaque": "keep"},
		}},
	}}
	store := newResponsesHistoryStore(2, "")

	// When
	store.Save("provider", "resp_1", buildResponsesHistorySnapshot(base, assistant))
	replayed := store.Load("provider", "resp_1")

	// Then
	if len(replayed) != 2 || len(replayed[1].ReasoningBlocks) != 1 {
		t.Fatalf("expected real rs_proxy reasoning replayed, got %#v", replayed)
	}
	reasoning := replayed[1].ReasoningBlocks[0]
	if reasoning["context"] != "ctx_opaque" || reasoning["phase"] != "analysis" {
		t.Fatalf("expected real reasoning state replayed, got %#v", reasoning)
	}
	vendor, _ := reasoning["vendor_reasoning"].(map[string]any)
	if vendor["opaque"] != "keep" {
		t.Fatalf("expected opaque vendor state replayed, got %#v", reasoning)
	}
}

func TestBuildResponsesHistorySnapshotKeepsProxyReasoningStateWithPlaceholderSummary(t *testing.T) {
	// Given
	base := []model.CanonicalMessage{{
		Role:  "user",
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}},
	}}
	assistant := []model.CanonicalMessage{{
		Role: "assistant",
		ReasoningBlocks: []map[string]any{{
			"type":    "reasoning",
			"id":      "rs_proxy",
			"context": "ctx_opaque",
			"summary": []map[string]any{{"type": "summary_text", "text": "代理层占位"}},
		}},
	}}

	// When
	snapshot := buildResponsesHistorySnapshot(base, assistant)

	// Then
	if len(snapshot) != 2 || len(snapshot[1].ReasoningBlocks) != 1 {
		t.Fatalf("expected real rs_proxy state preserved despite placeholder summary, got %#v", snapshot)
	}
}

func TestBuildResponsesHistorySnapshotDropsSyntheticNativeThinkingBlocks(t *testing.T) {
	base := []model.CanonicalMessage{{
		Role:  "user",
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}},
	}}
	assistant := []model.CanonicalMessage{{
		Role: "assistant",
		ReasoningBlocks: []map[string]any{{
			"type":     "thinking",
			"thinking": "**推理中**\n\n代理层占位，以兼容不同上游情况，便于客户端记录推理时长\n\n",
		}},
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "final answer"}},
	}}

	snapshot := buildResponsesHistorySnapshot(base, assistant)
	if len(snapshot) != 2 {
		t.Fatalf("expected user + assistant output, got %#v", snapshot)
	}
	if len(snapshot[1].ReasoningBlocks) != 0 {
		t.Fatalf("expected synthetic native thinking block excluded from history snapshot, got %#v", snapshot[1])
	}
	if snapshot[1].Parts[0].Text != "final answer" {
		t.Fatalf("expected assistant text preserved, got %#v", snapshot[1])
	}
}

func TestAssistantHistoryMessagesFromResultDropsSyntheticReasoningSummary(t *testing.T) {
	messages := assistantHistoryMessagesFromResult(aggregate.Result{
		ResponseMessageContent: []map[string]any{{"type": "output_text", "text": "final answer"}},
		Reasoning: map[string]any{
			"summary": "**推理中**\n\n代理层占位，以兼容不同上游情况，便于客户端记录推理时长\n\n",
		},
	})
	if len(messages) != 1 {
		t.Fatalf("expected one assistant history message, got %#v", messages)
	}
	if got := messages[0].ReasoningContent; got != "" {
		t.Fatalf("expected synthetic reasoning summary to be dropped from history message, got %#v", messages[0])
	}
	if len(messages[0].ReasoningBlocks) != 0 {
		t.Fatalf("expected no reasoning blocks when only synthetic summary exists, got %#v", messages[0])
	}
	if len(messages[0].Parts) != 1 || messages[0].Parts[0].Text != "final answer" {
		t.Fatalf("expected assistant text preserved, got %#v", messages[0])
	}
}

func TestAssistantHistoryMessagesFromResultKeepsToolCalls(t *testing.T) {
	messages := assistantHistoryMessagesFromResult(aggregate.Result{
		ToolCalls: []aggregate.ToolCall{{CallID: "call_1", ID: "fc_1", Name: "search_web", Arguments: `{"query":"weather"}`}},
	})
	if len(messages) != 1 {
		t.Fatalf("expected one assistant history message, got %#v", messages)
	}
	if len(messages[0].ToolCalls) != 1 {
		t.Fatalf("expected tool call to be preserved, got %#v", messages[0])
	}
	call := messages[0].ToolCalls[0]
	if call.ID != "call_1" || call.ResponseItemID != "fc_1" || call.Name != "search_web" || call.Arguments != `{"query":"weather"}` {
		t.Fatalf("expected call_1/search_web tool call preserved, got %#v", call)
	}
}

func TestAssistantHistoryMessagesFromResultDropsInvisibleSyntheticReasoningSummary(t *testing.T) {
	messages := assistantHistoryMessagesFromResult(aggregate.Result{
		ResponseMessageContent: []map[string]any{{"type": "output_text", "text": "final answer"}},
		Reasoning: map[string]any{
			"summary": "\u200b \ufeff\n\t",
		},
	})
	if len(messages) != 1 {
		t.Fatalf("expected one assistant history message, got %#v", messages)
	}
	if got := messages[0].ReasoningContent; got != "" {
		t.Fatalf("expected invisible synthetic reasoning summary to be dropped from history message, got %#v", messages[0])
	}
	if len(messages[0].ReasoningBlocks) != 0 {
		t.Fatalf("expected no reasoning blocks when only invisible synthetic summary exists, got %#v", messages[0])
	}
	if len(messages[0].Parts) != 1 || messages[0].Parts[0].Text != "final answer" {
		t.Fatalf("expected assistant text preserved, got %#v", messages[0])
	}
}

func TestShouldRestorePreviousConversationAllowsNewUserTurnWithoutClientHistory(t *testing.T) {
	messages := []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "follow up"}}}}
	if !shouldRestorePreviousConversation(messages) {
		t.Fatalf("expected previous conversation restore for a new user turn without assistant history")
	}
}

func TestShouldRestorePreviousConversationSkipsWhenClientAlreadySendsAssistantHistory(t *testing.T) {
	messages := []model.CanonicalMessage{
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}}},
		{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "search_web", Arguments: `{"query":"weather"}`}}},
		{Role: "tool", ToolCallID: "call_1", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"ok":true}`}}},
	}
	if shouldRestorePreviousConversation(messages) {
		t.Fatalf("expected restore to be skipped when client already sends assistant history")
	}
}

func TestResponsesHistoryCompactionItemsDoNotProvidePreviousResponseID(t *testing.T) {
	items := []map[string]any{
		{"role": "user", "content": []map[string]any{{"type": "input_text", "text": "follow up"}}},
		{"type": "compaction", "id": "cmp_123", "encrypted_content": "enc_payload"},
	}

	if got := previousResponseIDFromItems(items); got != "" {
		t.Fatalf("expected compaction items alone not to provide previous_response_id, got %q", got)
	}

	if !shouldRestorePreviousConversation([]model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "follow up"}}}}) {
		t.Fatalf("expected restore decision to still depend on canonical messages, not compaction item presence")
	}
}

func TestResponsesHistoryPreviousResponseIDStillComesFromPreservedTopLevelFieldsWithCompactionItems(t *testing.T) {
	items := []map[string]any{
		{"type": "compaction", "id": "cmp_123", "encrypted_content": "enc_payload"},
		{"__openai_compat_responses_top_level": map[string]any{"previous_response_id": "resp_123"}},
	}

	if got := previousResponseIDFromItems(items); got != "resp_123" {
		t.Fatalf("expected previous_response_id to come only from preserved top-level fields, got %q", got)
	}
}
