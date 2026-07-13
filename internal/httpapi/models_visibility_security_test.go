package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"openai-compat-proxy/internal/config"
)

func newModelVisibilityAliasUpstream(t *testing.T) (*httptest.Server, *atomic.Int32, func() string) {
	t.Helper()
	var mu sync.Mutex
	seenModel := ""
	responsesHits := &atomic.Int32{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"listed-model","object":"model"}]}`))
		case "/responses":
			responsesHits.Add(1)
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			mu.Lock()
			seenModel, _ = payload["model"].(string)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_openai","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	return upstream, responsesHits, func() string {
		mu.Lock()
		defer mu.Unlock()
		return seenModel
	}
}

func TestExplicitProviderReasoningModeModelsAdmitDisplayedVariants(t *testing.T) {
	tests := []struct {
		name         string
		capability   config.ReasoningModeProCapability
		manualModels []string
		model        string
		wantStatus   int
		wantHits     int32
		wantModel    string
		wantEffort   string
	}{
		{
			name:       "supported dynamically displayed base pro admitted",
			capability: config.ReasoningModeProCapabilitySupported,
			model:      "model-pro",
			wantStatus: http.StatusOK,
			wantHits:   1,
			wantModel:  "model",
		},
		{
			name:       "supported dynamically displayed low pro admitted",
			capability: config.ReasoningModeProCapabilitySupported,
			model:      "model-low-pro",
			wantStatus: http.StatusOK,
			wantHits:   1,
			wantModel:  "model",
			wantEffort: "low",
		},
		{
			name:       "probe dynamically displayed base pro admitted",
			capability: config.ReasoningModeProCapabilityProbe,
			model:      "model-pro",
			wantStatus: http.StatusOK,
			wantHits:   1,
			wantModel:  "model",
		},
		{
			name:       "probe dynamically displayed low pro admitted",
			capability: config.ReasoningModeProCapabilityProbe,
			model:      "model-low-pro",
			wantStatus: http.StatusOK,
			wantHits:   1,
			wantModel:  "model",
			wantEffort: "low",
		},
		{
			name:         "supported base pro",
			capability:   config.ReasoningModeProCapabilitySupported,
			manualModels: []string{"model"},
			model:        "model-pro",
			wantStatus:   http.StatusOK,
			wantHits:     1,
			wantModel:    "model",
		},
		{
			name:         "supported low pro",
			capability:   config.ReasoningModeProCapabilitySupported,
			manualModels: []string{"model"},
			model:        "model-low-pro",
			wantStatus:   http.StatusOK,
			wantHits:     1,
			wantModel:    "model-low",
		},
		{
			name:         "probe base pro",
			capability:   config.ReasoningModeProCapabilityProbe,
			manualModels: []string{"model"},
			model:        "model-pro",
			wantStatus:   http.StatusOK,
			wantHits:     1,
			wantModel:    "model",
		},
		{
			name:         "probe low pro",
			capability:   config.ReasoningModeProCapabilityProbe,
			manualModels: []string{"model"},
			model:        "model-low-pro",
			wantStatus:   http.StatusOK,
			wantHits:     1,
			wantModel:    "model-low",
		},
		{
			name:       "unsupported remains unlisted and rejected",
			capability: config.ReasoningModeProCapabilityUnsupported,
			model:      "model-pro",
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			var responseHits atomic.Int32
			var mu sync.Mutex
			seenModel := ""
			seenMode := ""
			seenEffort := ""
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/models":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"model","object":"model"},{"id":"model","object":"model"}]}`))
				case "/responses":
					responseHits.Add(1)
					var payload struct {
						Model     string `json:"model"`
						Reasoning struct {
							Mode   string `json:"mode"`
							Effort string `json:"effort"`
						} `json:"reasoning"`
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Fatalf("decode upstream request: %v", err)
					}
					mu.Lock()
					seenModel = payload.Model
					seenMode = payload.Reasoning.Mode
					seenEffort = payload.Reasoning.Effort
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[]}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			server := NewServer(config.Config{
				EnableReasoningModeSuffix:    true,
				EnableReasoningModeSuffixSet: true,
				DefaultProReasoningModeSet:   true,
				DefaultProReasoningMode:      false,
				DownstreamNonStreamStrategy:  config.DownstreamNonStreamStrategyUpstreamNonStream,
				Providers: []config.ProviderConfig{{
					ID:                              "openai",
					Enabled:                         true,
					UpstreamBaseURL:                 upstream.URL,
					UpstreamAPIKey:                  "test-key",
					UpstreamEndpointType:            config.UpstreamEndpointTypeResponses,
					SupportsModels:                  true,
					SupportsResponses:               true,
					ManualModels:                    test.manualModels,
					EnableReasoningEffortSuffix:     true,
					ExposeReasoningSuffixModels:     true,
					EnableReasoningModeSuffix:       true,
					EnableReasoningModeSuffixSet:    true,
					ExposeReasoningModeSuffixModels: true,
					ReasoningModeProCapability:      test.capability,
				}},
			})

			// When
			modelsReq := httptest.NewRequest(http.MethodGet, "/openai/v1/models", nil)
			modelsReq.Header.Set("Authorization", "Bearer test-key")
			modelsRec := httptest.NewRecorder()
			server.ServeHTTP(modelsRec, modelsReq)
			request := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{"model":"`+test.model+`","input":"hello"}`))
			request.Header.Set("Authorization", "Bearer test-key")
			request.Header.Set("Content-Type", "application/json")
			responseRec := httptest.NewRecorder()
			server.ServeHTTP(responseRec, request)

			// Then
			if modelsRec.Code != http.StatusOK {
				t.Fatalf("expected provider /models status 200, got %d body=%s", modelsRec.Code, modelsRec.Body.String())
			}
			listedIDs := modelIDsFromRewrittenBody(t, modelsRec.Body.Bytes())
			if test.capability == config.ReasoningModeProCapabilityUnsupported {
				for _, unexpected := range []string{"model-pro", "model-low-pro"} {
					if contains(listedIDs, unexpected) {
						t.Fatalf("expected unsupported variant %q to stay hidden, got %#v", unexpected, listedIDs)
					}
				}
			} else {
				positions := map[string]int{}
				for _, expected := range []string{"model", "model-pro", "model-low", "model-low-pro"} {
					matches := 0
					for index, listedID := range listedIDs {
						if listedID == expected {
							matches++
							positions[expected] = index
						}
					}
					if matches != 1 {
						t.Fatalf("expected displayed model %q exactly once, got %#v", expected, listedIDs)
					}
				}
				if positions["model"] >= positions["model-pro"] || positions["model-low"] >= positions["model-low-pro"] {
					t.Fatalf("expected canonical model order to stay stable, got %#v", listedIDs)
				}
			}
			for _, unexpected := range []string{"model-noprompt", "model-pro-noprompt", "model-ultra", "model-pro-ultra"} {
				if contains(listedIDs, unexpected) {
					t.Fatalf("expected no generated private variant %q, got %#v", unexpected, listedIDs)
				}
			}
			if responseRec.Code != test.wantStatus {
				t.Fatalf("expected response status %d, got %d body=%s", test.wantStatus, responseRec.Code, responseRec.Body.String())
			}
			if test.wantStatus == http.StatusBadRequest && !strings.Contains(responseRec.Body.String(), "invalid_model") {
				t.Fatalf("expected invalid_model for unlisted variant, got %s", responseRec.Body.String())
			}
			if got := responseHits.Load(); got != test.wantHits {
				t.Fatalf("expected %d upstream responses calls, got %d", test.wantHits, got)
			}
			if test.wantModel != "" {
				mu.Lock()
				gotModel, gotMode, gotEffort := seenModel, seenMode, seenEffort
				mu.Unlock()
				if gotModel != test.wantModel || gotMode != "pro" || gotEffort != test.wantEffort {
					t.Fatalf("expected upstream model %q with pro reasoning effort %q, got model=%q mode=%q effort=%q", test.wantModel, test.wantEffort, gotModel, gotMode, gotEffort)
				}
			}
		})
	}
}

func TestSecurity_DefaultGroupAllowsRegexModelMapHitOutsideVisibleModels(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	server := NewServer(testLegacyModelRoutingConfig(alpha.URL, beta.URL))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"owned-999","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected regex MODEL_MAP hit outside visible models to be accepted, got %d body=%s", rec.Code, rec.Body.String())
	}
	if alpha.Hits() != 1 || beta.Hits() != 0 {
		t.Fatalf("expected only alpha upstream hit for regex MODEL_MAP hit, alpha=%d beta=%d", alpha.Hits(), beta.Hits())
	}
}

func TestSecurity_DefaultGroupAllowsRegexOnlyMappingOutsideVisibleModels(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	cfg := testLegacyModelRoutingConfig(alpha.URL, beta.URL)
	cfg.Providers[0].ModelMap = []config.ModelMapEntry{config.NewModelMapEntry("#re:owned-(.*)", "alpha-$1-upstream")}
	cfg.Providers[0].ManualModels = []string{"visible-alpha"}
	cfg.Providers[1].ModelMap = nil
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"owned-999","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected regex-only MODEL_MAP hit outside visible models to be accepted, got %d body=%s", rec.Code, rec.Body.String())
	}
	if alpha.Hits() != 1 || beta.Hits() != 0 {
		t.Fatalf("expected only alpha upstream hit for regex-only MODEL_MAP hit, alpha=%d beta=%d", alpha.Hits(), beta.Hits())
	}
}

func TestSecurity_DefaultGroupAllowsStaticModelMapAliasOutsideVisibleModels(t *testing.T) {
	upstream, responsesHits, seenModel := newModelVisibilityAliasUpstream(t)
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			SupportsModels:       true,
			ModelMap:             []config.ModelMapEntry{config.NewModelMapEntry("client-alias", "upstream-real")},
			ManualModels:         []string{"listed-model"},
		}},
	})

	modelsReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	modelsRec := httptest.NewRecorder()
	server.ServeHTTP(modelsRec, modelsReq)
	if modelsRec.Code != http.StatusOK {
		t.Fatalf("expected bare models request 200, got %d body=%s", modelsRec.Code, modelsRec.Body.String())
	}
	if strings.Contains(modelsRec.Body.String(), "client-alias") {
		t.Fatalf("expected static MODEL_MAP alias to stay hidden from /models, got %s", modelsRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"client-alias","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected static MODEL_MAP alias outside visible models to be accepted, got %d body=%s", rec.Code, rec.Body.String())
	}
	if responsesHits.Load() != 1 {
		t.Fatalf("expected one upstream responses hit, got %d", responsesHits.Load())
	}
	if gotModel := seenModel(); gotModel != "upstream-real" {
		t.Fatalf("expected upstream request model upstream-real, got %q", gotModel)
	}
}

func TestSecurity_ExplicitProviderAllowsStaticModelMapAliasOutsideVisibleModels(t *testing.T) {
	upstream, responsesHits, seenModel := newModelVisibilityAliasUpstream(t)
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			SupportsModels:       true,
			ModelMap:             []config.ModelMapEntry{config.NewModelMapEntry("client-alias", "upstream-real")},
			ManualModels:         []string{"listed-model"},
		}},
	})

	modelsReq := httptest.NewRequest(http.MethodGet, "/openai/v1/models", nil)
	modelsReq.Header.Set("Authorization", "Bearer test-key")
	modelsRec := httptest.NewRecorder()
	server.ServeHTTP(modelsRec, modelsReq)
	if modelsRec.Code != http.StatusOK {
		t.Fatalf("expected explicit provider models request 200, got %d body=%s", modelsRec.Code, modelsRec.Body.String())
	}
	if strings.Contains(modelsRec.Body.String(), "client-alias") {
		t.Fatalf("expected static MODEL_MAP alias to stay hidden from provider /models, got %s", modelsRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{"model":"client-alias","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected explicit static MODEL_MAP alias outside visible models to be accepted, got %d body=%s", rec.Code, rec.Body.String())
	}
	if responsesHits.Load() != 1 {
		t.Fatalf("expected one upstream responses hit, got %d", responsesHits.Load())
	}
	if gotModel := seenModel(); gotModel != "upstream-real" {
		t.Fatalf("expected upstream request model upstream-real, got %q", gotModel)
	}
}

func TestSecurity_TaggedDefaultGroupAllowsStaticModelMapAliasOutsideVisibleModels(t *testing.T) {
	upstream, responsesHits, seenModel := newModelVisibilityAliasUpstream(t)
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:                   "openai",
		EnableLegacyV1Routes:              true,
		EnableDefaultProviderModelTags:    true,
		EnableAllDefaultProviderModelTags: true,
		DownstreamNonStreamStrategy:       config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			SupportsModels:       true,
			ModelMap:             []config.ModelMapEntry{config.NewModelMapEntry("client-alias", "upstream-real")},
			ManualModels:         []string{"listed-model"},
		}},
	})

	modelsReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	modelsRec := httptest.NewRecorder()
	server.ServeHTTP(modelsRec, modelsReq)
	if modelsRec.Code != http.StatusOK {
		t.Fatalf("expected tagged bare models request 200, got %d body=%s", modelsRec.Code, modelsRec.Body.String())
	}
	if strings.Contains(modelsRec.Body.String(), "client-alias") {
		t.Fatalf("expected static MODEL_MAP alias to stay hidden from tagged /models, got %s", modelsRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"[openai]client-alias","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected tagged static MODEL_MAP alias outside visible models to be accepted, got %d body=%s", rec.Code, rec.Body.String())
	}
	if responsesHits.Load() != 1 {
		t.Fatalf("expected one upstream responses hit, got %d", responsesHits.Load())
	}
	if gotModel := seenModel(); gotModel != "upstream-real" {
		t.Fatalf("expected upstream request model upstream-real, got %q", gotModel)
	}
}

func TestSecurity_DefaultGroupAllowsModelMapAliasFromExplicitReasoningEffortOutsideVisibleModels(t *testing.T) {
	upstream, responsesHits, seenModel := newModelVisibilityAliasUpstream(t)
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			SupportsModels:       true,
			ModelMap:             []config.ModelMapEntry{config.NewModelMapEntry("client-gpt-high", "upstream-priority")},
			ManualModels:         []string{"listed-model"},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"client-gpt","input":"hello","reasoning":{"effort":"high"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected explicit reasoning effort MODEL_MAP source outside visible models to be accepted, got %d body=%s", rec.Code, rec.Body.String())
	}
	if responsesHits.Load() != 1 {
		t.Fatalf("expected one upstream responses hit, got %d", responsesHits.Load())
	}
	if gotModel := seenModel(); gotModel != "upstream-priority" {
		t.Fatalf("expected upstream request model upstream-priority, got %q", gotModel)
	}
}

func TestSecurity_DefaultGroupRejectsNoPromptModelMapAliasWithoutReasoningButAllowsWithReasoning(t *testing.T) {
	upstream, responsesHits, seenModel := newModelVisibilityAliasUpstream(t)
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		EnableNoPromptModelSuffix:   true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			UpstreamBaseURL:             upstream.URL,
			UpstreamAPIKey:              "test-key",
			UpstreamEndpointType:        config.UpstreamEndpointTypeResponses,
			SupportsResponses:           true,
			SupportsModels:              true,
			EnableReasoningEffortSuffix: true,
			ModelMap: []config.ModelMapEntry{
				config.NewModelMapEntry("client-gpt-high", "upstream-priority"),
			},
			ManualModels: []string{"listed-model"},
		}},
	})

	noReasonReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"client-gpt-noprompt","input":"hello"}`))
	noReasonReq.Header.Set("Content-Type", "application/json")
	noReasonRec := httptest.NewRecorder()
	server.ServeHTTP(noReasonRec, noReasonReq)

	if noReasonRec.Code != http.StatusBadRequest {
		t.Fatalf("expected noprompt alias without reasoning to be rejected by visible-set logic, got %d body=%s", noReasonRec.Code, noReasonRec.Body.String())
	}
	if !strings.Contains(noReasonRec.Body.String(), "invalid_model") {
		t.Fatalf("expected invalid_model without reasoning, got %s", noReasonRec.Body.String())
	}

	reasonReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"client-gpt-noprompt","input":"hello","reasoning":{"effort":"high"}}`))
	reasonReq.Header.Set("Content-Type", "application/json")
	reasonRec := httptest.NewRecorder()
	server.ServeHTTP(reasonRec, reasonReq)

	if reasonRec.Code != http.StatusOK {
		t.Fatalf("expected noprompt alias with reasoning to be accepted, got %d body=%s", reasonRec.Code, reasonRec.Body.String())
	}
	if responsesHits.Load() != 1 {
		t.Fatalf("expected one upstream responses hit after allowing reasoning case, got %d", responsesHits.Load())
	}
	if gotModel := seenModel(); gotModel != "upstream-priority" {
		t.Fatalf("expected upstream request model upstream-priority, got %q", gotModel)
	}
}

func TestSecurity_DefaultGroupAllowsModelMapAliasWithNoPromptSuffixOutsideVisibleModels(t *testing.T) {
	upstream, responsesHits, seenModel := newModelVisibilityAliasUpstream(t)
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		EnableNoPromptModelSuffix:   true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			UpstreamBaseURL:             upstream.URL,
			UpstreamAPIKey:              "test-key",
			UpstreamEndpointType:        config.UpstreamEndpointTypeResponses,
			SupportsResponses:           true,
			SupportsModels:              true,
			ModelMap:                    []config.ModelMapEntry{config.NewModelMapEntry("client-gpt-high", "upstream-priority")},
			ManualModels:                []string{"listed-model"},
			EnableReasoningEffortSuffix: true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"client-gpt-noprompt-high","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected noprompt reasoning MODEL_MAP source outside visible models to be accepted, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get(headerClientToProxyNoPrompt) != "true" {
		t.Fatalf("expected noprompt header true, got %q", rec.Header().Get(headerClientToProxyNoPrompt))
	}
	if responsesHits.Load() != 1 {
		t.Fatalf("expected one upstream responses hit, got %d", responsesHits.Load())
	}
	if gotModel := seenModel(); gotModel != "upstream-priority" {
		t.Fatalf("expected upstream request model upstream-priority, got %q", gotModel)
	}
}

func TestSecurity_DefaultGroupAllowsBaseNoPromptModelWithoutReasoningOutsideVisibleModels(t *testing.T) {
	upstream, responsesHits, seenModel := newModelVisibilityAliasUpstream(t)
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		EnableNoPromptModelSuffix:   true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			SupportsModels:       true,
			ManualModels:         []string{"gpt-5.4-mini"},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4-mini-noprompt","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected base noprompt model without reasoning to be accepted, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get(headerClientToProxyNoPrompt) != "true" {
		t.Fatalf("expected noprompt header true, got %q", rec.Header().Get(headerClientToProxyNoPrompt))
	}
	if responsesHits.Load() != 1 {
		t.Fatalf("expected one upstream responses hit, got %d", responsesHits.Load())
	}
	if gotModel := seenModel(); gotModel != "gpt-5.4-mini" {
		t.Fatalf("expected upstream request model gpt-5.4-mini, got %q", gotModel)
	}
}

func TestSecurity_DefaultGroupAllowsRegexWinningModelMapDespiteStaticOverlap(t *testing.T) {
	var mu sync.Mutex
	seenModel := ""
	var responsesHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"listed-model","object":"model"}]}`))
		case "/responses":
			responsesHits.Add(1)
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			mu.Lock()
			seenModel, _ = payload["model"].(string)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_openai","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			SupportsModels:       true,
			ModelMap: []config.ModelMapEntry{
				config.NewModelMapEntry("client-alias", "static-target"),
				config.NewModelMapEntry("#re:client-(.*)", "regex-$1-target"),
			},
			ManualModels: []string{"listed-model"},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"client-alias","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected regex winning MODEL_MAP entry outside visible models to be accepted, got %d body=%s", rec.Code, rec.Body.String())
	}
	if responsesHits.Load() != 1 {
		t.Fatalf("expected one upstream hit for regex winning MODEL_MAP entry, got %d", responsesHits.Load())
	}
	mu.Lock()
	gotModel := seenModel
	mu.Unlock()
	if gotModel != "regex-alias-target" {
		t.Fatalf("expected upstream request model regex-alias-target, got %q", gotModel)
	}
}

