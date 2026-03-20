package integration_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestProviderScopedResponsesModelMappingUsesProviderSpecificAlias(t *testing.T) {
	var openAIModel string
	openAIStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		openAIModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {}\n\n"))
	}))
	defer openAIStub.Close()

	var anthropicModel string
	anthropicStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		anthropicModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {}\n\n"))
	}))
	defer anthropicStub.Close()

	server := newTestServerWithConfig(t, config.Config{
		ListenAddr: ":0",
		Providers: []config.ProviderConfig{
			{ID: "openai", Enabled: true, UpstreamBaseURL: openAIStub.URL, UpstreamAPIKey: "openai-key", SupportsResponses: true, ModelMap: map[string]string{"gpt-5": "gpt-5.4"}},
			{ID: "anthropic", Enabled: true, UpstreamBaseURL: anthropicStub.URL, UpstreamAPIKey: "anthropic-key", SupportsResponses: true, ModelMap: map[string]string{"gpt-5": "claude-sonnet-4-6"}},
		},
	})
	defer server.Close()

	openAIResp, err := http.Post(server.URL+"/openai/v1/responses", "application/json", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	openAIResp.Body.Close()

	anthropicResp, err := http.Post(server.URL+"/anthropic/v1/responses", "application/json", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	anthropicResp.Body.Close()

	if openAIModel != "gpt-5.4" {
		t.Fatalf("expected openai mapped model gpt-5.4, got %q", openAIModel)
	}
	if anthropicModel != "claude-sonnet-4-6" {
		t.Fatalf("expected anthropic mapped model claude-sonnet-4-6, got %q", anthropicModel)
	}
}

func TestProviderScopedChatModelMappingLeavesUnknownModelUnchanged(t *testing.T) {
	var receivedModel string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		receivedModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n"))
		_, _ = w.Write([]byte("event: response.completed\ndata: {}\n\n"))
	}))
	defer stub.Close()

	server := newTestServerWithConfig(t, config.Config{
		ListenAddr: ":0",
		Providers:  []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: stub.URL, UpstreamAPIKey: "openai-key", SupportsChat: true, ModelMap: map[string]string{"gpt-5": "gpt-5.4"}}},
	})
	defer server.Close()

	resp, err := http.Post(server.URL+"/openai/v1/chat/completions", "application/json", strings.NewReader(`{"model":"custom-model","messages":[{"role":"user","content":"hi"}],"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if receivedModel != "custom-model" {
		t.Fatalf("expected unknown model to pass through unchanged, got %q", receivedModel)
	}
}
