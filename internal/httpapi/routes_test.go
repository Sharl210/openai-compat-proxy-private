package httpapi

import (
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
)

func TestCanonicalV1PathsMatchSupportedPublicRoutes(t *testing.T) {
	want := []string{
		canonicalV1ModelsPath,
		canonicalV1ResponsesPath,
		canonicalV1ResponsesCompactPath,
		canonicalV1ChatCompletionsPath,
		canonicalV1MessagesPath,
		canonicalV1ImagesGenerationsPath,
		canonicalV1ImagesEditsPath,
		canonicalV1ImagesVariationsPath,
		canonicalV1EmbeddingsPath,
		canonicalV1RerankPath,
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

func TestRouteAliasesResolveToCanonicalV1Paths(t *testing.T) {
	cfg := config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:      "openai",
			Enabled: true,
		}},
	}

	tests := []struct {
		name         string
		path         string
		wantProvider string
		wantLegacy   bool
		wantPath     string
	}{
		{name: "bare models alias", path: "/models", wantProvider: "openai", wantLegacy: true, wantPath: canonicalV1ModelsPath},
		{name: "bare responses alias", path: "/responses", wantProvider: "openai", wantLegacy: true, wantPath: canonicalV1ResponsesPath},
		{name: "bare compact alias", path: "/responses/compact", wantProvider: "openai", wantLegacy: true, wantPath: canonicalV1ResponsesCompactPath},
		{name: "bare chat alias", path: "/chat/completions", wantProvider: "openai", wantLegacy: true, wantPath: canonicalV1ChatCompletionsPath},
		{name: "bare messages alias", path: "/messages", wantProvider: "openai", wantLegacy: true, wantPath: canonicalV1MessagesPath},
		{name: "bare images generations alias", path: "/images/generations", wantProvider: "openai", wantLegacy: true, wantPath: canonicalV1ImagesGenerationsPath},
		{name: "bare images edits alias", path: "/images/edits", wantProvider: "openai", wantLegacy: true, wantPath: canonicalV1ImagesEditsPath},
		{name: "bare images variations alias", path: "/images/variations", wantProvider: "openai", wantLegacy: true, wantPath: canonicalV1ImagesVariationsPath},
		{name: "bare embeddings alias", path: "/embeddings", wantProvider: "openai", wantLegacy: true, wantPath: canonicalV1EmbeddingsPath},
		{name: "bare rerank alias", path: "/rerank", wantProvider: "openai", wantLegacy: true, wantPath: canonicalV1RerankPath},
		{name: "provider models alias", path: "/openai/models", wantProvider: "openai", wantLegacy: false, wantPath: canonicalV1ModelsPath},
		{name: "provider responses alias", path: "/openai/responses", wantProvider: "openai", wantLegacy: false, wantPath: canonicalV1ResponsesPath},
		{name: "provider compact alias", path: "/openai/responses/compact", wantProvider: "openai", wantLegacy: false, wantPath: canonicalV1ResponsesCompactPath},
		{name: "provider chat alias", path: "/openai/chat/completions", wantProvider: "openai", wantLegacy: false, wantPath: canonicalV1ChatCompletionsPath},
		{name: "provider messages alias", path: "/openai/messages", wantProvider: "openai", wantLegacy: false, wantPath: canonicalV1MessagesPath},
		{name: "provider images generations alias", path: "/openai/images/generations", wantProvider: "openai", wantLegacy: false, wantPath: canonicalV1ImagesGenerationsPath},
		{name: "provider images edits alias", path: "/openai/images/edits", wantProvider: "openai", wantLegacy: false, wantPath: canonicalV1ImagesEditsPath},
		{name: "provider images variations alias", path: "/openai/images/variations", wantProvider: "openai", wantLegacy: false, wantPath: canonicalV1ImagesVariationsPath},
		{name: "provider embeddings alias", path: "/openai/embeddings", wantProvider: "openai", wantLegacy: false, wantPath: canonicalV1EmbeddingsPath},
		{name: "provider rerank alias", path: "/openai/rerank", wantProvider: "openai", wantLegacy: false, wantPath: canonicalV1RerankPath},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info, err := resolveRouteInfo(tc.path, cfg)
			if err != nil {
				t.Fatalf("expected %q to resolve, got error: %v", tc.path, err)
			}
			if info.ProviderID != tc.wantProvider {
				t.Fatalf("expected provider %q, got %q", tc.wantProvider, info.ProviderID)
			}
			if info.Legacy != tc.wantLegacy {
				t.Fatalf("expected legacy=%v, got %v", tc.wantLegacy, info.Legacy)
			}
			if info.CanonicalPath != tc.wantPath {
				t.Fatalf("expected canonical path %q, got %q", tc.wantPath, info.CanonicalPath)
			}
		})
	}
}

