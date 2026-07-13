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

func TestProxyModelIntentRouting_rejectsInvalidAndDisallowedModelsBeforeGeneration(t *testing.T) {
	tests := []struct {
		name         string
		model        string
		provider     config.ProviderConfig
		rootNoPrompt bool
	}{
		{
			name:     "partial tail",
			model:    "vendor-low-typo",
			provider: config.ProviderConfig{EnableReasoningEffortSuffix: true},
		},
		{
			name:  "disabled reasoning axis",
			model: "vendor-low",
		},
		{
			name:  "disabled noprompt axis",
			model: "vendor-noprompt",
		},
		{
			name:  "hidden map target",
			model: "alias",
			provider: config.ProviderConfig{
				ModelMap:     []config.ModelMapEntry{config.NewModelMapEntry("alias", "secret")},
				HiddenModels: []string{"secret"},
			},
		},
		{
			name:  "template mismatch",
			model: "vendor-low",
			provider: config.ProviderConfig{
				EnableReasoningEffortSuffix: true,
				ModelIDTemplate:             "packy-{{model}}-vip",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			var generationHits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/responses" {
					generationHits.Add(1)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[]}`))
			}))
			defer upstream.Close()
			provider := test.provider
			provider.ID = "packy"
			provider.Enabled = true
			provider.UpstreamBaseURL = upstream.URL
			provider.UpstreamAPIKey = "test-key"
			provider.UpstreamEndpointType = config.UpstreamEndpointTypeResponses
			provider.SupportsResponses = true
			provider.ManualModels = []string{"vendor"}
			server := NewServer(config.Config{
				DefaultProvider:             "packy",
				EnableLegacyV1Routes:        true,
				EnableNoPromptModelSuffix:   test.rootNoPrompt,
				DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
				Providers:                   []config.ProviderConfig{provider},
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"`+test.model+`","input":"hello"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			// When
			server.ServeHTTP(rec, req)

			// Then
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_model") {
				t.Fatalf("expected invalid_model, got %d body=%s", rec.Code, rec.Body.String())
			}
			if got := generationHits.Load(); got != 0 {
				t.Fatalf("expected no generation upstream hit, got %d", got)
			}
		})
	}
}

func TestDefaultOverlayRealtimeDiscovery_skipsProxyTailsAndTaggedRequests(t *testing.T) {
	tests := []struct {
		name   string
		model  string
		tagged bool
	}{
		{name: "proxy tail", model: "unknown-low"},
		{name: "tagged request", model: "[packy]unknown", tagged: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			var modelsHits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/models" {
					modelsHits.Add(1)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"unknown","object":"model"}]}`))
			}))
			defer upstream.Close()
			server := NewServer(config.Config{
				DefaultProvider:                   "packy",
				EnableLegacyV1Routes:              true,
				EnableDefaultProviderModelTags:    test.tagged,
				EnableAllDefaultProviderModelTags: test.tagged,
				Providers: []config.ProviderConfig{{
					ID:                   "packy",
					Enabled:              true,
					UpstreamBaseURL:      upstream.URL,
					UpstreamAPIKey:       "test-key",
					UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
					SupportsResponses:    true,
					SupportsModels:       true,
					ManualModels:         []string{"listed"},
				}},
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"`+test.model+`","input":"hello"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			// When
			server.ServeHTTP(rec, req)

			// Then
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_model") {
				t.Fatalf("expected invalid_model, got %d body=%s", rec.Code, rec.Body.String())
			}
			if got := modelsHits.Load(); got != 0 {
				t.Fatalf("expected no realtime models lookup, got %d", got)
			}
		})
	}
}

