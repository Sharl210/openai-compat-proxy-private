package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestResponsesRejectsGetWithMethodNotAllowed(t *testing.T) {
	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			SupportsResponses: true,
		}},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer root-secret")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET /v1/responses, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Allow"); got != http.MethodPost {
		t.Fatalf("expected Allow=%q, got %q", http.MethodPost, got)
	}
}

func TestResponsesCompactRejectsGetWithMethodNotAllowed(t *testing.T) {
	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			SupportsResponses: true,
		}},
	})

	t.Run("legacy", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/responses/compact", nil)
		req.Header.Set("Authorization", "Bearer root-secret")
		rec := httptest.NewRecorder()

		server.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405 for GET /v1/responses/compact, got %d body=%s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Allow"); got != http.MethodPost {
			t.Fatalf("expected Allow=%q, got %q", http.MethodPost, got)
		}
	})

	t.Run("provider", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/openai/v1/responses/compact", nil)
		req.Header.Set("Authorization", "Bearer root-secret")
		rec := httptest.NewRecorder()

		server.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405 for GET /openai/v1/responses/compact, got %d body=%s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Allow"); got != http.MethodPost {
			t.Fatalf("expected Allow=%q, got %q", http.MethodPost, got)
		}
	})
}

func TestChatRejectsGetWithMethodNotAllowed(t *testing.T) {
	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:           "openai",
			Enabled:      true,
			SupportsChat: true,
		}},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer root-secret")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET /v1/chat/completions, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Allow"); got != http.MethodPost {
		t.Fatalf("expected Allow=%q, got %q", http.MethodPost, got)
	}
}

func TestMessagesRejectsGetWithMethodNotAllowed(t *testing.T) {
	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "claude",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                        "claude",
			Enabled:                   true,
			SupportsAnthropicMessages: true,
			AnthropicVersion:          "2023-06-01",
		}},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer root-secret")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET /v1/messages, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Allow"); got != http.MethodPost {
		t.Fatalf("expected Allow=%q, got %q", http.MethodPost, got)
	}
}

func TestModelsRejectsPostWithMethodNotAllowed(t *testing.T) {
	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:             "openai",
			Enabled:        true,
			SupportsModels: true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/models", strings.NewReader(`{"unused":true}`))
	req.Header.Set("Authorization", "Bearer root-secret")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for POST /v1/models, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("expected Allow=%q, got %q", http.MethodGet, got)
	}
}