func TestRouteAliasesRespectLegacyToggle(t *testing.T) {
	cfg := config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: false,
		Providers: []config.ProviderConfig{{
			ID:      "openai",
			Enabled: true,
		}},
	}

	for _, path := range []string{"/models", "/responses", "/responses/compact", "/chat/completions", "/messages", "/images/generations", "/images/edits", "/images/variations", "/embeddings", "/rerank"} {
		t.Run(path, func(t *testing.T) {
			if _, err := resolveRouteInfo(path, cfg); err == nil {
				t.Fatalf("expected %q to be rejected when legacy v1 routes are disabled", path)
			}
		})
	}
}

func TestRouteAliasesDoNotCaptureUnrelatedPaths(t *testing.T) {
	cfg := config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:      "openai",
			Enabled: true,
		}},
	}

	for _, path := range []string{"/healthz", "/_admin/assets/app.js", "/_admin/api/config", "/v1/unknown", "/openai/unknown"} {
		t.Run(path, func(t *testing.T) {
			if _, err := resolveRouteInfo(path, cfg); err == nil {
				t.Fatalf("expected unrelated path %q to stay unresolved", path)
			}
		})
	}
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

func TestProviderConfigForRequestCarriesProviderResponsesToolCompatMode(t *testing.T) {
	snapshot := &config.RuntimeSnapshot{Config: config.Config{
		ResponsesToolCompatMode: config.ResponsesToolCompatModePreserve,
		Providers: []config.ProviderConfig{{
			ID:                      "openai",
			Enabled:                 true,
			ResponsesToolCompatMode: config.ResponsesToolCompatModeFunctionOnly,
		}},
	}}

	req := httptest.NewRequest("GET", "/openai/v1/responses", nil)
	req = req.Clone(withRuntimeSnapshot(withRouteInfo(req.Context(), routeInfo{ProviderID: "openai", CanonicalPath: "/v1/responses"}), snapshot))

	providerCfg := providerConfigForRequest(req)
	if providerCfg.ResponsesToolCompatMode != config.ResponsesToolCompatModeFunctionOnly {
		t.Fatalf("expected provider responses tool compat mode %q, got %q", config.ResponsesToolCompatModeFunctionOnly, providerCfg.ResponsesToolCompatMode)
	}
}

func TestLegacyImageRouteRefreshesDefaultProviderSelectionAfterModelChange(t *testing.T) {
	store := config.NewStaticRuntimeStore(config.Config{
		DefaultProvider:      "p1,p2",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:             "p1",
			Enabled:        true,
			SupportsModels: true,
			ManualModels:   []string{"model-a"},
		}, {
			ID:             "p2",
			Enabled:        true,
			SupportsModels: true,
			ManualModels:   []string{"model-b"},
		}},
	})
	server := NewServerWithStore(store, nil, nil)
	store.Active().Config.Providers[0].ManualModels = []string{"model-a", "model-new"}
	refreshDefaultProviderOverlayCache(store, time.Now())

	req := httptest.NewRequest("POST", "/v1/images/generations", nil)
	req = req.Clone(withRuntimeSnapshot(withRouteInfo(req.Context(), routeInfo{ProviderID: "p2", Legacy: true, CanonicalPath: canonicalV1ImagesGenerationsPath}), store.Active()))
	provider, _, providerID, resolvedModel, ok, err := providerSelectionForModelRequest(req, "model-new")
	if err != nil {
		t.Fatalf("expected refreshed provider selection without error, got %v", err)
	}
	if !ok {
		t.Fatalf("expected refreshed provider selection to succeed")
	}
	if providerID != "p1" || provider.ID != "p1" || resolvedModel != "model-new" {
		t.Fatalf("expected refreshed owner p1 for model-new, got providerID=%q provider=%q model=%q", providerID, provider.ID, resolvedModel)
	}
	_ = server
}

