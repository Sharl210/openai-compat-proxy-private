package integration_test

import (
	"strings"
	"testing"

	chatadapter "openai-compat-proxy/internal/adapter/chat"
)

func TestChatRequestMapsImageToolsAndReasoning(t *testing.T) {
	body := `{
		"model":"gpt-x",
		"stream":false,
		"messages":[{"role":"user","content":[{"type":"text","text":"what is in this image"},{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}],
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],
		"reasoning_effort":"medium"
	}`

	canon, err := chatadapter.DecodeRequest(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(canon.Messages[0].Parts) != 2 || len(canon.Tools) != 1 || canon.Reasoning == nil {
		t.Fatal("expected mapped multimodal, tools, and reasoning")
	}
}

func TestChatRequestMapsReasoningObject(t *testing.T) {
	body := `{
		"model":"gpt-x",
		"stream":false,
		"messages":[{"role":"user","content":"hi"}],
		"reasoning":{"effort":"high","summary":"auto"}
	}`

	canon, err := chatadapter.DecodeRequest(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if canon.Reasoning == nil || canon.Reasoning.Effort != "high" || canon.Reasoning.Summary != "auto" {
		t.Fatalf("expected reasoning object to map through, got %#v", canon.Reasoning)
	}
}

func TestChatRequestMapsReasoningEffortToDefaultSummaryAuto(t *testing.T) {
	body := `{
		"model":"gpt-x",
		"stream":false,
		"messages":[{"role":"user","content":"hi"}],
		"reasoning_effort":"high"
	}`

	canon, err := chatadapter.DecodeRequest(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if canon.Reasoning == nil || canon.Reasoning.Effort != "high" || canon.Reasoning.Summary != "auto" {
		t.Fatalf("expected reasoning_effort to map to effort+default summary auto, got %#v", canon.Reasoning)
	}
}

func TestChatRequestPreservesToolLoopHistory(t *testing.T) {
	body := `{
		"model":"gpt-x",
		"stream":true,
		"messages":[
			{"role":"assistant","reasoning_content":"正在调用工具…\n","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"search_web","arguments":"{\"query\":\"桂林天气\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"{\"result\":\"晴\"}"}
		]
	}`

	canon, err := chatadapter.DecodeRequest(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(canon.Messages) != 2 {
		t.Fatalf("expected two messages, got %#v", canon.Messages)
	}
	if len(canon.Messages[0].ToolCalls) != 1 || canon.Messages[0].ToolCalls[0].ID != "call_1" {
		t.Fatalf("expected assistant tool call history, got %#v", canon.Messages[0])
	}
	if canon.Messages[0].ReasoningContent != "正在调用工具…\n" {
		t.Fatalf("expected assistant reasoning content history, got %#v", canon.Messages[0].ReasoningContent)
	}
	if canon.Messages[1].ToolCallID != "call_1" {
		t.Fatalf("expected tool_call_id on tool message, got %#v", canon.Messages[1])
	}
}
