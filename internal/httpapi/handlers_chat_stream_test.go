package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/testutil"
	"openai-compat-proxy/internal/upstream"
)

func collectChatStreamChunks(t *testing.T, body string) []map[string]any {
	t.Helper()
	var chunks []map[string]any
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" || payload == "" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if object, _ := chunk["object"].(string); object == "chat.completion.chunk" {
			chunks = append(chunks, chunk)
		}
	}
	return chunks
}

func TestChatStreamUsesStructuredReasoningPlaceholder(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsChat:      true,
			SupportsResponses: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `"reasoning_content":"**推理中**\n\n代理层占位，以兼容不同上游情况，便于客户端记录推理时长\n\n"`) {
		t.Fatalf("expected chat placeholder reasoning to use titled format, got %s", body)
	}
}

func TestChatStreamChunksCarryIDAndModel(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsChat:      true,
			SupportsResponses: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	chunks := collectChatStreamChunks(t, rec.Body.String())
	if len(chunks) == 0 {
		t.Fatalf("expected chat completion chunks, got %s", rec.Body.String())
	}
	for _, chunk := range chunks {
		if _, ok := chunk["id"].(string); !ok {
			t.Fatalf("expected every chat completion chunk to include string id, got %#v", chunk)
		}
		if got, _ := chunk["model"].(string); got != "gpt-5" {
			t.Fatalf("expected every chat completion chunk to include model gpt-5, got %#v", chunk)
		}
	}
}

func TestChatStreamUsesToolCallsFinishReasonForToolCallTurns(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"get_weather\"}}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsChat:      true,
			SupportsResponses: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Fatalf("expected tool_calls finish_reason, got %s", body)
	}
}

func TestChatStreamSendsAssistantRoleBeforeToolCalls(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"get_weather\"}}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsChat:      true,
			SupportsResponses: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	roleIdx := strings.Index(body, `"delta":{"role":"assistant"}`)
	toolIdx := strings.Index(body, `"tool_calls":[{"function":{"arguments":"","name":"get_weather"},"id":"call_1","index":0,"type":"function"}]`)
	if toolIdx == -1 {
		toolIdx = strings.Index(body, `"tool_calls":[{"function":{"arguments":"{\"city\":\"Shanghai\"}","name":"get_weather"},"id":"call_1","index":0,"type":"function"}]`)
	}
	if roleIdx == -1 || toolIdx == -1 || roleIdx > toolIdx {
		t.Fatalf("expected assistant role chunk before tool_calls chunk, got %s", body)
	}
}

func TestChatStreamBuffersToolArgumentsUntilMetadataArrives(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.function_call_arguments.delta\n" +
			"data: {\"item_id\":\"fc_1\",\"delta\":\"{\\\"city\\\":\\\"Shanghai\\\"}\"}\n\n",
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"get_weather\"}}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsChat:      true,
			SupportsResponses: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `"tool_calls":[{"function":{"arguments":"{\"city\":\"Shanghai\"}","name":"get_weather"},"id":"call_1","index":0,"type":"function"}]`) {
		t.Fatalf("expected buffered arguments to merge into first metadata chunk, got %s", body)
	}
}

func TestChatStreamDoesNotKeepToolCallsFinishReasonAfterLaterText(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"get_weather\"}}\n\n",
		"event: response.function_call_arguments.delta\n" +
			"data: {\"item_id\":\"fc_1\",\"delta\":\"{\\\"city\\\":\\\"Shanghai\\\"}\"}\n\n",
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"最终答案\"}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "openai",
			Enabled:         true,
			UpstreamBaseURL: upstream.URL,
			UpstreamAPIKey:  "test-key",
			SupportsChat:    true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `"content":"最终答案"`) {
		t.Fatalf("expected later assistant text chunk, got %s", body)
	}
	if strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Fatalf("expected final finish_reason to stop after later text, got %s", body)
	}
	if !strings.Contains(body, `"finish_reason":"stop"`) {
		t.Fatalf("expected final finish_reason stop after later text, got %s", body)
	}
}

