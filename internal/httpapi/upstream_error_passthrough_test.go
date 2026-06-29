package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
)

func TestResponsesNonStreamPassesThroughPlainTextUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate limit from upstream"))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:          "openai",
		EnableLegacyV1Routes:     true,
		UpstreamThinkingTagStyle: config.UpstreamThinkingTagStyleLegacy,
		Providers: []config.ProviderConfig{{
			ID:                    "openai",
			Enabled:               true,
			UpstreamBaseURL:       upstream.URL,
			UpstreamAPIKey:        "test-key",
			SupportsResponses:     true,
			UpstreamRetryCountSet: true,
			UpstreamRetryCount:    1,
			UpstreamRetryDelaySet: true,
			UpstreamRetryDelay:    10 * time.Millisecond,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"input":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected upstream status 429 to be preserved, got %d body=%s", rec.Code, rec.Body.String())
	}
	expectedBody := "本代理层已重试1遍，每次重试间隔0.01秒，共重试了0.01秒。下面是上游错误原信息：rate limit from upstream"
	if got := rec.Body.String(); got != expectedBody {
		t.Fatalf("expected plain text body with retry notice, got %q", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("expected plain text content type to be preserved, got %q", got)
	}
}

func TestResponsesNonStreamReturnsRequestValidationErrorAsInvalidRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("expected request validation to fail before upstream call, got %s", r.URL.Path)
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "anthropic",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                        "anthropic",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      config.UpstreamEndpointTypeAnthropic,
			SupportsResponses:         true,
			SupportsAnthropicMessages: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"claude-sonnet-4-5",
		"input":[{"role":"user","content":"hello"}],
		"context_management":{"edits":[{"type":"unsupported_edit"}]}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected request validation error to return 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"code":"invalid_request"`) || !strings.Contains(body, "unsupported context_management edit type: unsupported_edit") {
		t.Fatalf("expected concrete invalid_request validation error, got %s", body)
	}
	if strings.Contains(body, `"code":"upstream_error"`) || strings.Contains(body, `"code":"upstream_timeout"`) {
		t.Fatalf("expected validation error not to be collapsed into generic upstream error, got %s", body)
	}
}

func TestResponsesNonStreamPreservesSpecificUpstreamJSONErrors(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       int
		body         string
		wantContains string
	}{
		{
			name:         "model not found",
			status:       http.StatusNotFound,
			body:         `{"error":{"message":"model not found","type":"invalid_request_error","code":"model_not_found"}}`,
			wantContains: "model_not_found",
		},
		{
			name:         "unsupported parameter",
			status:       http.StatusBadRequest,
			body:         `{"error":{"message":"unsupported parameter: response_format","type":"invalid_request_error","code":"unsupported_parameter"}}`,
			wantContains: "unsupported_parameter",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/responses" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer upstream.Close()

			server := NewServer(config.Config{
				DefaultProvider:             "openai",
				EnableLegacyV1Routes:        true,
				DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
				Providers: []config.ProviderConfig{{
					ID:                    "openai",
					Enabled:               true,
					UpstreamBaseURL:       upstream.URL,
					UpstreamAPIKey:        "test-key",
					UpstreamEndpointType:   config.UpstreamEndpointTypeResponses,
					SupportsResponses:     true,
					UpstreamRetryCountSet: true,
					UpstreamRetryCount:    1,
					UpstreamRetryDelaySet: true,
					UpstreamRetryDelay:    10 * time.Millisecond,
				}},
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
				"model":"gpt-5",
				"input":[{"role":"user","content":"hello"}]
			}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			if rec.Code != tc.status {
				t.Fatalf("expected upstream status %d to be preserved, got %d body=%s", tc.status, rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if !strings.Contains(body, tc.wantContains) {
				t.Fatalf("expected concrete upstream error to be preserved, got %s", body)
			}
			if strings.Contains(body, `"code":"upstream_error"`) || strings.Contains(body, `"code":"upstream_timeout"`) {
				t.Fatalf("expected specific upstream error not to be collapsed into generic upstream error, got %s", body)
			}
		})
	}
}

func TestResponsesStreamReturnsUpstreamErrorBeforeStartingSSE(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"upstream auth failed","detail":"bad key"}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                    "openai",
			Enabled:               true,
			UpstreamBaseURL:       upstream.URL,
			UpstreamAPIKey:        "test-key",
			SupportsResponses:     true,
			UpstreamRetryCountSet: true,
			UpstreamRetryCount:    1,
			UpstreamRetryDelaySet: true,
			UpstreamRetryDelay:    10 * time.Millisecond,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"input":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected pre-open upstream status 401 to be preserved, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `event: response.output_item.added`) || strings.Contains(body, `"id":"rs_proxy"`) {
		t.Fatalf("expected pre-open upstream error not to start synthetic SSE prelude, got %s", body)
	}
	if strings.Contains(body, `event: response.incomplete`) || strings.Contains(body, `"health_flag":"upstream_error"`) {
		t.Fatalf("expected pre-open upstream error not to be converted into terminal SSE event, got %s", body)
	}
	if !strings.Contains(body, `upstream auth failed`) || !strings.Contains(body, `bad key`) {
		t.Fatalf("expected upstream error detail to remain in terminal SSE payload, got %s", body)
	}
	if attempts.Load() != 1 {
		t.Fatalf("expected unauthorized upstream error to skip retries, got %d attempts", attempts.Load())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("expected upstream JSON content type to be preserved, got %q", got)
	}
	if got := rec.Header().Get("X-Accel-Buffering"); got != "" {
		t.Fatalf("expected SSE headers not to be set before upstream opens, got X-Accel-Buffering=%q", got)
	}
}

func TestResponsesStreamRetriesBeforeFirstUpstreamEvent(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("try later"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                    "openai",
			Enabled:               true,
			UpstreamBaseURL:       upstream.URL,
			UpstreamAPIKey:        "test-key",
			SupportsResponses:     true,
			UpstreamRetryCountSet: true,
			UpstreamRetryCount:    1,
			UpstreamRetryDelaySet: true,
			UpstreamRetryDelay:    10 * time.Millisecond,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"input":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected retried stream to succeed, got %d body=%s", rec.Code, rec.Body.String())
	}
	if attempts.Load() != 2 {
		t.Fatalf("expected one retry before first upstream event, got %d attempts", attempts.Load())
	}
	if !strings.Contains(rec.Body.String(), "event: response.completed") {
		t.Fatalf("expected successful SSE body after retry, got %s", rec.Body.String())
	}
}
