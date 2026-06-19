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

func TestImagesGenerationsRejectsGetWithMethodNotAllowed(t *testing.T) {
	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:      "openai",
			Enabled: true,
		}},
	})

	for _, path := range []string{"/v1/images/generations", "/openai/v1/images/generations"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer root-secret")
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("expected 405 for GET %s, got %d body=%s", path, rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Allow"); got != http.MethodPost {
				t.Fatalf("expected Allow=%q, got %q", http.MethodPost, got)
			}
		})
	}
}

func TestImagesEditsRejectsGetWithMethodNotAllowed(t *testing.T) {
	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:      "openai",
			Enabled: true,
		}},
	})

	for _, path := range []string{"/v1/images/edits", "/openai/v1/images/edits"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer root-secret")
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("expected 405 for GET %s, got %d body=%s", path, rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Allow"); got != http.MethodPost {
				t.Fatalf("expected Allow=%q, got %q", http.MethodPost, got)
			}
		})
	}
}

func TestImagesVariationsRejectsGetWithMethodNotAllowed(t *testing.T) {
	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:      "openai",
			Enabled: true,
		}},
	})

	for _, path := range []string{"/v1/images/variations", "/openai/v1/images/variations"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer root-secret")
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("expected 405 for GET %s, got %d body=%s", path, rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Allow"); got != http.MethodPost {
				t.Fatalf("expected Allow=%q, got %q", http.MethodPost, got)
			}
		})
	}
}

func TestEmbeddingsRejectsGetWithMethodNotAllowed(t *testing.T) {
	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:      "openai",
			Enabled: true,
		}},
	})

	for _, path := range []string{"/v1/embeddings", "/openai/v1/embeddings"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer root-secret")
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("expected 405 for GET %s, got %d body=%s", path, rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Allow"); got != http.MethodPost {
				t.Fatalf("expected Allow=%q, got %q", http.MethodPost, got)
			}
		})
	}
}

func TestRerankRejectsGetWithMethodNotAllowed(t *testing.T) {
	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:      "openai",
			Enabled: true,
		}},
	})

	for _, path := range []string{"/v1/rerank", "/openai/v1/rerank"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer root-secret")
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("expected 405 for GET %s, got %d body=%s", path, rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Allow"); got != http.MethodPost {
				t.Fatalf("expected Allow=%q, got %q", http.MethodPost, got)
			}
		})
	}
}

func TestPublicRouteAliasesReachHandlersWithoutV1Segment(t *testing.T) {
	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                        "openai",
			Enabled:                   true,
			SupportsModels:            true,
			SupportsResponses:         true,
			SupportsChat:              true,
			SupportsAnthropicMessages: true,
			AnthropicVersion:          "2023-06-01",
		}},
	})

	tests := []struct {
		path        string
		method      string
		wantAllow   string
		requestBody string
	}{
		{path: "/models", method: http.MethodPost, wantAllow: http.MethodGet, requestBody: `{"unused":true}`},
		{path: "/responses", method: http.MethodGet, wantAllow: http.MethodPost},
		{path: "/responses/compact", method: http.MethodGet, wantAllow: http.MethodPost},
		{path: "/chat/completions", method: http.MethodGet, wantAllow: http.MethodPost},
		{path: "/messages", method: http.MethodGet, wantAllow: http.MethodPost},
		{path: "/images/generations", method: http.MethodGet, wantAllow: http.MethodPost},
		{path: "/images/edits", method: http.MethodGet, wantAllow: http.MethodPost},
		{path: "/images/variations", method: http.MethodGet, wantAllow: http.MethodPost},
		{path: "/embeddings", method: http.MethodGet, wantAllow: http.MethodPost},
		{path: "/rerank", method: http.MethodGet, wantAllow: http.MethodPost},
		{path: "/openai/models", method: http.MethodPost, wantAllow: http.MethodGet, requestBody: `{"unused":true}`},
		{path: "/openai/responses", method: http.MethodGet, wantAllow: http.MethodPost},
		{path: "/openai/responses/compact", method: http.MethodGet, wantAllow: http.MethodPost},
		{path: "/openai/chat/completions", method: http.MethodGet, wantAllow: http.MethodPost},
		{path: "/openai/messages", method: http.MethodGet, wantAllow: http.MethodPost},
		{path: "/openai/images/generations", method: http.MethodGet, wantAllow: http.MethodPost},
		{path: "/openai/images/edits", method: http.MethodGet, wantAllow: http.MethodPost},
		{path: "/openai/images/variations", method: http.MethodGet, wantAllow: http.MethodPost},
		{path: "/openai/embeddings", method: http.MethodGet, wantAllow: http.MethodPost},
		{path: "/openai/rerank", method: http.MethodGet, wantAllow: http.MethodPost},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			var body *strings.Reader
			if tc.requestBody == "" {
				body = strings.NewReader("")
			} else {
				body = strings.NewReader(tc.requestBody)
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			req.Header.Set("Authorization", "Bearer root-secret")
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("expected alias %s to reach handler and return 405, got %d body=%s", tc.path, rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Allow"); got != tc.wantAllow {
				t.Fatalf("expected Allow=%q, got %q", tc.wantAllow, got)
			}
		})
	}
}
