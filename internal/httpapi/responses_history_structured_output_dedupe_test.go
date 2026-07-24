package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"openai-compat-proxy/internal/adapter/responses"
	"openai-compat-proxy/internal/model"
)

type responsesHistoryStructuredOutputMarshalerForTest struct {
	payload []byte
	err     error
	calls   *int
}

func (value responsesHistoryStructuredOutputMarshalerForTest) MarshalJSON() ([]byte, error) {
	if value.calls != nil {
		*value.calls = *value.calls + 1
	}
	return value.payload, value.err
}

func TestResponsesHistorySnapshotDedupesDecodedStructuredOutputWithoutMutatingCurrentRequest(t *testing.T) {
	requestBody := []byte(`{
		"model":"gpt-5",
		"stream":true,
		"previous_response_id":"resp_previous",
		"input":[
			{"type":"function_call","id":"fc_dedupe","call_id":"call_dedupe","name":"inspect","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_dedupe","output":{"float":1.25,"escaped":"<>&\n\u2028\u2029","array":[true,null,{"nested":"value"}],"boolean":true,"null":null}}
		]
	}`)
	canon, err := responses.DecodeRequest(bytes.NewReader(requestBody))
	if err != nil {
		t.Fatalf("decode structured output request: %v", err)
	}
	if !canon.Stream || previousResponseIDFromItems(canon.ResponseInputItems) != "resp_previous" {
		t.Fatalf("expected streaming request and previous_response_id to remain intact, got %#v", canon)
	}
	inputBefore, err := json.Marshal(canon.ResponseInputItems)
	if err != nil {
		t.Fatalf("marshal preserved input before save: %v", err)
	}
	messages := prepareCanonicalMessages(canon.Messages)
	expected := cloneCanonicalMessages(selectResponsesHistoryMessages(messages, nil))
	logicalBytes := estimateCanonicalMessagesBytes(expected) + estimateToolRecoveryBytes(expected)
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")
	saveResponsesHistorySnapshot(store, "openai", "resp_dedupe", messages, nil, "scope_dedupe", "portable_dedupe")

	snapshot := store.entries[responsesHistoryKey("openai", "resp_dedupe")]
	toolMessageIndex := responsesHistoryStructuredOutputToolMessageIndex(t, expected, "call_dedupe")
	if snapshot.Bytes != logicalBytes || len(snapshot.StructuredOutputTextRefs) != 1 {
		t.Fatalf("expected one explicit dedupe reference without changing logical bytes, got snapshot=%#v", snapshot)
	}
	ref := snapshot.StructuredOutputTextRefs[0]
	if ref.MessageIndex != toolMessageIndex || ref.PartIndex != 0 || ref.RawKey != responsesHistoryStructuredOutputRawKey || ref.RendererID != responses.StructuredFunctionCallOutputRendererID || ref.TextBytes != len(expected[toolMessageIndex].Parts[0].Text) {
		t.Fatalf("unexpected structured-output dedupe metadata: %#v", ref)
	}
	if snapshot.Messages[toolMessageIndex].Parts[0].Text != "" {
		t.Fatalf("expected only the stored clone text to be cleared, got %#v", snapshot.Messages[toolMessageIndex].Parts[0])
	}
	if !reflect.DeepEqual(snapshot.Messages[toolMessageIndex].Parts[0].Raw, expected[toolMessageIndex].Parts[0].Raw) {
		t.Fatalf("expected stored clone Raw to remain unchanged, got %#v want %#v", snapshot.Messages[toolMessageIndex].Parts[0].Raw, expected[toolMessageIndex].Parts[0].Raw)
	}
	if messages[toolMessageIndex].Parts[0].Text != expected[toolMessageIndex].Parts[0].Text || canon.Messages[toolMessageIndex].Parts[0].Text != expected[toolMessageIndex].Parts[0].Text {
		t.Fatalf("expected current canonical messages to remain unchanged, got messages=%#v canon=%#v", messages, canon.Messages)
	}
	inputAfter, err := json.Marshal(canon.ResponseInputItems)
	if err != nil {
		t.Fatalf("marshal preserved input after save: %v", err)
	}
	if !bytes.Equal(inputAfter, inputBefore) {
		t.Fatalf("expected ResponseInputItems to remain isolated from history snapshot mutation:\nbefore=%s\nafter=%s", inputBefore, inputAfter)
	}

	loaded := store.LoadScoped("openai", "resp_dedupe", "scope_dedupe")
	if !reflect.DeepEqual(loaded, expected) {
		t.Fatalf("expected decoded structured output to round-trip from deduped snapshot:\nwant=%#v\ngot=%#v", expected, loaded)
	}
	responsesHistoryAssertStructuredOutputDecodedTypes(t, responsesHistoryStructuredOutputRaw(t, loaded, "call_dedupe"))
	if strings.Contains(snapshot.Messages[toolMessageIndex].Parts[0].Text, "escaped") {
		t.Fatal("expected the compact snapshot to retain no duplicate structured output text")
	}
}

