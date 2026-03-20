package integration_test

import (
	"strings"
	"testing"

	anthropicadapter "openai-compat-proxy/internal/adapter/anthropic"
)

func TestAnthropicRequestAcceptsSystemArrayAndUndefinedOptionals(t *testing.T) {
	body := `{
		"model":"gpt-5.4",
		"max_tokens":4096,
		"temperature":"[undefined]",
		"top_k":"[undefined]",
		"top_p":"[undefined]",
		"stop_sequences":"[undefined]",
		"system":[{"type":"text","text":"You are helpful.","cache_control":"[undefined]"}],
		"messages":[{"role":"user","content":[{"type":"text","text":"你好","cache_control":"[undefined]"}]}],
		"tool_choice":{"type":"auto","disable_parallel_tool_use":"[undefined]"},
		"stream":"[undefined]"
	}`

	canon, err := anthropicadapter.DecodeRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("expected anthropic request to decode, got error: %v", err)
	}
	if canon.Instructions != "You are helpful." {
		t.Fatalf("expected system array text to map into instructions, got %q", canon.Instructions)
	}
	if canon.Stream {
		t.Fatal("expected undefined stream to be treated as false")
	}
	if canon.ToolChoice.Mode != "auto" {
		t.Fatalf("expected tool_choice auto, got %#v", canon.ToolChoice)
	}
	if len(canon.Messages) != 1 || canon.Messages[0].Parts[0].Text != "你好" {
		t.Fatalf("expected user content preserved, got %#v", canon.Messages)
	}
}
