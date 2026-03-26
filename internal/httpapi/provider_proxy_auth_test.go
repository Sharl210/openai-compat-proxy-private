package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/testutil"
)

func TestProviderScopedProxyAPIKeyOverrideAndDefaultLegacyFallback(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                     "openai",
			Enabled:                true,
			UpstreamBaseURL:        upstream.URL,
			UpstreamAPIKey:         "test-key",
			SupportsResponses:      true,
			ProxyAPIKeyOverride:    "provider-secret",
			ProxyAPIKeyOverrideSet: true,
		}},
	})

	legacyReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	legacyReq.Header.Set("Content-Type", "application/json")
	legacyReq.Header.Set("Authorization", "Bearer root-secret")
	legacyRec := httptest.NewRecorder()
	server.ServeHTTP(legacyRec, legacyReq)
	if legacyRec.Code != http.StatusOK {
		t.Fatalf("expected default legacy route to accept root key, got %d body=%s", legacyRec.Code, legacyRec.Body.String())
	}
	if got := legacyRec.Header().Get("X-STATUS-CHECK-URL"); !strings.Contains(got, "?key=root-secret") {
		t.Fatalf("expected legacy status URL to use root key, got %q", got)
	}
	requestID := legacyRec.Header().Get("X-Request-Id")

	providerReq := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	providerReq.Header.Set("Content-Type", "application/json")
	providerReq.Header.Set("Authorization", "Bearer provider-secret")
	providerRec := httptest.NewRecorder()
	server.ServeHTTP(providerRec, providerReq)
	if providerRec.Code != http.StatusOK {
		t.Fatalf("expected provider route to accept override key, got %d body=%s", providerRec.Code, providerRec.Body.String())
	}
	if got := providerRec.Header().Get("X-STATUS-CHECK-URL"); !strings.Contains(got, "?key=provider-secret") {
		t.Fatalf("expected provider status URL to use override key, got %q", got)
	}

	providerRootReq := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	providerRootReq.Header.Set("Content-Type", "application/json")
	providerRootReq.Header.Set("Authorization", "Bearer root-secret")
	providerRootRec := httptest.NewRecorder()
	server.ServeHTTP(providerRootRec, providerRootReq)
	if providerRootRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected provider route to reject root key when override set, got %d body=%s", providerRootRec.Code, providerRootRec.Body.String())
	}

	statusRootReq := httptest.NewRequest(http.MethodGet, "/openai/v1/requests/"+requestID+"?key=root-secret", nil)
	statusRootRec := httptest.NewRecorder()
	server.ServeHTTP(statusRootRec, statusRootReq)
	if statusRootRec.Code != http.StatusOK {
		t.Fatalf("expected default provider status route to accept root key, got %d body=%s", statusRootRec.Code, statusRootRec.Body.String())
	}

	statusProviderReq := httptest.NewRequest(http.MethodGet, "/openai/v1/requests/"+requestID+"?key=provider-secret", nil)
	statusProviderRec := httptest.NewRecorder()
	server.ServeHTTP(statusProviderRec, statusProviderReq)
	if statusProviderRec.Code != http.StatusOK {
		t.Fatalf("expected default provider status route to accept provider key, got %d body=%s", statusProviderRec.Code, statusProviderRec.Body.String())
	}
}

func TestProviderScopedProxyAPIKeyOverrideEmptyDisablesAuth(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                     "openai",
			Enabled:                true,
			UpstreamBaseURL:        upstream.URL,
			UpstreamAPIKey:         "test-key",
			SupportsResponses:      true,
			ProxyAPIKeyOverride:    "empty",
			ProxyAPIKeyOverrideSet: true,
		}},
	})

	providerReq := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	providerReq.Header.Set("Content-Type", "application/json")
	providerRec := httptest.NewRecorder()
	server.ServeHTTP(providerRec, providerReq)
	if providerRec.Code != http.StatusOK {
		t.Fatalf("expected empty override to disable auth for provider route, got %d body=%s", providerRec.Code, providerRec.Body.String())
	}
	requestID := providerRec.Header().Get("X-Request-Id")
	if got := providerRec.Header().Get("X-STATUS-CHECK-URL"); strings.Contains(got, "?key=") {
		t.Fatalf("expected empty override status URL to omit key, got %q", got)
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/openai/v1/requests/"+requestID, nil)
	statusRec := httptest.NewRecorder()
	server.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("expected empty override status route to require no auth, got %d body=%s", statusRec.Code, statusRec.Body.String())
	}
}
