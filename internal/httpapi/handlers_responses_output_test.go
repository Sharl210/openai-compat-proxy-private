package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/testutil"
)

func TestResponsesNonStreamReturnsFunctionCallOutputItems(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"fc_123\",\"type\":\"function_call\",\"call_id\":\"call_123\",\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\":\\\"Shanghai\\\"}\"}}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(testResponsesConfig(upstream.URL))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"input":[{"role":"user","content":"what is the weather?"}],
		"tools":[{
			"type":"function",
			"name":"get_weather",
			"description":"Get weather",
			"parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}
		}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}

	output, _ := payload["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("expected one output item, got %#v", payload["output"])
	}
	item, _ := output[0].(map[string]any)
	if got, _ := item["type"].(string); got != "function_call" {
		t.Fatalf("expected output item type function_call, got %#v", item)
	}
	if got, _ := item["call_id"].(string); got != "call_123" {
		t.Fatalf("expected function_call call_id call_123, got %#v", item)
	}
	parameters, _ := item["parameters"].(map[string]any)
	if parameters == nil || parameters["city"] != "Shanghai" {
		t.Fatalf("expected parsed parameters object, got %#v", item)
	}
	if _, exists := item["tool_calls"]; exists {
		t.Fatalf("expected responses output item shape, got nested tool_calls %#v", item)
	}
}

func TestProviderResponsesNonStreamAggregatesCompleteToolArgumentsForChatUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeFrame := func(payload map[string]any) {
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal frame: %v", err)
			}
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(encoded)
			_, _ = w.Write([]byte("\n\n"))
		}
		writeFrame(map[string]any{"id": "chatcmpl_123", "choices": []any{map[string]any{"delta": map[string]any{"role": "assistant"}, "index": 0}}})
		writeFrame(map[string]any{"id": "chatcmpl_123", "choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_1", "type": "function", "function": map[string]any{"name": "search_web", "arguments": `{"query":"open`}}}}, "index": 0}}})
		writeFrame(map[string]any{"id": "chatcmpl_123", "choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": `ai compat proxy","topic":"general"}`}}}}, "index": 0}}})
		writeFrame(map[string]any{"id": "chatcmpl_123", "choices": []any{map[string]any{"finish_reason": "tool_calls", "index": 0}}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}})
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "chat",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                   "chat",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeChat,
			SupportsResponses:    true,
			SupportsChat:         true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/chat/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"stream":false,
		"input":[{"role":"user","content":"search github"}],
		"tools":[{
			"type":"function",
			"name":"search_web",
			"description":"Search the web",
			"parameters":{"type":"object","properties":{"query":{"type":"string"},"topic":{"type":"string"}},"required":["query"]}
		}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}

	output, _ := payload["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("expected one output item, got %#v", payload["output"])
	}
	item, _ := output[0].(map[string]any)
	if got, _ := item["type"].(string); got != "function_call" {
		t.Fatalf("expected function_call item, got %#v", item)
	}
	if got, _ := item["arguments"].(string); got != `{"query":"openai compat proxy","topic":"general"}` {
		t.Fatalf("expected complete aggregated arguments, got %#v", item)
	}
	parameters, _ := item["parameters"].(map[string]any)
	if parameters == nil || parameters["query"] != "openai compat proxy" || parameters["topic"] != "general" {
		t.Fatalf("expected parsed parameters object, got %#v", item)
	}
}
