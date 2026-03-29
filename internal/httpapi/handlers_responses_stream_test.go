package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/testutil"
	"openai-compat-proxy/internal/upstream"
)

func TestResponsesStreamIncludesTypedChunks(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
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

	body := rec.Body.String()
	if !strings.Contains(body, "event: response.output_item.added") {
		t.Fatalf("expected synthetic reasoning output item start in stream body, got %s", body)
	}
	if !strings.Contains(body, `"id":"rs_proxy"`) || !strings.Contains(body, `"type":"reasoning"`) {
		t.Fatalf("expected synthetic reasoning item payload in stream body, got %s", body)
	}
	if !strings.Contains(body, `"type":"response.reasoning.delta"`) {
		t.Fatalf("expected synthetic reasoning chunk type in stream body, got %s", body)
	}
	if !strings.Contains(body, `"type":"response.reasoning_summary_text.delta"`) {
		t.Fatalf("expected synthetic reasoning summary chunk type in stream body, got %s", body)
	}
	if !strings.Contains(body, `"type":"response.output_text.delta"`) {
		t.Fatalf("expected output_text chunk type in stream body, got %s", body)
	}
}

func TestResponsesStreamPreservesRealReasoningItemLifecycle(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"summary\":[]}}\n\n",
		"event: response.reasoning_summary_text.delta\n" +
			"data: {\"item_id\":\"rs_1\",\"summary_index\":0,\"delta\":\"alpha\"}\n\n",
		"event: response.reasoning_summary_text.done\n" +
			"data: {\"item_id\":\"rs_1\",\"summary_index\":0,\"text\":\"alpha\"}\n\n",
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"alpha\"}]}}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
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
	body := rec.Body.String()

	proxyAddedIdx := strings.Index(body, `{"item":{"id":"rs_proxy"`)
	proxyDoneIdx := strings.Index(body, `event: response.output_item.done`+"\n"+`data: {"item":{"id":"rs_proxy"`)
	realAddedIdx := strings.Index(body, `{"item":{"id":"rs_1","summary":[],"type":"reasoning"},"type":"response.output_item.added"}`)
	realDeltaIdx := strings.Index(body, `{"delta":"alpha","item_id":"rs_1","summary_index":0,"type":"response.reasoning_summary_text.delta"}`)
	realDoneIdx := strings.LastIndex(body, `{"item":{"id":"rs_1","summary":[{"text":"alpha","type":"summary_text"}],"type":"reasoning"},"type":"response.output_item.done"}`)

	if proxyAddedIdx == -1 || proxyDoneIdx == -1 {
		t.Fatalf("expected proxy fallback reasoning item to open and close, got %s", body)
	}
	if realAddedIdx == -1 || realDeltaIdx == -1 || realDoneIdx == -1 {
		t.Fatalf("expected real reasoning item lifecycle and summary delta to be forwarded, got %s", body)
	}
	if !(proxyAddedIdx < proxyDoneIdx && proxyDoneIdx < realAddedIdx && realAddedIdx < realDeltaIdx && realDeltaIdx < realDoneIdx) {
		t.Fatalf("expected fallback reasoning to finish before real reasoning lifecycle begins, got %s", body)
	}
	if strings.Contains(body, `{"item":{"id":"rs_proxy","summary":[{"text":"alpha"`) {
		t.Fatalf("expected real reasoning content to stay on real item instead of being merged into rs_proxy, got %s", body)
	}
}

func TestResponsesStreamPreservesNativeFunctionCallArgumentDeltasForResponsesUpstream(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"get_weather\"}}\n\n",
		"event: response.function_call_arguments.delta\n" +
			"data: {\"item_id\":\"fc_1\",\"delta\":\"{\\\"city\\\":\\\"Shanghai\\\"}\"}\n\n",
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\":\\\"Shanghai\\\"}\"}}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(testResponsesConfigWithEndpoint(upstream.URL, config.UpstreamEndpointTypeResponses))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"input":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"response.function_call_arguments.delta"`) {
		t.Fatalf("expected responses upstream to preserve native arguments delta events, got %s", body)
	}
	if !strings.Contains(body, `{"item":{"call_id":"call_1","id":"fc_1","name":"get_weather","type":"function_call"},"type":"response.output_item.added"}`) {
		t.Fatalf("expected responses upstream to preserve early added event, got %s", body)
	}
}

