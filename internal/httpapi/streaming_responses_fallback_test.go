package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/testutil"
)

func TestResponsesStreamClosesSyntheticReasoningWithoutRealReasoning(t *testing.T) {
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
	addedIdx := strings.Index(body, `event: response.output_item.added`)
	doneIdx := strings.LastIndex(body, `event: response.output_item.done`)
	textIdx := strings.Index(body, `event: response.output_text.delta`)
	if addedIdx == -1 || doneIdx == -1 {
		t.Fatalf("expected synthetic reasoning item to be opened and closed, got %s", body)
	}
	if textIdx == -1 {
		t.Fatalf("expected output text event, got %s", body)
	}
	if !(addedIdx < doneIdx && doneIdx < textIdx) {
		t.Fatalf("expected synthetic reasoning item to close before text without extra late reasoning, got %s", body)
	}
	lateTail := body[textIdx:]
	if strings.Contains(lateTail, `response.output_item.done`) || strings.Contains(lateTail, `已完成思考`) {
		t.Fatalf("expected no late fallback reasoning text after output text, got %s", body)
	}
	if strings.Contains(body, `分析中…`) || strings.Contains(body, `正在组织回答…`) || strings.Contains(body, `正在调用工具…`) {
		t.Fatalf("expected synthetic fallback reasoning to use only one generic phrase, got %s", body)
	}
	if !strings.Contains(body, `"summary":[{"text":"**推理中**\n\n代理层占位，以兼容不同上游情况，便于客户端记录推理时长\n"`) {
		t.Fatalf("expected synthetic reasoning done item to include non-empty summary text, got %s", body)
	}
}

func TestResponsesStreamKeepsSyntheticReasoningAliveBeforeFirstRealOutput(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, ": upstream-ready\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(700 * time.Millisecond)
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-123\",\"choices\":[{\"delta\":{\"content\":\"<think>internal reasoning</think>hello\"}}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-123\",\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2},\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	originalTick := syntheticReasoningTickInterval
	syntheticReasoningTickInterval = 10 * time.Millisecond
	defer func() {
		syntheticReasoningTickInterval = originalTick
	}()

	server := NewServer(testResponsesConfigWithEndpointAndThinkingStyle(upstream.URL, config.UpstreamEndpointTypeChat, config.UpstreamThinkingTagStyleLegacy))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"input":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	textIdx := strings.Index(body, `event: response.output_text.delta`)
	if textIdx == -1 {
		t.Fatalf("expected output text event, got %s", body)
	}
	beforeText := body[:textIdx]
	addedIdx := strings.Index(beforeText, `event: response.output_item.added`)
	if addedIdx == -1 {
		t.Fatalf("expected synthetic reasoning block before first text, got %s", body)
	}
	if createdIdx := strings.Index(beforeText, `event: response.created`); createdIdx == -1 {
		t.Fatalf("expected at least one response.created before first text, got %s", body)
	}
	if !strings.Contains(beforeText, `event: response.output_item.added`) || !strings.Contains(beforeText, `event: response.reasoning_summary_text.delta`) {
		t.Fatalf("expected visible synthetic reasoning block before first text, got %s", body)
	}
	if strings.Contains(beforeText, `{"delta":"…","type":"response.reasoning_summary_text.delta"}`) {
		t.Fatalf("expected no ellipsis tick before first text, got %s", body)
	}
}

func TestResponsesStreamDoesNotHoldRealReasoningBehindSyntheticLeadTime(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-fast-think\",\"choices\":[{\"delta\":{\"content\":\"alpha\"}}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-fast-think\",\"choices\":[{\"delta\":{\"content\":\"</think>final\"}}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-fast-think\",\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2},\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	server := NewServer(testResponsesConfigWithEndpointAndThinkingStyle(upstream.URL, config.UpstreamEndpointTypeChat, config.UpstreamThinkingTagStyleLegacy))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"input":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	start := time.Now()
	server.ServeHTTP(rec, req)
	duration := time.Since(start)
	if duration >= 250*time.Millisecond {
		t.Fatalf("expected real reasoning to flow without waiting for synthetic lead time, took %s body=%s", duration, rec.Body.String())
	}
}

func TestResponsesStreamSkipsSyntheticReasoningWhenChatThinkTagsArePassedThrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-123\",\"choices\":[{\"delta\":{\"content\":\"<think>internal reasoning</think>final answer\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-123\",\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2},\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n")
	}))
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
	if strings.Contains(body, `"id":"rs_proxy"`) {
		t.Fatalf("expected no synthetic reasoning block when think tags are passed through, got %s", body)
	}
	if !strings.Contains(body, `event: response.output_text.delta`) {
		t.Fatalf("expected passthrough output text, got %s", body)
	}
}

