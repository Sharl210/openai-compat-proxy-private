package upstream

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
)

func TestNewTransportAppliesConnectTimeoutToDialContext(t *testing.T) {
	transport := newTransportWithDialer(config.Config{ConnectTimeout: 50 * time.Millisecond}, func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})

	started := time.Now()
	_, err := transport.DialContext(context.Background(), "tcp", "example.test:443")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("expected connect timeout quickly, got %v", elapsed)
	}
}

func TestModelsHonorsFirstByteTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{FirstByteTimeout: 50 * time.Millisecond})
	ctx := context.Background()
	_, _, _, err := client.Models(ctx, "")
	if err == nil {
		t.Fatalf("expected first byte timeout error")
	}
}

func TestStreamHonorsIdleTimeoutBetweenChunks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte("data: {\"delta\":\"hello\"}\n\n"))
		flusher.Flush()
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte("data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{IdleTimeout: 50 * time.Millisecond})
	_, err := client.Stream(context.Background(), model.CanonicalRequest{Model: "gpt-5"}, "")
	if err == nil {
		t.Fatalf("expected idle timeout error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "deadline") && !strings.Contains(strings.ToLower(err.Error()), "timeout") {
		t.Fatalf("expected timeout-shaped error, got %v", err)
	}
}
