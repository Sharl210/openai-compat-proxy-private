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
