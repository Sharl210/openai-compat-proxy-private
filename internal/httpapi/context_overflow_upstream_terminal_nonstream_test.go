package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/testutil"
)

func TestNonStreamingContextOverflow_preservesFailure_whenUpstreamSendsCreatedThenErrorThenResponseFailed(t *testing.T) {
	for _, tc := range contextOverflowRouteCases() {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			upstream := testutil.NewStreamingUpstream(t, []string{
				upstreamContextOverflowCreatedEvent,
				upstreamContextOverflowInProgressEvent,
				upstreamContextOverflowEvent,
				upstreamContextOverflowFailedEvent,
			})
			defer upstream.Close()
			server := NewServer(testContextOverflowStreamConfig(upstream.URL))
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.nonStreamBody))
			req.Header.Set("Content-Type", "application/json")
			if tc.setHeaders != nil {
				tc.setHeaders(req)
			}
			rec := httptest.NewRecorder()

			// When
			server.ServeHTTP(rec, req)

			// Then
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected buffered upstream overflow to return HTTP 400, got %d body=%s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			assertContextOverflowSignals(t, body)
			if !strings.Contains(body, tc.nonStreamMarker) {
				t.Fatalf("expected protocol envelope marker %q, got %s", tc.nonStreamMarker, body)
			}
			if strings.Contains(body, `"code":"upstream_error"`) || strings.Contains(body, `"code":"invalid_upstream_stream"`) {
				t.Fatalf("expected buffered overflow not to degrade into a generic proxy failure, got %s", body)
			}
		})
	}
}

func TestNonStreamingContextOverflow_normalizesUpstreamHTTP502AcrossProtocols(t *testing.T) {
	for _, tc := range contextOverflowRouteCases() {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/responses" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"error":{"message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"upstream_error"}}`))
			}))
			defer upstream.Close()
			server := NewServer(config.Config{
				DefaultProvider:             "openai",
				EnableLegacyV1Routes:        true,
				DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
				Providers: []config.ProviderConfig{{
					ID:                        "openai",
					Enabled:                   true,
					UpstreamBaseURL:           upstream.URL,
					UpstreamAPIKey:            "test-key",
					UpstreamEndpointType:      config.UpstreamEndpointTypeResponses,
					SupportsResponses:         true,
					SupportsChat:              true,
					SupportsAnthropicMessages: true,
					UpstreamRetryCountSet:     true,
					UpstreamRetryCount:        0,
					UpstreamRetryDelaySet:     true,
					UpstreamRetryDelay:        time.Millisecond,
				}},
			})
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.nonStreamBody))
			req.Header.Set("Content-Type", "application/json")
			if tc.setHeaders != nil {
				tc.setHeaders(req)
			}
			rec := httptest.NewRecorder()

			// When
			server.ServeHTTP(rec, req)

			// Then
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected upstream context overflow to normalize to HTTP 400, got %d body=%s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			assertContextOverflowSignals(t, body)
			if !strings.Contains(body, tc.nonStreamMarker) {
				t.Fatalf("expected protocol envelope marker %q, got %s", tc.nonStreamMarker, body)
			}
		})
	}
}
