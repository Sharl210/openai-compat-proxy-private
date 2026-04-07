package httpapi

import (
	"net/http/httptest"
	"reflect"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestCanonicalV1PathsMatchSupportedPublicRoutes(t *testing.T) {
	want := []string{
		canonicalV1ModelsPath,
		canonicalV1ResponsesPath,
		canonicalV1ResponsesCompactPath,
		canonicalV1ChatCompletionsPath,
		canonicalV1MessagesPath,
	}
	if got := canonicalV1Paths(); !reflect.DeepEqual(got, want) {
		t.Fatalf("expected canonical v1 paths %v, got %v", want, got)
	}
	for _, path := range want {
		if !isCanonicalV1Path(path) {
			t.Fatalf("expected %q to be recognized as a canonical v1 path", path)
		}
	}
	if isCanonicalV1Path("/v1/unknown") {
		t.Fatal("expected unknown path to be rejected from canonical v1 path set")
	}
}

func TestResponsesCompactRouteResolvesForLegacyAndProviderPaths(t *testing.T) {
	cfg := config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:      "openai",
			Enabled: true,
		}},
	}

	t.Run("legacy", func(t *testing.T) {
		info, err := resolveRouteInfo("/v1/responses/compact", cfg)
		if err != nil {
			t.Fatalf("expected legacy compact route to resolve, got error: %v", err)
		}
		if !info.Legacy {
			t.Fatalf("expected legacy route info")
		}
		if info.ProviderID != "openai" {
			t.Fatalf("expected provider openai, got %q", info.ProviderID)
		}
		if info.CanonicalPath != "/v1/responses/compact" {
			t.Fatalf("expected canonical compact path, got %q", info.CanonicalPath)
		}
	})

	t.Run("provider", func(t *testing.T) {
		info, err := resolveRouteInfo("/openai/v1/responses/compact", cfg)
		if err != nil {
			t.Fatalf("expected provider compact route to resolve, got error: %v", err)
		}
		if info.Legacy {
			t.Fatalf("expected explicit provider route to be non-legacy")
		}
		if info.ProviderID != "openai" {
			t.Fatalf("expected provider openai, got %q", info.ProviderID)
		}
		if info.CanonicalPath != "/v1/responses/compact" {
			t.Fatalf("expected canonical compact path, got %q", info.CanonicalPath)
		}
	})
}

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
