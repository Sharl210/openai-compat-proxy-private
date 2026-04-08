package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestResponsesStreamPreservesCompactionItemLifecycle(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"cmp_1\",\"type\":\"compaction\",\"encrypted_content\":\"enc_payload\",\"summary\":[]}}\n\n",
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"cmp_1\",\"type\":\"compaction\",\"encrypted_content\":\"enc_payload\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"condensed\"}]}}\n\n",
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

	proxyAddedIdx := strings.Index(body, `{"item":{"id":"rs_proxy"`)
	proxyDoneIdx := strings.Index(body, `event: response.output_item.done`+"\n"+`data: {"item":{"id":"rs_proxy"`)
	addedEvent := `{"item":{"encrypted_content":"enc_payload","id":"cmp_1","summary":[],"type":"compaction"},"type":"response.output_item.added"}`
	doneEvent := `{"item":{"encrypted_content":"enc_payload","id":"cmp_1","summary":[{"text":"condensed","type":"summary_text"}],"type":"compaction"},"type":"response.output_item.done"}`
	addedIdx := strings.Index(body, addedEvent)
	doneIdx := strings.Index(body, doneEvent)

	if proxyAddedIdx == -1 || proxyDoneIdx == -1 || addedIdx == -1 || doneIdx == -1 {
		t.Fatalf("expected compaction added/done lifecycle to be forwarded unchanged, got %s", body)
	}
	if !(proxyAddedIdx < proxyDoneIdx && proxyDoneIdx < addedIdx && addedIdx < doneIdx) {
		t.Fatalf("expected synthetic reasoning to finish before compaction lifecycle begins, got %s", body)
	}
	if strings.Count(body, `"encrypted_content":"enc_payload"`) != 2 {
		t.Fatalf("expected encrypted_content to survive unchanged on both compaction events, got %s", body)
	}
	if strings.Contains(body, `{"item":{"encrypted_content":"enc_payload","id":"rs_proxy"`) {
		t.Fatalf("expected compaction payload to stay off synthetic reasoning item, got %s", body)
	}
}

