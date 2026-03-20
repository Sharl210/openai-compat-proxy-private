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

func TestAnthropicMessagesHandlerReturnsToolUseBlock(t *testing.T) {
	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.done\ndata: {\"item\":{\"type\":\"function_call\",\"id\":\"fc_123\",\"call_id\":\"call_123\",\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\":\\\"Shanghai\\\"}\"}}\n\n",
		"event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":20,\"input_tokens_details\":{\"cached_tokens\":8}}}}\n\n",
	})
	defer stub.Close()

	server := newTestServerWithConfig(t, config.Config{
		ListenAddr: ":0",
		Providers:  []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: stub.URL, UpstreamAPIKey: "server-key", SupportsAnthropicMessages: true}},
	})
	defer server.Close()

	resp, err := http.Post(server.URL+"/anthropic/anthropic/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet","max_tokens":128,"messages":[{"role":"user","content":"use the tool"}],"tools":[{"name":"get_weather","description":"Get weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}],"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	content, ok := body["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("expected one content block, got %#v", body["content"])
	}
	block, _ := content[0].(map[string]any)
	if block["type"] != "tool_use" {
		t.Fatalf("expected tool_use block, got %#v", block)
	}
	if block["name"] != "get_weather" {
		t.Fatalf("expected tool name get_weather, got %#v", block["name"])
	}
	usage, ok := body["usage"].(map[string]any)
	if !ok {
		t.Fatalf("expected usage object, got %#v", body["usage"])
	}
	if usage["cache_read_input_tokens"] != float64(8) {
		t.Fatalf("expected cache_read_input_tokens=8, got %#v", usage)
	}
	if body["stop_reason"] != "tool_use" {
		t.Fatalf("expected stop_reason tool_use, got %#v", body["stop_reason"])
	}
}
