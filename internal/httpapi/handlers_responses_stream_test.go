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
	if strings.Contains(body, "代理层占位") || strings.Contains(body, "**推理中**") {
		t.Fatalf("expected responses stream not to expose proxy placeholder reasoning text, got %s", body)
	}
	if strings.Contains(body, invisibleSyntheticReasoningDelta) {
		t.Fatalf("expected responses stream not to emit invisible placeholder reasoning delta, got %s", body)
	}
	if !strings.Contains(body, `"type":"response.output_text.delta"`) {
		t.Fatalf("expected output_text chunk type in stream body, got %s", body)
	}
	if !strings.Contains(body, `"item_id":"msg_proxy"`) {
		t.Fatalf("expected synthetic text delta to include item_id for Responses clients, got %s", body)
	}
}

func TestResponsesStreamCompletedSnapshotCarriesFinalTextForResponsesClients(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello \"}\n\n",
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"world\"}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n",
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
	if !strings.Contains(body, `event: response.completed`) {
		t.Fatalf("expected completed event, got %s", body)
	}
	if !strings.Contains(body, `"status":"completed","type":"message"`) {
		t.Fatalf("expected completed snapshot to mark assistant message completed, got %s", body)
	}
	if !strings.Contains(body, `"text":"hello world","type":"output_text"`) {
		t.Fatalf("expected completed snapshot to carry final output text for clients that render from response.output, got %s", body)
	}
}

func TestResponsesStreamCompletedSnapshotTrimsOnlyFinalVisibleTextLineEndings(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"first\\r\\n\"}]}}\n\n",
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"msg_2\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"second \\t\\r\\n\"}]}}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(testResponsesConfig(upstream.URL))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","stream":true,"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	body := rec.Body.String()
	completedIndex := strings.LastIndex(body, "event: response.completed")
	if completedIndex == -1 {
		t.Fatalf("expected completed event, got %s", body)
	}
	completed := body[completedIndex:]
	if !strings.Contains(completed, `"text":"first\r\n"`) {
		t.Fatalf("expected earlier message line ending preserved as internal output, got %s", completed)
	}
	if !strings.Contains(completed, `"text":"second \t"`) {
		t.Fatalf("expected final visible text to retain terminal whitespace but drop CRLF, got %s", completed)
	}
	if strings.Contains(completed, `"text":"second \t\r\n"`) {
		t.Fatalf("expected final visible text CRLF removed, got %s", completed)
	}
}

func TestResponsesStreamFromChatJSONUpstreamInjectsPlaceholderAndText(t *testing.T) {
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

	server := NewServer(testResponsesConfigWithEndpointAndThinkingStyle(upstream.URL, config.UpstreamEndpointTypeChat, config.UpstreamThinkingTagStyleOff))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"input":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	textIdx := strings.Index(body, `"delta":"final answer"`)
	completedIdx := strings.Index(body, `event: response.completed`)
	proxyIdx := strings.Index(body, `"id":"rs_proxy"`)
	if proxyIdx == -1 || textIdx == -1 || !strings.Contains(body, `"type":"response.output_text.delta"`) || completedIdx == -1 {
		t.Fatalf("expected proxy reasoning lifecycle, final text and completed event, got %s", body)
	}
	if strings.Contains(body, "代理层占位") || strings.Contains(body, "**推理中**") {
		t.Fatalf("expected responses stream not to expose proxy placeholder reasoning text, got %s", body)
	}
	if strings.Contains(body, `event: response.incomplete`) {
		t.Fatalf("expected completed stream without incomplete event, got %s", body)
	}
	if !(proxyIdx < textIdx && textIdx < completedIdx) {
		t.Fatalf("expected proxy reasoning lifecycle before final text before completed, got %s", body)
	}
}

func TestResponsesStreamFromResponsesJSONUpstreamInjectsPlaceholderAndText(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_json","object":"response","status":"completed","output":[{"id":"msg_json","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"final answer"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
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
	textIdx := strings.Index(body, `"delta":"final answer"`)
	completedIdx := strings.Index(body, `event: response.completed`)
	proxyIdx := strings.Index(body, `"id":"rs_proxy"`)
	if proxyIdx == -1 || textIdx == -1 || !strings.Contains(body, `"type":"response.output_text.delta"`) || completedIdx == -1 {
		t.Fatalf("expected proxy reasoning lifecycle, final text and completed event, got %s", body)
	}
	if strings.Contains(body, "代理层占位") || strings.Contains(body, "**推理中**") {
		t.Fatalf("expected responses stream not to expose proxy placeholder reasoning text, got %s", body)
	}
	if strings.Contains(body, `event: response.incomplete`) {
		t.Fatalf("expected completed stream without incomplete event, got %s", body)
	}
	if !(proxyIdx < textIdx && textIdx < completedIdx) {
		t.Fatalf("expected proxy reasoning lifecycle before final text before completed, got %s", body)
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

	if proxyAddedIdx == -1 || proxyDoneIdx == -1 {
		t.Fatalf("expected proxy fallback reasoning item to open and close, got %s", body)
	}
	if !strings.Contains(body, `"id":"rs_1"`) || !strings.Contains(body, `"delta":"alpha"`) {
		t.Fatalf("expected real upstream reasoning lifecycle and summary text to be preserved, got %s", body)
	}
	if !strings.Contains(body, `event: response.completed`) {
		t.Fatalf("expected completed event to remain after preserving real reasoning, got %s", body)
	}
	if strings.Contains(body, `{"item":{"id":"rs_proxy","summary":[{"text":"alpha"`) {
		t.Fatalf("expected real reasoning content not to be merged into rs_proxy, got %s", body)
	}
}

func TestResponsesStreamCompletedSnapshotKeepsCompleteReasoningItem(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"summary\":[],\"encrypted_content\":\"enc_initial\"}}\n\n",
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"alpha\"}],\"encrypted_content\":\"enc_final\"}}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[{\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"alpha\"}]}]}}\n\n",
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
	completedIndex := strings.LastIndex(body, "event: response.completed")
	if completedIndex == -1 {
		t.Fatalf("expected response.completed event, got %s", body)
	}
	completed := body[completedIndex:]
	if !strings.Contains(completed, `"id":"rs_1"`) || !strings.Contains(completed, `"encrypted_content":"enc_final"`) {
		t.Fatalf("expected completed snapshot to retain the complete reasoning item, got %s", completed)
	}
	if strings.Contains(completed, `"id":"rs_proxy"`) || strings.Count(completed, `"type":"reasoning"`) != 1 {
		t.Fatalf("expected completed snapshot to contain exactly one real reasoning item, got %s", completed)
	}
}

func TestResponsesStreamCompletedSnapshotMergesMultipleReasoningWithoutChangingOtherItems(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"one\"}],\"encrypted_content\":\"enc_1\",\"done_state\":\"keep\"}}\n\n",
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n",
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"rs_2\",\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"two\"}],\"encrypted_content\":\"enc_2\"}}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"output\":[{\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"one\"}],\"terminal_state\":\"keep\"},{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"terminal_only\":\"keep\"},{\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"two\"}]}]}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(testResponsesConfigWithEndpoint(upstream.URL, config.UpstreamEndpointTypeResponses))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","stream":true,"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	body := rec.Body.String()
	completed := body[strings.LastIndex(body, "event: response.completed"):]
	for _, expected := range []string{`"id":"rs_1"`, `"encrypted_content":"enc_1"`, `"done_state":"keep"`, `"terminal_state":"keep"`, `"terminal_only":"keep"`, `"id":"rs_2"`, `"encrypted_content":"enc_2"`} {
		if !strings.Contains(completed, expected) {
			t.Fatalf("expected completed snapshot to preserve %s, got %s", expected, completed)
		}
	}
	if strings.Index(completed, `"id":"rs_1"`) > strings.Index(completed, `"id":"msg_1"`) || strings.Index(completed, `"id":"msg_1"`) > strings.Index(completed, `"id":"rs_2"`) {
		t.Fatalf("expected reasoning/message order to remain stable, got %s", completed)
	}
}

func TestResponsesStreamPreservesRealUpstreamReasoningText(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.reasoning.delta\n" +
			"data: {\"summary\":\"secret upstream reasoning\"}\n\n",
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"final answer\"}\n\n",
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

	if !strings.Contains(body, `secret upstream reasoning`) {
		t.Fatalf("expected real upstream reasoning to be preserved, got %s", body)
	}
	if !strings.Contains(body, `"delta":"final answer"`) {
		t.Fatalf("expected final answer to remain in stream, got %s", body)
	}
	if !strings.Contains(body, `event: response.completed`) {
		t.Fatalf("expected completed event to remain, got %s", body)
	}
	if !strings.Contains(body, `"id":"rs_proxy"`) {
		t.Fatalf("expected synthetic rs_proxy lifecycle to remain, got %s", body)
	}
	if strings.Contains(body, "代理层占位") || strings.Contains(body, "**推理中**") {
		t.Fatalf("expected responses stream not to expose proxy placeholder reasoning text, got %s", body)
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
	addedEvent := `{"item":{"encrypted_content":"enc_payload","id":"cmp_1","summary":[],"type":"compaction"},"output_index":1,"type":"response.output_item.added"}`
	doneEvent := `{"item":{"encrypted_content":"enc_payload","id":"cmp_1","summary":[{"text":"condensed","type":"summary_text"}],"type":"compaction"},"output_index":1,"type":"response.output_item.done"}`
	addedIdx := strings.Index(body, addedEvent)
	doneIdx := strings.Index(body, doneEvent)

	if proxyAddedIdx == -1 || proxyDoneIdx == -1 || addedIdx == -1 || doneIdx == -1 {
		t.Fatalf("expected compaction added/done lifecycle to be forwarded unchanged, got %s", body)
	}
	if !(proxyAddedIdx < proxyDoneIdx && proxyDoneIdx < addedIdx && addedIdx < doneIdx) {
		t.Fatalf("expected synthetic reasoning to finish before compaction lifecycle begins, got %s", body)
	}
	if strings.Count(body, `"encrypted_content":"enc_payload"`) < 2 {
		t.Fatalf("expected encrypted_content to survive unchanged on both compaction events, got %s", body)
	}
	if strings.Contains(body, `{"item":{"encrypted_content":"enc_payload","id":"rs_proxy"`) {
		t.Fatalf("expected compaction payload to stay off synthetic reasoning item, got %s", body)
	}
}

func TestResponseEventWriterHelperBeginsCompactionLifecycleByClosingSyntheticReasoning(t *testing.T) {
	h := &responseEventWriterHelper{
		downstreamType:            "responses",
		syntheticInjected:         true,
		syntheticReasoningStarted: true,
		syntheticSummary:          &strings.Builder{},
	}

	h.beginCompactionLifecycle()

	if !h.compactionLifecycleStarted {
		t.Fatal("expected compaction lifecycle state to be recorded")
	}
	if !h.syntheticReasoningClosed {
		t.Fatal("expected compaction lifecycle to close synthetic reasoning first")
	}
	if len(h.events) != 2 {
		t.Fatalf("expected synthesized response.created plus one reasoning close event, got %d", len(h.events))
	}
	if got := h.events[0].Event; got != "response.created" {
		t.Fatalf("expected response.created before reasoning close, got %q", got)
	}
	if got := h.events[1].Event; got != "response.output_item.done" {
		t.Fatalf("expected reasoning close event before compaction, got %q", got)
	}
}

func TestResponsesWriterChatReasoningDeltaStartsReasoningLifecycleWithoutSyntheticPrelude(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeChat,
		upstream.Event{Event: "response.created", Data: map[string]any{"response": map[string]any{"id": "resp_chat"}}},
		upstream.Event{Event: "response.reasoning.delta", Data: map[string]any{"summary": "alpha"}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}},
	)

	completedIdx := strings.Index(body, `event: response.completed`)
	if completedIdx == -1 {
		t.Fatalf("expected completed event, got %s", body)
	}
	if !strings.Contains(body, `"id":"rs_chat_reasoning"`) || !strings.Contains(body, `"delta":"alpha"`) {
		t.Fatalf("expected real chat reasoning to be preserved in responses downstream, got %s", body)
	}
	if strings.Contains(body, `"id":"rs_proxy"`) {
		t.Fatalf("expected no synthetic rs_proxy prelude in this path, got %s", body)
	}
}

