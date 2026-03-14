package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"openai-compat-proxy/internal/upstream"
)

func TestUpstreamClientRetriesTransient5xx(t *testing.T) {
	var calls int32
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("no clients available"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.completed\ndata: {}\n\n")
	}))
	defer stub.Close()

	client := upstream.NewClient(stub.URL)
	_, err := client.Stream(context.Background(), sampleCanonicalRequest(), "Bearer server-key")
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("expected 3 calls, got %d", atomic.LoadInt32(&calls))
	}
}
