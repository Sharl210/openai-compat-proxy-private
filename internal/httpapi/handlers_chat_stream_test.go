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

func chatChoiceFromChunk(t *testing.T, chunk map[string]any, body string) map[string]any {
	t.Helper()
	choices, ok := chunk["choices"].([]any)
	if !ok || len(choices) != 1 {
		t.Fatalf("expected one chat choice in chunk %#v from %s", chunk, body)
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		t.Fatalf("expected chat choice object in chunk %#v from %s", chunk, body)
	}
	return choice
}

func chatDeltaFromChunk(t *testing.T, chunk map[string]any, body string) map[string]any {
	t.Helper()
	choice := chatChoiceFromChunk(t, chunk, body)
	delta, ok := choice["delta"].(map[string]any)
	if !ok {
		t.Fatalf("expected chat delta object in chunk %#v from %s", chunk, body)
	}
	return delta
}

func onlyToolCallFromDelta(t *testing.T, delta map[string]any, body string) map[string]any {
	t.Helper()
	calls, ok := delta["tool_calls"].([]any)
	if !ok || len(calls) != 1 {
		t.Fatalf("expected one tool call in delta %#v from %s", delta, body)
	}
	call, ok := calls[0].(map[string]any)
	if !ok {
		t.Fatalf("expected tool call object in delta %#v from %s", delta, body)
	}
	return call
}

func assertAttemptCompletionOfficialToolCallChunks(t *testing.T, body, fullArgs string) {
	t.Helper()
	chunks := collectChatStreamChunks(t, body)
	if len(chunks) != 4 {
		t.Fatalf("expected attempt_completion stream chunks in order role -> metadata -> arguments -> finish, got %d chunks: %s", len(chunks), body)
	}

	roleDelta := chatDeltaFromChunk(t, chunks[0], body)
	if got, _ := roleDelta["role"].(string); got != "assistant" {
		t.Fatalf("expected first chunk to set assistant role, got %#v from %s", roleDelta, body)
	}
	if _, ok := roleDelta["tool_calls"]; ok {
		t.Fatalf("expected role chunk not to include tool_calls, got %#v from %s", roleDelta, body)
	}

	metadataCall := onlyToolCallFromDelta(t, chatDeltaFromChunk(t, chunks[1], body), body)
	if got := metadataCall["index"]; got != float64(0) {
		t.Fatalf("expected metadata chunk tool index 0, got %#v from %s", got, body)
	}
	if got, _ := metadataCall["id"].(string); got != "call_1" {
		t.Fatalf("expected metadata chunk tool id call_1, got %#v from %s", metadataCall, body)
	}
	if got, _ := metadataCall["type"].(string); got != "function" {
		t.Fatalf("expected metadata chunk tool type function, got %#v from %s", metadataCall, body)
	}
	metadataFunction, _ := metadataCall["function"].(map[string]any)
	if got, _ := metadataFunction["name"].(string); got != "attempt_completion" {
		t.Fatalf("expected metadata chunk function name attempt_completion, got %#v from %s", metadataFunction, body)
	}
	if _, ok := metadataFunction["arguments"]; ok {
		t.Fatalf("expected metadata chunk not to include function.arguments, got %#v from %s", metadataFunction, body)
	}

	argumentsCall := onlyToolCallFromDelta(t, chatDeltaFromChunk(t, chunks[2], body), body)
	if len(argumentsCall) != 2 {
		t.Fatalf("expected arguments chunk to include only index and function, got %#v from %s", argumentsCall, body)
	}
	if got := argumentsCall["index"]; got != float64(0) {
		t.Fatalf("expected arguments chunk tool index 0, got %#v from %s", got, body)
	}
	if _, ok := argumentsCall["id"]; ok {
		t.Fatalf("expected arguments chunk not to repeat id, got %#v from %s", argumentsCall, body)
	}
	if _, ok := argumentsCall["type"]; ok {
		t.Fatalf("expected arguments chunk not to repeat type, got %#v from %s", argumentsCall, body)
	}
	argumentsFunction, _ := argumentsCall["function"].(map[string]any)
	if len(argumentsFunction) != 1 {
		t.Fatalf("expected arguments chunk function to include only arguments, got %#v from %s", argumentsFunction, body)
	}
	if got, _ := argumentsFunction["arguments"].(string); got != fullArgs {
		t.Fatalf("expected one full attempt_completion arguments chunk %q, got %q from %s", fullArgs, got, body)
	}

	finishChoice := chatChoiceFromChunk(t, chunks[3], body)
	if got, _ := finishChoice["finish_reason"].(string); got != "tool_calls" {
		t.Fatalf("expected final chunk finish_reason tool_calls, got %#v from %s", finishChoice, body)
	}
	finishDelta, _ := finishChoice["delta"].(map[string]any)
	if len(finishDelta) != 0 {
		t.Fatalf("expected final chunk to have empty delta, got %#v from %s", finishDelta, body)
	}
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
	if strings.Contains(body, "代理层占位") || strings.Contains(body, "**推理中**") {
		t.Fatalf("expected chat stream not to expose proxy placeholder reasoning text, got %s", body)
	}
	if strings.Contains(body, `"reasoning_content":"`+invisibleSyntheticReasoningDelta+`"`) {
		t.Fatalf("expected chat stream not to emit invisible placeholder reasoning delta, got %s", body)
	}
}

