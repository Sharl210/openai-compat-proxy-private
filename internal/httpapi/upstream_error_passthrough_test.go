package httpapi

import (
	"encoding/json"
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
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
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

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected upstream status 401 to be preserved, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected JSON body, got decode error %v body=%s", err, rec.Body.String())
	}
	if got, _ := payload["detail"].(string); got != "bad key" {
		t.Fatalf("expected upstream detail to be preserved, got %#v", payload)
	}
	if got, _ := payload["message"].(string); got != "本代理层已重试1遍，每次重试间隔0.01秒，共重试了0.01秒。下面是上游错误原信息：upstream auth failed" {
		t.Fatalf("expected JSON message with retry notice, got %#v", payload)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("expected json content type to be preserved, got %q", got)
	}
	if got := rec.Header().Get("X-Accel-Buffering"); got != "" {
		t.Fatalf("expected SSE headers to be absent when upstream rejected before stream start, got X-Accel-Buffering=%q", got)
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
