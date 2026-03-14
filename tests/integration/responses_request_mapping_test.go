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
