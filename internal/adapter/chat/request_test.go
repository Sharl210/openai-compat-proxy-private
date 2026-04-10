package chat

import (
	"strings"
	"testing"
)

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
