package responses

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"openai-compat-proxy/internal/model"
)

func TestDecodeRequestMemoryOptimizationFixturePreservesDynamicFields(t *testing.T) {
	original, canon := decodeMemoryOptimizationFixture(t)

	wantInput, ok := original["input"].([]any)
	if !ok {
		t.Fatalf("fixture input is not an array: %#v", original["input"])
	}
	gotInput := make([]any, 0, len(canon.ResponseInputItems))
	for _, item := range canon.ResponseInputItems {
		if _, isTopLevelEcho := item[preservedResponsesTopLevelFieldsKey]; isTopLevelEcho {
			continue
		}
		gotInput = append(gotInput, item)
	}
	if !reflect.DeepEqual(gotInput, wantInput) {
		t.Fatalf("expected every raw input item and unknown field to remain lossless, got %#v want %#v", gotInput, wantInput)
	}
	if !reflect.DeepEqual(canon.PreservedTopLevelFields["vendor_top_level"], original["vendor_top_level"]) {
		t.Fatalf("expected unknown top-level field to remain lossless, got %#v", canon.PreservedTopLevelFields)
	}
	wantReasoning, _ := original["reasoning"].(map[string]any)
	if canon.Reasoning == nil || !reflect.DeepEqual(canon.Reasoning.Raw, wantReasoning) {
		t.Fatalf("expected reasoning fields to remain lossless, got %#v want %#v", canon.Reasoning, wantReasoning)
	}
	wantTools, _ := original["tools"].([]any)
	if len(canon.Tools) != 1 || len(wantTools) != 1 || !reflect.DeepEqual(canon.Tools[0].Raw, wantTools[0]) {
		t.Fatalf("expected tool fields to remain lossless, got %#v want %#v", canon.Tools, wantTools)
	}
}

func TestDecodeRequestPreservedFieldsSkipsKnownValuesAndDetectsStringInput(t *testing.T) {
	data := []byte(`{"model":"gpt-5.6","input":"hello","reasoning":{"effort":"high"},"vendor_top_level":{"keep":true},"vendor_array":[1,{"keep":true}]}`)

	got, inputWasString, err := decodeRequestPreservedFields(data)
	if err != nil {
		t.Fatalf("decode preserved fields: %v", err)
	}
	if !inputWasString {
		t.Fatal("expected string input to be detected")
	}
	if len(got) != 2 {
		t.Fatalf("expected only unknown top-level fields, got %#v", got)
	}
	if got["vendor_top_level"].(map[string]any)["keep"] != true {
		t.Fatalf("expected unknown object to be preserved, got %#v", got["vendor_top_level"])
	}
	if len(got["vendor_array"].([]any)) != 2 {
		t.Fatalf("expected unknown array to be preserved, got %#v", got["vendor_array"])
	}
}

func TestDecodeRequestPreservedFieldsHandlesNestedAndEscapedValues(t *testing.T) {
	data := []byte(`{"model":{"nested":[{"text":"known"}]},"input":[{"role":"user","content":"known"}],"vendor_key":"quote\\\"slash\\\\","vendor_nested":{"items":[1,{"text":"}"}]},"vendor_null":null}`)

	got, inputWasString, err := decodeRequestPreservedFields(data)
	if err != nil {
		t.Fatalf("decode preserved fields: %v", err)
	}
	if inputWasString {
		t.Fatal("expected array input not to be detected as string input")
	}
	if got["vendor_key"] != `quote\"slash\\` {
		t.Fatalf("expected escaped unknown string to be preserved, got %#v", got["vendor_key"])
	}
	nested, ok := got["vendor_nested"].(map[string]any)
	if !ok || len(nested["items"].([]any)) != 2 {
		t.Fatalf("expected nested unknown value to be preserved, got %#v", got["vendor_nested"])
	}
	if value, exists := got["vendor_null"]; !exists || value != nil {
		t.Fatalf("expected null unknown value to be preserved, got %#v", got["vendor_null"])
	}
}

