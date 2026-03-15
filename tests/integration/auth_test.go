package integration_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/auth"
	"openai-compat-proxy/internal/config"
)

func TestProxyAPIKeyRequiredWhenConfigured(t *testing.T) {
	cfg := config.Default()
	cfg.ProxyAPIKey = "proxy-secret"

	server := newTestServerWithConfig(t, cfg)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"x","input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestUpstreamKeyFallsBackToServerDefault(t *testing.T) {
	cfg := config.Default()
	cfg.UpstreamAPIKey = "server-key"

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	resolved, err := auth.ResolveUpstreamAuthorization(req, cfg)
	if err != nil {
		t.Fatal(err)
	}

	if resolved != "Bearer server-key" {
		t.Fatalf("unexpected auth header: %q", resolved)
	}
}

func TestChatRouteEmitsNormalizationVersionHeader(t *testing.T) {
	server := newServerWithStubbedUpstream(t, httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {}\n\n"))
	})).URL)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"x","messages":[{"role":"user","content":"hi"}],"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("X-Proxy-Normalization-Version"); got != "v1" {
		t.Fatalf("expected normalization version v1, got %q", got)
	}
}

func TestResponsesRouteEmitsNormalizationVersionHeader(t *testing.T) {
	server := newServerWithStubbedUpstream(t, httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {}\n\n"))
	})).URL)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"x","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("X-Proxy-Normalization-Version"); got != "v1" {
		t.Fatalf("expected normalization version v1, got %q", got)
	}
}
