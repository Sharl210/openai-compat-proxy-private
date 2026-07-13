package httpapi

import (
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
)

func TestApplyUltraMultiAgentRejectsNonResponsesEndpoint(t *testing.T) {
	req := model.CanonicalRequest{}
	err := applyUltraMultiAgent(&req, ultraIntent(), ultraProvider(), config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeChat})
	if err == nil || !strings.Contains(err.Error(), "requires UPSTREAM_ENDPOINT_TYPE=responses") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestApplyUltraMultiAgentRejectsExplicitlyDisabledProviderConfig(t *testing.T) {
	req := model.CanonicalRequest{}
	provider := ultraProvider()
	provider.SupportsResponsesMultiAgent = false
	err := applyUltraMultiAgent(&req, ultraIntent(), provider, config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeResponses})
	if err == nil || !strings.Contains(err.Error(), "set SUPPORTS_RESPONSES_MULTI_AGENT=true") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestApplyUltraMultiAgentRejectsConflictingClientPayload(t *testing.T) {
	req := model.CanonicalRequest{ResponseMultiAgent: []byte(`{"enabled":true,"max_concurrent_subagents":3}`)}
	err := applyUltraMultiAgent(&req, ultraIntent(), ultraProvider(), config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeResponses})
	if err == nil || !strings.Contains(err.Error(), "conflicts with -ultra") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestApplyUltraMultiAgentInjectsConfiguredCanonicalPayload(t *testing.T) {
	req := model.CanonicalRequest{}
	if err := applyUltraMultiAgent(&req, ultraIntent(), ultraProvider(), config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeResponses}); err != nil {
		t.Fatalf("apply ultra: %v", err)
	}
	if got := string(req.ResponseMultiAgent); got != `{"enabled":true,"max_concurrent_subagents":5}` {
		t.Fatalf("unexpected multi_agent payload: %s", got)
	}
}

func ultraIntent() model.ProxyModelIntent {
	return model.ProxyModelIntent{BaseModel: "gpt-5.6", HasUltra: true}
}

func ultraProvider() config.ProviderConfig {
	return config.ProviderConfig{SupportsResponsesMultiAgent: true, UltraMaxConcurrentSubagents: 5}
}
