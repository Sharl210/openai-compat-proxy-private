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
