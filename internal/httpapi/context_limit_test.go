package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/testutil"
)

func TestResponsesSuccessSetsModelLimitContextHeader(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                      "openai",
			Enabled:                 true,
			SupportsResponses:       true,
			ManualModels:            []string{"gpt-5.5"},
			ModelLimitContextTokens: -1,
			UpstreamEndpointType:    config.UpstreamEndpointTypeResponses,
			UpstreamBaseURL:         upstream.URL,
			UpstreamAPIKey:          "test-key",
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.5","input":"hello"}`))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerProxyModelLimitContextTokens); got != "-1" {
		t.Fatalf("expected context limit header -1, got %q", got)
	}
}

func TestResponsesContextLimitReturnsOpenAIOverflowShape(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                      "openai",
			Enabled:                 true,
			SupportsResponses:       true,
			ManualModels:            []string{"gpt-5.5"},
			ModelLimitContextTokens: 1,
			UpstreamEndpointType:    config.UpstreamEndpointTypeResponses,
			UpstreamBaseURL:         "https://upstream.invalid/v1",
			UpstreamAPIKey:          "test-key",
		}},
	})
	body := `{"model":"gpt-5.5","input":[{"role":"user","content":"` + strings.Repeat("hello ", 20) + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 context overflow, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerProxyModelLimitContextTokens); got != "1" {
		t.Fatalf("expected context limit header 1, got %q", got)
	}
	if !strings.Contains(rec.Body.String(), "context_length_exceeded") || !strings.Contains(rec.Body.String(), "prompt is too long") {
		t.Fatalf("expected opencode-compatible context overflow body, got %s", rec.Body.String())
	}
}

func TestResponsesContextLimitScopedRulesUseClientModelBeforeModelMap(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                      "openai",
			Enabled:                 true,
			SupportsResponses:       true,
			ManualModels:            []string{"client-gpt"},
			ModelMap:                []config.ModelMapEntry{config.NewModelMapEntry("client-gpt", "upstream-gpt")},
			ModelLimitContextTokens: -1,
			ModelLimitContextTokenRules: []config.ScopedIntRule{
				exactScopedRule("client-gpt", 1),
				exactScopedRule("upstream-gpt", 999999),
			},
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			UpstreamBaseURL:      "https://upstream.invalid/v1",
			UpstreamAPIKey:       "test-key",
		}},
	})
	body := `{"model":"client-gpt","input":[{"role":"user","content":"` + strings.Repeat("hello ", 20) + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 context overflow from client-model scoped rule, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerProxyModelLimitContextTokens); got != "1" {
		t.Fatalf("expected context limit header from client model rule, got %q", got)
	}
	if !strings.Contains(rec.Body.String(), "context_length_exceeded") {
		t.Fatalf("expected context overflow body, got %s", rec.Body.String())
	}
}

func TestChatContextLimitReturnsOpenAIOverflowShape(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                      "openai",
			Enabled:                 true,
			SupportsChat:            true,
			ManualModels:            []string{"gpt-5.5"},
			ModelLimitContextTokens: 1,
			UpstreamEndpointType:    config.UpstreamEndpointTypeChat,
			UpstreamBaseURL:         "https://upstream.invalid/v1",
			UpstreamAPIKey:          "test-key",
		}},
	})
	body := `{"model":"gpt-5.5","messages":[{"role":"user","content":"` + strings.Repeat("hello ", 20) + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 context overflow, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerProxyModelLimitContextTokens); got != "1" {
		t.Fatalf("expected context limit header 1, got %q", got)
	}
	if !strings.Contains(rec.Body.String(), "context_length_exceeded") || !strings.Contains(rec.Body.String(), "prompt is too long") {
		t.Fatalf("expected opencode-compatible context overflow body, got %s", rec.Body.String())
	}
}

func TestAnthropicContextLimitReturnsAnthropicOverflowShape(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                        "openai",
			Enabled:                   true,
			SupportsAnthropicMessages: true,
			ManualModels:              []string{"claude-sonnet-4-5"},
			ModelLimitContextTokens:   1,
			UpstreamEndpointType:      config.UpstreamEndpointTypeAnthropic,
			UpstreamBaseURL:           "https://upstream.invalid",
			UpstreamAPIKey:            "test-key",
		}},
	})
	body := `{"model":"claude-sonnet-4-5","max_tokens":128,"messages":[{"role":"user","content":"` + strings.Repeat("hello ", 20) + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 context overflow, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerProxyModelLimitContextTokens); got != "1" {
		t.Fatalf("expected context limit header 1, got %q", got)
	}
	if !strings.Contains(rec.Body.String(), "context_length_exceeded") || !strings.Contains(rec.Body.String(), "prompt is too long") {
		t.Fatalf("expected opencode-compatible context overflow body, got %s", rec.Body.String())
	}
}
