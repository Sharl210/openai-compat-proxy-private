package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newChatFormatUpstream creates an upstream that speaks chat/completions protocol
func newChatFormatUpstream(t *testing.T, responseBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody))
	}))
}

// newAnthropicFormatUpstream creates an upstream that speaks anthropic /messages protocol
func newAnthropicFormatUpstream(t *testing.T, responseBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody))
	}))
}

// newChatStreamingUpstream creates an upstream that speaks chat/completions streaming protocol
func newChatStreamingUpstream(t *testing.T, events []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, evt := range events {
			_, _ = fmt.Fprint(w, evt)
			flusher.Flush()
		}
	}))
}

// newAnthropicStreamingUpstream creates an upstream that speaks anthropic /messages streaming protocol
func newAnthropicStreamingUpstream(t *testing.T, events []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, evt := range events {
			_, _ = fmt.Fprint(w, evt)
			flusher.Flush()
		}
	}))
}

// ---------------------------------------------------------------------------
// Chat upstream → Responses downstream: tool args extraction boundary
// ---------------------------------------------------------------------------

func TestChatUpstreamToolArgsPreservedInResponsesNonStream(t *testing.T) {
	// Chat upstream returns tool_calls with JSON string arguments
	chatResponse := `{
		"id": "chatcmpl_123",
		"object": "chat.completion",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": null,
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {
						"name": "get_weather",
						"arguments": "{\"city\":\"Shanghai\",\"unit\":\"celsius\"}"
					}
				}]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
	}`
	upstream := newChatFormatUpstream(t, chatResponse)
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

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model": "gpt-5",
		"input": "hello",
		"tools": [{"type": "function", "name": "get_weather", "description": "Get weather", "parameters": {"type": "object", "properties": {"city": {"type": "string"}, "unit": {"type": "string"}}, "required": ["city"]}}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	output, _ := resp["output"].([]any)
	if len(output) == 0 {
		t.Fatalf("expected output items, got none body=%s", rec.Body.String())
	}

	// Find the function_call output item
	var foundArgs string
	for _, item := range output {
		if m, ok := item.(map[string]any); ok {
			if m["type"] == "function_call" {
				if args, ok := m["arguments"].(string); ok {
					foundArgs = args
					break
				}
			}
		}
	}

	if foundArgs == "" {
		t.Fatalf("expected function_call with arguments in output, got body=%s", rec.Body.String())
	}

	// Verify the JSON arguments are preserved as a proper JSON string
	if !strings.Contains(foundArgs, "Shanghai") || !strings.Contains(foundArgs, "celsius") {
		t.Fatalf("expected arguments to contain city and unit, got %q body=%s", foundArgs, rec.Body.String())
	}
}

func TestChatUpstreamToolArgsPreservedInChatDownstreamNonStream(t *testing.T) {
	// Chat upstream returns tool_calls with JSON string arguments
	chatResponse := `{
		"id": "chatcmpl_123",
		"object": "chat.completion",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": null,
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {
						"name": "get_weather",
						"arguments": "{\"city\":\"Beijing\"}"
					}
				}]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
	}`
	upstream := newChatFormatUpstream(t, chatResponse)
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

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model": "gpt-5",
		"messages": [{"role": "user", "content": "hello"}],
		"tools": [{"type": "function", "name": "get_weather", "description": "Get weather", "parameters": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]}}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	choices, _ := resp["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("expected choices, got none body=%s", rec.Body.String())
	}

	choice, _ := choices[0].(map[string]any)
	msg, _ := choice["message"].(map[string]any)
	toolCalls, _ := msg["tool_calls"].([]any)

	if len(toolCalls) == 0 {
		t.Fatalf("expected tool_calls in chat output, got body=%s", rec.Body.String())
	}

	tc, _ := toolCalls[0].(map[string]any)
	fn, _ := tc["function"].(map[string]any)
	args, _ := fn["arguments"].(string)

	if !strings.Contains(args, "Beijing") {
		t.Fatalf("expected arguments to contain Beijing, got %q body=%s", args, rec.Body.String())
	}
}