func TestProviderSelectionForLegacyModelMapAliasRequiresVisibleModel(t *testing.T) {
	store := config.NewStaticRuntimeStore(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:             "openai",
			Enabled:        true,
			ModelMap:       []config.ModelMapEntry{{Key: "client-alias", Target: "upstream-real"}},
			ManualModels:   []string{"listed-model"},
			SupportsModels: true,
		}},
	})
	server := NewServerWithStore(store, nil, nil)

	if containsString(store.Active().DefaultVisibleModels, "client-alias") {
		t.Fatalf("expected MODEL_MAP alias to stay out of default visible models, got %v", store.Active().DefaultVisibleModels)
	}
	req := httptest.NewRequest("POST", "/v1/responses", nil)
	req = req.Clone(withRuntimeSnapshot(withRouteInfo(req.Context(), routeInfo{ProviderID: "openai", Legacy: true, CanonicalPath: canonicalV1ResponsesPath}), store.Active()))
	_, _, _, _, ok, err := providerSelectionForModelRequest(req, "client-alias")
	if err != nil {
		t.Fatalf("expected provider selection without error, got %v", err)
	}
	if ok {
		t.Fatalf("expected hidden MODEL_MAP alias provider selection to fail until the model is added manually")
	}
	_ = server
}

func TestProviderSelectionForTaggedLegacyModelMapAliasRequiresVisibleModel(t *testing.T) {
	store := config.NewStaticRuntimeStore(config.Config{
		DefaultProvider:                   "openai",
		EnableLegacyV1Routes:              true,
		EnableDefaultProviderModelTags:    true,
		EnableAllDefaultProviderModelTags: true,
		Providers: []config.ProviderConfig{{
			ID:           "openai",
			Enabled:      true,
			ModelMap:     []config.ModelMapEntry{{Key: "client-alias", Target: "upstream-real"}},
			ManualModels: []string{"listed-model"},
		}},
	})
	server := NewServerWithStore(store, nil, nil)

	if containsString(store.Active().DefaultTaggedVisibleModels, "[openai]client-alias") {
		t.Fatalf("expected MODEL_MAP alias to stay out of tagged visible models, got %v", store.Active().DefaultTaggedVisibleModels)
	}
	req := httptest.NewRequest("POST", "/v1/responses", nil)
	req = req.Clone(withRuntimeSnapshot(withRouteInfo(req.Context(), routeInfo{ProviderID: "openai", Legacy: true, CanonicalPath: canonicalV1ResponsesPath}), store.Active()))
	_, _, _, _, ok, err := providerSelectionForModelRequest(req, "[openai]client-alias")
	if err != nil {
		t.Fatalf("expected provider selection without error, got %v", err)
	}
	if ok {
		t.Fatalf("expected hidden tagged MODEL_MAP alias provider selection to fail until the model is added manually")
	}
	_ = server
}

func TestProviderSelectionForLegacyNoPromptReasoningSuffix(t *testing.T) {
	store := config.NewStaticRuntimeStore(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		EnableNoPromptModelSuffix:   true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			ManualModels:                []string{"gpt-5.5"},
			EnableReasoningEffortSuffix: true,
		}},
	})
	server := NewServerWithStore(store, nil, nil)

	req := httptest.NewRequest("POST", "/v1/responses", nil)
	req = req.Clone(withRuntimeSnapshot(withRouteInfo(req.Context(), routeInfo{ProviderID: "openai", Legacy: true, CanonicalPath: canonicalV1ResponsesPath}), store.Active()))
	provider, _, providerID, resolvedModel, ok, err := providerSelectionForModelRequest(req, "gpt-5.5-low-noprompt")
	if err != nil {
		t.Fatalf("expected provider selection without error, got %v", err)
	}
	if !ok {
		t.Fatalf("expected noprompt reasoning suffix provider selection to succeed")
	}
	if providerID != "openai" || provider.ID != "openai" || resolvedModel != "gpt-5.5-low" {
		t.Fatalf("expected openai/gpt-5.5-low, got providerID=%q provider=%q model=%q", providerID, provider.ID, resolvedModel)
	}
	_ = server
}

func TestProviderSelectionForLegacyNoPromptBeforeReasoningSuffix(t *testing.T) {
	store := config.NewStaticRuntimeStore(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		EnableNoPromptModelSuffix:   true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			ManualModels:                []string{"gpt-5.5"},
			EnableReasoningEffortSuffix: true,
		}},
	})
	server := NewServerWithStore(store, nil, nil)

	req := httptest.NewRequest("POST", "/v1/responses", nil)
	req = req.Clone(withRuntimeSnapshot(withRouteInfo(req.Context(), routeInfo{ProviderID: "openai", Legacy: true, CanonicalPath: canonicalV1ResponsesPath}), store.Active()))
	provider, _, providerID, resolvedModel, ok, err := providerSelectionForModelRequest(req, "gpt-5.5-noprompt-low")
	if err != nil {
		t.Fatalf("expected provider selection without error, got %v", err)
	}
	if !ok {
		t.Fatalf("expected noprompt-before-reasoning suffix provider selection to succeed")
	}
	if providerID != "openai" || provider.ID != "openai" || resolvedModel != "gpt-5.5-low" {
		t.Fatalf("expected openai/gpt-5.5-low, got providerID=%q provider=%q model=%q", providerID, provider.ID, resolvedModel)
	}
	_ = server
}

