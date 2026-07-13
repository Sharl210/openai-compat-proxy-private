package chat

import (
	"reflect"
	"strings"
	"testing"
)

func TestDecodeRequestRecordsBodyReasoningMode(t *testing.T) {
	// Given
	requestBody := `{"model":"model","reasoning":{"mode":"pro","effort":"low","summary":"detailed","vendor_option":"keep"},"messages":[{"role":"user","content":"hello"}]}`

	// When
	canon, err := DecodeRequest(strings.NewReader(requestBody))

	// Then
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if canon.Reasoning == nil || canon.Reasoning.Raw["mode"] != "pro" || canon.Reasoning.Raw["vendor_option"] != "keep" {
		t.Fatalf("expected body reasoning payload preserved, got %#v", canon.Reasoning)
	}
	mode := reflect.ValueOf(canon.Reasoning).Elem().FieldByName("Mode")
	if !mode.IsValid() || mode.Type().Name() != "ReasoningMode" || mode.String() != "pro" {
		t.Fatalf("expected typed mode pro, got %#v", canon.Reasoning)
	}
	origin := reflect.ValueOf(canon).FieldByName("ReasoningModeOrigin")
	if !origin.IsValid() || origin.Type().Name() != "ReasoningModeOrigin" || origin.String() != "body" {
		t.Fatalf("expected body mode origin, got %#v", canon)
	}
}

func TestDecodeRequestAcceptsAssistantNullContentWithToolCalls(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"messages":[
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Shanghai\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"晴"}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if len(canon.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %#v", canon.Messages)
	}
	if len(canon.Messages[0].ToolCalls) != 1 {
		t.Fatalf("expected assistant tool call to survive null content, got %#v", canon.Messages[0])
	}
}

func TestDecodeRequestSanitizesAssistantToolCallArguments(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"messages":[
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup_project_facts","arguments":"请使用 {\"project\":\"atlas\",\"focus\":\"cache\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"{\"cache_state\":\"warm\"}"}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if len(canon.Messages) != 2 || len(canon.Messages[0].ToolCalls) != 1 {
		t.Fatalf("expected assistant tool call preserved, got %#v", canon.Messages)
	}
	if got := canon.Messages[0].ToolCalls[0].Arguments; got != `{"project":"atlas","focus":"cache"}` {
		t.Fatalf("expected sanitized tool arguments, got %q", got)
	}
}

func TestDecodeRequestSanitizesAssistantToolCallArgumentsWithTrailingGarbage(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"messages":[
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup_project_facts","arguments":"{\"focus\": \"cache\", \"project\": \"atlas\"}\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"{\"cache_state\":\"warm\"}"}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if len(canon.Messages) != 2 || len(canon.Messages[0].ToolCalls) != 1 {
		t.Fatalf("expected assistant tool call preserved, got %#v", canon.Messages)
	}
	if got := canon.Messages[0].ToolCalls[0].Arguments; got != `{"focus":"cache","project":"atlas"}` {
		t.Fatalf("expected trailing garbage to be stripped from tool arguments, got %q", got)
	}
}

func TestDecodeRequestPreservesClientToolOrderForCanonicalization(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"tools":[
			{"type":"function","function":{"name":"workspace_shell","description":"shell","parameters":{"type":"object"}}},
			{"type":"function","function":{"name":"search_web","description":"search","parameters":{"type":"object"}}}
		],
		"messages":[{"role":"user","content":"hello"}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	if len(canon.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %#v", canon.Tools)
	}
	if canon.Tools[0].Name != "workspace_shell" || canon.Tools[1].Name != "search_web" {
		t.Fatalf("expected client tool order preserved in canonical form, got %#v", canon.Tools)
	}
}

func TestDecodeRequestPreservesParallelToolCallsTriState(t *testing.T) {
	trueValue, falseValue := true, false
	for _, tc := range []struct {
		name string
		body string
		want *bool
	}{
		{name: "unspecified", body: `{"model":"gpt-5.6","messages":[{"role":"user","content":"hello"}]}`},
		{name: "allowed", body: `{"model":"gpt-5.6","messages":[{"role":"user","content":"hello"}],"parallel_tool_calls":true}`, want: &trueValue},
		{name: "disabled", body: `{"model":"gpt-5.6","messages":[{"role":"user","content":"hello"}],"parallel_tool_calls":false}`, want: &falseValue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			canon, err := DecodeRequest(strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("DecodeRequest error: %v", err)
			}
			if canon.ParallelToolCalls == nil || tc.want == nil || *canon.ParallelToolCalls != *tc.want {
				if canon.ParallelToolCalls == nil && tc.want == nil {
					return
				}
				t.Fatalf("expected ParallelToolCalls %#v, got %#v", tc.want, canon.ParallelToolCalls)
			}
		})
	}
}

func TestDecodeRequestNormalizesNamedToolChoice(t *testing.T) {
	canon, err := DecodeRequest(strings.NewReader(`{"model":"gpt-5.6","messages":[{"role":"user","content":"hello"}],"tool_choice":{"type":"function","function":{"name":"lookup"}}}`))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if got := string(canon.ToolChoice.Requirement); got != "required_named" || canon.ToolChoice.Name != "lookup" {
		t.Fatalf("expected required named lookup choice, got %#v", canon.ToolChoice)
	}
}

func TestDecodeRequestAcceptsStopAsSingleString(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"stop":"END",
		"messages":[{"role":"user","content":"hello"}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if len(canon.Stop) != 1 || canon.Stop[0] != "END" {
		t.Fatalf("expected single stop token END, got %#v", canon.Stop)
	}
}

func TestDecodeRequestAcceptsFileContentPart(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"file_123"}}]}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if len(canon.Messages) != 1 || len(canon.Messages[0].Parts) != 1 {
		t.Fatalf("expected one file content part, got %#v", canon.Messages)
	}
	if got := canon.Messages[0].Parts[0].Type; got != "input_file" {
		t.Fatalf("expected input_file part, got %#v", canon.Messages[0].Parts[0])
	}
	fileRaw, _ := canon.Messages[0].Parts[0].Raw["input_file"].(map[string]any)
	if got := fileRaw["file_id"]; got != "file_123" {
		t.Fatalf("expected file_id preserved, got %#v", canon.Messages[0].Parts[0])
	}
}

func TestDecodeRequestAcceptsInputAudioContentPart(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"YWJj","format":"mp3"}}]}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if len(canon.Messages) != 1 || len(canon.Messages[0].Parts) != 1 {
		t.Fatalf("expected one audio content part, got %#v", canon.Messages)
	}
	if got := canon.Messages[0].Parts[0].Type; got != "input_audio" {
		t.Fatalf("expected input_audio part, got %#v", canon.Messages[0].Parts[0])
	}
	audioRaw, _ := canon.Messages[0].Parts[0].Raw["input_audio"].(map[string]any)
	if got := audioRaw["format"]; got != "mp3" {
		t.Fatalf("expected audio format preserved, got %#v", canon.Messages[0].Parts[0])
	}
}