func TestDecodeRequestPreservedFieldsKeepsNullInputCompatibility(t *testing.T) {
	got, inputWasString, err := decodeRequestPreservedFields([]byte(`{"model":"gpt-5.6","input":null}`))
	if err != nil {
		t.Fatalf("decode preserved fields: %v", err)
	}
	if got != nil {
		t.Fatalf("expected no preserved fields, got %#v", got)
	}
	if !inputWasString {
		t.Fatal("expected null input to retain legacy string-input compatibility")
	}
}

func TestDecodeRequestPreservedFieldsUsesLastDuplicateInputAndDecodesEscapedKey(t *testing.T) {
	tests := []struct {
		name           string
		data           string
		inputWasString bool
	}{
		{
			name:           "string then number",
			data:           `{"input":"first","input":42}`,
			inputWasString: false,
		},
		{
			name:           "number then string",
			data:           `{"input":42,"input":"last"}`,
			inputWasString: true,
		},
		{
			name:           "escaped key then number",
			data:           `{"\u0069nput":"first","input":false}`,
			inputWasString: false,
		},
		{
			name:           "number then escaped key",
			data:           `{"input":false,"\u0069nput":null}`,
			inputWasString: true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, inputWasString, err := decodeRequestPreservedFields([]byte(testCase.data))
			if err != nil {
				t.Fatalf("decode preserved fields: %v", err)
			}
			if inputWasString != testCase.inputWasString {
				t.Fatalf("inputWasString=%v, want %v", inputWasString, testCase.inputWasString)
			}
		})
	}
}

func TestDecodeRequestPreservedFieldsHandlesTopLevelNull(t *testing.T) {
	got, inputWasString, err := decodeRequestPreservedFields([]byte(" null \n"))
	if err != nil {
		t.Fatalf("decode preserved fields: %v", err)
	}
	if got != nil || inputWasString {
		t.Fatalf("expected null request to keep zero preserved state, got fields=%#v string=%v", got, inputWasString)
	}
}

func TestDecodeRequestPreservedFieldsRejectsTrailingData(t *testing.T) {
	for _, data := range []string{
		`{"model":"gpt-5.6"} trailing`,
		`null trailing`,
	} {
		if _, _, err := decodeRequestPreservedFields([]byte(data)); err == nil {
			t.Fatalf("expected trailing data to fail for %q", data)
		}
	}
}

func TestDecodeRequestMemoryOptimizationFixturePreservesTypedMultimodalSemantics(t *testing.T) {
	_, canon := decodeMemoryOptimizationFixture(t)

	if canon.Model != "gpt-5.6" || !canon.ResponseInputItemsAreOriginal {
		t.Fatalf("expected original Responses graph metadata, got model=%q original=%v", canon.Model, canon.ResponseInputItemsAreOriginal)
	}
	if len(canon.Messages) != 3 {
		t.Fatalf("expected user, reasoning/tool, and tool-result messages, got %#v", canon.Messages)
	}
	user := canon.Messages[0]
	if user.Role != "user" || len(user.Parts) != 4 {
		t.Fatalf("expected typed user text/image/file/audio parts, got %#v", user)
	}
	if user.Parts[0].Type != "text" || user.Parts[0].Text != "hello" {
		t.Fatalf("expected typed text part, got %#v", user.Parts[0])
	}
	for _, testCase := range []struct {
		name         string
		partIndex    int
		containerKey string
		fieldKey     string
		want         any
	}{
		{name: "image", partIndex: 1, containerKey: "image_url", fieldKey: "vendor_image", want: map[string]any{"keep": true}},
		{name: "file", partIndex: 2, containerKey: "input_file", fieldKey: "vendor_file", want: []any{"keep", nil}},
		{name: "audio", partIndex: 3, containerKey: "input_audio", fieldKey: "vendor_audio", want: map[string]any{"keep": float64(7)}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			part := user.Parts[testCase.partIndex]
			container, ok := part.Raw[testCase.containerKey].(map[string]any)
			if !ok || !reflect.DeepEqual(container[testCase.fieldKey], testCase.want) {
				t.Fatalf("expected %s.%s=%#v, got %#v", testCase.containerKey, testCase.fieldKey, testCase.want, part)
			}
		})
	}

	assistant := canon.Messages[1]
	if assistant.Role != "assistant" || assistant.ReasoningContent != "thinking" || len(assistant.ReasoningBlocks) != 1 || len(assistant.ToolCalls) != 1 {
		t.Fatalf("expected typed reasoning and function call, got %#v", assistant)
	}
	if assistant.ToolCalls[0].ID != "call_1" || assistant.ToolCalls[0].Name != "lookup" {
		t.Fatalf("expected typed lookup call, got %#v", assistant.ToolCalls)
	}
	toolResult := canon.Messages[2]
	if toolResult.Role != "tool" || toolResult.ToolCallID != "call_1" || len(toolResult.Parts) != 1 {
		t.Fatalf("expected typed function output, got %#v", toolResult)
	}
}

