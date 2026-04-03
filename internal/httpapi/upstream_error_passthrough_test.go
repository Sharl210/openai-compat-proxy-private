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
			ID:                 "openai",
			Enabled:            true,
			UpstreamBaseURL:    upstream.URL,
			UpstreamAPIKey:     "test-key",
			SupportsResponses:  true,
			UpstreamRetryCount: 1,
			UpstreamRetryDelay: 10 * time.Millisecond,
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
			ID:                 "openai",
			Enabled:            true,
			UpstreamBaseURL:    upstream.URL,
			UpstreamAPIKey:     "test-key",
			SupportsResponses:  true,
			UpstreamRetryCount: 1,
			UpstreamRetryDelay: 10 * time.Millisecond,
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
		t.Fatalf("expected stream to stay in SSE protocol after placeholder prelude, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `event: response.output_item.added`) || !strings.Contains(body, "代理层占位") {
		t.Fatalf("expected early synthetic placeholder before upstream auth error, got %s", body)
	}
	if !strings.Contains(body, `event: response.incomplete`) || !strings.Contains(body, `"health_flag":"upstream_error"`) {
		t.Fatalf("expected SSE terminal upstream_error event, got %s", body)
	}
	if !strings.Contains(body, `upstream auth failed`) || !strings.Contains(body, `bad key`) {
		t.Fatalf("expected upstream error detail to remain in terminal SSE payload, got %s", body)
	}
	if attempts.Load() != 1 {
		t.Fatalf("expected unauthorized upstream error to skip retries, got %d attempts", attempts.Load())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("expected SSE content type after prelude starts, got %q", got)
	}
	if got := rec.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("expected SSE headers to remain present after prelude starts, got X-Accel-Buffering=%q", got)
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
			ID:                 "openai",
			Enabled:            true,
			UpstreamBaseURL:    upstream.URL,
			UpstreamAPIKey:     "test-key",
			SupportsResponses:  true,
			UpstreamRetryCount: 1,
			UpstreamRetryDelay: 10 * time.Millisecond,
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