func TestResponsesStreamMergesChatThinkTagsIntoSingleSyntheticReasoningBlock(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-123\",\"choices\":[{\"delta\":{\"content\":\"<think>internal reasoning</think>final answer\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-123\",\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2},\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n")
	}))
	defer upstream.Close()

	server := NewServer(testResponsesConfigWithEndpointAndThinkingStyle(upstream.URL, config.UpstreamEndpointTypeChat, config.UpstreamThinkingTagStyleLegacy))
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
	mergedReasoningIdx := strings.Index(body, `{"delta":"internal reasoning","type":"response.reasoning_summary_text.delta"}`)
	proxyDoneIdx := strings.Index(body, `event: response.output_item.done`+"\n"+`data: {"item":{"id":"rs_proxy"`)
	textIdx := strings.Index(body, `{"delta":"final answer","type":"response.output_text.delta"}`)
	if proxyAddedIdx == -1 || mergedReasoningIdx == -1 || proxyDoneIdx == -1 || textIdx == -1 {
		t.Fatalf("expected merged synthetic reasoning lifecycle and final text, got %s", body)
	}
	if !(proxyAddedIdx < mergedReasoningIdx && mergedReasoningIdx < proxyDoneIdx && proxyDoneIdx < textIdx) {
		t.Fatalf("expected merged reasoning to stay in rs_proxy until text starts, got %s", body)
	}
	if strings.Contains(body, `<think>`) || strings.Contains(body, `</think>`) {
		t.Fatalf("expected think tags to be removed from final output text, got %s", body)
	}
	if !strings.Contains(body, `"summary":[{"text":"**推理中**\n\n代理层占位，以兼容不同上游情况，便于客户端记录推理时长\ninternal reasoning\n","type":"summary_text"}]`) {
		t.Fatalf("expected rs_proxy summary to include merged real reasoning text, got %s", body)
	}
	if strings.Contains(body, `{"delta":"…","type":"response.reasoning_summary_text.delta"}`) {
		t.Fatalf("expected merged reasoning path to avoid synthetic ellipsis ticks, got %s", body)
	}
}

func TestResponsesStreamProgressivelyEmitsThinkReasoningBeforeClosingTag(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-think-stream\",\"choices\":[{\"delta\":{\"content\":\"<think>abc\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-think-stream\",\"choices\":[{\"delta\":{\"content\":\"def\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-think-stream\",\"choices\":[{\"delta\":{\"content\":\"</think>final\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-think-stream\",\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2},\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n")
	}))
	defer upstream.Close()

	server := NewServer(testResponsesConfigWithEndpointAndThinkingStyle(upstream.URL, config.UpstreamEndpointTypeChat, config.UpstreamThinkingTagStyleLegacy))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"input":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()

	abcIdx := strings.Index(body, `{"delta":"abc","type":"response.reasoning_summary_text.delta"}`)
	defIdx := strings.Index(body, `{"delta":"def","type":"response.reasoning_summary_text.delta"}`)
	textIdx := strings.Index(body, `{"delta":"final","type":"response.output_text.delta"}`)
	if abcIdx == -1 || defIdx == -1 || textIdx == -1 {
		t.Fatalf("expected progressive reasoning deltas before final text, got %s", body)
	}
	if !(abcIdx < defIdx && defIdx < textIdx) {
		t.Fatalf("expected abc then def reasoning deltas before final output text, got %s", body)
	}
}

func TestResponsesStreamDefaultsToReasoningUntilClosingTagWhenStyleEnabled(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-implicit-think\",\"choices\":[{\"delta\":{\"content\":\"abc\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-implicit-think\",\"choices\":[{\"delta\":{\"content\":\"def\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-implicit-think\",\"choices\":[{\"delta\":{\"content\":\"</think>final\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-implicit-think\",\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2},\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n")
	}))
	defer upstream.Close()

	server := NewServer(testResponsesConfigWithEndpointAndThinkingStyle(upstream.URL, config.UpstreamEndpointTypeChat, config.UpstreamThinkingTagStyleLegacy))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"input":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()

	abcIdx := strings.Index(body, `{"delta":"abc","type":"response.reasoning_summary_text.delta"}`)
	defIdx := strings.Index(body, `{"delta":"def","type":"response.reasoning_summary_text.delta"}`)
	textIdx := strings.Index(body, `{"delta":"final","type":"response.output_text.delta"}`)
	if abcIdx == -1 || defIdx == -1 || textIdx == -1 {
		t.Fatalf("expected implicit reasoning deltas before final text, got %s", body)
	}
	if !(abcIdx < defIdx && defIdx < textIdx) {
		t.Fatalf("expected implicit reasoning to stream before output text, got %s", body)
	}
}

