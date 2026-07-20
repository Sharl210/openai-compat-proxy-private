package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestDefaultOverlaySuffixRouting_resolvesRealtimeBaseBeforeProxyAxes(t *testing.T) {
	// Given
	alpha := newOverlaySuffixUpstream(t, []string{"realtime-base"})
	defer alpha.Close()
	beta := newOverlaySuffixUpstream(t, []string{"other-model"})
	defer beta.Close()
	cfg := defaultOverlaySuffixConfig(alpha.URL, beta.URL)
	cfg.Providers[0].ManualModels = []string{"realtime-base"}
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"realtime-base-high-pro-ultra-noprompt","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("expected realtime base suffix request to succeed, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Provider-Name"); got != "alpha" {
		discovery, _ := defaultOverlayDiscoveryFromRequest(req)
		intent, _ := proxyModelIntentFromRequest(req)
		t.Fatalf("expected alpha owner, got %q discovery=%#v intent=%#v", got, discovery, intent)
	}
	if got := rec.Header().Get(headerClientToProxyReasoningEffort); got != "high" {
		t.Fatalf("expected high effort, got %q", got)
	}
	if got := rec.Header().Get(headerClientToProxyReasoningMode); got != "pro" {
		t.Fatalf("expected pro mode, got %q", got)
	}
	if got := rec.Header().Get(headerClientToProxyNoPrompt); got != "true" {
		t.Fatalf("expected noprompt intent, got %q", got)
	}
	captured := <-alpha.requests
	if got := captured.body["model"]; got != "realtime-base" {
		t.Fatalf("expected stripped upstream model, got %#v", got)
	}
	reasoning, _ := captured.body["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" || reasoning["mode"] != "pro" {
		t.Fatalf("expected effort and pro reasoning, got %#v", reasoning)
	}
	multiAgent, _ := captured.body["multi_agent"].(map[string]any)
	if multiAgent["enabled"] != true || multiAgent["max_concurrent_subagents"] != float64(5) {
		t.Fatalf("expected ultra multi_agent payload, got %#v", multiAgent)
	}
	if !strings.Contains(captured.beta, "responses_multi_agent=v1") {
		t.Fatalf("expected multi-agent beta header, got %q", captured.beta)
	}
}

