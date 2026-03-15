package integration_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"openai-compat-proxy/internal/testutil"
)

func TestResponsesHandlerReturnsSynthesizedJSONWithToolCalls(t *testing.T) {
	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n",
		"event: response.function_call_arguments.delta\ndata: {\"item_id\":\"call_1\",\"delta\":\"{\\\"city\\\":\\\"shanghai\\\"}\"}\n\n",
		"event: response.completed\ndata: {}\n\n",
	})
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"x","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["object"] != "response" {
		t.Fatalf("unexpected object: %v", body["object"])
	}
	if body["status"] != "completed" {
		t.Fatalf("unexpected status: %v", body["status"])
	}
}

func TestResponsesHandlerPreservesUsageIncludingCachedTokens(t *testing.T) {
	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":20,\"total_tokens\":30,\"input_tokens_details\":{\"cached_tokens\":8},\"output_tokens_details\":{\"reasoning_tokens\":12}}}}\n\n",
	})
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"x","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	usage, ok := body["usage"].(map[string]any)
	if !ok {
		t.Fatalf("expected usage object, got %#v", body["usage"])
	}
	inputDetails, ok := usage["input_tokens_details"].(map[string]any)
	if !ok || inputDetails["cached_tokens"] != float64(8) {
		t.Fatalf("expected cached_tokens in usage, got %#v", usage)
	}
	outputDetails, ok := usage["output_tokens_details"].(map[string]any)
	if !ok || outputDetails["reasoning_tokens"] != float64(12) {
		t.Fatalf("expected reasoning_tokens in usage, got %#v", usage)
	}
}
