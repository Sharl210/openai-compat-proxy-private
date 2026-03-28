package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestResponsesRouteUsesChatUpstreamEndpointType(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from chat upstream"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
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

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("expected chat upstream path, got %q", gotPath)
	}
	if !strings.Contains(rec.Body.String(), `"object":"response"`) || !strings.Contains(rec.Body.String(), `"hello from chat upstream"`) {
		t.Fatalf("expected responses output normalized from chat upstream, got %s", rec.Body.String())
	}
}

func TestResponsesRoutePreservesTopLevelFieldsAcrossChatUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from chat upstream"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","previous_response_id":"resp_123","metadata":{"trace_id":"trace_123"},"parallel_tool_calls":true,"truncation":"auto","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"previous_response_id":"resp_123"`) || !strings.Contains(body, `"parallel_tool_calls":true`) || !strings.Contains(body, `"truncation":"auto"`) || !strings.Contains(body, `"trace_id":"trace_123"`) {
		t.Fatalf("expected preserved top-level fields in responses output, got %s", body)
	}
}

func TestResponsesRoutePreservesTopLevelFieldsAcrossAnthropicUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello from anthropic upstream"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","previous_response_id":"resp_123","metadata":{"trace_id":"trace_123"},"parallel_tool_calls":false,"truncation":"auto","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"previous_response_id":"resp_123"`) || !strings.Contains(body, `"parallel_tool_calls":false`) || !strings.Contains(body, `"truncation":"auto"`) || !strings.Contains(body, `"trace_id":"trace_123"`) {
		t.Fatalf("expected preserved top-level fields in responses output, got %s", body)
	}
}

func TestAnthropicRouteMapsReasoningSuffixToThinkingWhenEnabled(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true, EnableReasoningEffortSuffix: true, MapReasoningSuffixToAnthropicThinking: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-opus-4-6-high","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(gotBody, `"thinking":{"type":"adaptive"}`) {
		t.Fatalf("expected anthropic upstream payload to include adaptive thinking config, got %s", gotBody)
	}
	if !strings.Contains(gotBody, `"output_config":{"effort":"high"}`) {
		t.Fatalf("expected anthropic upstream payload to include output_config effort, got %s", gotBody)
	}
}

