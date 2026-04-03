package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

var responseCreatedIDPattern = regexp.MustCompile(`event: response\.created\s+data: \{"response":\{"id":"([^"]+)"[^}]*\}`)

func firstResponseIDFromStreamBody(t *testing.T, body string) string {
	t.Helper()
	matches := responseCreatedIDPattern.FindStringSubmatch(body)
	if len(matches) != 2 {
		t.Fatalf("expected stream body to expose a response id, got %s", body)
	}
	return matches[1]
}

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

func TestResponsesRouteUsesRealChatUpstreamResponseID(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_123","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from chat upstream"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
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
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response body: %v", err)
	}
	if got, _ := body["id"].(string); got != "chatcmpl_123" {
		t.Fatalf("expected responses output to keep upstream chat id chatcmpl_123, got %#v", body["id"])
	}
	if strings.HasPrefix(body["id"].(string), "resp_") {
		t.Fatalf("expected upstream chat id instead of synthesized proxy id, got %#v", body["id"])
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

func TestResponsesRouteDoesNotEchoArbitraryUnknownRequestFieldsBackIntoOutput(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from chat upstream"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","custom_flag":"alpha","custom_config":{"trace_id":"trace_123"},"previous_response_id":"resp_123","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `"custom_flag":"alpha"`) || strings.Contains(body, `"trace_id":"trace_123"`) {
		t.Fatalf("expected unknown request-only fields to stay out of normalized responses output, got %s", body)
	}
	if !strings.Contains(body, `"previous_response_id":"resp_123"`) {
		t.Fatalf("expected explicitly preserved responses field to remain, got %s", body)
	}
}

func TestResponsesRouteDoesNotForwardStoreIncludeToChatUpstream(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from chat upstream"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","store":false,"include":["reasoning.encrypted_content"],"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal chat upstream payload: %v body=%s", err, gotBody)
	}
	if _, exists := payload["store"]; exists {
		t.Fatalf("expected store to stay out of chat upstream payload, got %#v", payload)
	}
	if _, exists := payload["include"]; exists {
		t.Fatalf("expected include to stay out of chat upstream payload, got %#v", payload)
	}
	if got, _ := payload["model"].(string); got != "gpt-5" {
		t.Fatalf("expected regular responses fields to keep working, got %#v", payload)
	}
}

func TestResponsesRouteDoesNotForwardStoreIncludeToAnthropicUpstream(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello from anthropic upstream"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","store":false,"include":["reasoning.encrypted_content"],"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal anthropic upstream payload: %v body=%s", err, gotBody)
	}
	if _, exists := payload["store"]; exists {
		t.Fatalf("expected store to stay out of anthropic upstream payload, got %#v", payload)
	}
	if _, exists := payload["include"]; exists {
		t.Fatalf("expected include to stay out of anthropic upstream payload, got %#v", payload)
	}
	if got, _ := payload["model"].(string); got == "" {
		t.Fatalf("expected regular responses fields to keep working, got %#v", payload)
	}
}

func TestChatRoutePreservesUnhandledTopLevelFieldsAcrossChatUpstream(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from chat upstream"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","custom_flag":"alpha","custom_config":{"trace_id":"trace_123","nested":true},"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal chat upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["custom_flag"].(string); got != "alpha" {
		t.Fatalf("expected custom_flag passthrough, got %#v body=%s", payload["custom_flag"], gotBody)
	}
	customConfig, _ := payload["custom_config"].(map[string]any)
	if got, _ := customConfig["trace_id"].(string); got != "trace_123" {
		t.Fatalf("expected custom_config passthrough, got %#v body=%s", payload["custom_config"], gotBody)
	}
}

func TestAnthropicRoutePreservesUnhandledTopLevelFieldsAcrossAnthropicUpstream(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello from anthropic upstream"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":128,"service_tier":"flex","custom_config":{"trace_id":"trace_123","mode":"x"},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal anthropic upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["service_tier"].(string); got != "flex" {
		t.Fatalf("expected service_tier passthrough, got %#v body=%s", payload["service_tier"], gotBody)
	}
	customConfig, _ := payload["custom_config"].(map[string]any)
	if got, _ := customConfig["trace_id"].(string); got != "trace_123" {
		t.Fatalf("expected custom_config passthrough, got %#v body=%s", payload["custom_config"], gotBody)
	}
}

