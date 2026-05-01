package httpapi

import (
	"encoding/json"
	"io"
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

func TestResponsesUpstreamNonStreamRejectsEmptyUpstreamPayload(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	server := NewServer(testResponsesConfigWithStrategy(upstream.URL, config.DownstreamNonStreamStrategyUpstreamNonStream))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"input":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected status 502, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	errMap, _ := payload["error"].(map[string]any)
	if got, _ := errMap["code"].(string); got != "invalid_upstream_response" {
		t.Fatalf("expected invalid_upstream_response code, got %#v", payload)
	}
}

func TestResponsesUpstreamNonStreamPreservesUpstreamServiceTier(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","status":"completed","service_tier":"default","output":[{"id":"msg_123","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hello"}]}]}`))
	}))
	defer upstream.Close()

	server := NewServer(testResponsesConfigWithStrategy(upstream.URL, config.DownstreamNonStreamStrategyUpstreamNonStream))
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
	if got, _ := payload["service_tier"].(string); got != "default" {
		t.Fatalf("expected service_tier default, got %#v body=%s", payload["service_tier"], rec.Body.String())
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

func TestResponsesProxyBufferWithChatUpstreamTreatsEOFWithoutTerminalEventAsError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			_, _ = w.Write([]byte("event: chat\n" +
				"data: {\"id\":\"chat-123\",\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking\"}}]}\n\n" +
				"event: chat\n" +
				"data: {\"id\":\"chat-123\",\"choices\":[{\"delta\":{\"content\":\"final answer\"}}]}\n\n"))
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "chat",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyProxyBuffer,
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

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected status 502 for missing terminal event, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"message":"unexpected EOF"`) {
		t.Fatalf("expected upstream EOF surfaced to client, got %s", rec.Body.String())
	}
}

func TestProviderResponsesRouteDefaultsUsageForChatUpstreamNonStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"stream_options":{"include_usage":true}`) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl_123",
				"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5,"prompt_tokens_details":{"cached_tokens":1}}
			}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl_123",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]
		}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "chat",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
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
		t.Fatalf("unmarshal response: %v body=%s", err, rec.Body.String())
	}
	usage, _ := payload["usage"].(map[string]any)
	if usage == nil {
		t.Fatalf("expected non-stream compat route to include usage by default, got %s", rec.Body.String())
	}
	if usage["input_tokens"] != float64(3) || usage["output_tokens"] != float64(2) || usage["total_tokens"] != float64(5) {
		t.Fatalf("expected mapped usage fields, got %#v body=%s", usage, rec.Body.String())
	}
	inputDetails, _ := usage["input_tokens_details"].(map[string]any)
	if inputDetails == nil || inputDetails["cached_tokens"] != float64(1) {
		t.Fatalf("expected cached_tokens in usage payload, got %#v body=%s", usage, rec.Body.String())
	}
}
