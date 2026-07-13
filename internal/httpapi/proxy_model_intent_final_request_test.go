package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestRootModelMapTargetAxesControlFinalUpstreamRequest(t *testing.T) {
	// Given
	var upstreamPayload map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamPayload); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[]}`))
	}))
	defer upstream.Close()
	server := NewServer(config.Config{
		DefaultProvider:             "provider",
		DefaultProReasoningModeSet:  true,
		DefaultProReasoningMode:     false,
		EnableLegacyV1Routes:        true,
		EnableNoPromptModelSuffix:   true,
		UltraMaxConcurrentSubagents: 3,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		V1ModelMap: []config.ModelMapEntry{
			config.NewModelMapEntry("client", "upstream-low-pro"),
		},
		Providers: []config.ProviderConfig{{
			ID:                          "provider",
			Enabled:                     true,
			UpstreamBaseURL:             upstream.URL,
			UpstreamAPIKey:              "test-key",
			UpstreamEndpointType:        config.UpstreamEndpointTypeResponses,
			SupportsResponses:           true,
			SupportsResponsesMultiAgent: true,
			EnableReasoningEffortSuffix: true,
			ManualModels:                []string{"upstream"},
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"client-noprompt-ultra","reasoning":{"effort":"high"},"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamPayload["model"] != "upstream" {
		t.Fatalf("expected mapped base model upstream, got %#v", upstreamPayload)
	}
	reasoning, _ := upstreamPayload["reasoning"].(map[string]any)
	if reasoning["effort"] != "low" || reasoning["mode"] != "pro" {
		t.Fatalf("expected target effort and mode, got %#v", reasoning)
	}
	multiAgent, _ := upstreamPayload["multi_agent"].(map[string]any)
	if multiAgent["enabled"] != true || multiAgent["max_concurrent_subagents"] != float64(3) {
		t.Fatalf("expected client-private ultra axis preserved, got %#v", multiAgent)
	}
	if rec.Header().Get(headerClientToProxyNoPrompt) != "true" {
		t.Fatalf("expected client-private noprompt axis preserved, got %q", rec.Header().Get(headerClientToProxyNoPrompt))
	}
}

func TestRootModelMapBareTargetPreservesBodyReasoningEffort(t *testing.T) {
	// Given
	var upstreamPayload map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamPayload); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[]}`))
	}))
	defer upstream.Close()
	server := NewServer(config.Config{
		DefaultProvider:             "provider",
		DefaultProReasoningModeSet:  true,
		DefaultProReasoningMode:     false,
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		V1ModelMap: []config.ModelMapEntry{
			config.NewModelMapEntry("client", "upstream"),
		},
		Providers: []config.ProviderConfig{{
			ID:                          "provider",
			Enabled:                     true,
			UpstreamBaseURL:             upstream.URL,
			UpstreamAPIKey:              "test-key",
			UpstreamEndpointType:        config.UpstreamEndpointTypeResponses,
			SupportsResponses:           true,
			EnableReasoningEffortSuffix: true,
			ManualModels:                []string{"upstream"},
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"client","reasoning":{"effort":"high"},"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamPayload["model"] != "upstream" {
		t.Fatalf("expected mapped base model upstream, got %#v", upstreamPayload)
	}
	reasoning, _ := upstreamPayload["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" {
		t.Fatalf("expected body effort retained for bare target, got %#v", reasoning)
	}
}

func TestExplicitProviderRouteSkipsV1ModelMap(t *testing.T) {
	// Given
	var upstreamPayload map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamPayload); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[]}`))
	}))
	defer upstream.Close()
	server := NewServer(config.Config{
		DefaultProReasoningModeSet:  true,
		DefaultProReasoningMode:     false,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		V1ModelMap: []config.ModelMapEntry{
			config.NewModelMapEntry("client", "root-only-target"),
		},
		Providers: []config.ProviderConfig{{
			ID:                   "provider",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			ManualModels:         []string{"client"},
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/provider/v1/responses", strings.NewReader(`{"model":"client","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamPayload["model"] != "client" {
		t.Fatalf("expected explicit provider route to bypass V1_MODEL_MAP, got %#v", upstreamPayload)
	}
}
