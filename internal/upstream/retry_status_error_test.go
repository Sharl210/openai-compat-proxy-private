package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
)

const genericTemporaryUnavailableBody = `{"error":{"message":"Upstream service temporarily unavailable","type":"upstream_error"}}`

func TestClientResponseCapturesIdenticalHTTPRetryEvidenceWhenRetriesExhaust(t *testing.T) {
	// Given
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(genericTemporaryUnavailableBody))
	}))
	defer server.Close()
	client := NewClient(server.URL, config.Config{UpstreamRetryCount: 1, UpstreamRetryDelay: time.Millisecond})

	// When
	_, err := client.Response(context.Background(), model.CanonicalRequest{Model: "gpt-5"}, "")

	// Then
	var httpErr *HTTPStatusError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected HTTP status error, got %T %v", err, err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("expected two upstream attempts, got %d", got)
	}
	if got := httpErr.RetriesPerformed; got != 1 {
		t.Fatalf("expected one retry, got %d", got)
	}
	if got := httpErr.RetryEvidence.AttemptCount; got != 2 {
		t.Fatalf("expected two retry evidence attempts, got %d", got)
	}
	if !httpErr.RetryEvidence.AllAttemptsMatchFinal {
		t.Fatalf("expected identical retries to match final status error, got %#v", httpErr.RetryEvidence)
	}
}

func TestClientResponseMarksMixedHTTPRetryEvidenceWhenRetriesExhaust(t *testing.T) {
	// Given
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream capacity unavailable","type":"upstream_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(genericTemporaryUnavailableBody))
	}))
	defer server.Close()
	client := NewClient(server.URL, config.Config{UpstreamRetryCount: 1, UpstreamRetryDelay: time.Millisecond})

	// When
	_, err := client.Response(context.Background(), model.CanonicalRequest{Model: "gpt-5"}, "")

	// Then
	var httpErr *HTTPStatusError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected HTTP status error, got %T %v", err, err)
	}
	if got := httpErr.RetryEvidence.AttemptCount; got != 2 {
		t.Fatalf("expected two retry evidence attempts, got %d", got)
	}
	if httpErr.RetryEvidence.AllAttemptsMatchFinal {
		t.Fatalf("expected mixed retries not to match final status error, got %#v", httpErr.RetryEvidence)
	}
}