func TestResponsesHistorySnapshotRestoresDedupedStructuredOutputAfterCompressedRawFields(t *testing.T) {
	raw := map[string]any{
		"payload": strings.Repeat("compressible structured output ", 4096),
		"float":   float64(1.25),
		"number":  json.Number("9007199254740993"),
		"raw":     json.RawMessage(`{"escaped":"<>&\n\u2028\u2029"}`),
		"bytes":   []byte("binary payload"),
		"stringMap": map[string]string{
			"stable": "value",
		},
		"strings": []string{"first", "second"},
		"nested": []any{
			map[string]any{"slice": []map[string]any{{"ok": true}}},
			nil,
		},
	}
	messages := responsesHistoryStructuredOutputMessages(t, raw)
	want := cloneCanonicalMessages(messages)
	logicalBytes := estimateCanonicalMessagesBytes(messages)
	snapshot, _ := newResponsesConversationSnapshot(messages, logicalBytes)
	if snapshot.Bytes != logicalBytes || len(snapshot.StructuredOutputTextRefs) != 1 {
		t.Fatalf("expected compact snapshot metadata with unchanged logical bytes, got %#v", snapshot)
	}
	if snapshot.Messages[0].Parts[0].Text != "" {
		t.Fatalf("expected dedupe to clear only clone text, got %#v", snapshot.Messages[0].Parts[0])
	}
	foundCompressedRaw := false
	for _, field := range snapshot.CompressedFields {
		if field.Kind == responsesHistoryCompressedPartRawString {
			foundCompressedRaw = true
			break
		}
	}
	if !foundCompressedRaw {
		t.Fatalf("expected nested structured Raw payload to be compressed, got %#v", snapshot.CompressedFields)
	}

	loaded := loadResponsesConversationSnapshot(snapshot)
	if !reflect.DeepEqual(loaded, want) {
		t.Fatalf("expected all compressed fields to restore before structured output text:\nwant=%#v\ngot=%#v", want, loaded)
	}
	loadedRaw := responsesHistoryStructuredOutputRaw(t, loaded, "call_structured")
	if got, ok := loadedRaw["number"].(json.Number); !ok || got != json.Number("9007199254740993") {
		t.Fatalf("expected json.Number to round-trip, got %#v", loadedRaw["number"])
	}
	if got, ok := loadedRaw["raw"].(json.RawMessage); !ok || string(got) != `{"escaped":"<>&\n\u2028\u2029"}` {
		t.Fatalf("expected json.RawMessage to round-trip, got %#v", loadedRaw["raw"])
	}
	if got, ok := loadedRaw["bytes"].([]byte); !ok || string(got) != "binary payload" {
		t.Fatalf("expected []byte to round-trip, got %#v", loadedRaw["bytes"])
	}
	if got, ok := loadedRaw["stringMap"].(map[string]string); !ok || !reflect.DeepEqual(got, map[string]string{"stable": "value"}) {
		t.Fatalf("expected map[string]string to round-trip, got %#v", loadedRaw["stringMap"])
	}
	if got, ok := loadedRaw["strings"].([]string); !ok || !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("expected []string to round-trip, got %#v", loadedRaw["strings"])
	}
	if nested, ok := loadedRaw["nested"].([]any); !ok || len(nested) != 2 {
		t.Fatalf("expected nested map/slice to round-trip, got %#v", loadedRaw["nested"])
	}
	if got := loaded[0].Parts[0].Text; !strings.Contains(got, `\u003c`) || !strings.Contains(got, `\u003e`) || !strings.Contains(got, `\u0026`) || !strings.Contains(got, `\u2028`) || !strings.Contains(got, `\u2029`) {
		t.Fatalf("expected canonical escaping to round-trip, got %q", got)
	}
}

