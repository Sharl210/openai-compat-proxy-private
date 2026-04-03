package httpapi

import (
	"net/http/httptest"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestProviderConfigForRequestCarriesProviderUpstreamEndpointType(t *testing.T) {
	snapshot := &config.RuntimeSnapshot{Config: config.Config{
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyProxyBuffer,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			UpstreamBaseURL:      "https://example.com/v1",
			UpstreamAPIKey:       "provider-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic,
		}},
	}}

	req := httptest.NewRequest("GET", "/openai/v1/responses", nil)
	req = req.Clone(withRuntimeSnapshot(withRouteInfo(req.Context(), routeInfo{ProviderID: "openai", CanonicalPath: "/v1/responses"}), snapshot))

	providerCfg := providerConfigForRequest(req)
	if providerCfg.UpstreamEndpointType != config.UpstreamEndpointTypeAnthropic {
		t.Fatalf("expected provider upstream endpoint type %q, got %q", config.UpstreamEndpointTypeAnthropic, providerCfg.UpstreamEndpointType)
	}
}

func TestProviderConfigForRequestCarriesProviderClaudeInjectionOverrides(t *testing.T) {
	snapshot := &config.RuntimeSnapshot{Config: config.Config{
		InjectClaudeCodeMetadataUserID: false,
		InjectClaudeCodeSystemPrompt:   false,
		Providers: []config.ProviderConfig{{
			ID:                                "openai",
			Enabled:                           true,
			InjectClaudeCodeMetadataUserID:    true,
			InjectClaudeCodeMetadataUserIDSet: true,
			InjectClaudeCodeSystemPrompt:      true,
			InjectClaudeCodeSystemPromptSet:   true,
			UpstreamEndpointType:              config.UpstreamEndpointTypeAnthropic,
		}},
	}}

	req := httptest.NewRequest("GET", "/openai/v1/responses", nil)
	req = req.Clone(withRuntimeSnapshot(withRouteInfo(req.Context(), routeInfo{ProviderID: "openai", CanonicalPath: "/v1/responses"}), snapshot))

	providerCfg := providerConfigForRequest(req)
	if !providerCfg.InjectClaudeCodeMetadataUserID {
		t.Fatalf("expected provider metadata injection override to be applied")
	}
	if !providerCfg.InjectClaudeCodeSystemPrompt {
		t.Fatalf("expected provider system prompt injection override to be applied")
	}
}
