package integration_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"openai-compat-proxy/internal/testutil"
)

func TestResponsesStreamingRelaysSSEEvents(t *testing.T) {
	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.created\ndata: {\"type\":\"response.created\"}\n\n",
		"event: response.output_text.delta\ndata: {\"delta\":\"Hello\"}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n",
	})
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "event: response.output_text.delta") || !strings.Contains(text, "event: response.completed") {
		t.Fatalf("unexpected streaming body: %s", text)
	}
}

func TestChatStreamingTranslatesTextDeltasToChunks(t *testing.T) {
	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\ndata: {\"delta\":\"Hel\"}\n\n",
		"event: response.output_text.delta\ndata: {\"delta\":\"lo\"}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n",
	})
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "chat.completion.chunk") || !strings.Contains(text, "Hel") || !strings.Contains(text, "[DONE]") {
		t.Fatalf("unexpected chat stream body: %s", text)
	}
	if !strings.Contains(text, "\"finish_reason\":\"stop\"") {
		t.Fatalf("missing finish reason: %s", text)
	}
}

func TestChatStreamingStartsBeforeUpstreamCompletion(t *testing.T) {
	stub := testutil.NewDelayedStreamingUpstream(t, []string{
		"event: response.output_text.delta\ndata: {\"delta\":\"Hel\"}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n",
	}, 1500*time.Millisecond)
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	firstLine := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				errCh <- err
				return
			}
			if strings.TrimSpace(line) != "" {
				firstLine <- line
				return
			}
		}
	}()

	select {
	case line := <-firstLine:
		if !strings.Contains(line, "data: ") {
			t.Fatalf("expected early SSE data line, got %q", line)
		}
	case err := <-errCh:
		t.Fatalf("failed before receiving first line: %v", err)
	case <-time.After(600 * time.Millisecond):
		t.Fatal("timed out waiting for first downstream chunk before upstream completion")
	}
}

func TestChatStreamingEmitsSyntheticStatusBeforeText(t *testing.T) {
	stub := testutil.NewDelayedStreamingUpstream(t, []string{
		"event: response.output_item.added\ndata: {\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"phase\":\"final_answer\",\"role\":\"assistant\"}}\n\n",
		"event: response.output_text.delta\ndata: {\"delta\":\"Hello\"}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n",
	}, 400*time.Millisecond)
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	deadline := time.After(1200 * time.Millisecond)
	var lines []string
	for len(lines) < 4 {
		select {
		case <-deadline:
			joined := strings.Join(lines, "")
			if !strings.Contains(joined, `"reasoning_content":"分析中…"`) {
				t.Fatalf("expected synthetic reasoning status before text, got %s", joined)
			}
			return
		default:
			line, err := reader.ReadString('\n')
			if err != nil {
				joined := strings.Join(lines, "")
				if !strings.Contains(joined, `"reasoning_content":"分析中…"`) {
					t.Fatalf("expected synthetic reasoning status before text, got %s (err=%v)", joined, err)
				}
				return
			}
			if strings.TrimSpace(line) != "" {
				lines = append(lines, line)
			}
		}
	}

	joined := strings.Join(lines, "")
	if !strings.Contains(joined, `"reasoning_content":"正在组织回答…\n"`) {
		t.Fatalf("expected synthetic reasoning status before text, got %s", joined)
	}
}

func TestChatStreamingEmitsToolStatusAndNewlinesWhenNoRealReasoning(t *testing.T) {
	stub := testutil.NewDelayedStreamingUpstream(t, []string{
		"event: response.output_item.done\ndata: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"name\":\"lookup\",\"call_id\":\"call_1\",\"arguments\":\"\"}}\n\n",
		"event: response.function_call_arguments.delta\ndata: {\"item_id\":\"fc_1\",\"delta\":\"{\\\"q\\\":\\\"x\\\"}\"}\n\n",
		"event: response.output_item.added\ndata: {\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"phase\":\"final_answer\",\"role\":\"assistant\"}}\n\n",
		"event: response.output_text.delta\ndata: {\"delta\":\"Hello\"}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n",
	}, 200*time.Millisecond)
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, `"reasoning_content":"正在调用工具…\n"`) {
		t.Fatalf("expected tool status with newline, got %s", text)
	}
	if !strings.Contains(text, `"reasoning_content":"正在组织回答…\n"`) {
		t.Fatalf("expected planning status with newline, got %s", text)
	}
}