func TestResponsesHistorySnapshotDedupesOnlyExactCanonicalStructuredOutputText(t *testing.T) {
	raw := map[string]any{"value": "canonical"}
	canonicalText, err := responses.RenderStructuredFunctionCallOutput(raw)
	if err != nil {
		t.Fatalf("render canonical structured output: %v", err)
	}

	for _, testCase := range []struct {
		name string
		raw  any
		text string
	}{
		{name: "empty text", raw: raw, text: ""},
		{name: "noncanonical text", raw: raw, text: canonicalText + " "},
		{name: "custom marshaler failure", raw: responsesHistoryStructuredOutputMarshalerForTest{err: errors.New("marshal failed")}, text: "fallback"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			messages := []model.CanonicalMessage{{
				Role:       "tool",
				ToolCallID: "call_ineligible",
				Parts: []model.CanonicalContentPart{{
					Type: "text",
					Text: testCase.text,
					Raw:  map[string]any{responsesHistoryStructuredOutputRawKey: testCase.raw},
				}},
			}}
			snapshot, _ := newResponsesConversationSnapshot(messages, estimateCanonicalMessagesBytes(messages))
			if len(snapshot.StructuredOutputTextRefs) != 0 || snapshot.Messages[0].Parts[0].Text != testCase.text {
				t.Fatalf("expected ineligible structured output to keep text inline, got refs=%#v part=%#v", snapshot.StructuredOutputTextRefs, snapshot.Messages[0].Parts[0])
			}
		})
	}
}

func TestResponsesHistoryStructuredOutputRawWhitelist(t *testing.T) {
	customCalls := 0
	for _, testCase := range []struct {
		name   string
		value  any
		stable bool
	}{
		{name: "nil", value: nil, stable: true},
		{name: "bool", value: true, stable: true},
		{name: "string", value: "value", stable: true},
		{name: "signed integers", value: int64(-1), stable: true},
		{name: "unsigned integers", value: uint64(1), stable: true},
		{name: "uintptr", value: uintptr(1), stable: true},
		{name: "float", value: float64(1.25), stable: true},
		{name: "json number", value: json.Number("9007199254740993"), stable: true},
		{name: "raw message", value: json.RawMessage(`{"nested":true}`), stable: true},
		{name: "bytes", value: []byte("raw-bytes"), stable: true},
		{name: "dynamic map", value: map[string]any{"nested": []any{map[string]any{"ok": true}}}, stable: true},
		{name: "dynamic map slice", value: []map[string]any{{"ok": true}}, stable: true},
		{name: "string map", value: map[string]string{"key": "value"}, stable: true},
		{name: "string slice", value: []string{"first", "second"}, stable: true},
		{name: "invalid raw message", value: json.RawMessage(`{`), stable: false},
		{name: "custom marshaler", value: responsesHistoryStructuredOutputMarshalerForTest{payload: []byte(`{"value":"custom"}`), calls: &customCalls}, stable: false},
		{name: "nested custom marshaler", value: map[string]any{"nested": responsesHistoryStructuredOutputMarshalerForTest{payload: []byte(`{"value":"custom"}`), calls: &customCalls}}, stable: false},
		{name: "unknown struct", value: struct{ Value string }{Value: "custom"}, stable: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := isStableResponsesHistoryStructuredOutputRaw(testCase.value); got != testCase.stable {
				t.Fatalf("stable raw whitelist result = %t, want %t for %#v", got, testCase.stable, testCase.value)
			}
		})
	}
	if customCalls != 0 {
		t.Fatalf("whitelist must not invoke custom MarshalJSON, got %d calls", customCalls)
	}
}

func TestResponsesHistorySnapshotDoesNotRenderCustomStructuredOutputMarshalers(t *testing.T) {
	calls := 0
	custom := responsesHistoryStructuredOutputMarshalerForTest{payload: []byte(`{"value":"custom"}`), calls: &calls}
	const text = `{"value":"custom"}`
	messages := []model.CanonicalMessage{{
		Role:       "tool",
		ToolCallID: "call_custom",
		Parts: []model.CanonicalContentPart{{
			Type: "text",
			Text: text,
			Raw:  map[string]any{responsesHistoryStructuredOutputRawKey: custom},
		}},
	}}
	snapshot, _ := newResponsesConversationSnapshot(messages, estimateCanonicalMessagesBytes(messages))
	if calls != 0 {
		t.Fatalf("save dedupe must not invoke custom MarshalJSON, got %d calls", calls)
	}
	if len(snapshot.StructuredOutputTextRefs) != 0 || snapshot.Messages[0].Parts[0].Text != text {
		t.Fatalf("custom marshaler must retain Text inline without a dedupe ref, got %#v", snapshot)
	}
}

