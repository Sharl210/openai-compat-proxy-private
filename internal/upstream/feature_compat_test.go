package upstream

import (
	"encoding/json"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
)

func TestCheckResponsesFeatureCompatibilityRejectsSemanticFeaturesOutsideResponses(t *testing.T) {
	tests := []struct {
		name string
		req  model.CanonicalRequest
	}{
		{
			name: "programmatic tool calling",
			req:  model.CanonicalRequest{Tools: []model.CanonicalTool{{Type: "programmatic_tool_calling"}}},
		},
		{
			name: "multi agent",
			req:  model.CanonicalRequest{ResponseMultiAgent: json.RawMessage(`{"enabled":true}`)},
		},
		{
			name: "persisted reasoning item",
			req: model.CanonicalRequest{ResponseInputItems: []map[string]any{{
				"type":              "reasoning",
				"encrypted_content": "opaque",
				"phase":             "analysis",
			}}},
		},
		{
			name: "persisted reasoning include",
			req:  model.CanonicalRequest{ResponseInclude: []string{"reasoning.encrypted_content"}},
		},
		{
			name: "reasoning context",
			req:  model.CanonicalRequest{Reasoning: &model.CanonicalReasoning{Raw: map[string]any{"context": "opaque"}}},
		},
		{
			name: "prompt cache controls",
			req:  model.CanonicalRequest{ResponsePromptCacheKey: json.RawMessage(`"stable-key"`)},
		},
		{
			name: "original image detail",
			req: model.CanonicalRequest{Messages: []model.CanonicalMessage{{Parts: []model.CanonicalContentPart{{
				Type: "input_image",
				Raw:  map[string]any{"image_url": map[string]any{"url": "https://example.com/image.png", "detail": "original"}},
			}}}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, endpoint := range []string{
				config.UpstreamEndpointTypeResponses,
				config.UpstreamEndpointTypeChat,
				config.UpstreamEndpointTypeAnthropic,
			} {
				t.Run(endpoint, func(t *testing.T) {
					err := CheckResponsesFeatureCompatibility(tt.req, endpoint)
					if endpoint == config.UpstreamEndpointTypeResponses && err != nil {
						t.Fatalf("Responses upstream unexpectedly rejected %s: %v", tt.name, err)
					}
					if endpoint != config.UpstreamEndpointTypeResponses && err == nil {
						t.Fatalf("%s upstream unexpectedly accepted %s", endpoint, tt.name)
					}
				})
			}
		})
	}
}

func TestClassifyResponsesFeatureCompatibilityDistinguishesAllowMapAndReject(t *testing.T) {
	tests := []struct {
		name     string
		req      model.CanonicalRequest
		endpoint string
		want     ResponsesFeatureCompatibilityDecision
	}{
		{
			name:     "responses native feature is allowed",
			req:      model.CanonicalRequest{Tools: []model.CanonicalTool{{Type: "programmatic_tool_calling"}}},
			endpoint: config.UpstreamEndpointTypeResponses,
			want:     ResponsesFeatureCompatibilityDecisionAllow,
		},
		{
			name:     "reasoning effort is mapped to chat",
			req:      model.CanonicalRequest{Reasoning: &model.CanonicalReasoning{Effort: "high"}},
			endpoint: config.UpstreamEndpointTypeChat,
			want:     ResponsesFeatureCompatibilityDecisionMap,
		},
		{
			name:     "programmatic tool calling is rejected by chat",
			req:      model.CanonicalRequest{Tools: []model.CanonicalTool{{Type: "programmatic_tool_calling"}}},
			endpoint: config.UpstreamEndpointTypeChat,
			want:     ResponsesFeatureCompatibilityDecisionReject,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyResponsesFeatureCompatibility(tt.req, tt.endpoint)
			if got.Decision != tt.want {
				t.Fatalf("expected %s, got %#v", tt.want, got)
			}
		})
	}
}