func TestResponseEventWriterHelperBeginsCompactionLifecycleByClosingSyntheticReasoning(t *testing.T) {
	h := &responseEventWriterHelper{
		downstreamType:    "responses",
		syntheticInjected: true,
		reasoningStarted:  true,
		syntheticSummary:  &strings.Builder{},
	}

	h.beginCompactionLifecycle()

	if !h.compactionLifecycleStarted {
		t.Fatal("expected compaction lifecycle state to be recorded")
	}
	if !h.reasoningClosed {
		t.Fatal("expected compaction lifecycle to close synthetic reasoning first")
	}
	if len(h.events) != 1 {
		t.Fatalf("expected exactly one synthetic reasoning close event, got %d", len(h.events))
	}
	if got := h.events[0].Event; got != "response.output_item.done" {
		t.Fatalf("expected reasoning close event before compaction, got %q", got)
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

func TestResponsesStreamDoesNotEmitSyntheticFunctionCallArgumentsDoneForResponsesUpstream(t *testing.T) {
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
	if strings.Contains(body, `"type":"response.function_call_arguments.done"`) {
		t.Fatalf("expected native responses upstream stream to avoid synthetic function_call_arguments.done, got %s", body)
	}
	if !strings.Contains(body, `"type":"response.function_call_arguments.delta"`) {
		t.Fatalf("expected native responses upstream stream to keep raw function_call_arguments.delta, got %s", body)
	}
	if !strings.Contains(body, `{"item":{"arguments":"{\"query\":\"weather\"}","call_id":"call_1","id":"fc_1","name":"search_web","type":"function_call"},"type":"response.output_item.done"}`) {
		t.Fatalf("expected native responses upstream stream to keep final function_call output_item.done, got %s", body)
	}
}

func TestResponsesStreamFunctionCallArgumentsStayNativeWithCompatModeEnabled(t *testing.T) {
	var gotBody string
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer does not support flushing")
		}
		for _, evt := range []string{
			"event: response.output_item.added\n" +
				"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"get_weather\"}}\n\n",
			"event: response.function_call_arguments.delta\n" +
				"data: {\"item_id\":\"fc_1\",\"delta\":\"{\\\"city\\\":\\\"Shanghai\\\"}\"}\n\n",
			"event: response.output_item.done\n" +
				"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\":\\\"Shanghai\\\"}\"}}\n\n",
			"event: response.completed\n" +
				"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
		} {
			_, _ = w.Write([]byte(evt))
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	cfg := testResponsesConfigWithEndpoint(upstream.URL, config.UpstreamEndpointTypeResponses)
	cfg.Providers[0].ResponsesToolCompatMode = config.ResponsesToolCompatModeFunctionOnly
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"input":[{"role":"user","content":"hello"}],
		"tools":[
			{"type":"custom","name":"code_exec","description":"Run code"},
			{"type":"function","name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}},
			{"type":"web_search","name":"web_lookup","description":"Search the web"}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/responses" {
		t.Fatalf("expected streaming request to keep responses upstream path, got %q", gotPath)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal responses upstream request body: %v body=%s", err, gotBody)
	}
	rawTools, _ := payload["tools"].([]any)
	if len(rawTools) != 3 {
		t.Fatalf("expected 3 rewritten tools in streaming request body, got %#v body=%s", payload["tools"], gotBody)
	}
	for idx, rawTool := range rawTools {
		tool, _ := rawTool.(map[string]any)
		if got, _ := tool["type"].(string); got != "function" {
			t.Fatalf("expected compat mode to rewrite streaming request tool %d to function, got %#v body=%s", idx, tool, gotBody)
		}
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"response.function_call_arguments.delta"`) {
		t.Fatalf("expected compat mode to keep native responses function_call_arguments.delta, got %s", body)
	}
	if strings.Contains(body, `"type":"response.function_call_arguments.done"`) {
		t.Fatalf("expected compat mode to avoid synthetic function_call_arguments.done on responses upstream, got %s", body)
	}
	if !strings.Contains(body, `{"item":{"arguments":"{\"city\":\"Shanghai\"}","call_id":"call_1","id":"fc_1","name":"get_weather","type":"function_call"},"type":"response.output_item.done"}`) {
		t.Fatalf("expected compat mode to keep final native responses function_call item done event, got %s", body)
	}
}

func TestResponsesStreamEmitsFunctionCallLifecycleWithCompleteArgumentsForClients(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeAnthropic,
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "get_weather"}}},
		upstream.Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `{"city":"Shanghai"}`}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}},
	)

	addedIdx := strings.Index(body, `{"item":{"call_id":"call_1","id":"fc_1","name":"get_weather","type":"function_call"},"type":"response.output_item.added"}`)
	doneIdx := strings.Index(body, `{"item":{"arguments":"{\"city\":\"Shanghai\"}","call_id":"call_1","id":"fc_1","name":"get_weather","parameters":{"city":"Shanghai"},"type":"function_call"},"type":"response.output_item.done"}`)
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
	if !strings.Contains(body, `{"item":{"arguments":"{\"query\":\"Quectel\",\"topic\":\"finance\"}","call_id":"call_1","id":"fc_1","name":"search_web","parameters":{"query":"Quectel","topic":"finance"},"type":"function_call"},"type":"response.output_item.done"}`) {
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
	if !strings.Contains(body, `{"item":{"call_id":"call_1","id":"fc_1","name":"search_web","type":"function_call"},"type":"response.output_item.added"}`) {
		t.Fatalf("expected compatibility mode to emit metadata-only added event, got %s", body)
	}
	if !strings.Contains(body, `{"item":{"arguments":"{\"query\":\"Quectel\",\"topic\":\"finance\"}","call_id":"call_1","id":"fc_1","name":"search_web","parameters":{"query":"Quectel","topic":"finance"},"type":"function_call"},"type":"response.output_item.done"}`) {
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

	if !strings.Contains(body, `{"item":{"arguments":"{\"query\":\"Quectel\"}","call_id":"call_1","id":"fc_1","name":"search_web","parameters":{"query":"Quectel"},"type":"function_call"},"type":"response.output_item.done"}`) {
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
	if !strings.Contains(body, `"status":"completed"`) {
		t.Fatalf("expected completed response payload to include completed status, got %s", body)
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
	if !strings.Contains(body, `"response":{"id":"resp_proxy","object":"response","status":"completed","usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}`) {
		t.Fatalf("expected completed event to also include response.usage and status for compatibility, got %s", body)
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
	if !strings.Contains(body, `"response":{"id":"resp_proxy","object":"response","status":"completed","usage":{"input_tokens":2,"output_tokens":4,"total_tokens":6}}`) {
		t.Fatalf("expected done event to also include response.usage and status for compatibility, got %s", body)
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

func TestResponsesStreamUpstreamDisconnectsWithoutTerminalEventStaysInSSEProtocol(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n",
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
	if !strings.Contains(body, `"delta":"hello"`) {
		t.Fatalf("expected streamed content before upstream disconnect, got %s", body)
	}
	if !strings.Contains(body, `event: response.incomplete`) {
		t.Fatalf("expected response.incomplete terminal SSE event on upstream disconnect, got %s", body)
	}
	if !strings.Contains(body, `"health_flag":"upstreamStreamBroken","message":"unexpected EOF"`) {
		t.Fatalf("expected unexpected EOF to surface in response.incomplete event, got %s", body)
	}
	if strings.Count(body, `event: response.incomplete`) != 1 {
		t.Fatalf("expected exactly one response.incomplete terminal SSE event, got %s", body)
	}
	if strings.Contains(body, `"code":"upstream_error"`) || strings.Contains(body, `"type":"proxy_error"`) {
		t.Fatalf("expected no JSON error body after SSE start, got %s", body)
	}
}

func TestProviderResponsesRouteForcesUsageForChatStreamingUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n"))
		if strings.Contains(string(body), `"stream_options":{"include_usage":true}`) {
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5,\"prompt_tokens_details\":{\"cached_tokens\":1}},\"choices\":[{\"finish_reason\":\"stop\",\"index\":0}]}\n\n"))
		} else {
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"finish_reason\":\"stop\",\"index\":0}]}\n\n"))
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "chat",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                       "chat",
			Enabled:                  true,
			UpstreamBaseURL:          upstream.URL,
			UpstreamAPIKey:           "test-key",
			UpstreamEndpointType:     config.UpstreamEndpointTypeChat,
			UpstreamThinkingTagStyle: config.UpstreamThinkingTagStyleLegacy,
			SupportsResponses:        true,
			SupportsChat:             true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/chat/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"input":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, body)
	}
	if !strings.Contains(body, `"response":{"finish_reason":"stop","id":"chatcmpl_123","model":"gpt-5","object":"response","status":"completed","usage":{"input_tokens":3,"input_tokens_details":{"cached_tokens":1},"output_tokens":2,"total_tokens":5}}`) {
		t.Fatalf("expected provider /chat/v1/responses stream to include usage, model, and status by default, got %s", body)
	}
	if !strings.Contains(body, `"cached_tokens":1`) {
		t.Fatalf("expected cached_tokens to be surfaced in usage payload, got %s", body)
	}
}

func TestProviderResponsesRouteTreatsChatFinishReasonWithoutDoneAsIncomplete(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"delta\":{\"content\":\"hello\"},\"index\":0}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"finish_reason\":\"stop\",\"index\":0}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n"))
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
		"stream":true,
		"input":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `event: response.incomplete`) {
		t.Fatalf("expected missing raw [DONE] to surface as response.incomplete, got %s", body)
	}
	if strings.Contains(body, `"type":"response.completed"`) {
		t.Fatalf("expected missing raw [DONE] to avoid synthetic completed success, got %s", body)
	}
}