func TestResponsesHistorySnapshotRejectsInvalidStructuredOutputTextRefs(t *testing.T) {
	baseline := responsesHistoryStructuredOutputSnapshot(t, map[string]any{"payload": "valid"})
	for _, testCase := range []struct {
		name   string
		mutate func(*responsesConversationSnapshot)
	}{
		{
			name: "missing raw",
			mutate: func(snapshot *responsesConversationSnapshot) {
				snapshot.Messages[0].Parts[0].Raw = nil
			},
		},
		{
			name: "invalid renderer",
			mutate: func(snapshot *responsesConversationSnapshot) {
				snapshot.StructuredOutputTextRefs[0].RendererID = "unknown"
			},
		},
		{
			name: "invalid raw key",
			mutate: func(snapshot *responsesConversationSnapshot) {
				snapshot.StructuredOutputTextRefs[0].RawKey = "other"
			},
		},
		{
			name: "out of range message",
			mutate: func(snapshot *responsesConversationSnapshot) {
				snapshot.StructuredOutputTextRefs[0].MessageIndex = 7
			},
		},
		{
			name: "out of range part",
			mutate: func(snapshot *responsesConversationSnapshot) {
				snapshot.StructuredOutputTextRefs[0].PartIndex = 7
			},
		},
		{
			name: "changed text hash",
			mutate: func(snapshot *responsesConversationSnapshot) {
				snapshot.StructuredOutputTextRefs[0].TextSHA256 = sha256.Sum256([]byte("different"))
			},
		},
		{
			name: "changed text size",
			mutate: func(snapshot *responsesConversationSnapshot) {
				snapshot.StructuredOutputTextRefs[0].TextBytes++
			},
		},
		{
			name: "non-tool message",
			mutate: func(snapshot *responsesConversationSnapshot) {
				snapshot.Messages[0].Role = "user"
			},
		},
		{
			name: "empty tool call id",
			mutate: func(snapshot *responsesConversationSnapshot) {
				snapshot.Messages[0].ToolCallID = ""
			},
		},
		{
			name: "duplicate target",
			mutate: func(snapshot *responsesConversationSnapshot) {
				snapshot.StructuredOutputTextRefs = append(snapshot.StructuredOutputTextRefs, snapshot.StructuredOutputTextRefs[0])
			},
		},
		{
			name: "nonempty compact text",
			mutate: func(snapshot *responsesConversationSnapshot) {
				snapshot.Messages[0].Parts[0].Text = "unexpected"
			},
		},
		{
			name: "changed raw",
			mutate: func(snapshot *responsesConversationSnapshot) {
				snapshot.Messages[0].Parts[0].Raw[responsesHistoryStructuredOutputRawKey] = map[string]any{"payload": "changed"}
			},
		},
		{
			name: "render failure",
			mutate: func(snapshot *responsesConversationSnapshot) {
				snapshot.Messages[0].Parts[0].Raw[responsesHistoryStructuredOutputRawKey] = math.Inf(1)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			snapshot := cloneResponsesHistoryStructuredOutputSnapshot(baseline)
			testCase.mutate(&snapshot)
			if loaded := loadResponsesConversationSnapshot(snapshot); loaded != nil {
				t.Fatalf("expected invalid structured-output metadata to fail closed, got %#v", loaded)
			}
		})
	}
}