func TestDecodeRequestTextOnlyMessageFastPathPreservesDynamicFields(t *testing.T) {
	requestBody := []byte(`{
		"model":"gpt-5.6",
		"input":[{
			"type":"message",
			"id":"msg_text_only",
			"role":"user",
			"content":[
				{"type":"input_text","text":"first","vendor_text":{"keep":true}},
				{"type":"text","text":"second","vendor_metadata":[1,{"keep":"yes"}]},
				{"type":"output_text","text":null}
			],
			"vendor_message":{"nested":{"keep":true}}
		}]
	}`)
	var original map[string]any
	if err := json.Unmarshal(requestBody, &original); err != nil {
		t.Fatalf("unmarshal original request body: %v", err)
	}

	canon, err := DecodeRequest(bytes.NewReader(requestBody))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	wantInput, _ := original["input"].([]any)
	gotInput := make([]any, len(canon.ResponseInputItems))
	for index := range canon.ResponseInputItems {
		gotInput[index] = canon.ResponseInputItems[index]
	}
	if !reflect.DeepEqual(gotInput, wantInput) {
		t.Fatalf("expected text-only message fields to remain lossless, got %#v want %#v", gotInput, wantInput)
	}
	if len(canon.Messages) != 1 || canon.Messages[0].Role != "user" {
		t.Fatalf("expected one canonical user message, got %#v", canon.Messages)
	}
	parts := canon.Messages[0].Parts
	if len(parts) != 3 || parts[0].Text != "first" || parts[1].Text != "second" || parts[2].Text != "" {
		t.Fatalf("expected canonical text parts preserved, got %#v", parts)
	}
}

func TestDecodeRequestTextOnlyMessageFastPathBoundarySemantics(t *testing.T) {
	tests := []struct {
		name     string
		item     string
		wantText []string
	}{
		{
			name:     "omitted content",
			item:     `{"type":"message","role":"user","vendor_case":{"keep":true}}`,
			wantText: []string{},
		},
		{
			name:     "null content",
			item:     `{"type":"message","role":"user","content":null,"vendor_case":{"keep":true}}`,
			wantText: []string{},
		},
		{
			name:     "empty content string",
			item:     `{"type":"message","role":"user","content":"","vendor_case":{"keep":true}}`,
			wantText: []string{},
		},
		{
			name:     "undefined content string",
			item:     `{"type":"message","role":"user","content":"undefined","vendor_case":{"keep":true}}`,
			wantText: []string{},
		},
		{
			name:     "bracketed undefined content string",
			item:     `{"type":"message","role":"user","content":"[undefined]","vendor_case":{"keep":true}}`,
			wantText: []string{},
		},
		{
			name:     "omitted text field",
			item:     `{"type":"message","role":"user","content":[{"type":"input_text","vendor_part":{"keep":true}}],"vendor_case":{"keep":true}}`,
			wantText: []string{""},
		},
		{
			name:     "null text field",
			item:     `{"type":"message","role":"user","content":[{"type":"input_text","text":null,"vendor_part":{"keep":true}}],"vendor_case":{"keep":true}}`,
			wantText: []string{""},
		},
		{
			name:     "empty text field",
			item:     `{"type":"message","role":"user","content":[{"type":"input_text","text":"","vendor_part":{"keep":true}}],"vendor_case":{"keep":true}}`,
			wantText: []string{""},
		},
		{
			name:     "literal undefined text field",
			item:     `{"type":"message","role":"user","content":[{"type":"input_text","text":"undefined","vendor_part":{"keep":true}}],"vendor_case":{"keep":true}}`,
			wantText: []string{"undefined"},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			rawItem, canon, err := decodeSingleResponsesInputItem(t, testCase.item)
			if err != nil {
				t.Fatalf("DecodeRequest error: %v", err)
			}
			if _, ok := decodeTextOnlyMessageInputItem(rawItem); !ok {
				t.Fatal("expected text-only message to remain fast-path eligible")
			}
			if !canon.ResponseInputItemsAreOriginal {
				t.Fatalf("expected raw-first forwarding to remain enabled, got %#v", canon)
			}
			assertResponsesInputItemMatchesRaw(t, canon, rawItem)
			if len(canon.Messages) != 1 || canon.Messages[0].Role != "user" {
				t.Fatalf("expected one canonical user message, got %#v", canon.Messages)
			}
			assertCanonicalTextParts(t, canon.Messages[0], testCase.wantText)
		})
	}
}