func TestChatStreamingSkipsSyntheticStatusWhenRealReasoningArrives(t *testing.T) {
	stub := testutil.NewDelayedStreamingUpstream(t, []string{
		"event: response.reasoning.delta\ndata: {\"summary\":\"real\"}\n\n",
		"event: response.output_text.delta\ndata: {\"delta\":\"Hello\"}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n",
	}, 300*time.Millisecond)
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, `"reasoning_content":"分析中…"`) {
		t.Fatalf("expected synthetic status to be suppressed when real reasoning exists, got %s", text)
	}
	if !strings.Contains(text, `"reasoning_content":"real"`) {
		t.Fatalf("expected real reasoning content, got %s", text)
	}
}

func TestChatStreamingContinuesRealReasoningAroundToolCalls(t *testing.T) {
	stub := testutil.NewDelayedStreamingUpstream(t, []string{
		"event: response.reasoning.delta\ndata: {\"summary\":\"先分析\"}\n\n",
		"event: response.output_item.done\ndata: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"name\":\"lookup\",\"call_id\":\"call_1\",\"arguments\":\"\"}}\n\n",
		"event: response.function_call_arguments.delta\ndata: {\"item_id\":\"fc_1\",\"delta\":\"{\\\"q\\\":\\\"x\\\"}\"}\n\n",
		"event: response.reasoning.delta\ndata: {\"summary\":\"再分析\"}\n\n",
		"event: response.output_text.delta\ndata: {\"delta\":\"done\"}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n",
	}, 150*time.Millisecond)
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, `"reasoning_content":"先分析"`) || !strings.Contains(text, `"reasoning_content":"再分析"`) {
		t.Fatalf("expected reasoning before and after tool call, got %s", text)
	}
	if !strings.Contains(text, `"name":"lookup"`) || !strings.Contains(text, `"arguments":"{\"q\":\"x\"}"`) {
		t.Fatalf("expected interleaved tool call, got %s", text)
	}
	if strings.Contains(text, `"reasoning_content":"正在调用工具…\n"`) || strings.Contains(text, `"reasoning_content":"正在组织回答…\n"`) {
		t.Fatalf("expected no synthetic statuses when real reasoning exists, got %s", text)
	}
}

func TestChatStreamingTranslatesToolCallDeltas(t *testing.T) {
	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.done\ndata: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"name\":\"get_weather\",\"call_id\":\"call_1\",\"arguments\":\"\"}}\n\n",
		"event: response.function_call_arguments.delta\ndata: {\"item_id\":\"fc_1\",\"delta\":\"{\\\"city\\\":\\\"Shanghai\\\"}\"}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n",
	})
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "get_weather") || !strings.Contains(text, "tool_calls") || !strings.Contains(text, "Shanghai") {
		t.Fatalf("unexpected tool stream body: %s", text)
	}
	if !strings.Contains(text, `"arguments":""`) {
		t.Fatalf("missing initial empty tool arguments chunk: %s", text)
	}
	if !strings.Contains(text, `"arguments":"{\"city\":\"Shanghai\"}"`) {
		t.Fatalf("missing tool argument delta chunk: %s", text)
	}
	if strings.Count(text, `"name":"get_weather"`) != 1 {
		t.Fatalf("expected tool name to be sent once, got body: %s", text)
	}
}

func TestChatStreamingTranslatesReasoningDeltasToExtensionField(t *testing.T) {
	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.reasoning.delta\ndata: {\"summary\":\"care\"}\n\n",
		"event: response.reasoning.delta\ndata: {\"summary\":\"ful\"}\n\n",
		"event: response.output_text.delta\ndata: {\"delta\":\"done\"}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n",
	})
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, `"reasoning_content":"care"`) || !strings.Contains(text, `"reasoning_content":"ful"`) {
		t.Fatalf("missing reasoning_content deltas: %s", text)
	}
	if !strings.Contains(text, "[DONE]") {
		t.Fatalf("missing done marker: %s", text)
	}
}