func TestResponsesHistorySnapshotRejectsForgedCustomStructuredOutputRefWithoutRendering(t *testing.T) {
	calls := 0
	custom := responsesHistoryStructuredOutputMarshalerForTest{payload: []byte(`{"value":"custom"}`), calls: &calls}
	const forgedText = `{"value":"custom"}`
	snapshot := responsesConversationSnapshot{
		Messages: []model.CanonicalMessage{{
			Role:       "tool",
			ToolCallID: "call_custom",
			Parts: []model.CanonicalContentPart{{
				Type: "text",
				Raw:  map[string]any{responsesHistoryStructuredOutputRawKey: custom},
			}},
		}},
		StructuredOutputTextRefs: []responsesHistoryStructuredOutputTextRef{{
			MessageIndex: 0,
			PartIndex:    0,
			RawKey:       responsesHistoryStructuredOutputRawKey,
			RendererID:   responses.StructuredFunctionCallOutputRendererID,
			TextBytes:    len(forgedText),
			TextSHA256:   sha256.Sum256([]byte(forgedText)),
		}},
	}
	if loaded := loadResponsesConversationSnapshot(snapshot); loaded != nil {
		t.Fatalf("expected forged custom marshaler ref to fail closed, got %#v", loaded)
	}
	if calls != 0 {
		t.Fatalf("restore must not invoke custom MarshalJSON, got %d calls", calls)
	}
}

func TestResponsesHistorySnapshotDedupePreservesOpaqueReasoningAndToolRecovery(t *testing.T) {
	raw := map[string]any{"payload": strings.Repeat("structured output ", 4096)}
	messages := []model.CanonicalMessage{
		{
			Role: "assistant",
			ReasoningBlocks: []map[string]any{{
				"id":                "rs_structured",
				"type":              "reasoning",
				"encrypted_content": "opaque-state",
			}},
			ToolCalls: []model.CanonicalToolCall{{ID: "call_structured", Type: "function", Name: "inspect", Arguments: `{}`}},
		},
		responsesHistoryStructuredOutputMessage(t, raw),
	}
	store := newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")
	store.Save("openai", "resp_structured", messages, "scope_structured")
	key := responsesHistoryKey("openai", "resp_structured")
	store.mu.Lock()
	store.indexOpaqueThinkingLocked("openai", key, messages, "scope_structured")
	store.mu.Unlock()

	call, blocks, ok := store.LoadToolCall("openai", "call_structured", "scope_structured")
	if !ok || call.Name != "inspect" || len(blocks) != 1 || blocks[0]["encrypted_content"] != "opaque-state" {
		t.Fatalf("expected tool recovery and reasoning to survive structured-output dedupe, got call=%#v blocks=%#v ok=%t", call, blocks, ok)
	}
	public := responsesOpaqueThinkingPublicBlock(messages[0].ReasoningBlocks[0], 0)
	if opaque, ok := store.LoadOpaqueThinking("openai", "scope_structured", public); !ok || !reflect.DeepEqual(opaque, messages[0].ReasoningBlocks[0]) {
		t.Fatalf("expected opaque reasoning to remain recoverable, got ok=%t opaque=%#v", ok, opaque)
	}
	loaded := store.LoadScoped("openai", "resp_structured", "scope_structured")
	if len(loaded) != 2 || !reflect.DeepEqual(responsesHistoryStructuredOutputRaw(t, loaded, "call_structured"), raw) {
		t.Fatalf("expected structured output to remain scoped with reasoning, got %#v", loaded)
	}
}

func TestResponsesHistorySnapshotDedupeRemovesPhysicalTextDuplicateWithoutChangingLogicalBytes(t *testing.T) {
	raw := map[string]any{"payload": responsesHistoryStructuredOutputPayload("physical-", 64<<10)}
	messages := responsesHistoryStructuredOutputMessages(t, raw)
	logicalBytes := estimateCanonicalMessagesBytes(messages)
	legacy := responsesConversationSnapshot{Messages: cloneCanonicalMessages(messages), Bytes: logicalBytes}
	originalTextBytes := len(legacy.Messages[0].Parts[0].Text)
	snapshot, _ := newResponsesConversationSnapshot(messages, logicalBytes)
	if snapshot.Bytes != logicalBytes {
		t.Fatalf("expected logical history bytes to remain %d, got %d", logicalBytes, snapshot.Bytes)
	}
	if originalTextBytes == 0 || snapshot.Messages[0].Parts[0].Text != "" || len(snapshot.StructuredOutputTextRefs) != 1 {
		t.Fatalf("expected physical duplicate text removal from the snapshot clone, original=%d snapshot=%#v", originalTextBytes, snapshot)
	}
	if got := len(snapshot.Messages[0].Parts[0].Text); legacy.Messages[0].Parts[0].Text == snapshot.Messages[0].Parts[0].Text || legacy.Messages[0].Parts[0].Text == "" || got != 0 {
		t.Fatalf("expected snapshot to release exactly the inline duplicate while retaining Raw, legacy=%d deduped=%d", len(legacy.Messages[0].Parts[0].Text), got)
	}
}