func TestResponsesWriterCompletedSnapshotIncludesSynthesizedChatReasoningSummary(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeChat,
		upstream.Event{Event: "response.created", Data: map[string]any{"response": map[string]any{"id": "resp_chat"}}},
		upstream.Event{Event: "response.reasoning.delta", Data: map[string]any{"summary": "alpha"}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}},
	)

	completedIdx := strings.LastIndex(body, "event: response.completed")
	if completedIdx == -1 {
		t.Fatalf("expected completed event, got %s", body)
	}
	completed := body[completedIdx:]
	if !strings.Contains(completed, `"id":"rs_chat_reasoning","summary":[{"text":"alpha","type":"summary_text"}]`) {
		t.Fatalf("expected completed snapshot to retain synthesized reasoning summary, got %s", completed)
	}
}

func TestResponsesWriterClosesChatReasoningLifecycleBeforeText(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeChat,
		upstream.Event{Event: "response.created", Data: map[string]any{"response": map[string]any{"id": "resp_chat"}}},
		upstream.Event{Event: "response.reasoning.delta", Data: map[string]any{"summary": "alpha"}},
		upstream.Event{Event: "response.output_text.delta", Data: map[string]any{"delta": "answer"}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}},
	)

	textIdx := strings.Index(body, `"type":"response.output_text.delta"`)
	if textIdx == -1 {
		t.Fatalf("expected text event, got %s", body)
	}
	if !strings.Contains(body, `"id":"rs_chat_reasoning"`) || !strings.Contains(body, `"delta":"alpha"`) {
		t.Fatalf("expected real chat reasoning to be preserved before text in responses downstream, got %s", body)
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
	if !strings.Contains(body, `{"item":{"arguments":"","call_id":"call_1","id":"fc_1","name":"get_weather","status":"in_progress","type":"function_call"},"output_index":1,"type":"response.output_item.added"}`) {
		t.Fatalf("expected responses upstream to preserve early added event, got %s", body)
	}
}

func TestResponsesStreamAddsOutputIndexToNativeFunctionCallEventsForCodex(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"get_weather\"}}\n\n",
		"event: response.function_call_arguments.delta\n" +
			"data: {\"item_id\":\"fc_1\",\"delta\":\"{\\\"city\\\":\\\"Shanghai\\\"}\"}\n\n",
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\":\\\"Shanghai\\\"}\"}}\n\n",
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
	if !strings.Contains(body, `"output":[]`) {
		t.Fatalf("expected synthesized response.created to initialize empty output array for Codex snapshots, got %s", body)
	}
	for _, want := range []string{
		`{"item":{"arguments":"","call_id":"call_1","id":"fc_1","name":"get_weather","status":"in_progress","type":"function_call"},"output_index":1,"type":"response.output_item.added"}`,
		`{"delta":"{\"city\":\"Shanghai\"}","item_id":"call_1","output_index":1,"type":"response.function_call_arguments.delta"}`,
		`{"item":{"arguments":"{\"city\":\"Shanghai\"}","call_id":"call_1","id":"fc_1","name":"get_weather","status":"completed","type":"function_call"},"output_index":1,"type":"response.output_item.done"}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected native Responses tool event to include output_index payload %s, got %s", want, body)
		}
	}
}

func TestResponsesStreamAddsIndexesToNativeOutputTextDeltaForCodex(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n",
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
	for _, want := range []string{`"type":"response.output_text.delta"`, `"delta":"hello"`, `"item_id":"msg_proxy"`, `"output_index":1`, `"content_index":0`} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected output_text.delta to include %s for Responses clients, got %s", want, body)
		}
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
	if !strings.Contains(body, `{"item":{"arguments":"{\"query\":\"weather\"}","call_id":"call_1","id":"fc_1","name":"search_web","status":"completed","type":"function_call"},"output_index":1,"type":"response.output_item.done"}`) {
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
	if !strings.Contains(body, `{"item":{"arguments":"{\"city\":\"Shanghai\"}","call_id":"call_1","id":"fc_1","name":"get_weather","status":"completed","type":"function_call"},"output_index":1,"type":"response.output_item.done"}`) {
		t.Fatalf("expected compat mode to keep final native responses function_call item done event, got %s", body)
	}
}

func TestResponsesStreamEmitsFunctionCallLifecycleWithCompleteArgumentsForClients(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeAnthropic,
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "get_weather"}}},
		upstream.Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `{"city":"Shanghai"}`}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}},
	)

	addedIdx := strings.Index(body, `{"item":{"arguments":"","call_id":"call_1","id":"fc_1","name":"get_weather","status":"in_progress","type":"function_call"},"output_index":0,"type":"response.output_item.added"}`)
	doneIdx := strings.Index(body, `{"item":{"arguments":"{\"city\":\"Shanghai\"}","call_id":"call_1","id":"fc_1","name":"get_weather","parameters":{"city":"Shanghai"},"status":"completed","type":"function_call"},"output_index":0,"type":"response.output_item.done"}`)
	completedIdx := strings.LastIndex(body, `event: response.completed`)

	if addedIdx == -1 || doneIdx == -1 || completedIdx == -1 {
		t.Fatalf("expected function call added/done/completed lifecycle, got %s", body)
	}
	if strings.Contains(body, `"type":"response.function_call_arguments.delta"`) {
		t.Fatalf("expected compatibility mode to suppress raw arguments delta events, got %s", body)
	}
	if !strings.Contains(body, `{"arguments":"{\"city\":\"Shanghai\"}","item_id":"call_1","output_index":0,"type":"response.function_call_arguments.done"}`) {
		t.Fatalf("expected function_call_arguments.done with stable call_id, got %s", body)
	}
	if !(addedIdx < doneIdx && doneIdx < completedIdx) {
		t.Fatalf("expected function call lifecycle added -> done -> completed, got %s", body)
	}
}

func TestResponsesStreamFunctionCallLifecycleIncludesOutputIndexForCodex(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeAnthropic,
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "get_weather"}}},
		upstream.Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `{"city":"Shanghai"}`}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}},
	)

	for _, want := range []string{
		`{"item":{"arguments":"","call_id":"call_1","id":"fc_1","name":"get_weather","status":"in_progress","type":"function_call"},"output_index":0,"type":"response.output_item.added"}`,
		`{"arguments":"{\"city\":\"Shanghai\"}","item_id":"call_1","output_index":0,"type":"response.function_call_arguments.done"}`,
		`{"item":{"arguments":"{\"city\":\"Shanghai\"}","call_id":"call_1","id":"fc_1","name":"get_weather","parameters":{"city":"Shanghai"},"status":"completed","type":"function_call"},"output_index":0,"type":"response.output_item.done"}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected Codex-compatible output_index payload %s, got %s", want, body)
		}
	}
}