func TestResponsesStreamEmitsFunctionCallArgumentsDoneForResponsesUpstream(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.created\n" +
			"data: {\"response\":{\"id\":\"resp_1\"}}\n\n",
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"search_web\",\"arguments\":\"\"}}\n\n",
		"event: response.function_call_arguments.delta\n" +
			"data: {\"item_id\":\"fc_1\",\"delta\":\"{\\\"query\\\":\\\"weather\\\"}\"}\n\n",
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"search_web\",\"arguments\":\"{\\\"query\\\":\\\"weather\\\"}\"}}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"id\":\"resp_1\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(testResponsesConfigWithEndpoint(upstream.URL, config.UpstreamEndpointTypeResponses))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"input":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"response.function_call_arguments.done"`) {
		t.Fatalf("expected responses upstream stream to emit function_call_arguments.done for RikkaHub, got %s", body)
	}
	if !strings.Contains(body, `"arguments":"{\"query\":\"weather\"}"`) {
		t.Fatalf("expected done event to carry full arguments, got %s", body)
	}
}

func TestResponsesStreamEmitsFunctionCallLifecycleWithCompleteArgumentsForClients(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeAnthropic,
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "get_weather"}}},
		upstream.Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `{"city":"Shanghai"}`}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}},
	)

	addedIdx := strings.Index(body, `{"item":{"arguments":"{\"city\":\"Shanghai\"}","call_id":"call_1","id":"fc_1","name":"get_weather","type":"function_call"},"type":"response.output_item.added"}`)
	doneIdx := strings.Index(body, `{"item":{"arguments":"{\"city\":\"Shanghai\"}","call_id":"call_1","id":"fc_1","name":"get_weather","type":"function_call"},"type":"response.output_item.done"}`)
	completedIdx := strings.LastIndex(body, `event: response.completed`)

	if addedIdx == -1 || doneIdx == -1 || completedIdx == -1 {
		t.Fatalf("expected function call added/done/completed lifecycle, got %s", body)
	}
	if strings.Contains(body, `"type":"response.function_call_arguments.delta"`) {
		t.Fatalf("expected compatibility mode to suppress raw arguments delta events, got %s", body)
	}
	if !strings.Contains(body, `{"arguments":"{\"city\":\"Shanghai\"}","item_id":"call_1","type":"response.function_call_arguments.done"}`) {
		t.Fatalf("expected function_call_arguments.done with stable call_id, got %s", body)
	}
	if !(addedIdx < doneIdx && doneIdx < completedIdx) {
		t.Fatalf("expected function call lifecycle added -> done -> completed, got %s", body)
	}
}

func TestResponsesStreamAccumulatesMultipleFunctionCallArgumentDeltasBeforeDone(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeAnthropic,
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "search_web"}}},
		upstream.Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `{"query":"Quectel"`}},
		upstream.Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `,"topic":"finance"}`}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}},
	)
	if !strings.Contains(body, `{"item":{"arguments":"{\"query\":\"Quectel\",\"topic\":\"finance\"}","call_id":"call_1","id":"fc_1","name":"search_web","type":"function_call"},"type":"response.output_item.done"}`) {
		t.Fatalf("expected final function call item to contain merged arguments, got %s", body)
	}
	if !strings.Contains(body, `{"arguments":"{\"query\":\"Quectel\",\"topic\":\"finance\"}","item_id":"call_1","type":"response.function_call_arguments.done"}`) {
		t.Fatalf("expected merged arguments done event, got %s", body)
	}
}

func TestResponsesStreamOmitsPartialFunctionCallArgumentDeltasForCompatibility(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeAnthropic,
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "search_web"}}},
		upstream.Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `{"query":"Quectel"`}},
		upstream.Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `,"topic":"finance"}`}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}},
	)
	if strings.Contains(body, `"type":"response.function_call_arguments.delta"`) {
		t.Fatalf("expected compatibility mode to suppress partial function_call_arguments.delta events, got %s", body)
	}
	if !strings.Contains(body, `{"item":{"arguments":"{\"query\":\"Quectel\",\"topic\":\"finance\"}","call_id":"call_1","id":"fc_1","name":"search_web","type":"function_call"},"type":"response.output_item.added"}`) {
		t.Fatalf("expected compatibility mode to delay added until full arguments are available, got %s", body)
	}
	if !strings.Contains(body, `{"item":{"arguments":"{\"query\":\"Quectel\",\"topic\":\"finance\"}","call_id":"call_1","id":"fc_1","name":"search_web","type":"function_call"},"type":"response.output_item.done"}`) {
		t.Fatalf("expected final function_call item to retain full merged arguments, got %s", body)
	}
	if !strings.Contains(body, `{"arguments":"{\"query\":\"Quectel\",\"topic\":\"finance\"}","item_id":"call_1","type":"response.function_call_arguments.done"}`) {
		t.Fatalf("expected done event with stable call_id, got %s", body)
	}
}

