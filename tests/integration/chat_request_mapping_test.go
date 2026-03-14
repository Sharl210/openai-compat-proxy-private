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
