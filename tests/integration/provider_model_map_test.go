package integration_test

import (
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestProviderResolveModelUsesExactMatchBeforeWildcard(t *testing.T) {
	provider := config.ProviderConfig{ModelMap: map[string]string{
		"gpt-5": "gpt-5.4",
		"*":     "vendor-default",
	}}

	if got := provider.ResolveModel("gpt-5"); got != "gpt-5.4" {
		t.Fatalf("expected exact match before wildcard, got %q", got)
	}
	if got := provider.ResolveModel("unknown-model"); got != "vendor-default" {
		t.Fatalf("expected wildcard fallback, got %q", got)
	}
}
