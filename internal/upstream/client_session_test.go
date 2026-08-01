package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/debugarchive"
	"openai-compat-proxy/internal/model"
)

func TestOpenEventStreamCarriesSessionIDFromContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\"}\n\n"))
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeResponses})
	ctx := WithSessionID(context.Background(), "session-upstream-123")
	req := model.CanonicalRequest{
		RequestID: "req-session-test",
		Model:     "gpt-test",
		Messages: []model.CanonicalMessage{{
			Role:  "user",
			Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}},
		}},
	}

	stream, err := client.OpenEventStream(ctx, req, "")
	if err != nil {
		t.Fatalf("open event stream: %v", err)
	}
	defer stream.Close()

	if stream.sessionID != "session-upstream-123" {
		t.Fatalf("expected stream session ID %q, got %q", "session-upstream-123", stream.sessionID)
	}
}

func TestEventStreamArchiveIncludesSessionID(t *testing.T) {
	root := t.TempDir()
	archive := debugarchive.NewArchiveWriter(root, "req-session-test")
	if archive == nil {
		t.Fatal("expected archive writer")
	}
	stream := &EventStream{archive: archive, sessionID: "session-archive-123"}
	stream.recordEvent(
		Event{Event: "response.output_text.delta", Raw: json.RawMessage(`{"type":"response.output_text.delta"}`)},
		Event{Event: "response.output_text.delta", Data: map[string]any{"delta": "hello"}},
	)
	if err := archive.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(root, "req-session-test", "raw.ndjson"))
	if err != nil {
		t.Fatalf("read raw archive: %v", err)
	}
	if !containsSessionID(string(raw), "session-archive-123") {
		t.Fatalf("expected raw archive session ID, got %s", raw)
	}

	canonical, err := os.ReadFile(filepath.Join(root, "req-session-test", "canonical.ndjson"))
	if err != nil {
		t.Fatalf("read canonical archive: %v", err)
	}
	if !containsSessionID(string(canonical), "session-archive-123") {
		t.Fatalf("expected canonical archive session ID, got %s", canonical)
	}
}

func containsSessionID(contents, sessionID string) bool {
	return len(contents) > 0 && sessionID != "" && strings.Contains(contents, `"session_id":"`+sessionID+`"`)
}
