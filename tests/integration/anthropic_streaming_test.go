package integration_test

import (
	"bufio"
	"net/http"
	"strings"
	"testing"

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
