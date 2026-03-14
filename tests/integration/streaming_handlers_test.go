package integration_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

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