func TestChatStreamingEmitsReasoningContentFromReasoningItemSummary(t *testing.T) {
	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.done\ndata: {\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"summary visible\"}]}}\n\n",
		"event: response.output_text.delta\ndata: {\"delta\":\"done\"}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n",
	})
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, `"reasoning_content":"summary visible"`) {
		t.Fatalf("missing reasoning summary content chunk: %s", text)
	}
}

func TestChatStreamingIncludesUsageReasoningTokensWhenRequested(t *testing.T) {
	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\ndata: {\"delta\":\"done\"}\n\n",
		"event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":20,\"total_tokens\":30,\"output_tokens_details\":{\"reasoning_tokens\":12}}}}\n\n",
	})
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	body := `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true}}`
	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"usage":{"completion_tokens":20,"completion_tokens_details":{"reasoning_tokens":12},"prompt_tokens":10,"total_tokens":30}`) {
		t.Fatalf("missing final usage chunk: %s", text)
	}
	if !strings.Contains(text, `"choices":[]`) {
		t.Fatalf("expected empty choices usage chunk: %s", text)
	}
}

func TestChatStreamingIncludesCachedTokensWhenRequested(t *testing.T) {
	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\ndata: {\"delta\":\"done\"}\n\n",
		"event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":20,\"total_tokens\":30,\"input_tokens_details\":{\"cached_tokens\":8},\"output_tokens_details\":{\"reasoning_tokens\":12}}}}\n\n",
	})
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	body := `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true}}`
	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"prompt_tokens_details":{"cached_tokens":8}`) {
		t.Fatalf("missing cached_tokens usage chunk: %s", text)
	}
	if !strings.Contains(text, `"completion_tokens_details":{"reasoning_tokens":12}`) {
		t.Fatalf("missing reasoning_tokens usage chunk: %s", text)
	}
}

func TestChatStreamingNormalizesToolArraySchemaBeforeUpstream(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}

		tools, ok := body["tools"].([]any)
		if !ok || len(tools) != 1 {
			t.Fatalf("expected one tool, got %#v", body["tools"])
		}

		tool, ok := tools[0].(map[string]any)
		if !ok {
			t.Fatalf("expected tool object, got %#v", tools[0])
		}
		parameters, ok := tool["parameters"].(map[string]any)
		if !ok {
			t.Fatalf("expected parameters object, got %#v", tool["parameters"])
		}
		properties, ok := parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("expected properties object, got %#v", parameters["properties"])
		}
		ids, ok := properties["ids"].(map[string]any)
		if !ok {
			t.Fatalf("expected ids schema object, got %#v", properties["ids"])
		}
		if _, ok := ids["items"]; !ok {
			t.Fatalf("expected proxy to inject array items schema, got %#v", ids)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"delta\":\"ok\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"))
	}))
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	body := `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"memory_get","parameters":{"type":"object","properties":{"ids":{"type":"array"}}}}}],"stream":true}`
	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "chat.completion.chunk") || !strings.Contains(text, "[DONE]") {
		t.Fatalf("unexpected normalized stream body: %s", text)
	}
}

func TestChatNonStreamingRejectsUnsupportedMultimodalOutput(t *testing.T) {
	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.done\ndata: {\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_image\",\"image_url\":\"https://example.com/x.png\"}]}}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n",
	})
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", resp.StatusCode)
	}
}

func TestResponsesNonStreamingPreservesMultimodalOutput(t *testing.T) {
	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.done\ndata: {\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_image\",\"image_url\":\"https://example.com/x.png\"}]}}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n",
	})
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "output_image") || !strings.Contains(text, "https://example.com/x.png") {
		t.Fatalf("unexpected responses body: %s", text)
	}
}
