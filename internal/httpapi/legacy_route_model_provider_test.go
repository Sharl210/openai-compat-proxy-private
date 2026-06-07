package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestLegacyChatRouteChoosesProviderByExactModelOwner(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	server := NewServer(testLegacyModelRoutingConfig(alpha.URL, beta.URL))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha-chat","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Provider-Name"); got != "alpha" {
		t.Fatalf("expected X-Provider-Name alpha, got %q", got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "alpha-chat-upstream" {
		t.Fatalf("expected %s alpha-chat-upstream, got %q", headerProxyToUpstreamModel, got)
	}
	if alpha.Hits() != 1 || beta.Hits() != 0 {
		t.Fatalf("expected only alpha upstream to be hit, alpha=%d beta=%d", alpha.Hits(), beta.Hits())
	}
	if !strings.Contains(rec.Body.String(), `"choices"`) {
		t.Fatalf("expected chat response body, got %s", rec.Body.String())
	}
}

func TestLegacyChatRouteAppliesRootV1ModelMapBeforeDefaultProviderSelection(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	cfg := testLegacyModelRoutingConfig(alpha.URL, beta.URL)
	cfg.V1ModelMap = []config.ModelMapEntry{config.NewModelMapEntry("#re:alias-(alpha)", "$1-chat")}
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alias-alpha","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Provider-Name"); got != "alpha" {
		t.Fatalf("expected V1 model alias to route to alpha, got %q", got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "alpha-chat-upstream" {
		t.Fatalf("expected provider MODEL_MAP to still run after V1_MODEL_MAP, got %q", got)
	}
	if alpha.Hits() != 1 || beta.Hits() != 0 {
		t.Fatalf("expected only alpha upstream to be hit, alpha=%d beta=%d", alpha.Hits(), beta.Hits())
	}
}

func TestExplicitProviderChatRouteDoesNotApplyRootV1ModelMap(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	cfg := testLegacyModelRoutingConfig(alpha.URL, beta.URL)
	cfg.V1ModelMap = []config.ModelMapEntry{config.NewModelMapEntry("alias-alpha", "alpha-chat")}
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/beta/v1/chat/completions", strings.NewReader(`{"model":"alias-alpha","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Provider-Name"); got != "beta" {
		t.Fatalf("expected explicit provider route to stay on beta, got %q", got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "alias-alpha" {
		t.Fatalf("expected explicit provider route to skip V1_MODEL_MAP, got upstream model %q", got)
	}
	if alpha.Hits() != 0 || beta.Hits() != 1 {
		t.Fatalf("expected only beta upstream to be hit, alpha=%d beta=%d", alpha.Hits(), beta.Hits())
	}
}

func TestLegacyChatRouteRejectsRootV1ModelMapTargetOutsideVisibleModels(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	cfg := testLegacyModelRoutingConfig(alpha.URL, beta.URL)
	cfg.V1ModelMap = []config.ModelMapEntry{config.NewModelMapEntry("alias-missing", "missing-model")}
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alias-missing","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected mapped unknown model to return invalid_model, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid_model") {
		t.Fatalf("expected invalid_model error body, got %s", rec.Body.String())
	}
	if alpha.Hits() != 0 || beta.Hits() != 0 {
		t.Fatalf("expected no upstream hit for mapped unknown model, alpha=%d beta=%d", alpha.Hits(), beta.Hits())
	}
}

func TestLegacyResponsesRouteV1ModelMapTargetKeepsReasoningSuffixBehavior(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	cfg := testLegacyModelRoutingConfig(alpha.URL, beta.URL)
	cfg.V1ModelMap = []config.ModelMapEntry{config.NewModelMapEntry("alias-reason-high", "reason-model-high")}
	cfg.Providers[0].ModelMap = nil
	cfg.Providers[0].ManualModels = []string{"reason-model"}
	cfg.Providers[0].EnableReasoningEffortSuffix = true
	cfg.Providers[0].ExposeReasoningSuffixModels = true
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"alias-reason-high","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected mapped reasoning suffix target to succeed, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Provider-Name"); got != "alpha" {
		t.Fatalf("expected mapped reasoning suffix target to route to alpha, got %q", got)
	}
	if got := rec.Header().Get("X-Client-To-Proxy-Reasoning-Effort"); got != "high" {
		t.Fatalf("expected reasoning suffix target to preserve high effort semantics, got %q", got)
	}
	if alpha.Hits() != 1 || beta.Hits() != 0 {
		t.Fatalf("expected only alpha upstream to be hit, alpha=%d beta=%d", alpha.Hits(), beta.Hits())
	}
}

func TestLegacyChatRouteV1ModelMapTargetPassesSingleDefaultProviderModelAllowance(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "alpha",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		V1ModelMap:                  []config.ModelMapEntry{config.NewModelMapEntry("alias-alpha", "alpha-chat")},
		Providers: []config.ProviderConfig{{
			ID:                   "alpha",
			Enabled:              true,
			UpstreamBaseURL:      alpha.URL,
			UpstreamAPIKey:       "alpha-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsChat:         true,
			ModelMap:             []config.ModelMapEntry{config.NewModelMapEntry("alpha-chat", "alpha-chat-upstream")},
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alias-alpha","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected mapped target to pass single default provider allowance, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "alpha-chat-upstream" {
		t.Fatalf("expected provider MODEL_MAP to run after single-provider V1_MODEL_MAP, got %q", got)
	}
}

func TestLegacyResponsesRouteRejectsModelOutsideVisibleModelsList(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	server := NewServer(testLegacyModelRoutingConfig(alpha.URL, beta.URL))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"owned-5","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected regex-only model outside visible list to be rejected, got %d body=%s", rec.Code, rec.Body.String())
	}
	if alpha.Hits() != 0 || beta.Hits() != 0 {
		t.Fatalf("expected no upstream hit for model outside visible list, alpha=%d beta=%d", alpha.Hits(), beta.Hits())
	}
}

func TestLegacyMessagesRouteChoosesProviderByExactModelOwner(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	server := NewServer(testLegacyModelRoutingConfig(alpha.URL, beta.URL))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"alpha-message","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Provider-Name"); got != "alpha" {
		t.Fatalf("expected X-Provider-Name alpha, got %q", got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "alpha-message-upstream" {
		t.Fatalf("expected %s alpha-message-upstream, got %q", headerProxyToUpstreamModel, got)
	}
	if alpha.Hits() != 1 || beta.Hits() != 0 {
		t.Fatalf("expected only alpha upstream to be hit, alpha=%d beta=%d", alpha.Hits(), beta.Hits())
	}
	if !strings.Contains(rec.Body.String(), `"type":"message"`) {
		t.Fatalf("expected anthropic message response body, got %s", rec.Body.String())
	}
}

func TestLegacyMessagesRouteDropsContextManagementForResponsesUpstream(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	server := NewServer(testLegacyModelRoutingConfig(alpha.URL, beta.URL))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"alpha-message","max_tokens":64,"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if alpha.Hits() != 1 || beta.Hits() != 0 {
		t.Fatalf("expected only alpha upstream hit after dropping context_management, alpha=%d beta=%d", alpha.Hits(), beta.Hits())
	}
}

func TestExplicitChatRouteKeepsExplicitProviderEvenWhenAnotherDefaultOwnsModel(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	server := NewServer(testLegacyModelRoutingConfig(alpha.URL, beta.URL))
	req := httptest.NewRequest(http.MethodPost, "/beta/v1/chat/completions", strings.NewReader(`{"model":"alpha-chat","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Provider-Name"); got != "beta" {
		t.Fatalf("expected X-Provider-Name beta, got %q", got)
	}
	if alpha.Hits() != 0 || beta.Hits() != 1 {
		t.Fatalf("expected only beta upstream to be hit, alpha=%d beta=%d", alpha.Hits(), beta.Hits())
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "alpha-chat" {
		t.Fatalf("expected explicit provider to keep beta normalization path, got %q", got)
	}
	if !strings.Contains(rec.Body.String(), `"choices"`) {
		t.Fatalf("expected chat response body, got %s", rec.Body.String())
	}
}

func TestLegacyResponsesRouteTaggedVisibleModelChoosesTaggedProvider(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	cfg := testLegacyModelRoutingConfig(alpha.URL, beta.URL)
	cfg.EnableDefaultProviderModelTags = true
	cfg.EnableAllDefaultProviderModelTags = true
	cfg.Providers[0].ModelMap = nil
	cfg.Providers[0].ManualModels = []string{"visible-alpha"}
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"[alpha]visible-alpha","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected tagged status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Provider-Name"); got != "alpha" {
		t.Fatalf("expected tagged request to route to alpha, got %q", got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "visible-alpha" {
		t.Fatalf("expected tagged request to strip prefix before upstream mapping, got %q", got)
	}
	if alpha.Hits() != 1 || beta.Hits() != 0 {
		t.Fatalf("expected only alpha upstream to be hit for tagged request, alpha=%d beta=%d", alpha.Hits(), beta.Hits())
	}
}

func TestLegacyResponsesRouteRejectsTaggedModelOutsideVisibleModelsList(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	cfg := testLegacyModelRoutingConfig(alpha.URL, beta.URL)
	cfg.EnableDefaultProviderModelTags = true
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"[alpha]owned-5","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected tagged regex-only model outside visible list to be rejected, got %d body=%s", rec.Code, rec.Body.String())
	}
	if alpha.Hits() != 0 || beta.Hits() != 0 {
		t.Fatalf("expected no upstream hit for tagged model outside visible list, alpha=%d beta=%d", alpha.Hits(), beta.Hits())
	}
}

func TestExplicitProviderResponsesRouteRejectsModelOutsideProviderModelsList(t *testing.T) {
	var modelsHits atomic.Int32
	var responsesHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			modelsHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"visible-explicit","object":"model"}]}`))
		case "/responses":
			responsesHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_beta","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "beta",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "beta",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "beta-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			SupportsModels:       true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/beta/v1/responses", strings.NewReader(`{"model":"missing-explicit","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer beta-key")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected explicit provider model outside models list to be rejected, got %d body=%s", rec.Code, rec.Body.String())
	}
	if modelsHits.Load() == 0 {
		t.Fatalf("expected explicit provider validation to consult provider models list")
	}
	if responsesHits.Load() != 0 {
		t.Fatalf("expected no upstream responses hit when model is outside provider models list, got %d", responsesHits.Load())
	}
}

func TestExplicitProviderResponsesRouteFollowsReasoningSuffixVisibleModels(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "alpha",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                          "alpha",
			Enabled:                     true,
			UpstreamBaseURL:             alpha.URL,
			UpstreamAPIKey:              "alpha-key",
			UpstreamEndpointType:        config.UpstreamEndpointTypeResponses,
			SupportsResponses:           true,
			ManualModels:                []string{"visible-reasoning"},
			EnableReasoningEffortSuffix: true,
			ExposeReasoningSuffixModels: true,
			HiddenModels:                []string{"visible-reasoning-high", "visible-reasoning-low"},
		}},
	})

	allowedReq := httptest.NewRequest(http.MethodPost, "/alpha/v1/responses", strings.NewReader(`{"model":"visible-reasoning-medium","input":"hello"}`))
	allowedReq.Header.Set("Content-Type", "application/json")
	allowedReq.Header.Set("Authorization", "Bearer alpha-key")
	allowedRec := httptest.NewRecorder()
	server.ServeHTTP(allowedRec, allowedReq)
	if allowedRec.Code != http.StatusOK {
		t.Fatalf("expected visible reasoning suffix model to succeed, got %d body=%s", allowedRec.Code, allowedRec.Body.String())
	}

	rejectedReq := httptest.NewRequest(http.MethodPost, "/alpha/v1/responses", strings.NewReader(`{"model":"visible-reasoning-high","input":"hello"}`))
	rejectedReq.Header.Set("Content-Type", "application/json")
	rejectedReq.Header.Set("Authorization", "Bearer alpha-key")
	rejectedRec := httptest.NewRecorder()
	server.ServeHTTP(rejectedRec, rejectedReq)
	if rejectedRec.Code != http.StatusBadRequest {
		t.Fatalf("expected hidden reasoning suffix model to be rejected, got %d body=%s", rejectedRec.Code, rejectedRec.Body.String())
	}
}

func TestExplicitProviderResponsesRouteHiddenEffortSelectorOverridesGlobalReasoningSuffix(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "alpha",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                          "alpha",
			Enabled:                     true,
			UpstreamBaseURL:             alpha.URL,
			UpstreamAPIKey:              "alpha-key",
			UpstreamEndpointType:        config.UpstreamEndpointTypeResponses,
			SupportsResponses:           true,
			ManualModels:                []string{"visible-reasoning"},
			EnableReasoningEffortSuffix: true,
			ExposeReasoningSuffixModels: true,
			HiddenModels:                []string{"#reason_suffix:-minimal"},
		}},
	})

	allowedReq := httptest.NewRequest(http.MethodPost, "/alpha/v1/responses", strings.NewReader(`{"model":"visible-reasoning-low","input":"hello"}`))
	allowedReq.Header.Set("Content-Type", "application/json")
	allowedReq.Header.Set("Authorization", "Bearer alpha-key")
	allowedRec := httptest.NewRecorder()
	server.ServeHTTP(allowedRec, allowedReq)
	if allowedRec.Code != http.StatusOK {
		t.Fatalf("expected non-hidden global reasoning suffix model to succeed, got %d body=%s", allowedRec.Code, allowedRec.Body.String())
	}

	rejectedReq := httptest.NewRequest(http.MethodPost, "/alpha/v1/responses", strings.NewReader(`{"model":"visible-reasoning-minimal","input":"hello"}`))
	rejectedReq.Header.Set("Content-Type", "application/json")
	rejectedReq.Header.Set("Authorization", "Bearer alpha-key")
	rejectedRec := httptest.NewRecorder()
	server.ServeHTTP(rejectedRec, rejectedReq)
	if rejectedRec.Code != http.StatusBadRequest {
		t.Fatalf("expected hidden effort selector to override global reasoning suffix request, got %d body=%s", rejectedRec.Code, rejectedRec.Body.String())
	}
}

func TestExplicitProviderResponsesRouteAllowsReasoningSuffixWhenNotExposedInModelsList(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "alpha",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                          "alpha",
			Enabled:                     true,
			UpstreamBaseURL:             alpha.URL,
			UpstreamAPIKey:              "alpha-key",
			UpstreamEndpointType:        config.UpstreamEndpointTypeResponses,
			SupportsResponses:           true,
			ManualModels:                []string{"visible-reasoning"},
			EnableReasoningEffortSuffix: true,
			ExposeReasoningSuffixModels: false,
			HiddenModels:                []string{"visible-reasoning-high"},
		}},
	})

	allowedReq := httptest.NewRequest(http.MethodPost, "/alpha/v1/responses", strings.NewReader(`{"model":"visible-reasoning-medium","input":"hello"}`))
	allowedReq.Header.Set("Content-Type", "application/json")
	allowedReq.Header.Set("Authorization", "Bearer alpha-key")
	allowedRec := httptest.NewRecorder()
	server.ServeHTTP(allowedRec, allowedReq)
	if allowedRec.Code != http.StatusOK {
		t.Fatalf("expected explicit suffix request to succeed even when suffix variants are hidden from /models, got %d body=%s", allowedRec.Code, allowedRec.Body.String())
	}
	if got := allowedRec.Header().Get("X-Client-To-Proxy-Reasoning-Effort"); got != "medium" {
		t.Fatalf("expected reasoning suffix effort medium to be preserved, got %q", got)
	}

	rejectedReq := httptest.NewRequest(http.MethodPost, "/alpha/v1/responses", strings.NewReader(`{"model":"visible-reasoning-high","input":"hello"}`))
	rejectedReq.Header.Set("Content-Type", "application/json")
	rejectedReq.Header.Set("Authorization", "Bearer alpha-key")
	rejectedRec := httptest.NewRecorder()
	server.ServeHTTP(rejectedRec, rejectedReq)
	if rejectedRec.Code != http.StatusBadRequest {
		t.Fatalf("expected explicitly hidden suffix model to stay rejected, got %d body=%s", rejectedRec.Code, rejectedRec.Body.String())
	}
}

func TestLegacyResponsesRouteAllowsReasoningSuffixForMappedTargetWhenNotExposedInModelsList(t *testing.T) {
	var responsesHits atomic.Int32
	alpha := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.5","object":"model"}]}`))
		case "/responses":
			responsesHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"id":"resp_alpha","object":"response","status":"completed","output":[{"id":"msg_alpha","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hello from alpha"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer alpha.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "alpha",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "alpha",
			Enabled:              true,
			UpstreamBaseURL:      alpha.URL,
			UpstreamAPIKey:       "alpha-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			SupportsModels:       true,
			ManualModels:         []string{"gpt-5.4-mini"},
			ModelMap: []config.ModelMapEntry{
				config.NewModelMapEntry("MiniMax-M2.7", "gpt-5.5-low"),
				config.NewModelMapEntry("gpt-5.4", "gpt-5.5"),
			},
			EnableReasoningEffortSuffix: true,
			ExposeReasoningSuffixModels: false,
			HiddenModels:                []string{"#re:.*mini.*", "#re:.*5\\.4-.*"},
		}},
	})

	allowedReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.5-low","input":"hello"}`))
	allowedReq.Header.Set("Content-Type", "application/json")
	allowedRec := httptest.NewRecorder()
	server.ServeHTTP(allowedRec, allowedReq)
	if allowedRec.Code != http.StatusOK {
		t.Fatalf("expected bare /v1 target suffix request to succeed even when suffix variants are hidden from /models, got %d body=%s", allowedRec.Code, allowedRec.Body.String())
	}
	if got := allowedRec.Header().Get("X-Client-To-Proxy-Reasoning-Effort"); got != "low" {
		t.Fatalf("expected reasoning suffix effort low to be preserved, got %q", got)
	}
	if got := allowedRec.Header().Get(headerProxyToUpstreamModel); got != "gpt-5.5" {
		t.Fatalf("expected upstream model to resolve to suffix base gpt-5.5, got %q", got)
	}
	if responsesHits.Load() != 1 {
		t.Fatalf("expected one upstream responses hit, got %d", responsesHits.Load())
	}

	rejectedReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4-high","input":"hello"}`))
	rejectedReq.Header.Set("Content-Type", "application/json")
	rejectedRec := httptest.NewRecorder()
	server.ServeHTTP(rejectedRec, rejectedReq)
	if rejectedRec.Code != http.StatusBadRequest {
		t.Fatalf("expected explicitly hidden public suffix alias to stay rejected, got %d body=%s", rejectedRec.Code, rejectedRec.Body.String())
	}
}

