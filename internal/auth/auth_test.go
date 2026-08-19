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

func TestResolveUpstreamAuthorizationBlankKeyDisablesAllAuthSources(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		header string
		value  string
	}{
		{name: "x upstream authorization", header: "X-Upstream-Authorization", value: "Bearer explicit"},
		{name: "authorization", header: "Authorization", value: "Bearer client-token"},
		{name: "x api key", header: "X-API-Key", value: "client-key"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/claude/v1/messages", nil)
			req.Header.Set(testCase.header, testCase.value)
			got, err := ResolveUpstreamAuthorization(req, config.Config{})
			if err != nil || got != "" {
				t.Fatalf("blank upstream key must disable auth, got authorization=%q error=%v", got, err)
			}
		})
	}
}

func TestResolveUpstreamAuthorizationUsesConfiguredKeyAndExplicitOverride(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	got, err := ResolveUpstreamAuthorization(req, config.Config{UpstreamAPIKey: "server-key"})
	if err != nil || got != "Bearer server-key" {
		t.Fatalf("configured upstream key = %q, %v", got, err)
	}
	req.Header.Set("X-Upstream-Authorization", "Bearer request-key")
	got, err = ResolveUpstreamAuthorization(req, config.Config{UpstreamAPIKey: "server-key"})
	if err != nil || got != "Bearer request-key" {
		t.Fatalf("explicit upstream override = %q, %v", got, err)
	}
}

func TestResolveUpstreamAuthorizationTreatsEmptyWordAsLiteralKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	got, err := ResolveUpstreamAuthorization(req, config.Config{UpstreamAPIKey: "empty"})
	if err != nil || got != "Bearer empty" {
		t.Fatalf("empty word must no longer be a sentinel, got authorization=%q error=%v", got, err)
	}
}
