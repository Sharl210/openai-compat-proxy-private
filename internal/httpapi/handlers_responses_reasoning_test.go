package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/testutil"
)

func TestResponsesNonStreamPreservesReasoningOutputItems(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"rs_123\",\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"thinking\"}],\"encrypted_content\":\"enc_123\"}}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(testResponsesConfig(upstream.URL))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"store":false,
		"include":["reasoning.encrypted_content"],
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

	reasoning, _ := payload["reasoning"].(map[string]any)
	if got, _ := reasoning["summary"].(string); got != "thinking" {
		t.Fatalf("expected top-level reasoning summary preserved, got %#v", payload["reasoning"])
	}
	output, _ := payload["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("expected real upstream reasoning output item preserved, got %#v", payload["output"])
	}
	item, _ := output[0].(map[string]any)
	if got, _ := item["type"].(string); got != "reasoning" {
		t.Fatalf("expected reasoning output item preserved, got %#v", item)
	}
	itemID, _ := item["id"].(string)
	if itemID == "" {
		t.Fatalf("expected reasoning output item id preserved, got %#v", item)
	}
	if got, _ := item["type"].(string); got != "reasoning" {
		t.Fatalf("expected reasoning output item preserved, got %#v", item)
	}
	content, _ := item["content"].([]any)
	if len(content) != 0 {
		t.Fatalf("expected preserved reasoning item to stay separate from assistant message content, got %#v", item)
	}
}

func TestPortableResponsesReasoningBlockReadsNestedSummaryText(t *testing.T) {
	block := map[string]any{
		"type": "reasoning",
		"summary": []any{
			map[string]any{
				"type":         "summary_text",
				"summary_text": map[string]any{"text": "nested reasoning"},
			},
		},
	}

	portable := portableResponsesReasoningBlock(block, true)
	if got := stringValue(portable["text"]); got != "nested reasoning" {
		t.Fatalf("expected nested summary text to project to portable reasoning, got %#v", portable)
	}
}

func TestPortableResponsesReasoningBlockProjectsSummaryTextWithoutOpaqueState(t *testing.T) {
	portable := portableResponsesReasoningBlock(map[string]any{
		"type":    "reasoning",
		"summary": []any{map[string]any{"type": "summary_text", "text": "portable reasoning"}},
	}, true)
	if got := stringValue(portable["text"]); got != "portable reasoning" {
		t.Fatalf("expected summary text to remain portable, got %#v", portable)
	}
}

func TestPortableResponsesReasoningBlockRejectsOpaqueState(t *testing.T) {
	portable := portableResponsesReasoningBlock(map[string]any{
		"type":              "reasoning",
		"summary":           []any{map[string]any{"type": "summary_text", "text": "untrusted reasoning"}},
		"encrypted_content": "enc_should_not_leave_scope",
		"signature":         "sig_should_not_leave_scope",
	}, true)
	if portable != nil {
		t.Fatalf("opaque state must not project across protocols: %#v", portable)
	}
}

func TestStripCrossScopeNativeThinkingKeepsPortableResponsesSummary(t *testing.T) {
	messages := []model.CanonicalMessage{{
		Role:             "assistant",
		ReasoningContent: "原始摘要",
		ReasoningBlocks: []map[string]any{{
			"type":              "reasoning",
			"id":                "rs_real",
			"summary":           []any{map[string]any{"type": "summary_text", "text": "原始摘要"}},
			"encrypted_content": "opaque",
			"signature":         "sig",
		}},
	}}
	stripped := stripCrossScopeNativeAnthropicThinking(messages)
	if got := stripped[0].ReasoningContent; got != "原始摘要" {
		t.Fatalf("reasoning content=%q, want portable summary", got)
	}
	if len(stripped[0].ReasoningBlocks) != 0 {
		t.Fatalf("opaque reasoning block leaked across scope: %#v", stripped[0].ReasoningBlocks)
	}
}