func TestLegacyResponsesRealtimeModels429SurfacesUpstreamErrorInsteadOfInvalidModel(t *testing.T) {
	var modelsHits atomic.Int32
	var responsesHits atomic.Int32
	alpha := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			modelsHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"USAGE_LIMIT_EXCEEDED","message":"daily usage limit exceeded","type":"insufficient_quota"}}`))
		case "/responses":
			responsesHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_alpha","object":"response","status":"completed","output":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "alpha,beta",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{
			{
				ID:                   "alpha",
				Enabled:              true,
				UpstreamBaseURL:      alpha.URL,
				UpstreamAPIKey:       "alpha-key",
				UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
				SupportsResponses:    true,
				SupportsModels:       true,
			},
			{
				ID:                   "beta",
				Enabled:              true,
				UpstreamBaseURL:      beta.URL,
				UpstreamAPIKey:       "beta-key",
				UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
				SupportsResponses:    true,
				ManualModels:         []string{"beta-model"},
			},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"alpha-live","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected upstream 429 to surface, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "USAGE_LIMIT_EXCEEDED") || !strings.Contains(body, "daily usage limit exceeded") {
		t.Fatalf("expected upstream quota body to be preserved, got %s", body)
	}
	if strings.Contains(body, "requested model is not in models list") || strings.Contains(body, "invalid_model") {
		t.Fatalf("expected upstream error not model availability error, got %s", body)
	}
	if modelsHits.Load() == 0 {
		t.Fatalf("expected realtime /models lookup to be attempted")
	}
	if responsesHits.Load() != 0 || beta.Hits() != 0 {
		t.Fatalf("expected no upstream response call after model discovery 429, alpha responses=%d beta hits=%d", responsesHits.Load(), beta.Hits())
	}
}

func TestExplicitProviderResponsesModelList429SurfacesUpstreamError(t *testing.T) {
	var modelsHits atomic.Int32
	var responsesHits atomic.Int32
	alpha := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			modelsHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"USAGE_LIMIT_EXCEEDED","message":"daily usage limit exceeded","type":"insufficient_quota"}}`))
		case "/responses":
			responsesHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_alpha","object":"response","status":"completed","output":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer alpha.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "alpha",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "alpha",
			Enabled:              true,
			UpstreamBaseURL:      alpha.URL,
			UpstreamAPIKey:       "alpha-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			SupportsModels:       true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/alpha/v1/responses", strings.NewReader(`{"model":"alpha-live","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer alpha-key")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected provider model preflight 429 to surface, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "USAGE_LIMIT_EXCEEDED") || !strings.Contains(body, "daily usage limit exceeded") {
		t.Fatalf("expected upstream quota body to be preserved, got %s", body)
	}
	if strings.Contains(body, "requested model is not in models list") || strings.Contains(body, "invalid_model") {
		t.Fatalf("expected upstream error not model availability error, got %s", body)
	}
	if modelsHits.Load() == 0 {
		t.Fatalf("expected provider /models preflight to be attempted")
	}
	if responsesHits.Load() != 0 {
		t.Fatalf("expected no upstream response call after model preflight 429, got %d", responsesHits.Load())
	}
}

func TestLegacyResponsesRouteRejectsUntaggedOverlappingModelWhenTagModeEnabled(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	cfg := testLegacyModelRoutingConfig(alpha.URL, beta.URL)
	cfg.EnableDefaultProviderModelTags = true
	cfg.Providers[1].ModelMap = []config.ModelMapEntry{
		config.NewModelMapEntry("#re:owned-(.*)", "beta-$1-upstream"),
	}
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"owned-5","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected untagged overlapping model to be rejected in tag mode, got %d body=%s", rec.Code, rec.Body.String())
	}
	if alpha.Hits() != 0 || beta.Hits() != 0 {
		t.Fatalf("expected no upstream hit for ambiguous untagged model, alpha=%d beta=%d", alpha.Hits(), beta.Hits())
	}
}

func TestLegacyResponsesRouteRejectsUnknownBareModelWhenTagModeEnabled(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	cfg := testLegacyModelRoutingConfig(alpha.URL, beta.URL)
	cfg.EnableDefaultProviderModelTags = true
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"missing-model","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected unknown bare model to be rejected in tag mode, got %d body=%s", rec.Code, rec.Body.String())
	}
	if alpha.Hits() != 0 || beta.Hits() != 0 {
		t.Fatalf("expected no upstream hit for unknown bare model, alpha=%d beta=%d", alpha.Hits(), beta.Hits())
	}
}

func TestLegacyResponsesRouteUsesRealtimeOverlayModelOwnerForBareRequests(t *testing.T) {
	minimaxUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"MiniMax-M2.7","object":"model"}]}`))
		case "/responses":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_minimax","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer minimaxUpstream.Close()

	var codexForHits atomic.Int32
	codexForUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			if got := r.Header.Get("Authorization"); got != "Bearer codex-for-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.1","object":"model"}]}`))
		case "/responses":
			if got := r.Header.Get("Authorization"); got != "Bearer codex-for-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			codexForHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_codex_for","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer codexForUpstream.Close()

	codexUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.4","object":"model"}]}`))
		case "/responses":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_codex","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer codexUpstream.Close()

	server := NewServer(config.Config{
		ProxyAPIKey:                 "root-secret",
		DefaultProvider:             "minimax,codex-for,codex",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "minimax",
			Enabled:              true,
			UpstreamBaseURL:      minimaxUpstream.URL,
			UpstreamAPIKey:       "minimax-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			SupportsModels:       true,
			ManualModels:         []string{"MiniMax-M2.7"},
		}, {
			ID:                   "codex-for",
			Enabled:              true,
			UpstreamBaseURL:      codexForUpstream.URL,
			UpstreamAPIKey:       "codex-for-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			SupportsModels:       true,
		}, {
			ID:                   "codex",
			Enabled:              true,
			UpstreamBaseURL:      codexUpstream.URL,
			UpstreamAPIKey:       "codex-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			SupportsModels:       true,
			ManualModels:         []string{"gpt-5.4"},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.1","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer root-secret")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected bare route to route realtime overlay model owner, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Provider-Name"); got != "codex-for" {
		t.Fatalf("expected realtime overlay owner codex-for, got %q", got)
	}
	if codexForHits.Load() != 1 {
		t.Fatalf("expected codex-for upstream to handle bare realtime-overlay request, got %d hits", codexForHits.Load())
	}
}

