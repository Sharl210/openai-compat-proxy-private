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
		t.Fatalf("expected wildcard-only model outside visible list to be rejected, got %d body=%s", rec.Code, rec.Body.String())
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
		t.Fatalf("expected tagged wildcard-only model outside visible list to be rejected, got %d body=%s", rec.Code, rec.Body.String())
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

func TestLegacyResponsesRouteRejectsUntaggedOverlappingModelWhenTagModeEnabled(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	cfg := testLegacyModelRoutingConfig(alpha.URL, beta.URL)
	cfg.EnableDefaultProviderModelTags = true
	cfg.Providers[1].ModelMap = []config.ModelMapEntry{
		config.NewModelMapEntry("owned-*", "beta-$1-upstream"),
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
					config.NewModelMapEntry("owned-*", "alpha-$1-upstream"),
				},
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
