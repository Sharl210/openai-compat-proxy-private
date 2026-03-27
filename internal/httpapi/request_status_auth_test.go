package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestBuildStatusCheckURLUsesProviderScopedPathWithoutQuery(t *testing.T) {
	req := httptest.NewRequest("GET", "http://proxy.example/v1/responses", nil)
	if got := buildStatusCheckURL(req, "openai", "req-1", "ignored-token"); got != "http://proxy.example/openai/v1/requests/req-1" {
		t.Fatalf("expected provider-scoped status URL without query, got %q", got)
	}
}

func TestBuildStatusCheckURLUsesForwardedProtoWithoutToken(t *testing.T) {
	req := httptest.NewRequest("GET", "http://proxy.example/v1/responses", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	if got := buildStatusCheckURL(req, "openai", "req-2", "another-token"); got != "https://proxy.example/openai/v1/requests/req-2" {
		t.Fatalf("expected forwarded proto status URL without token, got %q", got)
	}
}