func TestProviderSelectionUnpacksExternalModelIDAfterLegacyProviderSelection(t *testing.T) {
	store := config.NewStaticRuntimeStore(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "openai",
			Enabled:         true,
			ManualModels:    []string{"gpt-5.5"},
			ModelIDTemplate: "openai-{{model}}",
		}},
	})
	server := NewServerWithStore(store, nil, nil)

	req := httptest.NewRequest("POST", "/v1/responses", nil)
	req = req.Clone(withRuntimeSnapshot(withRouteInfo(req.Context(), routeInfo{ProviderID: "openai", Legacy: true, CanonicalPath: canonicalV1ResponsesPath}), store.Active()))
	provider, _, providerID, resolvedModel, ok, err := providerSelectionForModelRequest(req, "openai-gpt-5.5")
	if err != nil {
		t.Fatalf("expected provider selection without error, got %v", err)
	}
	if !ok || providerID != "openai" || provider.ID != "openai" || resolvedModel != "gpt-5.5" {
		t.Fatalf("expected external legacy model to resolve to openai/gpt-5.5, got providerID=%q provider=%q model=%q ok=%v", providerID, provider.ID, resolvedModel, ok)
	}
	_ = server
}

func TestProviderSelectionUnpacksExternalModelIDForExplicitProviderRoute(t *testing.T) {
	store := config.NewStaticRuntimeStore(config.Config{
		DefaultProvider: "openai",
		Providers: []config.ProviderConfig{{
			ID:              "openai",
			Enabled:         true,
			ManualModels:    []string{"gpt-5.5"},
			ModelIDTemplate: "openai-{{model}}",
		}},
	})
	server := NewServerWithStore(store, nil, nil)

	req := httptest.NewRequest("POST", "/openai/v1/responses", nil)
	req = req.Clone(withRuntimeSnapshot(withRouteInfo(req.Context(), routeInfo{ProviderID: "openai", CanonicalPath: canonicalV1ResponsesPath}), store.Active()))
	provider, _, providerID, resolvedModel, ok, err := providerSelectionForModelRequest(req, "openai-gpt-5.5")
	if err != nil {
		t.Fatalf("expected provider selection without error, got %v", err)
	}
	if !ok || providerID != "openai" || provider.ID != "openai" || resolvedModel != "gpt-5.5" {
		t.Fatalf("expected external provider-route model to resolve to openai/gpt-5.5, got providerID=%q provider=%q model=%q ok=%v", providerID, provider.ID, resolvedModel, ok)
	}
	_ = server
}

func TestProviderSelectionKeepsRawModelForRootOnlyExplicitProviderRoute(t *testing.T) {
	store := config.NewStaticRuntimeStore(config.Config{
		DefaultProvider: "openai",
		Providers: []config.ProviderConfig{{
			ID:                      "openai",
			Enabled:                 true,
			ManualModels:            []string{"gpt-5.5"},
			ModelIDTemplate:         "openai-{{model}}",
			ModelIDTemplateRootOnly: true,
		}},
	})
	server := NewServerWithStore(store, nil, nil)

	req := httptest.NewRequest("POST", "/openai/v1/responses", nil)
	req = req.Clone(withRuntimeSnapshot(withRouteInfo(req.Context(), routeInfo{ProviderID: "openai", CanonicalPath: canonicalV1ResponsesPath}), store.Active()))
	provider, _, providerID, resolvedModel, ok, err := providerSelectionForModelRequest(req, "gpt-5.5")
	if err != nil {
		t.Fatalf("expected provider selection without error, got %v", err)
	}
	if !ok || providerID != "openai" || provider.ID != "openai" || resolvedModel != "gpt-5.5" {
		t.Fatalf("expected raw provider-route model to remain openai/gpt-5.5, got providerID=%q provider=%q model=%q ok=%v", providerID, provider.ID, resolvedModel, ok)
	}
	_, _, _, _, ok, err = providerSelectionForModelRequest(req, "openai-gpt-5.5")
	if err != nil {
		t.Fatalf("expected provider selection rejection without upstream error, got %v", err)
	}
	if ok {
		t.Fatalf("expected root-only explicit provider route to reject templated external model id")
	}
	_ = server
}