func TestProviderResponsesRouteTreatsAnthropicStopReasonWithoutMessageStopAsIncomplete(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"))
		_, _ = w.Write([]byte("event: message_delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}\n\n"))
	}))
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
			SupportsAnthropicMessages: true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"input":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `event: response.incomplete`) {
		t.Fatalf("expected missing message_stop to surface as response.incomplete, got %s", body)
	}
	if strings.Contains(body, `"type":"response.completed"`) {
		t.Fatalf("expected missing message_stop to avoid synthetic completed success, got %s", body)
	}
}

func TestProviderResponsesRouteStreamsCompleteToolArgumentsForChatUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"scrape_web\",\"arguments\":\"\"}}]},\"index\":0}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"url\\\":\\\"https://example.com\\\"}\"}}]},\"index\":0}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"finish_reason\":\"tool_calls\",\"index\":0}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n"))
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
		"stream":true,
		"input":[{"role":"user","content":"open repo"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, body)
	}
	if !strings.Contains(body, `{"item":{"call_id":"call_1","id":"call_1","name":"scrape_web","type":"function_call"},"type":"response.output_item.added"}`) {
		t.Fatalf("expected compatibility stream to emit metadata-only added event, got %s", body)
	}
	if !strings.Contains(body, `"parameters":{"url":"https://example.com"}`) {
		t.Fatalf("expected compatibility stream to also expose parsed parameters object, got %s", body)
	}
	if !strings.Contains(body, `{"item":{"arguments":"{\"url\":\"https://example.com\"}","call_id":"call_1","id":"call_1","name":"scrape_web","parameters":{"url":"https://example.com"},"type":"function_call"},"type":"response.output_item.done"}`) {
		t.Fatalf("expected compatibility stream to emit done event with parsed parameters object, got %s", body)
	}
	if !strings.Contains(body, `{"arguments":"{\"url\":\"https://example.com\"}","item_id":"call_1","type":"response.function_call_arguments.done"}`) {
		t.Fatalf("expected compatibility stream to emit arguments.done with full arguments, got %s", body)
	}
	addedIndex := strings.Index(body, `{"item":{"call_id":"call_1","id":"call_1","name":"scrape_web","type":"function_call"},"type":"response.output_item.added"}`)
	doneIndex := strings.Index(body, `{"item":{"arguments":"{\"url\":\"https://example.com\"}","call_id":"call_1","id":"call_1","name":"scrape_web","parameters":{"url":"https://example.com"},"type":"function_call"},"type":"response.output_item.done"}`)
	argumentsDoneIndex := strings.Index(body, `{"arguments":"{\"url\":\"https://example.com\"}","item_id":"call_1","type":"response.function_call_arguments.done"}`)
	if addedIndex == -1 || doneIndex == -1 || argumentsDoneIndex == -1 {
		t.Fatalf("expected added, done and arguments.done events, got %s", body)
	}
	if !(addedIndex < doneIndex && doneIndex < argumentsDoneIndex) {
		t.Fatalf("expected added then done then arguments.done, got %s", body)
	}
	if count := strings.Count(body, `{"arguments":"{\"url\":\"https://example.com\"}","item_id":"call_1","type":"response.function_call_arguments.done"}`); count != 1 {
		t.Fatalf("expected exactly one arguments.done event, got count=%d body=%s", count, body)
	}
}

