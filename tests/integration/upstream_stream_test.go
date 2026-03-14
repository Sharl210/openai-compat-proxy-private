package integration_test

import (
	"context"
	"testing"

	"openai-compat-proxy/internal/testutil"
	"openai-compat-proxy/internal/upstream"
)

func TestUpstreamClientReadsTextAndToolEvents(t *testing.T) {
	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\ndata: {\"delta\":\"hel\"}\n\n",
		"event: response.function_call_arguments.delta\ndata: {\"item_id\":\"call_1\",\"delta\":\"{\\\"id\\\":1\"}\n\n",
		"event: response.completed\ndata: {}\n\n",
	})
	defer stub.Close()

	client := upstream.NewClient(stub.URL)
	events, err := client.Stream(context.Background(), sampleCanonicalRequest(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("expected streamed events")
	}
	if events[0].Event != "response.output_text.delta" {
		t.Fatalf("unexpected first event: %s", events[0].Event)
	}
}
