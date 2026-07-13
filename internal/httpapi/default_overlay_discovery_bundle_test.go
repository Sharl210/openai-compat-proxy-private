package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestLegacyResponsesRoute_reusesRealtimeDiscoveryBundleForModelAllowance(t *testing.T) {
	// Given
	var modelsHits atomic.Int32
	var generationHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			modelsHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.1","object":"model"}]}`))
		case "/responses":
			generationHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	server := NewServer(config.Config{
		DefaultProvider:             "realtime",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "realtime",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			SupportsModels:       true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.1","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := modelsHits.Load(); got != 1 {
		t.Fatalf("expected one request-scoped models discovery, got %d", got)
	}
	if got := generationHits.Load(); got != 1 {
		t.Fatalf("expected one generation request, got %d", got)
	}
}

func TestLegacyResponsesRoute_routesRealtimeExactModelBeforeProxyTailInterpretation(t *testing.T) {
	for _, modelName := range []string{"vendor-low", "vendor-pro", "vendor-noprompt"} {
		t.Run(modelName, func(t *testing.T) {
			// Given
			var modelsHits atomic.Int32
			var generationHits atomic.Int32
			naturalUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/models":
					modelsHits.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"` + modelName + `","object":"model"}]}`))
				case "/responses":
					generationHits.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"id":"resp_natural","object":"response","status":"completed","output":[]}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer naturalUpstream.Close()
			fallbackUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.NotFound(w, r)
			}))
			defer fallbackUpstream.Close()
			server := NewServer(config.Config{
				DefaultProvider:             "natural,fallback",
				EnableLegacyV1Routes:        true,
				DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
				Providers: []config.ProviderConfig{{
					ID:                   "natural",
					Enabled:              true,
					UpstreamBaseURL:      naturalUpstream.URL,
					UpstreamAPIKey:       "natural-key",
					UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
					SupportsResponses:    true,
					SupportsModels:       true,
				}, {
					ID:                   "fallback",
					Enabled:              true,
					UpstreamBaseURL:      fallbackUpstream.URL,
					UpstreamAPIKey:       "fallback-key",
					UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
					SupportsResponses:    true,
					SupportsModels:       true,
				}},
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"`+modelName+`","input":"hello"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			// When
			server.ServeHTTP(rec, req)

			// Then
			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("X-Provider-Name"); got != "natural" {
				t.Fatalf("expected natural provider, got %q", got)
			}
			if got := modelsHits.Load(); got != 1 {
				t.Fatalf("expected exactly one models lookup, got %d", got)
			}
			if got := generationHits.Load(); got != 1 {
				t.Fatalf("expected exactly one generation request, got %d", got)
			}
		})
	}
}

func TestLegacyResponsesRoute_usesLaterDefaultProviderForDuplicateRealtimeExactRawModel(t *testing.T) {
	// Given
	newUpstream := func(t *testing.T, responseID string) *httptest.Server {
		t.Helper()
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/models":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"vendor-noprompt-pro","object":"model"}]}`))
			case "/responses":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"` + responseID + `","object":"response","status":"completed","output":[]}`))
			default:
				http.NotFound(w, r)
			}
		}))
	}
	first := newUpstream(t, "resp_first")
	defer first.Close()
	second := newUpstream(t, "resp_second")
	defer second.Close()
	server := NewServer(config.Config{
		DefaultProvider:             "first,second",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "first",
			Enabled:              true,
			UpstreamBaseURL:      first.URL,
			UpstreamAPIKey:       "first-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			SupportsModels:       true,
		}, {
			ID:                   "second",
			Enabled:              true,
			UpstreamBaseURL:      second.URL,
			UpstreamAPIKey:       "second-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			SupportsModels:       true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"vendor-noprompt-pro","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Provider-Name"); got != "second" {
		t.Fatalf("expected later exact raw owner second, got %q", got)
	}
}