func TestChatRouteUsesAnthropicUpstreamEndpointType(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Header.Get("x-api-key") != "test-key" {
			t.Fatalf("expected x-api-key header, got %q", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Fatalf("expected anthropic-version header, got %q", r.Header.Get("anthropic-version"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello from anthropic upstream"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "anthropic",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                        "anthropic",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      config.UpstreamEndpointTypeAnthropic,
			SupportsResponses:         true,
			SupportsChat:              true,
			SupportsAnthropicMessages: true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/messages" {
		t.Fatalf("expected anthropic upstream path, got %q", gotPath)
	}
	if !strings.Contains(rec.Body.String(), `"object":"chat.completion"`) || !strings.Contains(rec.Body.String(), `"hello from anthropic upstream"`) {
		t.Fatalf("expected chat output normalized from anthropic upstream, got %s", rec.Body.String())
	}
}

func TestAnthropicRouteUsesChatUpstreamReasoningAsThinking(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"final answer","reasoning_content":"think first"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "anthropic",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                        "anthropic",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      config.UpstreamEndpointTypeChat,
			SupportsResponses:         true,
			SupportsChat:              true,
			SupportsAnthropicMessages: true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"thinking"`) || !strings.Contains(body, `"thinking":"think first"`) {
		t.Fatalf("expected reasoning_content to map into anthropic thinking block, got %s", body)
	}
	if !strings.Contains(body, `"type":"text"`) || !strings.Contains(body, `"text":"final answer"`) {
		t.Fatalf("expected final text to remain present, got %s", body)
	}
}

func TestAnthropicRouteUsesChatUpstreamToolCallsAsToolUse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"search_web","arguments":"{\"query\":\"weather\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "anthropic",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                        "anthropic",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      config.UpstreamEndpointTypeChat,
			SupportsResponses:         true,
			SupportsChat:              true,
			SupportsAnthropicMessages: true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"tool_use"`) || !strings.Contains(body, `"name":"search_web"`) {
		t.Fatalf("expected chat tool_calls to map into anthropic tool_use, got %s", body)
	}
	if !strings.Contains(body, `"query":"weather"`) {
		t.Fatalf("expected tool arguments preserved, got %s", body)
	}
}

func TestChatRouteUsesAnthropicUpstreamToolUseAsToolCalls(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"call_1","name":"search_web","input":{"query":"weather"}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "anthropic",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                        "anthropic",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      config.UpstreamEndpointTypeAnthropic,
			SupportsResponses:         true,
			SupportsChat:              true,
			SupportsAnthropicMessages: true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"tool_calls"`) || !strings.Contains(body, `"name":"search_web"`) {
		t.Fatalf("expected anthropic tool_use to map into chat tool_calls, got %s", body)
	}
	if !strings.Contains(body, `"arguments":"{\"query\":\"weather\"}"`) {
		t.Fatalf("expected tool arguments preserved in chat output, got %s", body)
	}
}

func TestResponsesRouteRestoresPreviousToolUseForAnthropicFollowUp(t *testing.T) {
	requestCount := 0
	var secondBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		bodyBytes, _ := io.ReadAll(r.Body)
		if requestCount == 2 {
			secondBody = string(bodyBytes)
		}
		w.Header().Set("Content-Type", "application/json")
		if requestCount == 1 {
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"call_1","name":"search_web","input":{"query":"weather"}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":3}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstRec := httptest.NewRecorder()
	server.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("expected first status 200, got %d body=%s", firstRec.Code, firstRec.Body.String())
	}
	var firstResp map[string]any
	if err := json.Unmarshal(firstRec.Body.Bytes(), &firstResp); err != nil {
		t.Fatalf("unmarshal first response: %v", err)
	}
	responseID, _ := firstResp["id"].(string)
	if responseID == "" {
		t.Fatalf("expected first responses output to include id, got %#v", firstResp)
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","previous_response_id":"`+responseID+`","input":[{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}]}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected second status 200, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if !strings.Contains(secondBody, `"type":"tool_use"`) || !strings.Contains(secondBody, `"id":"call_1"`) {
		t.Fatalf("expected second anthropic request to restore previous assistant tool_use, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"type":"tool_result"`) || !strings.Contains(secondBody, `"tool_use_id":"call_1"`) {
		t.Fatalf("expected second anthropic request to include current tool_result, got %s", secondBody)
	}
}

func TestResponsesStreamRouteRestoresPreviousToolUseForAnthropicFollowUp(t *testing.T) {
	var secondBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		bodyText := string(bodyBytes)
		if strings.Contains(bodyText, `"tool_result"`) {
			secondBody = string(bodyBytes)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: message_start\n" +
			"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
			"event: content_block_start\n" +
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"search_web\"}}\n\n" +
			"event: content_block_delta\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"query\\\":\\\"weather\\\"}\"}}\n\n" +
			"event: message_delta\n" +
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"input_tokens\":2,\"output_tokens\":3}}\n\n" +
			"event: message_stop\n" +
			"data: {\"type\":\"message_stop\"}\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","stream":true,"input":"hello"}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstRec := httptest.NewRecorder()
	server.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("expected first status 200, got %d body=%s", firstRec.Code, firstRec.Body.String())
	}
	responseID := "resp_call_1"
	if !strings.Contains(firstRec.Body.String(), `"call_id":"call_1"`) {
		t.Fatalf("expected first stream to include tool call call_1, got %s", firstRec.Body.String())
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","previous_response_id":"`+responseID+`","input":[{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}]}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected second status 200, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if !strings.Contains(secondBody, `"type":"tool_use"`) || !strings.Contains(secondBody, `"id":"call_1"`) {
		t.Fatalf("expected second anthropic request to restore previous streamed tool_use, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"type":"tool_result"`) || !strings.Contains(secondBody, `"tool_use_id":"call_1"`) {
		t.Fatalf("expected second anthropic request to include tool_result after streamed first round, got %s", secondBody)
	}
}

func TestResponsesRoutePreservesChatFinishReasonLength(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"partial"},"finish_reason":"length"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"status":"incomplete"`) || !strings.Contains(body, `"reason":"length"`) {
		t.Fatalf("expected responses output to preserve length reason, got %s", body)
	}
}

func TestChatRoutePreservesAnthropicStopReasonMaxTokens(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"partial"}],"stop_reason":"max_tokens","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"finish_reason":"length"`) {
		t.Fatalf("expected chat output to map anthropic max_tokens into length, got %s", rec.Body.String())
	}
}

func TestAnthropicRouteRejectsAudioWhenUpstreamIsAnthropic(t *testing.T) {
	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: "https://example.com", UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"input_audio","input_audio":{"data":"YWJj","format":"mp3"}}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `unsupported anthropic content type: input_audio`) {
		t.Fatalf("expected explicit anthropic audio rejection, got %s", rec.Body.String())
	}
}

func TestResponsesRouteMapsChatRefusal(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":null,"refusal":"nope"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()
	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"type":"refusal"`) || !strings.Contains(rec.Body.String(), `"refusal":"nope"`) {
		t.Fatalf("expected chat refusal to map into responses payload, got %s", rec.Body.String())
	}
}