func TestChatStreamDoesNotEmitEmptyToolArgumentsBeforeDeltaArrives(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"get_weather\"}}\n\n",
		"event: response.function_call_arguments.delta\n" +
			"data: {\"item_id\":\"fc_1\",\"delta\":\"{\\\"city\\\":\\\"Shanghai\\\"}\"}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsChat:      true,
			SupportsResponses: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, `"tool_calls":[{"function":{"arguments":"","name":"get_weather"},"id":"call_1","index":0,"type":"function"}]`) {
		t.Fatalf("expected no provisional empty arguments tool chunk, got %s", body)
	}
	if !strings.Contains(body, `"tool_calls":[{"function":{"arguments":"{\"city\":\"Shanghai\"}","name":"get_weather"},"id":"call_1","index":0,"type":"function"}]`) {
		t.Fatalf("expected first emitted tool chunk to contain full arguments, got %s", body)
	}
}

func TestWriteChatSSEDoesNotEmitEmptyToolArgumentsBeforeDeltaArrives(t *testing.T) {
	rec := httptest.NewRecorder()
	err := writeChatSSE(rec, nil, []upstream.Event{
		{Event: "response.output_item.added", Data: map[string]any{"item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "get_weather"}}},
		{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `{"city":"Shanghai"}`}},
		{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}},
	}, true)
	if err != nil {
		t.Fatalf("writeChatSSE error: %v", err)
	}
	body := rec.Body.String()
	if strings.Contains(body, `"tool_calls":[{"function":{"arguments":"","name":"get_weather"},"id":"call_1","index":0,"type":"function"}]`) {
		t.Fatalf("expected direct writeChatSSE path to avoid empty arguments chunk, got %s", body)
	}
	if !strings.Contains(body, `"tool_calls":[{"function":{"arguments":"{\"city\":\"Shanghai\"}","name":"get_weather"},"id":"call_1","index":0,"type":"function"}]`) {
		t.Fatalf("expected direct writeChatSSE path to emit full arguments chunk, got %s", body)
	}
}

func TestChatEventWriterDoesNotEmitEmptyToolArgumentsBeforeDeltaArrives(t *testing.T) {
	rec := httptest.NewRecorder()
	state := &chatStreamState{toolMeta: map[string]map[string]string{}, toolIndex: map[string]int{}, toolSent: map[string]bool{}, pendingToolArgs: map[string]string{}}
	helper := &responseEventWriterHelper{downstreamType: "chat", upstreamEndpointType: config.UpstreamEndpointTypeResponses, toolIDAliases: map[string]string{}, toolItems: map[string]*responsesToolItemState{}}
	writer := NewChatEventWriter(rec, nil, state, helper, nil)

	for _, evt := range []upstream.Event{
		{Event: "response.output_item.added", Data: map[string]any{"item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "get_weather"}}},
		{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `{"city":"Shanghai"}`}},
		{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}},
	} {
		if err := writer.WriteEvent(evt.Event, evt.Data); err != nil {
			t.Fatalf("writer.WriteEvent error: %v", err)
		}
	}
	body := rec.Body.String()
	if strings.Contains(body, `"tool_calls":[{"function":{"arguments":"","name":"get_weather"},"id":"call_1","index":0,"type":"function"}]`) {
		t.Fatalf("expected ChatEventWriter path to avoid empty arguments chunk, got %s", body)
	}
	if !strings.Contains(body, `"tool_calls":[{"function":{"arguments":"{\"city\":\"Shanghai\"}","name":"get_weather"},"id":"call_1","index":0,"type":"function"}]`) {
		t.Fatalf("expected ChatEventWriter path to emit full arguments chunk, got %s", body)
	}
}

func TestChatStreamMapsReasoningSummaryDeltaToReasoningContent(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.reasoning_summary_text.delta\n" +
			"data: {\"item_id\":\"rs_1\",\"summary_index\":0,\"delta\":\"alpha\"}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsChat:      true,
			SupportsResponses: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `"reasoning_content":"alpha"`) {
		t.Fatalf("expected reasoning summary delta to map into chat reasoning_content, got %s", body)
	}
}

