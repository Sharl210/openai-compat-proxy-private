package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"openai-compat-proxy/internal/upstream"
)

func TestUpstreamClientReturnsHTTPErrorBody(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("no clients available"))
	}))
	defer stub.Close()

	client := upstream.NewClient(stub.URL)
	_, err := client.Stream(context.Background(), sampleCanonicalRequest(), "")
	if err == nil {
		t.Fatal("expected upstream error")
	}
	if err.Error() != "upstream status 500: no clients available" {
		t.Fatalf("unexpected error: %v", err)
	}
}