func TestResponsesHistoryReasoningSummaryProjectsToChatReasoningContent(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_deepseek\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"deepseek/deepseek-v4-flash\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"chatcmpl_deepseek\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"deepseek/deepseek-v4-flash\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "deepseek",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                   "deepseek",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeChat,
			SupportsResponses:    true,
			SupportsChat:         true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"deepseek/deepseek-v4-flash",
		"stream":true,
		"include":["reasoning.encrypted_content"],
		"input":[
			{"role":"user","content":"查天气"},
			{"type":"reasoning","id":"rs_chat_reasoning","summary":[{"type":"summary_text","text":"先分析天气"}]},
			{"role":"assistant","content":"准备查询天气"},
			{"type":"function_call","call_id":"call_weather","name":"search_web","arguments":"{\"query\":\"桂林天气\"}"},
			{"type":"function_call_output","call_id":"call_weather","output":"{\"ok\":true}"}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected DeepSeek history replay to reach Chat upstream, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got, _ := upstreamBody["stream"].(bool); !got {
		t.Fatalf("expected stream=true to reach Chat upstream, got %#v", upstreamBody)
	}
	if !strings.Contains(rec.Body.String(), "event: response.completed") {
		t.Fatalf("expected streamed Responses completion, got %s", rec.Body.String())
	}
	messages, _ := upstreamBody["messages"].([]any)
	var assistantToolCall map[string]any
	for _, rawMessage := range messages {
		message, _ := rawMessage.(map[string]any)
		if len(message) == 0 {
			continue
		}
		if toolCalls, _ := message["tool_calls"].([]any); len(toolCalls) == 1 {
			assistantToolCall = message
			break
		}
	}
	if assistantToolCall == nil {
		t.Fatalf("expected assistant tool-call message in Chat upstream payload, got %#v", upstreamBody)
	}
	if got := stringValue(assistantToolCall["reasoning_content"]); got != "先分析天气" {
		t.Fatalf("expected matching assistant tool call to retain reasoning_content, got %#v", assistantToolCall)
	}
	encoded, err := json.Marshal(upstreamBody)
	if err != nil {
		t.Fatalf("marshal upstream payload: %v", err)
	}
	if strings.Contains(string(encoded), "encrypted_content") || strings.Contains(string(encoded), "signature") {
		t.Fatalf("optional encrypted reasoning include must not fabricate opaque state: %s", encoded)
	}
}

func TestPortableResponsesReasoningBlockPreservesPlaintextVariants(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		block map[string]any
		want  string
	}{
		{name: "summary string", block: map[string]any{"type": "reasoning", "summary": "summary string"}, want: "summary string"},
		{name: "summary map", block: map[string]any{"type": "reasoning", "summary": map[string]any{"summary_text": map[string]any{"text": "nested summary"}}}, want: "nested summary"},
		{name: "summary thinking", block: map[string]any{"type": "reasoning", "summary": map[string]any{"thinking": "nested thinking"}}, want: "nested thinking"},
		{name: "reasoning content", block: map[string]any{"type": "reasoning", "reasoning_content": "reasoning content"}, want: "reasoning content"},
		{name: "content", block: map[string]any{"type": "reasoning", "content": "content fallback"}, want: "content fallback"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			portable := portableResponsesReasoningBlock(testCase.block, true)
			if got := stringValue(portable["text"]); got != testCase.want {
				t.Fatalf("portable text=%q, want %q from %#v", got, testCase.want, testCase.block)
			}
		})
	}
}

func TestResponsesPortableHistorySummaryStringProjectsToChatReasoningContent(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_portable","object":"chat.completion","created":1,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "target", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "target", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true}}})
	responseID := "resp_portable_summary_string"
	portableScope := responsesHistoryPortableScope("anonymous")
	saveResponsesHistorySnapshotWithSession(server.history, "source", responseID,
		[]model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "earlier user"}}}},
		[]model.CanonicalMessage{{Role: "assistant", ReasoningContent: "来自 summary string", ReasoningBlocks: []map[string]any{{"type": "reasoning", "summary": "来自 summary string"}}, ToolCalls: []model.CanonicalToolCall{{ID: "call_weather", Type: "function", Name: "search_web", Arguments: `{"query":"桂林天气"}`}}}},
		"session-portable", "source-scope", portableScope)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","include":["reasoning.encrypted_content"],"previous_response_id":"`+responseID+`","input":[{"type":"function_call_output","call_id":"call_weather","output":"{\"ok\":true}"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	messages, _ := upstreamBody["messages"].([]any)
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if calls, _ := message["tool_calls"].([]any); len(calls) > 0 && stringValue(message["reasoning_content"]) == "来自 summary string" {
			encoded, _ := json.Marshal(message)
			if strings.Contains(string(encoded), "encrypted_content") || strings.Contains(string(encoded), "signature") || strings.Contains(string(encoded), "previous_response_id") {
				t.Fatalf("portable assistant leaked native state: %s", encoded)
			}
			return
		}
	}
	t.Fatalf("portable summary string did not reach matching tool call: %#v", upstreamBody)
}