func TestChatStreamFromChatJSONUpstreamInjectsPlaceholderAndText(t *testing.T) {
	chatResponse := `{
		"id": "chatcmpl_json",
		"object": "chat.completion",
		"choices": [{
			"index": 0,
			"message": {"role": "assistant", "content": "final answer"},
			"finish_reason": "stop"
		}],
		"usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
	}`
	upstream := newChatFormatUpstream(t, chatResponse)
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
			SupportsChat:         true,
			SupportsResponses:    true,
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
	if strings.Contains(body, "代理层占位") || strings.Contains(body, "**推理中**") {
		t.Fatalf("expected chat stream not to expose proxy placeholder reasoning text, got %s", body)
	}
	if !strings.Contains(body, `"content":"final answer"`) {
		t.Fatalf("expected final answer content, got %s", body)
	}
	if !strings.Contains(body, `data: [DONE]`) {
		t.Fatalf("expected chat stream done marker, got %s", body)
	}
}

func TestChatStreamRemovesOnlyTerminalTextLineEndings(t *testing.T) {
	rec := httptest.NewRecorder()
	state := &chatStreamState{
		chunkID:         "chatcmpl_tail",
		toolIDAliases:   map[string]string{},
		toolMeta:        map[string]map[string]string{},
		toolIndex:       map[string]int{},
		toolSent:        map[string]bool{},
		pendingToolArgs: map[string]string{},
	}

	for _, event := range []upstream.Event{
		{Event: "response.output_text.delta", Data: map[string]any{"delta": "first\r\nsecond \t\r"}},
		{Event: "response.output_text.delta", Data: map[string]any{"delta": "\nthird\r"}},
		{Event: "response.output_text.delta", Data: map[string]any{"delta": "\n\n"}},
		{Event: "response.completed", Data: map[string]any{}},
	} {
		if err := writeChatEvent(rec, nil, state, event, false, nil); err != nil {
			t.Fatalf("writeChatEvent(%s): %v", event.Event, err)
		}
	}

	var content strings.Builder
	for _, chunk := range collectChatStreamChunks(t, rec.Body.String()) {
		delta := chatDeltaFromChunk(t, chunk, rec.Body.String())
		if text, _ := delta["content"].(string); text != "" {
			content.WriteString(text)
		}
	}
	if got := content.String(); got != "first\r\nsecond \t\r\nthird" {
		t.Fatalf("expected internal CRLF and terminal whitespace preserved while terminal line endings are omitted, got %q; body=%s", got, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "data: [DONE]") {
		t.Fatalf("expected terminal done marker, got %s", rec.Body.String())
	}
}

func TestChatStreamDropsPendingTextLineEndingsOnFailure(t *testing.T) {
	rec := httptest.NewRecorder()
	state := &chatStreamState{
		chunkID:         "chatcmpl_tail_failure",
		toolIDAliases:   map[string]string{},
		toolMeta:        map[string]map[string]string{},
		toolIndex:       map[string]int{},
		toolSent:        map[string]bool{},
		pendingToolArgs: map[string]string{},
	}

	for _, event := range []upstream.Event{
		{Event: "response.output_text.delta", Data: map[string]any{"delta": "answer\r\n"}},
		{Event: "response.failed", Data: map[string]any{"health_flag": "upstream_error", "message": "failed"}},
	} {
		if err := writeChatEvent(rec, nil, state, event, false, nil); err != nil {
			t.Fatalf("writeChatEvent(%s): %v", event.Event, err)
		}
	}

	var content strings.Builder
	for _, chunk := range collectChatStreamChunks(t, rec.Body.String()) {
		delta := chatDeltaFromChunk(t, chunk, rec.Body.String())
		if text, _ := delta["content"].(string); text != "" {
			content.WriteString(text)
		}
	}
	if got := content.String(); got != "answer" {
		t.Fatalf("expected pending terminal line endings discarded before failure, got %q; body=%s", got, rec.Body.String())
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

func TestChatStreamRecognizesNativeWebSearchCallItems(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"ws_1\",\"type\":\"web_search_call\",\"call_id\":\"call_ws_1\",\"name\":\"web_search\"}}\n\n",
		"event: response.function_call_arguments.delta\n" +
			"data: {\"item_id\":\"ws_1\",\"delta\":\"{\\\"query\\\":\\\"Shanghai\\\"}\"}\n\n",
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
	if !strings.Contains(body, `"tool_calls":[{"function":{"arguments":"{\"query\":\"Shanghai\"}","name":"web_search"},"id":"call_ws_1","index":0,"type":"function"}]`) {
		t.Fatalf("expected native web_search_call to map into chat tool_calls, got %s", body)
	}
	if !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Fatalf("expected native web_search_call turn to end as tool_calls, got %s", body)
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

func TestChatStreamUnexpectedEOFFlushesPendingToolArgumentsBeforeError(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"get_weather\"}}\n\n",
		"event: response.function_call_arguments.delta\n" +
			"data: {\"item_id\":\"fc_1\",\"delta\":\"{\\\"city\\\":\\\"Shanghai\\\"}\"}\n\n",
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
		t.Fatalf("expected pending tool arguments flushed before EOF error, got %s", body)
	}
	if !strings.Contains(body, `"delta":{"error":{"health_flag":"upstreamStreamBroken","message":"unexpected EOF"}}`) {
		t.Fatalf("expected EOF to remain a protocol error after flushing tool arguments, got %s", body)
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

func TestChatStreamKeepsLaterToolArgumentDeltaAfterReasoningAndText(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"get_weather\"}}\n\n",
		"event: response.function_call_arguments.delta\n" +
			"data: {\"item_id\":\"fc_1\",\"delta\":\"{\\\"city\\\":\\\"Shanghai\\\"}\"}\n\n",
		"event: response.reasoning.delta\n" +
			"data: {\"summary\":\"alpha\"}\n\n",
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"working\"}\n\n",
		"event: response.function_call_arguments.delta\n" +
			"data: {\"item_id\":\"fc_1\",\"delta\":\" \"}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n",
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
	firstToolIdx := strings.Index(body, `"tool_calls":[{"function":{"arguments":"{\"city\":\"Shanghai\"}","name":"get_weather"},"id":"call_1","index":0,"type":"function"}]`)
	reasoningIdx := strings.Index(body, `"reasoning_content":"alpha"`)
	textIdx := strings.Index(body, `"content":"working"`)
	laterArgsIdx := strings.Index(body, `"tool_calls":[{"function":{"arguments":" "},"index":0}]`)
	if firstToolIdx == -1 || reasoningIdx == -1 || textIdx == -1 || laterArgsIdx == -1 {
		t.Fatalf("expected initial tool chunk, reasoning chunk, text chunk, and later tool arguments delta, got %s", body)
	}
	if !(firstToolIdx < reasoningIdx && reasoningIdx < textIdx && textIdx < laterArgsIdx) {
		t.Fatalf("expected later tool arguments delta to stay after interleaved reasoning/text, got %s", body)
	}
}

func TestChatStreamKeepsReasoningAndFinalTextAfterTwoToolCalls(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"search_web\"}}\n\n",
		"event: response.function_call_arguments.delta\n" +
			"data: {\"item_id\":\"fc_1\",\"delta\":\"{\\\"q\\\":\\\"alpha\\\"}\"}\n\n",
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"search_web\",\"arguments\":\"{\\\"q\\\":\\\"alpha\\\"}\"}}\n\n",
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"fc_2\",\"type\":\"function_call\",\"call_id\":\"call_2\",\"name\":\"open_url\"}}\n\n",
		"event: response.function_call_arguments.delta\n" +
			"data: {\"item_id\":\"fc_2\",\"delta\":\"{\\\"url\\\":\\\"https://example.com\\\"}\"}\n\n",
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"fc_2\",\"type\":\"function_call\",\"call_id\":\"call_2\",\"name\":\"open_url\",\"arguments\":\"{\\\"url\\\":\\\"https://example.com\\\"}\"}}\n\n",
		"event: response.reasoning.delta\n" +
			"data: {\"summary\":\"alpha beta\"}\n\n",
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
	tool1Idx := strings.Index(body, `"id":"call_1"`)
	tool2Idx := strings.Index(body, `"id":"call_2"`)
	reasoningIdx := strings.Index(body, `"reasoning_content":"alpha beta"`)
	textIdx := strings.Index(body, `"content":"最终答案"`)
	if tool1Idx == -1 || tool2Idx == -1 || reasoningIdx == -1 || textIdx == -1 {
		t.Fatalf("expected two tool calls followed by reasoning and final text, got %s", body)
	}
	if !(tool1Idx < tool2Idx && tool2Idx < reasoningIdx && reasoningIdx < textIdx) {
		t.Fatalf("expected reasoning and final text to continue after two tool calls, got %s", body)
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

func TestChatEventWriterKeepsLaterToolArgumentDeltaAfterReasoningAndTextInCompatMode(t *testing.T) {
	rec := httptest.NewRecorder()
	state := &chatStreamState{toolMeta: map[string]map[string]string{}, toolIndex: map[string]int{}, toolSent: map[string]bool{}, pendingToolArgs: map[string]string{}}
	helper := &responseEventWriterHelper{downstreamType: "chat", upstreamEndpointType: config.UpstreamEndpointTypeAnthropic, toolIDAliases: map[string]string{}, toolItems: map[string]*responsesToolItemState{}}
	writer := NewChatEventWriter(rec, nil, state, helper, nil)

	for _, evt := range []upstream.Event{
		{Event: "response.output_item.added", Data: map[string]any{"item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "get_weather"}}},
		{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `{"city":"Shanghai"}`}},
		{Event: "response.reasoning.delta", Data: map[string]any{"summary": "alpha"}},
		{Event: "response.output_text.delta", Data: map[string]any{"delta": "working"}},
		{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": " "}},
		{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 2, "total_tokens": 3}}}},
	} {
		if err := writer.WriteEvent(evt.Event, evt.Data); err != nil {
			t.Fatalf("writer.WriteEvent error: %v", err)
		}
	}
	body := rec.Body.String()
	firstToolIdx := strings.Index(body, `"tool_calls":[{"function":{"arguments":"{\"city\":\"Shanghai\"}","name":"get_weather"},"id":"call_1","index":0,"type":"function"}]`)
	reasoningIdx := strings.Index(body, `"reasoning_content":"alpha"`)
	textIdx := strings.Index(body, `"content":"working"`)
	laterArgsIdx := strings.Index(body, `"tool_calls":[{"function":{"arguments":" "},"index":0}]`)
	if firstToolIdx == -1 || reasoningIdx == -1 || textIdx == -1 || laterArgsIdx == -1 {
		t.Fatalf("expected initial tool chunk, reasoning chunk, text chunk, and later tool arguments delta, got %s", body)
	}
	if !(firstToolIdx < reasoningIdx && reasoningIdx < textIdx && textIdx < laterArgsIdx) {
		t.Fatalf("expected later tool arguments delta to stay after interleaved reasoning/text in compat mode, got %s", body)
	}
}

func TestChatEventWriterFormatsReasoningTitleAcrossChunks(t *testing.T) {
	rec := httptest.NewRecorder()
	state := &chatStreamState{toolMeta: map[string]map[string]string{}, toolIndex: map[string]int{}, toolSent: map[string]bool{}, pendingToolArgs: map[string]string{}}
	helper := &responseEventWriterHelper{downstreamType: "chat", upstreamEndpointType: config.UpstreamEndpointTypeResponses, toolIDAliases: map[string]string{}, toolItems: map[string]*responsesToolItemState{}}
	writer := NewChatEventWriter(rec, nil, state, helper, nil)

	for _, evt := range []upstream.Event{
		{Event: "response.reasoning.delta", Data: map[string]any{"reasoning_content": "**标题**"}},
		{Event: "response.reasoning.delta", Data: map[string]any{"reasoning_content": "正文"}},
		{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}},
	} {
		if err := writer.WriteEvent(evt.Event, evt.Data); err != nil {
			t.Fatalf("writer.WriteEvent error: %v", err)
		}
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"reasoning_content":"**标题**"`) || !strings.Contains(body, `"reasoning_content":"\n正文"`) {
		t.Fatalf("expected reasoning title formatting across chat chunks, got %s", body)
	}
}

func TestChatEventWriterBuffersAttemptCompletionArgumentsUntilDone(t *testing.T) {
	rec := httptest.NewRecorder()
	state := &chatStreamState{toolMeta: map[string]map[string]string{}, toolIndex: map[string]int{}, toolSent: map[string]bool{}, pendingToolArgs: map[string]string{}}
	helper := &responseEventWriterHelper{downstreamType: "chat", upstreamEndpointType: config.UpstreamEndpointTypeResponses, toolIDAliases: map[string]string{}, toolItems: map[string]*responsesToolItemState{}}
	writer := NewChatEventWriter(rec, nil, state, helper, nil)
	fullArgs := `{"command":"","result":"第一段\n第二段","task_progress":"- [x] done"}`

	for _, evt := range []upstream.Event{
		{Event: "response.output_item.added", Data: map[string]any{"item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "attempt_completion"}}},
		{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `{"command":""`}},
		{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `,"result":"第一段\n`}},
		{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `第二段","task_progress":"- [x] done"}`}},
		{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "attempt_completion", "arguments": fullArgs}}},
		{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 2, "total_tokens": 3}}}},
	} {
		if err := writer.WriteEvent(evt.Event, evt.Data); err != nil {
			t.Fatalf("writer.WriteEvent error: %v", err)
		}
	}
	body := rec.Body.String()
	assertAttemptCompletionOfficialToolCallChunks(t, body, fullArgs)
}