func TestResponsesStreamEmitsEmptyFunctionCallArgumentsForClients(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeAnthropic,
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "tooluse_1", "type": "function_call", "call_id": "tooluse_1", "name": "get_current_time"}}},
		upstream.Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "tooluse_1", "delta": `{}`}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}},
	)

	if !strings.Contains(body, `{"item":{"arguments":"{}","call_id":"tooluse_1","id":"tooluse_1","name":"get_current_time","status":"completed","type":"function_call"},"output_index":0,"type":"response.output_item.done"}`) {
		t.Fatalf("expected final function call item to contain empty arguments object, got %s", body)
	}
	if !strings.Contains(body, `{"arguments":"{}","item_id":"tooluse_1","output_index":0,"type":"response.function_call_arguments.done"}`) {
		t.Fatalf("expected function_call_arguments.done with empty object arguments, got %s", body)
	}
}

func TestResponsesStreamAccumulatesMultipleFunctionCallArgumentDeltasBeforeDone(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeAnthropic,
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "search_web"}}},
		upstream.Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `{"query":"Quectel"`}},
		upstream.Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `,"topic":"finance"}`}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}},
	)
	if !strings.Contains(body, `{"item":{"arguments":"{\"query\":\"Quectel\",\"topic\":\"finance\"}","call_id":"call_1","id":"fc_1","name":"search_web","parameters":{"query":"Quectel","topic":"finance"},"status":"completed","type":"function_call"},"output_index":0,"type":"response.output_item.done"}`) {
		t.Fatalf("expected final function call item to contain merged arguments, got %s", body)
	}
	if !strings.Contains(body, `{"arguments":"{\"query\":\"Quectel\",\"topic\":\"finance\"}","item_id":"call_1","output_index":0,"type":"response.function_call_arguments.done"}`) {
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
	if !strings.Contains(body, `{"item":{"arguments":"","call_id":"call_1","id":"fc_1","name":"search_web","status":"in_progress","type":"function_call"},"output_index":0,"type":"response.output_item.added"}`) {
		t.Fatalf("expected compatibility mode to emit metadata-only added event, got %s", body)
	}
	if !strings.Contains(body, `{"item":{"arguments":"{\"query\":\"Quectel\",\"topic\":\"finance\"}","call_id":"call_1","id":"fc_1","name":"search_web","parameters":{"query":"Quectel","topic":"finance"},"status":"completed","type":"function_call"},"output_index":0,"type":"response.output_item.done"}`) {
		t.Fatalf("expected final function_call item to retain full merged arguments, got %s", body)
	}
	if !strings.Contains(body, `{"arguments":"{\"query\":\"Quectel\",\"topic\":\"finance\"}","item_id":"call_1","output_index":0,"type":"response.function_call_arguments.done"}`) {
		t.Fatalf("expected done event with stable call_id, got %s", body)
	}
}

func TestResponsesStreamUsesUpstreamMessageLifecycleWithoutSyntheticProxyMessage(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.created", Data: map[string]any{"response": map[string]any{"id": "resp_native"}}},
		upstream.Event{Event: "response.output_item.added", Data: map[string]any{"output_index": 1, "item": map[string]any{"id": "msg_native", "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}}},
		upstream.Event{Event: "response.content_part.added", Data: map[string]any{"output_index": 1, "content_index": 0, "item_id": "msg_native", "part": map[string]any{"type": "output_text", "text": ""}}},
		upstream.Event{Event: "response.output_text.delta", Data: map[string]any{"output_index": 1, "content_index": 0, "item_id": "msg_native", "delta": "你好"}},
		upstream.Event{Event: "response.output_text.delta", Data: map[string]any{"output_index": 1, "content_index": 0, "item_id": "msg_native", "delta": "，我在。"}},
		upstream.Event{Event: "response.output_text.done", Data: map[string]any{"output_index": 1, "content_index": 0, "item_id": "msg_native", "text": "你好，我在。"}},
		upstream.Event{Event: "response.content_part.done", Data: map[string]any{"output_index": 1, "content_index": 0, "item_id": "msg_native", "part": map[string]any{"type": "output_text", "text": "你好，我在。"}}},
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"output_index": 1, "item": map[string]any{"id": "msg_native", "type": "message", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "你好，我在。"}}}}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"id": "resp_native", "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "你好，我在。"}}}}}}},
	)

	if strings.Contains(body, `"id":"msg_proxy"`) {
		t.Fatalf("expected native responses message lifecycle not to synthesize msg_proxy, got %s", body)
	}
	if strings.Count(body, `"type":"response.output_text.delta"`) != 2 {
		t.Fatalf("expected text to be streamed only through delta events, got %s", body)
	}
	if strings.Contains(body, `event: response.output_text.done
data: {"content_index":0,"item_id":"msg_native","output_index":1,"text":"你好，我在。"`) {
		t.Fatalf("expected intermediate output_text.done text snapshot to be stripped, got %s", body)
	}
	if !strings.Contains(body, `"text":"你好，我在。","type":"output_text"`) {
		t.Fatalf("expected terminal completed snapshot to retain final text, got %s", body)
	}
}

func TestResponsesStreamDoneCarriesFinalTextSnapshot(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.output_item.added", Data: map[string]any{"output_index": 0, "item": map[string]any{"id": "msg_native", "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}}},
		upstream.Event{Event: "response.output_text.delta", Data: map[string]any{"output_index": 0, "content_index": 0, "item_id": "msg_native", "delta": "hello"}},
		upstream.Event{Event: "response.done", Data: map[string]any{"response": map[string]any{"id": "resp_native", "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "hello"}}}}}}},
	)

	if strings.Contains(body, `"id":"msg_proxy"`) {
		t.Fatalf("expected native responses done path not to synthesize msg_proxy, got %s", body)
	}
	if !strings.Contains(body, `"text":"hello","type":"output_text"`) {
		t.Fatalf("expected response.done terminal snapshot to retain final text, got %s", body)
	}
}

func TestResponsesStreamTextSnapshotStrippingPreservesToolAndReasoningItems(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"output_index": 0, "item": map[string]any{"id": "rs_native", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "reasoning summary"}}}}},
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"output_index": 1, "item": map[string]any{"id": "fc_native", "type": "function_call", "call_id": "call_native", "name": "lookup", "arguments": `{"query":"hello"}`}}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"id": "resp_native", "status": "completed", "usage": map[string]any{"input_tokens": 1, "output_tokens": 2, "total_tokens": 3}, "output": []any{
			map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "hello"}}},
			map[string]any{"id": "rs_native", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "reasoning summary"}}},
			map[string]any{"id": "fc_native", "type": "function_call", "call_id": "call_native", "name": "lookup", "arguments": `{"query":"hello"}`},
		}}}},
	)

	if !strings.Contains(body, `"text":"hello","type":"output_text"`) {
		t.Fatalf("expected terminal message output text snapshot to be retained, got %s", body)
	}
	for _, want := range []string{`"type":"reasoning"`, `"text":"reasoning summary"`, `"type":"function_call"`, `"arguments":"{\"query\":\"hello\"}"`, `"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}`} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %s to survive snapshot stripping, got %s", want, body)
		}
	}
}

func TestResponsesStreamSynthesizesMissingNativeReasoningAddedBeforeDone(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"output_index": 0, "item": map[string]any{
			"id":                "rs_native",
			"type":              "reasoning",
			"encrypted_content": "enc_native",
			"summary":           []any{map[string]any{"type": "summary_text", "text": "reasoning summary"}},
		}}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"id": "resp_native", "status": "completed"}}},
	)

	addedIdx := strings.Index(body, "event: response.output_item.added\n"+`data: {"item":{"encrypted_content":"enc_native","id":"rs_native","summary":[],"type":"reasoning"},"output_index":0,"type":"response.output_item.added"}`)
	doneIdx := strings.Index(body, "event: response.output_item.done\n"+`data: {"item":{"encrypted_content":"enc_native","id":"rs_native","summary":[{"text":"reasoning summary","type":"summary_text"}],"type":"reasoning"},"output_index":0,"type":"response.output_item.done"}`)
	if addedIdx == -1 || doneIdx == -1 {
		t.Fatalf("expected missing native reasoning lifecycle start to be synthesized, got %s", body)
	}
	if addedIdx >= doneIdx {
		t.Fatalf("expected synthesized native reasoning added before done, got %s", body)
	}
}