func TestRootV1ModelMapDiscoversMappedTargetOwnerBeforeDefaultFallback(t *testing.T) {
	target := newOverlaySuffixUpstream(t, []string{"gpt-5.6-sol"})
	defer target.Close()
	fallback := newOverlaySuffixUpstream(t, []string{"deepseek-v4-pro"})
	defer fallback.Close()
	cfg := defaultOverlaySuffixConfig(target.URL, fallback.URL)
	cfg.V1ModelMap = []config.ModelMapEntry{config.NewModelMapEntry("gpt-5.6-sol", "gpt-5.6-sol-xhigh")}
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-sol","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected root mapped realtime model to succeed, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Provider-Name"); got != "alpha" {
		t.Fatalf("expected mapped target owner alpha, got %q", got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "gpt-5.6-sol" {
		t.Fatalf("expected mapped target base model, got %q", got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamReasoningEffort); got != "xhigh" {
		t.Fatalf("expected mapped xhigh reasoning effort, got %q", got)
	}
	if fallback.responseHits.Load() != 0 {
		t.Fatalf("expected no fallback upstream calls, got %d", fallback.responseHits.Load())
	}
	captured := <-target.requests
	if got := captured.body["model"]; got != "gpt-5.6-sol" {
		t.Fatalf("expected target upstream model, got %#v", got)
	}
	reasoning, _ := captured.body["reasoning"].(map[string]any)
	if reasoning["effort"] != "xhigh" {
		t.Fatalf("expected target upstream xhigh effort, got %#v", reasoning)
	}
}

func TestDefaultOverlaySuffixRouting_preservesExactSuffixLikeLiteralOwner(t *testing.T) {
	// Given
	literalModel := "realtime-base-high-pro-ultra-noprompt"
	alpha := newOverlaySuffixUpstream(t, []string{"realtime-base"})
	defer alpha.Close()
	beta := newOverlaySuffixUpstream(t, []string{literalModel})
	defer beta.Close()
	cfg := defaultOverlaySuffixConfig(alpha.URL, beta.URL)
	cfg.Providers[0].ManualModels = []string{"realtime-base"}
	cfg.Providers[1].ManualModels = []string{literalModel}
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"`+literalModel+`","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("expected exact suffix-like literal to succeed, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Provider-Name"); got != "beta" {
		discovery, _ := defaultOverlayDiscoveryFromRequest(req)
		intent, _ := proxyModelIntentFromRequest(req)
		t.Fatalf("expected exact literal owner beta, got %q discovery=%#v intent=%#v", got, discovery, intent)
	}
	captured := <-beta.requests
	if got := captured.body["model"]; got != literalModel {
		t.Fatalf("expected literal upstream model unchanged, got %#v", got)
	}
	if alpha.responseHits.Load() != 0 {
		t.Fatalf("expected derived alpha owner not to receive exact literal request, got %d hits", alpha.responseHits.Load())
	}
}

func TestDefaultOverlayRealtimeDiscovery_prefersExactSuffixLikeRawLiteral(t *testing.T) {
	literalModel := "realtime-base-high-pro-ultra-noprompt"
	alpha := newOverlaySuffixUpstream(t, []string{"realtime-base"})
	defer alpha.Close()
	beta := newOverlaySuffixUpstream(t, []string{literalModel})
	defer beta.Close()
	cfg := defaultOverlaySuffixConfig(alpha.URL, beta.URL)
	cfg.Providers[0].ManualModels = []string{"realtime-base"}
	cfg.Providers[1].ManualModels = []string{literalModel}
	store := config.NewStaticRuntimeStore(cfg)
	req := overlayProviderSelectionRequest(store)

	discovery, err, found := resolveDefaultProviderSelectionFromRealtimeModels(req, store.Active(), literalModel)

	if err != nil || !found {
		t.Fatalf("expected exact realtime discovery, found=%t err=%v", found, err)
	}
	if discovery.ProviderID != "beta" || discovery.RawModelID != literalModel || !discovery.ExactLiteral || !discovery.SourceProxyModelIntent.IsExactLiteral {
		t.Fatalf("expected beta exact literal discovery, got %#v", discovery)
	}
}

func TestProviderSelectionForModelRequest_consumesRealtimeExactLiteralDiscovery(t *testing.T) {
	literalModel := "realtime-base-high-pro-ultra-noprompt"
	alpha := newOverlaySuffixUpstream(t, []string{"realtime-base"})
	defer alpha.Close()
	beta := newOverlaySuffixUpstream(t, []string{literalModel})
	defer beta.Close()
	cfg := defaultOverlaySuffixConfig(alpha.URL, beta.URL)
	cfg.Providers[0].ManualModels = []string{"realtime-base"}
	cfg.Providers[1].ManualModels = []string{literalModel}
	store := config.NewStaticRuntimeStore(cfg)
	req := overlayProviderSelectionRequest(store)

	resolveDefaultOverlayDiscoveryBeforeProviderSelection(req, literalModel)
	if discovery, found := defaultOverlayDiscoveryFromRequest(req); !found || discovery.ProviderID != "beta" || discovery.RequestedModelID != literalModel {
		t.Fatalf("expected beta discovery on request context, found=%t discovery=%#v", found, discovery)
	}
	_, _, providerID, resolvedModel, found, err := providerSelectionForModelRequest(req, literalModel)

	if err != nil || !found || providerID != "beta" || resolvedModel != literalModel {
		t.Fatalf("expected beta exact literal selection, provider=%q model=%q found=%t err=%v", providerID, resolvedModel, found, err)
	}
}

func TestConfiguredProviderSelectionUsesFirstConfiguredProxyIntent(t *testing.T) {
	// Given
	literalModel := "realtime-base-high-pro-ultra-noprompt"
	alpha := newOverlaySuffixUpstream(t, []string{"realtime-base"})
	defer alpha.Close()
	beta := newOverlaySuffixUpstream(t, []string{literalModel})
	defer beta.Close()
	cfg := defaultOverlaySuffixConfig(alpha.URL, beta.URL)
	cfg.Providers[0].ManualModels = []string{"realtime-base"}
	store := config.NewStaticRuntimeStore(cfg)
	// When
	exactProvider, exactModel, _, exactOK := configuredDefaultProviderSelection(store.Active(), literalModel, "")
	derivedProvider, derivedModel, _, derivedOK := configuredDefaultProviderSelection(store.Active(), "realtime-base-high-pro-ultra-noprompt-extra", "")

	// Then
	if !exactOK || exactProvider != "alpha" || exactModel != "realtime-base-high" {
		t.Fatalf("expected first configured proxy intent alpha, got provider=%q model=%q ok=%t", exactProvider, exactModel, exactOK)
	}
	if derivedOK {
		t.Fatalf("expected malformed derived tail to avoid configured selection, got provider=%q model=%q", derivedProvider, derivedModel)
	}
}

func TestProviderSelectionForModelRequestUsesConfiguredProxyIntentBeforeFallback(t *testing.T) {
	// Given
	literalModel := "realtime-base-high-pro-ultra-noprompt"
	alpha := newOverlaySuffixUpstream(t, []string{"realtime-base"})
	defer alpha.Close()
	beta := newOverlaySuffixUpstream(t, []string{literalModel})
	defer beta.Close()
	cfg := defaultOverlaySuffixConfig(alpha.URL, beta.URL)
	cfg.Providers[0].ManualModels = []string{"realtime-base"}
	store := config.NewStaticRuntimeStore(cfg)
	exactRequest := overlayProviderSelectionRequest(store)
	derivedRequest := overlayProviderSelectionRequest(store)

	// When
	_, _, exactProvider, exactModel, exactOK, exactErr := providerSelectionForModelRequest(exactRequest, literalModel)
	_, _, derivedProvider, derivedModel, derivedOK, derivedErr := providerSelectionForModelRequest(derivedRequest, "realtime-base-high-pro-ultra-noprompt-extra")

	// Then
	if exactErr != nil || !exactOK || exactProvider != "alpha" || exactModel != "realtime-base-high" {
		t.Fatalf("expected configured alpha selection, got provider=%q model=%q ok=%t err=%v", exactProvider, exactModel, exactOK, exactErr)
	}
	if derivedErr != nil || !derivedOK || derivedProvider != "beta" || derivedModel != "realtime-base-high-pro-ultra-noprompt-extra" {
		t.Fatalf("expected deterministic fallback for malformed derived model, got provider=%q model=%q ok=%t err=%v", derivedProvider, derivedModel, derivedOK, derivedErr)
	}
}

func overlayProviderSelectionRequest(store *config.RuntimeStore) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx := withRouteInfo(req.Context(), routeInfo{ProviderID: "alpha", Legacy: true, CanonicalPath: canonicalV1ResponsesPath})
	ctx = withRuntimeStore(ctx, store)
	ctx = withRuntimeSnapshot(ctx, store.Active())
	return req.WithContext(ctx)
}