func TestLegacyResponsesRouteTaggedManualModelStripsPrefixBeforeUpstream(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	cfg := testLegacyModelRoutingConfig(alpha.URL, beta.URL)
	cfg.EnableDefaultProviderModelTags = true
	cfg.EnableAllDefaultProviderModelTags = true
	cfg.Providers[0].ModelMap = nil
	cfg.Providers[0].ManualModels = []string{"manual-only"}
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"[alpha]manual-only","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected tagged manual model status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Provider-Name"); got != "alpha" {
		t.Fatalf("expected tagged manual model to route to alpha, got %q", got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "manual-only" {
		t.Fatalf("expected tagged manual model to strip prefix before upstream request, got %q", got)
	}
	if alpha.Hits() != 1 || beta.Hits() != 0 {
		t.Fatalf("expected only alpha upstream to be hit for tagged manual model, alpha=%d beta=%d", alpha.Hits(), beta.Hits())
	}
}

func testLegacyModelRoutingConfig(alphaURL, betaURL string) config.Config {
	return config.Config{
		DefaultProvider:             "alpha,beta",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{
			{
				ID:                        "alpha",
				Enabled:                   true,
				UpstreamBaseURL:           alphaURL,
				UpstreamAPIKey:            "alpha-key",
				UpstreamEndpointType:      config.UpstreamEndpointTypeResponses,
				SupportsChat:              true,
				SupportsResponses:         true,
				SupportsAnthropicMessages: true,
				ModelMap: []config.ModelMapEntry{
					config.NewModelMapEntry("alpha-chat", "alpha-chat-upstream"),
					config.NewModelMapEntry("alpha-message", "alpha-message-upstream"),
					config.NewModelMapEntry("#re:owned-(.*)", "alpha-$1-upstream"),
				},
				ManualModels: []string{"alpha-chat", "alpha-message"},
			},
			{
				ID:                        "beta",
				Enabled:                   true,
				UpstreamBaseURL:           betaURL,
				UpstreamAPIKey:            "beta-key",
				UpstreamEndpointType:      config.UpstreamEndpointTypeResponses,
				SupportsChat:              true,
				SupportsResponses:         true,
				SupportsAnthropicMessages: true,
			},
		},
	}
}

type responsesProviderUpstream struct {
	*httptest.Server
	hits atomic.Int32
}

func newResponsesProviderUpstream(t *testing.T, providerID string) *responsesProviderUpstream {
	t.Helper()
	upstream := &responsesProviderUpstream{}
	upstream.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.hits.Add(1)
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"resp_%[1]s","object":"response","status":"completed","output":[{"id":"msg_%[1]s","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hello from %[1]s"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`, providerID)
	}))
	return upstream
}

func (u *responsesProviderUpstream) Hits() int {
	if u == nil {
		return 0
	}
	return int(u.hits.Load())
}