func TestResponsesStreamSuppressesDuplicateNativeReasoningItemDone(t *testing.T) {
	reasoningDone := upstream.Event{Event: "response.output_item.done", Data: map[string]any{"output_index": 0, "item": map[string]any{
		"id":      "rs_native",
		"type":    "reasoning",
		"summary": []any{map[string]any{"type": "summary_text", "text": "reasoning summary"}},
	}}}
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		reasoningDone,
		reasoningDone,
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"id": "resp_native", "status": "completed"}}},
	)

	if count := strings.Count(body, "event: response.output_item.done"); count != 1 {
		t.Fatalf("expected duplicate native reasoning done to be suppressed, got %d events: %s", count, body)
	}
}

func TestResponsesStreamDoesNotDuplicateNativeReasoningSummaryPartAdded(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.output_item.added", Data: map[string]any{"output_index": 0, "item": map[string]any{"id": "rs_native", "type": "reasoning", "summary": []any{}}}},
		upstream.Event{Event: "response.reasoning_summary_part.added", Data: map[string]any{
			"item_id":       "rs_native",
			"output_index":  0,
			"summary_index": 0,
			"part":          map[string]any{"type": "summary_text", "text": ""},
		}},
		upstream.Event{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_native", "output_index": 0, "summary_index": 0, "delta": "alpha"}},
		upstream.Event{Event: "response.reasoning_summary_part.done", Data: map[string]any{
			"item_id":       "rs_native",
			"output_index":  0,
			"summary_index": 0,
			"part":          map[string]any{"type": "summary_text", "text": "alpha"},
		}},
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"output_index": 0, "item": map[string]any{"id": "rs_native", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "alpha"}}}}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"id": "resp_native", "status": "completed"}}},
	)

	if count := strings.Count(body, "event: response.reasoning_summary_part.added"); count != 1 {
		t.Fatalf("expected one native reasoning summary part start, got %d events: %s", count, body)
	}
	if count := strings.Count(body, "event: response.output_item.added"); count != 1 {
		t.Fatalf("expected one native reasoning item start, got %d events: %s", count, body)
	}
}

func TestResponsesStreamRetainsCompletedSummaryWhenReasoningItemDoneIsEmptyBeforeOverflow(t *testing.T) {
	for _, testCase := range []struct {
		name string
		item map[string]any
	}{
		{
			name: "empty summary",
			item: map[string]any{"id": "rs_native", "type": "reasoning", "summary": []any{}},
		},
		{
			name: "missing summary",
			item: map[string]any{"id": "rs_native", "type": "reasoning"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
				upstream.Event{Event: "response.created", Data: map[string]any{"response": map[string]any{"id": "resp_native_overflow"}}},
				upstream.Event{Event: "response.output_item.added", Data: map[string]any{"output_index": 0, "item": map[string]any{"id": "rs_native", "type": "reasoning", "summary": []any{}}}},
				upstream.Event{Event: "response.reasoning_summary_part.added", Data: map[string]any{"item_id": "rs_native", "output_index": 0, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}}},
				upstream.Event{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_native", "output_index": 0, "summary_index": 0, "delta": "finalized reasoning"}},
				upstream.Event{Event: "response.reasoning_summary_part.done", Data: map[string]any{"item_id": "rs_native", "output_index": 0, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": "finalized reasoning"}}},
				upstream.Event{Event: "response.output_item.done", Data: map[string]any{"output_index": 0, "item": testCase.item}},
				upstream.Event{Event: "error", Data: map[string]any{"error": map[string]any{"type": "invalid_request_error", "code": "context_length_exceeded", "message": "prompt is too long: context_length_exceeded from upstream"}}},
			)

			partDone := `{"item_id":"rs_native","output_index":0,"part":{"text":"finalized reasoning","type":"summary_text"},"summary_index":0,"type":"response.reasoning_summary_part.done"}`
			itemDone := `{"item":{"id":"rs_native","summary":[{"text":"finalized reasoning","type":"summary_text"}],"type":"reasoning"},"output_index":0,"type":"response.output_item.done"}`
			partDoneIndex := strings.Index(body, partDone)
			itemDoneIndex := strings.Index(body, itemDone)
			failedIndex := strings.Index(body, `event: response.failed`)
			if partDoneIndex == -1 || itemDoneIndex == -1 || failedIndex == -1 || !(partDoneIndex < itemDoneIndex && itemDoneIndex < failedIndex) {
				t.Fatalf("expected finalized summary item closure before one overflow terminal, got %s", body)
			}
			if strings.Count(body, `event: response.reasoning_summary_text.done`) > 1 || strings.Count(body, `event: response.reasoning_summary_part.done`) != 1 || strings.Count(body, itemDone) != 1 {
				t.Fatalf("expected each finalized reasoning lifecycle event exactly once, got %s", body)
			}
			if strings.Count(body, `event: response.failed`) != 1 || strings.Contains(body, `event: response.completed`) || strings.Contains(body, `event: response.done`) || strings.Contains(body, `event: response.incomplete`) {
				t.Fatalf("expected exactly one failed terminal after lifecycle closure, got %s", body)
			}
		})
	}
}

func TestResponsesStreamRetainsSummaryDeltaForEmptyOrMissingTerminalPayloads(t *testing.T) {
	tests := []struct {
		name          string
		summaryEvents []upstream.Event
		item          map[string]any
	}{
		{
			name: "summary text and part done are empty",
			summaryEvents: []upstream.Event{
				{Event: "response.reasoning_summary_text.done", Data: map[string]any{"item_id": "rs_native", "output_index": 0, "summary_index": 0, "text": ""}},
				{Event: "response.reasoning_summary_part.done", Data: map[string]any{"item_id": "rs_native", "output_index": 0, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}}},
			},
			item: map[string]any{"id": "rs_native", "type": "reasoning", "summary": []any{}},
		},
		{
			name: "summary text and part done omit text",
			summaryEvents: []upstream.Event{
				{Event: "response.reasoning_summary_text.done", Data: map[string]any{"item_id": "rs_native", "output_index": 0, "summary_index": 0}},
				{Event: "response.reasoning_summary_part.done", Data: map[string]any{"item_id": "rs_native", "output_index": 0, "summary_index": 0, "part": map[string]any{"type": "summary_text"}}},
			},
			item: map[string]any{"id": "rs_native", "type": "reasoning"},
		},
		{
			name: "summary part done is empty without text done",
			summaryEvents: []upstream.Event{
				{Event: "response.reasoning_summary_part.done", Data: map[string]any{"item_id": "rs_native", "output_index": 0, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}}},
			},
			item: map[string]any{"id": "rs_native", "type": "reasoning", "summary": []any{}},
		},
		{
			name: "summary part done omits text without text done",
			summaryEvents: []upstream.Event{
				{Event: "response.reasoning_summary_part.done", Data: map[string]any{"item_id": "rs_native", "output_index": 0, "summary_index": 0, "part": map[string]any{"type": "summary_text"}}},
			},
			item: map[string]any{"id": "rs_native", "type": "reasoning"},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			events := []upstream.Event{
				{Event: "response.created", Data: map[string]any{"response": map[string]any{"id": "resp_native_overflow"}}},
				{Event: "response.output_item.added", Data: map[string]any{"output_index": 0, "item": map[string]any{"id": "rs_native", "type": "reasoning", "summary": []any{}}}},
				{Event: "response.reasoning_summary_part.added", Data: map[string]any{"item_id": "rs_native", "output_index": 0, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}}},
				{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_native", "output_index": 0, "summary_index": 0, "delta": "finalized reasoning"}},
			}
			events = append(events, testCase.summaryEvents...)
			events = append(events,
				upstream.Event{Event: "response.output_item.done", Data: map[string]any{"output_index": 0, "item": testCase.item}},
				upstream.Event{Event: "error", Data: map[string]any{"error": map[string]any{"type": "invalid_request_error", "code": "context_length_exceeded", "message": "prompt is too long: context_length_exceeded from upstream"}}},
			)
			body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses, events...)

			textDone := `{"item_id":"rs_native","output_index":0,"summary_index":0,"text":"finalized reasoning","type":"response.reasoning_summary_text.done"}`
			partDone := `{"item_id":"rs_native","output_index":0,"part":{"text":"finalized reasoning","type":"summary_text"},"summary_index":0,"type":"response.reasoning_summary_part.done"}`
			itemDone := `{"item":{"id":"rs_native","summary":[{"text":"finalized reasoning","type":"summary_text"}],"type":"reasoning"},"output_index":0,"type":"response.output_item.done"}`
			textDoneIndex := strings.Index(body, textDone)
			partDoneIndex := strings.Index(body, partDone)
			itemDoneIndex := strings.Index(body, itemDone)
			failedIndex := strings.Index(body, `event: response.failed`)
			if textDoneIndex == -1 || partDoneIndex == -1 || itemDoneIndex == -1 || failedIndex == -1 || !(textDoneIndex < partDoneIndex && partDoneIndex < itemDoneIndex && itemDoneIndex < failedIndex) {
				t.Fatalf("expected preserved summary lifecycle before overflow terminal, got %s", body)
			}
			if strings.Count(body, `event: response.reasoning_summary_text.done`) != 1 || strings.Count(body, `event: response.reasoning_summary_part.done`) != 1 || strings.Count(body, itemDone) != 1 || strings.Count(body, `event: response.failed`) != 1 {
				t.Fatalf("expected exactly one closed reasoning lifecycle and failure terminal, got %s", body)
			}
		})
	}
}

