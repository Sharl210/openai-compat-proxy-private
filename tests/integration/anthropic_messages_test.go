package integration_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/testutil"
)

func TestAnthropicMessagesHandlerReturnsMessageObject(t *testing.T) {
	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}}\n\n",
	})
	defer stub.Close()

	server := newTestServerWithConfig(t, config.Config{
		ListenAddr: ":0",
		Providers:  []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: stub.URL, UpstreamAPIKey: "server-key", SupportsAnthropicMessages: true}},
	})
	defer server.Close()

	resp, err := http.Post(server.URL+"/anthropic/anthropic/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet","max_tokens":128,"messages":[{"role":"user","content":"hi"}],"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["type"] != "message" {
		t.Fatalf("expected type=message, got %#v", body["type"])
	}
	content, ok := body["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("expected one content block, got %#v", body["content"])
	}
	block, _ := content[0].(map[string]any)
	if block["text"] != "hello" {
		t.Fatalf("expected text hello, got %#v", block["text"])
	}
}

func TestAnthropicMessagesHandlerRejectsUnsupportedProvider(t *testing.T) {
	server := newTestServerWithConfig(t, config.Config{
		ListenAddr: ":0",
		Providers:  []config.ProviderConfig{{ID: "openai", Enabled: true, SupportsAnthropicMessages: false}},
	})
	defer server.Close()

	resp, err := http.Post(server.URL+"/openai/anthropic/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet","max_tokens":128,"messages":[{"role":"user","content":"hi"}],"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}