type overlaySuffixCapture struct {
	body map[string]any
	beta string
}

type overlaySuffixUpstream struct {
	*httptest.Server
	requests     chan overlaySuffixCapture
	responseHits atomic.Int32
}

func newOverlaySuffixUpstream(t *testing.T, modelIDs []string) *overlaySuffixUpstream {
	t.Helper()
	upstream := &overlaySuffixUpstream{requests: make(chan overlaySuffixCapture, 8)}
	upstream.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			data := make([]map[string]any, 0, len(modelIDs))
			for _, modelID := range modelIDs {
				data = append(data, map[string]any{"id": modelID, "object": "model"})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
		case "/responses":
			upstream.responseHits.Add(1)
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			upstream.requests <- overlaySuffixCapture{body: body, beta: r.Header.Get("OpenAI-Beta")}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_overlay","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	return upstream
}

func defaultOverlaySuffixConfig(alphaURL string, betaURL string) config.Config {
	cfg := config.Default()
	cfg.DefaultProvider = "alpha,beta"
	cfg.EnableLegacyV1Routes = true
	cfg.DownstreamNonStreamStrategy = config.DownstreamNonStreamStrategyUpstreamNonStream
	cfg.DefaultProReasoningModeSet = true
	cfg.DefaultProReasoningMode = false
	cfg.Providers = []config.ProviderConfig{
		overlaySuffixProvider("alpha", alphaURL),
		overlaySuffixProvider("beta", betaURL),
	}
	return cfg
}

func overlaySuffixProvider(id string, upstreamURL string) config.ProviderConfig {
	return config.ProviderConfig{
		ID:                             id,
		Enabled:                        true,
		UpstreamBaseURL:                upstreamURL,
		UpstreamAPIKey:                 id + "-key",
		UpstreamEndpointType:           config.UpstreamEndpointTypeResponses,
		SupportsChat:                   true,
		SupportsResponses:              true,
		SupportsModels:                 true,
		SupportsAnthropicMessages:      true,
		ManualModels:                   []string{id + "-static"},
		EnableReasoningEffortSuffix:    true,
		ReasoningModeProCapability:     config.ReasoningModeProCapabilitySupported,
		SupportsResponsesMultiAgent:    true,
		UltraMaxConcurrentSubagents:    5,
		UltraMaxConcurrentSubagentsSet: true,
	}
}