func TestChatEventWriterFlushesBufferedAttemptCompletionArgumentsWithoutDoneArgs(t *testing.T) {
	rec := httptest.NewRecorder()
	state := &chatStreamState{toolMeta: map[string]map[string]string{}, toolIndex: map[string]int{}, toolSent: map[string]bool{}, pendingToolArgs: map[string]string{}}
	helper := &responseEventWriterHelper{downstreamType: "chat", upstreamEndpointType: config.UpstreamEndpointTypeResponses, toolIDAliases: map[string]string{}, toolItems: map[string]*responsesToolItemState{}}
	writer := NewChatEventWriter(rec, nil, state, helper, nil)
	fullArgs := `{"command":"","result":"只靠 delta 累积","task_progress":"- [x] done"}`

	for _, evt := range []upstream.Event{
		{Event: "response.output_item.added", Data: map[string]any{"item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "attempt_completion"}}},
		{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `{"command":""`}},
		{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `,"result":"只靠 delta 累积"`}},
		{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `,"task_progress":"- [x] done"}`}},
		{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "attempt_completion"}}},
		{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 2, "total_tokens": 3}}}},
	} {
		if err := writer.WriteEvent(evt.Event, evt.Data); err != nil {
			t.Fatalf("writer.WriteEvent error: %v", err)
		}
	}
	body := rec.Body.String()
	assertAttemptCompletionOfficialToolCallChunks(t, body, fullArgs)
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
