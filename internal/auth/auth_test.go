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