func TestResponsesPortableHistoryProjectsReasoningPerUpstream(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		endpointType string
		assertBody   func(*testing.T, map[string]any)
	}{
		{
			name:         "chat retains all portable summary parts",
			endpointType: config.UpstreamEndpointTypeChat,
			assertBody: func(t *testing.T, body map[string]any) {
				t.Helper()
				messages, _ := body["messages"].([]any)
				for _, raw := range messages {
					message, _ := raw.(map[string]any)
					if stringValue(message["role"]) == "assistant" && stringValue(message["reasoning_content"]) == "第一段第二段" {
						return
					}
				}
				t.Fatalf("expected restored portable summary in Chat reasoning_content, got %#v", body)
			},
		},
		{
			name:         "anthropic drops summary-only portable replay",
			endpointType: config.UpstreamEndpointTypeAnthropic,
			assertBody: func(t *testing.T, body map[string]any) {
				t.Helper()
				encoded, err := json.Marshal(body)
				if err != nil {
					t.Fatalf("marshal upstream body: %v", err)
				}
				if strings.Contains(string(encoded), "第一段") || strings.Contains(string(encoded), "第二段") {
					t.Fatalf("summary-only portable replay reached Anthropic upstream: %s", encoded)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var upstreamBody map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer r.Body.Close()
				if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
					t.Fatalf("decode upstream request: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				if testCase.endpointType == config.UpstreamEndpointTypeAnthropic {
					_, _ = w.Write([]byte(`{"id":"msg_portable","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":"chatcmpl_portable","object":"chat.completion","created":1,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
			}))
			defer upstream.Close()

			providerID := "target"
			server := NewServer(config.Config{
				DefaultProvider:             providerID,
				EnableLegacyV1Routes:        true,
				DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
				Providers: []config.ProviderConfig{{
					ID:                        providerID,
					Enabled:                   true,
					UpstreamBaseURL:           upstream.URL,
					UpstreamAPIKey:            "test-key",
					UpstreamEndpointType:      testCase.endpointType,
					SupportsResponses:         true,
					SupportsChat:              testCase.endpointType == config.UpstreamEndpointTypeChat,
					SupportsAnthropicMessages: testCase.endpointType == config.UpstreamEndpointTypeAnthropic,
				}},
			})
			responseID := "resp_portable_reasoning"
			portableScope := responsesHistoryPortableScope("anonymous")
			saveResponsesHistorySnapshotWithSession(server.history, "source", responseID,
				[]model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "earlier user"}}}},
				[]model.CanonicalMessage{{Role: "assistant", ReasoningBlocks: []map[string]any{{
					"type": "reasoning",
					"summary": []any{
						map[string]any{"type": "summary_text", "text": "第一段"},
						map[string]any{"type": "summary_text", "text": "第二段"},
					},
				}}}}, "session-portable", "source-scope", portableScope)

			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","include":["reasoning.encrypted_content"],"previous_response_id":"`+responseID+`","input":"continue"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected portable continuation to reach upstream, got %d body=%s", rec.Code, rec.Body.String())
			}
			testCase.assertBody(t, upstreamBody)
			encoded, err := json.Marshal(upstreamBody)
			if err != nil {
				t.Fatalf("marshal upstream body: %v", err)
			}
			if strings.Contains(string(encoded), "encrypted_content") || strings.Contains(string(encoded), "signature") {
				t.Fatalf("portable history leaked opaque state: %s", encoded)
			}
		})
	}
}

func TestResponsesStreamMovesTopLevelServiceTierIntoCompletedResponseObject(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\n" +
			"data: {\"service_tier\":\"default\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(testResponsesConfig(upstream.URL))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"input":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `"response":{"id":"resp_`) && !strings.Contains(body, `"response":{"id":"resp_`) {
		t.Fatalf("unexpected body structure: %s", body)
	}
	if !strings.Contains(body, `"response":{"id":"resp_`) {
		t.Fatalf("expected completed event to include response object, got %s", body)
	}
	if !strings.Contains(body, `"service_tier":"default"`) {
		t.Fatalf("expected completed event to preserve service_tier, got %s", body)
	}
	if strings.Contains(body, `"response":{"id":"resp_`) && strings.Contains(body, `"response":{"id":"resp_`) {
		// no-op guard to keep test body readable after string checks
	}
	if strings.Contains(body, `"response":{"id":"resp_`) && strings.Contains(body, `"response":{"id":"resp_`) && strings.Contains(body, `"response":{"id":"resp_`) {
		// no-op
	}
	if strings.Contains(body, `"response":{"id":"resp_req-`) && strings.Contains(body, `"response":{"id":"resp_req-`) {
		// no-op
	}
	if strings.Contains(body, `"type":"response.completed"`) && strings.Contains(body, `"response":{"id":"resp_req-`) && strings.Contains(body, `"service_tier":"default","type":"response.completed"`) {
		t.Fatalf("expected service_tier to live under response object instead of top-level completed payload, got %s", body)
	}
}
