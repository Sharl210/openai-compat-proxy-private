package integration_test

import (
	"bufio"
	"net/http"
	"strings"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/testutil"
)

func TestAnthropicMessagesStreamingEmitsAnthropicStyleEvents(t *testing.T) {
	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}}\n\n",
	})
	defer stub.Close()

	server := newTestServerWithConfig(t, config.Config{
		ListenAddr: ":0",
		Providers:  []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: stub.URL, UpstreamAPIKey: "server-key", SupportsAnthropicMessages: true}},
	})
	defer server.Close()

	resp, err := http.Post(server.URL+"/anthropic/anthropic/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet","max_tokens":128,"messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	body, err := ioReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, expected := range []string{"event: message_start", "event: content_block_start", "event: content_block_delta", "event: content_block_stop", "event: message_stop"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in stream, got %s", expected, text)
		}
	}
}

func TestAnthropicMessagesStreamingStartsBeforeUpstreamCompletion(t *testing.T) {
	stub := testutil.NewDelayedStreamingUpstream(t, []string{
		"event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}}\n\n",
	}, 1500*time.Millisecond)
	defer stub.Close()

	server := newTestServerWithConfig(t, config.Config{
		ListenAddr: ":0",
		Providers:  []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: stub.URL, UpstreamAPIKey: "server-key", SupportsAnthropicMessages: true}},
	})
	defer server.Close()

	resp, err := http.Post(server.URL+"/anthropic/anthropic/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet","max_tokens":128,"messages":[{"role":"user","content":"hi"}],"stream":true}`))
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
		if !strings.Contains(line, "event: message_start") {
			t.Fatalf("expected early anthropic SSE line, got %q", line)
		}
	case err := <-errCh:
		t.Fatalf("failed before receiving first line: %v", err)
	case <-time.After(600 * time.Millisecond):
		t.Fatal("timed out waiting for first anthropic SSE line before upstream completion")
	}
}

func TestAnthropicMessagesStreamingImmediatelyEmitsUnreasonedPlaceholder(t *testing.T) {
	stub := testutil.NewDelayedStreamingUpstream(t, []string{
		"event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}}\n\n",
	}, 1500*time.Millisecond)
	defer stub.Close()

	server := newTestServerWithConfig(t, config.Config{
		ListenAddr: ":0",
		Providers:  []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: stub.URL, UpstreamAPIKey: "server-key", SupportsAnthropicMessages: true}},
	})
	defer server.Close()

	resp, err := http.Post(server.URL+"/anthropic/anthropic/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet","max_tokens":128,"messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	body, err := ioReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	joined := string(body)
	if !strings.Contains(joined, `"thinking":"推理中…\n"`) {
		t.Fatalf("expected immediate unreasoned placeholder, got %s", joined)
	}
}

func TestAnthropicMessagesStreamingEmitsToolUseBlock(t *testing.T) {
	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.done\ndata: {\"item\":{\"type\":\"function_call\",\"id\":\"fc_123\",\"call_id\":\"call_123\",\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\":\\\"Shanghai\\\"}\"}}\n\n",
		"event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}}\n\n",
	})
	defer stub.Close()

	server := newTestServerWithConfig(t, config.Config{
		ListenAddr: ":0",
		Providers:  []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: stub.URL, UpstreamAPIKey: "server-key", SupportsAnthropicMessages: true}},
	})
	defer server.Close()

	resp, err := http.Post(server.URL+"/anthropic/anthropic/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet","max_tokens":128,"messages":[{"role":"user","content":"use the tool"}],"tools":[{"name":"get_weather","description":"Get weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	body, err := ioReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, expected := range []string{"event: content_block_start", `"type":"tool_use"`, `"name":"get_weather"`, `"type":"input_json_delta"`, `"partial_json":"{\"city\":\"Shanghai\"}"`, `"stop_reason":"tool_use"`, `"output_tokens":20`, "event: content_block_stop", "event: message_delta", "event: message_stop"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in stream, got %s", expected, text)
		}
	}
}

func TestAnthropicMessagesStreamingEmitsThinkingBlock(t *testing.T) {
	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.reasoning.delta\ndata: {\"summary\":\"先分析\"}\n\n",
		"event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}}\n\n",
	})
	defer stub.Close()

	server := newTestServerWithConfig(t, config.Config{
		ListenAddr: ":0",
		Providers:  []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: stub.URL, UpstreamAPIKey: "server-key", SupportsAnthropicMessages: true}},
	})
	defer server.Close()

	resp, err := http.Post(server.URL+"/anthropic/anthropic/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet","max_tokens":128,"messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	body, err := ioReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, expected := range []string{`"type":"thinking"`, `"type":"thinking_delta"`, `"thinking":"先分析"`, `"type":"text_delta"`, `"text":"hello"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in stream, got %s", expected, text)
		}
	}
}

func TestAnthropicMessagesStreamingEmitsSyntheticThinkingBeforeText(t *testing.T) {
	stub := testutil.NewDelayedStreamingUpstream(t, []string{
		"event: response.output_item.added\ndata: {\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"phase\":\"final_answer\",\"role\":\"assistant\"}}\n\n",
		"event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}}\n\n",
	}, 300*time.Millisecond)
	defer stub.Close()

	server := newTestServerWithConfig(t, config.Config{
		ListenAddr: ":0",
		Providers:  []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: stub.URL, UpstreamAPIKey: "server-key", SupportsAnthropicMessages: true}},
	})
	defer server.Close()

	resp, err := http.Post(server.URL+"/anthropic/anthropic/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet","max_tokens":128,"messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := ioReadAll(bufio.NewReader(resp.Body))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, `"type":"thinking_delta"`) || !strings.Contains(text, `"thinking":"分析中…`) {
		t.Fatalf("expected synthetic thinking delta before text, got %s", text)
	}
	for _, expected := range []string{`"index":0,"type":"content_block_start"`, `"index":1,"type":"content_block_start"`, `"index":1,"type":"content_block_delta"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in stream, got %s", expected, text)
		}
	}
	if strings.Count(text, `"index":0,"type":"content_block_stop"`) != 1 {
		t.Fatalf("expected one thinking block stop, got %s", text)
	}
	if strings.Count(text, `"index":1,"type":"content_block_stop"`) != 1 {
		t.Fatalf("expected one text block stop, got %s", text)
	}
}

func TestAnthropicMessagesStreamingSuppressesSyntheticThinkingWhenRealThinkingExists(t *testing.T) {
	stub := testutil.NewDelayedStreamingUpstream(t, []string{
		"event: response.reasoning.delta\ndata: {\"summary\":\"real\"}\n\n",
		"event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}}\n\n",
	}, 300*time.Millisecond)
	defer stub.Close()

	server := newTestServerWithConfig(t, config.Config{
		ListenAddr: ":0",
		Providers:  []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: stub.URL, UpstreamAPIKey: "server-key", SupportsAnthropicMessages: true}},
	})
	defer server.Close()

	resp, err := http.Post(server.URL+"/anthropic/anthropic/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet","max_tokens":128,"messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := ioReadAll(bufio.NewReader(resp.Body))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, `"thinking":"分析中…`) {
		t.Fatalf("expected synthetic thinking to be suppressed, got %s", text)
	}
	if !strings.Contains(text, `"thinking":"real"`) {
		t.Fatalf("expected real thinking delta, got %s", text)
	}
}

func ioReadAll(r *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		line, err := r.ReadBytes('\n')
		out = append(out, line...)
		if err != nil {
			if len(line) == 0 {
				return out, nil
			}
			return out, nil
		}
	}
}
