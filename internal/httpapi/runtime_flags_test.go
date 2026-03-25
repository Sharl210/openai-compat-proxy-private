package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/testutil"
)

func TestChatRejectsProviderWithoutChatSupport(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "openai",
			Enabled:         true,
			UpstreamBaseURL: "https://example.test",
			UpstreamAPIKey:  "test-key",
			SupportsChat:    false,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	assertUnsupportedProviderContract(t, rec, "provider does not support chat completions")
}

func TestResponsesRejectsProviderWithoutResponsesSupport(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   "https://example.test",
			UpstreamAPIKey:    "test-key",
			SupportsResponses: false,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	assertUnsupportedProviderContract(t, rec, "provider does not support responses")
}

func TestModelsRejectsProviderWithoutModelsSupport(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "openai",
			Enabled:         true,
			UpstreamBaseURL: "https://example.test",
			UpstreamAPIKey:  "test-key",
			SupportsModels:  false,
		}},
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	assertUnsupportedProviderContract(t, rec, "provider does not support models")
}

func TestLegacyV1RoutesDisabledReturnsNotFound(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: false,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsResponses: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func assertUnsupportedProviderContract(t *testing.T, rec *httptest.ResponseRecorder, message string) {
	t.Helper()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error response: %v body=%s", err, rec.Body.String())
	}
	errMap, _ := payload["error"].(map[string]any)
	if got, _ := errMap["code"].(string); got != "unsupported_provider_contract" {
		t.Fatalf("expected unsupported_provider_contract code, got %#v", payload)
	}
	if got, _ := errMap["message"].(string); got != message {
		t.Fatalf("expected message %q, got %#v", message, payload)
	}
}