func TestChatStreamMergesIncludeUsageIntoTerminalFinishChunk(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2,\"input_tokens_details\":{\"cached_tokens\":4,\"cache_creation_tokens\":2}}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsChat:      true,
			SupportsResponses: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"stream_options":{"include_usage":true},
		"messages":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `data: [DONE]`) {
		t.Fatalf("expected DONE marker, got %s", body)
	}

	var finishChunk map[string]any
	for _, frame := range strings.Split(body, "\n\n") {
		frame = strings.TrimSpace(frame)
		if !strings.HasPrefix(frame, "data: ") || frame == "data: [DONE]" {
			continue
		}
		payload := strings.TrimPrefix(frame, "data: ")
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			if _, hasUsage := chunk["usage"]; hasUsage {
				t.Fatalf("expected no usage-only trailing chunk, got %s", body)
			}
			continue
		}
		choice, _ := choices[0].(map[string]any)
		if finishReason, _ := choice["finish_reason"].(string); finishReason == "stop" {
			finishChunk = chunk
		}
	}
	if finishChunk == nil {
		t.Fatalf("expected a terminal finish chunk, got %s", body)
	}
	usage, _ := finishChunk["usage"].(map[string]any)
	if len(usage) == 0 {
		t.Fatalf("expected terminal finish chunk to carry usage payload, got %s", body)
	}
	if got := usage["cached_tokens"]; got != float64(4) {
		t.Fatalf("expected cached_tokens 4 in terminal finish chunk, got %#v body=%s", got, body)
	}
	if got := usage["cache_creation_tokens"]; got != float64(2) {
		t.Fatalf("expected cache_creation_tokens 2 in terminal finish chunk, got %#v body=%s", got, body)
	}
	details, _ := usage["prompt_tokens_details"].(map[string]any)
	if got := details["cached_tokens"]; got != float64(4) {
		t.Fatalf("expected prompt_tokens_details.cached_tokens 4, got %#v body=%s", got, body)
	}
	if got := details["cache_creation_tokens"]; got != float64(2) {
		t.Fatalf("expected prompt_tokens_details.cache_creation_tokens 2, got %#v body=%s", got, body)
	}
}

func TestChatStreamTerminalFailureAfterSSEStartStaysInSSEProtocol(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n",
		"event: response.incomplete\n" +
			"data: {\"health_flag\":\"upstream_error\",\"message\":\"boom\"}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsChat:      true,
			SupportsResponses: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `"delta":{"error":{"health_flag":"upstream_error","message":"boom"}}`) {
		t.Fatalf("expected terminal failure to stay in chat SSE chunk, got %s", body)
	}
	if strings.Count(body, `"delta":{"error":{"health_flag":"upstream_error","message":"boom"}}`) != 1 {
		t.Fatalf("expected exactly one terminal failure SSE chunk, got %s", body)
	}
	if !strings.Contains(body, `data: [DONE]`) {
		t.Fatalf("expected terminal failure to finish with DONE marker, got %s", body)
	}
	if strings.Contains(body, `"code":"upstream_error"`) || strings.Contains(body, `"type":"proxy_error"`) {
		t.Fatalf("expected no JSON error body after SSE start, got %s", body)
	}
}

func TestChatStreamUpstreamDisconnectsWithoutTerminalEventStaysInSSEProtocol(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsChat:      true,
			SupportsResponses: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `"content":"hello"`) {
		t.Fatalf("expected streamed content before upstream disconnect, got %s", body)
	}
	if !strings.Contains(body, `"delta":{"error":{"health_flag":"upstreamStreamBroken","message":"unexpected EOF"}}`) {
		t.Fatalf("expected unexpected EOF to stay in chat SSE protocol, got %s", body)
	}
	if strings.Count(body, `"delta":{"error":{"health_flag":"upstreamStreamBroken","message":"unexpected EOF"}}`) != 1 {
		t.Fatalf("expected exactly one terminal failure chat chunk, got %s", body)
	}
	if !strings.Contains(body, `data: [DONE]`) {
		t.Fatalf("expected disconnect path to end with DONE marker, got %s", body)
	}
	if strings.Contains(body, `"code":"upstream_error"`) || strings.Contains(body, `"type":"proxy_error"`) {
		t.Fatalf("expected no plain JSON error body after SSE start, got %s", body)
	}
}
