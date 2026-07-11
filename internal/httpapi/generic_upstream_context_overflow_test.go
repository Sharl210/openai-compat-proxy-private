package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
)

const genericTemporaryUnavailableResponse = `{"error":{"message":"Upstream service temporarily unavailable","type":"upstream_error"}}`

const genericContextOverflowTestTokenFloor = 1_000_000

func TestRetryExhaustedGeneric502ReturnsContextOverflowAcrossDownstreamProtocols(t *testing.T) {
	for _, tc := range []struct {
		name       string
		path       string
		requestFor func(string) string
		setHeaders func(*http.Request)
	}{
		{
			name: "responses",
			path: "/v1/responses",
			requestFor: func(content string) string {
				return `{"model":"gpt-5.6-sol","stream":true,"input":[{"role":"user","content":"` + content + `"}]}`
			},
		},
		{
			name: "chat completions",
			path: "/v1/chat/completions",
			requestFor: func(content string) string {
				return `{"model":"gpt-5.6-sol","stream":true,"messages":[{"role":"user","content":"` + content + `"}]}`
			},
		},
		{
			name: "anthropic messages",
			path: "/v1/messages",
			requestFor: func(content string) string {
				return `{"model":"gpt-5.6-sol","stream":true,"max_tokens":128,"messages":[{"role":"user","content":"` + content + `"}]}`
			},
			setHeaders: func(req *http.Request) {
				req.Header.Set("anthropic-version", "2023-06-01")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			var attempts atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(genericTemporaryUnavailableResponse))
			}))
			defer upstream.Close()
			server := NewServer(generic502ContextOverflowConfig(upstream.URL, 1))
			largeContent := strings.Repeat("x", genericContextOverflowTestTokenFloor*4)
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.requestFor(largeContent)))
			req.Header.Set("Content-Type", "application/json")
			if tc.setHeaders != nil {
				tc.setHeaders(req)
			}
			rec := httptest.NewRecorder()

			// When
			server.ServeHTTP(rec, req)

			// Then
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected generic retry-exhausted 502 to normalize to 400, got %d body=%s", rec.Code, rec.Body.String())
			}
			if got := attempts.Load(); got != 2 {
				t.Fatalf("expected two upstream attempts, got %d", got)
			}
			body := rec.Body.String()
			if !strings.Contains(body, "context_length_exceeded") || !strings.Contains(body, "prompt is too long") {
				t.Fatalf("expected context overflow response, got %s", body)
			}
			if strings.Contains(body, "event: ") {
				t.Fatalf("expected pre-open HTTP error rather than downstream SSE, got %s", body)
			}
			estimatedTokens, err := strconv.Atoi(rec.Header().Get(headerProxyEstimatedInputTokens))
			if err != nil || estimatedTokens < genericContextOverflowTestTokenFloor {
				t.Fatalf("expected estimated input tokens at or above fallback floor, got %q", rec.Header().Get(headerProxyEstimatedInputTokens))
			}
			if got := rec.Header().Get(headerProxyModelLimitContextTokens); got != "-1" {
				t.Fatalf("expected disabled context limit header, got %q", got)
			}
		})
	}
}

func TestRetryExhaustedGeneric502PreservesNonQualifyingFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		retryCount int
		content    string
		status     int
		body       string
	}{
		{
			name:       "small request",
			retryCount: 1,
			content:    "hello",
			status:     http.StatusBadGateway,
			body:       genericTemporaryUnavailableResponse,
		},
		{
			name:       "no retries",
			retryCount: 0,
			content:    strings.Repeat("x", genericContextOverflowTestTokenFloor*4),
			status:     http.StatusBadGateway,
			body:       genericTemporaryUnavailableResponse,
		},
		{
			name:       "service unavailable",
			retryCount: 1,
			content:    strings.Repeat("x", genericContextOverflowTestTokenFloor*4),
			status:     http.StatusServiceUnavailable,
			body:       genericTemporaryUnavailableResponse,
		},
		{
			name:       "gateway timeout",
			retryCount: 1,
			content:    strings.Repeat("x", genericContextOverflowTestTokenFloor*4),
			status:     http.StatusGatewayTimeout,
			body:       genericTemporaryUnavailableResponse,
		},
		{
			name:       "quota signal",
			retryCount: 1,
			content:    strings.Repeat("x", genericContextOverflowTestTokenFloor*4),
			status:     http.StatusBadGateway,
			body:       `{"error":{"message":"Upstream service temporarily unavailable","type":"insufficient_quota"}}`,
		},
		{
			name:       "extra error field",
			retryCount: 1,
			content:    strings.Repeat("x", genericContextOverflowTestTokenFloor*4),
			status:     http.StatusBadGateway,
			body:       `{"error":{"code":"upstream_error","message":"Upstream service temporarily unavailable","type":"upstream_error"}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer upstream.Close()
			server := NewServer(generic502ContextOverflowConfig(upstream.URL, tc.retryCount))
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-sol","stream":true,"input":[{"role":"user","content":"`+tc.content+`"}]}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			// When
			server.ServeHTTP(rec, req)

			// Then
			if rec.Code != tc.status {
				t.Fatalf("expected status %d to be preserved, got %d body=%s", tc.status, rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "context_length_exceeded") || strings.Contains(rec.Body.String(), "prompt is too long") {
				t.Fatalf("expected non-qualifying upstream failure to remain unchanged, got %s", rec.Body.String())
			}
		})
	}
}

func generic502ContextOverflowConfig(upstreamURL string, retryCount int) config.Config {
	return config.Config{
		DefaultProvider:      "test",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                        "test",
			Enabled:                   true,
			ManualModels:              []string{"gpt-5.6-sol"},
			ModelLimitContextTokens:   -1,
			UpstreamBaseURL:           upstreamURL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      config.UpstreamEndpointTypeResponses,
			SupportsResponses:         true,
			SupportsChat:              true,
			SupportsAnthropicMessages: true,
			UpstreamRetryCountSet:     true,
			UpstreamRetryCount:        retryCount,
			UpstreamRetryDelaySet:     true,
			UpstreamRetryDelay:        time.Millisecond,
		}},
	}
}