func TestDecodeRequestHoistsInstructionMessages(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"messages":[
			{"role":"system","content":"system one"},
			{"role":"developer","content":[{"type":"text","text":"developer two"}]},
			{"role":"user","content":"hello"}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if canon.Instructions != "system one\n\ndeveloper two" {
		t.Fatalf("expected instruction messages hoisted, got %q", canon.Instructions)
	}
	if len(canon.Messages) != 1 || canon.Messages[0].Role != "user" {
		t.Fatalf("expected only user message to remain, got %#v", canon.Messages)
	}
}

func TestDecodeRequestMapsServiceTierAliasToServiceTierSnakeCase(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"serviceTier":"priority",
		"messages":[{"role":"user","content":"hello"}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if got, _ := canon.PreservedTopLevelFields["service_tier"].(string); got != "priority" {
		t.Fatalf("expected service_tier mapped from serviceTier alias, got %#v", canon.PreservedTopLevelFields)
	}
	if _, exists := canon.PreservedTopLevelFields["serviceTier"]; exists {
		t.Fatalf("expected serviceTier alias to be removed, got %#v", canon.PreservedTopLevelFields)
	}
}

func TestDecodeRequestServiceTierSnakeCaseTakesPrecedenceOverAlias(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"service_tier":"flex",
		"serviceTier":"priority",
		"messages":[{"role":"user","content":"hello"}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if got, _ := canon.PreservedTopLevelFields["service_tier"].(string); got != "flex" {
		t.Fatalf("expected explicit service_tier to win over alias, got %#v", canon.PreservedTopLevelFields)
	}
	if _, exists := canon.PreservedTopLevelFields["serviceTier"]; exists {
		t.Fatalf("expected serviceTier alias to be removed, got %#v", canon.PreservedTopLevelFields)
	}
}
