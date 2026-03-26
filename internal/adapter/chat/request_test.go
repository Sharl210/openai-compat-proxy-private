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
