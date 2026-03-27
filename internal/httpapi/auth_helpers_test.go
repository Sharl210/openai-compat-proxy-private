package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestAuthModeForUpstreamLegacyRouteUsesServerDefaultKey(t *testing.T) {
	cfg := config.Config{
		ProxyAPIKey:     "root-secret",
		DefaultProvider: "openai",
		Providers: []config.ProviderConfig{{
			ID:                     "openai",
			Enabled:                true,
			UpstreamAPIKey:         "upstream-secret",
			ProxyAPIKeyOverride:    "provider-secret",
			ProxyAPIKeyOverrideSet: true,
		}},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer root-secret")
	req = req.Clone(withRouteInfo(req.Context(), routeInfo{ProviderID: "openai", Legacy: true}))

	if got := authModeForUpstream(req, cfg); got != "server_default_key" {
		t.Fatalf("expected legacy route to keep server default upstream key mode, got %q", got)
	}
}

func TestAuthModeForUpstreamProviderEmptyOverrideUsesAuthorizationPassthrough(t *testing.T) {
	cfg := config.Config{
		ProxyAPIKey:     "root-secret",
		DefaultProvider: "openai",
		Providers: []config.ProviderConfig{{
			ID:                     "openai",
			Enabled:                true,
			ProxyAPIKeyOverride:    "empty",
			ProxyAPIKeyOverrideSet: true,
		}},
	}
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer real-upstream-token")
	req = req.Clone(withRouteInfo(req.Context(), routeInfo{ProviderID: "openai", Legacy: false}))

	if got := authModeForUpstream(req, cfg); got != "authorization_passthrough" {
		t.Fatalf("expected provider route with empty override to passthrough authorization, got %q", got)
	}
}

func TestAuthModeForUpstreamPrefersXUpstreamAuthorization(t *testing.T) {
	cfg := config.Config{Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamAPIKey: "upstream-secret"}}}
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	req.Header.Set("X-Upstream-Authorization", "Bearer custom")
	req = req.Clone(withRouteInfo(req.Context(), routeInfo{ProviderID: "openai"}))

	if got := authModeForUpstream(req, cfg); got != "x_upstream_authorization" {
		t.Fatalf("expected x_upstream_authorization mode, got %q", got)
	}
}
