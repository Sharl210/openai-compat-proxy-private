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
	legacyStatusURL := legacyRec.Header().Get("X-STATUS-CHECK-URL")
	if strings.Contains(legacyStatusURL, "provider-secret") || strings.Contains(legacyStatusURL, "root-secret") {
		t.Fatalf("expected legacy status URL to hide proxy keys, got %q", legacyStatusURL)
	}
	if legacyStatusURL != "http://example.com/openai/v1/requests/"+legacyRec.Header().Get("X-Request-Id") {
		t.Fatalf("expected legacy status URL to use provider-scoped path without token, got %q", legacyStatusURL)
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
	providerStatusURL := providerRec.Header().Get("X-STATUS-CHECK-URL")
	if strings.Contains(providerStatusURL, "provider-secret") || strings.Contains(providerStatusURL, "root-secret") {
		t.Fatalf("expected provider status URL to hide proxy keys, got %q", providerStatusURL)
	}
	if providerStatusURL != "http://example.com/openai/v1/requests/"+providerRec.Header().Get("X-Request-Id") {
		t.Fatalf("expected provider status URL to omit token and key query, got %q", providerStatusURL)
	}

	providerRootReq := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	providerRootReq.Header.Set("Content-Type", "application/json")
	providerRootReq.Header.Set("Authorization", "Bearer root-secret")
	providerRootRec := httptest.NewRecorder()
	server.ServeHTTP(providerRootRec, providerRootReq)
	if providerRootRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected provider route to reject root key when override set, got %d body=%s", providerRootRec.Code, providerRootRec.Body.String())
	}

	providerQueryReq := httptest.NewRequest(http.MethodPost, "/openai/v1/responses?key=provider-secret", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	providerQueryReq.Header.Set("Content-Type", "application/json")
	providerQueryRec := httptest.NewRecorder()
	server.ServeHTTP(providerQueryRec, providerQueryReq)
	if providerQueryRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected provider route to reject query key auth, got %d body=%s", providerQueryRec.Code, providerQueryRec.Body.String())
	}

	statusRootReq := httptest.NewRequest(http.MethodGet, "/openai/v1/requests/"+requestID+"?key=root-secret", nil)
	statusRootRec := httptest.NewRecorder()
	server.ServeHTTP(statusRootRec, statusRootReq)
	if statusRootRec.Code != http.StatusOK {
		t.Fatalf("expected provider-scoped status route to ignore query key and stay public, got %d body=%s", statusRootRec.Code, statusRootRec.Body.String())
	}

	statusProviderReq := httptest.NewRequest(http.MethodGet, "/openai/v1/requests/"+requestID, nil)
	statusProviderRec := httptest.NewRecorder()
	server.ServeHTTP(statusProviderRec, statusProviderReq)
	if statusProviderRec.Code != http.StatusOK {
		t.Fatalf("expected default provider status route to allow unauthenticated lookup, got %d body=%s", statusProviderRec.Code, statusProviderRec.Body.String())
	}

	statusHeaderReq := httptest.NewRequest(http.MethodGet, "/openai/v1/requests/"+requestID, nil)
	statusHeaderReq.Header.Set("Authorization", "Bearer provider-secret")
	statusHeaderRec := httptest.NewRecorder()
	server.ServeHTTP(statusHeaderRec, statusHeaderReq)
	if statusHeaderRec.Code != http.StatusOK {
		t.Fatalf("expected default provider status route to stay accessible even with auth header present, got %d body=%s", statusHeaderRec.Code, statusHeaderRec.Body.String())
	}
}

func TestProviderScopedProxyAPIKeyOverrideEmptyAllowsAuthorizationPassthrough(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                     "openai",
			Enabled:                true,
			UpstreamBaseURL:        upstream.URL,
			SupportsResponses:      true,
			ProxyAPIKeyOverride:    "empty",
			ProxyAPIKeyOverrideSet: true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer real-upstream-token")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected passthrough auth request to succeed, got %d body=%s", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer real-upstream-token" {
		t.Fatalf("expected upstream authorization passthrough, got %q", gotAuth)
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

func TestDefaultLegacyRouteWithEmptyOverrideStillRequiresRootProxyKey(t *testing.T) {
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

	unauthorizedReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	unauthorizedReq.Header.Set("Content-Type", "application/json")
	unauthorizedRec := httptest.NewRecorder()
	server.ServeHTTP(unauthorizedRec, unauthorizedReq)
	if unauthorizedRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected legacy default route to reject missing root key when override is empty, got %d body=%s", unauthorizedRec.Code, unauthorizedRec.Body.String())
	}

	authorizedReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	authorizedReq.Header.Set("Content-Type", "application/json")
	authorizedReq.Header.Set("Authorization", "Bearer root-secret")
	authorizedRec := httptest.NewRecorder()
	server.ServeHTTP(authorizedRec, authorizedReq)
	if authorizedRec.Code != http.StatusOK {
		t.Fatalf("expected legacy default route to accept root key even when override is empty, got %d body=%s", authorizedRec.Code, authorizedRec.Body.String())
	}
}