func TestResponsesStreamCompatibilityKeepsOriginalItemIDWhilePreservingCallID(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeAnthropic,
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "search_web"}}},
		upstream.Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `{"query":"Quectel"}`}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}},
	)

	if !strings.Contains(body, `{"item":{"arguments":"{\"query\":\"Quectel\"}","call_id":"call_1","id":"fc_1","name":"search_web","type":"function_call"},"type":"response.output_item.done"}`) {
		t.Fatalf("expected compatibility mode to preserve original function_call item id while retaining call_id, got %s", body)
	}
	if !strings.Contains(body, `{"arguments":"{\"query\":\"Quectel\"}","item_id":"call_1","type":"response.function_call_arguments.done"}`) {
		t.Fatalf("expected arguments.done to keep stable call_id for follow-up, got %s", body)
	}
}

func TestResponsesStreamCompletedCarriesStableResponseIDForToolFollowUp(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeAnthropic,
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "search_web"}}},
		upstream.Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `{"query":"Quectel"}`}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}, "stop_reason": "tool_use"}},
	)
	if !strings.Contains(body, `event: response.completed`) {
		t.Fatalf("expected completed event, got %s", body)
	}
	if !strings.Contains(body, `"id":"resp_call_1"`) {
		t.Fatalf("expected completed response payload to include stable response id resp_call_1, got %s", body)
	}
	if !strings.Contains(body, `"object":"response"`) {
		t.Fatalf("expected completed response payload to identify response object, got %s", body)
	}
}

func TestResponsesStreamCompletedMirrorsTopLevelUsageIntoResponseUsageForCompatibility(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeAnthropic,
		upstream.Event{Event: "response.completed", Data: map[string]any{
			"usage": map[string]any{"input_tokens": 3, "output_tokens": 5, "total_tokens": 8},
		}},
	)

	if !strings.Contains(body, `"usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}`) {
		t.Fatalf("expected top-level usage to stay in completed event, got %s", body)
	}
	if !strings.Contains(body, `"response":{"id":"resp_proxy","object":"response","usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}`) {
		t.Fatalf("expected completed event to also include response.usage for compatibility, got %s", body)
	}
}

func TestResponsesStreamDoneMirrorsTopLevelUsageIntoResponseUsageForCompatibility(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeChat,
		upstream.Event{Event: "response.done", Data: map[string]any{
			"usage": map[string]any{"input_tokens": 2, "output_tokens": 4, "total_tokens": 6},
		}},
	)

	if !strings.Contains(body, `event: response.done`) {
		t.Fatalf("expected response.done event, got %s", body)
	}
	if !strings.Contains(body, `"response":{"id":"resp_proxy","object":"response","usage":{"input_tokens":2,"output_tokens":4,"total_tokens":6}}`) {
		t.Fatalf("expected done event to also include response.usage for compatibility, got %s", body)
	}
}

func TestResponsesStreamTerminalFailureAfterSSEStartStaysInSSEProtocol(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n",
		"event: response.incomplete\n" +
			"data: {\"health_flag\":\"upstream_error\",\"message\":\"boom\"}\n\n",
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
	body := rec.Body.String()
	if !strings.Contains(body, "event: response.incomplete") {
		t.Fatalf("expected response.incomplete terminal SSE event, got %s", body)
	}
	if strings.Count(body, "event: response.incomplete") != 1 {
		t.Fatalf("expected exactly one response.incomplete terminal SSE event, got %s", body)
	}
	if !strings.Contains(body, `"health_flag":"upstream_error","message":"boom"`) {
		t.Fatalf("expected terminal failure payload in SSE body, got %s", body)
	}
	if strings.Contains(body, `"code":"upstream_error"`) || strings.Contains(body, `"type":"proxy_error"`) {
		t.Fatalf("expected no JSON error body after SSE start, got %s", body)
	}
}

func testResponsesConfig(upstreamURL string) config.Config {
	return testResponsesConfigWithEndpoint(upstreamURL, config.UpstreamEndpointTypeResponses)
}

func renderResponsesWriterEvents(t *testing.T, endpointType string, events ...upstream.Event) string {
	t.Helper()
	rec := httptest.NewRecorder()
	state := &responsesStreamState{toolItems: map[string]*responsesToolItemState{}, toolIDAliases: map[string]string{}, upstreamEndpointType: endpointType}
	for _, evt := range events {
		if err := writeResponsesEvent(rec, nil, state, evt, nil); err != nil {
			t.Fatalf("writeResponsesEvent error: %v", err)
		}
	}
	return rec.Body.String()
}

func testResponsesConfigWithEndpoint(upstreamURL string, endpointType string) config.Config {
	return config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			UpstreamBaseURL:      upstreamURL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: endpointType,
			SupportsModels:       true,
			SupportsResponses:    true,
		}},
	}
}