func responsesHistoryStructuredOutputMessages(t testing.TB, raw any) []model.CanonicalMessage {
	t.Helper()
	return []model.CanonicalMessage{responsesHistoryStructuredOutputMessage(t, raw)}
}

func responsesHistoryStructuredOutputMessage(t testing.TB, raw any) model.CanonicalMessage {
	t.Helper()
	text, err := responses.RenderStructuredFunctionCallOutput(raw)
	if err != nil {
		t.Fatalf("render structured output: %v", err)
	}
	return model.CanonicalMessage{
		Role:       "tool",
		ToolCallID: "call_structured",
		Parts:      []model.CanonicalContentPart{{Type: "text", Text: text, Raw: map[string]any{responsesHistoryStructuredOutputRawKey: raw}}},
	}
}

func responsesHistoryStructuredOutputSnapshot(t testing.TB, raw any) responsesConversationSnapshot {
	t.Helper()
	messages := responsesHistoryStructuredOutputMessages(t, raw)
	snapshot, _ := newResponsesConversationSnapshot(messages, estimateCanonicalMessagesBytes(messages))
	if len(snapshot.StructuredOutputTextRefs) != 1 {
		t.Fatalf("expected valid structured-output dedupe metadata, got %#v", snapshot)
	}
	return snapshot
}

func cloneResponsesHistoryStructuredOutputSnapshot(snapshot responsesConversationSnapshot) responsesConversationSnapshot {
	cloned := snapshot
	cloned.Messages = cloneCanonicalMessages(snapshot.Messages)
	cloned.CompressedFields = cloneResponsesHistoryCompressedFields(snapshot.CompressedFields)
	cloned.StructuredOutputTextRefs = append([]responsesHistoryStructuredOutputTextRef(nil), snapshot.StructuredOutputTextRefs...)
	return cloned
}

func responsesHistoryStructuredOutputToolMessageIndex(t testing.TB, messages []model.CanonicalMessage, callID string) int {
	t.Helper()
	for index, message := range messages {
		if message.Role == "tool" && message.ToolCallID == callID {
			return index
		}
	}
	t.Fatalf("expected tool message for %q, got %#v", callID, messages)
	return -1
}

func responsesHistoryStructuredOutputRaw(t testing.TB, messages []model.CanonicalMessage, callID string) map[string]any {
	t.Helper()
	messageIndex := responsesHistoryStructuredOutputToolMessageIndex(t, messages, callID)
	for _, part := range messages[messageIndex].Parts {
		if raw, ok := part.Raw[responsesHistoryStructuredOutputRawKey].(map[string]any); ok {
			return raw
		}
	}
	t.Fatalf("expected structured output Raw for %q, got %#v", callID, messages[messageIndex])
	return nil
}

func responsesHistoryAssertStructuredOutputDecodedTypes(t testing.TB, raw map[string]any) {
	t.Helper()
	if value, ok := raw["float"].(float64); !ok || value != 1.25 {
		t.Fatalf("expected float64 structured output value, got %#v", raw["float"])
	}
	if raw["boolean"] != true || raw["null"] != nil {
		t.Fatalf("expected boolean and null structured output values, got %#v", raw)
	}
	array, ok := raw["array"].([]any)
	if !ok || len(array) != 3 || array[0] != true || array[1] != nil {
		t.Fatalf("expected structured array to round-trip, got %#v", raw["array"])
	}
	if nested, ok := array[2].(map[string]any); !ok || nested["nested"] != "value" {
		t.Fatalf("expected nested structured map to round-trip, got %#v", array[2])
	}
}

func responsesHistoryStructuredOutputPayload(marker string, size int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	var builder strings.Builder
	builder.Grow(size)
	builder.WriteString(marker)
	state := uint64(0x9e3779b97f4a7c15)
	for builder.Len() < size {
		state = state*6364136223846793005 + 1442695040888963407
		builder.WriteByte(alphabet[(state>>32)%uint64(len(alphabet))])
	}
	return builder.String()
}
