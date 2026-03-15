package integration_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"openai-compat-proxy/internal/testutil"
)

func TestChatHandlerReturnsChatCompletionWithToolCalls(t *testing.T) {
	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n",
		"event: response.function_call_arguments.delta\ndata: {\"item_id\":\"call_1\",\"delta\":\"{\\\"city\\\":\\\"shanghai\\\"}\"}\n\n",
		"event: response.completed\ndata: {}\n\n",
	})
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	body := `{"model":"x","messages":[{"role":"user","content":"hi"}],"stream":false}`
	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out["object"] != "chat.completion" {
		t.Fatalf("unexpected object: %v", out["object"])
	}
}

func TestChatHandlerIncludesReasoningContentExtension(t *testing.T) {
	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.reasoning.delta\ndata: {\"summary\":\"careful\"}\n\n",
		"event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\ndata: {}\n\n",
	})
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	body := `{"model":"x","messages":[{"role":"user","content":"hi"}],"stream":false}`
	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	choices, ok := out["choices"].([]any)
	if !ok || len(choices) != 1 {
		t.Fatalf("unexpected choices: %#v", out["choices"])
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if message["reasoning_content"] != "careful" {
		t.Fatalf("expected reasoning_content extension, got %#v", message["reasoning_content"])
	}
}

func TestChatHandlerIncludesReasoningContentFromReasoningItemSummary(t *testing.T) {
	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.done\ndata: {\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"brief thinking\"}]}}\n\n",
		"event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":20,\"total_tokens\":30,\"output_tokens_details\":{\"reasoning_tokens\":12}}}}\n\n",
	})
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	body := `{"model":"x","messages":[{"role":"user","content":"hi"}],"stream":false}`
	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	choices := out["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	if message["reasoning_content"] != "brief thinking" {
		t.Fatalf("expected reasoning_content from summary item, got %#v", message["reasoning_content"])
	}
	usage, ok := out["usage"].(map[string]any)
	if !ok {
		t.Fatalf("expected usage object, got %#v", out["usage"])
	}
	completionDetails, ok := usage["completion_tokens_details"].(map[string]any)
	if !ok || completionDetails["reasoning_tokens"] != float64(12) {
		t.Fatalf("expected reasoning_tokens in usage, got %#v", usage)
	}
}

func TestChatHandlerPreservesCachedTokensInUsage(t *testing.T) {
	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":20,\"total_tokens\":30,\"input_tokens_details\":{\"cached_tokens\":8},\"output_tokens_details\":{\"reasoning_tokens\":12}}}}\n\n",
	})
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	body := `{"model":"x","messages":[{"role":"user","content":"hi"}],"stream":false}`
	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	usage, ok := out["usage"].(map[string]any)
	if !ok {
		t.Fatalf("expected usage object, got %#v", out["usage"])
	}
	promptDetails, ok := usage["prompt_tokens_details"].(map[string]any)
	if !ok || promptDetails["cached_tokens"] != float64(8) {
		t.Fatalf("expected cached_tokens in prompt details, got %#v", usage)
	}
	completionDetails, ok := usage["completion_tokens_details"].(map[string]any)
	if !ok || completionDetails["reasoning_tokens"] != float64(12) {
		t.Fatalf("expected reasoning_tokens in completion details, got %#v", usage)
	}
}
