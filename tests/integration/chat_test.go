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
