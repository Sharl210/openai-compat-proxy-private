package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestResponsesRouteDropsPromptCacheHintsOnlyWhenProviderAllows(t *testing.T) {
	for _, upstreamType := range []string{
		config.UpstreamEndpointTypeChat,
		config.UpstreamEndpointTypeAnthropic,
	} {
		for _, allowed := range []bool{false, true} {
			t.Run(upstreamType+"_allowed_"+strconv.FormatBool(allowed), func(t *testing.T) {
				upstreamHits := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
					upstreamHits++
					assertResponsesPromptCacheHintsAreAbsent(t, request)
					writeFeatureCompatibilityMatrixResponse(w, upstreamType)
				}))
				defer upstream.Close()

				server := NewServer(config.Config{
					DefaultProvider:             "openai",
					DefaultProReasoningModeSet:  true,
					DefaultProReasoningMode:     false,
					EnableLegacyV1Routes:        true,
					DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
					Providers: []config.ProviderConfig{{
						ID:                                "openai",
						Enabled:                           true,
						UpstreamBaseURL:                   upstream.URL,
						UpstreamAPIKey:                    "test-key",
						UpstreamEndpointType:              upstreamType,
						AllowResponsesPromptCacheHintDrop: allowed,
						SupportsResponses:                 true,
					}},
				})
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6","input":"hello","prompt_cache_key":"private-key","prompt_cache_options":{"retention":"24h"}}`))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()

				server.ServeHTTP(rec, req)

				if allowed {
					if rec.Code != http.StatusOK {
						t.Fatalf("expected configured prompt cache hint drop to allow request, got %d body=%s", rec.Code, rec.Body.String())
					}
					if upstreamHits != 1 {
						t.Fatalf("expected one upstream request when drop is allowed, got %d", upstreamHits)
					}
					return
				}

				if rec.Code != http.StatusBadRequest {
					t.Fatalf("expected default fail-closed response, got %d body=%s", rec.Code, rec.Body.String())
				}
				if upstreamHits != 0 {
					t.Fatalf("expected no upstream request when drop is not allowed, got %d", upstreamHits)
				}
			})
		}
	}
}

func assertResponsesPromptCacheHintsAreAbsent(t *testing.T, request *http.Request) {
	t.Helper()
	var payload map[string]any
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		t.Fatalf("decode upstream request: %v", err)
	}
	for _, field := range []string{"prompt_cache_key", "prompt_cache_options"} {
		if _, exists := payload[field]; exists {
			t.Fatalf("expected %s to be absent from non-Responses upstream request, got %#v", field, payload)
		}
	}
}