func TestSecurity_DefaultGroupRejectsStaticModelMapAliasToHiddenTarget(t *testing.T) {
	var responsesHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"listed-model","object":"model"}]}`))
		case "/responses":
			responsesHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_openai","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			SupportsModels:       true,
			ModelMap:             []config.ModelMapEntry{config.NewModelMapEntry("client-alias", "hidden-target")},
			ManualModels:         []string{"listed-model"},
			HiddenModels:         []string{"hidden-target"},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"client-alias","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected static MODEL_MAP alias to hidden target to be rejected, got %d body=%s", rec.Code, rec.Body.String())
	}
	if responsesHits.Load() != 0 {
		t.Fatalf("expected no upstream hit for hidden target alias, got %d", responsesHits.Load())
	}
}

func TestSecurity_DefaultGroupAllowsTaggedRegexModelMapHitOutsideVisibleModels(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	cfg := testLegacyModelRoutingConfig(alpha.URL, beta.URL)
	cfg.EnableDefaultProviderModelTags = true
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"[alpha]owned-999","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected tagged regex MODEL_MAP hit outside visible models to be accepted, got %d body=%s", rec.Code, rec.Body.String())
	}
	if alpha.Hits() != 1 || beta.Hits() != 0 {
		t.Fatalf("expected only alpha upstream hit for tagged regex MODEL_MAP hit, alpha=%d beta=%d", alpha.Hits(), beta.Hits())
	}
}

func TestSecurity_ExplicitProviderHiddenUpstreamModelCannotBeRequested(t *testing.T) {
	var responsesHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"public-model","object":"model"},{"id":"admin-secret-model","object":"model"}]}`))
		case "/responses":
			responsesHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_openai","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			SupportsModels:       true,
			HiddenModels:         []string{"#re:admin-.*"},
		}},
	})

	modelsReq := httptest.NewRequest(http.MethodGet, "/openai/v1/models", nil)
	modelsReq.Header.Set("Authorization", "Bearer test-key")
	modelsRec := httptest.NewRecorder()
	server.ServeHTTP(modelsRec, modelsReq)
	if modelsRec.Code != http.StatusOK {
		t.Fatalf("expected explicit provider models request 200, got %d body=%s", modelsRec.Code, modelsRec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(modelsRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode models payload: %v body=%s", err, modelsRec.Body.String())
	}
	data, _ := payload["data"].([]any)
	for _, item := range data {
		entry, _ := item.(map[string]any)
		if got, _ := entry["id"].(string); got == "admin-secret-model" {
			t.Fatalf("expected hidden upstream model to be removed from /models list, got %s", modelsRec.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{"model":"admin-secret-model","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected hidden upstream model request to be rejected, got %d body=%s", rec.Code, rec.Body.String())
	}
	if responsesHits.Load() != 0 {
		t.Fatalf("expected no upstream responses hit for hidden model attack, got %d", responsesHits.Load())
	}
}

func TestSecurity_BareSingleDefaultProviderRejectsHiddenUpstreamModelRequest(t *testing.T) {
	var responsesHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"public-model","object":"model"},{"id":"admin-secret-model","object":"model"}]}`))
		case "/responses":
			responsesHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_openai","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			SupportsModels:       true,
			HiddenModels:         []string{"#re:admin-.*"},
		}},
	})

	modelsReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	modelsRec := httptest.NewRecorder()
	server.ServeHTTP(modelsRec, modelsReq)
	if modelsRec.Code != http.StatusOK {
		t.Fatalf("expected bare models request 200, got %d body=%s", modelsRec.Code, modelsRec.Body.String())
	}
	if strings.Contains(modelsRec.Body.String(), "admin-secret-model") {
		t.Fatalf("expected hidden upstream model removed from bare /v1/models, got %s", modelsRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"admin-secret-model","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected bare hidden upstream model request to be rejected, got %d body=%s", rec.Code, rec.Body.String())
	}
	if responsesHits.Load() != 0 {
		t.Fatalf("expected no upstream responses hit for bare hidden model attack, got %d", responsesHits.Load())
	}
}