func TestProviderResponsesRouteStreamsSingleArgumentsDoneAfterToolEvents(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"search_web\",\"arguments\":\"{\\\"query\\\": \\\"openai compat proxy github\\\", \\\"topic\\\": \\\"general\\\"}\"}}]},\"index\":0}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"finish_reason\":\"tool_calls\",\"index\":0}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n"))
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
		"stream":true,
		"input":[{"role":"user","content":"search github"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, body)
	}
	addedEvent := `{"item":{"call_id":"call_1","id":"call_1","name":"search_web","type":"function_call"},"type":"response.output_item.added"}`
	doneEvent := `{"item":{"arguments":"{\"query\": \"openai compat proxy github\", \"topic\": \"general\"}","call_id":"call_1","id":"call_1","name":"search_web","parameters":{"query":"openai compat proxy github","topic":"general"},"type":"function_call"},"type":"response.output_item.done"}`
	addedIndex := strings.Index(body, addedEvent)
	doneIndex := strings.Index(body, doneEvent)
	if addedIndex == -1 || doneIndex == -1 {
		t.Fatalf("expected added and done events, got %s", body)
	}
	if !(addedIndex < doneIndex) {
		t.Fatalf("expected output_item.added then output_item.done, got %s", body)
	}
	argumentsDoneEvent := `{"arguments":"{\"query\": \"openai compat proxy github\", \"topic\": \"general\"}","item_id":"call_1","type":"response.function_call_arguments.done"}`
	if !strings.Contains(body, argumentsDoneEvent) {
		t.Fatalf("expected arguments.done event, got %s", body)
	}
	argumentsDoneIndex := strings.Index(body, argumentsDoneEvent)
	if !(addedIndex < doneIndex && doneIndex < argumentsDoneIndex) {
		t.Fatalf("expected output_item.added then output_item.done then arguments.done, got %s", body)
	}
	if count := strings.Count(body, argumentsDoneEvent); count != 1 {
		t.Fatalf("expected exactly one arguments.done event, got count=%d body=%s", count, body)
	}
}