func TestDecodeRequestTextOnlyMessageFastPathFallsBackForBoundaryPredicates(t *testing.T) {
	tests := []struct {
		name         string
		item         string
		wantOriginal bool
		wantErr      bool
		verify       func(*testing.T, model.CanonicalRequest)
	}{
		{
			name:         "assistant tool calls null",
			item:         `{"type":"message","role":"assistant","content":"hello","tool_calls":null,"vendor_case":{"keep":true}}`,
			wantOriginal: false,
			verify: func(t *testing.T, canon model.CanonicalRequest) {
				t.Helper()
				if len(canon.Messages) != 1 || canon.Messages[0].Role != "assistant" || len(canon.Messages[0].ToolCalls) != 0 {
					t.Fatalf("expected assistant fallback without tool calls, got %#v", canon.Messages)
				}
				assertCanonicalTextParts(t, canon.Messages[0], []string{"hello"})
			},
		},
		{
			name:         "tool call id null",
			item:         `{"type":"message","role":"tool","content":"hello","tool_call_id":null,"vendor_case":{"keep":true}}`,
			wantOriginal: true,
			verify: func(t *testing.T, canon model.CanonicalRequest) {
				t.Helper()
				if len(canon.Messages) != 1 || canon.Messages[0].Role != "tool" || canon.Messages[0].ToolCallID != "" {
					t.Fatalf("expected tool fallback with an empty tool call id, got %#v", canon.Messages)
				}
				assertCanonicalTextParts(t, canon.Messages[0], []string{"hello"})
			},
		},
		{
			name:    "malformed text field",
			item:    `{"type":"message","role":"user","content":[{"type":"input_text","text":42}]}`,
			wantErr: true,
		},
		{
			name:         "mixed text and reasoning",
			item:         `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"},{"type":"reasoning","id":"rs_real","summary":[{"type":"summary_text","text":"thinking"}],"encrypted_content":"enc_real","vendor_reasoning":{"keep":true}}],"vendor_case":{"keep":true}}`,
			wantOriginal: true,
			verify: func(t *testing.T, canon model.CanonicalRequest) {
				t.Helper()
				if len(canon.Messages) != 1 || len(canon.Messages[0].ReasoningBlocks) != 1 {
					t.Fatalf("expected fallback to preserve the reasoning block, got %#v", canon.Messages)
				}
				assertCanonicalTextParts(t, canon.Messages[0], []string{"answer"})
				block := canon.Messages[0].ReasoningBlocks[0]
				if block["encrypted_content"] != "enc_real" {
					t.Fatalf("expected opaque reasoning state preserved, got %#v", block)
				}
				vendor, _ := block["vendor_reasoning"].(map[string]any)
				if vendor["keep"] != true {
					t.Fatalf("expected unknown reasoning field preserved, got %#v", block)
				}
			},
		},
		{
			name:         "mixed text and image",
			item:         `{"type":"message","role":"user","content":[{"type":"input_text","text":"describe"},{"type":"input_image","image_url":{"url":"https://example.test/image.png","detail":"high","vendor_image":{"keep":true}}}],"vendor_case":{"keep":true}}`,
			wantOriginal: true,
			verify: func(t *testing.T, canon model.CanonicalRequest) {
				t.Helper()
				if len(canon.Messages) != 1 || len(canon.Messages[0].Parts) != 2 {
					t.Fatalf("expected fallback to preserve text and image parts, got %#v", canon.Messages)
				}
				assertCanonicalTextParts(t, model.CanonicalMessage{Parts: canon.Messages[0].Parts[:1]}, []string{"describe"})
				image := canon.Messages[0].Parts[1]
				if image.Type != "input_image" || image.ImageURL != "https://example.test/image.png" {
					t.Fatalf("expected canonical image part, got %#v", image)
				}
				rawImage, _ := image.Raw["image_url"].(map[string]any)
				vendor, _ := rawImage["vendor_image"].(map[string]any)
				if rawImage["detail"] != "high" || vendor["keep"] != true {
					t.Fatalf("expected image fields preserved, got %#v", image)
				}
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			rawItem, canon, err := decodeSingleResponsesInputItem(t, testCase.item)
			if _, ok := decodeTextOnlyMessageInputItem(rawItem); ok {
				t.Fatal("expected boundary case to fall back to the existing decoder")
			}
			if testCase.wantErr {
				if err == nil {
					t.Fatal("expected fallback decoder to reject malformed content")
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodeRequest error: %v", err)
			}
			if canon.ResponseInputItemsAreOriginal != testCase.wantOriginal {
				t.Fatalf("ResponseInputItemsAreOriginal=%v, want %v", canon.ResponseInputItemsAreOriginal, testCase.wantOriginal)
			}
			assertResponsesInputItemMatchesRaw(t, canon, rawItem)
			testCase.verify(t, canon)
		})
	}
}

func decodeSingleResponsesInputItem(t *testing.T, item string) (map[string]any, model.CanonicalRequest, error) {
	t.Helper()
	requestBody := []byte(`{"model":"gpt-5.6","input":[` + item + `]}`)
	var original map[string]any
	if err := json.Unmarshal(requestBody, &original); err != nil {
		t.Fatalf("unmarshal original request body: %v", err)
	}
	input, _ := original["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("expected one original input item, got %#v", original["input"])
	}
	rawItem, _ := input[0].(map[string]any)
	if rawItem == nil {
		t.Fatalf("expected object input item, got %#v", input[0])
	}
	canon, err := DecodeRequest(bytes.NewReader(requestBody))
	return rawItem, canon, err
}

func assertResponsesInputItemMatchesRaw(t *testing.T, canon model.CanonicalRequest, rawItem map[string]any) {
	t.Helper()
	gotInput := make([]any, len(canon.ResponseInputItems))
	for index := range canon.ResponseInputItems {
		gotInput[index] = canon.ResponseInputItems[index]
	}
	if !reflect.DeepEqual(gotInput, []any{rawItem}) {
		t.Fatalf("expected raw input item preserved for raw-first forwarding, got %#v want %#v", gotInput, rawItem)
	}
}

func assertCanonicalTextParts(t *testing.T, message model.CanonicalMessage, wantText []string) {
	t.Helper()
	if len(message.Parts) != len(wantText) {
		t.Fatalf("expected %d canonical text parts, got %#v", len(wantText), message.Parts)
	}
	if len(wantText) == 0 && message.Parts == nil {
		t.Fatalf("expected empty content to preserve the existing non-nil canonical parts slice")
	}
	for index, want := range wantText {
		part := message.Parts[index]
		if part.Type != "text" || part.Text != want {
			t.Fatalf("canonical text part %d = %#v, want text %q", index, part, want)
		}
	}
}

func decodeMemoryOptimizationFixture(t *testing.T) (map[string]any, model.CanonicalRequest) {
	t.Helper()
	body, err := os.ReadFile("testdata/memory-optimization-semantic-request.json")
	if err != nil {
		t.Fatalf("read semantic fixture: %v", err)
	}
	var original map[string]any
	if err := json.Unmarshal(body, &original); err != nil {
		t.Fatalf("decode semantic fixture: %v", err)
	}
	canon, err := DecodeRequest(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	return original, canon
}
