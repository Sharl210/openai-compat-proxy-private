package integration_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"openai-compat-proxy/internal/testutil"
)

func TestResponsesRoutePreservesReasoningMetadataWhenAvailable(t *testing.T) {
	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n",
		"event: response.reasoning.delta\ndata: {\"effort\":\"high\",\"summary\":\"careful\"}\n\n",
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
	if _, ok := body["reasoning"]; !ok {
		t.Fatal("expected reasoning metadata")
	}
}