func TestResponsesStreamDoesNotDuplicateCompletedReasoningSummaryText(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.output_item.added", Data: map[string]any{"output_index": 0, "item": map[string]any{"id": "rs_native", "type": "reasoning", "summary": []any{}}}},
		upstream.Event{Event: "response.reasoning_summary_part.added", Data: map[string]any{"item_id": "rs_native", "output_index": 0, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}}},
		upstream.Event{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_native", "output_index": 0, "summary_index": 0, "delta": "finalized reasoning"}},
		upstream.Event{Event: "response.reasoning_summary_text.done", Data: map[string]any{"item_id": "rs_native", "output_index": 0, "summary_index": 0, "text": "finalized reasoning"}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"id": "resp_native", "status": "completed"}}},
	)

	if count := strings.Count(body, "event: response.reasoning_summary_text.done"); count != 1 {
		t.Fatalf("expected one completed reasoning summary text event, got %d events: %s", count, body)
	}
	if count := strings.Count(body, "event: response.reasoning_summary_part.done"); count != 1 {
		t.Fatalf("expected terminal lifecycle to close the summary part once, got %d events: %s", count, body)
	}
}

func TestResponsesStreamTextTailBuffersPreserveCompletedNonFinalContent(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.output_item.added", Data: map[string]any{"output_index": 0, "item": map[string]any{"id": "msg_one", "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}}},
		upstream.Event{Event: "response.output_text.delta", Data: map[string]any{"output_index": 0, "item_id": "msg_one", "content_index": 0, "delta": "first\r\n"}},
		upstream.Event{Event: "response.content_part.done", Data: map[string]any{"output_index": 0, "item_id": "msg_one", "content_index": 0, "part": map[string]any{"type": "output_text", "text": "first\r\n"}}},
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"output_index": 0, "item": map[string]any{"id": "msg_one", "type": "message", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "first\r\n"}}}}},
		upstream.Event{Event: "response.output_item.added", Data: map[string]any{"output_index": 1, "item": map[string]any{"id": "msg_two", "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}}},
		upstream.Event{Event: "response.output_text.delta", Data: map[string]any{"output_index": 1, "item_id": "msg_two", "content_index": 0, "delta": "second\r\n"}},
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"output_index": 1, "item": map[string]any{"id": "msg_two", "type": "message", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "second\r\n"}}}}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"id": "resp_text", "status": "completed", "output": []any{
			map[string]any{"id": "msg_one", "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "first\r\n"}}},
			map[string]any{"id": "msg_two", "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "second\r\n"}}},
		}}}},
	)

	firstTail := `{"content_index":0,"delta":"\r\n","item_id":"msg_one","output_index":0,"type":"response.output_text.delta"}`
	firstTailIndex := strings.Index(body, firstTail)
	firstDoneIndex := strings.Index(body, `"id":"msg_one"`)
	completedIndex := strings.LastIndex(body, `event: response.completed`)
	if firstTailIndex == -1 || completedIndex == -1 || firstTailIndex > completedIndex {
		t.Fatalf("expected completed first message tail to be materialized before terminal, got %s", body)
	}
	completed := body[completedIndex:]
	if !strings.Contains(completed, `"text":"first\r\n"`) || !strings.Contains(completed, `"text":"second"`) || strings.Contains(completed, `"text":"second\r\n"`) {
		t.Fatalf("expected only final visible text tail to be trimmed, got %s", completed)
	}
	if firstDoneIndex == -1 {
		t.Fatalf("expected first message lifecycle, got %s", body)
	}
}

func TestResponsesStreamTextTailBuffersStayIsolatedByContentIndex(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.output_item.added", Data: map[string]any{"output_index": 0, "item": map[string]any{"id": "msg_multi", "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}}},
		upstream.Event{Event: "response.output_text.delta", Data: map[string]any{"output_index": 0, "item_id": "msg_multi", "content_index": 0, "delta": "first\r\n"}},
		upstream.Event{Event: "response.output_text.delta", Data: map[string]any{"output_index": 0, "item_id": "msg_multi", "content_index": 1, "delta": "second\r\n"}},
		upstream.Event{Event: "response.content_part.done", Data: map[string]any{"output_index": 0, "item_id": "msg_multi", "content_index": 0, "part": map[string]any{"type": "output_text", "text": "first\r\n"}}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"id": "resp_multi", "status": "completed", "output": []any{map[string]any{"id": "msg_multi", "type": "message", "role": "assistant", "content": []any{
			map[string]any{"type": "output_text", "text": "first\r\n"},
			map[string]any{"type": "output_text", "text": "second\r\n"},
		}}}}}},
	)

	firstTail := `{"content_index":0,"delta":"\r\n","item_id":"msg_multi","output_index":0,"type":"response.output_text.delta"}`
	if !strings.Contains(body, firstTail) {
		t.Fatalf("expected completed first content tail to be emitted independently, got %s", body)
	}
	completedIndex := strings.LastIndex(body, `event: response.completed`)
	completed := body[completedIndex:]
	if !strings.Contains(completed, `"text":"first\r\n"`) || !strings.Contains(completed, `"text":"second"`) || strings.Contains(completed, `"text":"second\r\n"`) {
		t.Fatalf("expected only final content index tail to be trimmed, got %s", completed)
	}
}

func TestResponsesStreamTextTailBuffersTrimFinalVisibleTextBeforeToolCall(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.output_item.added", Data: map[string]any{"output_index": 0, "item": map[string]any{"id": "msg_final", "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}}},
		upstream.Event{Event: "response.output_text.delta", Data: map[string]any{"output_index": 0, "item_id": "msg_final", "content_index": 0, "delta": "answer\r\n"}},
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"output_index": 0, "item": map[string]any{"id": "msg_final", "type": "message", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "answer\r\n"}}}}},
		upstream.Event{Event: "response.output_item.added", Data: map[string]any{"output_index": 1, "item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "lookup"}}},
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"output_index": 1, "item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": `{}`}}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"id": "resp_tool", "status": "completed", "output": []any{
			map[string]any{"id": "msg_final", "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "answer\r\n"}}},
			map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": `{}`},
		}}}},
	)

	if strings.Contains(body, `"item_id":"msg_final","output_index":0,"type":"response.output_text.delta"`) && strings.Contains(body, `"delta":"\r\n"`) {
		t.Fatalf("expected final visible text tail to remain withheld before trailing tool output, got %s", body)
	}
	completedIndex := strings.LastIndex(body, `event: response.completed`)
	if completedIndex == -1 {
		t.Fatalf("expected completed event, got %s", body)
	}
	completed := body[completedIndex:]
	if !strings.Contains(completed, `"text":"answer"`) || strings.Contains(completed, `"text":"answer\r\n"`) || !strings.Contains(completed, `"type":"function_call"`) {
		t.Fatalf("expected trimmed final visible text and preserved trailing tool call, got %s", completed)
	}
}

func TestResponsesStreamFailurePreservesCompletedTextTailBeforeTool(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.output_item.added", Data: map[string]any{"output_index": 0, "item": map[string]any{"id": "msg_completed", "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}}},
		upstream.Event{Event: "response.output_text.delta", Data: map[string]any{"output_index": 0, "item_id": "msg_completed", "content_index": 0, "delta": "answer\r\n"}},
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"output_index": 0, "item": map[string]any{"id": "msg_completed", "type": "message", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "answer\r\n"}}}}},
		upstream.Event{Event: "response.output_item.added", Data: map[string]any{"output_index": 1, "item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "lookup"}}},
		upstream.Event{Event: "response.output_text.delta", Data: map[string]any{"output_index": 2, "item_id": "msg_unfinished", "content_index": 0, "delta": "discard\r\n"}},
		upstream.Event{Event: "response.failed", Data: map[string]any{"response": map[string]any{"status": "failed", "error": map[string]any{"type": "upstream_error", "code": "upstream_error", "message": "boom"}}}},
	)

	completedTail := `{"content_index":0,"delta":"\r\n","item_id":"msg_completed","output_index":0,"type":"response.output_text.delta"}`
	completedTailIndex := strings.Index(body, completedTail)
	toolIndex := strings.Index(body, `"id":"fc_1"`)
	failedIndex := strings.Index(body, `event: response.failed`)
	if completedTailIndex == -1 || toolIndex == -1 || failedIndex == -1 || !(toolIndex < completedTailIndex && completedTailIndex < failedIndex) {
		t.Fatalf("expected completed message tail before failed terminal without losing tool ordering, got %s", body)
	}
	if strings.Count(body, `"item_id":"msg_unfinished"`) != 1 {
		t.Fatalf("expected unfinished message tail to remain discarded on failure, got %s", body)
	}
	if strings.Count(body, `event: response.failed`) != 1 || strings.Contains(body, `event: response.completed`) || strings.Contains(body, `event: response.done`) {
		t.Fatalf("expected one failed terminal only, got %s", body)
	}
}

