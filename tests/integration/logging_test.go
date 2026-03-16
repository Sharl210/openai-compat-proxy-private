package integration_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/logging"
	"openai-compat-proxy/internal/testutil"
)

func TestLoggingCapturesDownstreamAndUpstreamMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "proxy.jsonl")
	closeFn, err := logging.Init(config.Config{LogFilePath: logPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":20,\"total_tokens\":30,\"input_tokens_details\":{\"cached_tokens\":8}}}}\n\n",
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

	file, err := os.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	var events []string
	var sawRedactedAuth bool
	var sawCachedTokens bool
	var sawMessageHashes bool
	var sawStreamUsageLog bool
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if event, _ := record["event"].(string); event != "" {
			events = append(events, event)
		}
		if record["x_upstream_authorization"] == "[REDACTED]" {
			sawRedactedAuth = true
		}
		if record["cached_tokens"] == float64(8) {
			sawCachedTokens = true
		}
		if _, ok := record["message_hashes"]; ok {
			sawMessageHashes = true
		}
		if event, _ := record["event"].(string); event == "upstream_stream_usage_observed" {
			sawStreamUsageLog = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(events, ",")
	if !strings.Contains(joined, "downstream_request_received") || !strings.Contains(joined, "canonical_request_built") || !strings.Contains(joined, "upstream_request_built") || !strings.Contains(joined, "upstream_response_completed") || !strings.Contains(joined, "downstream_response_sent") {
		t.Fatalf("expected request lifecycle events, got %s", joined)
	}
	if !sawRedactedAuth {
		t.Fatalf("expected upstream auth header to be redacted in logs, got events %s", joined)
	}
	if !sawCachedTokens {
		t.Fatalf("expected cached_tokens to be logged, got events %s", joined)
	}
	if !sawMessageHashes {
		t.Fatalf("expected message hashes to be logged, got events %s", joined)
	}
	if !sawStreamUsageLog {
		t.Fatalf("expected stream usage observation log, got events %s", joined)
	}
}
