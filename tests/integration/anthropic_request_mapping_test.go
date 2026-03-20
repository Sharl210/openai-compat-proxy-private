package integration_test

import (
	"strings"
	"testing"

	anthropicadapter "openai-compat-proxy/internal/adapter/anthropic"
)

func TestAnthropicRequestPreservesToolLoopHistory(t *testing.T) {
	body := `{
		"model":"claude-sonnet",
		"messages":[
			{
				"role":"assistant",
				"content":[
					{"type":"tool_use","id":"call_1","name":"get_weather","input":{"city":"Shanghai"}}
				]
			},
			{
				"role":"user",
				"content":[
					{"type":"tool_result","tool_use_id":"call_1","content":"{\"temp\":25}"}
				]
			}
		],
		"stream":false
	}`

	canon, err := anthropicadapter.DecodeRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("expected request to decode, got error: %v", err)
	}
	if len(canon.Messages) != 2 {
		t.Fatalf("expected 2 canonical messages, got %d", len(canon.Messages))
	}
	if len(canon.Messages[0].ToolCalls) != 1 || canon.Messages[0].ToolCalls[0].Name != "get_weather" {
		t.Fatalf("expected assistant tool_use to map into canonical tool call, got %#v", canon.Messages[0].ToolCalls)
	}
	if canon.Messages[1].Role != "tool" || canon.Messages[1].ToolCallID != "call_1" {
		t.Fatalf("expected tool_result to map into tool replay message, got %#v", canon.Messages[1])
	}
	if len(canon.Messages[1].Parts) != 1 || canon.Messages[1].Parts[0].Text != `{"temp":25}` {
		t.Fatalf("expected tool_result content preserved, got %#v", canon.Messages[1].Parts)
	}
}

func TestAnthropicRequestPreservesInterleavedTextAndToolResultInSameMessage(t *testing.T) {
	body := `{
		"model":"claude-sonnet",
		"messages":[
			{
				"role":"user",
				"content":[
					{"type":"text","text":"工具结果如下："},
					{"type":"tool_result","tool_use_id":"call_1","content":"{\"temp\":25}"}
				]
			}
		],
		"stream":false
	}`

	canon, err := anthropicadapter.DecodeRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("expected request to decode, got error: %v", err)
	}
	if len(canon.Messages) != 2 {
		t.Fatalf("expected text message plus tool replay, got %#v", canon.Messages)
	}
	if canon.Messages[0].Role != "user" || len(canon.Messages[0].Parts) != 1 || canon.Messages[0].Parts[0].Text != "工具结果如下：" {
		t.Fatalf("expected leading user text to be preserved, got %#v", canon.Messages[0])
	}
	if canon.Messages[1].Role != "tool" || canon.Messages[1].ToolCallID != "call_1" {
		t.Fatalf("expected trailing tool replay message, got %#v", canon.Messages[1])
	}
}