func TestResponsesStreamSynthesizesNativeReasoningLifecycleBeforeSummaryDelta(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_native", "output_index": 0, "summary_index": 0, "delta": "alpha"}},
		upstream.Event{Event: "response.reasoning_summary_part.done", Data: map[string]any{
			"item_id":       "rs_native",
			"output_index":  0,
			"summary_index": 0,
			"part":          map[string]any{"type": "summary_text", "text": "alpha"},
		}},
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"output_index": 0, "item": map[string]any{"id": "rs_native", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "alpha"}}}}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"id": "resp_native", "status": "completed"}}},
	)

	addedIdx := strings.Index(body, `{"item":{"id":"rs_native","summary":[],"type":"reasoning"},"output_index":0,"type":"response.output_item.added"}`)
	partIdx := strings.Index(body, `{"item_id":"rs_native","output_index":0,"part":{"text":"","type":"summary_text"},"summary_index":0,"type":"response.reasoning_summary_part.added"}`)
	deltaIdx := strings.Index(body, `{"delta":"alpha","item_id":"rs_native","output_index":0,"summary_index":0,"type":"response.reasoning_summary_text.delta"}`)
	if addedIdx == -1 || partIdx == -1 || deltaIdx == -1 || !(addedIdx < partIdx && partIdx < deltaIdx) {
		t.Fatalf("expected native summary delta to receive a complete preceding lifecycle, got %s", body)
	}
}

func TestResponsesStreamFormatsReasoningItemTitle(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"output_index": 0, "item": map[string]any{
			"id": "rs_native", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "**标题**正文"}},
		}}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"id": "resp_native", "status": "completed"}}},
	)

	if !strings.Contains(body, `"text":"**标题**正文","type":"summary_text"`) {
		t.Fatalf("expected reasoning item title to be separated, got %s", body)
	}
}

func TestResponsesStreamFormatsReasoningTitleAcrossEvents(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.reasoning.delta", Data: map[string]any{"summary": "**标题**"}},
		upstream.Event{Event: "response.reasoning.delta", Data: map[string]any{"summary": "**正文**"}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"id": "resp_native", "status": "completed"}}},
	)

	if !strings.Contains(body, `"delta":"**标题**"`) || !strings.Contains(body, `"delta":"**正文**"`) {
		t.Fatalf("expected append-only reasoning events, got %s", body)
	}
	if strings.Contains(body, `"delta":"\n**标题**\n\n**正文**\n"`) {
		t.Fatalf("expected formatted full buffer not to be replayed as a Responses delta, got %s", body)
	}
}

func TestResponsesStreamCompletedWithoutOutputUsesNativeMessageMetadataWithoutText(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.output_item.added", Data: map[string]any{"output_index": 0, "item": map[string]any{"id": "msg_native", "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}}},
		upstream.Event{Event: "response.output_text.delta", Data: map[string]any{"output_index": 0, "content_index": 0, "item_id": "msg_native", "delta": "hello"}},
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"output_index": 0, "item": map[string]any{"id": "msg_native", "type": "message", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "hello"}}}}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"id": "resp_native", "status": "completed"}}},
	)

	if !strings.Contains(body, `"output":[{"content":[{"text":"hello","type":"output_text"}],"id":"msg_native","role":"assistant","status":"completed","type":"message"}]`) {
		t.Fatalf("expected synthesized completed output to preserve native message metadata with final text, got %s", body)
	}
}

func TestResponsesStreamCompatibilityKeepsOriginalItemIDWhilePreservingCallID(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeAnthropic,
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "search_web"}}},
		upstream.Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `{"query":"Quectel"}`}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}},
	)

	if !strings.Contains(body, `{"item":{"arguments":"{\"query\":\"Quectel\"}","call_id":"call_1","id":"fc_1","name":"search_web","parameters":{"query":"Quectel"},"status":"completed","type":"function_call"},"output_index":0,"type":"response.output_item.done"}`) {
		t.Fatalf("expected compatibility mode to preserve original function_call item id while retaining call_id, got %s", body)
	}
	if !strings.Contains(body, `{"arguments":"{\"query\":\"Quectel\"}","item_id":"call_1","output_index":0,"type":"response.function_call_arguments.done"}`) {
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
	if !strings.Contains(body, `"response":{"id":"resp_proxy","object":"response","output":[],"status":"completed","usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}`) {
		t.Fatalf("expected completed event to also include response.usage and status for compatibility, got %s", body)
	}
}

func TestResponsesStreamNormalizesAnthropicEndTurnFinishReasonForResponsesClients(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeAnthropic,
		upstream.Event{Event: "response.output_text.delta", Data: map[string]any{"delta": "done"}},
		upstream.Event{Event: "response.completed", Data: map[string]any{
			"response": map[string]any{"finish_reason": "end_turn"},
		}},
	)

	if !strings.Contains(body, `event: response.completed`) {
		t.Fatalf("expected completed event, got %s", body)
	}
	if !strings.Contains(body, `"finish_reason":"stop"`) {
		t.Fatalf("expected responses-facing finish_reason stop for Anthropic end_turn, got %s", body)
	}
	if strings.Contains(body, `"finish_reason":"end_turn"`) {
		t.Fatalf("expected Anthropic end_turn not to leak to responses clients, got %s", body)
	}
}

func TestResponsesStreamNormalizesAnthropicToolUseFinishReasonForResponsesClients(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeAnthropic,
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "search_web"}}},
		upstream.Event{Event: "response.completed", Data: map[string]any{
			"response": map[string]any{"finish_reason": "tool_use"},
		}},
	)

	if !strings.Contains(body, `event: response.completed`) {
		t.Fatalf("expected completed event, got %s", body)
	}
	if !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Fatalf("expected responses-facing finish_reason tool_calls for Anthropic tool_use, got %s", body)
	}
	if strings.Contains(body, `"finish_reason":"tool_use"`) {
		t.Fatalf("expected Anthropic tool_use not to leak to responses clients, got %s", body)
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
	if !strings.Contains(body, `"response":{"id":"resp_proxy","object":"response","output":[],"status":"completed","usage":{"input_tokens":2,"output_tokens":4,"total_tokens":6}}`) {
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
	if !strings.Contains(body, "event: response.failed") {
		t.Fatalf("expected response.failed terminal SSE event, got %s", body)
	}
	if strings.Count(body, "event: response.failed") != 1 {
		t.Fatalf("expected exactly one response.failed terminal SSE event, got %s", body)
	}
	if !strings.Contains(body, `"health_flag":"upstream_error","message":"boom"`) {
		t.Fatalf("expected terminal failure payload in SSE body, got %s", body)
	}
	if !strings.Contains(body, `"response":{"error"`) {
		t.Fatalf("expected response.failed to carry a response error object after SSE start, got %s", body)
	}
}

func TestResponsesStreamContextOverflowClosesNativeReasoningBeforeOneFailedTerminal(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.created", Data: map[string]any{"response": map[string]any{"id": "resp_native_overflow"}}},
		upstream.Event{Event: "response.output_item.added", Data: map[string]any{"output_index": 0, "item": map[string]any{"id": "rs_native", "type": "reasoning", "summary": []any{}}}},
		upstream.Event{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_native", "output_index": 0, "summary_index": 0, "delta": "partial reasoning"}},
		upstream.Event{Event: "error", Data: map[string]any{"error": map[string]any{"type": "invalid_request_error", "code": "context_length_exceeded", "message": "prompt is too long: context_length_exceeded from upstream"}}},
	)

	partDoneIdx := strings.Index(body, `event: response.reasoning_summary_part.done`)
	itemDoneIdx := strings.LastIndex(body, `event: response.output_item.done`)
	failedIdx := strings.Index(body, `event: response.failed`)
	if partDoneIdx == -1 || itemDoneIdx == -1 || failedIdx == -1 || !(partDoneIdx < itemDoneIdx && itemDoneIdx < failedIdx) {
		t.Fatalf("expected native reasoning lifecycle to close before failed terminal, got %s", body)
	}
	if strings.Count(body, `event: response.failed`) != 1 {
		t.Fatalf("expected exactly one failed terminal event, got %s", body)
	}
	if !strings.Contains(body, `"id":"resp_native_overflow"`) || !strings.Contains(body, `"code":"context_length_exceeded"`) || !strings.Contains(body, `prompt is too long`) {
		t.Fatalf("expected failed response to retain the response ID and normalized overflow details, got %s", body)
	}
	if strings.Contains(body, `event: response.completed`) || strings.Contains(body, `event: response.done`) || strings.Contains(body, `event: response.incomplete`) {
		t.Fatalf("expected overflow to emit no successful or incomplete terminal, got %s", body)
	}
}

func TestResponsesStreamReasoningPartNotFoundIsNotReclassifiedAsContextOverflow(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.created", Data: map[string]any{"response": map[string]any{"id": "resp_reasoning_missing"}}},
		upstream.Event{Event: "response.output_item.added", Data: map[string]any{"output_index": 0, "item": map[string]any{"id": "rs_native", "type": "reasoning", "summary": []any{}}}},
		upstream.Event{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_native", "output_index": 0, "summary_index": 0, "delta": "partial reasoning"}},
		upstream.Event{Event: "response.failed", Data: map[string]any{"response": map[string]any{"status": "failed", "error": map[string]any{"type": "invalid_request_error", "code": "invalid_request_error", "message": "reasoning part rs_opaque not found"}}}},
	)

	if strings.Contains(body, `context_length_exceeded`) || strings.Contains(body, `prompt is too long`) {
		t.Fatalf("expected missing reasoning part error to remain distinct from context overflow, got %s", body)
	}
	if !strings.Contains(body, `reasoning part rs_opaque not found`) {
		t.Fatalf("expected original missing reasoning part error to remain visible, got %s", body)
	}
}

func TestResponsesStreamReturnsHTTP400ForEarlyContextTooLarge(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: error\n" +
			"data: {\"type\":\"error\",\"code\":\"context_too_large\",\"message\":\"context too large\"}\n\n",
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
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected early context-too-large upstream error to return HTTP 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `event:`) || strings.Contains(body, `"id":"rs_proxy"`) {
		t.Fatalf("expected early context overflow not to start SSE, got %s", body)
	}
	if !strings.Contains(body, `"code":"context_length_exceeded"`) || !strings.Contains(body, `prompt is too long`) {
		t.Fatalf("expected client-compatible context overflow body, got %s", body)
	}
}