func TestProviderResponsesRouteStreamsAvoidsDuplicateCompleteArgumentsAcrossEvents(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"search_web\",\"arguments\":\"{\\\"query\\\": \\\"k3ss-official g0dm0d3 github\\\", \\\"topic\\\": \\\"general\\\"}\"}}]},\"index\":0}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"finish_reason\":\"tool_calls\",\"index\":0}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n"))
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
		"stream":true,
		"input":[{"role":"user","content":"search github"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, body)
	}
	addedWithArguments := `{"item":{"arguments":"{\"query\": \"k3ss-official g0dm0d3 github\", \"topic\": \"general\"}","call_id":"call_1","id":"call_1","name":"search_web","parameters":{"query":"k3ss-official g0dm0d3 github","topic":"general"},"type":"function_call"},"type":"response.output_item.added"}`
	if strings.Contains(body, addedWithArguments) {
		t.Fatalf("expected added event to avoid carrying complete arguments payload, got %s", body)
	}
	if !strings.Contains(body, `{"arguments":"{\"query\": \"k3ss-official g0dm0d3 github\", \"topic\": \"general\"}","item_id":"call_1","type":"response.function_call_arguments.done"}`) {
		t.Fatalf("expected compat stream to emit one final arguments.done payload for RikkaHub, got %s", body)
	}
	if !strings.Contains(body, `{"item":{"arguments":"{\"query\": \"k3ss-official g0dm0d3 github\", \"topic\": \"general\"}","call_id":"call_1","id":"call_1","name":"search_web","parameters":{"query":"k3ss-official g0dm0d3 github","topic":"general"},"type":"function_call"},"type":"response.output_item.done"}`) {
		t.Fatalf("expected done event to carry the complete final arguments once, got %s", body)
	}
	if count := strings.Count(body, `{"arguments":"{\"query\": \"k3ss-official g0dm0d3 github\", \"topic\": \"general\"}","item_id":"call_1","type":"response.function_call_arguments.done"}`); count != 1 {
		t.Fatalf("expected exactly one arguments.done event, got count=%d body=%s", count, body)
	}
}

func TestProviderResponsesRouteStreamsMetadataAddedAndSingleArgumentsDoneForRikkahub(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeAnthropic,
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "search_web"}}},
		upstream.Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `{"query":"k3ss-official g0dm0d3 github","topic":"general"}`}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}},
	)

	if !strings.Contains(body, `{"item":{"call_id":"call_1","id":"fc_1","name":"search_web","type":"function_call"},"type":"response.output_item.added"}`) {
		t.Fatalf("expected metadata-only added event for client placeholder, got %s", body)
	}
	if !strings.Contains(body, `{"arguments":"{\"query\":\"k3ss-official g0dm0d3 github\",\"topic\":\"general\"}","item_id":"call_1","type":"response.function_call_arguments.done"}`) {
		t.Fatalf("expected final arguments.done event for client parser, got %s", body)
	}
	if strings.Contains(body, `{"item":{"arguments":"{\"query\":\"k3ss-official g0dm0d3 github\",\"topic\":\"general\"}","call_id":"call_1","id":"fc_1","name":"search_web","parameters":{"query":"k3ss-official g0dm0d3 github","topic":"general"},"type":"function_call"},"type":"response.output_item.added"}`) {
		t.Fatalf("expected added event to avoid duplicate complete arguments payload, got %s", body)
	}
	if count := strings.Count(body, `{"arguments":"{\"query\":\"k3ss-official g0dm0d3 github\",\"topic\":\"general\"}","item_id":"call_1","type":"response.function_call_arguments.done"}`); count != 1 {
		t.Fatalf("expected exactly one arguments.done event, got count=%d body=%s", count, body)
	}
}

func TestProviderResponsesRouteRepairsMalformedFunctionArgumentsDoneForChatUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"scrape_web\"}}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"url\\\": \\\"https://github.com/k3ss-official/g0dm\"}}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"finish_reason\":\"tool_calls\",\"index\":0}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n"))
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
		"stream":true,
		"input":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()

	if !strings.Contains(body, `"type":"response.output_item.added"`) || !strings.Contains(body, `"name":"scrape_web"`) {
		t.Fatalf("expected repaired tool call metadata to emit added item, got %s", body)
	}
	if !strings.Contains(body, `"type":"response.output_item.done"`) || !strings.Contains(body, `"parameters":{"url":"https://github.com/k3ss-official/g0dm"}`) {
		t.Fatalf("expected repaired malformed tool arguments to emit completed function_call, got %s", body)
	}
	if !strings.Contains(body, `"type":"response.function_call_arguments.done"`) {
		t.Fatalf("expected repaired malformed tool arguments to emit synthetic arguments.done, got %s", body)
	}
}