func TestExplicitProviderRoute_rejectsHiddenProxyTailModelMapAlias(t *testing.T) {
	// Given
	var generationHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/responses" {
			generationHits.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[]}`))
	}))
	defer upstream.Close()
	server := NewServer(config.Config{Providers: []config.ProviderConfig{{
		ID:                          "packy",
		Enabled:                     true,
		UpstreamBaseURL:             upstream.URL,
		UpstreamAPIKey:              "test-key",
		UpstreamEndpointType:        config.UpstreamEndpointTypeResponses,
		SupportsResponses:           true,
		EnableReasoningEffortSuffix: true,
		ModelMap:                    []config.ModelMapEntry{config.NewModelMapEntry("alias", "upstream")},
		HiddenModels:                []string{"alias-low"},
	}}})
	req := httptest.NewRequest(http.MethodPost, "/packy/v1/responses", strings.NewReader(`{"model":"alias-low","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_model") {
		t.Fatalf("expected invalid_model, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := generationHits.Load(); got != 0 {
		t.Fatalf("expected no generation request, got %d", got)
	}
}

func TestExplicitProviderRoute_usesEffectiveReasoningModeSuffix(t *testing.T) {
	// Given
	var generationHits atomic.Int32
	var routedModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/responses" {
			generationHits.Add(1)
			var payload struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode upstream request: %v", err)
			}
			routedModel = payload.Model
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[]}`))
	}))
	defer upstream.Close()

	tests := []struct {
		name                     string
		rootReasoningMode        bool
		rootReasoningModeSet     bool
		providerReasoningMode    bool
		providerReasoningModeSet bool
		wantStatus               int
		wantGenerationHits       int32
		wantRoutedModel          string
	}{
		{
			name:                 "root false provider unset rejects",
			rootReasoningModeSet: true,
			rootReasoningMode:    false,
			wantStatus:           http.StatusBadRequest,
		},
		{
			name:                     "provider true overrides root false",
			rootReasoningModeSet:     true,
			rootReasoningMode:        false,
			providerReasoningModeSet: true,
			providerReasoningMode:    true,
			wantStatus:               http.StatusOK,
			wantGenerationHits:       1,
			wantRoutedModel:          "vendor",
		},
		{
			name:                     "provider false overrides root true",
			rootReasoningModeSet:     true,
			rootReasoningMode:        true,
			providerReasoningModeSet: true,
			providerReasoningMode:    false,
			wantStatus:               http.StatusBadRequest,
		},
		{
			name:               "root and provider unset use enabled default",
			wantStatus:         http.StatusOK,
			wantGenerationHits: 1,
			wantRoutedModel:    "vendor",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// When
			generationHitsBefore := generationHits.Load()
			server := NewServer(config.Config{
				EnableReasoningModeSuffix:    test.rootReasoningMode,
				EnableReasoningModeSuffixSet: test.rootReasoningModeSet,
				Providers: []config.ProviderConfig{{
					ID:                           "packy",
					Enabled:                      true,
					UpstreamBaseURL:              upstream.URL,
					UpstreamAPIKey:               "test-key",
					UpstreamEndpointType:         config.UpstreamEndpointTypeResponses,
					SupportsResponses:            true,
					ManualModels:                 []string{"vendor"},
					EnableReasoningModeSuffix:    test.providerReasoningMode,
					EnableReasoningModeSuffixSet: test.providerReasoningModeSet,
				}},
			})
			req := httptest.NewRequest(http.MethodPost, "/packy/v1/responses", strings.NewReader(`{"model":"vendor-pro","input":"hello"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer test-key")
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			// Then
			if rec.Code != test.wantStatus {
				t.Fatalf("expected status %d, got %d body=%s", test.wantStatus, rec.Code, rec.Body.String())
			}
			if test.wantStatus == http.StatusBadRequest && !strings.Contains(rec.Body.String(), "invalid_model") {
				t.Fatalf("expected invalid_model, got body=%s", rec.Body.String())
			}
			if got := generationHits.Load() - generationHitsBefore; got != test.wantGenerationHits {
				t.Fatalf("expected generation hits %d, got %d", test.wantGenerationHits, got)
			}
			if test.wantRoutedModel != "" && routedModel != test.wantRoutedModel {
				t.Fatalf("expected routed model %q, got %q", test.wantRoutedModel, routedModel)
			}
		})
	}
}
