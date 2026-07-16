package upstream

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
)

func TestTransportPoolReusesProviderGeneration(t *testing.T) {
	pool := NewTransportPool()
	defer pool.Close()

	cfg := config.Config{
		ConnectTimeout:    time.Second,
		FirstByteTimeout:  2 * time.Second,
		StreamOpenTimeout: 3 * time.Second,
		IdleTimeout:       time.Minute,
	}
	first := pool.Get("provider-a", "https://upstream.example", cfg)
	second := pool.Get("provider-a", "https://upstream.example", cfg)

	if first != second {
		t.Fatal("same provider configuration should reuse one transport generation")
	}
	if first.Regular == first.StreamOpen {
		t.Fatal("regular and stream-open requests must use separate transports")
	}
}

func TestTransportPoolRetiresPreviousGenerationOnConfigChange(t *testing.T) {
	pool := NewTransportPool()
	defer pool.Close()

	first := pool.Get("provider-a", "https://upstream.example", config.Config{IdleTimeout: time.Minute})
	second := pool.Get("provider-a", "https://upstream.example", config.Config{IdleTimeout: 2 * time.Minute})

	if first == second {
		t.Fatal("changed provider configuration should create a new transport generation")
	}
	if !first.Regular.retired.Load() || !first.StreamOpen.retired.Load() {
		t.Fatal("previous transport generation should be retired")
	}
	if second.Regular.retired.Load() || second.StreamOpen.retired.Load() {
		t.Fatal("current transport generation must remain active")
	}
}

func TestTransportPoolReusesIdleHTTPConnection(t *testing.T) {
	var newConnections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok")
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConnections.Add(1)
		}
	}
	server.Start()
	defer server.Close()

	pool := NewTransportPool()
	defer pool.Close()
	transports := pool.Get("provider-a", server.URL, config.Config{IdleTimeout: time.Minute})
	client := &http.Client{Transport: transports.Regular}
	for range 2 {
		response, err := client.Get(server.URL)
		if err != nil {
			t.Fatalf("GET %s: %v", server.URL, err)
		}
		if _, err := io.Copy(io.Discard, response.Body); err != nil {
			t.Fatalf("read response body: %v", err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatalf("close response body: %v", err)
		}
	}

	if got := newConnections.Load(); got != 1 {
		t.Fatalf("expected one reused HTTP connection, got %d", got)
	}
}