func TestChatRoutePrependsProviderPromptIntoChatUpstreamSystemMessage(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from chat upstream"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true, SystemPromptText: "provider system", SystemPromptPosition: config.SystemPromptPositionPrepend}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"system","content":"chat system"},{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal chat upstream payload: %v body=%s", err, gotBody)
	}
	messages, _ := payload["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("expected merged system message plus user message, got %#v", payload["messages"])
	}
	first, _ := messages[0].(map[string]any)
	if role, _ := first["role"].(string); role != "system" {
		t.Fatalf("expected first chat upstream message role system, got %#v", first)
	}
	content, _ := first["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("expected merged system content parts, got %#v body=%s", first["content"], gotBody)
	}
	firstPart, _ := content[0].(map[string]any)
	secondPart, _ := content[1].(map[string]any)
	if text, _ := firstPart["text"].(string); text != "provider system\n\n" {
		t.Fatalf("expected prepended provider text part, got %#v body=%s", firstPart, gotBody)
	}
	if text, _ := secondPart["text"].(string); text != "chat system" {
		t.Fatalf("expected original system text part to remain, got %#v body=%s", secondPart, gotBody)
	}
}

func TestResponsesRouteAppendsProviderPromptIntoResponsesUpstreamInstructions(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","output":[{"id":"msg_123","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello from responses upstream"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeResponses, SupportsResponses: true, SupportsChat: true, SystemPromptText: "provider system", SystemPromptPosition: config.SystemPromptPositionAppend}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","instructions":"user instructions","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal responses upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["instructions"].(string); got != "user instructions\n\nprovider system" {
		t.Fatalf("expected appended provider prompt in responses instructions, got %#v body=%s", payload["instructions"], gotBody)
	}
	input, _ := payload["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("expected user input items to remain unchanged, got %#v", payload["input"])
	}
}

func TestChatRouteUsesProviderPromptAsSystemMessageWhenClientHasNoInstructionRole(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from chat upstream"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true, SystemPromptText: "provider system"}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal chat upstream payload: %v body=%s", err, gotBody)
	}
	messages, _ := payload["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("expected provider system message plus user message, got %#v", payload["messages"])
	}
	first, _ := messages[0].(map[string]any)
	if role, _ := first["role"].(string); role != "system" {
		t.Fatalf("expected first chat upstream message role system, got %#v", first)
	}
	if text, _ := first["content"].(string); text != "provider system" {
		t.Fatalf("expected provider prompt to become standalone system message, got %#v body=%s", first["content"], gotBody)
	}
}

func TestResponsesRouteUsesProviderPromptAsInstructionsWhenClientHasNoInstructions(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","output":[{"id":"msg_123","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello from responses upstream"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeResponses, SupportsResponses: true, SupportsChat: true, SystemPromptText: "provider system"}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal responses upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["instructions"].(string); got != "provider system" {
		t.Fatalf("expected provider prompt to become responses instructions, got %#v body=%s", payload["instructions"], gotBody)
	}
}

func TestAnthropicRouteAppendsProviderPromptIntoAnthropicSystemField(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello from anthropic upstream"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true, SystemPromptText: "provider system", SystemPromptPosition: config.SystemPromptPositionAppend}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":128,"system":"anthropic system","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal anthropic upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["system"].(string); got != "anthropic system\n\nprovider system" {
		t.Fatalf("expected appended provider prompt in anthropic system field, got %#v body=%s", payload["system"], gotBody)
	}
	messages, _ := payload["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected anthropic user messages to remain intact, got %#v", payload["messages"])
	}
}

