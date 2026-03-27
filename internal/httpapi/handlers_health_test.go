package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestHealthzReturnsServiceUnavailableWhenDefaultProviderMissingUpstreamBaseURL(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:      "openai",
			Enabled: true,
		}},
	})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode health payload: %v body=%s", err, rec.Body.String())
	}
	if got, _ := payload["status"].(string); got != "error" {
		t.Fatalf("expected error status, got %#v", payload)
	}
	if got, _ := payload["error"].(string); got != "default provider must define UPSTREAM_BASE_URL" {
		t.Fatalf("unexpected error payload: %#v", payload)
	}
}

func TestHealthzStaysOKWithoutDefaultProviderWhenLegacyRoutesDisabled(t *testing.T) {
	server := NewServer(config.Config{
		EnableLegacyV1Routes: false,
		Providers: []config.ProviderConfig{{
			ID:              "openai",
			Enabled:         true,
			UpstreamBaseURL: "https://example.test",
			UpstreamAPIKey:  "test-key",
		}},
	})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); body != `{"status":"ok"}` {
		t.Fatalf("unexpected health response: %s", body)
	}
}
