package integration_test

import (
	"strings"
	"testing"

	responsesadapter "openai-compat-proxy/internal/adapter/responses"
)

func TestResponsesRequestMapsToolsReasoningAndImageInput(t *testing.T) {
	body := `{
		"model":"gpt-x",
		"stream":false,
		"input":[{"role":"user","content":[{"type":"input_text","text":"describe"},{"type":"input_image","image_url":"https://example.com/a.png"}]}],
		"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],
		"reasoning":{"effort":"high"}
	}`

	canon, err := responsesadapter.DecodeRequest(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(canon.Messages) != 1 || len(canon.Tools) != 1 || canon.Reasoning.Effort != "high" {
		t.Fatal("expected mapped responses request")
	}
}

func TestResponsesRequestSortsToolsByNameForStableReplay(t *testing.T) {
	body := `{
		"model":"gpt-x",
		"stream":false,
		"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],
		"tools":[
			{"type":"function","name":"zeta","parameters":{"type":"object"}},
			{"type":"function","name":"alpha","parameters":{"type":"object"}}
		]
	}`

	canon, err := responsesadapter.DecodeRequest(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(canon.Tools) != 2 || canon.Tools[0].Name != "alpha" || canon.Tools[1].Name != "zeta" {
		t.Fatalf("expected responses tools sorted by name, got %#v", canon.Tools)
	}
}

func TestResponsesRequestPreservesReasoningRawAndDefaultSummary(t *testing.T) {
	body := `{
		"model":"gpt-x",
		"stream":false,
		"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],
		"reasoning":{"effort":"high","extra":"keep-me"}
	}`

	canon, err := responsesadapter.DecodeRequest(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if canon.Reasoning == nil {
		t.Fatal("expected reasoning")
	}
	if canon.Reasoning.Summary != "auto" {
		t.Fatalf("expected default summary auto, got %#v", canon.Reasoning)
	}
	if canon.Reasoning.Raw["extra"] != "keep-me" {
		t.Fatalf("expected raw reasoning fields preserved, got %#v", canon.Reasoning.Raw)
	}
}

func TestResponsesRequestAcceptsStringContentAndUndefinedOptionals(t *testing.T) {
	body := `{
		"model":"gpt-5.4",
		"input":[
			{"role":"developer","content":"You are helpful."},
			{"role":"user","content":[{"type":"input_text","text":"hi"}]}
		],
		"temperature":"[undefined]",
		"top_p":"[undefined]",
		"max_output_tokens":"[undefined]",
		"instructions":"[undefined]",
		"user":"[undefined]",
		"stream":true
	}`

	canon, err := responsesadapter.DecodeRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("expected request to decode, got error: %v", err)
	}

	if len(canon.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(canon.Messages))
	}
	if canon.Messages[0].Role != "developer" {
		t.Fatalf("expected first role developer, got %q", canon.Messages[0].Role)
	}
	if len(canon.Messages[0].Parts) != 1 || canon.Messages[0].Parts[0].Text != "You are helpful." {
		t.Fatalf("expected developer string content to map to one text part, got %#v", canon.Messages[0].Parts)
	}
	if canon.Temperature != nil || canon.TopP != nil || canon.MaxOutputTokens != nil {
		t.Fatalf("expected undefined optional numeric fields to be ignored, got temp=%v top_p=%v max_output=%v", canon.Temperature, canon.TopP, canon.MaxOutputTokens)
	}
	if canon.Instructions != "" {
		t.Fatalf("expected undefined instructions to be ignored, got %q", canon.Instructions)
	}
}

func TestResponsesRequestPreservesToolLoopHistory(t *testing.T) {
	body := `{
		"model":"gpt-5.4",
		"input":[
			{
				"role":"assistant",
				"content":"",
				"tool_calls":[
					{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Shanghai\"}"}}
				]
			},
			{
				"role":"tool",
				"tool_call_id":"call_1",
				"content":"{\"temp\":25}"
			}
		],
		"stream":false
	}`

	canon, err := responsesadapter.DecodeRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("expected request to decode, got error: %v", err)
	}
	if len(canon.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(canon.Messages))
	}
	if len(canon.Messages[0].ToolCalls) != 1 || canon.Messages[0].ToolCalls[0].Name != "get_weather" {
		t.Fatalf("expected assistant tool call history preserved, got %#v", canon.Messages[0].ToolCalls)
	}
	if canon.Messages[1].Role != "tool" || canon.Messages[1].ToolCallID != "call_1" {
		t.Fatalf("expected tool message replay preserved, got %#v", canon.Messages[1])
	}
}

func TestResponsesRequestPreservesAssistantTextAlongsideToolCalls(t *testing.T) {
	body := `{
		"model":"gpt-5.4",
		"input":[
			{
				"role":"assistant",
				"content":"我先查一下。",
				"tool_calls":[{"id":"call_1","type":"function","function":{"name":"search_web","arguments":"{\"query\":\"上海天气\"}"}}]
			}
		],
		"stream":false
	}`

	canon, err := responsesadapter.DecodeRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("expected request to decode, got error: %v", err)
	}
	if len(canon.Messages) != 1 {
		t.Fatalf("expected one message, got %#v", canon.Messages)
	}
	if len(canon.Messages[0].Parts) != 1 || canon.Messages[0].Parts[0].Text != "我先查一下。" {
		t.Fatalf("expected assistant text preserved, got %#v", canon.Messages[0])
	}
	if len(canon.Messages[0].ToolCalls) != 1 || canon.Messages[0].ToolCalls[0].ID != "call_1" {
		t.Fatalf("expected assistant tool call preserved, got %#v", canon.Messages[0])
	}
}
