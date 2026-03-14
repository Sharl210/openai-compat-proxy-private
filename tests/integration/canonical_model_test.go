package integration_test

import (
	"testing"

	"openai-compat-proxy/internal/model"
)

func TestCanonicalModelSupportsTextImageToolAndReasoning(t *testing.T) {
	req := model.CanonicalRequest{
		Model: "gpt-x",
		Messages: []model.CanonicalMessage{{
			Role: "user",
			Parts: []model.CanonicalContentPart{
				{Type: "text", Text: "describe"},
				{Type: "image_url", ImageURL: "https://example.com/a.png"},
			},
		}},
		Tools:     []model.CanonicalTool{{Type: "function", Name: "lookup"}},
		Reasoning: &model.CanonicalReasoning{Effort: "medium"},
	}

	if len(req.Messages[0].Parts) != 2 || req.Tools[0].Name != "lookup" || req.Reasoning.Effort != "medium" {
		t.Fatal("canonical model missing expected capability")
	}
}
