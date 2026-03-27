package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestResponsesNonStreamStrategiesAlignCoreSemantics(t *testing.T) {
	semanticPayload := map[string]any{
		"id":     "resp_123",
		"object": "response",
		"status": "completed",
		"reasoning": map[string]any{
			"summary": "thinking",
		},
		"usage": map[string]any{
			"input_tokens":  11,
			"output_tokens": 7,
			"total_tokens":  18,
			"output_tokens_details": map[string]any{
				"reasoning_tokens": 5,
			},
		},
		"output": []any{
			map[string]any{
				"id":     "msg_123",
				"type":   "message",
				"status": "completed",
				"role":   "assistant",
				"content": []any{
					map[string]any{"type": "output_text", "text": "hello"},
				},
			},
			map[string]any{
				"id":   "rs_123",
				"type": "reasoning",
				"summary": []any{
					map[string]any{"type": "summary_text", "text": "thinking"},
				},
			},
			map[string]any{
				"id":        "fc_123",
				"type":      "function_call",
				"status":    "completed",
				"call_id":   "call_123",
				"name":      "get_weather",
				"arguments": `{"city":"Shanghai"}`,
			},
		},
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}

		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}

		if stream, _ := req["stream"].(bool); stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(
				"event: response.output_item.done\n" +
					"data: {\"item\":{\"id\":\"msg_123\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}}\n\n" +
					"event: response.output_item.done\n" +
					"data: {\"item\":{\"id\":\"rs_123\",\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"thinking\"}]}}\n\n" +
					"event: response.output_item.done\n" +
					"data: {\"item\":{\"id\":\"fc_123\",\"type\":\"function_call\",\"status\":\"completed\",\"call_id\":\"call_123\",\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\":\\\"Shanghai\\\"}\"}}\n\n" +
					"event: response.completed\n" +
					"data: {\"response\":{\"usage\":{\"input_tokens\":11,\"output_tokens\":7,\"total_tokens\":18,\"output_tokens_details\":{\"reasoning_tokens\":5}}}}\n\n",
			))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(semanticPayload); err != nil {
			t.Fatalf("encode upstream payload: %v", err)
		}
	}))
	defer upstream.Close()

	proxyBuffer := performResponsesNonStreamRequest(t, upstream.URL, config.DownstreamNonStreamStrategyProxyBuffer)
	upstreamNonStream := performResponsesNonStreamRequest(t, upstream.URL, config.DownstreamNonStreamStrategyUpstreamNonStream)

	for _, key := range []string{"status", "output", "reasoning", "usage"} {
		if !reflect.DeepEqual(proxyBuffer[key], upstreamNonStream[key]) {
			t.Fatalf("expected %s semantics to match between proxy_buffer and upstream_non_stream\nproxy_buffer=%#v\nupstream_non_stream=%#v", key, proxyBuffer[key], upstreamNonStream[key])
		}
	}
}

func performResponsesNonStreamRequest(t *testing.T, upstreamURL string, strategy string) map[string]any {
	t.Helper()

	server := NewServer(testResponsesConfigWithStrategy(upstreamURL, strategy))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"input":[{"role":"user","content":"hello"}]
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
	return payload
}

func testResponsesConfigWithStrategy(upstreamURL string, strategy string) config.Config {
	cfg := testResponsesConfig(upstreamURL)
	cfg.DownstreamNonStreamStrategy = strategy
	return cfg
}