func TestResponsesStreamCompatibilityRepairsMalformedFunctionArgumentsDone(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeChat,
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "call_1", "type": "function_call", "call_id": "call_1", "name": "scrape_web", "arguments": `{"url": "https://github.com/k3ss-official/g0dm`}}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}},
	)

	if !strings.Contains(body, `{"item":{"call_id":"call_1","id":"call_1","name":"scrape_web","type":"function_call"},"type":"response.output_item.added"}`) {
		t.Fatalf("expected repaired malformed tool call to emit added metadata item, got %s", body)
	}
	if !strings.Contains(body, `{"item":{"arguments":"{\"url\": \"https://github.com/k3ss-official/g0dm\"}","call_id":"call_1","id":"call_1","name":"scrape_web","parameters":{"url":"https://github.com/k3ss-official/g0dm"},"type":"function_call"},"type":"response.output_item.done"}`) {
		t.Fatalf("expected repaired malformed arguments to emit completed function_call, got %s", body)
	}
	if !strings.Contains(body, `{"arguments":"{\"url\": \"https://github.com/k3ss-official/g0dm\"}","item_id":"call_1","type":"response.function_call_arguments.done"}`) {
		t.Fatalf("expected repaired arguments to emit function_call_arguments.done, got %s", body)
	}
}

func TestProviderResponsesRouteEmitsPlaceholderBeforeFirstUpstreamEvent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(400 * time.Millisecond)
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"delta\":{\"content\":\"hello\"},\"index\":0}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"finish_reason\":\"stop\",\"index\":0}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	handler := NewServer(config.Config{
		DefaultProvider:          "chat",
		EnableLegacyV1Routes:     true,
		UpstreamThinkingTagStyle: config.UpstreamThinkingTagStyleLegacy,
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
	proxy := httptest.NewServer(handler)
	defer proxy.Close()

	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/chat/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"input":[{"role":"user","content":"hello"}]
	}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request failed: %v", err)
	}
	defer resp.Body.Close()
	if elapsed := time.Since(start); elapsed >= 250*time.Millisecond {
		t.Fatalf("expected response headers before first upstream event, got %s", elapsed)
	}

	readDone := make(chan string, 1)
	go func() {
		buf := make([]byte, 8192)
		var out strings.Builder
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				out.Write(buf[:n])
				if strings.Contains(out.String(), "代理层占位") {
					readDone <- out.String()
					return
				}
			}
			if err != nil {
				readDone <- out.String()
				return
			}
		}
	}()

	select {
	case body := <-readDone:
		if !strings.Contains(body, "代理层占位") {
			t.Fatalf("expected placeholder body before first upstream event, got %q", body)
		}
	case <-time.After(200 * time.Millisecond):
		select {
		case body := <-readDone:
			_ = resp.Body.Close()
			t.Fatalf("expected placeholder bytes before upstream event, but only received later body %q", body)
		case <-time.After(350 * time.Millisecond):
			_ = resp.Body.Close()
			t.Fatal("expected placeholder bytes to reach client before upstream event")
		}
	}
}

func testResponsesConfig(upstreamURL string) config.Config {
	return testResponsesConfigWithEndpoint(upstreamURL, config.UpstreamEndpointTypeResponses)
}

func renderResponsesWriterEvents(t *testing.T, endpointType string, events ...upstream.Event) string {
	t.Helper()
	rec := httptest.NewRecorder()
	state := &responsesStreamState{toolItems: map[string]*responsesToolItemState{}, toolIDAliases: map[string]string{}, upstreamEndpointType: endpointType}
	writer := &ResponsesEventWriter{w: rec, flusher: nil}
	for _, evt := range events {
		if err := writeResponsesEvent(writer, state, evt, nil); err != nil {
			t.Fatalf("writeResponsesEvent error: %v", err)
		}
	}
	return rec.Body.String()
}

func testResponsesConfigWithEndpoint(upstreamURL string, endpointType string) config.Config {
	return config.Config{
		DefaultProvider:          "openai",
		EnableLegacyV1Routes:     true,
		UpstreamThinkingTagStyle: config.UpstreamThinkingTagStyleLegacy,
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

func testResponsesConfigWithEndpointAndThinkingStyle(upstreamURL string, endpointType string, thinkingTagStyle string) config.Config {
	cfg := testResponsesConfigWithEndpoint(upstreamURL, endpointType)
	cfg.UpstreamThinkingTagStyle = thinkingTagStyle
	if len(cfg.Providers) > 0 {
		cfg.Providers[0].UpstreamThinkingTagStyle = thinkingTagStyle
	}
	return cfg
}