func TestAnthropicRouteUsesProviderPromptAsSystemWhenClientHasNoSystem(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello from anthropic upstream"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true, SystemPromptText: "provider system"}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal anthropic upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["system"].(string); got != "provider system" {
		t.Fatalf("expected provider prompt to become anthropic system field, got %#v body=%s", payload["system"], gotBody)
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

func TestChatRouteMapsReasoningSuffixToAnthropicThinkingWhenEnabled(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true, EnableReasoningEffortSuffix: true, MapReasoningSuffixToAnthropicThinking: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"claude-opus-4-6-high","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
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

func TestResponsesRouteMapsReasoningSuffixToAnthropicThinkingWhenEnabled(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true, EnableReasoningEffortSuffix: true, MapReasoningSuffixToAnthropicThinking: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"claude-opus-4-6-high","max_output_tokens":128,"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
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

func TestChatRouteDoesNotMapThinkingWhenUpstreamIsNotAnthropic(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true, EnableReasoningEffortSuffix: true, MapReasoningSuffixToAnthropicThinking: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"claude-opus-4-6-high","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(gotBody, `"thinking":`) || strings.Contains(gotBody, `"output_config":`) {
		t.Fatalf("expected non-anthropic chat upstream payload to avoid anthropic thinking fields, got %s", gotBody)
	}
}

func TestResponsesRouteDoesNotMapThinkingWhenUpstreamIsNotAnthropic(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true, EnableReasoningEffortSuffix: true, MapReasoningSuffixToAnthropicThinking: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"claude-opus-4-6-high","max_output_tokens":128,"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(gotBody, `"thinking":`) || strings.Contains(gotBody, `"output_config":`) {
		t.Fatalf("expected non-anthropic responses upstream payload to avoid anthropic thinking fields, got %s", gotBody)
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

func TestChatRouteHoistsInstructionRolesIntoAnthropicSystem(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
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
			SystemPromptText:          "provider system",
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"system","content":"chat system"},{"role":"developer","content":"chat developer"},{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["system"].(string); got != "provider system\n\nchat system\n\nchat developer" {
		t.Fatalf("expected anthropic system to include provider + chat instruction roles, got %#v body=%s", payload["system"], gotBody)
	}
	messages, _ := payload["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected only non-instruction messages to remain, got %#v", payload["messages"])
	}
	message, _ := messages[0].(map[string]any)
	if role, _ := message["role"].(string); role != "user" {
		t.Fatalf("expected remaining anthropic message role user, got %#v", message)
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
	if responseID != "msg_1" {
		t.Fatalf("expected first responses output to keep upstream anthropic message id msg_1, got %#v", firstResp["id"])
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
	if !strings.Contains(secondBody, `"role":"user"`) || !strings.Contains(secondBody, `"hello"`) {
		t.Fatalf("expected second anthropic request to preserve original user question context, got %s", secondBody)
	}
}

func TestResponsesRouteUsesResponsesUpstreamForFunctionCallFollowUp(t *testing.T) {
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
			_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","output":[{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"search_web","arguments":"{\"query\":\"weather\"}"}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"resp_2","object":"response","output":[{"id":"msg_2","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "resp", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "resp", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeResponses, SupportsResponses: true, SupportsChat: true}}})

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello","tools":[{"type":"function","name":"search_web","description":"Search","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}]}`))
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
	if responseID != "resp_1" {
		t.Fatalf("expected first responses output to keep upstream response id resp_1, got %#v", firstResp["id"])
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","previous_response_id":"`+responseID+`","input":[{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}]}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected second status 200, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if !strings.Contains(secondBody, `"previous_response_id":"resp_1"`) {
		t.Fatalf("expected responses upstream follow-up to preserve previous_response_id, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"type":"function_call_output"`) || !strings.Contains(secondBody, `"call_id":"call_1"`) {
		t.Fatalf("expected responses upstream follow-up to preserve function_call_output, got %s", secondBody)
	}
}

func TestResponsesRouteUsesResponsesUpstreamForComplexFunctionCallFollowUp(t *testing.T) {
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
			_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","output":[{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"search_web","arguments":"{\"query\":\"weather\"}"}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"resp_2","object":"response","output":[{"id":"msg_2","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "resp", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "resp", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeResponses, SupportsResponses: true, SupportsChat: true}}})

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello","tools":[{"type":"function","name":"search_web","description":"Search","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}]}`))
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

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","previous_response_id":"`+responseID+`","parallel_tool_calls":true,"metadata":{"trace_id":"trace_123"},"input":[{"role":"user","content":"hello"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"search_web","arguments":"{\"query\":\"weather\"}"}}]},{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}]}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected second status 200, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if !strings.Contains(secondBody, `"previous_response_id":"resp_1"`) {
		t.Fatalf("expected complex responses follow-up to preserve previous_response_id, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"parallel_tool_calls":true`) || !strings.Contains(secondBody, `"trace_id":"trace_123"`) {
		t.Fatalf("expected complex responses follow-up to preserve top-level stateful fields, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"role":"assistant"`) || !strings.Contains(secondBody, `"tool_calls"`) {
		t.Fatalf("expected complex responses follow-up to preserve assistant tool_call history, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"type":"function_call_output"`) || !strings.Contains(secondBody, `"call_id":"call_1"`) {
		t.Fatalf("expected complex responses follow-up to preserve function_call_output, got %s", secondBody)
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
	responseID := firstResponseIDFromStreamBody(t, firstRec.Body.String())
	if !strings.Contains(firstRec.Body.String(), `"call_id":"call_1"`) {
		t.Fatalf("expected first stream to include tool call call_1, got %s", firstRec.Body.String())
	}
	if responseID != "msg_1" {
		t.Fatalf("expected first stream to expose real upstream anthropic id msg_1, got %q body=%s", responseID, firstRec.Body.String())
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
	if !strings.Contains(secondBody, `"role":"user"`) || !strings.Contains(secondBody, `"hello"`) {
		t.Fatalf("expected second anthropic streamed follow-up to preserve original user question context, got %s", secondBody)
	}
}

func TestResponsesRouteSkipsPreviousHistoryRestoreWhenClientAlreadySendsHistory(t *testing.T) {
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

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","previous_response_id":"`+responseID+`","input":[{"role":"user","content":"hello"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"search_web","arguments":"{\"query\":\"weather\"}"}}]},{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}]}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected second status 200, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if strings.Count(secondBody, `"type":"tool_use"`) != 1 {
		t.Fatalf("expected client-provided history to avoid duplicate restored tool_use, got %s", secondBody)
	}
	if strings.Count(secondBody, `"role":"user"`) != 2 {
		t.Fatalf("expected one original user plus current tool_result wrapper, got %s", secondBody)
	}
}