func TestChatUpstreamToolArgsPreservedInAnthropicDownstreamNonStream(t *testing.T) {
	// Chat upstream returns tool_calls with JSON string arguments
	chatResponse := `{
		"id": "chatcmpl_123",
		"object": "chat.completion",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": null,
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {
						"name": "get_weather",
						"arguments": "{\"city\":\"Tokyo\"}"
					}
				}]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
	}`
	upstream := newChatFormatUpstream(t, chatResponse)
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                        "openai",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      config.UpstreamEndpointTypeChat,
			SupportsResponses:         true,
			SupportsChat:              true,
			SupportsAnthropicMessages: true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model": "claude-sonnet-4-5",
		"max_tokens": 128,
		"messages": [{"role": "user", "content": [{"type": "text", "text": "hello"}]}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	content, _ := resp["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("expected content blocks, got body=%s", rec.Body.String())
	}

	// Find the tool_use block
	var foundArgs string
	for _, block := range content {
		if m, ok := block.(map[string]any); ok {
			if m["type"] == "tool_use" {
				input, _ := m["input"].(map[string]any)
				if input != nil {
					if city, ok := input["city"].(string); ok {
						foundArgs = city
						break
					}
				}
				// Arguments might be nested differently
				if argsRaw, ok := m["arguments"]; ok {
					if argsStr, ok := argsRaw.(string); ok {
						foundArgs = argsStr
					}
				}
			}
		}
	}

	if !strings.Contains(foundArgs, "Tokyo") && !strings.Contains(rec.Body.String(), "Tokyo") {
		t.Fatalf("expected Tokyo in tool_use output, got body=%s", rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Anthropic upstream → Responses downstream: tool args extraction boundary
// ---------------------------------------------------------------------------

func TestAnthropicUpstreamToolArgsPreservedInResponsesNonStream(t *testing.T) {
	// Anthropic upstream returns tool_use with structured input
	anthropicResponse := `{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"model": "claude-sonnet-4-5",
		"content": [
			{"type": "text", "text": "The weather in Shanghai is 25C."},
			{"type": "tool_use", "id": "toolu_1", "name": "get_weather", "input": {"city": "Shanghai", "unit": "celsius"}}
		],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 10, "output_tokens": 20}
	}`
	upstream := newAnthropicFormatUpstream(t, anthropicResponse)
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

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model": "claude-sonnet-4-5",
		"input": "hello"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	output, _ := resp["output"].([]any)
	if len(output) == 0 {
		t.Fatalf("expected output items, got body=%s", rec.Body.String())
	}

	// Find the function_call output item
	var foundName string
	var foundArgs string
	for _, item := range output {
		if m, ok := item.(map[string]any); ok {
			if m["type"] == "function_call" {
				foundName, _ = m["name"].(string)
				if args, ok := m["arguments"].(string); ok {
					foundArgs = args
				}
				break
			}
		}
	}

	if foundName != "get_weather" {
		t.Fatalf("expected function_call name get_weather, got %q body=%s", foundName, rec.Body.String())
	}

	if !strings.Contains(foundArgs, "Shanghai") || !strings.Contains(foundArgs, "celsius") {
		t.Fatalf("expected arguments to contain Shanghai and celsius, got %q body=%s", foundArgs, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Reasoning / thinking extraction boundary tests
// ---------------------------------------------------------------------------

func TestChatUpstreamReasoningPreservedInResponsesNonStream(t *testing.T) {
	// Chat upstream returns reasoning_content
	chatResponse := `{
		"id": "chatcmpl_123",
		"object": "chat.completion",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "final answer",
				"reasoning_content": "thinking step by step"
			},
			"finish_reason": "stop"
		}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
	}`
	upstream := newChatFormatUpstream(t, chatResponse)
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

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model": "gpt-5",
		"input": "hello"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	reasoning, _ := resp["reasoning"].(map[string]any)
	if reasoning == nil {
		t.Fatalf("expected reasoning field in responses output, got body=%s", rec.Body.String())
	}

	summary, _ := reasoning["summary"].(string)
	if !strings.Contains(summary, "thinking step by step") {
		t.Fatalf("expected reasoning.summary to contain thinking, got %q body=%s", summary, rec.Body.String())
	}
}

func TestAnthropicUpstreamThinkingPreservedInResponsesNonStream(t *testing.T) {
	// Anthropic upstream returns thinking block
	anthropicResponse := `{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"model": "claude-sonnet-4-5",
		"content": [
			{"type": "thinking", "thinking": "let me think about this"},
			{"type": "text", "text": "final answer"}
		],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 10, "output_tokens": 20}
	}`
	upstream := newAnthropicFormatUpstream(t, anthropicResponse)
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

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model": "claude-sonnet-4-5",
		"input": "hello"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	reasoning, _ := resp["reasoning"].(map[string]any)
	if reasoning == nil {
		t.Fatalf("expected reasoning field in responses output from anthropic thinking, got body=%s", rec.Body.String())
	}

	summary, _ := reasoning["summary"].(string)
	if !strings.Contains(summary, "let me think about this") {
		t.Fatalf("expected reasoning.summary to contain thinking, got %q body=%s", summary, rec.Body.String())
	}
}

func TestAnthropicUpstreamThinkingPreservedInChatDownstreamNonStream(t *testing.T) {
	// Anthropic upstream returns thinking block
	anthropicResponse := `{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"model": "claude-sonnet-4-5",
		"content": [
			{"type": "thinking", "thinking": "reasoning through this"},
			{"type": "text", "text": "final answer"}
		],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 10, "output_tokens": 20}
	}`
	upstream := newAnthropicFormatUpstream(t, anthropicResponse)
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

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model": "gpt-5",
		"messages": [{"role": "user", "content": "hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	// Chat output should have reasoning_content
	if !strings.Contains(body, "reasoning_content") && !strings.Contains(body, "reasoning") {
		t.Fatalf("expected reasoning_content in chat output from anthropic thinking, got body=%s", body)
	}
}

// ---------------------------------------------------------------------------
// Usage extraction boundary tests
// ---------------------------------------------------------------------------

func TestChatUpstreamUsagePreservedInResponsesNonStream(t *testing.T) {
	chatResponse := `{
		"id": "chatcmpl_123",
		"object": "chat.completion",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "hello"
			},
			"finish_reason": "stop"
		}],
		"usage": {
			"prompt_tokens": 100,
			"completion_tokens": 50,
			"total_tokens": 150,
			"prompt_tokens_details": {"cached_tokens": 30},
			"completion_tokens_details": {"reasoning_tokens": 10}
		}
	}`
	upstream := newChatFormatUpstream(t, chatResponse)
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

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model": "gpt-5",
		"input": "hello"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	usage, _ := resp["usage"].(map[string]any)
	if usage == nil {
		t.Fatalf("expected usage field, got body=%s", rec.Body.String())
	}

	if got, _ := usage["input_tokens"].(float64); got != 100 {
		t.Fatalf("expected input_tokens=100, got %v body=%s", got, rec.Body.String())
	}
	if got, _ := usage["output_tokens"].(float64); got != 50 {
		t.Fatalf("expected output_tokens=50, got %v body=%s", got, rec.Body.String())
	}
	if got, _ := usage["total_tokens"].(float64); got != 150 {
		t.Fatalf("expected total_tokens=150, got %v body=%s", got, rec.Body.String())
	}
}

func TestAnthropicUpstreamUsagePreservedInResponsesNonStream(t *testing.T) {
	anthropicResponse := `{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"model": "claude-sonnet-4-5",
		"content": [{"type": "text", "text": "hello"}],
		"stop_reason": "end_turn",
		"usage": {
			"input_tokens": 200,
			"output_tokens": 100,
			"cache_creation_input_tokens": 50,
			"cache_read_input_tokens": 100
		}
	}`
	upstream := newAnthropicFormatUpstream(t, anthropicResponse)
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

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model": "claude-sonnet-4-5",
		"input": "hello"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	usage, _ := resp["usage"].(map[string]any)
	if usage == nil {
		t.Fatalf("expected usage field, got body=%s", rec.Body.String())
	}

	// Canonical usage should normalize anthropic diff input into OpenAI-style total input.
	if got, _ := usage["input_tokens"].(float64); got != 350 {
		t.Fatalf("expected normalized input_tokens=350 from anthropic upstream, got %v body=%s", got, rec.Body.String())
	}
	if got, _ := usage["output_tokens"].(float64); got != 100 {
		t.Fatalf("expected output_tokens=100 from anthropic, got %v body=%s", got, rec.Body.String())
	}
	inputDetails, _ := usage["input_tokens_details"].(map[string]any)
	if got, _ := inputDetails["cached_tokens"].(float64); got != 100 {
		t.Fatalf("expected cached_tokens=100 from anthropic upstream, got %v body=%s", got, rec.Body.String())
	}
	if got, _ := inputDetails["cache_creation_tokens"].(float64); got != 50 {
		t.Fatalf("expected cache_creation_tokens=50 from anthropic upstream, got %v body=%s", got, rec.Body.String())
	}
}

func TestChatUpstreamUsageConvertsToAnthropicDiffInputInMessagesNonStream(t *testing.T) {
	chatResponse := `{
		"id": "chatcmpl_123",
		"object": "chat.completion",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "hello"
			},
			"finish_reason": "stop"
		}],
		"usage": {
			"prompt_tokens": 100,
			"completion_tokens": 50,
			"total_tokens": 150,
			"prompt_tokens_details": {"cached_tokens": 30, "cache_creation_tokens": 20}
		}
	}`
	upstream := newChatFormatUpstream(t, chatResponse)
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                        "openai",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      config.UpstreamEndpointTypeChat,
			SupportsResponses:         true,
			SupportsChat:              true,
			SupportsAnthropicMessages: true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model": "claude-sonnet-4-5",
		"max_tokens": 128,
		"messages": [{"role": "user", "content": [{"type": "text", "text": "hello"}]}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	usage, _ := resp["usage"].(map[string]any)
	if usage == nil {
		t.Fatalf("expected usage field, got body=%s", rec.Body.String())
	}
	if got, _ := usage["input_tokens"].(float64); got != 50 {
		t.Fatalf("expected anthropic diff input_tokens=50, got %v body=%s", got, rec.Body.String())
	}
	if got, _ := usage["cache_read_input_tokens"].(float64); got != 30 {
		t.Fatalf("expected cache_read_input_tokens=30, got %v body=%s", got, rec.Body.String())
	}
	if got, _ := usage["cache_creation_input_tokens"].(float64); got != 20 {
		t.Fatalf("expected cache_creation_input_tokens=20, got %v body=%s", got, rec.Body.String())
	}
	if got, _ := usage["output_tokens"].(float64); got != 50 {
		t.Fatalf("expected output_tokens=50, got %v body=%s", got, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Streaming: chat upstream → responses downstream
// ---------------------------------------------------------------------------

func TestChatStreamingUpstreamToolArgsPreservedInResponsesStream(t *testing.T) {
	// Simplified: stream tool_calls with complete arguments in one chunk
	events := []string{
		"data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n",
		"data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\":\\\"Beijing\\\"}\"}}]},\"index\":0}]}\n\n",
		"data: {\"id\":\"chatcmpl_123\",\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15},\"choices\":[{\"finish_reason\":\"tool_calls\",\"index\":0}]}\n\n",
		"data: [DONE]\n\n",
	}
	upstream := newChatStreamingUpstream(t, events)
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
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

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model": "gpt-5",
		"stream": true,
		"input": "hello",
		"tools": [{"type": "function", "name": "get_weather", "description": "Get weather", "parameters": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]}}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, body)
	}

	// Check tool_use appears in the stream
	if !strings.Contains(body, `"type":"tool_use"`) && !strings.Contains(body, `"type":"function_call"`) {
		t.Fatalf("expected tool_use or function_call in responses stream, got %s", body)
	}

	// Check arguments are accumulated - the key test is that partial_json deltas work
	if !strings.Contains(body, "Beijing") {
		t.Fatalf("expected accumulated arguments to contain Beijing, got %s", body)
	}
}

// ---------------------------------------------------------------------------
// Streaming: anthropic upstream → responses downstream
// ---------------------------------------------------------------------------

func TestAnthropicStreamingUpstreamToolArgsPreservedInResponsesStream(t *testing.T) {
	events := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"type\":\"message\",\"role\":\"assistant\"}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"get_weather\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"Tokyo\\\"}\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
	upstream := newAnthropicStreamingUpstream(t, events)
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "anthropic",
		EnableLegacyV1Routes: true,
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

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model": "claude-sonnet-4-5",
		"stream": true,
		"input": "hello"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, body)
	}

	// Check Tokyo appears in the accumulated arguments
	if !strings.Contains(body, "Tokyo") {
		t.Fatalf("expected accumulated arguments to contain Tokyo, got %s", body)
	}
}

// ---------------------------------------------------------------------------
// Streaming: anthropic upstream → anthropic messages downstream
// ---------------------------------------------------------------------------

func TestAnthropicStreamingUpstreamThinkingPreservedInAnthropicMessagesStream(t *testing.T) {
	events := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"type\":\"message\",\"role\":\"assistant\"}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"reasoning here\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"final answer\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
	upstream := newAnthropicStreamingUpstream(t, events)
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "anthropic",
		EnableLegacyV1Routes: true,
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

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model": "claude-sonnet-4-5",
		"stream": true,
		"max_tokens": 128,
		"messages": [{"role": "user", "content": [{"type": "text", "text": "hello"}]}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, body)
	}

	// Check thinking content is preserved
	if !strings.Contains(body, "reasoning here") {
		t.Fatalf("expected thinking content in anthropic messages stream, got %s", body)
	}
	if !strings.Contains(body, `"stop_reason":"end_turn"`) {
		t.Fatalf("expected anthropic messages stream to end with end_turn stop_reason, got %s", body)
	}
}

func TestChatStreamingUpstreamToolUseMapsToAnthropicStopReason(t *testing.T) {
	upstream := newChatStreamingUpstream(t, []string{
		"data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n",
		"data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"search_web\",\"arguments\":\"{\\\"query\\\":\\\"weather\\\",\\\"topic\\\":\\\"general\\\"}\"}}]},\"index\":0}]}\n\n",
		"data: {\"id\":\"chatcmpl_123\",\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15},\"choices\":[{\"finish_reason\":\"tool_calls\",\"index\":0}]}\n\n",
		"data: [DONE]\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "chat",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                        "chat",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      config.UpstreamEndpointTypeChat,
			SupportsChat:              true,
			SupportsAnthropicMessages: true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"max_tokens":128,
		"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, body)
	}
	if !strings.Contains(body, `"stop_reason":"tool_use"`) {
		t.Fatalf("expected anthropic tool_use stop_reason, got %s", body)
	}
	if strings.Contains(body, `"stop_reason":"tool_calls"`) {
		t.Fatalf("expected anthropic stream to avoid OpenAI tool_calls stop_reason, got %s", body)
	}
}

// ---------------------------------------------------------------------------
// Proxy buffer strategy vs upstream_non_stream parity for tool args
// ---------------------------------------------------------------------------

func TestToolArgsParityBetweenProxyBufferAndUpstreamNonStream(t *testing.T) {
	// This test verifies that tool args extraction produces identical results
	// regardless of whether we use proxy_buffer or upstream_non_stream strategy

	chatResponse := `{
		"id": "chatcmpl_123",
		"object": "chat.completion",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": null,
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {
						"name": "search_web",
						"arguments": "{\"query\":\"weather\",\"topic\":\"general\"}"
					}
				}]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
	}`

	// Test proxy_buffer strategy
	proxyBufferResp, proxyBufferArgs := performChatUpstreamToolArgsTest(t, chatResponse, config.DownstreamNonStreamStrategyProxyBuffer)

	// Test upstream_non_stream strategy
	upstreamNonStreamResp, upstreamNonStreamArgs := performChatUpstreamToolArgsTest(t, chatResponse, config.DownstreamNonStreamStrategyUpstreamNonStream)

	// Compare the arguments extracted
	if proxyBufferArgs != upstreamNonStreamArgs {
		t.Fatalf("tool args mismatch between strategies\nproxy_buffer=%q\nupstream_non_stream=%q\nproxy_buffer_resp=%s\nupstream_non_stream_resp=%s",
			proxyBufferArgs, upstreamNonStreamArgs, proxyBufferResp, upstreamNonStreamResp)
	}

	// Verify both have the same structure
	if !strings.Contains(proxyBufferResp, "search_web") || !strings.Contains(upstreamNonStreamResp, "search_web") {
		t.Fatalf("expected search_web in both responses")
	}
}

func performChatUpstreamToolArgsTest(t *testing.T, chatResponse string, strategy string) (string, string) {
	t.Helper()
	var upstream *httptest.Server
	if strategy == config.DownstreamNonStreamStrategyProxyBuffer {
		upstream = newChatStreamingUpstream(t, []string{
			"data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n",
			"data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"search_web\",\"arguments\":\"{\\\"query\\\":\\\"weather\\\",\\\"topic\\\":\\\"general\\\"}\"}}]},\"index\":0}]}\n\n",
			"data: {\"id\":\"chatcmpl_123\",\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15},\"choices\":[{\"finish_reason\":\"tool_calls\",\"index\":0}]}\n\n",
			"data: [DONE]\n\n",
		})
	} else {
		upstream = newChatFormatUpstream(t, chatResponse)
	}
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: strategy,
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

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model": "gpt-5",
		"input": "hello"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("strategy %s: expected status 200, got %d body=%s", strategy, rec.Code, rec.Body.String())
	}

	body := rec.Body.String()

	// Extract arguments
	var args string
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err == nil {
		if output, ok := resp["output"].([]any); ok {
			for _, item := range output {
				if m, ok := item.(map[string]any); ok {
					if m["type"] == "function_call" {
						if a, ok := m["arguments"].(string); ok {
							args = a
							break
						}
					}
				}
			}
		}
	}

	return body, args
}

// ---------------------------------------------------------------------------
// Edge case: nested JSON arguments
// ---------------------------------------------------------------------------

func TestChatUpstreamNestedJSONArgsPreservedInResponses(t *testing.T) {
	// Chat upstream returns tool_calls with deeply nested JSON arguments
	chatResponse := `{
		"id": "chatcmpl_123",
		"object": "chat.completion",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": null,
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {
						"name": "search_nested",
						"arguments": "{\"filter\":{\"area\":{\"city\":{\"name\":\"Shanghai\"}},\"tags\":[\"weather\",\"temp\"]}}"
					}
				}]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
	}`
	upstream := newChatFormatUpstream(t, chatResponse)
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

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model": "gpt-5",
		"input": "hello"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()

	// Verify the nested JSON structure is preserved
	if !strings.Contains(body, "Shanghai") {
		t.Fatalf("expected nested city Shanghai in arguments, got %s", body)
	}
	if !strings.Contains(body, "weather") || !strings.Contains(body, "temp") {
		t.Fatalf("expected nested tags in arguments, got %s", body)
	}
}

// ---------------------------------------------------------------------------
// Test that function_call output items with id collision are handled
// ---------------------------------------------------------------------------

func TestChatUpstreamMultipleToolCallsWithDifferentIDsPreserved(t *testing.T) {
	chatResponse := `{
		"id": "chatcmpl_123",
		"object": "chat.completion",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": null,
				"tool_calls": [
					{"id": "call_1", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"Beijing\"}"}},
					{"id": "call_2", "type": "function", "function": {"name": "get_time", "arguments": "{\"tz\":\"Asia/Shanghai\"}"}}
				]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
	}`
	upstream := newChatFormatUpstream(t, chatResponse)
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

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model": "gpt-5",
		"input": "hello"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	output, _ := resp["output"].([]any)
	if len(output) != 2 {
		t.Fatalf("expected 2 function_call items, got %d body=%s", len(output), rec.Body.String())
	}

	names := make(map[string]bool)
	for _, item := range output {
		if m, ok := item.(map[string]any); ok {
			if name, ok := m["name"].(string); ok {
				names[name] = true
			}
		}
	}

	if !names["get_weather"] {
		t.Fatalf("expected get_weather in output, got %s", rec.Body.String())
	}
	if !names["get_time"] {
		t.Fatalf("expected get_time in output, got %s", rec.Body.String())
	}
}
