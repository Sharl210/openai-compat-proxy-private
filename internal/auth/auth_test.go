package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestValidateProxyAuthForProviderDoesNotAllowDeletedRequestStatusQueryKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/openai/v1/requests/req-1?key=proxy-secret", nil)

	provider := config.ProviderConfig{ID: "openai", Enabled: true}
	if err := ValidateProxyAuthForProvider(req, "proxy-secret", provider, true); err == nil {
		t.Fatalf("expected deleted request status query key path to be rejected")
	}
}

func TestResolveUpstreamAuthorizationUsesXAPIKeyWhenProxyAuthDisabled(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/claude/v1/messages", nil)
	req.Header.Set("x-api-key", "real-upstream-token")

	got, err := ResolveUpstreamAuthorization(req, config.Config{})
	if err != nil {
		t.Fatalf("expected x-api-key passthrough, got error: %v", err)
	}
	if got != "Bearer real-upstream-token" {
		t.Fatalf("expected x-api-key to become bearer upstream auth, got %q", got)
	}
}

func TestResolveUpstreamAuthorizationDoesNotUseXAPIKeyWhenItMatchesProxyKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/claude/v1/messages", nil)
	req.Header.Set("x-api-key", "proxy-secret")

	_, err := ResolveUpstreamAuthorization(req, config.Config{ProxyAPIKey: "proxy-secret"})
	if err != ErrMissingUpstreamAuth {
		t.Fatalf("expected missing upstream auth when x-api-key only matches proxy key, got %v", err)
	}
}
