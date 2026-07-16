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