func TestResponsesStreamReturnsHTTP400ForEarlyTopLevelContextTooLargeCode(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: error\n" +
			"data: {\"type\":\"error\",\"code\":\"context_too_large\",\"message\":\"request rejected\"}\n\n",
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
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected early top-level context-too-large code to return HTTP 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `event:`) || strings.Contains(body, `"id":"rs_proxy"`) {
		t.Fatalf("expected early context overflow not to start SSE, got %s", body)
	}
	if !strings.Contains(body, `"code":"context_length_exceeded"`) || !strings.Contains(body, `prompt is too long`) {
		t.Fatalf("expected client-compatible context overflow body, got %s", body)
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
	if !strings.Contains(body, `event: response.failed`) {
		t.Fatalf("expected response.failed terminal SSE event on upstream disconnect, got %s", body)
	}
	if !strings.Contains(body, `"health_flag":"upstreamStreamBroken","message":"unexpected EOF"`) {
		t.Fatalf("expected unexpected EOF to surface in response.failed event, got %s", body)
	}
	if strings.Count(body, `event: response.failed`) != 1 {
		t.Fatalf("expected exactly one response.failed terminal SSE event, got %s", body)
	}
	if !strings.Contains(body, `"response":{"error"`) {
		t.Fatalf("expected response.failed to carry a response error object after SSE start, got %s", body)
	}
}

func TestResponsesStreamChatMaxTokensToolCallIsIncompleteButNotStreamBroken(t *testing.T) {
	upstream := newChatStreamingUpstream(t, []string{
		"event: chat\n" +
			"data: {\"id\":\"chat-123\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_partial\",\"function\":{\"name\":\"lookup_record\"}}]}}]}\n\n",
		"event: chat\n" +
			"data: {\"id\":\"chat-123\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"query\\\":\"}}]}}]}\n\n",
		"event: chat\n" +
			"data: {\"id\":\"chat-123\",\"choices\":[{\"finish_reason\":\"max_tokens\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(testResponsesConfigWithEndpoint(upstream.URL, config.UpstreamEndpointTypeChat))
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
		t.Fatalf("expected response.incomplete terminal event, got %s", body)
	}
	if strings.Contains(body, `"health_flag":"upstreamStreamBroken"`) {
		t.Fatalf("expected max_tokens truncation not to be reported as stream broken, got %s", body)
	}
	if !strings.Contains(body, `"health_flag":"upstream_max_tokens"`) || !strings.Contains(body, `"finish_reason":"max_tokens"`) {
		t.Fatalf("expected max_tokens health flag and finish reason, got %s", body)
	}
	if strings.Contains(body, `"type":"function_call","id":"call_partial"`) && strings.Contains(body, `"type":"response.output_item.done"`) {
		t.Fatalf("expected partial tool call not to be completed, got %s", body)
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
	if !strings.Contains(body, `"response":{"finish_reason":"stop","id":"chatcmpl_123","model":"gpt-5","object":"response","output":[{"id":"rs_proxy","summary":[],"type":"reasoning"}],"status":"completed","usage":{"input_tokens":3,"input_tokens_details":{"cached_tokens":1},"output_tokens":2,"total_tokens":5}}`) {
		t.Fatalf("expected provider /chat/v1/responses stream to include usage, model, and status by default, got %s", body)
	}
	if !strings.Contains(body, `"cached_tokens":1`) {
		t.Fatalf("expected cached_tokens to be surfaced in usage payload, got %s", body)
	}
}

func TestProviderResponsesRouteTreatsChatFinishReasonWithoutDoneAsFailed(t *testing.T) {
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
	if !strings.Contains(body, `event: response.failed`) {
		t.Fatalf("expected missing raw [DONE] to surface as response.failed, got %s", body)
	}
	if strings.Contains(body, `"type":"response.completed"`) {
		t.Fatalf("expected missing raw [DONE] to avoid synthetic completed success, got %s", body)
	}
}

func TestProviderResponsesRouteTreatsAnthropicStopReasonWithoutMessageStopAsFailed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
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
	if !strings.Contains(body, `event: response.failed`) {
		t.Fatalf("expected missing message_stop to surface as response.failed, got %s", body)
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
	if !strings.Contains(body, `{"item":{"arguments":"","call_id":"call_1","id":"call_1","name":"scrape_web","status":"in_progress","type":"function_call"},"output_index":0,"type":"response.output_item.added"}`) {
		t.Fatalf("expected compatibility stream to emit metadata-only added event, got %s", body)
	}
	if !strings.Contains(body, `"parameters":{"url":"https://example.com"}`) {
		t.Fatalf("expected compatibility stream to also expose parsed parameters object, got %s", body)
	}
	if !strings.Contains(body, `{"item":{"arguments":"{\"url\":\"https://example.com\"}","call_id":"call_1","id":"call_1","name":"scrape_web","parameters":{"url":"https://example.com"},"status":"completed","type":"function_call"},"output_index":0,"type":"response.output_item.done"}`) {
		t.Fatalf("expected compatibility stream to emit done event with parsed parameters object, got %s", body)
	}
	if !strings.Contains(body, `{"arguments":"{\"url\":\"https://example.com\"}","item_id":"call_1","output_index":0,"type":"response.function_call_arguments.done"}`) {
		t.Fatalf("expected compatibility stream to emit arguments.done with full arguments, got %s", body)
	}
	addedIndex := strings.Index(body, `{"item":{"arguments":"","call_id":"call_1","id":"call_1","name":"scrape_web","status":"in_progress","type":"function_call"},"output_index":0,"type":"response.output_item.added"}`)
	doneIndex := strings.Index(body, `{"item":{"arguments":"{\"url\":\"https://example.com\"}","call_id":"call_1","id":"call_1","name":"scrape_web","parameters":{"url":"https://example.com"},"status":"completed","type":"function_call"},"output_index":0,"type":"response.output_item.done"}`)
	argumentsDoneIndex := strings.Index(body, `{"arguments":"{\"url\":\"https://example.com\"}","item_id":"call_1","output_index":0,"type":"response.function_call_arguments.done"}`)
	if addedIndex == -1 || doneIndex == -1 || argumentsDoneIndex == -1 {
		t.Fatalf("expected added, done and arguments.done events, got %s", body)
	}
	if !(addedIndex < doneIndex && doneIndex < argumentsDoneIndex) {
		t.Fatalf("expected added then done then arguments.done, got %s", body)
	}
	if count := strings.Count(body, `{"arguments":"{\"url\":\"https://example.com\"}","item_id":"call_1","output_index":0,"type":"response.function_call_arguments.done"}`); count != 1 {
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
	addedEvent := `{"item":{"arguments":"","call_id":"call_1","id":"call_1","name":"search_web","status":"in_progress","type":"function_call"},"output_index":0,"type":"response.output_item.added"}`
	doneEvent := `{"item":{"arguments":"{\"query\": \"openai compat proxy github\", \"topic\": \"general\"}","call_id":"call_1","id":"call_1","name":"search_web","parameters":{"query":"openai compat proxy github","topic":"general"},"status":"completed","type":"function_call"},"output_index":0,"type":"response.output_item.done"}`
	addedIndex := strings.Index(body, addedEvent)
	doneIndex := strings.Index(body, doneEvent)
	if addedIndex == -1 || doneIndex == -1 {
		t.Fatalf("expected added and done events, got %s", body)
	}
	if !(addedIndex < doneIndex) {
		t.Fatalf("expected output_item.added then output_item.done, got %s", body)
	}
	argumentsDoneEvent := `{"arguments":"{\"query\": \"openai compat proxy github\", \"topic\": \"general\"}","item_id":"call_1","output_index":0,"type":"response.function_call_arguments.done"}`
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
	if !strings.Contains(body, `{"arguments":"{\"query\": \"k3ss-official g0dm0d3 github\", \"topic\": \"general\"}","item_id":"call_1","output_index":0,"type":"response.function_call_arguments.done"}`) {
		t.Fatalf("expected compat stream to emit one final arguments.done payload for RikkaHub, got %s", body)
	}
	if !strings.Contains(body, `{"item":{"arguments":"{\"query\": \"k3ss-official g0dm0d3 github\", \"topic\": \"general\"}","call_id":"call_1","id":"call_1","name":"search_web","parameters":{"query":"k3ss-official g0dm0d3 github","topic":"general"},"status":"completed","type":"function_call"},"output_index":0,"type":"response.output_item.done"}`) {
		t.Fatalf("expected done event to carry the complete final arguments once, got %s", body)
	}
	if count := strings.Count(body, `{"arguments":"{\"query\": \"k3ss-official g0dm0d3 github\", \"topic\": \"general\"}","item_id":"call_1","output_index":0,"type":"response.function_call_arguments.done"}`); count != 1 {
		t.Fatalf("expected exactly one arguments.done event, got count=%d body=%s", count, body)
	}
}

func TestProviderResponsesRouteStreamsMetadataAddedAndSingleArgumentsDoneForRikkahub(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeAnthropic,
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "search_web"}}},
		upstream.Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "fc_1", "delta": `{"query":"k3ss-official g0dm0d3 github","topic":"general"}`}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}},
	)

	if !strings.Contains(body, `{"item":{"arguments":"","call_id":"call_1","id":"fc_1","name":"search_web","status":"in_progress","type":"function_call"},"output_index":0,"type":"response.output_item.added"}`) {
		t.Fatalf("expected metadata-only added event for client placeholder, got %s", body)
	}
	if !strings.Contains(body, `{"arguments":"{\"query\":\"k3ss-official g0dm0d3 github\",\"topic\":\"general\"}","item_id":"call_1","output_index":0,"type":"response.function_call_arguments.done"}`) {
		t.Fatalf("expected final arguments.done event for client parser, got %s", body)
	}
	if strings.Contains(body, `{"item":{"arguments":"{\"query\":\"k3ss-official g0dm0d3 github\",\"topic\":\"general\"}","call_id":"call_1","id":"fc_1","name":"search_web","parameters":{"query":"k3ss-official g0dm0d3 github","topic":"general"},"status":"completed","type":"function_call"},"output_index":0,"type":"response.output_item.added"}`) {
		t.Fatalf("expected added event to avoid duplicate complete arguments payload, got %s", body)
	}
	if count := strings.Count(body, `{"arguments":"{\"query\":\"k3ss-official g0dm0d3 github\",\"topic\":\"general\"}","item_id":"call_1","output_index":0,"type":"response.function_call_arguments.done"}`); count != 1 {
		t.Fatalf("expected exactly one arguments.done event, got count=%d body=%s", count, body)
	}
}

