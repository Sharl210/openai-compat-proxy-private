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
