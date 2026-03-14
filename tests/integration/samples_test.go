package integration_test

import "openai-compat-proxy/internal/model"

func sampleCanonicalRequest() model.CanonicalRequest {
	return model.CanonicalRequest{
		Model:  "gpt-x",
		Stream: true,
		Messages: []model.CanonicalMessage{{
			Role:  "user",
			Parts: []model.CanonicalContentPart{{Type: "text", Text: "hi"}},
		}},
	}
}
