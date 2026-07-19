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

func TestCheckResponsesFeatureCompatibilityDropsEncryptedReasoningIncludeOutsideResponses(t *testing.T) {
	req := model.CanonicalRequest{ResponseInclude: []string{"reasoning.encrypted_content"}}
	for _, endpoint := range []string{config.UpstreamEndpointTypeChat, config.UpstreamEndpointTypeAnthropic} {
		if err := CheckResponsesFeatureCompatibility(req, endpoint); err != nil {
			t.Fatalf("%s upstream should ignore unavailable encrypted reasoning output: %v", endpoint, err)
		}
	}
}

func TestCheckResponsesFeatureCompatibilityAllowsRepresentablePersistedReasoning(t *testing.T) {
	chatReasoning := model.CanonicalRequest{ResponseInputItems: []map[string]any{{
		"type":    "reasoning",
		"phase":   "analysis",
		"summary": []any{map[string]any{"type": "summary_text", "text": "need tool output"}},
	}}}
	if err := CheckResponsesFeatureCompatibility(chatReasoning, config.UpstreamEndpointTypeChat); err != nil {
		t.Fatalf("chat upstream should accept representable reasoning: %v", err)
	}

	anthropicReasoning := model.CanonicalRequest{ResponseInputItems: []map[string]any{{
		"type":     "reasoning",
		"phase":    "analysis",
		"thinking": "need tool output",
	}}}
	if err := CheckResponsesFeatureCompatibility(anthropicReasoning, config.UpstreamEndpointTypeAnthropic); err != nil {
		t.Fatalf("anthropic upstream should accept plaintext replayable reasoning: %v", err)
	}
}

func TestCheckResponsesFeatureCompatibilityRejectsNonRepresentablePersistedReasoning(t *testing.T) {
	for _, testCase := range []struct {
		name string
		item map[string]any
	}{
		{
			name: "opaque only",
			item: map[string]any{
				"type":              "reasoning",
				"phase":             "analysis",
				"encrypted_content": "opaque",
			},
		},
		{
			name: "plaintext plus opaque state",
			item: map[string]any{
				"type":              "reasoning",
				"phase":             "analysis",
				"summary":           []any{map[string]any{"type": "summary_text", "text": "replayable text"}},
				"encrypted_content": "opaque",
			},
		},
		{
			name: "thinking plus opaque state without provenance",
			item: map[string]any{
				"type":              "reasoning",
				"thinking":          "replayable text",
				"encrypted_content": "opaque",
			},
		},
		{
			name: "thinking plus signature without encrypted content",
			item: map[string]any{
				"type":      "reasoning",
				"thinking":  "replayable text",
				"signature": "sig_123",
			},
		},
		{
			name: "thinking plus empty signature",
			item: map[string]any{
				"type":      "reasoning",
				"thinking":  "replayable text",
				"signature": "",
			},
		},
		{
			name: "thinking plus non string encrypted content",
			item: map[string]any{
				"type":              "reasoning",
				"thinking":          "replayable text",
				"encrypted_content": map[string]any{"opaque": true},
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			req := model.CanonicalRequest{ResponseInputItems: []map[string]any{testCase.item}}
			for _, endpoint := range []string{config.UpstreamEndpointTypeChat, config.UpstreamEndpointTypeAnthropic} {
				if err := CheckResponsesFeatureCompatibility(req, endpoint); err == nil {
					t.Fatalf("%s upstream should reject %s persisted reasoning", endpoint, testCase.name)
				}
			}
		})
	}
}

func TestCheckResponsesFeatureCompatibilityRejectsMatchingOpaqueThinkingSignature(t *testing.T) {
	req := model.CanonicalRequest{ResponseInputItems: []map[string]any{{
		"type":              "reasoning",
		"thinking":          "native thinking",
		"encrypted_content": "sig_123",
		"signature":         "sig_123",
	}}}
	for _, endpoint := range []string{config.UpstreamEndpointTypeChat, config.UpstreamEndpointTypeAnthropic} {
		if err := CheckResponsesFeatureCompatibility(req, endpoint); err == nil {
			t.Fatalf("%s upstream should reject client-supplied opaque thinking signature replay", endpoint)
		}
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