func TestResponsesStreamDoesNotReenterImplicitReasoningAfterFirstClose(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-implicit-once\",\"choices\":[{\"delta\":{\"content\":\"alpha\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-implicit-once\",\"choices\":[{\"delta\":{\"content\":\"</think>final\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-implicit-once\",\"choices\":[{\"delta\":{\"content\":\" trailing text\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-implicit-once\",\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2},\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n")
	}))
	defer upstream.Close()

	server := NewServer(testResponsesConfigWithEndpointAndThinkingStyle(upstream.URL, config.UpstreamEndpointTypeChat, config.UpstreamThinkingTagStyleLegacy))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"input":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()

	alphaIdx := strings.Index(body, `{"delta":"alpha","type":"response.reasoning_summary_text.delta"}`)
	finalIdx := strings.Index(body, `{"delta":"final","type":"response.output_text.delta"}`)
	trailingIdx := strings.Index(body, `{"delta":" trailing text","type":"response.output_text.delta"}`)
	if alphaIdx == -1 || finalIdx == -1 || trailingIdx == -1 {
		t.Fatalf("expected one implicit reasoning phase followed by stable output text, got %s", body)
	}
	if !(alphaIdx < finalIdx && finalIdx < trailingIdx) {
		t.Fatalf("expected post-close text deltas to stay as output text, got %s", body)
	}
	if strings.Contains(body[finalIdx:], `{"delta":" trailing text","type":"response.reasoning_summary_text.delta"}`) {
		t.Fatalf("expected no re-entry into implicit reasoning after first close, got %s", body)
	}
}

func TestResponsesStreamSkipsWhitespaceOnlyOutputAfterThinkExtraction(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-blank\",\"choices\":[{\"delta\":{\"content\":\"<think>internal reasoning</think>\\n\\n\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-blank\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"fetch_webpage\",\"arguments\":\"{\\\"url\\\":\\\"https://github.com/k3ss-official/g0dm0d3\\\"}\"}}]}}]}\n\n")
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-blank\",\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2},\"choices\":[{\"finish_reason\":\"tool_calls\"}]}\n\n")
	}))
	defer upstream.Close()

	server := NewServer(testResponsesConfigWithEndpointAndThinkingStyle(upstream.URL, config.UpstreamEndpointTypeChat, config.UpstreamThinkingTagStyleLegacy))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"input":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()

	if strings.Contains(body, `event: response.output_text.delta`) {
		t.Fatalf("expected whitespace-only text left after think extraction to be suppressed, got %s", body)
	}
	if !strings.Contains(body, `{"item":{"call_id":"call_1","id":"call_1","name":"fetch_webpage","type":"function_call"},"type":"response.output_item.added"}`) {
		t.Fatalf("expected tool event to remain after suppressing blank text block, got %s", body)
	}
}

func TestResponsesStreamSkipsSplitWhitespaceOnlyFrameAfterThinkExtraction(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-blank-split\",\"choices\":[{\"delta\":{\"content\":\"<think>internal reasoning</think>\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-blank-split\",\"choices\":[{\"delta\":{\"content\":\"\\n\\n\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-blank-split\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"fetch_webpage\",\"arguments\":\"{\\\"url\\\":\\\"https://github.com/k3ss-official/g0dm0d3\\\"}\"}}]}}]}\n\n")
		_, _ = fmt.Fprint(w, "event: chat\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-blank-split\",\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2},\"choices\":[{\"finish_reason\":\"tool_calls\"}]}\n\n")
	}))
	defer upstream.Close()

	server := NewServer(testResponsesConfigWithEndpointAndThinkingStyle(upstream.URL, config.UpstreamEndpointTypeChat, config.UpstreamThinkingTagStyleLegacy))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"input":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()

	if strings.Contains(body, `event: response.output_text.delta`) {
		t.Fatalf("expected split whitespace-only text frame after think extraction to be suppressed, got %s", body)
	}
	if !strings.Contains(body, `{"item":{"call_id":"call_1","id":"call_1","name":"fetch_webpage","type":"function_call"},"type":"response.output_item.added"}`) {
		t.Fatalf("expected tool event to remain after suppressing split blank text block, got %s", body)
	}
}