func TestResponsesRouteDedupesDuplicateToolResultsFromClientFollowUp(t *testing.T) {
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

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","previous_response_id":"`+responseID+`","input":[{"role":"user","content":"hello"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"search_web","arguments":"{\"query\":\"weather\"}"}}]},{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"},{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}]}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected second status 200, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if strings.Count(secondBody, `"tool_use_id":"call_1"`) != 1 {
		t.Fatalf("expected duplicate tool_result to be deduped before upstream request, got %s", secondBody)
	}
}

func TestResponsesRouteHoistsInstructionsAndDeveloperIntoAnthropicSystem(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello from anthropic upstream"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true, SystemPromptText: "provider system"}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","instructions":"response instructions","input":[{"role":"developer","content":[{"type":"input_text","text":"response developer"}]},{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["system"].(string); got != "provider system\n\nresponse instructions\n\nresponse developer" {
		t.Fatalf("expected anthropic system to include instructions + developer + provider prompt, got %#v body=%s", payload["system"], gotBody)
	}
	messages, _ := payload["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected only user message to remain after hoisting instruction roles, got %#v", payload["messages"])
	}
	message, _ := messages[0].(map[string]any)
	if role, _ := message["role"].(string); role != "user" {
		t.Fatalf("expected remaining anthropic message role user, got %#v", message)
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

func TestChatUpstreamToResponsesDownstreamUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		_, _ = w.Write([]byte("data: {\"id\":\"chat-123\",\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"))
		flusher.Flush()

		_, _ = w.Write([]byte("data: {\"id\":\"chat-123\",\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n"))
		flusher.Flush()

		_, _ = w.Write([]byte("data: {\"id\":\"chat-123\",\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15},\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n"))
		flusher.Flush()

		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
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

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	body := rec.Body.String()
	t.Logf("Response body:\n%s", body)
	if count := strings.Count(body, `event: response.created`); count != 1 {
		t.Fatalf("expected exactly one response.created event, got count=%d body=\n%s", count, body)
	}
	if responseID := firstResponseIDFromStreamBody(t, body); responseID != "chat-123" {
		t.Fatalf("expected response.created to use upstream chat id chat-123, got %q body=\n%s", responseID, body)
	}

	if !strings.Contains(body, "input_tokens") {
		t.Errorf("expected response to contain input_tokens, got:\n%s", body)
	}
	if !strings.Contains(body, "output_tokens") {
		t.Errorf("expected response to contain output_tokens, got:\n%s", body)
	}
	if !strings.Contains(body, "total_tokens") {
		t.Errorf("expected response to contain total_tokens, got:\n%s", body)
	}
	if !strings.Contains(body, "event: response.completed") {
		t.Errorf("expected response to contain response.completed event, got:\n%s", body)
	}
}
