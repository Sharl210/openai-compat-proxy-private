package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

	server := NewServer(testResponsesConfig(upstream.URL))
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
	if got := rec.Body.String(); got != "rate limit from upstream" {
		t.Fatalf("expected upstream plain text body to be preserved, got %q", got)
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
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsResponses: true,
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
	if got := strings.TrimSpace(rec.Body.String()); got != `{"message":"upstream auth failed","detail":"bad key"}` {
		t.Fatalf("expected upstream JSON body to be preserved, got %q", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("expected json content type to be preserved, got %q", got)
	}
	if got := rec.Header().Get("X-Accel-Buffering"); got != "" {
		t.Fatalf("expected SSE headers to be absent when upstream rejected before stream start, got X-Accel-Buffering=%q", got)
	}
}
