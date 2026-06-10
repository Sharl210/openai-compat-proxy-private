package upstream

import (
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
)

func TestBuildResponsesUpstreamToolPayloadPreserveWebSearchOmitsFunctionFields(t *testing.T) {
	payload := buildResponsesUpstreamToolPayload(model.CanonicalTool{
		Type:        "web_search",
		Name:        "",
		Description: "",
	}, config.ResponsesToolCompatModePreserve)

	if got, _ := payload["type"].(string); got != "web_search" {
		t.Fatalf("expected preserved tool type web_search, got %#v", payload)
	}
	if _, exists := payload["description"]; exists {
		t.Fatalf("expected preserve web_search payload to omit description, got %#v", payload)
	}
	if _, exists := payload["parameters"]; exists {
		t.Fatalf("expected preserve web_search payload to omit parameters, got %#v", payload)
	}
	if _, exists := payload["name"]; exists {
		t.Fatalf("expected preserve web_search payload to omit empty name, got %#v", payload)
	}
}