func TestResponsesCompatibilityStreamFlushesConsecutiveToolCallsWithoutTextBridge(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeAnthropic,
		upstream.Event{Event: "response.output_item.added", Data: map[string]any{"item": map[string]any{"id": "call_1", "type": "function_call", "call_id": "call_1", "name": "search_web"}}},
		upstream.Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "call_1", "delta": `{"query":"2026年6月 热门新闻"}`}},
		upstream.Event{Event: "response.output_item.added", Data: map[string]any{"item": map[string]any{"id": "call_2", "type": "function_call", "call_id": "call_2", "name": "search_web"}}},
		upstream.Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": "call_2", "delta": `{"query":"科技 最新 热点 2026"}`}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}, "stop_reason": "tool_use"}},
	)

	call1Done := `{"item":{"arguments":"{\"query\":\"2026年6月 热门新闻\"}","call_id":"call_1","id":"call_1","name":"search_web","parameters":{"query":"2026年6月 热门新闻"},"status":"completed","type":"function_call"},"output_index":0,"type":"response.output_item.done"}`
	call2Done := `{"item":{"arguments":"{\"query\":\"科技 最新 热点 2026\"}","call_id":"call_2","id":"call_2","name":"search_web","parameters":{"query":"科技 最新 热点 2026"},"status":"completed","type":"function_call"},"output_index":1,"type":"response.output_item.done"}`
	call1ArgsDone := `{"arguments":"{\"query\":\"2026年6月 热门新闻\"}","item_id":"call_1","output_index":0,"type":"response.function_call_arguments.done"}`
	call2ArgsDone := `{"arguments":"{\"query\":\"科技 最新 热点 2026\"}","item_id":"call_2","output_index":1,"type":"response.function_call_arguments.done"}`

	if !strings.Contains(body, call1Done) || !strings.Contains(body, call2Done) {
		t.Fatalf("expected both consecutive tool calls to emit done events, got %s", body)
	}
	if !strings.Contains(body, call1ArgsDone) || !strings.Contains(body, call2ArgsDone) {
		t.Fatalf("expected both consecutive tool calls to emit arguments.done events, got %s", body)
	}
	call1DoneIdx := strings.Index(body, call1Done)
	call1ArgsDoneIdx := strings.Index(body, call1ArgsDone)
	call2DoneIdx := strings.Index(body, call2Done)
	call2ArgsDoneIdx := strings.Index(body, call2ArgsDone)
	if !(call1DoneIdx != -1 && call1ArgsDoneIdx != -1 && call2DoneIdx != -1 && call2ArgsDoneIdx != -1) {
		t.Fatalf("expected all indices present, got %s", body)
	}
	if !(call1DoneIdx < call1ArgsDoneIdx && call1ArgsDoneIdx < call2DoneIdx && call2DoneIdx < call2ArgsDoneIdx) {
		t.Fatalf("expected consecutive tool completions to preserve order, got %s", body)
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

	if !strings.Contains(body, `{"item":{"arguments":"","call_id":"call_1","id":"call_1","name":"scrape_web","status":"in_progress","type":"function_call"},"output_index":0,"type":"response.output_item.added"}`) {
		t.Fatalf("expected repaired malformed tool call to emit added metadata item, got %s", body)
	}
	if !strings.Contains(body, `{"item":{"arguments":"{\"url\": \"https://github.com/k3ss-official/g0dm\"}","call_id":"call_1","id":"call_1","name":"scrape_web","parameters":{"url":"https://github.com/k3ss-official/g0dm"},"status":"completed","type":"function_call"},"output_index":0,"type":"response.output_item.done"}`) {
		t.Fatalf("expected repaired malformed arguments to emit completed function_call, got %s", body)
	}
	if !strings.Contains(body, `{"arguments":"{\"url\": \"https://github.com/k3ss-official/g0dm\"}","item_id":"call_1","output_index":0,"type":"response.function_call_arguments.done"}`) {
		t.Fatalf("expected repaired arguments to emit function_call_arguments.done, got %s", body)
	}
}

func TestProviderResponsesRouteDefersPlaceholderUntilFirstUpstreamEvent(t *testing.T) {
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

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request failed: %v", err)
	}
	defer resp.Body.Close()
	readDone := make(chan string, 1)
	go func() {
		buf := make([]byte, 8192)
		var out strings.Builder
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				out.Write(buf[:n])
				if strings.Contains(out.String(), `"id":"rs_proxy"`) {
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

	body := <-readDone
	if !strings.Contains(body, `"id":"rs_proxy"`) {
		t.Fatalf("expected proxy reasoning lifecycle after first upstream event, got %q", body)
	}
	if strings.Contains(body, "代理层占位") || strings.Contains(body, "**推理中**") {
		t.Fatalf("expected proxy lifecycle not to expose placeholder reasoning text, got %q", body)
	}
}

func TestCloneResponsesStreamStateInitializesLifecycleMaps(t *testing.T) {
	state := cloneResponsesStreamState(nil, "req_test", config.UpstreamEndpointTypeResponses)
	if state.reasoningSummaryParts == nil || state.reasoningSummaryPartClosed == nil || state.reasoningSummaryTextDone == nil || state.activeReasoningItems == nil || state.reasoningItemsClosed == nil || state.textTailBuffers == nil || state.completedTextItems == nil || state.completedTextParts == nil {
		t.Fatalf("expected nil initial state to initialize all Responses lifecycle maps, got %#v", state)
	}
}

func testResponsesConfig(upstreamURL string) config.Config {
	return testResponsesConfigWithEndpoint(upstreamURL, config.UpstreamEndpointTypeResponses)
}

func renderResponsesWriterEvents(t *testing.T, endpointType string, events ...upstream.Event) string {
	t.Helper()
	rec := httptest.NewRecorder()
	state := newResponsesStreamState("", endpointType)
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
