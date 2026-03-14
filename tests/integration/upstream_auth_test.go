package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"openai-compat-proxy/internal/upstream"
)

func TestUpstreamClientSendsAuthorizationHeader(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer server-key" {
			t.Fatalf("unexpected authorization header: %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {}\n\n"))
	}))
	defer stub.Close()

	client := upstream.NewClient(stub.URL)
	_, err := client.Stream(context.Background(), sampleCanonicalRequest(), "Bearer server-key")
	if err != nil {
		t.Fatal(err)
	}
}
